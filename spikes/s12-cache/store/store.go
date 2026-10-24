// Package store wraps the artifacts bucket (plain S3) and defines its key layout.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Key layout of the artifacts bucket [proposal]. All keys are derived from validated
// hashes / names only; never from raw client input.
const (
	StagingPrefix = "staging/"
)

func StagingNar(build, h string) string     { return "staging/" + build + "/nar/" + h + ".nar.zst" }
func StagingNarInfo(build, h string) string { return "staging/" + build + "/narinfo/" + h + ".narinfo" }
func StagingBlob(build, hex string) string  { return "staging/" + build + "/oci/blobs/sha256/" + hex }
func StagingImages(build string) string     { return "staging/" + build + "/oci/images.json" }
func StagingComplete(build string) string   { return "staging/" + build + "/COMPLETE.json" }

func NarInfoKey(h string) string        { return "nix/narinfo/" + h + ".narinfo" }
func NarKey(h string) string            { return "nix/nar/" + h + ".nar.zst" }
func LsKey(h string) string             { return "nix/ls/" + h + ".ls" }
func NixVisPrefix(h string) string      { return "nix/vis/" + h + "/" }
func NixVisKey(h, scope string) string  { return "nix/vis/" + h + "/" + scope }
func BlobKey(hex string) string         { return "oci/blobs/sha256/" + hex }
func RepoTagKey(repo, tag string) string { return "oci/repos/" + repo + "/_tags/" + tag }
func RepoTagPrefix(repo string) string  { return "oci/repos/" + repo + "/_tags/" }
func RepoBlobKey(repo, hex string) string {
	return "oci/repos/" + repo + "/_blobs/" + hex
}
func RepoVisPrefix(repo string) string       { return "oci/repos/" + repo + "/_vis/" }
func RepoVisKey(repo, scope string) string   { return "oci/repos/" + repo + "/_vis/" + scope }

// Scope strings for visibility markers.
const ScopePublic = "public"

func ProjectScope(p string) string { return "p." + p }

type Store struct {
	C      *s3.Client
	Bucket string
	up     *manager.Uploader
}

func New(endpoint, access, secret, bucket string) *Store {
	c := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       "us-east-1",
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(access, secret, ""),
		// SeaweedFS and others don't need the new default checksums; keep requests plain.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		HTTPClient: &http.Client{Transport: &http.Transport{
			MaxIdleConnsPerHost: 64, DisableCompression: true,
		}},
	})
	up := manager.NewUploader(c, func(u *manager.Uploader) {
		u.PartSize = 16 << 20
		u.Concurrency = 4
	})
	return &Store{C: c, Bucket: bucket, up: up}
}

var ErrNotFound = errors.New("not found")
var ErrPrecondition = errors.New("precondition failed")

func classify(err error) error {
	if err == nil {
		return nil
	}
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return fmt.Errorf("%w: %v", ErrNotFound, err)
		case "PreconditionFailed", "412":
			return fmt.Errorf("%w: %v", ErrPrecondition, err)
		}
	}
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		switch re.HTTPStatusCode() {
		case 404:
			return fmt.Errorf("%w: %v", ErrNotFound, err)
		case 412:
			return fmt.Errorf("%w: %v", ErrPrecondition, err)
		}
	}
	return err
}

// Get opens an object; rng is an HTTP Range header value or "".
func (s *Store) Get(ctx context.Context, key, rng string) (*s3.GetObjectOutput, error) {
	in := &s3.GetObjectInput{Bucket: &s.Bucket, Key: &key}
	if rng != "" {
		in.Range = &rng
	}
	out, err := s.C.GetObject(ctx, in)
	return out, classify(err)
}

func (s *Store) GetSmall(ctx context.Context, key string, max int64) ([]byte, error) {
	out, err := s.Get(ctx, key, "")
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(io.LimitReader(out.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: object larger than %d bytes", key, max)
	}
	return b, nil
}

func (s *Store) Head(ctx context.Context, key string) (*s3.HeadObjectOutput, error) {
	out, err := s.C.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.Bucket, Key: &key})
	return out, classify(err)
}

func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Head(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// PutSmall writes a small object. ifNoneMatch makes it create-only (conditional write).
func (s *Store) PutSmall(ctx context.Context, key string, body []byte, ctype string, ifNoneMatch bool) error {
	in := &s3.PutObjectInput{Bucket: &s.Bucket, Key: &key, Body: strings.NewReader(string(body)),
		ContentLength: aws.Int64(int64(len(body)))}
	if ctype != "" {
		in.ContentType = &ctype
	}
	if ifNoneMatch {
		in.IfNoneMatch = aws.String("*")
	}
	_, err := s.C.PutObject(ctx, in)
	return classify(err)
}

// Upload streams r of unknown length with multipart upload (bounded memory:
// PartSize*Concurrency).
func (s *Store) Upload(ctx context.Context, key string, r io.Reader, ctype string) error {
	in := &s3.PutObjectInput{Bucket: &s.Bucket, Key: &key, Body: r}
	if ctype != "" {
		in.ContentType = &ctype
	}
	_, err := s.up.Upload(ctx, in)
	return classify(err)
}

// Copy does a server-side copy, guarded by the source ETag that was verified.
func (s *Store) Copy(ctx context.Context, src, dst, srcETag, ctype string) error {
	in := &s3.CopyObjectInput{Bucket: &s.Bucket, Key: &dst, CopySource: aws.String(s.Bucket + "/" + src)}
	if srcETag != "" {
		in.CopySourceIfMatch = &srcETag
	}
	if ctype != "" {
		in.ContentType = &ctype
		in.MetadataDirective = types.MetadataDirectiveReplace
	}
	_, err := s.C.CopyObject(ctx, in)
	return classify(err)
}

func (s *Store) List(ctx context.Context, prefix string, max int32) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(s.C, &s3.ListObjectsV2Input{Bucket: &s.Bucket, Prefix: &prefix, MaxKeys: aws.Int32(max)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, classify(err)
		}
		for _, o := range page.Contents {
			keys = append(keys, *o.Key)
		}
		if int32(len(keys)) >= max {
			break
		}
	}
	return keys, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.C.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.Bucket, Key: &key})
	return classify(err)
}
