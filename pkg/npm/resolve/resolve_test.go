package resolve

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// fv is one version of a fake registry package.
type fv struct {
	deps, opt, peers map[string]string
	peerOpt          []string
	os, cpu, libc    []string
	deprecated       string
	engine           string
	bundled          []string
	bin              any
	install          bool
	integrity        string // overrides the computed sha512
}

// fpkg is a fake package: versions plus dist-tags (latest defaults to the highest version).
type fpkg struct {
	versions map[string]fv
	tags     map[string]string
}

type fakeRegistry struct {
	t     *testing.T
	pkgs  map[string]fpkg
	raw   map[string]func(w http.ResponseWriter, host string) // hostile documents by name
	mu    sync.Mutex
	hits  map[string]int
	codes map[int]int
	srv   *httptest.Server
}

func integrityOf(name, v string) string {
	sum := sha512.Sum512([]byte(name + "@" + v))
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func (f *fakeRegistry) versionDoc(name, v string, x fv, host string, full bool) map[string]any {
	base := name[strings.LastIndex(name, "/")+1:]
	integ := x.integrity
	if integ == "" {
		integ = integrityOf(name, v)
	}
	m := map[string]any{"name": name, "version": v,
		"dist": map[string]any{"tarball": "http://" + host + "/" + name + "/-/" + base + "-" + v + ".tgz", "integrity": integ}}
	if x.deps != nil {
		m["dependencies"] = x.deps
	}
	if x.opt != nil {
		m["optionalDependencies"] = x.opt
		deps := map[string]string{}
		for k, val := range x.deps {
			deps[k] = val
		}
		for k, val := range x.opt { // the registry lists optional deps in dependencies too
			deps[k] = val
		}
		m["dependencies"] = deps
	}
	if x.peers != nil {
		m["peerDependencies"] = x.peers
	}
	if len(x.peerOpt) > 0 {
		meta := map[string]any{}
		for _, p := range x.peerOpt {
			meta[p] = map[string]bool{"optional": true}
		}
		m["peerDependenciesMeta"] = meta
	}
	if x.os != nil {
		m["os"] = x.os
	}
	if x.cpu != nil {
		m["cpu"] = x.cpu
	}
	if full && x.libc != nil {
		m["libc"] = x.libc
	}
	if x.deprecated != "" {
		m["deprecated"] = x.deprecated
	}
	if x.engine != "" {
		m["engines"] = map[string]string{"node": x.engine}
	}
	if x.bundled != nil {
		m["bundleDependencies"] = x.bundled
	}
	if x.bin != nil {
		m["bin"] = x.bin
	}
	if x.install {
		m["hasInstallScript"] = true
	}
	return m
}

func newFakeRegistry(t *testing.T, pkgs map[string]fpkg) *fakeRegistry {
	f := &fakeRegistry{t: t, pkgs: pkgs, raw: map[string]func(http.ResponseWriter, string){}, hits: map[string]int{}, codes: map[int]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) url() string { return f.srv.URL + "/" }

func (f *fakeRegistry) count(code int, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[path]++
	f.codes[code]++
}

func (f *fakeRegistry) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/") // %2f is decoded here
	segs := strings.Split(p, "/")
	n := 1
	if strings.HasPrefix(p, "@") {
		n = 2
	}
	if len(segs) < n {
		http.NotFound(w, r)
		return
	}
	name := strings.Join(segs[:n], "/")
	if h := f.raw[name]; h != nil {
		f.count(200, p)
		h(w, r.Host)
		return
	}
	pk, ok := f.pkgs[name]
	if !ok {
		f.count(404, p)
		http.NotFound(w, r)
		return
	}
	var doc any
	if len(segs) == n+1 {
		x, ok := pk.versions[segs[n]]
		if !ok {
			f.count(404, p)
			http.NotFound(w, r)
			return
		}
		doc = f.versionDoc(name, segs[n], x, r.Host, true)
	} else {
		versions := map[string]any{}
		var vs []*Version
		for v, x := range pk.versions {
			versions[v] = f.versionDoc(name, v, x, r.Host, false)
			pv, err := ParseVersion(v, false)
			if err != nil {
				f.t.Fatalf("fake registry: %v", err)
			}
			vs = append(vs, pv)
		}
		tags := map[string]string{}
		for k, v := range pk.tags {
			tags[k] = v
		}
		if _, ok := tags["latest"]; !ok {
			var best *Version
			for _, v := range vs {
				if len(v.Pre) == 0 && (best == nil || v.Compare(best) > 0) {
					best = v
				}
			}
			if best != nil {
				tags["latest"] = best.String()
			}
		}
		doc = map[string]any{"name": name, "dist-tags": tags, "versions": versions}
	}
	b, _ := json.Marshal(doc)
	etag := fmt.Sprintf(`"%x"`, sha512.Sum512(b))[:20] + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		f.count(304, p)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	f.count(200, p)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func v(deps ...string) fv {
	x := fv{}
	if len(deps) > 0 {
		x.deps = map[string]string{}
		for i := 0; i+1 < len(deps); i += 2 {
			x.deps[deps[i]] = deps[i+1]
		}
	}
	return x
}

func pkg(vs map[string]fv) fpkg { return fpkg{versions: vs} }

func resolveWith(t *testing.T, f *fakeRegistry, in Input) (*Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return Resolve(ctx, in, Options{Registry: &Registry{URL: f.url()}, NodeVersion: "24.21.0"})
}

func mustResolve(t *testing.T, f *fakeRegistry, in Input) Section {
	t.Helper()
	res, err := resolveWith(t, f, in)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return res.Section
}

func app(dir string, deps ...Dep) Importer { return Importer{Dir: dir, Deps: deps} }

func dep(name, req string) Dep { return Dep{Alias: name, Req: req} }

func keysOf(m map[string]Package) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func wantResolveError(t *testing.T, err error, contains string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v (%T), want *resolve.Error", err, err)
	}
	if d := e.Diag(); d.Code != 1015 || d.Code.ID() != "BOB-E-1015" {
		t.Errorf("diag code %v, want BOB-E-1015", d.Code)
	}
	if !strings.Contains(err.Error(), contains) {
		t.Errorf("error %q does not mention %q", err, contains)
	}
	if !strings.HasPrefix(err.Error(), "cannot resolve npm dependencies for ") {
		t.Errorf("error %q is not the E-RESOLVE message", err)
	}
	var _ = diag.Code(0)
}

// R-NPM-001a: two importers with different peers get one instance each, keyed with the peer.
func TestPeerSplitInstances(t *testing.T) {
	f := newFakeRegistry(t, map[string]fpkg{
		"react": pkg(map[string]fv{"18.3.1": v(), "19.2.0": v()}),
		"uses":  pkg(map[string]fv{"1.7.0": {peers: map[string]string{"react": "^18 || ^19"}}}),
	})
	s := mustResolve(t, f, Input{Importers: []Importer{
		app(".", dep("react", "^18"), dep("uses", "^1")),
		app("b", dep("react", "^19"), dep("uses", "^1")),
	}})
	root, b := s.Importers["."].Deps["uses"], s.Importers["b"].Deps["uses"]
	if root != "uses@1.7.0(react@18.3.1)" || b != "uses@1.7.0(react@19.2.0)" {
		t.Fatalf("instances %q and %q; packages %v", root, b, keysOf(s.Packages))
	}
	for _, k := range []string{root, b} {
		p := s.Packages[k]
		if len(p.Peers) != 1 || p.Peers[0] != "react" || !strings.Contains(k, "("+p.Deps["react"]+")") {
			t.Errorf("%s: %+v", k, p)
		}
	}
	if len(s.Packages) != 4 {
		t.Errorf("packages %v, want react x2 and uses x2", keysOf(s.Packages))
	}
}

// npm's placement: the first dependent hoists, a conflicting one nests; edges follow
// node_modules resolution, and a duplicated version with different dependencies becomes two
// instances.
func TestHoistingNestingAndDuplicates(t *testing.T) {
	f := newFakeRegistry(t, map[string]fpkg{
		"a": pkg(map[string]fv{"1.0.0": v("c", "^1")}),
		"b": pkg(map[string]fv{"1.0.0": v("c", "^2")}),
		"c": pkg(map[string]fv{"1.0.0": v(), "1.5.0": v(), "2.0.0": v(), "2.1.0": v()}),
		// x@1 nested twice: under p (sees p's y@1) and under q (sees the root's y@2)
		"x": pkg(map[string]fv{"1.0.0": v("y", "*"), "2.0.0": v()}),
		"y": pkg(map[string]fv{"1.0.0": v(), "2.0.0": v()}),
		"p": pkg(map[string]fv{"1.0.0": v("x", "^1", "y", "^1")}),
		"q": pkg(map[string]fv{"1.0.0": v("x", "^1")}),
	})
	s := mustResolve(t, f, Input{Importers: []Importer{
		app("app", dep("a", "^1"), dep("b", "^1"), dep("x", "^2"), dep("y", "^2"), dep("p", "^1"), dep("q", "^1")),
	}})
	if got := s.Packages["a@1.0.0"].Deps["c"]; got != "c@1.5.0" {
		t.Errorf("a → c = %q, want c@1.5.0 (highest ^1, hoisted)", got)
	}
	if got := s.Packages["b@1.0.0"].Deps["c"]; got != "c@2.1.0" {
		t.Errorf("b → c = %q, want c@2.1.0 (nested)", got)
	}
	var xs []string
	for k, p := range s.Packages {
		if p.Name == "x" && p.Version == "1.0.0" {
			xs = append(xs, k+"→"+p.Deps["y"])
		}
	}
	sort.Strings(xs)
	if len(xs) != 2 || !strings.HasSuffix(xs[0], "→y@1.0.0") && !strings.HasSuffix(xs[1], "→y@1.0.0") {
		t.Fatalf("x@1.0.0 instances %v, want two (y@1.0.0 under p, y@2.0.0 under q)", xs)
	}
	for _, x := range xs {
		if !strings.HasPrefix(x, "x@1.0.0_") {
			t.Errorf("instance %s: a duplicated label must carry a disambiguating suffix", x)
		}
	}
}

func TestAliasDistTagExactAndPick(t *testing.T) {
	f := newFakeRegistry(t, map[string]fpkg{
		"lodash": pkg(map[string]fv{"3.10.1": v(), "4.17.21": v(), "4.17.22": {deprecated: "use 4.17.21"}}),
		"tagged": {versions: map[string]fv{"1.0.0": v(), "2.0.0-beta.1": v(), "1.5.0": v()}, tags: map[string]string{"latest": "1.0.0", "next": "2.0.0-beta.1"}},
		"eng":    pkg(map[string]fv{"1.0.0": v(), "1.1.0": {engine: ">=99"}}),
		"exact":  pkg(map[string]fv{"1.0.0": v(), "1.0.1": v()}),
	})
	s := mustResolve(t, f, Input{Importers: []Importer{app("app",
		Dep{Alias: "lodash4", Name: "lodash", Req: "^4"},
		dep("tagged", "^1"), Dep{Alias: "nx", Name: "tagged", Req: "next"},
		dep("eng", "^1"), dep("exact", "1.0.0"),
	)}})
	d := s.Importers["app"].Deps
	want := map[string]string{
		"lodash4": "lodash@4.17.21",      // the deprecated 4.17.22 is picked last
		"tagged":  "tagged@1.0.0",        // latest wins when it satisfies, even below 1.5.0
		"nx":      "tagged@2.0.0-beta.1", // dist-tag
		"eng":     "eng@1.0.0",           // 1.1.0 needs node >=99
		"exact":   "exact@1.0.0",
	}
	for a, k := range want {
		if d[a] != k {
			t.Errorf("%s → %q, want %q", a, d[a], k)
		}
	}
	if p := s.Packages["lodash@4.17.21"]; p.Name != "lodash" || p.Integrity != integrityOf("lodash", "4.17.21") || p.Tarball != "" {
		t.Errorf("lodash record %+v (default tarball must be omitted)", p)
	}
}

func TestOptionalPlatformBundledAndBins(t *testing.T) {
	f := newFakeRegistry(t, map[string]fpkg{
		"tool": pkg(map[string]fv{"1.0.0": {opt: map[string]string{"tool-linux-arm64": "1.0.0", "tool-darwin-x64": "1.0.0", "gone": "^1"},
			deps: map[string]string{"inner": "^1", "shared": "^1"}, bundled: []string{"inner"}, bin: "./bin/tool.js", install: true}}),
		"tool-linux-arm64": pkg(map[string]fv{"1.0.0": {os: []string{"linux"}, cpu: []string{"arm64"}, libc: []string{"glibc"}}}),
		"tool-darwin-x64":  pkg(map[string]fv{"1.0.0": {os: []string{"darwin"}, cpu: []string{"x64"}}}),
		"shared":           pkg(map[string]fv{"1.0.0": v()}),
		"@s/cli":           pkg(map[string]fv{"2.0.0": {bin: map[string]string{"s-cli": "../../etc/x", "@evil/../x": "a.js"}}}),
	})
	s := mustResolve(t, f, Input{Importers: []Importer{app("app", dep("tool", "^1"), Dep{Alias: "@s/cli", Req: "^2", Dev: true})}})
	tool := s.Packages["tool@1.0.0"]
	if _, ok := tool.Deps["inner"]; ok || len(tool.Bundled) != 1 || tool.Bundled[0] != "inner" {
		t.Errorf("bundled dependency must not be resolved: %+v", tool)
	}
	if tool.Bin["tool"] != "bin/tool.js" || !tool.HasInstallScript || tool.Optional {
		t.Errorf("tool record %+v", tool)
	}
	lin := s.Packages["tool-linux-arm64@1.0.0"]
	if !lin.Optional || len(lin.Libc) != 1 || lin.Libc[0] != "glibc" || lin.OS[0] != "linux" || lin.CPU[0] != "arm64" {
		t.Errorf("platform package %+v (all platforms are kept; libc from the per-version document)", lin)
	}
	if _, ok := s.Packages["tool-darwin-x64@1.0.0"]; !ok {
		t.Errorf("darwin package dropped; platform filtering happens at build time")
	}
	if _, ok := tool.Deps["gone"]; ok {
		t.Errorf("a missing optional dependency must be left out")
	}
	cli := s.Packages["@s/cli@2.0.0"]
	if cli.Bin["s-cli"] != "etc/x" || cli.Bin["x"] != "a.js" || len(cli.Bin) != 2 {
		t.Errorf("bins must be normalised like npm-normalize-package-bin: %v", cli.Bin)
	}
	if got := s.Importers["app"].Dev; len(got) != 1 || got[0] != "@s/cli" {
		t.Errorf("dev list %v", got)
	}
}

func TestWorkspaceLibraryAndExtend(t *testing.T) {
	f := newFakeRegistry(t, map[string]fpkg{
		"svelte":  pkg(map[string]fv{"5.0.0": v()}),
		"plugin":  pkg(map[string]fv{"1.0.0": {peers: map[string]string{"svelte": "^5"}}}),
		"phantom": pkg(map[string]fv{"1.0.0": v()}),
		"cors":    pkg(map[string]fv{"1.0.0": v()}),
		"fastify": pkg(map[string]fv{"5.0.0": v()}),
		"helper":  pkg(map[string]fv{"1.0.0": v()}),
	})
	in := Input{
		Importers: []Importer{
			{Dir: "packages/ui", Name: "@demo/ui", Peers: []Dep{dep("svelte", "^5")}, Deps: []Dep{{Alias: "plugin", Req: "^1", Dev: true}}},
			app("apps/web", dep("svelte", "^5"), Dep{Alias: "@demo/ui", Link: "packages/ui"}, dep("cors", "^1"), dep("fastify", "^5")),
		},
		Extend: []Extension{{Package: "cors", Peers: []string{"fastify"}, Deps: []Dep{dep("helper", "^1")}}},
	}
	s := mustResolve(t, f, in)
	web := s.Importers["apps/web"]
	if web.Deps["@demo/ui"] != "link:packages/ui" || web.Deps["svelte"] != "svelte@5.0.0" {
		t.Errorf("apps/web %+v", web)
	}
	ui := s.Importers["packages/ui"]
	if ui.Deps["svelte"] != "svelte@5.0.0" || ui.Deps["plugin"] != "plugin@1.0.0(svelte@5.0.0)" {
		t.Errorf("packages/ui %+v (a library's peers are installed for its own build)", ui)
	}
	cors := s.Packages[web.Deps["cors"]]
	if web.Deps["cors"] != "cors@1.0.0(fastify@5.0.0)" || cors.Deps["fastify"] != "fastify@5.0.0" || cors.Deps["helper"] != "helper@1.0.0" {
		t.Errorf("npm.Extend: cors %q %+v", web.Deps["cors"], cors)
	}
}

// R-NPM-001a (hostile registry input): documents over the cap, invalid names and unknown
// integrity algorithms are rejected with E-RESOLVE.
func TestHostileRegistryDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		doc        func(w http.ResponseWriter, host string)
	}{
		{"oversize", "larger than", func(w http.ResponseWriter, host string) {
			_, _ = io.WriteString(w, `{"name":"evil","dist-tags":{"latest":"1.0.0"},"junk":"`+strings.Repeat("a", 4096)+`","versions":{}}`)
		}},
		{"badname", "invalid dependency name", func(w http.ResponseWriter, host string) {
			fmt.Fprintf(w, `{"name":"evil","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"evil","version":"1.0.0","dependencies":{"../../etc/passwd":"1.0.0"},"dist":{"tarball":"http://%s/evil/-/evil-1.0.0.tgz","integrity":"%s"}}}}`, host, integrityOf("evil", "1.0.0"))
		}},
		{"integrity", "unsupported integrity algorithm", func(w http.ResponseWriter, host string) {
			fmt.Fprintf(w, `{"name":"evil","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"evil","version":"1.0.0","dist":{"tarball":"http://%s/evil/-/evil-1.0.0.tgz","integrity":"md4-AAAAAAAAAAAAAAAAAAAAAA=="}}}}`, host)
		}},
		{"tarball", "invalid tarball URL", func(w http.ResponseWriter, host string) {
			fmt.Fprintf(w, `{"name":"evil","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"evil","version":"1.0.0","dist":{"tarball":"file:///etc/passwd","integrity":"%s"}}}}`, integrityOf("evil", "1.0.0"))
		}},
		{"json", "not valid JSON", func(w http.ResponseWriter, host string) { _, _ = io.WriteString(w, `{"name":`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRegistry(t, map[string]fpkg{})
			f.raw["evil"] = tc.doc
			reg := &Registry{URL: f.url(), MaxBytes: 2048}
			for _, d := range []Dep{dep("evil", "^1"), {Alias: "evil", Req: "^1", Optional: true}} {
				_, err := Resolve(context.Background(), Input{Importers: []Importer{app("app", d)}}, Options{Registry: reg})
				wantResolveError(t, err, tc.want)
			}
		})
	}
	if MaxDocBytes != 128<<20 || (&Registry{}).base() != DefaultRegistry {
		t.Errorf("registry defaults changed")
	}
}

