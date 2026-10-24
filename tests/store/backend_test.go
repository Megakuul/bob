package store

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/store/env"
)

// probe sends one raw request as the harness root and returns status and headers.
func probe(t testing.TB, be *env.Backend, method, path string, hdr http.Header, body []byte) (int, http.Header, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := be.FS.Do(ctx, be.FS.Root, method, path, hdr, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, resp.Header, sb.String()
}

// TestRSTORE102BackendHonoursTheSevenChecks runs the seven checks of the acceptance list
// against the reference backend (RustFS 1.0.3), then requires store.Open to run its own probe
// against it and to leave nothing behind under _bob/probe/.
func TestRSTORE102BackendHonoursTheSevenChecks(t *testing.T) {
	parity.Covers(t, "R-STORE-102")
	be := env.Start(t)
	cfg := be.Bucket(t, "fleet")
	bk := "/" + cfg.Name
	base := bk + "/_bob/probe/r1"

	// 1. PUT If-None-Match: * twice -> the second is 412.
	h := http.Header{"If-None-Match": {"*"}}
	if st, _, _ := probe(t, be, http.MethodPut, base+"/x", h, []byte("1")); st != http.StatusOK {
		t.Fatalf("check 1: first conditional create = %d, want 200", st)
	}
	if st, _, _ := probe(t, be, http.MethodPut, base+"/x", h, []byte("2")); st != http.StatusPreconditionFailed {
		t.Errorf("check 1: second conditional create = %d, want 412", st)
	}
	// 2. PUT If-Match: <stale> -> 412.
	if st, _, _ := probe(t, be, http.MethodPut, base+"/x", http.Header{"If-Match": {`"00000000000000000000000000000000"`}}, []byte("3")); st != http.StatusPreconditionFailed {
		t.Errorf("check 2: PUT with a stale If-Match = %d, want 412", st)
	}
	// 3. GET If-None-Match: <current ETag> -> 304; 7. every GET returns an ETag.
	st, hd, _ := probe(t, be, http.MethodGet, base+"/x", nil, nil)
	etag := hd.Get("ETag")
	if st != http.StatusOK || etag == "" {
		t.Fatalf("check 7: GET = %d with ETag %q, want 200 and an ETag", st, etag)
	}
	if st, _, _ := probe(t, be, http.MethodGet, base+"/x", http.Header{"If-None-Match": {etag}}, nil); st != http.StatusNotModified {
		t.Errorf("check 3: GET If-None-Match with the current ETag = %d, want 304", st)
	}
	// 4. GET of a missing key -> 404 NoSuchKey.
	st, _, body := probe(t, be, http.MethodGet, base+"/missing", nil, nil)
	if st != http.StatusNotFound || !strings.Contains(body, "NoSuchKey") {
		t.Errorf("check 4: GET of a missing key = %d %q, want 404 NoSuchKey", st, body)
	}
	// 5. DELETE If-Match: <stale> -> 412.
	if st, _, _ := probe(t, be, http.MethodDelete, base+"/x", http.Header{"If-Match": {`"00000000000000000000000000000000"`}}, nil); st != http.StatusPreconditionFailed {
		t.Errorf("check 5: DELETE with a stale If-Match = %d, want 412", st)
	}
	// 6. GET of <dir>/ after deleting <dir>/x -> 404 (no phantom directory marker).
	if st, _, _ := probe(t, be, http.MethodDelete, base+"/x", nil, nil); st != http.StatusNoContent && st != http.StatusOK {
		t.Fatalf("check 6: DELETE = %d", st)
	}
	if st, _, _ := probe(t, be, http.MethodGet, base+"/", nil, nil); st != http.StatusNotFound {
		t.Errorf("check 6: GET of the emptied directory key = %d, want 404", st)
	}

	// store.Open runs the same checks under _bob/probe/<random> and cleans up.
	b := be.Open(t, "fleet")
	if b == nil {
		return
	}
	var puts int
	for _, r := range be.Proxy.Counts().Rows {
		if strings.HasPrefix(r.Bucket, "t") && r.Prefix == "_bob/" && r.Op == s3count.OpPut {
			puts += r.Count
		}
	}
	if puts < 2 {
		t.Errorf("store.Open made %d PUTs under _bob/, want >= 2 (create twice, stale If-Match)", puts)
	}
	st, _, body = probe(t, be, http.MethodGet, bk+"?list-type=2&prefix=_bob/probe/", nil, nil)
	if st != http.StatusOK {
		t.Fatalf("list _bob/probe/: %d %s", st, body)
	}
	if strings.Count(body, "<Key>") != 0 {
		t.Errorf("objects remain under _bob/probe/ after Open: %s", body)
	}
}

const (
	unsupportedFmt = "✗ storage backend %s is not supported: conditional create (If-None-Match) is ignored"
	unsupportedHnt = "hint: bob needs an AWS-S3-compatible server with conditional writes, e.g. RustFS ≥ 1.0.3 or AWS S3"
)

// TestRSTORE102RefusesBackendThatIgnoresIfNoneMatch: a fake backend (the proxy strips the
// conditional headers) makes store.Open fail, and every bob-server mode exit 78 with the text.
func TestRSTORE102RefusesBackendThatIgnoresIfNoneMatch(t *testing.T) {
	parity.Covers(t, "R-STORE-102")
	be := env.Start(t)
	be.DefaultBuckets(t)
	be.Proxy.SetScript(s3count.Script{Seed: 102, Faults: []s3count.Fault{{Kind: s3count.FaultIgnoreCond}}})

	be.Creds(t)
	cfg := be.Bucket(t, "fleet")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err := store.Open(ctx, cfg)
	if env.Red(t, err) {
		goto binary
	}
	if err == nil || !strings.Contains(err.Error(), "conditional create (If-None-Match) is ignored") ||
		!strings.Contains(err.Error(), be.Proxy.URL) {
		t.Errorf("store.Open on a backend that ignores If-None-Match = %v; want an error naming the endpoint and %q",
			err, "conditional create (If-None-Match) is ignored")
	}

binary:
	for _, mode := range []string{"orchestrator", "worker"} {
		r := harness.Run(t, "bob-server", harness.Spec{Env: be.ServerEnv(), Timeout: time.Minute}, mode)
		if harness.Red(t, r) {
			continue
		}
		if r.Exit != 78 {
			t.Errorf("bob-server %s exit = %d, want 78\n%s", mode, r.Exit, r.Combined())
		}
		out := r.Stderr
		if !strings.Contains(out, strings.TrimSpace(fmtEndpoint(unsupportedFmt, be.Proxy.URL))) || !strings.Contains(out, unsupportedHnt) {
			t.Errorf("bob-server %s stderr lacks the refusal text\nwant: %s\n      %s\ngot:\n%s", mode, fmtEndpoint(unsupportedFmt, be.Proxy.URL), unsupportedHnt, out)
		}
	}
}

func fmtEndpoint(f, endpoint string) string { return strings.Replace(f, "%s", endpoint, 1) }
