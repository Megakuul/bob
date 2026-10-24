package main

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"bobspike/s09/internal/store"
)

// dirMarker: does LIST return the "directory" key after objects are deleted, and what does GET on it return?
func dirMarker(endpoint string) {
	ctx := context.Background()
	c := store.Open(endpoint, "s09-dir", "bobadmin", "bobadmin-secret", nil, 1)
	_ = c.EnsureBucket(ctx, true)
	bk := aws.String("s09-dir")
	put := func(k string) { c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: bk, Key: aws.String(k), Body: bytes.NewReader([]byte("x"))}) }
	del := func(k string) { c.S3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: bk, Key: aws.String(k)}) }
	list := func(stage string) {
		objs, err := c.ListObjects(ctx, "q/p/j/", 0)
		fmt.Printf("  %-34s LIST q/p/j/ -> %d keys %v err=%v\n", stage, len(objs), objs, err)
		g, err := c.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: bk, Key: aws.String("q/p/j/")})
		if err == nil {
			b, _ := io.ReadAll(g.Body)
			fmt.Printf("  %-34s GET q/p/j/  -> 200, %d bytes\n", "", len(b))
		} else {
			fmt.Printf("  %-34s GET q/p/j/  -> %v\n", "", firstN(err.Error(), 90))
		}
	}
	fmt.Println(endpoint)
	put("q/p/j/1")
	put("q/p/j/2")
	list("two objects")
	del("q/p/j/1")
	list("one deleted")
	del("q/p/j/2")
	list("both deleted")
	put("q/p/j/3")
	list("new object after empty")
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// fileDir: can "a/x" (object) and "a/x/s/1" (object) coexist?
func fileDir(endpoint string) {
	ctx := context.Background()
	c := store.Open(endpoint, "s09-dir", "bobadmin", "bobadmin-secret", nil, 1)
	_ = c.EnsureBucket(ctx, true)
	bk := aws.String("s09-dir")
	_, e1 := c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: bk, Key: aws.String("a/x"), Body: bytes.NewReader([]byte("parent"))})
	_, e2 := c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: bk, Key: aws.String("a/x/s/1"), Body: bytes.NewReader([]byte("child"))})
	_, g1 := c.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: bk, Key: aws.String("a/x")})
	_, e3 := c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: bk, Key: aws.String("b/y/s/1"), Body: bytes.NewReader([]byte("child"))})
	_, e4 := c.S3.PutObject(ctx, &s3.PutObjectInput{Bucket: bk, Key: aws.String("b/y"), Body: bytes.NewReader([]byte("parent"))})
	_, g2 := c.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: bk, Key: aws.String("b/y")})
	fmt.Printf("%s parent-then-child: put parent=%v put child=%v get parent=%v\n", endpoint, e1 == nil, errS(e2), errS(g1))
	fmt.Printf("%s child-then-parent: put child=%v put parent=%v get parent=%v\n", endpoint, e3 == nil, errS(e4), errS(g2))
}

func errS(err error) string {
	if err == nil {
		return "ok"
	}
	return firstN(err.Error(), 120)
}
