package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"bobspike/s09/internal/store"
)

// readDuringWrite: 1 writer overwrites a key continuously, 8 readers GET it; count empty/short bodies.
func readDuringWrite(endpoint string, ifMatch bool) {
	ctx := context.Background()
	c := store.Open(endpoint, "s09-rw", "bobadmin", "bobadmin-secret", nil, 1)
	_ = c.EnsureBucket(ctx, false)
	body := bytes.Repeat([]byte("x"), 200)
	po, err := c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("s09-rw"), Key: aws.String("k"), Body: bytes.NewReader(body)})
	if err != nil {
		fmt.Println(err)
		return
	}
	etag := po.ETag
	stop := time.Now().Add(10 * time.Second)
	var writes, reads, empty, short, errs atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; time.Now().Before(stop); i++ {
			b := append([]byte(fmt.Sprintf("%08d", i)), body[8:]...)
			in := &s3.PutObjectInput{Bucket: aws.String("s09-rw"), Key: aws.String("k"), Body: bytes.NewReader(b)}
			if ifMatch {
				in.IfMatch = etag
			}
			out, err := c.S3.PutObject(ctx, in)
			if err == nil {
				etag = out.ETag
				writes.Add(1)
			}
		}
	}()
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				out, err := c.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("s09-rw"), Key: aws.String("k")})
				if err != nil {
					errs.Add(1)
					continue
				}
				b, err := io.ReadAll(out.Body)
				out.Body.Close()
				reads.Add(1)
				switch {
				case err != nil:
					errs.Add(1)
				case len(b) == 0:
					empty.Add(1)
				case len(b) != 200:
					short.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	fmt.Printf("%s ifMatch=%v writes=%d reads=%d emptyBodies=%d wrongLength=%d readErrors=%d\n", endpoint, ifMatch, writes.Load(), reads.Load(), empty.Load(), short.Load(), errs.Load())
}
