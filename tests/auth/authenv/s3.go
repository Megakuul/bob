package authenv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/s3sig"
)

func (s *Server) s3(ctx context.Context, path string, q url.Values) ([]byte, error) {
	endpoint, _ := s.Info["s3_url"].(string) // direct to RustFS: the harness LIST must not count against the proxy
	ak, _ := s.Info["access_key"].(string)
	sk, _ := s.Info["secret_key"].(string)
	region, _ := s.Info["region"].(string)
	u := endpoint + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	empty := sha256.Sum256(nil)
	s3sig.Sign(req, s3sig.Creds{AccessKey: ak, SecretKey: sk, Region: region}, hex.EncodeToString(empty[:]), time.Now())
	resp, err := HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("S3 GET %s: %s: %s", path, resp.Status, b)
	}
	return b, nil
}

// Scan calls fn for every object of bucket (key and bytes). It lists with the stack's root
// credentials (a harness check, not product behavior) and returns the number of objects.
func (s *Server) Scan(t testing.TB, bucket string, fn func(key string, data []byte)) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	token, n := "", 0
	for {
		q := url.Values{"list-type": {"2"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		b, err := s.s3(ctx, "/"+bucket, q)
		if err != nil {
			t.Fatalf("harness error: %v", err)
		}
		var res struct {
			Contents              []struct{ Key string }
			IsTruncated           bool
			NextContinuationToken string
		}
		if err := xml.Unmarshal(b, &res); err != nil {
			t.Fatalf("harness error: list %s: %v", bucket, err)
		}
		for _, o := range res.Contents {
			data, err := s.s3(ctx, "/"+bucket+"/"+(&url.URL{Path: o.Key}).EscapedPath(), nil)
			if err != nil {
				t.Fatalf("harness error: %v", err)
			}
			n++
			fn(o.Key, data)
		}
		if !res.IsTruncated {
			return n
		}
		token = res.NextContinuationToken
	}
}

// ObjectsContaining returns the keys of every object of bucket whose bytes contain any needle.
func (s *Server) ObjectsContaining(t testing.TB, bucket string, needles ...string) (hits []string, scanned int) {
	t.Helper()
	scanned = s.Scan(t, bucket, func(key string, data []byte) {
		for _, n := range needles {
			if n != "" && bytes.Contains(data, []byte(n)) {
				hits = append(hits, key)
				return
			}
		}
	})
	return hits, scanned
}

// ObjectMatches returns every match of re in every object of bucket.
func (s *Server) ObjectMatches(t testing.TB, bucket string, re *regexp.Regexp) []string {
	t.Helper()
	var out []string
	s.Scan(t, bucket, func(key string, data []byte) {
		for _, m := range re.FindAll(data, -1) {
			out = append(out, string(m))
		}
	})
	return out
}
