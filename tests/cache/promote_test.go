package cache

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/clients"
	"github.com/megakuul/bob/tests/serverenv/regenv"
	"github.com/megakuul/bob/tests/serverenv/s3count"
)

const artifacts = "bob-artifacts"

// awaitStaged waits (the orchestrators' staging reads are delayed, regenv.DelayStaging) until a
// job has written COMPLETE.json and returns the staging prefix "staging/<build>/<job>/<attempt>/".
func awaitStaged(t *testing.T, e *regenv.Env) string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		for _, k := range e.S3List("staging/") {
			if p, ok := strings.CutSuffix(k, "COMPLETE.json"); ok {
				return p
			}
		}
	}
	t.Fatalf("no job staged a COMPLETE.json within 10 minutes")
	return ""
}

// stagedKeys returns the keys below prefix+sub.
func stagedKeys(e *regenv.Env, prefix, sub string) []string {
	return e.S3List(prefix + sub)
}

func hashOfKey(k string) string {
	base := k[strings.LastIndex(k, "/")+1:]
	return base[:strings.Index(base, ".")]
}

// waitPromotion polls the jobs of the project's first build until one has a promotion verdict.
func waitPromotion(t *testing.T, e *regenv.Env, project, want string) regenv.Job {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		for _, b := range e.Builds(project) {
			for _, j := range e.Jobs(b.ID) {
				if j.Promotion == want {
					return j
				}
			}
		}
	}
	t.Fatalf("no job of project %s reached promotion %q within 5 minutes", project, want)
	return regenv.Job{}
}

func startTagged(t *testing.T, e *regenv.Env, project, repo, version string) {
	t.Helper()
	e.Project(project, repo)
	dir := fixtureDir(t)
	e.Push(dir, repo, "main")
	e.Tag(dir, repo, "v"+version)
}

func TestRREG002WorkerCannotReadSigningKeys(t *testing.T) {
	parity.Covers(t, "R-REG-002")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	keys := e.PublicKeys()
	if len(keys) == 0 {
		t.Fatalf("server lists no signing key")
	}
	name, _, _ := strings.Cut(keys[0], ":")
	code, _ := e.S3Do(e.S3Creds("worker"), http.MethodGet, "bob-control", "skey/"+name, nil)
	if code != http.StatusForbidden {
		t.Errorf("(1) worker credential GET bob-control/skey/%s: HTTP %d, want 403", name, code)
	}
	e.AssertNoList()
}

