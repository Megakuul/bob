package promote_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/megakuul/bob/internal/api"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/internal/promote"
	"github.com/megakuul/bob/internal/store"
	"github.com/megakuul/bob/internal/store/s3client"
	"github.com/megakuul/bob/internal/store/sweep"
	"github.com/megakuul/bob/pkg/engine/nar"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/rustfs"
)

// env is RustFS with bob's buckets, a signed fleet (orchestrator key, one worker) and the
// cache side of one orchestrator.
type env struct {
	t                *testing.T
	ctx              context.Context
	url              string
	control, fleet   *store.Bucket
	obj              *cache.Objects
	vis              *cache.VisStore
	keys             *promote.Keys
	orchKey, wrkKey  ed25519.PrivateKey
	logs             *syncBuf
	srv              *httptest.Server
	region, endpoint string
	// upKey signs the upstream narinfos of skipped paths (the promoters' UpstreamKeys).
	upKey *cache.SecretKey
	// outputs overrides the package outputs of a job's evaluation report (job id → paths);
	// by default every staged path is an output.
	outputs map[string][]*path
	// noUpstream lists skipped paths whose upstream narinfo stage leaves out.
	noUpstream map[*path]bool
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newEnv(t *testing.T) *env {
	t.Helper()
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	s, err := rustfs.Start(ctx, rustfs.Options{Dir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	t.Setenv("AWS_ACCESS_KEY_ID", rustfs.RootAccessKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", rustfs.RootSecretKey)
	t.Setenv("AWS_REGION", "us-east-1")
	e := &env{t: t, ctx: ctx, url: s.URL, logs: &syncBuf{}, region: "us-east-1", endpoint: s.URL,
		outputs: map[string][]*path{}, noUpstream: map[*path]bool{}}
	if e.upKey, err = cache.GenerateKey("upstream-test-1"); err != nil {
		t.Fatal(err)
	}
	open := func(name string) *store.Bucket {
		b, err := store.Open(ctx, store.BucketConfig{Endpoint: s.URL, Region: "us-east-1", Name: name, Role: name})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	e.control, e.fleet = open("bob-control"), open("bob-fleet")
	if e.obj, err = cache.OpenObjects(ctx, s3client.Options{Endpoint: s.URL, Region: "us-east-1"}, "bob-artifacts"); err != nil {
		t.Fatal(err)
	}
	e.vis = cache.NewVisStore(e.obj, cache.NixVisPrefix, nil)
	e.keys = &promote.Keys{Control: e.control}
	if err := e.keys.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	var orchPub, wrkPub ed25519.PublicKey
	orchPub, e.orchKey, _ = ed25519.GenerateKey(rand.Reader)
	wrkPub, e.wrkKey, _ = ed25519.GenerateKey(rand.Reader)
	if err := store.Create(ctx, e.fleet, &store.FleetConfig{Name: store.ID("cfg"), Keys: store.Set([]store.FleetKey{{Kid: "k1", Pub: orchPub}})}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, e.fleet, &store.WorkerRecord{Wid: store.ID("w-evil1"), SigPub: store.Set([]byte(wrkPub))}); err != nil {
		t.Fatal(err)
	}
	h := cache.NewHandler(cache.Config{Objects: e.obj, Vis: e.vis, Log: slog.New(slog.NewTextHandler(e.logs, nil)),
		Principal: func(r *http.Request) (*api.Principal, error) {
			_, pw, ok := r.BasicAuth()
			switch {
			case !ok:
				return nil, nil
			case pw == "p1-token":
				return &api.Principal{ID: "sa:p1"}, nil
			case pw == "p2-token":
				return &api.Principal{ID: "sa:p2"}, nil
			}
			return nil, errors.New("invalid token")
		},
		Allowed: func(_ context.Context, p *api.Principal, projects []string) (bool, error) {
			for _, pr := range projects {
				if "sa:"+pr == p.ID {
					return true, nil
				}
			}
			return false, nil
		}})
	mux := http.NewServeMux()
	mux.Handle("GET /nix/", h)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) promoter(name string) *promote.Promoter {
	p, err := promote.New(promote.Config{Name: name, Control: e.control, Fleet: e.fleet, Objects: e.obj, Vis: e.vis,
		Keys: e.keys, UpstreamKeys: []string{e.upKey.Public()}, Log: slog.New(slog.NewTextHandler(io.MultiWriter(e.logs, &prefixed{name, e.t}), nil))})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

type prefixed struct {
	name string
	t    *testing.T
}

func (p *prefixed) Write(b []byte) (int, error) {
	p.t.Logf("%s: %s", p.name, bytes.TrimSpace(b))
	return len(b), nil
}

// path is one store path to stage.
type path struct {
	name  string
	hash  string
	files map[string]string // relative file → content ("@<name>" in content = that path's store path)
	refs  []*path
	nar   []byte // filled by stage
}

func (p *path) store() string { return "/nix/store/" + p.hash + "-" + p.name }

func newPath(name string, files map[string]string, refs ...*path) *path {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return &path{name: name, hash: cache.Nix32(b), files: files, refs: refs}
}

func (e *env) narOf(p *path) []byte {
	dir := filepath.Join(e.t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	for f, c := range p.files {
		for _, r := range p.refs {
			c = strings.ReplaceAll(c, "@"+r.name, r.store())
		}
		fp := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			e.t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(f, ".AppImage") {
			mode = 0o755
		}
		if err := os.WriteFile(fp, []byte(c), mode); err != nil {
			e.t.Fatal(err)
		}
	}
	var b bytes.Buffer
	if err := nar.Write(&b, dir, nil); err != nil {
		e.t.Fatal(err)
	}
	return b.Bytes()
}

func zst(t *testing.T, b []byte) []byte {
	var out bytes.Buffer
	w, err := zstd.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}

func narHash(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + cache.Nix32(h[:])
}

func (e *env) put(key string, body []byte) {
	if _, err := e.obj.Put(e.ctx, key, body, "", cache.Cond{}); err != nil {
		e.t.Fatal(err)
	}
}

// job stages paths as job id of build b1 / project and writes its signed job record.
type job struct {
	id, staging string
	paths       []*path
}

func (e *env) stage(id, project string, paths []*path, claimRefs func(*path) []*path) *job {
	return e.stageKinds(id, project, paths, claimRefs, nil, nil)
}

// stageKinds is stage with the declared kinds of COMPLETE.json (A149) and skipped paths.
func (e *env) stageKinds(id, project string, paths []*path, claimRefs func(*path) []*path, kinds map[*path]string, skipped []*path) *job {
	j := &job{id: id, staging: "staging/b1/" + id + "/1/", paths: paths}
	var stores []string
	for _, p := range paths {
		if p.nar == nil {
			p.nar = e.narOf(p)
		}
		refs := p.refs
		if claimRefs != nil {
			refs = claimRefs(p)
		}
		var rs []string
		for _, r := range refs {
			rs = append(rs, strings.TrimPrefix(r.store(), "/nix/store/"))
		}
		claim := fmt.Sprintf("StorePath: %s\nURL: nar/%s.nar.zst\nCompression: zstd\nNarHash: %s\nNarSize: %d\nReferences: %s\n",
			p.store(), p.hash, narHash(p.nar), len(p.nar), strings.Join(rs, " "))
		e.put(j.staging+"nar/"+p.hash+".nar.zst", zst(e.t, p.nar))
		e.put(j.staging+"narinfo/"+p.hash+".narinfo", []byte(claim))
		stores = append(stores, p.store())
	}
	skip := []string{}
	for _, p := range skipped {
		skip = append(skip, p.store())
		if e.noUpstream[p] {
			continue
		}
		// the worker stages the upstream cache's signed narinfo of every path it skips
		if p.nar == nil {
			p.nar = e.narOf(p)
		}
		info := cache.PathInfo{StorePath: p.store(), NarHash: narHash(p.nar), NarSize: uint64(len(p.nar))}
		up := fmt.Sprintf("StorePath: %s\nURL: nar/x.nar.xz\nCompression: xz\nNarHash: %s\nNarSize: %d\nReferences: \nSig: %s\n",
			p.store(), info.NarHash, info.NarSize, e.upKey.Sign(info))
		e.put(j.staging+"upstream/"+p.hash+".narinfo", []byte(up))
	}
	outs := e.outputs[id]
	if outs == nil {
		outs = paths
	}
	var outStores []string
	for _, p := range outs {
		outStores = append(outStores, p.store())
	}
	report, _ := json.Marshal(map[string]any{"v": 1, "pipelines": []any{}, "matched": []string{}, "targets": []string{}, "tests": []string{},
		"packages": []any{map[string]any{"label": "//x:all", "status": "built", "outputs": outStores}}, "errors": []any{}})
	e.put(j.staging+"eval.json", report)
	evalSum := sha256.Sum256(report)
	doc := map[string]any{"v": 1, "job": id, "build": "b1", "project": project, "paths": stores,
		"images": []any{}, "assets": []any{}, "skipped": skip}
	if len(kinds) > 0 {
		k := map[string]string{}
		for p, kind := range kinds {
			k[p.store()] = kind
		}
		doc["kinds"] = k
	}
	complete, _ := json.Marshal(doc)
	e.put(j.staging+"COMPLETE.json", complete)
	sum := sha256.Sum256(complete)
	spec, err := fleet.Encode(store.JobSpec{V: 1, JobID: id, BuildID: "b1", Project: project, Kind: "evalbuild", Staging: j.staging})
	if err != nil {
		e.t.Fatal(err)
	}
	rs, err := fleet.Encode(store.JobResult{V: 1, Status: "ok", Complete: j.staging + "COMPLETE.json", CompSHA: hex.EncodeToString(sum[:]),
		EvalSHA: hex.EncodeToString(evalSum[:])})
	if err != nil {
		e.t.Fatal(err)
	}
	rec := &store.JobRecord{ID: store.ID(id), Spec: store.Set(spec), SpecSig: store.Set(fleet.SignSpec(e.orchKey, spec)), Kid: store.Set[string]("k1"), State: store.Set[string]("succeeded"),
		Owner: store.Set[string]("w-evil1"), Epoch: store.Set[int64](1), Attempt: store.Set[int64](1), Changed: store.Set(time.Now()), Result: store.Set(rs), ResultSig: store.Set(fleet.SignResult(e.wrkKey, id, 1, rs))}
	if err := store.Create(e.ctx, e.fleet, rec); err != nil {
		e.t.Fatal(err)
	}
	if err := store.ActiveJobs(e.fleet, 16).Add(e.ctx, id, store.ActiveJob{Build: "b1"}); err != nil {
		e.t.Fatal(err)
	}
	return j
}

func (e *env) record(id string) *store.JobRecord {
	r, err := store.Get(e.ctx, e.fleet, &store.JobRecord{ID: store.ID(id)})
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) get(path, token string, hdr map[string]string) (int, http.Header, []byte) {
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if token != "" {
		req.SetBasicAuth("bob", token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, r.Header, b
}

func (e *env) narinfo(p *path) *cache.NarInfo {
	code, _, body := e.get("/nix/"+p.hash+".narinfo", "p1-token", nil)
	if code != http.StatusOK {
		e.t.Fatalf("narinfo of %s: HTTP %d", p.name, code)
	}
	ni, err := cache.ParseNarInfo(body)
	if err != nil {
		e.t.Fatal(err)
	}
	return ni
}

func (e *env) exists(key string) bool {
	_, err := e.obj.Head(e.ctx, key)
	return err == nil
}

func chain() (a, b, c *path) {
	a = newPath("bob-a", map[string]string{"share/a.txt": "hello a\n", "bin/x": "#!/bin/sh\n"})
	b = newPath("bob-b", map[string]string{"b.txt": "uses @bob-a/share/a.txt\n"}, a)
	c = newPath("bob-c", map[string]string{"c.txt": "uses @bob-b and @bob-a\n"}, a, b)
	return
}

// TestPromoteServeAndSubstitute: promotion of a 3-path job, then the real Nix client
// substitutes the closure through the /nix/ handler and verifies it with bob's key alone.
func TestPromoteServeAndSubstitute(t *testing.T) {
	e := newEnv(t)
	a, b, c := chain()
	j := e.stage("j1", "p1", []*path{c, b, a}, nil)
	if err := e.promoter("o1").Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if pm := e.record("j1").Promotion.Value(); pm != promote.Done {
		t.Fatalf("promotion = %q, want done", pm)
	}
	if !strings.Contains(e.logs.String(), "promoted 3") {
		t.Errorf("no `promoted 3` log line")
	}
	// R-STORE-105: staged objects deleted by known key
	for _, p := range []*path{a, b, c} {
		for _, k := range []string{j.staging + "nar/" + p.hash + ".nar.zst", j.staging + "narinfo/" + p.hash + ".narinfo"} {
			if e.exists(k) {
				t.Errorf("staged %s still exists", k)
			}
		}
	}
	if e.exists(j.staging + "COMPLETE.json") {
		t.Errorf("COMPLETE.json still exists")
	}
	pub, err := e.keys.PublicKeys(e.ctx)
	if err != nil || len(pub) != 1 {
		t.Fatalf("public keys %v %v", pub, err)
	}
	ni := e.narinfo(c)
	if len(ni.Sigs) != 1 || !cache.Verify(pub[0], ni.Sigs[0], ni.Info()) || len(ni.References) != 2 {
		t.Fatalf("narinfo of c: %+v", ni)
	}

	// visibility and credentials (SPEC §6.6.1)
	for _, tc := range []struct {
		token string
		want  int
	}{{"", 404}, {"p2-token", 404}, {"wrong", 401}, {"p1-token", 200}} {
		if code, h, _ := e.get("/nix/"+c.hash+".narinfo", tc.token, nil); code != tc.want {
			t.Errorf("narinfo with %q: HTTP %d, want %d", tc.token, code, tc.want)
		} else if code == 200 && h.Get("Cache-Control") != "private, max-age=300" {
			t.Errorf("Cache-Control %q", h.Get("Cache-Control"))
		} else if code == 401 && h.Get("WWW-Authenticate") != `Basic realm="bob"` {
			t.Errorf("WWW-Authenticate %q", h.Get("WWW-Authenticate"))
		}
	}
	if code, _, body := e.get("/nix/nix-cache-info", "", nil); code != 200 || string(body) != "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n" {
		t.Errorf("nix-cache-info: %d %q", code, body)
	}
	if code, _, _ := e.get("/nix/nix-cache-info", "wrong", nil); code != 401 {
		t.Errorf("nix-cache-info with a wrong token: %d", code)
	}
	if code, _, _ := e.get("/nix/"+strings.Repeat("e", 32)+".narinfo", "p1-token", nil); code != 404 {
		t.Errorf("bad hash: %d", code)
	}
	if code, _, body := e.get("/nix/"+a.hash+".ls", "p1-token", nil); code != 200 || !json.Valid(body) || !strings.Contains(string(body), `"a.txt":{"type":"regular","size":8,"narOffset":`) {
		t.Errorf(".ls: %d %s", code, body)
	}
	// download proxy: single range, multi range, past the end
	nk := "/nix/" + ni.URL
	if code, h, body := e.get(nk, "p1-token", map[string]string{"Range": "bytes=0-9"}); code != 206 || len(body) != 10 || !strings.HasPrefix(h.Get("Content-Range"), "bytes 0-9/") {
		t.Errorf("range: %d %d %q", code, len(body), h.Get("Content-Range"))
	}
	if code, _, body := e.get(nk, "p1-token", map[string]string{"Range": "bytes=0-1,4-5"}); code != 200 || uint64(len(body)) != ni.FileSize {
		t.Errorf("multi range: %d %d", code, len(body))
	}
	if code, _, _ := e.get(nk, "p1-token", map[string]string{"Range": fmt.Sprintf("bytes=%d-", ni.FileSize+10)}); code != 416 {
		t.Errorf("range past the end: %d", code)
	}

	// public visibility: anonymous access within the cache TTL (fresh store here)
	if err := e.vis.SetPublic(e.ctx, c.hash, "p1.0000000001", true); err != nil {
		t.Fatal(err)
	}
	if code, h, _ := e.get("/nix/"+c.hash+".narinfo", "", nil); code != 200 || h.Get("Cache-Control") != "public, max-age=300" {
		t.Errorf("public narinfo anonymously: %d %q", code, h.Get("Cache-Control"))
	}

	// real Nix client
	n := clients.Nix(t)
	root := n.Dir("root")
	netrc := filepath.Join(n.Dir("netrc"), "netrc")
	if err := os.WriteFile(netrc, []byte("machine 127.0.0.1 login bob password p1-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.Own(netrc)
	run := func(args ...string) clients.Result {
		return n.RunEnv([]string{"NIX_STORE_DIR=/nix/store"}, args...)
	}
	r := run("copy", "--from", e.srv.URL+"/nix", "--to", "local?root="+root, "--option", "trusted-public-keys", pub[0],
		"--option", "netrc-file", netrc, "--option", "narinfo-cache-negative-ttl", "0", c.store())
	if r.Exit != 0 {
		t.Fatalf("nix copy: %s", r.Combined())
	}
	r = run("store", "verify", "--store", "local?root="+root, "--trusted-public-keys", pub[0], c.store(), b.store(), a.store())
	if r.Exit != 0 {
		t.Fatalf("nix store verify: %s", r.Combined())
	}
	got, err := os.ReadFile(filepath.Join(root, "nix/store", b.hash+"-bob-b", "b.txt"))
	if err != nil || string(got) != "uses "+a.store()+"/share/a.txt\n" {
		t.Errorf("substituted content: %q %v", got, err)
	}
}

// TestTwoPromotersOneJob: two orchestrators promote the same job at once; one reports
// `promoted N`, the other `alreadyThere N` (R-REG-152 (1)).
func TestTwoPromotersOneJob(t *testing.T) {
	e := newEnv(t)
	a, b, c := chain()
	e.stage("j1", "p1", []*path{a, b, c}, nil)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, name := range []string{"o1", "o2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = e.promoter(name).Promote(e.ctx, "j1")
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "promoted 3") || !strings.Contains(logs, "alreadyThere 3") {
		t.Errorf("logs lack `promoted 3` and `alreadyThere 3`:\n%s", logs)
	}
	if pm := e.record("j1").Promotion.Value(); pm != promote.Done {
		t.Fatalf("promotion = %q", pm)
	}
}

// TestTamperedNARFails: a staged NAR replaced after upload → failed: artifact verification,
// the path and its dependents stay 404, the security event names the worker (R-REG-002 (3)).
func TestTamperedNARFails(t *testing.T) {
	e := newEnv(t)
	a, b, c := chain()
	j := e.stage("j1", "p1", []*path{a, b, c}, nil)
	e.put(j.staging+"nar/"+b.hash+".nar.zst", []byte("replaced after upload"))
	if err := e.promoter("o1").Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if pm := e.record("j1").Promotion.Value(); pm != promote.Failed {
		t.Fatalf("promotion = %q, want %q", pm, promote.Failed)
	}
	for _, p := range []*path{b, c} {
		if e.exists(cache.NarInfoKey(p.hash)) {
			t.Errorf("narinfo of %s is served", p.name)
		}
	}
	if !e.exists(cache.NarInfoKey(a.hash)) {
		t.Errorf("the good path a was not served")
	}
	found := false
	for _, l := range strings.Split(e.logs.String(), "\n") {
		if strings.Contains(l, "artifact verification") && strings.Contains(l, "w-evil1") {
			found = true
		}
	}
	if !found {
		t.Errorf("no security log line with the worker id")
	}
}

// TestClaimOmittingReferenceFails: R-REG-152 (4).
func TestClaimOmittingReferenceFails(t *testing.T) {
	e := newEnv(t)
	a, b, _ := chain()
	e.stage("j1", "p1", []*path{a, b}, func(p *path) []*path { return nil })
	if err := e.promoter("o1").Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if pm := e.record("j1").Promotion.Value(); pm != promote.Failed {
		t.Fatalf("promotion = %q, want %q", pm, promote.Failed)
	}
	if e.exists(cache.NarInfoKey(b.hash)) {
		t.Errorf("b is served although its claim hides a reference")
	}
}

// TestNonReproducibleKeepsFirst: same path, different bytes → non_reproducible, the first
// publication stays (R-REG-152 (3)); a second project with the same bytes gains visibility.
func TestNonReproducibleKeepsFirst(t *testing.T) {
	e := newEnv(t)
	x := newPath("bob-x", map[string]string{"x": "first\n"})
	e.stage("j1", "p1", []*path{x}, nil)
	p := e.promoter("o1")
	if err := p.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	first := e.narinfo(x)
	x2 := &path{name: x.name, hash: x.hash, files: map[string]string{"x": "second\n"}}
	e.stage("j2", "p1", []*path{x2}, nil)
	same := &path{name: x.name, hash: x.hash, nar: x.nar}
	e.stage("j3", "p2", []*path{same}, nil)
	if err := p.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if r := e.record("j2"); r.Promotion.Value() != promote.Done || len(r.NonReproducible.Value()) != 1 || r.NonReproducible.Value()[0] != x.store() {
		t.Fatalf("j2: pm %q nr %v", r.Promotion.Value(), r.NonReproducible.Value())
	}
	if again := e.narinfo(x); again.NarHash != first.NarHash || again.Sigs[0] != first.Sigs[0] {
		t.Errorf("first publication changed")
	}
	if r := e.record("j3"); r.Promotion.Value() != promote.Done || len(r.NonReproducible.Value()) != 0 {
		t.Fatalf("j3: pm %q nr %v", r.Promotion.Value(), r.NonReproducible.Value())
	}
	if code, _, _ := e.get("/nix/"+x.hash+".narinfo", "p2-token", nil); code != 200 {
		t.Errorf("p2 does not see the path it also built: %d", code)
	}
}

// TestKeyRotation: R-REG-153 — a new path carries only the new key; after retirement every
// narinfo verifies with the new key alone and the old key is not public any more.
func TestKeyRotation(t *testing.T) {
	e := newEnv(t)
	old, err := e.keys.PublicKeys(e.ctx)
	if err != nil || len(old) != 1 {
		t.Fatalf("keys %v %v", old, err)
	}
	a := newPath("bob-old", map[string]string{"f": "old\n"})
	e.stage("j1", "p1", []*path{a}, nil)
	p := e.promoter("o1")
	if err := p.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	svc := &promote.SettingsService{Keys: e.keys}
	nk, err := e.keys.Create(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := newPath("bob-new", map[string]string{"f": "new\n"})
	e.stage("j2", "p1", []*path{b}, nil)
	if err := p.Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if ni := e.narinfo(b); len(ni.Sigs) != 1 || !cache.Verify(nk.Public, ni.Sigs[0], ni.Info()) {
		t.Errorf("new path sigs %v", ni.Sigs)
	}
	oldName, _, _ := strings.Cut(old[0], ":")
	if _, err := e.keys.SetState(e.ctx, nk.Name, promote.KeyRetiring, promote.KeyVerifyOnly); !errors.Is(err, promote.ErrKeyActive) {
		t.Errorf("retiring the active key: %v", err)
	}
	if _, err := e.keys.SetState(e.ctx, oldName, promote.KeyRetiring, promote.KeyVerifyOnly); err != nil {
		t.Fatal(err)
	}
	lister, err := sweep.NewNarInfos(e.ctx, e.endpoint, e.region, "bob-artifacts")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RetireKeys(e.ctx, lister); err != nil {
		t.Fatal(err)
	}
	for _, x := range []*path{a, b} {
		ni := e.narinfo(x)
		if len(ni.Sigs) != 1 || !cache.Verify(nk.Public, ni.Sigs[0], ni.Info()) {
			t.Errorf("%s after retirement: sigs %v", x.name, ni.Sigs)
		}
	}
	pub, _ := e.keys.PublicKeys(e.ctx)
	if len(pub) != 1 || pub[0] != nk.Public {
		t.Errorf("public keys after retirement: %v", pub)
	}
	_ = svc
}

// TestBlockedWithoutComplete: no COMPLETE.json → blocked: staging incomplete.
func TestBlockedWithoutComplete(t *testing.T) {
	e := newEnv(t)
	a := newPath("bob-a", map[string]string{"f": "a\n"})
	j := e.stage("j1", "p1", []*path{a}, nil)
	if err := e.obj.Delete(e.ctx, j.staging+"COMPLETE.json"); err != nil {
		t.Fatal(err)
	}
	if err := e.promoter("o1").Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if pm := e.record("j1").Promotion.Value(); pm != promote.Blocked {
		t.Fatalf("promotion = %q", pm)
	}
}

// TestDiscardRefsKinds: outputs that discard references by design (A149, SPEC §6.6.4) may hold
// store hashes of their own job's closure as data (image config PATH/Entrypoint, an AppImage's
// interpreter inside the zstd squashfs); a foreign hash still fails, an ordinary output keeps
// the strict scan, and a declared kind must match the path's name and layout.
func TestDiscardRefsKinds(t *testing.T) {
	e := newEnv(t)
	glibc := newPath("glibc-2.42-100", map[string]string{"lib/ld-linux-aarch64.so.1": "ld\n"})
	golink := newPath("go-link-api", map[string]string{"bin/api": "ELF interp @glibc-2.42-100/lib/ld-linux-aarch64.so.1\n"}, glibc)
	image := func(name string, roots ...*path) (img, layer *path) {
		layer = newPath("oci-layer-1a2b3c4d-"+name, map[string]string{"layer.json": `{"paths":["` + name + `"]}` + "\n", "layer.tar.gz": "\x1f\x8b gz"})
		cfg := `{"config":{"Env":["PATH=`
		for _, r := range roots {
			cfg += "@" + r.name + "/bin:"
		}
		cfg += `/usr/bin"],"Entrypoint":["@` + roots[0].name + `/bin/api"]}}`
		img = newPath("oci-image-"+name+"-image-1.4.5", map[string]string{"oci-layout": `{"imageLayoutVersion":"1.0.0"}`,
			"index.json": `{"manifests":[]}`, "blobs/sha256/" + strings.Repeat("c", 64): cfg}, append([]*path{layer}, roots...)...)
		return img, layer
	}
	onlyLayer := func(layer *path) func(*path) []*path {
		return func(p *path) []*path {
			if strings.HasPrefix(p.name, "oci-image-") {
				return []*path{layer}
			}
			return p.refs
		}
	}

	// j0 (another project) publishes a path the image of j2 names without having it in its job
	foreign := newPath("secret-tool", map[string]string{"bin/api": "secret\n"})
	e.stage("j0", "p2", []*path{foreign}, nil)

	// j1: image with PATH/Entrypoint store paths inside the job's closure passes
	img1, layer1 := image("api", golink)
	e.stageKinds("j1", "p1", []*path{img1, layer1, golink, glibc}, onlyLayer(layer1),
		map[*path]string{img1: promote.KindOCIImage, layer1: promote.KindOCIImage}, nil)

	// j2: image naming a served path of another project (outside its closure) fails
	golink2 := newPath("go-link-api", map[string]string{"bin/api": "api\n"})
	img2, layer2 := image("api2", foreign)
	img2.refs = []*path{layer2, foreign}
	e.stageKinds("j2", "p1", []*path{img2, layer2, golink2}, onlyLayer(layer2),
		map[*path]string{img2: promote.KindOCIImage, layer2: promote.KindOCIImage}, nil)

	// j3: the same image content without a declared kind is an ordinary output: strict scan
	golink3 := newPath("go-link-api", map[string]string{"bin/api": "api\n"})
	img3, layer3 := image("api3", golink3)
	e.stageKinds("j3", "p1", []*path{img3, layer3, golink3}, onlyLayer(layer3), nil, nil)

	// j4: a declared kind that does not match the path (an ordinary program) fails
	golink4 := newPath("go-link-api", map[string]string{"bin/api": "api\n"})
	tool4 := newPath("bob-tool", map[string]string{"bin/tool": "runs @go-link-api/bin/api\n"}, golink4)
	e.stageKinds("j4", "p1", []*path{tool4, golink4}, func(*path) []*path { return nil },
		map[*path]string{tool4: promote.KindOCIImage}, nil)

	// j5: AppImage with the interpreter path inside the zstd-compressed squashfs passes; the
	// interpreter (glibc) is a skipped path of the job (served by cache.nixos.org)
	glibc5 := newPath("glibc-2.42-100", nil)
	golink5 := newPath("go-link-api", map[string]string{"bin/api": "api\n"})
	launch := `{"interp":"` + glibc5.store() + `/lib/ld-linux-aarch64.so.1","exec":"` + golink5.store() + `/bin/api"}`
	// mksquashfs -comp zstd emits raw (not entropy-coded) literals for such blocks (I-B22 found
	// the glibc path at offset 9105927 of a real AppImage); the encoder is told to do the same
	var sq bytes.Buffer
	zw, err := zstd.NewWriter(&sq, zstd.WithNoEntropyCompression(true))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zw.Write([]byte(launch + strings.Repeat("\x00", 64)))
	_ = zw.Close()
	squash := sq.Bytes()
	if !bytes.Contains(squash, []byte(glibc5.hash)) {
		t.Fatalf("precondition: zstd did not keep the store hash as a raw literal")
	}
	elf := "\x7fELF\x02\x01\x01\x00AI\x02" + strings.Repeat("\x00", 53) + "hsqs" + string(squash)
	app5 := newPath("appimage-api-appimage", map[string]string{"api-aarch64.AppImage": elf})
	e.stageKinds("j5", "p1", []*path{app5, golink5}, func(*path) []*path { return nil },
		map[*path]string{app5: "appimage.bundle"}, []*path{glibc5})

	if err := e.promoter("o1").Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ job, want, log string }{
		{"j0", promote.Done, ""},
		{"j1", promote.Done, ""},
		{"j2", promote.Failed, "the oci.image output contains the store hash " + foreign.hash + ", which is not in this job's closure"},
		{"j3", promote.Failed, "the NAR references " + golink3.hash + ", which the claim does not list"},
		{"j4", promote.Failed, "COMPLETE.json declares kind oci.image, but the path is not an oci-image- or oci-layer- output"},
		{"j5", promote.Done, ""},
	} {
		if pm := e.record(tc.job).Promotion.Value(); pm != tc.want {
			t.Errorf("%s: promotion = %q, want %q", tc.job, pm, tc.want)
		}
		if tc.log != "" && !strings.Contains(e.logs.String(), tc.log) {
			t.Errorf("%s: no log line with %q", tc.job, tc.log)
		}
	}
	// the found hashes are data, not references: the image references only its layer
	if ni := e.narinfo(img1); len(ni.References) != 1 || ni.References[0] != layer1.store() {
		t.Errorf("image references %v, want only its layer", ni.References)
	}
	if ni := e.narinfo(app5); len(ni.References) != 0 {
		t.Errorf("AppImage references %v, want none", ni.References)
	}
	// the foreign path's image never became visible
	if code, _, _ := e.get("/nix/"+img2.hash+".narinfo", "p1-token", nil); code != http.StatusNotFound {
		t.Errorf("rejected image narinfo: HTTP %d", code)
	}
}

// TestParseCompleteKinds: COMPLETE.json kinds must name staged paths and discarding kinds.
func TestParseCompleteKinds(t *testing.T) {
	p := "/nix/store/" + strings.Repeat("a", 32) + "-oci-image-api"
	doc := func(kinds string) []byte {
		return []byte(`{"v":1,"job":"j","build":"b","project":"p","paths":["` + p + `"],"images":[],"assets":[],"skipped":[],"kinds":` + kinds + `}`)
	}
	if c, err := promote.ParseComplete(doc(`{"`+p+`":"oci.image"}`), "j", "b", "p"); err != nil || c.Kinds[p] != "oci.image" {
		t.Fatalf("valid kinds: %v %v", c, err)
	}
	for _, bad := range []string{
		`{"` + p + `":"golang.binary"}`,
		`{"/nix/store/` + strings.Repeat("b", 32) + `-x":"oci.image"}`,
		`{"` + p + `":"appimage."}`,
	} {
		if _, err := promote.ParseComplete(doc(bad), "j", "b", "p"); err == nil {
			t.Errorf("kinds %s accepted", bad)
		}
	}
}

// failJob turns the staged job id into a failed job: result status failed, naming its
// COMPLETE.json (withComplete) or none. complete, if set, replaces the staged COMPLETE.json.
func (e *env) failJob(j *job, withComplete bool, complete []byte) {
	if complete != nil {
		e.put(j.staging+"COMPLETE.json", complete)
	}
	res := store.JobResult{V: 1, Status: "failed", Failed: []string{"//cmd/api:test"}, Error: "BOB-E-2005 test failed"}
	if rep, _, err := e.obj.Read(e.ctx, j.staging+"eval.json", 4<<20); err == nil {
		sum := sha256.Sum256(rep)
		res.EvalSHA = hex.EncodeToString(sum[:])
	}
	if withComplete {
		body, _, err := e.obj.Read(e.ctx, j.staging+"COMPLETE.json", promote.MaxComplete)
		if err != nil {
			e.t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		res.Complete, res.CompSHA = j.staging+"COMPLETE.json", hex.EncodeToString(sum[:])
	}
	rs, err := fleet.Encode(res)
	if err != nil {
		e.t.Fatal(err)
	}
	err = store.Change(e.ctx, e.fleet, &store.JobRecord{ID: store.ID(j.id)}, func(cur *store.JobRecord) (*store.JobRecord, error) {
		next := *cur
		next.State, next.Result, next.ResultSig = store.Set[string]("failed"), store.Set(rs), store.Set(fleet.SignResult(e.wrkKey, j.id, 1, rs))
		return &next, nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

// TestFailedJobPromotesBuiltPaths (A200): the closure of the packages that built in a failed
// job is verified and promoted like a succeeded job's; a failed job without COMPLETE.json
// awaits nothing; a failed job that stages images fails verification.
func TestFailedJobPromotesBuiltPaths(t *testing.T) {
	e := newEnv(t)
	a, b, _ := chain()
	j := e.stage("j1", "p1", []*path{b, a}, nil)
	e.failJob(j, true, nil)
	if !promote.Awaits(e.record("j1")) {
		t.Fatalf("a failed job with COMPLETE.json does not await promotion")
	}
	if err := e.promoter("o1").Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if pm := e.record("j1").Promotion.Value(); pm != promote.Done {
		t.Fatalf("promotion = %q, want done", pm)
	}
	if ni := e.narinfo(b); len(ni.References) != 1 {
		t.Errorf("narinfo of b: %+v", ni)
	}

	c := newPath("bob-c2", map[string]string{"f": "c\n"})
	j2 := e.stage("j2", "p1", []*path{c}, nil)
	e.failJob(j2, false, nil)
	if promote.Awaits(e.record("j2")) {
		t.Errorf("a failed job without COMPLETE.json awaits promotion")
	}
	if err := e.promoter("o1").Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if pm := e.record("j2").Promotion.Value(); pm != promote.Pending {
		t.Errorf("failed job without COMPLETE.json: promotion = %q", pm)
	}
	if code, _, _ := e.get("/nix/"+c.hash+".narinfo", "p1-token", nil); code != http.StatusNotFound {
		t.Errorf("path of a failed job without COMPLETE.json served: HTTP %d", code)
	}

	d := newPath("bob-d", map[string]string{"f": "d\n"})
	j3 := e.stage("j3", "p1", []*path{d}, nil)
	withImage, _ := json.Marshal(map[string]any{"v": 1, "job": "j3", "build": "b1", "project": "p1", "paths": []string{d.store()},
		"images": []any{map[string]any{"repo": "p1/app", "tag": "b-1", "manifest": "sha256:" + strings.Repeat("a", 64),
			"mediaType": "application/vnd.oci.image.index.v1+json", "blobs": []string{}}},
		"assets": []any{}, "skipped": []string{}})
	e.failJob(j3, true, withImage)
	if err := e.promoter("o1").Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	if pm := e.record("j3").Promotion.Value(); pm != promote.Failed {
		t.Errorf("failed job with images: promotion = %q, want %q", pm, promote.Failed)
	}
}
