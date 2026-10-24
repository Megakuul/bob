// Command s3probe checks what a credential may do in the artifacts bucket (R-SEC-002 evidence)
// and which S3 features the backend supports (conditional writes, copy-source-if-match, ranges).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"bobspike/s12/store"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func main() {
	ctx := context.Background()
	ep := "http://127.0.0.1:21203"
	w := store.New(ep, "workerkey", "workersecret0123456789", "artifacts")
	o := store.New(ep, "orchkey", "orchsecret0123456789", "artifacts")
	res := func(name string, err error) {
		s := "OK"
		if err != nil {
			s = "DENIED/ERR: " + err.Error()
			if len(s) > 140 {
				s = s[:140]
			}
		}
		fmt.Printf("%-55s %s\n", name, s)
	}
	fmt.Println("== worker credential")
	res("worker PUT staging/probe/x", w.PutSmall(ctx, "staging/probe/x", []byte("x"), "", false))
	res("worker GET staging/probe/x", func() error { _, err := w.GetSmall(ctx, "staging/probe/x", 10); return err }())
	res("worker PUT nix/narinfo/probe.narinfo", w.PutSmall(ctx, "nix/narinfo/probe.narinfo", []byte("x"), "", false))
	res("worker PUT oci/blobs/sha256/probe", w.PutSmall(ctx, "oci/blobs/sha256/probe", []byte("x"), "", false))
	res("worker GET nix/... (existing object)", func() error {
		o.PutSmall(ctx, "nix/probe-read", []byte("x"), "", false)
		_, err := w.GetSmall(ctx, "nix/probe-read", 10)
		return err
	}())
	res("worker DELETE nix/probe-read", w.Delete(ctx, "nix/probe-read"))
	res("worker LIST nix/", func() error { _, err := w.List(ctx, "nix/", 10); return err }())
	res("worker COPY staging->nix/probe-copy", w.Copy(ctx, "staging/probe/x", "nix/probe-copy", "", ""))
	fmt.Println("== backend features (orchestrator credential)")
	res("PUT If-None-Match:* on new key", o.PutSmall(ctx, "probe/inm", []byte("1"), "", true))
	err := o.PutSmall(ctx, "probe/inm", []byte("2"), "", true)
	fmt.Printf("%-55s %v (want precondition error) isPrecondition=%v\n", "PUT If-None-Match:* on existing key", err != nil, errors.Is(err, store.ErrPrecondition))
	b, _ := o.GetSmall(ctx, "probe/inm", 10)
	fmt.Printf("%-55s %q (want \"1\")\n", "content after second conditional PUT", b)
	ho, _ := o.Head(ctx, "staging/probe/x")
	res("COPY with CopySourceIfMatch=correct ETag", o.Copy(ctx, "staging/probe/x", "probe/copy-ok", *ho.ETag, ""))
	err = o.Copy(ctx, "staging/probe/x", "probe/copy-bad", `"0000000000000000000000000000000f"`, "")
	fmt.Printf("%-55s refused=%v err=%v\n", "COPY with CopySourceIfMatch=wrong ETag", err != nil, err)
	ok, _ := o.Exists(ctx, "probe/copy-bad")
	fmt.Printf("%-55s %v (want false)\n", "copy-bad object exists", ok)
	o.PutSmall(ctx, "probe/range", []byte("0123456789"), "", false)
	g, err := o.Get(ctx, "probe/range", "bytes=3-5")
	if err == nil {
		rb, _ := io.ReadAll(g.Body)
		fmt.Printf("%-55s %q content-range=%s\n", "GET Range bytes=3-5", rb, aws.ToString(g.ContentRange))
	}
	_, err = o.Get(ctx, "probe/range", "bytes=100-")
	fmt.Printf("%-55s %v\n", "GET Range bytes=100- (beyond end)", err)
	// lifecycle (expiry of staging/) support
	_, err = o.C.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{Bucket: aws.String("artifacts"),
		LifecycleConfiguration: &typesLC})
	res("PutBucketLifecycleConfiguration staging/ expire 7d", err)
	_ = os.Stdout
}
