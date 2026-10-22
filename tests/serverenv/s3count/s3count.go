// Package s3count is the counting and fault-injecting S3 proxy of HARNESS.md
// §8.1. It sits in front of RustFS, classifies every request by bucket, key
// prefix and operation, attributes it to a credential (from the SigV4 access
// key) and to a sweep controller (User-Agent suffix bob-sweep/<controller>,
// R-STORE-101), and injects seeded, replayable faults.
//
// D60 forbids LIST on the hot paths: every server e2e test ends with
// AssertNoList(t, "bob-control", "bob-fleet").
//
// Per-role counters: Options.Roles (exact access key) and Options.RoleOf (e.g. a key
// prefix) name the role of each credential; Counts.Roles and Counts.ByRole report
// the requests per role.
//
// Counters are served at GET /_s3count (JSON) and reset with DELETE /_s3count.
// A fault script is installed with PUT /_s3count/faults or SetScript. Bucket
// names cannot start with "_", so these paths never shadow S3.
package s3count

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/megakuul/bob/tests/serverenv/s3sig"
)

// Operations.
const (
	OpGet         = "GET"
	OpHead        = "HEAD"
	OpPut         = "PUT"
	OpDelete      = "DELETE"
	OpPost        = "POST"
	OpList        = "LIST"         // object listings: list-type=2, v1, ?versions, ?uploads
	OpListBuckets = "LIST_BUCKETS" // GET /
	OpBucket      = "BUCKET"       // bucket-level calls that list nothing (?location, PUT bucket, ...)
)

// Class is the classification of one request.
type Class struct {
	Bucket string
	Key    string
	Prefix string // first key segment including "/" (or the key); a LIST's prefix parameter
	Op     string
}

// listParams are the query parameters of a plain object listing; a bucket GET
// with only these (or none) is ListObjects v1.
var listParams = map[string]bool{
	"prefix": true, "delimiter": true, "marker": true, "max-keys": true,
	"encoding-type": true, "continuation-token": true, "start-after": true,
	"fetch-owner": true, "list-type": true, "x-id": true,
}

// Classify classifies a path-style S3 request.
func Classify(r *http.Request) Class {
	p := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(p, "/")
	c := Class{Bucket: bucket, Key: key, Prefix: prefixOf(key)}
	q := r.URL.Query()
	switch {
	case bucket == "":
		c.Op = OpListBuckets
		if r.Method != http.MethodGet {
			c.Op = r.Method
		}
	case key == "":
		c.Op = OpBucket
		if r.Method == http.MethodGet && isListing(q) {
			c.Op = OpList
			c.Prefix = q.Get("prefix")
		}
	default:
		c.Op = r.Method
	}
	return c
}

func isListing(q url.Values) bool {
	if q.Has("list-type") || q.Has("versions") || q.Has("uploads") {
		return true
	}
	for k := range q {
		if !listParams[k] {
			return false // a sub-resource such as ?location or ?policy
		}
	}
	return true
}

func prefixOf(key string) string {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i+1]
	}
	return key
}

// Row is one counter.
type Row struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
	Op     string `json:"op"`
	Cred   string `json:"cred"`            // role name from Options.Roles, else the access key
	Sweep  string `json:"sweep,omitempty"` // controller from the bob-sweep/<controller> User-Agent suffix
	Count  int    `json:"count"`
}

// Counts is the JSON document of GET /_s3count.
type Counts struct {
	Rows   []Row      `json:"rows"`
	Faults []Injected `json:"faults"`
	// Roles is the request count per credential role (Options.Roles / RoleOf; the
	// access key itself for an unmapped key), all buckets and operations.
	Roles map[string]int `json:"roles"`
}

// ByRole returns the requests made with credentials of role matching op ("" = any
// operation) on bucket ("" = any bucket).
func (c Counts) ByRole(role, bucket, op string) int {
	n := 0
	for _, r := range c.Rows {
		if r.Cred == role && (bucket == "" || r.Bucket == bucket) && (op == "" || r.Op == op) {
			n += r.Count
		}
	}
	return n
}