func TestRREG002TamperedStagingIsRejected(t *testing.T) {
	parity.Covers(t, "R-REG-002")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.DelayStaging(20 * time.Second)
	startTagged(t, e, "acme", "demo", "1.4.2")
	prefix := awaitStaged(t, e)

	nars := stagedKeys(e, prefix, "nar/")
	if len(nars) == 0 {
		t.Fatalf("job staged no NAR below %s", prefix)
	}
	victim := hashOfKey(nars[0])
	if code, _ := e.S3Do(e.S3Creds("root"), http.MethodPut, artifacts, nars[0], []byte("replaced after upload")); code/100 != 2 {
		t.Fatalf("harness error: replacing the staged NAR: HTTP %d", code)
	}
	job := waitPromotion(t, e, "acme", "failed: artifact verification")

	// the path and every staged path that references it stay 404
	bad := map[string]bool{victim: true}
	for _, k := range stagedKeys(e, prefix, "narinfo/") {
		_, body := e.S3Do(e.S3Creds("root"), http.MethodGet, artifacts, k, nil)
		for h := range bad {
			if strings.Contains(refsLine(string(body)), h) {
				bad[hashOfKey(k)] = true
			}
		}
	}
	for h := range bad {
		if code, _, _ := e.Get(e.API+"/nix/"+h+".narinfo", e.Admin, nil); code != http.StatusNotFound {
			t.Errorf("(3) tampered path or dependent %s: narinfo HTTP %d, want 404", h, code)
		}
	}
	found := false
	for _, l := range e.OrchestratorLogs() {
		for _, line := range strings.Split(l, "\n") {
			if strings.Contains(line, "artifact verification") && strings.Contains(line, job.WorkerID) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("(3) no orchestrator log line names the security event with worker id %q", job.WorkerID)
	}
	e.AssertNoList()
}

func refsLine(narinfo string) string {
	for _, l := range strings.Split(narinfo, "\n") {
		if v, ok := strings.CutPrefix(l, "References:"); ok {
			return v
		}
	}
	return ""
}

func TestRREG152ClaimOmittingReferenceIsRejected(t *testing.T) {
	parity.Covers(t, "R-REG-152")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.DelayStaging(20 * time.Second)
	startTagged(t, e, "acme", "demo", "1.4.2")
	prefix := awaitStaged(t, e)
	edited := false
	for _, k := range stagedKeys(e, prefix, "narinfo/") {
		_, body := e.S3Do(e.S3Creds("root"), http.MethodGet, artifacts, k, nil)
		if strings.TrimSpace(refsLine(string(body))) == "" {
			continue
		}
		forged := regexp.MustCompile(`(?m)^References:.*$`).ReplaceAllString(string(body), "References:")
		if code, _ := e.S3Do(e.S3Creds("root"), http.MethodPut, artifacts, k, []byte(forged)); code/100 != 2 {
			t.Fatalf("harness error: rewriting the staged narinfo: HTTP %d", code)
		}
		edited = true
		break
	}
	if !edited {
		t.Fatalf("no staged narinfo of the fixture has references; the acceptance needs a path that references another")
	}
	waitPromotion(t, e, "acme", "failed: artifact verification") // (4)
	e.AssertNoList()
}

func TestRREG152TwoOrchestratorsPromoteOneJob(t *testing.T) {
	parity.Covers(t, "R-REG-152")
	e := regenv.Up(t, 2)
	if e == nil {
		return
	}
	// A promotion that takes longer than one pass interval (10 s) overlaps the other orchestrator's
	// pass over the same job; staged reads are delayed for that window.
	e.DelayStaging(12 * time.Second)
	rel, root := publishP1(t, e)
	e.SetFaults(s3count.Script{})
	keys := e.PublicKeys()
	n := clients.Nix(t)
	promoted, already := regexp.MustCompile(`promoted (\d+)`), regexp.MustCompile(`alreadyThere (\d+)`)
	var p, a string
	for _, l := range e.OrchestratorLogs() {
		if m := promoted.FindStringSubmatch(l); m != nil && m[1] != "0" && p == "" {
			p = m[1]
		}
		if m := already.FindStringSubmatch(l); m != nil && a == "" {
			a = m[1]
		}
	}
	if p == "" || a == "" || p != a {
		t.Errorf("(1) orchestrator logs report promoted %q and alreadyThere %q; want one `promoted N` and one `alreadyThere N`", p, a)
	}
	l := newLocal(n)
	if r := l.copyFrom(e.API+"/nix", regenv.Netrc(t, n, e.API, e.Admin), keys, root); r.Exit != 0 {
		t.Fatalf("nix copy: %s", r.Combined())
	}
	if v := l.verify(keys, l.closure(t, root)...); v.Exit != 0 {
		t.Errorf("(1) nix store verify: %s", v.Combined())
	}
	_ = rel
	e.AssertNoList()
}

func TestRREG152NonReproduciblePathKeepsFirstPublication(t *testing.T) {
	parity.Covers(t, "R-REG-152")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	// Two workers build the same input-addressed path with different bytes (fixture
	// "nondet": output contains a nonce); both stage before the first promotion.
	e.DelayStaging(15 * time.Second)
	// Record the first publication of every narinfo at the moment it first appears (a tight
	// poll), so "the first publication is unchanged" compares against what was really first.
	firstSeen := map[string]map[string]string{}
	var fsMu sync.Mutex
	stopPoll := make(chan struct{})
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		for {
			for _, k := range e.S3List("nix/narinfo/") {
				h := hashOfKey(k)
				fsMu.Lock()
				_, ok := firstSeen[h]
				fsMu.Unlock()
				if ok {
					continue
				}
				if code, _, body := e.Get(e.API+"/nix/"+h+".narinfo", e.Admin, nil); code == http.StatusOK {
					fsMu.Lock()
					firstSeen[h] = narinfoFields(string(body))
					fsMu.Unlock()
				}
			}
			select {
			case <-stopPoll:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	defer func() { close(stopPoll); <-pollDone }()
	e.Project("acme", "demo")
	dir := nondetDir(t)
	e.Push(dir, "demo", "feature-a")
	e.Push(dir, "demo", "feature-b")
	builds := e.WaitBuilds("acme", 2, 5*time.Minute)
	var first map[string]string
	var path string
	for _, b := range builds {
		e.WaitBuild(b.ID, 15*time.Minute, regenv.Terminal)
		for _, j := range e.Jobs(b.ID) {
			if len(j.NonReproducible) > 0 {
				path = j.NonReproducible[0]
			}
		}
	}
	if path == "" {
		t.Fatalf("(3) no job lists a non_reproducible path")
	}
	_, _, body := e.Get(e.API+"/nix/"+regenv.StoreHash(path)+".narinfo", e.Admin, nil)
	if first = narinfoFields(string(body)); first["NarHash"] == "" {
		t.Fatalf("(3) the first publication of %s is not served", path)
	}
	fsMu.Lock()
	rec, recorded := firstSeen[regenv.StoreHash(path)]
	fsMu.Unlock()
	if !recorded {
		t.Fatalf("(3) the poll never saw the first publication of %s appear", path)
	}
	if rec["NarHash"] != first["NarHash"] || rec["Sig"] != first["Sig"] {
		t.Errorf("(3) the narinfo of %s changed after its first publication: first seen %v, served after both builds %v", path, rec, first)
	}
	first = rec
	for _, b := range builds { // a later request must still answer the first publication
		e.MustCall(e.Admin, "bob.build.v1.BuildService/RerunBuild", map[string]any{"id": b.ID}, nil)
	}
	time.Sleep(5 * time.Second)
	_, _, again := e.Get(e.API+"/nix/"+regenv.StoreHash(path)+".narinfo", e.Admin, nil)
	if got := narinfoFields(string(again)); got["NarHash"] != first["NarHash"] || got["Sig"] != first["Sig"] {
		t.Errorf("(3) published narinfo of %s changed: %v -> %v", path, first, got)
	}
	e.AssertNoList()
}

func narinfoFields(s string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(l, ": "); ok {
			m[k] = v
		}
	}
	return m
}

func TestRREG005SkippedPathsAreNotServed(t *testing.T) {
	parity.Covers(t, "R-REG-005")
	e := regenv.Up(t, 1)
	if e == nil {
		return
	}
	e.DelayStaging(20 * time.Second)
	startTagged(t, e, "acme", "demo", "1.4.2")
	prefix := awaitStaged(t, e)
	code, body := e.S3Do(e.S3Creds("root"), http.MethodGet, artifacts, prefix+"COMPLETE.json", nil)
	if code != http.StatusOK {
		t.Fatalf("harness error: GET COMPLETE.json: HTTP %d", code)
	}
	var complete struct {
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal(body, &complete); err != nil {
		t.Fatalf("COMPLETE.json: %v", err)
	}
	if len(complete.Skipped) == 0 {
		t.Fatalf("COMPLETE.json.skipped is empty: the closure of the fixture build contains nixpkgs paths (e.g. hello, glibc) that cache.nixos.org has")
	}
	waitPromotion(t, e, "acme", "done")
	for _, p := range complete.Skipped {
		if code, _, _ := e.Get(e.API+"/nix/"+regenv.StoreHash(p)+".narinfo", e.Admin, nil); code != http.StatusNotFound {
			t.Errorf("skipped path %s: narinfo HTTP %d, want 404", p, code)
		}
	}
	e.AssertNoList()
}

// TestRREG005CacheHasNoOutboundHTTPClient is the dependency check of R-REG-005: no outbound
// HTTP client in internal/cache (the server never proxies cache.nixos.org).
func TestRREG005CacheHasNoOutboundHTTPClient(t *testing.T) {
	parity.Covers(t, "R-REG-005")
	root := harness.Root(t)
	banned := map[string]bool{"Client": true, "Get": true, "Head": true, "Post": true, "PostForm": true,
		"DefaultClient": true, "NewRequest": true, "NewRequestWithContext": true, "Transport": true, "DefaultTransport": true}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "internal", "cache"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		httpNames := map[string]bool{}
		for _, im := range f.Imports {
			switch strings.Trim(im.Path.Value, `"`) {
			case "net/http":
				httpNames["http"] = true
				if im.Name != nil {
					httpNames[im.Name.Name] = true
				}
			case "net/http/httputil":
				t.Errorf("%s imports net/http/httputil (reverse proxy)", p)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && httpNames[id.Name] && banned[s.Sel.Name] {
					t.Errorf("%s: %s uses http.%s: internal/cache must have no outbound HTTP client", p, fset.Position(s.Pos()), s.Sel.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
