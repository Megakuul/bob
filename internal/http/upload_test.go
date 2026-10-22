package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memSources struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (m *memSources) Has(_ context.Context, project, sha string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objs[project+"/"+sha]
	return ok, nil
}

func (m *memSources) Create(_ context.Context, project, sha string, r io.ReadSeeker, size int64) error {
	b, err := io.ReadAll(r)
	if err != nil || int64(len(b)) != size {
		return errors.New("short read")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objs[project+"/"+sha]; !ok {
		m.objs[project+"/"+sha] = b
	}
	return nil
}

// countingBody records how many bytes the server read.
type countingBody struct {
	r io.Reader
	n int
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestSourceUpload(t *testing.T) {
	src := &memSources{objs: map[string][]byte{}}
	h := NewSourceUpload(SourceUpload{
		Authorize: func(r *http.Request, project string) error {
			switch {
			case r.Header.Get("Authorization") != "Bearer bob_at_ok":
				return ErrUnauthenticated
			case project != "p1":
				return ErrNoAccess
			}
			return nil
		},
		Sources: src, Max: 1 << 20, TempDir: t.TempDir(), Log: slog.New(slog.DiscardHandler),
	})
	srv := httptest.NewServer(Handler(Routes{Patterns: map[string]http.Handler{UploadPattern: h}}, slog.New(slog.DiscardHandler)))
	defer srv.Close()

	body := bytes.Repeat([]byte("snapshot "), 1000)
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	put := func(sha, project, token, ctype string, b io.Reader) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/upload/source/sha256/"+sha, b)
		req.Header.Set("Bob-Project", project)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", ctype)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(msg))
	}
	const zst = "application/zstd"

	cases := []struct {
		name, sha, project, token, ctype string
		body                             []byte
		code                             int
		msg                              string
	}{
		{"bad hash syntax", "ABC", "p1", "bob_at_ok", zst, body, 400, "invalid sha256"},
		{"no project header", sha, "", "bob_at_ok", zst, body, 400, "Bob-Project"},
		{"no credential", sha, "p1", "nope", zst, body, 401, "unauthorized"},
		{"no CreateBuild on the project", sha, "p2", "bob_at_ok", zst, body, 404, "project not found"},
		{"wrong content type", sha, "p1", "bob_at_ok", "application/octet-stream", body, 415, "application/zstd"},
		{"too large", sha, "p1", "bob_at_ok", zst, make([]byte, 1<<20+1), 413, "too large"},
		{"wrong hash", strings.Repeat("0", 64), "p1", "bob_at_ok", zst, body, 400, "upload does not match its sha256"},
		{"created", sha, "p1", "bob_at_ok", zst, body, 201, ""},
	}
	for _, c := range cases {
		code, msg := put(c.sha, c.project, c.token, c.ctype, bytes.NewReader(c.body))
		if code != c.code || !strings.Contains(msg, c.msg) {
			t.Errorf("%s: %d %q, want %d %q", c.name, code, msg, c.code, c.msg)
		}
	}
	if len(src.objs) != 1 || !bytes.Equal(src.objs["p1/"+sha], body) {
		t.Fatalf("stored objects: %d (the wrong hash must leave none)", len(src.objs))
	}

	// the same upload again: 200 before the body is read (Expect: 100-continue)
	big := &countingBody{r: bytes.NewReader(bytes.Repeat([]byte("x"), 900<<10))}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/upload/source/sha256/"+sha, big)
	req.ContentLength = 900 << 10
	req.Header.Set("Bob-Project", "p1")
	req.Header.Set("Authorization", "Bearer bob_at_ok")
	req.Header.Set("Content-Type", zst)
	req.Header.Set("Expect", "100-continue")
	tr := &http.Transport{ExpectContinueTimeout: 5 * time.Second}
	defer tr.CloseIdleConnections()
	start := time.Now()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || time.Since(start) > 2*time.Second || big.n > 0 {
		t.Errorf("repeated upload: %d after %v with %d body bytes sent; want 200 at once without the body", resp.StatusCode, time.Since(start), big.n)
	}

	// another project has its own namespace
	h.Authorize = func(*http.Request, string) error { return nil }
	if code, _ := put(sha, "p3", "x", zst, bytes.NewReader(body)); code != 201 || len(src.objs) != 2 {
		t.Errorf("upload to another project: %d, %d objects", code, len(src.objs))
	}
}
