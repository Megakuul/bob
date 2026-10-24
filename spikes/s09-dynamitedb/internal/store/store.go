// Package store wraps dynamitedb for the spike: panic-safe calls, request
// counting and fault injection at the HTTP layer.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/megakuul/dynamitedb"
)

// ErrPanic marks an error that was converted from a dynamitedb panic.
var ErrPanic = errors.New("store: dynamitedb panicked")

// Call runs fn and converts a panic in the calling goroutine into ErrPanic.
// It cannot catch panics in goroutines that dynamitedb starts itself (Query).
func Call(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v\n%s", ErrPanic, r, debug.Stack())
		}
	}()
	return fn()
}

func Get[T any](ctx context.Context, b *dynamitedb.Bucket, f *T) (out *T, err error) {
	err = Call(func() error { var e error; out, e = dynamitedb.Get(ctx, b, f); return e })
	return
}
func Query[T any](ctx context.Context, b *dynamitedb.Bucket, f *T, o ...dynamitedb.Option) (out []*T, err error) {
	err = Call(func() error { var e error; out, e = dynamitedb.Query(ctx, b, f, o...); return e })
	return
}
func Create[T any](ctx context.Context, b *dynamitedb.Bucket, m *T, o ...dynamitedb.Option) error {
	return Call(func() error { return dynamitedb.Create(ctx, b, m, o...) })
}
func Put[T any](ctx context.Context, b *dynamitedb.Bucket, m *T, o ...dynamitedb.Option) error {
	return Call(func() error { return dynamitedb.Put(ctx, b, m, o...) })
}
func Update[T any](ctx context.Context, b *dynamitedb.Bucket, m *T, o ...dynamitedb.Option) error {
	return Call(func() error { return dynamitedb.Update(ctx, b, m, o...) })
}
func Delete[T any](ctx context.Context, b *dynamitedb.Bucket, f *T) error {
	return Call(func() error { return dynamitedb.Delete(ctx, b, f) })
}

// Counter counts HTTP requests by S3 operation class.
type Counter struct {
	List, Get, Put, Delete, Head, Other atomic.Int64
	Status412, Status404, Status5xx    atomic.Int64
}

func (c *Counter) Total() int64 {
	return c.List.Load() + c.Get.Load() + c.Put.Load() + c.Delete.Load() + c.Head.Load() + c.Other.Load()
}
func (c *Counter) Reset() {
	for _, a := range []*atomic.Int64{&c.List, &c.Get, &c.Put, &c.Delete, &c.Head, &c.Other, &c.Status412, &c.Status404, &c.Status5xx} {
		a.Store(0)
	}
}
func (c *Counter) String() string {
	return fmt.Sprintf("LIST=%d GET=%d PUT=%d DELETE=%d other=%d (412=%d 404=%d 5xx=%d)",
		c.List.Load(), c.Get.Load(), c.Put.Load(), c.Delete.Load(), c.Head.Load()+c.Other.Load(),
		c.Status412.Load(), c.Status404.Load(), c.Status5xx.Load())
}

// Fault describes an injected failure. Match returns true for requests to fail.
type Fault struct {
	// Mode: "500" (return 500 without forwarding), "reset" (connection error, not forwarded),
	// "lostresp" (forward, then drop response), "noetag" (forward, strip ETag header),
	// "slow" (forward after Delay).
	Mode  string
	Delay time.Duration
	Match func(*http.Request) bool
	// Remaining is the number of requests to fail; <0 means unlimited.
	Remaining int
}

// Transport counts requests and injects faults.
type Transport struct {
	Base    http.RoundTripper
	Count   *Counter
	mu      sync.Mutex
	faults  []*Fault
	Injected atomic.Int64
	// UnconditionalPuts counts PUTs that carried neither If-Match nor If-None-Match.
	UnconditionalPuts atomic.Int64
}

func (t *Transport) AddFault(f *Fault) { t.mu.Lock(); t.faults = append(t.faults, f); t.mu.Unlock() }
func (t *Transport) ClearFaults()      { t.mu.Lock(); t.faults = nil; t.mu.Unlock() }

func (t *Transport) pick(r *http.Request) *Fault {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, f := range t.faults {
		if f.Remaining == 0 || (f.Match != nil && !f.Match(r)) {
			continue
		}
		if f.Remaining > 0 {
			f.Remaining--
		}
		return f
	}
	return nil
}

