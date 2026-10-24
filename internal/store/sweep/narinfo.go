package sweep

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/megakuul/bob/internal/store/s3client"
)

// KeyRetireController names the signing-key retirement sweep (User-Agent
// bob-sweep/key-retire): the maintenance LIST over nix/narinfo/ of R-REG-153 (never a hot path).
const KeyRetireController = "key-retire"

// NarInfoPrefix is where the served narinfos live (§05.2.4).
const NarInfoPrefix = "nix/narinfo/"

// NarInfos lists the served narinfos of the artifacts bucket.
type NarInfos struct {
	s3     *s3.Client
	bucket string
}

// NewNarInfos returns the narinfo lister of the artifacts bucket at endpoint (credentials from
// the AWS SDK default chain).
func NewNarInfos(ctx context.Context, endpoint, region, bucket string) (*NarInfos, error) {
	c, err := s3client.New(ctx, s3client.Options{Endpoint: endpoint, Region: region, Sweep: KeyRetireController})
	if err != nil {
		return nil, fmt.Errorf("sweep: %w", err)
	}
	return &NarInfos{s3: c, bucket: bucket}, nil
}

// Each calls fn with the key of every object below nix/narinfo/, page by page; it stops at
// the first error of fn.
func (n *NarInfos) Each(ctx context.Context, fn func(key string) error) error {
	p := s3.NewListObjectsV2Paginator(n.s3, &s3.ListObjectsV2Input{Bucket: aws.String(n.bucket), Prefix: aws.String(NarInfoPrefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("sweep: list %s/%s: %w", n.bucket, NarInfoPrefix, err)
		}
		for _, o := range page.Contents {
			if o.Key == nil {
				continue
			}
			if err := fn(*o.Key); err != nil {
				return err
			}
		}
	}
	return nil
}
