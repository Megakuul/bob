package sweep

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/megakuul/bob/internal/store/s3client"
)

// FsckController is the name of `bob-server store fsck` (User-Agent bob-sweep/fsck). It is an
// operator command, never a controller: the only LIST of the control and fleet buckets.
const FsckController = "fsck"

// NewFsckClient returns the client of `bob-server store fsck` (credentials from the AWS SDK
// default chain).
func NewFsckClient(ctx context.Context, endpoint, region string) (*s3.Client, error) {
	c, err := s3client.New(ctx, s3client.Options{Endpoint: endpoint, Region: region, Sweep: FsckController})
	if err != nil {
		return nil, fmt.Errorf("sweep: %w", err)
	}
	return c, nil
}

// Keys lists every key of bucket.
func Keys(ctx context.Context, c *s3.Client, bucket string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return keys, fmt.Errorf("sweep: list %s: %w", bucket, err)
		}
		for _, o := range page.Contents {
			if o.Key != nil {
				keys = append(keys, *o.Key)
			}
		}
	}
	return keys, nil
}
