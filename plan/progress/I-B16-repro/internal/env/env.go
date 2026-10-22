// Package env gives every repro an S3 client of a local RustFS/S3 endpoint and a fresh bucket.
// Endpoint: $REPRO_S3 (default http://127.0.0.1:19777), keys $REPRO_KEY / $REPRO_SECRET.
package env

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func get(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Client returns a path-style S3 client; hc (nil = default) is its HTTP client.
func Client(hc s3.HTTPClient) *s3.Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return s3.New(s3.Options{
		BaseEndpoint: aws.String(get("REPRO_S3", "http://127.0.0.1:19777")),
		Region:       "us-east-1",
		UsePathStyle: true,
		HTTPClient:   hc,
		Credentials: credentials.NewStaticCredentialsProvider(get("REPRO_KEY", "bobharnessroot"),
			get("REPRO_SECRET", "bobharnessroot-secret-0123456789"), ""),
	})
}

// Bucket creates a fresh S3 bucket and returns its name.
func Bucket(ctx context.Context, prefix string) string {
	name := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	if _, err := Client(nil).CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &name}); err != nil {
		Fatal("create bucket: %v", err)
	}
	return name
}

// Fatal prints and exits 2 (setup failure, not the reproduced bug).
func Fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "SETUP FAILED: "+f+"\n", a...)
	os.Exit(2)
}
