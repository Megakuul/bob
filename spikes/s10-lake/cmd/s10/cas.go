package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"bobspike/s10/logstore"
)

func init() { experiments["cas"] = casTest }

// casTest checks whether If-Match conditional PUTs are atomic: G goroutines each do
// read-modify-write increments of one counter object; every successful PUT must be
// reflected in the final value.
func casTest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cas", flag.ExitOnError)
	ep := endpointFlag(fs)
	g := fs.Int("g", 16, "goroutines")
	n := fs.Int("n", 50, "successful increments per goroutine")
	fs.Parse(args)
	cl := logstore.Client(endpoint(*ep), nil)
	const bucket = "s10-cas"
	if err := logstore.FreshBucket(ctx, cl, bucket); err != nil {
		return err
	}
	key := aws.String("counter")
	if _, err := cl.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: key, Body: bytes.NewReader([]byte("0"))}); err != nil {
		return err
	}
	var ok, conflicts, other atomic.Int64
	var wg sync.WaitGroup
	for range *g {
		wg.Add(1)
		go func() {
			defer wg.Done()
			done := 0
			for done < *n {
				o, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: key})
				if err != nil {
					other.Add(1)
					continue
				}
				b, _ := io.ReadAll(o.Body)
				o.Body.Close()
				v, _ := strconv.Atoi(string(b))
				_, err = cl.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: key, IfMatch: o.ETag, Body: bytes.NewReader([]byte(strconv.Itoa(v + 1)))})
				if err != nil {
					var ae smithy.APIError
					if errors.As(err, &ae) && ae.ErrorCode() == "PreconditionFailed" {
						conflicts.Add(1)
					} else {
						other.Add(1)
					}
					continue
				}
				ok.Add(1)
				done++
			}
		}()
	}
	wg.Wait()
	o, err := cl.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: key})
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(o.Body)
	final, _ := strconv.Atoi(string(b))
	fmt.Printf("cas %s: successful PUTs=%d final value=%d lost updates=%d conflicts=%d other errors=%d\n", *ep, ok.Load(), final, ok.Load()-int64(final), conflicts.Load(), other.Load())
	return nil
}
