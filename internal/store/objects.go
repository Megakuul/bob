package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/megakuul/bob/internal/store/s3client"
)

// Errors of the artifacts bucket (plain objects, not records: ErrObjectNotFound is distinct from
// ErrNotFound). Callers act on them.
var (
	ErrObjectNotFound = errors.New("not found")
	ErrPrecondition   = errors.New("precondition failed")
	ErrRange          = errors.New("range not satisfiable")
	ErrTooLarge       = errors.New("object too large")
)

// Objects is the artifacts bucket (plain S3 objects, not dynamitedb records; SPEC §05.2.4).
// Requests are never retried by the SDK; reads retry transient failures themselves, writes
// resolve a lost response by reading back (the store's rule, R-STORE-103 rule 7).
type Objects struct {
	s3     *s3.Client
	bucket string
}

// OpenObjects returns bucket behind the S3 endpoint of opts (credentials: opts, else the AWS
// SDK chain).
func OpenObjects(ctx context.Context, opts s3client.Options, bucket string) (*Objects, error) {
	c, err := s3client.New(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("store: artifacts bucket: %w", err)
	}
	return &Objects{s3: c, bucket: bucket}, nil
}

// Bucket is the bucket name.
func (o *Objects) Bucket() string { return o.bucket }

// Object is an open download.
type Object struct {
	Body          io.ReadCloser
	Length        int64  // bytes in Body
	Total         int64  // size of the whole object
	ContentRange  string // "bytes a-b/total" for a ranged read, else ""
	ETag          string // as the backend sent it (quoted)
	LastModified  time.Time
	Meta          map[string]string
	ContentType   string
	PartialAnswer bool // the backend answered 206
}

// Info is the metadata of an object (HEAD).
type Info struct {
	Size         int64
	ETag         string
	LastModified time.Time
	Meta         map[string]string
}

func classifyObject(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch s3client.Status(err) {
	case 404:
		return fmt.Errorf("%w: %w", ErrObjectNotFound, err)
	case 409, 412:
		return fmt.Errorf("%w: %w", ErrPrecondition, err)
	case 416:
		return fmt.Errorf("%w: %w", ErrRange, err)
	}
	var nk *types.NoSuchKey
	if errors.As(err, &nk) {
		return fmt.Errorf("%w: %w", ErrObjectNotFound, err)
	}
	return err
}

// transientObject reports whether the outcome of a request is unknown (no response or 5xx).
func transientObject(err error) bool {
	st := s3client.Status(err)
	return st == 0 || st >= 500
}

func pauseObject(ctx context.Context, attempt int) error {
	t := time.NewTimer(time.Duration(attempt+1) * 50 * time.Millisecond)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

const objectAttempts = 3

// Get opens key for reading. rng is "" or one HTTP byte range ("bytes=a-b", "bytes=a-"),
// forwarded to S3 as is. ifMatch, when set, makes a changed object ErrPrecondition.
func (o *Objects) Get(ctx context.Context, key, rng, ifMatch string) (*Object, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key)}
	if rng != "" {
		in.Range = aws.String(rng)
	}
	if ifMatch != "" {
		in.IfMatch = aws.String(ifMatch)
	}
	var out *s3.GetObjectOutput
	var err error
	for a := 0; ; a++ {
		out, err = o.s3.GetObject(ctx, in)
		if err == nil || ctx.Err() != nil || !transientObject(err) || a == objectAttempts-1 {
			break
		}
		if perr := pauseObject(ctx, a); perr != nil {
			return nil, perr
		}
	}
	if err != nil {
		return nil, classifyObject(ctx, err)
	}
	obj := &Object{Body: out.Body, Length: aws.ToInt64(out.ContentLength), ETag: aws.ToString(out.ETag),
		LastModified: aws.ToTime(out.LastModified), Meta: out.Metadata, ContentType: aws.ToString(out.ContentType)}
	obj.Total = obj.Length
	if cr := aws.ToString(out.ContentRange); cr != "" {
		obj.ContentRange = cr
		obj.PartialAnswer = true
		if _, tot, ok := strings.Cut(cr, "/"); ok {
			var n int64
			if _, err := fmt.Sscan(tot, &n); err == nil {
				obj.Total = n
			}
		}
	}
	return obj, nil
}

// Read reads the whole object key, at most max bytes (ErrTooLarge beyond).
func (o *Objects) Read(ctx context.Context, key string, max int64) (body []byte, etag string, err error) {
	obj, err := o.Get(ctx, key, "", "")
	if err != nil {
		return nil, "", err
	}
	defer obj.Body.Close()
	if obj.Length > max {
		return nil, "", fmt.Errorf("%s: %w (%d > %d bytes)", key, ErrTooLarge, obj.Length, max)
	}
	b, err := io.ReadAll(io.LimitReader(obj.Body, max+1))
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", key, classifyObject(ctx, err))
	}
	if int64(len(b)) > max {
		return nil, "", fmt.Errorf("%s: %w (> %d bytes)", key, ErrTooLarge, max)
	}
	return b, obj.ETag, nil
}

// Head returns the metadata of key.
func (o *Objects) Head(ctx context.Context, key string) (*Info, error) {
	var out *s3.HeadObjectOutput
	var err error
	for a := 0; ; a++ {
		out, err = o.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key)})
		if err == nil || ctx.Err() != nil || !transientObject(err) || a == objectAttempts-1 {
			break
		}
		if perr := pauseObject(ctx, a); perr != nil {
			return nil, perr
		}
	}
	if err != nil {
		return nil, classifyObject(ctx, err)
	}
	return &Info{Size: aws.ToInt64(out.ContentLength), ETag: aws.ToString(out.ETag),
		LastModified: aws.ToTime(out.LastModified), Meta: out.Metadata}, nil
}