func TestInvalidDeclarations(t *testing.T) {
	f := newFakeRegistry(t, map[string]fpkg{"ok": pkg(map[string]fv{"1.0.0": v()})})
	for _, tc := range []struct {
		in   Input
		want string
	}{
		{Input{Importers: []Importer{app("app", dep("../escape", "^1"))}}, `invalid package name "../escape"`},
		{Input{Importers: []Importer{app("app", dep("ok", "file:../x"))}}, "file and directory dependencies are not supported"},
		{Input{Importers: []Importer{app("app", dep("ok", "github:user/repo"))}}, "git and URL dependencies are not supported"},
		{Input{Importers: []Importer{app("app", dep("ok", "user/repo"))}}, "not supported"},
		{Input{Importers: []Importer{app("../up", dep("ok", "^1"))}}, "invalid importer directory"},
		{Input{Importers: []Importer{app("app", Dep{Alias: "ui", Link: "nowhere"})}}, "does not exist"},
		{Input{Importers: []Importer{app("app", dep("missing", "^1"))}}, "missing is not in the registry"},
		{Input{Importers: []Importer{app("app", dep("ok", "^2"))}}, `no version of ok matches "^2"`},
	} {
		_, err := resolveWith(t, f, tc.in)
		wantResolveError(t, err, tc.want)
	}
}

