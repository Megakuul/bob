package logstore

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Shard cache bounds. SPEC §05.10.4 says "the last 4 downloaded shards (≤ 256 MiB)", sized for
// 32 MB compacted shards; uncompacted shards of running builds are small (one per commit), so
// the cache is bounded by bytes (256 MiB) and holds up to 256 shards, none over 64 MiB
// (decision D-IB7-3).
const (
	cacheShards   = 256
	cacheBytes    = 256 << 20
	cacheMaxShard = 64 << 20
)

// shardCache keeps the bodies of the last downloaded shard objects in memory, keyed by object
// key. lake names every shard `<table>/<uuid>.parquet` and never rewrites one, so a cached body
// stays valid; only GETs of such keys are cached. lake downloads whole shards on every query
// (S10 F8), so this saves the repeated download of a running build's shards (S10 §3.4: 38 GB
// in 5 min without it).
type shardCache struct {
	next s3.HTTPClient

	mu    sync.Mutex
	order []string // least recently used first
	items map[string][]byte
	size  int
}

func newShardCache(next s3.HTTPClient) *shardCache {
	return &shardCache{next: next, items: map[string][]byte{}}
}

func cacheable(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, ".parquet") &&
		r.Header.Get("Range") == "" && r.URL.RawQuery == ""
}

// Do serves a cached shard or forwards the request.
func (c *shardCache) Do(r *http.Request) (*http.Response, error) {
	if !cacheable(r) {
		return c.next.Do(r)
	}
	key := r.URL.Path
	if b, ok := c.get(key); ok {
		return &http.Response{
			Status: "200 OK", StatusCode: http.StatusOK, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header:        http.Header{"Content-Length": {strconv.Itoa(len(b))}, "Content-Type": {"application/octet-stream"}},
			Body:          io.NopCloser(bytes.NewReader(b)),
			ContentLength: int64(len(b)),
			Request:       r,
		}, nil
	}
	resp, err := c.next.Do(r)
	if err != nil || resp.StatusCode != http.StatusOK || resp.ContentLength > cacheMaxShard {
		return resp, err
	}
	body := resp.Body
	b, err := io.ReadAll(io.LimitReader(body, cacheMaxShard+1))
	if err != nil {
		_ = body.Close()
		return nil, err
	}
	if len(b) > cacheMaxShard { // unknown length and too big: stream the rest uncached
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(b), body), body}
		return resp, nil
	}
	_ = body.Close()
	c.put(key, b)
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	return resp, nil
}

func (c *shardCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.items[key]
	if ok {
		c.touch(key)
	}
	return b, ok
}

func (c *shardCache) touch(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

func (c *shardCache) put(key string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.items[key]; ok {
		c.size -= len(old)
	}
	c.items[key] = b
	c.size += len(b)
	c.touch(key)
	for len(c.order) > cacheShards || c.size > cacheBytes {
		k := c.order[0]
		c.order = c.order[1:]
		c.size -= len(c.items[k])
		delete(c.items, k)
	}
}