// Cond is the precondition of a write.
type Cond struct {
	IfNoneMatch bool   // create only
	IfMatch     string // replace only this version (an ETag)
}

// Put writes body to key under cond. A precondition that does not hold is ErrPrecondition.
// A lost response is resolved by reading the object back: our bytes → success.
func (o *Objects) Put(ctx context.Context, key string, body []byte, contentType string, c Cond) (string, error) {
	for a := 0; ; a++ {
		in := &s3.PutObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key), Body: bytes.NewReader(body),
			ContentLength: aws.Int64(int64(len(body)))}
		if contentType != "" {
			in.ContentType = aws.String(contentType)
		}
		if c.IfNoneMatch {
			in.IfNoneMatch = aws.String("*")
		}
		if c.IfMatch != "" {
			in.IfMatch = aws.String(c.IfMatch)
		}
		out, err := o.s3.PutObject(ctx, in)
		if err == nil {
			return aws.ToString(out.ETag), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if !transientObject(err) {
			return "", classifyObject(ctx, err)
		}
		// Unknown outcome: did our write land?
		cur, etag, rerr := o.Read(ctx, key, int64(len(body))+1)
		switch {
		case rerr == nil && bytes.Equal(cur, body):
			return etag, nil
		case rerr == nil && c.IfNoneMatch, rerr == nil && c.IfMatch != "" && etag != c.IfMatch:
			return "", fmt.Errorf("put %s: %w", key, ErrPrecondition)
		case errors.Is(rerr, ErrObjectNotFound) && c.IfMatch != "":
			return "", fmt.Errorf("put %s: %w", key, ErrPrecondition)
		}
		if a == objectAttempts-1 {
			return "", fmt.Errorf("put %s: %w", key, err)
		}
		if perr := pauseObject(ctx, a); perr != nil {
			return "", perr
		}
	}
}

// PutFile writes the contents of r (size bytes, seekable for signing) to key unconditionally.
func (o *Objects) PutFile(ctx context.Context, key string, r io.ReadSeeker, size int64, contentType string) error {
	for a := 0; ; a++ {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err := o.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key), Body: r,
			ContentLength: aws.Int64(size), ContentType: aws.String(contentType)})
		if err == nil || ctx.Err() != nil || !transientObject(err) || a == objectAttempts-1 {
			return classifyObject(ctx, err)
		}
		if perr := pauseObject(ctx, a); perr != nil {
			return perr
		}
	}
}

// CreateFile writes the contents of r (size bytes, seekable for signing) to key only when key
// does not exist (If-None-Match: *); an existing key is ErrPrecondition. A lost response is
// resolved by HEAD: an object of our size is taken as ours (key is content-addressed).
func (o *Objects) CreateFile(ctx context.Context, key string, r io.ReadSeeker, size int64, contentType string) error {
	for a := 0; ; a++ {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return err
		}
		_, err := o.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key), Body: r,
			ContentLength: aws.Int64(size), ContentType: aws.String(contentType), IfNoneMatch: aws.String("*")})
		if err == nil || ctx.Err() != nil || !transientObject(err) {
			if err = classifyObject(ctx, err); err != nil {
				return fmt.Errorf("put %s: %w", key, err)
			}
			return nil
		}
		if inf, herr := o.Head(ctx, key); herr == nil && inf.Size == size {
			return nil
		}
		if a == objectAttempts-1 {
			return fmt.Errorf("put %s: %w", key, err)
		}
		if perr := pauseObject(ctx, a); perr != nil {
			return perr
		}
	}
}

// Copy copies src (only while its ETag is srcETag) to dst, replacing dst's metadata with
// meta. dstETag "" creates dst only when absent; otherwise only that version of dst is
// replaced. A precondition that does not hold (dst, or a changed src) is ErrPrecondition.
func (o *Objects) Copy(ctx context.Context, src, srcETag, dst string, meta map[string]string, dstETag string) error {
	in := &s3.CopyObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(dst),
		CopySource: aws.String(o.bucket + "/" + src), MetadataDirective: types.MetadataDirectiveReplace, Metadata: meta}
	if srcETag != "" {
		in.CopySourceIfMatch = aws.String(srcETag)
	}
	if dstETag == "" {
		in.IfNoneMatch = aws.String("*")
	} else {
		in.IfMatch = aws.String(dstETag)
	}
	for a := 0; ; a++ {
		_, err := o.s3.CopyObject(ctx, in)
		if err == nil || ctx.Err() != nil || !transientObject(err) || a == objectAttempts-1 {
			return classifyObject(ctx, err)
		}
		// Unknown outcome: a copy that landed shows up with our metadata.
		if inf, herr := o.Head(ctx, dst); herr == nil && sameMeta(inf.Meta, meta) {
			return nil
		}
		if perr := pauseObject(ctx, a); perr != nil {
			return perr
		}
	}
}

func sameMeta(got, want map[string]string) bool {
	for k, v := range want {
		if got[strings.ToLower(k)] != v && got[k] != v {
			return false
		}
	}
	return true
}

// Delete removes key (a missing key is not an error).
func (o *Objects) Delete(ctx context.Context, key string) error {
	for a := 0; ; a++ {
		_, err := o.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key)})
		if err == nil || ctx.Err() != nil || !transientObject(err) || a == objectAttempts-1 {
			err = classifyObject(ctx, err)
			if errors.Is(err, ErrObjectNotFound) {
				return nil
			}
			return err
		}
		if perr := pauseObject(ctx, a); perr != nil {
			return perr
		}
	}
}