// Lists returns the LIST requests on bucket that no sweep controller made.
func (c Counts) Lists(bucket string) int {
	n := 0
	for _, r := range c.Rows {
		if r.Op == OpList && r.Bucket == bucket && r.Sweep == "" {
			n += r.Count
		}
	}
	return n
}

// Fault kinds.
const (
	Fault500        = "500"
	Fault503        = "503"
	FaultReset      = "reset"       // connection reset, request not forwarded
	FaultLost       = "lost"        // request forwarded and applied, response dropped
	FaultStripETag  = "strip-etag"  // ETag removed from the response
	FaultIgnoreCond = "ignore-cond" // If-Match / If-None-Match removed (non-compliant backend, BOB-E-7007)
	FaultLatency    = "latency"     // added delay, then forwarded
)

// Fault is one rule of a fault script. Empty match fields match anything.
type Fault struct {
	Kind      string        `json:"kind"`
	Op        string        `json:"op,omitempty"`
	Bucket    string        `json:"bucket,omitempty"`
	KeyPrefix string        `json:"key_prefix,omitempty"`
	Cred      string        `json:"cred,omitempty"`
	Prob      float64       `json:"prob,omitempty"`  // probability per matching request; 0 means always
	Limit     int           `json:"limit,omitempty"` // injections at most; 0 means unlimited
	Delay     time.Duration `json:"delay,omitempty"` // for FaultLatency
}

// Script is a seeded list of faults; the same seed and request sequence give
// the same injections, so a chaos failure is replayable.
type Script struct {
	Seed   uint64  `json:"seed"`
	Faults []Fault `json:"faults"`
}

