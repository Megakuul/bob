package sweep

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/megakuul/bob/internal/store/s3client"
)

// StagingController is the name of the staging sweep (User-Agent bob-sweep/staging-sweep).
const StagingController = "staging-sweep"

// StagingPrefix is where workers upload job outputs (§05.2.4).
const StagingPrefix = "staging/"

// StagingMaxAge is how long abandoned staging survives (R-STORE-105).
const StagingMaxAge = 7 * 24 * time.Hour

// Staging is the artifacts-bucket side of the staging sweep.
type Staging struct {
	s3     *s3.Client
	bucket string
}

// NewStaging returns the sweep of the artifacts bucket at endpoint (credentials from the AWS SDK
// default chain).
func NewStaging(ctx context.Context, endpoint, region, bucket string) (*Staging, error) {
	c, err := s3client.New(ctx, s3client.Options{Endpoint: endpoint, Region: region, Sweep: StagingController})
	if err != nil {
		return nil, fmt.Errorf("sweep: %w", err)
	}
	return &Staging{s3: c, bucket: bucket}, nil
}

// EnsureLifecycle installs the lifecycle rule "staging/ expires after 7 days" (R-STORE-105).
// applied is false when the backend does not implement lifecycle configuration; the hourly
// Sweep then does the cleanup alone.
func (s *Staging) EnsureLifecycle(ctx context.Context) (applied bool, err error) {
	_, err = s.s3.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(s.bucket),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{{
			ID:         aws.String("bob-staging-7d"),
			Status:     types.ExpirationStatusEnabled,
			Filter:     &types.LifecycleRuleFilter{Prefix: aws.String(StagingPrefix)},
			Expiration: &types.LifecycleExpiration{Days: aws.Int32(int32(StagingMaxAge / (24 * time.Hour)))},
		}}},
	})
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if st := s3client.Status(err); st == 400 || st == 405 || st == 501 {
		return false, nil
	}
	return false, fmt.Errorf("sweep: lifecycle rule on %s: %w", s.bucket, err)
}

// Sweep lists staging/ and deletes every object last modified before now − maxAge. It returns
// the number of deleted objects. A failed delete is reported after the pass; the next pass
// retries it.
func (s *Staging) Sweep(ctx context.Context, now time.Time, maxAge time.Duration) (deleted int, err error) {
	cutoff := now.Add(-maxAge)
	p := s3.NewListObjectsV2Paginator(s.s3, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(StagingPrefix)})
	var errs []error
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return deleted, fmt.Errorf("sweep: list %s/%s: %w", s.bucket, StagingPrefix, err)
		}
		for _, o := range page.Contents {
			if o.Key == nil || o.LastModified == nil || !o.LastModified.Before(cutoff) {
				continue
			}
			if _, err := s.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: o.Key}); err != nil {
				if ctx.Err() != nil {
					return deleted, ctx.Err()
				}
				errs = append(errs, fmt.Errorf("delete %s: %w", *o.Key, err))
				continue
			}
			deleted++
		}
	}
	if len(errs) > 0 {
		return deleted, fmt.Errorf("sweep: %s: %w", s.bucket, errors.Join(errs...))
	}
	return deleted, nil
}