// npm fails with ERESOLVE when an importer's own dependencies have conflicting peers.
func TestPeerConflictIsAnError(t *testing.T) {
	f := newFakeRegistry(t, map[string]fpkg{
		"react":     pkg(map[string]fv{"17.0.2": v(), "18.3.1": v()}),
		"react-dom": pkg(map[string]fv{"18.3.1": {peers: map[string]string{"react": "^18.3.1"}}}),
	})
	_, err := resolveWith(t, f, Input{Importers: []Importer{app("app", dep("react", "^17"), dep("react-dom", "^18"))}})
	wantResolveError(t, err, "conflicting peer dependency")
}

func TestMetadataCacheAndDeterminism(t *testing.T) {
	f := newFakeRegistry(t, map[string]fpkg{
		"a": pkg(map[string]fv{"1.0.0": v("b", "^1")}),
		"b": pkg(map[string]fv{"1.0.0": {os: []string{"linux"}, libc: []string{"musl"}}}),
	})
	dir := t.TempDir()
	in := Input{Importers: []Importer{app("app", dep("a", "^1"))}}
	run := func(maxAge time.Duration) []byte {
		res, err := Resolve(context.Background(), in, Options{Registry: &Registry{URL: f.url(), CacheDir: dir, MaxAge: maxAge}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res.Section)
		return b
	}
	first := run(0)
	if f.codes[200] != 3 || f.codes[304] != 0 {
		t.Fatalf("cold: %v, want 3 documents (a, b, b@1.0.0)", f.codes)
	}
	second := run(0)
	if f.codes[200] != 3 || f.codes[304] != 3 {
		t.Errorf("warm: %v, want 3 revalidations answered 304", f.codes)
	}
	third := run(time.Hour)
	if f.codes[200]+f.codes[304] != 6 {
		t.Errorf("fresh cache must not touch the registry: %v", f.codes)
	}
	if string(first) != string(second) || string(first) != string(third) {
		t.Errorf("resolution is not deterministic:\n%s\n%s\n%s", first, second, third)
	}
	if !strings.Contains(string(first), `"libc":["musl"]`) {
		t.Errorf("libc missing: %s", first)
	}
	if !strings.HasPrefix(InputsHash(in, ""), "sha256-") {
		t.Errorf("inputs hash format")
	}
}

func TestIntegrityForms(t *testing.T) {
	sha1hex := "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	for _, tc := range []struct{ sri, shasum, want, err string }{
		{integrityOf("a", "1"), "", integrityOf("a", "1"), ""},
		{"sha1-2jmj7l5rSw0yVb/vlWAYkK/YBwk= " + integrityOf("a", "1"), "", integrityOf("a", "1"), ""},
		{"", sha1hex, "sha1-2jmj7l5rSw0yVb/vlWAYkK/YBwk=", ""},
		{"md4-AAAAAAAAAAAAAAAAAAAAAA==", "", "", "unsupported integrity algorithm"},
		{"sha512-AAAA", "", "", "malformed"},
		{"", "", "", "no integrity"},
	} {
		got, err := normalizeIntegrity(tc.sri, tc.shasum)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: err %v, want %q", tc.sri, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: %q %v, want %q", tc.sri, got, err, tc.want)
		}
	}
}

func TestValidName(t *testing.T) {
	for n, ok := range map[string]bool{
		"react": true, "@types/node": true, "JSONStream": true, "lodash.merge": true, "a-b_c": true,
		"": false, "../x": false, "@scope": false, "@/x": false, "a/b": false, ".hidden": false, "_x": false,
		"node_modules": false, "@s/.x": false, "@../x": false, "x y": false, strings.Repeat("a", 215): false,
	} {
		if ValidName(n) != ok {
			t.Errorf("ValidName(%q) = %v", n, !ok)
		}
	}
}
