package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/megakuul/bob/internal/store/s3client"
)

// Bucket is one dynamitedb bucket plus a raw S3 client for conditional GETs.
// It is safe for concurrent use.
//
// The store keeps dynamitedb's object format (keys <pk>/<pkval>[/<sk>/<skval>], one CBOR map
// per record, ETag optimistic locking) but issues the requests itself; see doc.go for why.
type Bucket struct {
	name     string
	endpoint string
	role     string
	s3       *s3.Client
}

// BucketConfig selects one bucket. Credentials come from the AWS SDK default chain.
type BucketConfig struct {
	Endpoint string // e.g. "https://s3.eu-central-1.amazonaws.com" or "http://127.0.0.1:9000"
	Region   string // e.g. "us-east-1"
	Name     string // bucket name, e.g. "bob-fleet"
	Role     string // "control" | "fleet" (metrics label only)
}

// Open connects to the bucket, runs the conformance probe (R-STORE-102) once per process,
// and returns a Bucket. It never lists.
//
// A probe the credential may not run (403 on the first probe write, e.g. a worker credential
// on bob-fleet, whose policy allows writes only below its own prefixes) is skipped: the
// backend is then verified through a bucket the credential may write (OpenStorage).
// A failed check returns a *Refusal.
func Open(ctx context.Context, cfg BucketConfig) (*Bucket, error) {
	b, err := newBucket(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := probe(ctx, b); err != nil && !errors.Is(err, errProbeDenied) {
		return nil, err
	}
	return b, nil
}

func newBucket(ctx context.Context, cfg BucketConfig) (*Bucket, error) {
	if _, err := Segment(cfg.Name); err != nil {
		return nil, fmt.Errorf("store: bucket name %q is invalid", cfg.Name)
	}
	c, err := s3client.New(ctx, s3client.Options{Endpoint: cfg.Endpoint, Region: cfg.Region})
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Bucket{name: cfg.Name, endpoint: cfg.Endpoint, role: cfg.Role, s3: c}, nil
}

// Name is the bucket name.
func (b *Bucket) Name() string { return b.name }

// Internal errors of the request layer. Model operations map them to the exported errors.
var (
	errPrecondition = errors.New("precondition failed")
	errNoETag       = fmt.Errorf("%w: the backend returned no ETag header", ErrPanic)
	errTooLarge     = fmt.Errorf("record larger than %d bytes", maxBody)
)

// maxBody caps a record read from the bucket (hostile-input rule, AGENTS.md §5.4). The largest
// record of SPEC §05.4 (BuildRecord with 1,024 jobs) is far below it.
const maxBody = 8 << 20

// object is one stored record: its raw CBOR body and ETag.
type object struct {
	body []byte
	etag string
}

// transient reports whether err left the outcome of a request unknown: no response, or a
// server error. Client errors (4xx) are definite answers.
func transient(err error) bool {
	st := s3client.Status(err)
	return st == 0 || st >= 500
}

// failed returns ctx's error when the context ended (S9 B11: check ctx first), else err.
func failed(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func pause(ctx context.Context, min, max time.Duration) error {
	t := time.NewTimer(min + rand.N(max-min))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// get reads key. With ifNoneMatch set, an unchanged object returns notModified=true and no body.
// Transient failures are retried (a GET is idempotent).
func (b *Bucket) get(ctx context.Context, key, ifNoneMatch string) (o object, notModified bool, err error) {
	for attempt := 0; ; attempt++ {
		o, notModified, err = b.getOnce(ctx, key, ifNoneMatch)
		definite := errors.Is(err, ErrNotFound) || errors.Is(err, errNoETag) || errors.Is(err, errTooLarge)
		if err == nil || ctx.Err() != nil || definite || !transient(err) || attempt == 2 {
			return o, notModified, failed(ctx, err)
		}
		if perr := pause(ctx, 20*time.Millisecond, 100*time.Millisecond); perr != nil {
			return o, false, perr
		}
	}
}

func (b *Bucket) getOnce(ctx context.Context, key, ifNoneMatch string) (object, bool, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)}
	if ifNoneMatch != "" {
		in.IfNoneMatch = aws.String(ifNoneMatch)
	}
	resp, err := b.s3.GetObject(ctx, in)
	if err != nil {
		switch s3client.Status(err) {
		case 304:
			return object{}, true, nil
		case 404:
			return object{}, false, ErrNotFound
		}
		return object{}, false, err
	}
	defer resp.Body.Close()
	if resp.ETag == nil || *resp.ETag == "" {
		return object{}, false, errNoETag
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return object{}, false, err
	}
	if len(body) > maxBody {
		return object{}, false, errTooLarge
	}
	return object{body: body, etag: *resp.ETag}, false, nil
}

// cond is the precondition of a write.
type cond int

const (
	always   cond = iota // unconditional (PutOwned)
	ifAbsent             // If-None-Match: *
	ifMatch              // If-Match: <etag>
)

func (b *Bucket) putOnce(ctx context.Context, key string, body []byte, c cond, etag string) (string, error) {
	in := &s3.PutObjectInput{
		Bucket: aws.String(b.name), Key: aws.String(key),
		Body: bytes.NewReader(body), ContentLength: aws.Int64(int64(len(body))),
		ContentType: aws.String("application/cbor"),
	}
	switch c {
	case ifAbsent:
		in.IfNoneMatch = aws.String("*")
	case ifMatch:
		in.IfMatch = aws.String(etag)
	}
	resp, err := b.s3.PutObject(ctx, in)
	if err != nil {
		// 409 is AWS's answer to a conditional write racing another one on the same key.
		if st := s3client.Status(err); st == 412 || st == 409 {
			return "", errPrecondition
		}
		return "", err
	}
	if resp.ETag == nil {
		return "", errNoETag
	}
	return *resp.ETag, nil
}

// put writes body at key under c and returns the new ETag. When the outcome of a write is
// unknown (no response, 5xx), put re-reads the key: if it holds exactly body, the write is
// ours and succeeded (R-STORE-103 rule 7; records that several parties write carry a write
// id, so equal bytes mean the same writer); if the precondition still holds, the write did not
// land and is sent again; otherwise somebody else wrote first (errPrecondition).
func (b *Bucket) put(ctx context.Context, key string, body []byte, c cond, etag string) (string, error) {
	for attempt := 0; ; attempt++ {
		tag, err := b.putOnce(ctx, key, body, c, etag)
		if err == nil || errors.Is(err, errPrecondition) || ctx.Err() != nil || !transient(err) {
			return tag, failed(ctx, err)
		}
		if attempt == 3 {
			return "", fmt.Errorf("write outcome unknown after %d attempts: %w", attempt+1, err)
		}
		if perr := pause(ctx, 20*time.Millisecond, 100*time.Millisecond); perr != nil {
			return "", perr
		}
		cur, _, gerr := b.get(ctx, key, "")
		switch {
		case gerr == nil && bytes.Equal(cur.body, body):
			return cur.etag, nil
		case c == always:
		case c == ifAbsent && errors.Is(gerr, ErrNotFound):
		case c == ifMatch && gerr == nil && cur.etag == etag:
		case gerr == nil || errors.Is(gerr, ErrNotFound):
			return "", errPrecondition
		default:
			return "", gerr
		}
	}
}

// del deletes key; with etag set only while the stored ETag matches. A lost response is
// resolved like in put: gone means done.
func (b *Bucket) del(ctx context.Context, key, etag string) error {
	for attempt := 0; ; attempt++ {
		in := &s3.DeleteObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)}
		if etag != "" {
			in.IfMatch = aws.String(etag)
		}
		_, err := b.s3.DeleteObject(ctx, in)
		if err == nil {
			return nil
		}
		switch st := s3client.Status(err); {
		case st == 412 || st == 409:
			return errPrecondition
		case st == 404:
			return ErrNotFound
		case ctx.Err() != nil || !transient(err):
			return failed(ctx, err)
		}
		if attempt == 3 {
			return fmt.Errorf("delete outcome unknown after %d attempts: %w", attempt+1, err)
		}
		if perr := pause(ctx, 20*time.Millisecond, 100*time.Millisecond); perr != nil {
			return perr
		}
		cur, _, gerr := b.get(ctx, key, "")
		switch {
		case errors.Is(gerr, ErrNotFound):
			return nil
		case gerr != nil:
			return gerr
		case etag != "" && cur.etag != etag:
			return errPrecondition
		}
	}
}
