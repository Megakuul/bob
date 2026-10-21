package s3count_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/rustfs"
	"github.com/megakuul/bob/tests/serverenv/s3count"
	"github.com/megakuul/bob/tests/serverenv/s3sig"
)

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		method, target string
		want           s3count.Class
	}{
		{"GET", "/", s3count.Class{Op: s3count.OpListBuckets}},
		{"GET", "/bob-fleet?list-type=2&prefix=jobs/", s3count.Class{Bucket: "bob-fleet", Prefix: "jobs/", Op: s3count.OpList}},
		{"GET", "/bob-fleet", s3count.Class{Bucket: "bob-fleet", Op: s3count.OpList}},
		{"GET", "/bob-fleet/", s3count.Class{Bucket: "bob-fleet", Op: s3count.OpList}},
		{"GET", "/bob-fleet?prefix=a&delimiter=/", s3count.Class{Bucket: "bob-fleet", Prefix: "a", Op: s3count.OpList}},
		{"GET", "/bob-control?versions", s3count.Class{Bucket: "bob-control", Op: s3count.OpList}},
		{"GET", "/bob-control?uploads", s3count.Class{Bucket: "bob-control", Op: s3count.OpList}},
		{"GET", "/bob-control?location", s3count.Class{Bucket: "bob-control", Op: s3count.OpBucket}},
		{"GET", "/bob-control?policy", s3count.Class{Bucket: "bob-control", Op: s3count.OpBucket}},
		{"PUT", "/bob-control", s3count.Class{Bucket: "bob-control", Op: s3count.OpBucket}},
		{"GET", "/bob-fleet/jobs/j1", s3count.Class{Bucket: "bob-fleet", Key: "jobs/j1", Prefix: "jobs/", Op: "GET"}},
		{"PUT", "/bob-fleet/worker/w1/hb", s3count.Class{Bucket: "bob-fleet", Key: "worker/w1/hb", Prefix: "worker/", Op: "PUT"}},
		{"HEAD", "/bob-control/_bob/probe", s3count.Class{Bucket: "bob-control", Key: "_bob/probe", Prefix: "_bob/", Op: "HEAD"}},
		{"GET", "/bob-fleet/k?uploadId=1", s3count.Class{Bucket: "bob-fleet", Key: "k", Prefix: "k", Op: "GET"}},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			got := s3count.Classify(httptest.NewRequest(tc.method, tc.target, nil))
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// fakeTB records assertion failures so a test can check that an assertion fails.
type fakeTB struct {
	mu   sync.Mutex
	errs []string
}

func (f *fakeTB) Helper() {}
func (f *fakeTB) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}

func newProxy(t *testing.T, backend http.Handler, o s3count.Options) *s3count.Server {
	t.Helper()
	be := httptest.NewServer(backend)
	t.Cleanup(be.Close)
	o.Backend = be.URL
	p, err := s3count.New(o)
	if err != nil {
		t.Fatal(err)
	}
	s, err := s3count.Listen(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func get(t *testing.T, client *http.Client, req *http.Request) (*http.Response, error) {
	t.Helper()
	resp, err := client.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return resp, err
}

func TestCountsAndSweepAttribution(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("ETag", `"e"`) })
	s := newProxy(t, ok, s3count.Options{Roles: map[string]string{"AKW": "worker"}})
	creds := s3sig.Creds{AccessKey: "AKW", SecretKey: "x"}
	do := func(target, ua string) {
		req, _ := http.NewRequest(http.MethodGet, s.URL+target, nil)
		req.Header.Set("User-Agent", ua)
		s3sig.Sign(req, creds, s3sig.UnsignedPayload, time.Now())
		if _, err := get(t, http.DefaultClient, req); err != nil {
			t.Fatal(err)
		}
	}
	do("/bob-fleet/jobs/a", "bob/1")
	do("/bob-fleet/jobs/b", "bob/1")
	do("/bob-fleet?list-type=2", "bob/1 bob-sweep/lease-reaper")

	c := s.Counts()
	want := []s3count.Row{
		{Bucket: "bob-fleet", Prefix: "", Op: "LIST", Cred: "worker", Sweep: "lease-reaper", Count: 1},
		{Bucket: "bob-fleet", Prefix: "jobs/", Op: "GET", Cred: "worker", Count: 2},
	}
	if fmt.Sprint(c.Rows) != fmt.Sprint(want) {
		t.Fatalf("rows\n got %+v\nwant %+v", c.Rows, want)
	}
	// A sweep LIST is allowed (R-STORE-101).
	var tb fakeTB
	s.AssertNoList(&tb, "bob-fleet")
	if len(tb.errs) != 0 {
		t.Fatalf("sweep LIST flagged: %v", tb.errs)
	}
	remote, err := s3count.Fetch(context.Background(), s.URL)
	if err != nil || fmt.Sprint(remote.Rows) != fmt.Sprint(want) {
		t.Fatalf("GET /_s3count: %+v, %v", remote, err)
	}
	req, _ := http.NewRequest(http.MethodDelete, s.URL+"/_s3count", nil)
	if _, err := get(t, http.DefaultClient, req); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Counts().Rows); n != 0 {
		t.Fatalf("after reset: %d rows", n)
	}
}

