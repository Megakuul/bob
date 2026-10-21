package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	d "github.com/megakuul/dynamitedb"
)

// Page is one page of a List.
type Page[T any] struct {
	Items []*T
	// Next is the last key seen; pass it as After to continue. Empty when done.
	Next string
}

// ListKeys lists raw object keys under prefix (one LIST request per 1000 keys, no GETs).
// prefix is the dynamitedb key layout: "<pk>/<pkval>/<sk>/" or "<pk>/".
func (c *Conn) ListKeys(ctx context.Context, prefix, after string, limit int) ([]string, string, error) {
	var keys []string
	in := &s3.ListObjectsV2Input{Bucket: aws.String(c.Name), Prefix: aws.String(prefix), Delimiter: aws.String("/")}
	if after != "" {
		in.StartAfter = aws.String(after)
	}
	if limit > 0 && limit < 1000 {
		in.MaxKeys = aws.Int32(int32(limit))
	}
	for {
		out, err := c.S3.ListObjectsV2(ctx, in)
		if err != nil {
			return nil, "", err
		}
		for _, o := range out.Contents {
			if strings.HasSuffix(*o.Key, "/") {
				continue // SeaweedFS lists an emptied "directory" as an object
			}
			keys = append(keys, *o.Key)
			if limit > 0 && len(keys) >= limit {
				return keys, *o.Key, nil
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated || out.NextContinuationToken == nil {
			return keys, "", nil
		}
		in.ContinuationToken, in.StartAfter = out.NextContinuationToken, nil
	}
}

// List is the spike's replacement for dynamitedb.Query: raw LIST (stops at limit), then one
// dynamitedb.Get per key in goroutines this package owns (panics recovered, at most `par`
// in flight, objects deleted between LIST and GET skipped, filter mismatches skipped).
// mk builds the exact-key filter for one listed key.
func List[T any](ctx context.Context, c *Conn, prefix, after string, limit, par int, mk func(key string) *T) (Page[T], error) {
	keys, next, err := c.ListKeys(ctx, prefix, after, limit)
	if err != nil {
		return Page[T]{}, err
	}
	if par <= 0 {
		par = 16
	}
	items := make([]*T, len(keys))
	errs := make([]error, len(keys))
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("%w: %v", ErrPanic, r)
				}
			}()
			it, err := Get(ctx, c.Bucket, mk(k))
			if errors.Is(err, d.ErrNotFound) || errors.Is(err, d.ErrFilterMismatch) {
				return
			}
			items[i], errs[i] = it, err
		}()
	}
	wg.Wait()
	out := Page[T]{Next: next}
	for i := range keys {
		if errs[i] != nil {
			return Page[T]{}, errs[i]
		}
		if items[i] != nil {
			out.Items = append(out.Items, items[i])
		}
	}
	return out, nil
}

// SortKeyOf returns the last segment of a dynamitedb key "<pk>/<v>/<sk>/<v>".
func SortKeyOf(key string) string { return key[strings.LastIndex(key, "/")+1:] }

// Obj is one listed object.
type Obj struct{ Key, ETag string }

// ListObjects is ListKeys with ETags (S3 LIST returns them), for conditional deletes without a GET.
func (c *Conn) ListObjects(ctx context.Context, prefix string, limit int) ([]Obj, error) {
	in := &s3.ListObjectsV2Input{Bucket: aws.String(c.Name), Prefix: aws.String(prefix), Delimiter: aws.String("/")}
	if limit > 0 && limit < 1000 {
		in.MaxKeys = aws.Int32(int32(limit))
	}
	out, err := c.S3.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, err
	}
	objs := make([]Obj, 0, len(out.Contents))
	for _, o := range out.Contents {
		if strings.HasSuffix(*o.Key, "/") {
			continue // SeaweedFS lists an emptied "directory" as an object
		}
		objs = append(objs, Obj{*o.Key, aws.ToString(o.ETag)})
	}
	return objs, nil
}
