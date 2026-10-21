// Package logstore is the S10 spike's log/analytics layer on top of lake.
package logstore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Counter counts S3 requests by kind, plus bytes sent and received.
type Counter struct {
	mu      sync.Mutex
	counts  map[string]int64
	kindUp  [2]atomic.Int64 // 0 data, 1 catalog
	kindDn  [2]atomic.Int64
	BytesUp atomic.Int64
	BytesDn atomic.Int64
}

func NewCounter() *Counter { return &Counter{counts: map[string]int64{}} }

func (c *Counter) add(k string) {
	c.mu.Lock()
	c.counts[k]++
	c.mu.Unlock()
}

// Snapshot returns a copy of the counts.
func (c *Counter) Snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int64{}
	for k, v := range c.counts {
		out[k] = v
	}
	return out
}

func (c *Counter) Reset() {
	c.mu.Lock()
	c.counts = map[string]int64{}
	c.mu.Unlock()
	c.BytesUp.Store(0)
	c.BytesDn.Store(0)
	for i := range 2 {
		c.kindUp[i].Store(0)
		c.kindDn[i].Store(0)
	}
}

// KindBytes returns bytes sent/received for data objects and for the catalog.
func (c *Counter) KindBytes() (dataUp, dataDn, catUp, catDn int64) {
	return c.kindUp[0].Load(), c.kindDn[0].Load(), c.kindUp[1].Load(), c.kindDn[1].Load()
}

func (c *Counter) String() string {
	s := c.Snapshot()
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%d ", k, s[k])
	}
	fmt.Fprintf(&b, "up=%.1fMB down=%.1fMB (data up=%.1fMB down=%.1fMB, catalog up=%.1fMB down=%.1fMB)", float64(c.BytesUp.Load())/1e6, float64(c.BytesDn.Load())/1e6,
		float64(c.kindUp[0].Load())/1e6, float64(c.kindDn[0].Load())/1e6, float64(c.kindUp[1].Load())/1e6, float64(c.kindDn[1].Load())/1e6)
	return b.String()
}

type countingBody struct {
	io.ReadCloser
	n, k *atomic.Int64
}

func (b countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	b.k.Add(int64(n))
	return n, err
}

type countingRT struct {
	next http.RoundTripper
	c    *Counter
}

func (t countingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	kind, ki := "data", 0
	if strings.HasSuffix(r.URL.Path, "lakedb.catalog") {
		kind, ki = "catalog", 1
	}
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = countingBody{r.Body, &t.c.BytesUp, &t.c.kindUp[ki]}
	}
	resp, err := t.next.RoundTrip(r)
	if err != nil {
		t.c.add(r.Method + "." + kind + ".neterr")
		return resp, err
	}
	key := r.Method + "." + kind
	if resp.StatusCode >= 300 {
		key += fmt.Sprintf(".%d", resp.StatusCode)
	}
	t.c.add(key)
	resp.Body = countingBody{resp.Body, &t.c.BytesDn, &t.c.kindDn[ki]}
	return resp, nil
}

// Endpoint describes an S3 server.
type Endpoint struct {
	URL, Access, Secret string
}

var SeaweedFS = Endpoint{"http://127.0.0.1:21000", "bobadmin", "bobadmin-secret"}
var RustFS = Endpoint{"http://127.0.0.1:21050", "bobadmin", "bobadmin-secret"}

// Client builds an S3 client whose requests are counted in c (c may be nil).
func Client(ep Endpoint, c *Counter) *s3.Client {
	return ClientWith(ep, c, nil)
}

// RTFunc adapts a function to http.RoundTripper.
type RTFunc func(*http.Request) (*http.Response, error)

func (f RTFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ClientWith is Client with an extra RoundTripper wrapper (for fault injection).
func ClientWith(ep Endpoint, c *Counter, wrap func(http.RoundTripper) http.RoundTripper) *s3.Client {
	var rt http.RoundTripper = &http.Transport{MaxIdleConns: 200, MaxIdleConnsPerHost: 200, IdleConnTimeout: 90 * time.Second}
	if c != nil {
		rt = countingRT{rt, c}
	}
	if wrap != nil {
		rt = wrap(rt)
	}
	return s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(ep.URL),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(ep.Access, ep.Secret, ""),
		HTTPClient:   &http.Client{Transport: rt},
	})
}

// FreshBucket creates (or empties) an S3 bucket.
func FreshBucket(ctx context.Context, cl *s3.Client, name string) error {
	_, err := cl.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
	if err != nil && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") && !strings.Contains(err.Error(), "BucketAlreadyExists") {
		return err
	}
	p := s3.NewListObjectsV2Paginator(cl, &s3.ListObjectsV2Input{Bucket: aws.String(name)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, o := range page.Contents {
			if _, err := cl.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(name), Key: o.Key}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListObjects returns key -> size for a bucket.
func ListObjects(ctx context.Context, cl *s3.Client, name string) (map[string]int64, error) {
	out := map[string]int64{}
	p := s3.NewListObjectsV2Paginator(cl, &s3.ListObjectsV2Input{Bucket: aws.String(name)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			out[*o.Key] = *o.Size
		}
	}
	return out, nil
}