func TestFaults(t *testing.T) {
	var mu sync.Mutex
	var seen []http.Header
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("ETag", `"e1"`)
		_, _ = io.WriteString(w, "body")
	})
	noKeepAlive := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	for _, tc := range []struct {
		fault  s3count.Fault
		check  func(t *testing.T, resp *http.Response, err error)
		header string // request header the backend must not see
	}{
		{fault: s3count.Fault{Kind: s3count.Fault500}, check: wantStatus(500)},
		{fault: s3count.Fault{Kind: s3count.Fault503}, check: wantStatus(503)},
		{fault: s3count.Fault{Kind: s3count.FaultReset}, check: wantErr},
		{fault: s3count.Fault{Kind: s3count.FaultLost}, check: wantErr},
		{fault: s3count.Fault{Kind: s3count.FaultStripETag}, check: func(t *testing.T, resp *http.Response, err error) {
			if err != nil || resp.Header.Get("ETag") != "" {
				t.Errorf("ETag not stripped: %v %v", resp, err)
			}
		}},
		{fault: s3count.Fault{Kind: s3count.FaultLatency, Delay: 50 * time.Millisecond}, check: wantStatus(200)},
		{fault: s3count.Fault{Kind: s3count.FaultIgnoreCond}, check: wantStatus(200), header: "If-Match"},
	} {
		t.Run(tc.fault.Kind, func(t *testing.T) {
			mu.Lock()
			seen = nil
			mu.Unlock()
			s := newProxy(t, backend, s3count.Options{Secrets: map[string]string{"AK": "SK"}})
			s.SetScript(s3count.Script{Seed: 1, Faults: []s3count.Fault{tc.fault}})
			req, _ := http.NewRequest(http.MethodPut, s.URL+"/bob-control/builds/b1", strings.NewReader("x"))
			req.Header.Set("If-Match", `"e0"`)
			s3sig.Sign(req, s3sig.Creds{AccessKey: "AK", SecretKey: "SK"}, s3sig.UnsignedPayload, time.Now())
			start := time.Now()
			resp, err := get(t, noKeepAlive, req)
			tc.check(t, resp, err)
			if tc.fault.Kind == s3count.FaultLatency && time.Since(start) < 50*time.Millisecond {
				t.Error("no latency added")
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.fault.Kind == s3count.FaultLost && len(seen) != 1 {
				t.Errorf("lost response: backend saw %d requests, want 1", len(seen))
			}
			if tc.header != "" && (len(seen) != 1 || seen[0].Get(tc.header) != "") {
				t.Errorf("backend saw %s: %v", tc.header, seen)
			}
			if got := s.Counts().Faults; len(got) != 1 || got[0].Kind != tc.fault.Kind {
				t.Errorf("injection log %+v", got)
			}
		})
	}
}