// Injected records one injected fault.
type Injected struct {
	Seq    int    `json:"seq"` // request sequence number since the last reset
	Kind   string `json:"kind"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Cred   string `json:"cred"`
}

// Options configure a Proxy.
type Options struct {
	Backend string            // RustFS base URL
	Roles   map[string]string // access key → role name (e.g. "worker", "orchestrator")
	// RoleOf maps an access key to a role name when Roles has no entry (e.g. by key
	// prefix); "" means unmapped. Row.Cred and Counts.Roles use the role.
	RoleOf  func(accessKey string) string
	Secrets map[string]string // access key → secret, for faults that rewrite signed headers
}

type rowKey struct{ bucket, prefix, op, cred, sweep string }

// Proxy is the counting proxy. It is an http.Handler.
type Proxy struct {
	opt     Options
	backend *url.URL
	client  *http.Client

	mu       sync.Mutex
	seq      int
	counts   map[rowKey]int
	script   Script
	used     []int
	rng      *rand.Rand
	injected []Injected
}

// New returns a proxy for o.Backend.
func New(o Options) (*Proxy, error) {
	u, err := url.Parse(o.Backend)
	if err != nil {
		return nil, fmt.Errorf("s3count: backend: %w", err)
	}
	p := &Proxy{
		opt: o, backend: u,
		client: &http.Client{
			Transport: &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 64},
			// Redirects are the client's business, never the proxy's.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		counts: map[rowKey]int{},
	}
	p.SetScript(Script{})
	return p, nil
}

// SetScript installs a fault script and restarts its random sequence.
func (p *Proxy) SetScript(s Script) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.script = s
	p.used = make([]int, len(s.Faults))
	p.rng = rand.New(rand.NewPCG(s.Seed, s.Seed^0x9e3779b97f4a7c15))
}

// Reset clears the counters and the injection log; the script stays.
func (p *Proxy) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq = 0
	p.counts = map[rowKey]int{}
	p.injected = nil
}

// Counts returns a snapshot of the counters, sorted.
func (p *Proxy) Counts() Counts {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := Counts{Rows: []Row{}, Faults: append([]Injected{}, p.injected...), Roles: map[string]int{}}
	for k, n := range p.counts {
		c.Roles[k.cred] += n
		c.Rows = append(c.Rows, Row{Bucket: k.bucket, Prefix: k.prefix, Op: k.op, Cred: k.cred, Sweep: k.sweep, Count: n})
	}
	sort.Slice(c.Rows, func(i, j int) bool {
		a, b := c.Rows[i], c.Rows[j]
		return fmt.Sprint(a.Bucket, a.Prefix, a.Op, a.Cred, a.Sweep) < fmt.Sprint(b.Bucket, b.Prefix, b.Op, b.Cred, b.Sweep)
	})
	return c
}

// TB is the part of testing.TB the assertions need.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
}

// AssertNoList fails t for every LIST on the given buckets that no sweep
// controller made (D60, R-STORE-004/101).
func (p *Proxy) AssertNoList(t TB, buckets ...string) {
	t.Helper()
	assertNoList(t, p.Counts(), buckets)
}

// AssertNoList is Proxy.AssertNoList for a proxy in another process (devstack).
func AssertNoList(ctx context.Context, t TB, proxyURL string, buckets ...string) {
	t.Helper()
	c, err := Fetch(ctx, proxyURL)
	if err != nil {
		t.Errorf("s3count: %v", err)
		return
	}
	assertNoList(t, c, buckets)
}

func assertNoList(t TB, c Counts, buckets []string) {
	t.Helper()
	for _, b := range buckets {
		for _, r := range c.Rows {
			if r.Op == OpList && r.Bucket == b && r.Sweep == "" {
				t.Errorf("s3count: %d LIST on %s (prefix %q, cred %s) outside a sweep controller (D60)", r.Count, b, r.Prefix, r.Cred)
			}
		}
	}
}

// Fetch reads the counters of a proxy at proxyURL.
func Fetch(ctx context.Context, proxyURL string) (Counts, error) {
	var c Counts
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL+"/_s3count", nil)
	if err != nil {
		return c, fmt.Errorf("s3count: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return c, fmt.Errorf("s3count: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return c, fmt.Errorf("s3count: GET /_s3count: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return c, fmt.Errorf("s3count: decode counts: %w", err)
	}
	return c, nil
}

// ServeHTTP counts, maybe injects a fault, and forwards.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_s3count" || r.URL.Path == "/_s3count/faults" {
		p.serveControl(w, r)
		return
	}
	c := Classify(r)
	ak := s3sig.AccessKey(r)
	cred := ak
	if role, ok := p.opt.Roles[ak]; ok {
		cred = role
	} else if p.opt.RoleOf != nil {
		if role := p.opt.RoleOf(ak); role != "" {
			cred = role
		}
	}
	sweep := ""
	if _, after, ok := strings.Cut(r.UserAgent(), "bob-sweep/"); ok {
		sweep, _, _ = strings.Cut(after, " ")
	}

	p.mu.Lock()
	p.seq++
	seq := p.seq
	p.counts[rowKey{c.Bucket, c.Prefix, c.Op, cred, sweep}]++
	f := p.pick(c, cred)
	if f != nil {
		p.injected = append(p.injected, Injected{Seq: seq, Kind: f.Kind, Method: r.Method, Path: r.URL.RequestURI(), Cred: cred})
	}
	p.mu.Unlock()

	kind := ""
	if f != nil {
		kind = f.Kind
	}
	switch kind {
	case Fault500, Fault503:
		code := http.StatusInternalServerError
		if kind == Fault503 {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(code)
		fmt.Fprintf(w, "<Error><Code>%s</Code><Message>s3count injected fault</Message></Error>", http.StatusText(code))
		return
	case FaultReset:
		abort(w)
		return
	case FaultLatency:
		select {
		case <-time.After(f.Delay):
		case <-r.Context().Done():
			return
		}
	case FaultIgnoreCond:
		if r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != "" {
			if err := p.stripConditional(r, ak); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
	}

	resp, err := p.forward(r)
	if err != nil {
		http.Error(w, "s3count: backend: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if kind == FaultLost {
		// The backend applied the request; the client must not learn the result.
		_, _ = io.Copy(io.Discard, resp.Body)
		abort(w)
		return
	}
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	if kind == FaultStripETag {
		w.Header().Del("ETag")
	}
	w.WriteHeader(resp.StatusCode)
	// A copy error means the client went away; there is nobody left to tell.
	_, _ = io.Copy(w, resp.Body)
}

// pick returns the fault for this request, if any. Called with p.mu held.
func (p *Proxy) pick(c Class, cred string) *Fault {
	for i := range p.script.Faults {
		f := &p.script.Faults[i]
		if (f.Op != "" && f.Op != c.Op) || (f.Bucket != "" && f.Bucket != c.Bucket) ||
			(f.KeyPrefix != "" && !strings.HasPrefix(c.Key, f.KeyPrefix)) || (f.Cred != "" && f.Cred != cred) {
			continue
		}
		if f.Limit > 0 && p.used[i] >= f.Limit {
			continue
		}
		// Draw for every matching rule so the sequence depends only on the
		// requests, not on which rule fired before.
		if f.Prob > 0 && p.rng.Float64() >= f.Prob {
			continue
		}
		p.used[i]++
		return f
	}
	return nil
}

func (p *Proxy) forward(r *http.Request) (*http.Response, error) {
	u := *p.backend
	u.Path = r.URL.Path
	u.RawPath = r.URL.RawPath
	u.RawQuery = r.URL.RawQuery
	out, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), r.Body)
	if err != nil {
		return nil, fmt.Errorf("s3count: %w", err)
	}
	out.Header = r.Header.Clone()
	// SigV4 signs the Host header the client used, so forward it unchanged.
	out.Host = r.Host
	out.ContentLength = r.ContentLength
	if r.ContentLength == 0 {
		out.Body = http.NoBody
	}
	resp, err := p.client.Do(out)
	if err != nil {
		return nil, fmt.Errorf("s3count: %w", err)
	}
	return resp, nil
}

// stripConditional removes the conditional headers and re-signs the request
// with the known secret, since the headers are signed.
func (p *Proxy) stripConditional(r *http.Request, ak string) error {
	sk, ok := p.opt.Secrets[ak]
	if !ok {
		return fmt.Errorf("s3count: ignore-cond fault needs the secret of %q", ak)
	}
	hash := r.Header.Get("X-Amz-Content-Sha256")
	if strings.HasPrefix(hash, "STREAMING-") {
		return errors.New("s3count: ignore-cond fault cannot re-sign a streaming payload")
	}
	region := "us-east-1"
	if _, rest, ok := strings.Cut(r.Header.Get("Authorization"), "Credential="); ok {
		if parts := strings.Split(strings.SplitN(rest, ",", 2)[0], "/"); len(parts) >= 3 {
			region = parts[2]
		}
	}
	date, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		date = time.Now()
	}
	r.Header.Del("If-Match")
	r.Header.Del("If-None-Match")
	s3sig.Sign(r, s3sig.Creds{AccessKey: ak, SecretKey: sk, Region: region}, hash, date)
	return nil
}

// abort closes the client connection without a response (a reset).
func abort(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		fail502(w)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		fail502(w)
		return
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0) // RST instead of FIN
	}
	_ = conn.Close()
}

func fail502(w http.ResponseWriter) {
	http.Error(w, "s3count: cannot reset this connection", http.StatusBadGateway)
}

func (p *Proxy) serveControl(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/_s3count" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.Counts())
	case r.URL.Path == "/_s3count" && r.Method == http.MethodDelete:
		p.Reset()
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/_s3count/faults" && r.Method == http.MethodPut:
		var s Script
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err == nil {
			err = json.NewDecoder(bytes.NewReader(b)).Decode(&s)
		}
		if err != nil {
			http.Error(w, "s3count: bad script: "+err.Error(), http.StatusBadRequest)
			return
		}
		p.SetScript(s)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "s3count: unknown control request", http.StatusNotFound)
	}
}

// Server is a proxy listening on loopback.
type Server struct {
	*Proxy
	URL string
	srv *http.Server
	ln  net.Listener
	err chan error
}

// Listen serves p on 127.0.0.1:port.
func Listen(p *Proxy, port int) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("s3count: listen: %w", err)
	}
	s := &Server{Proxy: p, URL: "http://" + ln.Addr().String(), ln: ln, err: make(chan error, 1),
		srv: &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}}
	go func() { s.err <- s.srv.Serve(ln) }()
	return s, nil
}

// Close stops the listener and waits for in-flight requests.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.srv.Shutdown(ctx)
	if serr := <-s.err; serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		err = errors.Join(err, serr)
	}
	s.client.CloseIdleConnections()
	if err != nil {
		return fmt.Errorf("s3count: %w", err)
	}
	return nil
}
