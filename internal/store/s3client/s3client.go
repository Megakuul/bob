// Package s3client builds the AWS S3 clients of the store and its sweeps: path-style
// addressing, SDK retries only where a caller asks for them (the store's dynamitedb buckets,
// which resolve a retried write themselves, R-STORE-103 rule 7) and plain payload checksums so
// that signed requests stay simple.
package s3client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmw "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
)

// Options select the endpoint and identity of a client.
type Options struct {
	Endpoint string // e.g. "http://127.0.0.1:9000"
	Region   string // "" = SDK default chain (AWS_REGION, shared config)
	// AccessKey and SecretKey set a static credential; empty = SDK default chain (env, shared
	// file, IMDSv2, web identity). bob never mints credentials (R-SEC-100).
	AccessKey, SecretKey string
	// Sweep names a sweep controller; its requests carry "bob-sweep/<Sweep>" in the User-Agent,
	// which is how the LIST accounting attributes them (R-STORE-101).
	Sweep string
	// Retries is the SDK's attempt count per request (standard retryer, no client-side rate
	// limit); 0 or 1 = no retries.
	Retries int
	// APIOptions are added to the client's middleware stack.
	APIOptions []func(*middleware.Stack) error
}

// New returns a client for o.
func New(ctx context.Context, o Options) (*s3.Client, error) {
	if o.Endpoint == "" {
		return nil, errors.New("s3client: no endpoint configured (storage.endpoint)")
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
		if o.Retries > 1 {
			so.Retryer = retry.NewStandard(func(r *retry.StandardOptions) {
				r.MaxAttempts = o.Retries
				r.RateLimiter = ratelimit.None
			})
		}
		so.APIOptions = append(so.APIOptions, o.APIOptions...)
		so.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		so.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		if o.Sweep != "" {
			so.APIOptions = append(so.APIOptions, awsmw.AddUserAgentKeyValue("bob-sweep", o.Sweep))
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