func wantStatus(code int) func(*testing.T, *http.Response, error) {
	return func(t *testing.T, resp *http.Response, err error) {
		t.Helper()
		if err != nil || resp.StatusCode != code {
			t.Errorf("got %v %v, want status %d", resp, err, code)
		}
	}
}

func wantErr(t *testing.T, resp *http.Response, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("got %s, want a connection error", resp.Status)
	}
}

// The same seed and request sequence inject the same faults.
func TestScriptReplayable(t *testing.T) {
	run := func() []int {
		s := newProxy(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), s3count.Options{})
		s.SetScript(s3count.Script{Seed: 42, Faults: []s3count.Fault{{Kind: s3count.Fault500, Prob: 0.3}}})
		for i := range 50 {
			req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/bob-fleet/k%d", s.URL, i), nil)
			if _, err := get(t, http.DefaultClient, req); err != nil {
				t.Fatal(err)
			}
		}
		var seqs []int
		for _, f := range s.Counts().Faults {
			seqs = append(seqs, f.Seq)
		}
		return seqs
	}
	a, b := run(), run()
	if len(a) == 0 || len(a) == 50 || fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("not replayable or degenerate: %v vs %v", a, b)
	}
}

// TestPlantedList is M1.5's acceptance: against real RustFS, a planted LIST on
// bob-control is counted and AssertNoList fails; GETs and conditional writes
// pass through unharmed.
func TestPlantedList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rs, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rs.Stop(); err != nil {
			t.Error(err)
		}
	})
	p, err := s3count.New(s3count.Options{Backend: rs.URL, Roles: map[string]string{rustfs.RootAccessKey: "orchestrator"}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := s3count.Listen(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	do := func(method, path string, hdr http.Header, body string, want int) {
		t.Helper()
		resp, err := rustfs.Do(ctx, s.URL, rs.Root, method, path, hdr, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %s %s, want %d", method, path, resp.Status, b, want)
		}
	}
	do("PUT", "/bob-control/projects/p1", http.Header{"If-None-Match": {"*"}}, "v1", 200)
	do("PUT", "/bob-control/projects/p1", http.Header{"If-None-Match": {"*"}}, "v2", 412)
	do("GET", "/bob-control/projects/p1", nil, "", 200)

	var clean fakeTB
	s.AssertNoList(&clean, "bob-control", "bob-fleet")
	if len(clean.errs) != 0 {
		t.Fatalf("LIST reported before any was made: %v", clean.errs)
	}

	do("GET", "/bob-control?list-type=2&prefix=projects/", nil, "", 200) // the planted LIST

	c := s.Counts()
	if n := c.Lists("bob-control"); n != 1 {
		t.Fatalf("LIST count on bob-control = %d, want 1; rows %+v", n, c.Rows)
	}
	var tb fakeTB
	s.AssertNoList(&tb, "bob-control", "bob-fleet")
	if len(tb.errs) != 1 || !strings.Contains(tb.errs[0], `LIST on bob-control (prefix "projects/"`) {
		t.Fatalf("AssertNoList did not catch the planted LIST: %v", tb.errs)
	}
	t.Logf("planted LIST caught: %s", tb.errs[0])
}

// With ignore-cond, RustFS sees an unconditional write: the second create
// succeeds instead of 412, which is what a non-compliant backend looks like.
func TestIgnoreCondAgainstRustFS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rs, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rs.Stop(); err != nil {
			t.Error(err)
		}
	})
	p, err := s3count.New(s3count.Options{Backend: rs.URL, Secrets: map[string]string{rustfs.RootAccessKey: rustfs.RootSecretKey}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := s3count.Listen(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	s.SetScript(s3count.Script{Faults: []s3count.Fault{{Kind: s3count.FaultIgnoreCond}}})
	for i, want := range []int{200, 200} {
		resp, err := rustfs.Do(ctx, s.URL, rs.Root, "PUT", "/bob-fleet/x", http.Header{"If-None-Match": {"*"}}, []byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("write %d: %s %s, want %d", i, resp.Status, b, want)
		}
	}
}
