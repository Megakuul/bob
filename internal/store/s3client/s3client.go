// Package s3client builds the AWS S3 clients of the store and its sweeps: path-style
// addressing, no SDK retries (the store decides what a failed write means, R-STORE-103
// rule 7) and plain payload checksums so that signed requests stay simple.
package s3client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmw "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Options select the endpoint and identity of a client.
type Options struct {
	Endpoint string // e.g. "http://127.0.0.1:9000"
	Region   string // "" = SDK default chain (AWS_REGION, shared config)
	// AccessKey and SecretKey set a static credential; empty = SDK default chain (env, shared
	// file, IMDSv2, web identity). bob never mints credentials (R-SEC-100).
	AccessKey, SecretKey string
	// UserAgent is appended to the SDK user agent, e.g. "bob-sweep/staging-sweep" (R-STORE-101).
	UserAgent string
}

// New returns a client for o.
func New(ctx context.Context, o Options) (*s3.Client, error) {
	if o.Endpoint == "" {
		return nil, fmt.Errorf("s3client: no endpoint configured (storage.endpoint)")
	}
	opts := []func(*config.LoadOptions) error{
		config.WithHTTPClient(&http.Client{Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
			IdleConnTimeout:     90 * time.Second,
		}}),
	}
	if o.Region != "" {
		opts = append(opts, config.WithRegion(o.Region))
	}
	if o.AccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(o.AccessKey, o.SecretKey, "")))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3client: load AWS configuration: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return s3.NewFromConfig(cfg, func(so *s3.Options) {
		so.BaseEndpoint = aws.String(o.Endpoint)
		so.UsePathStyle = true
		so.Retryer = aws.NopRetryer{}
		so.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		so.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		if o.UserAgent != "" {
			so.APIOptions = append(so.APIOptions, awsmw.AddUserAgentKey(o.UserAgent))
		}
	}), nil
}

// Status is the HTTP status of a failed S3 call, 0 when no response arrived.
func Status(err error) int {
	var re interface{ HTTPStatusCode() int }
	if err != nil && errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}