func classify(r *http.Request) string {
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Has("list-type") || strings.Count(strings.Trim(r.URL.Path, "/"), "/") == 0 {
			return "LIST"
		}
		return "GET"
	case http.MethodPut:
		return "PUT"
	case http.MethodDelete:
		return "DELETE"
	case http.MethodHead:
		return "HEAD"
	}
	return "OTHER"
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.Count != nil {
		switch classify(r) {
		case "LIST":
			t.Count.List.Add(1)
		case "GET":
			t.Count.Get.Add(1)
		case "PUT":
			t.Count.Put.Add(1)
		case "DELETE":
			t.Count.Delete.Add(1)
		case "HEAD":
			t.Count.Head.Add(1)
		default:
			t.Count.Other.Add(1)
		}
	}
	if r.Method == http.MethodPut && r.Header.Get("If-Match") == "" && r.Header.Get("If-None-Match") == "" {
		t.UnconditionalPuts.Add(1)
	}
	f := t.pick(r)
	if f != nil {
		t.Injected.Add(1)
		switch f.Mode {
		case "500":
			return &http.Response{StatusCode: 500, Status: "500 Internal Server Error", Header: http.Header{"Content-Type": {"application/xml"}},
				Body:    io.NopCloser(strings.NewReader(`<?xml version="1.0"?><Error><Code>InternalError</Code><Message>injected</Message></Error>`)),
				Request: r, ProtoMajor: 1, ProtoMinor: 1}, nil
		case "503":
			return &http.Response{StatusCode: 503, Status: "503 Slow Down", Header: http.Header{"Content-Type": {"application/xml"}},
				Body:    io.NopCloser(strings.NewReader(`<?xml version="1.0"?><Error><Code>SlowDown</Code><Message>injected</Message></Error>`)),
				Request: r, ProtoMajor: 1, ProtoMinor: 1}, nil
		case "reset":
			return nil, errors.New("injected: connection reset by peer")
		case "slow":
			select {
			case <-time.After(f.Delay):
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
	}
	resp, err := t.Base.RoundTrip(r)
	if err != nil {
		return resp, err
	}
	if t.Count != nil {
		switch {
		case resp.StatusCode == 412:
			t.Count.Status412.Add(1)
		case resp.StatusCode == 404:
			t.Count.Status404.Add(1)
		case resp.StatusCode >= 500:
			t.Count.Status5xx.Add(1)
		}
	}
	if f != nil {
		switch f.Mode {
		case "lostresp":
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, errors.New("injected: response lost after server processed the request")
		case "noetag":
			resp.Header.Del("ETag")
		}
	}
	return resp, nil
}

// Conn is one S3 client + dynamitedb bucket with its own counters.
type Conn struct {
	S3     *s3.Client
	Bucket *dynamitedb.Bucket
	T      *Transport
	Name   string
}

// Open creates a client. maxAttempts<=0 keeps the SDK default (3).
func Open(endpoint, bucket, ak, sk string, count *Counter, maxAttempts int) *Conn {
	tr := &Transport{Base: &http.Transport{MaxIdleConns: 200, MaxIdleConnsPerHost: 200, IdleConnTimeout: 90 * time.Second}, Count: count}
	cli := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(ak, sk, ""),
		HTTPClient:   &http.Client{Transport: tr},
		Retryer: retry.NewStandard(func(o *retry.StandardOptions) {
			if maxAttempts > 0 {
				o.MaxAttempts = maxAttempts
			}
		}),
		// RustFS/SeaweedFS do not need the new default checksums; keep requests plain.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Conn{S3: cli, Bucket: dynamitedb.NewFromClient(cli, bucket), T: tr, Name: bucket}
}

// EnsureBucket creates the bucket if it does not exist and empties it if wipe is set.
func (c *Conn) EnsureBucket(ctx context.Context, wipe bool) error {
	_, err := c.S3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(c.Name)})
	if err != nil && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") && !strings.Contains(err.Error(), "BucketAlreadyExists") {
		return err
	}
	if !wipe {
		return nil
	}
	p := s3.NewListObjectsV2Paginator(c.S3, &s3.ListObjectsV2Input{Bucket: aws.String(c.Name)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, o := range page.Contents {
			if _, err := c.S3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.Name), Key: o.Key}); err != nil {
				return err
			}
		}
	}
	return nil
}
