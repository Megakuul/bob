package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmw "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
	"github.com/megakuul/dynamitedb"

	"github.com/megakuul/bob/internal/store/s3client"
)

// Bucket is one dynamitedb bucket plus a raw S3 client for conditional GETs.
// It is safe for concurrent use.
//
// Records are read and written only through dynamitedb (db). The raw client s3 serves what
// the library does not cover: the conformance probe, GET If-None-Match of GetCached, and the
// re-read that decides a write whose response was lost (R-STORE-103 rule 7).
type Bucket struct {
	name     string
	endpoint string
	role     string
	s3       *s3.Client
	db       *dynamitedb.Bucket
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

// OpenUnprobed connects to the bucket without the conformance probe (it writes nothing). Only
// for operator commands that read (bob-server store fsck); a server process uses Open.
func OpenUnprobed(ctx context.Context, cfg BucketConfig) (*Bucket, error) {
	return newBucket(ctx, cfg)
}

// ReadRaw returns the stored bytes of key (bob-server store fsck checks the objects of kinds it
// has no record type for). A missing key is ErrNotFound.
func ReadRaw(ctx context.Context, b *Bucket, key string) ([]byte, error) {
	return Call(ctx, func(ctx context.Context) ([]byte, error) {
		if err := checkBucket(b); err != nil {
			return nil, err
		}
		o, _, err := b.rawGet(ctx, key, "")
		return o.body, wrap(key, failed(ctx, err))
	})
}

func newBucket(ctx context.Context, cfg BucketConfig) (*Bucket, error) {
	if _, err := Segment(cfg.Name); err != nil {
		return nil, fmt.Errorf("store: bucket name %q is invalid", cfg.Name)
	}
	c, err := s3client.New(ctx, s3client.Options{Endpoint: cfg.Endpoint, Region: cfg.Region,
		Retries: 3, APIOptions: []func(*middleware.Stack) error{observeWire}})
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Bucket{name: cfg.Name, endpoint: cfg.Endpoint, role: cfg.Role, s3: c,
		db: dynamitedb.NewFromClient(c, cfg.Name)}, nil
}

// Name is the bucket name.
func (b *Bucket) Name() string { return b.name }

// errNoETag is a raw read without an ETag header (a non-compliant backend).
var errNoETag = fmt.Errorf("%w: the backend returned no ETag header", ErrPanic)

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

// unchanged sends GET If-None-Match: etag (GetCached). It reports true on 304; on 200 the
// body is discarded (the caller reads the record with dynamitedb.Get).
func (b *Bucket) unchanged(ctx context.Context, key, etag string) (bool, error) {
	resp, err := b.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.name), Key: aws.String(key), IfNoneMatch: aws.String(etag)})
	if err != nil {
		switch s3client.Status(err) {
		case 304:
			return true, nil
		case 404:
			return false, ErrNotFound
		}
		return false, failed(ctx, err)
	}
	_ = resp.Body.Close()
	return false, nil
}

// holds re-reads key raw and reports whether it holds exactly body (mine) and whether it
// exists at all (found). It decides writes whose response was lost (R-STORE-103 rule 7).
func (b *Bucket) holds(ctx context.Context, key string, body []byte) (mine, found bool, err error) {
	resp, err := b.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.name), Key: aws.String(key)})
	if err != nil {
		if s3client.Status(err) == 404 {
			return false, false, nil
		}
		return false, false, failed(ctx, err)
	}
	defer resp.Body.Close()
	if resp.ETag == nil {
		return false, true, errNoETag
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, true, failed(ctx, err)
	}
	return body != nil && bytes.Equal(got, body), true, nil
}

// Operation names of the wire log.
const (
	opGet    = "GetObject"
	opPut    = "PutObject"
	opDelete = "DeleteObject"
)

// wire is what the S3 client saw while one store call ran dynamitedb: per operation the
// number of attempts and the HTTP status of the last one (0 = no response), and the body of
// the last PutObject. dynamitedb hides S3 errors behind its sentinels (by design); the store
// still needs to know whether a write's answer was lost (rule 7), whether a PUT If-Match hit
// a deleted key (S9 B13), and whether dynamitedb.Delete found the key.
type wire struct {
	mu       sync.Mutex
	attempts map[string]int
	last     map[string]int
	body     []byte
}

type wireKey struct{}

// observe returns ctx carrying a fresh wire log.
func observe(ctx context.Context) (context.Context, *wire) {
	w := &wire{attempts: map[string]int{}, last: map[string]int{}}
	return context.WithValue(ctx, wireKey{}, w), w
}

func (w *wire) retried(op string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempts[op] > 1
}

// unanswered reports whether the last attempt of op got no definite answer (no response or 5xx).
func (w *wire) unanswered(op string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	st, ok := w.last[op]
	return ok && (st == 0 || st >= 500)
}

func (w *wire) status(op string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last[op]
}

func (w *wire) sent() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body
}

// observeWire adds the wire log middlewares to a client: the PutObject body is captured once
// per call (initialize step) and every attempt's status is recorded after the SDK's retry
// middleware (finalize step). Calls without a wire log in their context are not touched.
func observeWire(st *middleware.Stack) error {
	body := middleware.InitializeMiddlewareFunc("bobWireBody", func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
		if w, ok := ctx.Value(wireKey{}).(*wire); ok {
			if p, ok := in.Parameters.(*s3.PutObjectInput); ok && p.Body != nil {
				b, err := io.ReadAll(p.Body)
				if err != nil {
					return middleware.InitializeOutput{}, middleware.Metadata{}, err
				}
				p.Body = bytes.NewReader(b)
				w.mu.Lock()
				w.body = b
				w.mu.Unlock()
			}
		}
		return next.HandleInitialize(ctx, in)
	})
	attempt := middleware.FinalizeMiddlewareFunc("bobWireAttempt", func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
		out, md, err := next.HandleFinalize(ctx, in)
		if w, ok := ctx.Value(wireKey{}).(*wire); ok {
			st := 200
			if err != nil {
				st = s3client.Status(err)
			}
			op := awsmw.GetOperationName(ctx)
			w.mu.Lock()
			w.attempts[op]++
			w.last[op] = st
			w.mu.Unlock()
		}
		return out, md, err
	})
	if err := st.Initialize.Add(body, middleware.Before); err != nil {
		return err
	}
	return st.Finalize.Insert(attempt, "Retry", middleware.After)
}
