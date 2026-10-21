package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"bobspike/s17/store"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	d "github.com/megakuul/dynamitedb"
)

const (
	fleetBucket = "s17-fleet"
	lakeBucket  = "s17-analytics" // stand-in for lake: one object per flush (see write-up)
	ctlBucket   = "s17-control"
)

// S3 byte/request counters for one process.
type s3Count struct{ Req, GetBytes, PutBytes, List atomic.Int64 }

type countRT struct {
	base http.RoundTripper
	c    *s3Count
}

func (t countRT) RoundTrip(r *http.Request) (*http.Response, error) {
	t.c.Req.Add(1)
	if r.Method == http.MethodPut && r.ContentLength > 0 {
		t.c.PutBytes.Add(r.ContentLength)
	}
	if r.Method == http.MethodGet && r.URL.Query().Has("list-type") {
		t.c.List.Add(1)
	}
	resp, err := t.base.RoundTrip(r)
	if err == nil && r.Method == http.MethodGet && resp.ContentLength > 0 {
		t.c.GetBytes.Add(resp.ContentLength)
	}
	return resp, err
}

func newS3(endpoint string, c *s3Count) *s3.Client {
	return s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		Credentials:                credentials.NewStaticCredentialsProvider("bobadmin", "bobadmin-secret", ""),
		HTTPClient:                 &http.Client{Transport: countRT{&http.Transport{MaxIdleConnsPerHost: 100}, c}, Timeout: 20 * time.Second},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// ---------- discovery records (fleet bucket, dynamitedb) ----------

// OrchRec: key orch/<orchID>. Written only by that orchestrator (workers have no write
// permission on orch/, S11 policy). Read by orchestrators and workers.
type OrchRec struct {
	ID        d.KeyField             `pk:"orch" cbor:"-"`
	Endpoint  d.DataField[string]    `cbor:"u,omitempty"`  // https://host:port
	CertFP    d.DataField[string]    `cbor:"fp,omitempty"` // hex sha256 of the TLS leaf SPKI, pinned by peers
	Peering   d.DataField[bool]      `cbor:"pe,omitempty"` // serves bob.peer.v1
	Proto     d.DataField[int]       `cbor:"pv,omitempty"` // peer protocol version
	Heartbeat d.DataField[time.Time] `cbor:"h,omitempty"`
	Started   d.DataField[time.Time] `cbor:"st,omitempty"`
}

type Member struct {
	ID, Endpoint, CertFP string
	Peering              bool
	Heartbeat            time.Time
}

func putOrch(ctx context.Context, b *d.Bucket, m Member, started time.Time) error {
	return store.Put(ctx, b, &OrchRec{ID: d.Key(m.ID), Endpoint: d.Set(m.Endpoint), CertFP: d.Set(m.CertFP),
		Peering: d.Set(m.Peering), Proto: d.Set(1), Heartbeat: d.Set(time.Now().UTC()), Started: d.Set(started)})
}

// listMembers: one LIST on orch/ plus one GET per orchestrator; members whose heartbeat is
// older than stale are dropped.
func listMembers(ctx context.Context, cli *s3.Client, b *d.Bucket, stale time.Duration) ([]Member, error) {
	out, err := cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(fleetBucket), Prefix: aws.String("orch/")})
	if err != nil {
		return nil, err
	}
	var ms []Member
	for _, o := range out.Contents {
		id := strings.TrimPrefix(*o.Key, "orch/")
		r, err := store.Get(ctx, b, &OrchRec{ID: d.Key(id)})
		if err != nil || r == nil {
			continue
		}
		m := Member{ID: id, Endpoint: val(r.Endpoint), CertFP: val(r.CertFP), Peering: val(r.Peering), Heartbeat: val(r.Heartbeat)}
		if time.Since(m.Heartbeat) < stale {
			ms = append(ms, m)
		}
	}
	return ms, nil
}

func val[T any](f d.DataField[T]) (v T) {
	defer func() { recover() }()
	if f == nil {
		return v
	}
	return f.Value()
}

// ---------- lake stand-in ----------
// Key: lake/<build>/<pkg>/<att 3d>-<first 10d>-<last 10d>.jsonl, and lake/<build>/_end.json.
// Objects are immutable, so a reader caches by key.

func segKey(build, pkg string, att int32, first, last int64) string {
	return fmt.Sprintf("lake/%s/%s/%03d-%010d-%010d.jsonl", build, pkg, att, first, last)
}

func putJSONL(ctx context.Context, cli *s3.Client, key string, lines []Line) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range lines {
		enc.Encode(l)
	}
	_, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(lakeBucket), Key: aws.String(key), Body: bytes.NewReader(buf.Bytes())})
	return err
}

func putEnd(ctx context.Context, cli *s3.Client, build string, f []Final) error {
	b, _ := json.Marshal(f)
	_, err := cli.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(lakeBucket), Key: aws.String("lake/" + build + "/_end.json"), Body: bytes.NewReader(b)})
	return err
}

func listLake(ctx context.Context, cli *s3.Client, build string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(cli, &s3.ListObjectsV2Input{Bucket: aws.String(lakeBucket), Prefix: aws.String("lake/" + build + "/")})
	for p.HasMorePages() {
		pg, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range pg.Contents {
			keys = append(keys, *o.Key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func getObj(ctx context.Context, cli *s3.Client, key string) ([]byte, error) {
	o, err := cli.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(lakeBucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer o.Body.Close()
	return io.ReadAll(o.Body)
}

func parseJSONL(b []byte) []Line {
	var out []Line
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var l Line
		if dec.Decode(&l) != nil {
			return out
		}
		out = append(out, l)
	}
}

// segMeta parses att and first seq from a segment key.
func segMeta(key string) (pkg string, att int32, first int64, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 {
		return "", 0, 0, false
	}
	var last int64
	if _, err := fmt.Sscanf(parts[3], "%03d-%010d-%010d.jsonl", &att, &first, &last); err != nil {
		return "", 0, 0, false
	}
	return parts[2], att, first, true
}

func ensureBuckets(ctx context.Context, cli *s3.Client) error {
	for _, b := range []string{fleetBucket, lakeBucket, ctlBucket} {
		_, err := cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(b)})
		if err != nil && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") && !strings.Contains(err.Error(), "BucketAlreadyExists") {
			return err
		}
	}
	return nil
}
