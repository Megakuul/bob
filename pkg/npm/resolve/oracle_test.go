package resolve

// Oracle comparison against npm itself (R-NPM-001a evidence before `bob lock` exists, A78).
// Opt-in: it needs the network (or a warm snapshot) and the pinned npm:
//
//	BOB_NPM_ORACLE=/path/to/nodejs-24/bin  BOB_NPM_SNAPSHOT=/var/tmp/npm-snapshot \
//	  go test ./pkg/npm/resolve -run TestOracle -v
//
// A caching proxy in front of registry.npmjs.org serves the same abbreviated documents to npm
// and to bob (like tools/regmirror); documents are kept in BOB_NPM_SNAPSHOT so reruns compare
// against one registry state. The comparison is the one of tests/eco/npm/resolve_test.go.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type ogDep struct {
	name, req string
	dev       bool
	alias     string
}

type ogImporter struct {
	dir, kind   string
	deps, peers []ogDep
}

type ogGraph struct {
	name      string
	importers []ogImporter
	peerSplit bool
	fails     bool // npm fails with ERESOLVE; bob must fail too
}

func od(n, r string) ogDep     { return ogDep{name: n, req: r} }
func odd(n, r string) ogDep    { return ogDep{name: n, req: r, dev: true} }
func oal(a, n, r string) ogDep { return ogDep{name: n, req: r, alias: a} }
func ogGraphs() []ogGraph {
	return []ogGraph{
		{name: "vite-react", importers: []ogImporter{{dir: "web", kind: "vite", deps: []ogDep{
			od("react", "^19"), od("react-dom", "^19"), odd("vite", "^7.1.0"), odd("@vitejs/plugin-react", "^5"),
			odd("typescript", "~5.9.2"), odd("@types/react", "^19"), odd("@types/react-dom", "^19")}}}},
		{name: "ts-lib", importers: []ogImporter{{dir: "lib", kind: "lib", deps: []ogDep{
			od("zod", "^4.0.0"), odd("typescript", "~5.9.2"), odd("@types/node", "^24"), odd("vitest", "^3.2.0"),
			odd("eslint", "^9"), odd("typescript-eslint", "^8")}}}},
		{name: "fastify", importers: []ogImporter{{dir: "api", kind: "app", deps: []ogDep{
			od("fastify", "^5"), od("@fastify/cors", "^11"), od("pino-pretty", "^13"), odd("esbuild", "^0.25.0")}}}},
		{name: "native", importers: []ogImporter{{dir: "native", kind: "app", deps: []ogDep{
			od("better-sqlite3", "^12.2.0"), od("esbuild", "^0.25.0"), oal("lodash4", "lodash", "^4.17.21"),
			odd("@types/better-sqlite3", "^7.6.0")}}}},
		{name: "mono", importers: []ogImporter{
			{dir: ".", kind: "app", deps: []ogDep{odd("npm", "^11")}},
			{dir: "packages/ui", kind: "lib", deps: []ogDep{odd("typescript", "~5.9.2")}, peers: []ogDep{od("svelte", "^5")}},
			{dir: "apps/web", kind: "vite", deps: []ogDep{od("svelte", "^5"), odd("vite", "^7.1.0"), odd("@sveltejs/vite-plugin-svelte", "^6")}},
		}},
		{name: "stress", importers: []ogImporter{{dir: "stress", kind: "app", deps: []ogDep{
			odd("jest", "^29"), odd("webpack", "^5"), odd("eslint", "^8"), odd("@babel/core", "^7"),
			odd("ts-jest", "^29"), odd("@babel/preset-env", "^7"), odd("babel-jest", "^29"), odd("webpack-cli", "^5")}}}},
		{name: "sveltekit", importers: []ogImporter{{dir: "kit", kind: "vite", deps: []ogDep{
			od("svelte", "^5"), odd("@sveltejs/kit", "^2"), odd("@sveltejs/adapter-static", "^3"),
			odd("@sveltejs/vite-plugin-svelte", "^6"), odd("vite", "^7.1.0"), odd("typescript", "~5.9.2")}}}},
		{name: "vite-svelte", importers: []ogImporter{{dir: "site", kind: "vite", deps: []ogDep{
			od("svelte", "^5"), odd("vite", "^7.1.0"), odd("@sveltejs/vite-plugin-svelte", "^6")}}}},
		{name: "bun-app", importers: []ogImporter{{dir: "hono", kind: "app", deps: []ogDep{od("hono", "^4"), od("zod", "^4")}}}},
		{name: "peersplit", peerSplit: true, importers: []ogImporter{
			{dir: ".", kind: "app", deps: []ogDep{od("react", "^18"), od("use-sync-external-store", "^1")}},
			{dir: "b", kind: "app", deps: []ogDep{od("react", "^19"), od("use-sync-external-store", "^1")}},
		}},
	}
}

func (g ogGraph) input() Input {
	var in Input
	for _, im := range g.importers {
		x := Importer{Dir: im.dir}
		if im.kind == "lib" {
			x.Name = "@graph/" + path.Base(im.dir)
		}
		conv := func(ds []ogDep) []Dep {
			var out []Dep
			for _, d := range ds {
				dep := Dep{Alias: d.name, Req: d.req, Dev: d.dev}
				if d.alias != "" {
					dep = Dep{Alias: d.alias, Name: d.name, Req: d.req, Dev: d.dev}
				}
				out = append(out, dep)
			}
			return out
		}
		x.Deps, x.Peers = conv(im.deps), conv(im.peers)
		in.Importers = append(in.Importers, x)
	}
	return in
}

// snapshotProxy serves abbreviated packuments (and per-version documents) from a disk snapshot,
// filling it from the upstream registry on a miss.
type snapshotProxy struct {
	dir      string
	upstream string
	mu       sync.Mutex
}

func (s *snapshotProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.EscapedPath()
	if strings.Contains(p, "/-/") { // tarballs (npm cracks open bundles): pass through
		http.Redirect(w, r, s.upstream+p, http.StatusFound)
		return
	}
	accept := acceptAbbreviated
	segs := strings.Split(strings.Trim(strings.Replace(p, "%2f", "/", 1), "/"), "/")
	if n := 1 + btoi(strings.HasPrefix(segs[0], "@")); len(segs) > n {
		accept = acceptFull // per-version document
	}
	h := sha256.Sum256([]byte(p))
	file := filepath.Join(s.dir, hex.EncodeToString(h[:12])+".json")
	s.mu.Lock()
	b, err := os.ReadFile(file)
	s.mu.Unlock()
	if err != nil {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, s.upstream+p, nil)
		req.Header.Set("Accept", accept)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			http.NotFound(w, r)
			return
		}
		if resp.StatusCode != http.StatusOK {
			http.Error(w, resp.Status, http.StatusBadGateway)
			return
		}
		b, err = io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.mu.Lock()
		_ = os.MkdirAll(s.dir, 0o755)
		_ = os.WriteFile(file, b, 0o644)
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

type ogLock struct {
	Packages map[string]struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Link         bool              `json:"link"`
		InBundle     bool              `json:"inBundle"`
		Deps         map[string]string `json:"dependencies"`
		DevDeps      map[string]string `json:"devDependencies"`
		OptionalDeps map[string]string `json:"optionalDependencies"`
		PeerDeps     map[string]string `json:"peerDependencies"`
		PeerMeta     map[string]struct {
			Optional bool `json:"optional"`
		} `json:"peerDependenciesMeta"`
	} `json:"packages"`
}

type ogRes struct{ pkgs, edges map[string]bool }

// npmRes is npmResolution of tests/eco/npm/resolve_test.go; withDev also counts importers'
// devDependencies (the test reads only dependencies and optionalDependencies of importers).
func npmRes(t *testing.T, raw []byte, importers []string, withDev bool) ogRes {
	var l ogLock
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	r := ogRes{map[string]bool{}, map[string]bool{}}
	nameOf := func(key string) string {
		if p := l.Packages[key]; p.Name != "" {
			return p.Name
		}
		_, n, _ := strings.Cut(key[strings.LastIndex(key, "node_modules/"):], "/")
		return n
	}
	lookup := func(loc, dep string) (string, bool) {
		for {
			cand := path.Join(loc, "node_modules", dep)
			if loc == "" {
				cand = "node_modules/" + dep
			}
			if p, ok := l.Packages[cand]; ok && !p.InBundle {
				if p.Link {
					return "", false
				}
				return cand, true
			}
			if loc == "" {
				return "", false
			}
			if i := strings.LastIndex(loc, "/node_modules/"); i >= 0 {
				loc = loc[:i]
			} else {
				loc = ""
			}
		}
	}
	edges := func(from, loc string, deps ...map[string]string) {
		for _, m := range deps {
			for alias := range m {
				if to, ok := lookup(loc, alias); ok {
					r.edges[from+"|"+alias+"|"+nameOf(to)+"@"+l.Packages[to].Version] = true
				}
			}
		}
	}
	for key, p := range l.Packages {
		if p.Link || p.InBundle || !strings.Contains(key, "node_modules/") {
			continue
		}
		from := nameOf(key) + "@" + p.Version
		r.pkgs[from] = true
		peers := map[string]string{}
		for n := range p.PeerDeps {
			if !p.PeerMeta[n].Optional {
				peers[n] = ""
			}
		}
		for n := range p.PeerMeta {
			if _, ok := p.PeerDeps[n]; ok {
				peers[n] = ""
			}
		}
		edges(from, key, p.Deps, p.OptionalDeps, peers)
	}
	for _, dir := range importers {
		key := dir
		if dir == "." {
			key = ""
		}
		p := l.Packages[key]
		if withDev {
			edges("importer:"+dir, key, p.Deps, p.OptionalDeps, p.DevDeps, p.PeerDeps)
		} else {
			edges("importer:"+dir, key, p.Deps, p.OptionalDeps)
		}
	}
	return r
}

func bobRes(s Section) ogRes {
	r := ogRes{map[string]bool{}, map[string]bool{}}
	id := func(key string) string {
		if strings.HasPrefix(key, "link:") {
			return key
		}
		p := s.Packages[key]
		return p.Name + "@" + p.Version
	}
	for _, p := range s.Packages {
		r.pkgs[p.Name+"@"+p.Version] = true
		for alias, to := range p.Deps {
			r.edges[p.Name+"@"+p.Version+"|"+alias+"|"+id(to)] = true
		}
	}
	for dir, im := range s.Importers {
		for alias, to := range im.Deps {
			r.edges["importer:"+dir+"|"+alias+"|"+id(to)] = true
		}
	}
	return r
}

func ogDiff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func oraclePkgJSON(im ogImporter) map[string]any {
	deps, dev, peers := map[string]string{}, map[string]string{}, map[string]string{}
	for _, x := range im.deps {
		spec, name := x.req, x.name
		if x.alias != "" {
			name, spec = x.alias, "npm:"+x.name+"@"+x.req
		}
		if x.dev {
			dev[name] = spec
		} else {
			deps[name] = spec
		}
	}
	for _, x := range im.peers {
		peers[x.name] = x.req
	}
	m := map[string]any{"name": "@graph/" + strings.ReplaceAll(filepath.Base(im.dir), ".", "root"), "version": "0.0.0", "private": true}
	if len(deps) > 0 {
		m["dependencies"] = deps
	}
	if len(dev) > 0 {
		m["devDependencies"] = dev
	}
	if len(peers) > 0 {
		m["peerDependencies"] = peers
	}
	return m
}

func runNpm(t *testing.T, bin string, g ogGraph, registry string) []byte {
	b, err := tryNpm(t, bin, g, registry)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func tryNpm(t *testing.T, bin string, g ogGraph, registry string) ([]byte, error) {
	dir := t.TempDir()
	var ws []string
	root := map[string]any{"name": "oracle-root", "version": "0.0.0", "private": true}
	for _, im := range g.importers {
		pj := oraclePkgJSON(im)
		if im.dir == "." {
			root = pj
			continue
		}
		ws = append(ws, im.dir)
		b, _ := json.MarshalIndent(pj, "", "  ")
		_ = os.MkdirAll(filepath.Join(dir, im.dir), 0o755)
		_ = os.WriteFile(filepath.Join(dir, im.dir, "package.json"), b, 0o644)
	}
	if len(ws) > 0 {
		root["workspaces"] = ws
	}
	b, _ := json.MarshalIndent(root, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "package.json"), b, 0o644)
	cmd := exec.Command(filepath.Join(bin, "npm"), "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund", "--registry", registry)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + dir, "npm_config_update_notifier=false",
		"npm_config_cache=" + filepath.Join(dir, ".npm-cache")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("npm: %v\n%s", err, out)
	}
	return os.ReadFile(filepath.Join(dir, "package-lock.json"))
}

func TestOracle(t *testing.T) {
	bin := os.Getenv("BOB_NPM_ORACLE")
	if bin == "" {
		t.Skip("set BOB_NPM_ORACLE to the bin directory of the pinned nodejs to compare with npm")
	}
	snap := os.Getenv("BOB_NPM_SNAPSHOT")
	if snap == "" {
		snap = t.TempDir()
	}
	srv := httptest.NewServer(&snapshotProxy{dir: snap, upstream: "https://registry.npmjs.org"})
	defer srv.Close()
	reg := &Registry{URL: srv.URL + "/"}
	only := os.Getenv("BOB_NPM_ORACLE_GRAPH")
	graphs := ogGraphs()
	if os.Getenv("BOB_NPM_ORACLE_EXTRA") != "" {
		graphs = append(graphs, ogExtraGraphs()...)
	}
	for _, g := range graphs {
		if only != "" && g.name != only {
			continue
		}
		t.Run(g.name, func(t *testing.T) {
			start := time.Now()
			res, err := Resolve(context.Background(), g.input(), Options{Registry: reg, NodeVersion: "24.21.0"})
			if g.fails {
				_, nerr := tryNpm(t, bin, g, srv.URL+"/")
				if err == nil || nerr == nil || !strings.Contains(nerr.Error(), "ERESOLVE") {
					t.Fatalf("want ERESOLVE from both: bob %v; npm %v", err, nerr)
				}
				t.Logf("bob: %v", err)
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			took := time.Since(start)
			lock := runNpm(t, bin, g, srv.URL+"/")
			if dump := os.Getenv("BOB_NPM_ORACLE_DUMP"); dump != "" {
				_ = os.MkdirAll(dump, 0o755)
				_ = os.WriteFile(filepath.Join(dump, g.name+".npm.json"), lock, 0o644)
				b, _ := json.MarshalIndent(res.Section, "", "  ")
				_ = os.WriteFile(filepath.Join(dump, g.name+".bob.json"), b, 0o644)
			}
			var dirs []string
			for _, im := range g.importers {
				dirs = append(dirs, im.dir)
			}
			bob := bobRes(res.Section)
			for _, withDev := range []bool{false, true} {
				want := npmRes(t, lock, dirs, withDev)
				skip := func(e string) bool {
					return g.peerSplit && strings.Contains(e, "use-sync-external-store") ||
						!withDev && strings.HasPrefix(e, "importer:")
				}
				if !withDev {
					if d := ogDiff(want.pkgs, bob.pkgs); len(d) > 0 {
						t.Errorf("npm resolves packages bob does not: %v", d)
					}
					if d := ogDiff(bob.pkgs, want.pkgs); len(d) > 0 {
						t.Errorf("bob resolves packages npm does not: %v", d)
					}
				}
				for _, e := range ogDiff(want.edges, bob.edges) {
					if !skip(e) {
						t.Errorf("edge npm has and bob lacks (withDev=%v): %s", withDev, e)
					}
				}
				for _, e := range ogDiff(bob.edges, want.edges) {
					if !skip(e) {
						t.Errorf("edge bob has and npm lacks (withDev=%v): %s", withDev, e)
					}
				}
				if !withDev {
					t.Logf("%s: %d packages, %d edges, %d instances, resolve %v, %d warnings", g.name, len(bob.pkgs), len(want.edges), len(res.Section.Packages), took.Round(time.Millisecond), len(res.Warnings))
				}
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ogExtraGraphs are further graphs (peer-heavy, old, large, workspace conflicts) used to check
// the Arborist port beyond the SPEC corpus.
func ogExtraGraphs() []ogGraph {
	app := func(name string, deps ...ogDep) ogGraph {
		return ogGraph{name: name, importers: []ogImporter{{dir: "app", kind: "app", deps: deps}}}
	}
	return []ogGraph{
		app("next", od("next", "^14"), od("react", "^18"), od("react-dom", "^18"), odd("@testing-library/react", "^14"),
			odd("jest", "^29"), odd("jest-environment-jsdom", "^29"), odd("@types/react", "^18")),
		app("angular", od("@angular/core", "^17"), od("@angular/common", "^17"), od("@angular/platform-browser", "^17"),
			od("@angular/router", "^17"), od("@angular/forms", "^17"), od("rxjs", "~7.8.0"), od("zone.js", "~0.14.0"),
			odd("@angular/cli", "^17"), odd("@angular-devkit/build-angular", "^17"), odd("@angular/compiler-cli", "^17"), odd("typescript", "~5.2.0")),
		app("storybook", od("react", "^18"), od("react-dom", "^18"), odd("storybook", "^8"), odd("@storybook/react-vite", "^8"),
			odd("@storybook/addon-essentials", "^8"), odd("vite", "^5"), odd("@vitejs/plugin-react", "^4")),
		app("airbnb", odd("eslint", "^8"), odd("eslint-config-airbnb", "^19"), odd("eslint-plugin-react", "^7"),
			odd("eslint-plugin-import", "^2"), odd("eslint-plugin-jsx-a11y", "^6"), odd("eslint-plugin-react-hooks", "^4"),
			odd("@typescript-eslint/parser", "^6"), odd("@typescript-eslint/eslint-plugin", "^6"), odd("prettier", "^3"), odd("eslint-config-prettier", "^9")),
		app("nest", od("@nestjs/core", "^10"), od("@nestjs/common", "^10"), od("@nestjs/platform-express", "^10"),
			od("reflect-metadata", "^0.2"), od("rxjs", "^7"), odd("@nestjs/cli", "^10"), odd("@nestjs/testing", "^10"), odd("ts-jest", "^29"), odd("jest", "^29")),
		app("cra", od("react-scripts", "5.0.1"), od("react", "^18"), od("react-dom", "^18"), od("typescript", "^4.9")),
		app("vue", od("vue", "^3"), od("pinia", "^2"), od("vue-router", "^4"), odd("@vitejs/plugin-vue", "^5"), odd("vite", "^5"),
			odd("vitest", "^1"), odd("@vue/test-utils", "^2"), odd("vue-tsc", "^2")),
		app("oldies", odd("mocha", "^5"), odd("chai", "^4"), odd("sinon", "^7"), odd("nyc", "^14"), od("request", "^2"),
			od("express", "^4"), od("gulp", "^4"), od("grunt", "^1")),
		app("gatsby", od("gatsby", "^5"), od("react", "^18"), od("react-dom", "^18")),
		app("rn", od("react-native", "0.74.5"), od("react", "18.2.0"), odd("@babel/core", "^7.20.0"), odd("@react-native/babel-preset", "0.74.87"), odd("@react-native/metro-config", "0.74.87")),
		app("docusaurus", od("@docusaurus/core", "^3"), od("@docusaurus/preset-classic", "^3"), od("react", "^18"), od("react-dom", "^18"), od("@mdx-js/react", "^3")),
		app("nuxt", od("nuxt", "^3"), od("vue", "^3")),
		app("mui", od("@mui/material", "^5"), od("@emotion/react", "^11"), od("@emotion/styled", "^11"), od("react", "^18"), od("react-dom", "^18"), od("@mui/icons-material", "^5")),
		app("typeorm", od("typeorm", "^0.3"), od("pg", "^8"), od("reflect-metadata", "^0.2"), od("sqlite3", "^5")),
		app("vuecli", odd("@vue/cli-service", "^5"), od("vue", "^3"), odd("@vue/cli-plugin-babel", "^5"), odd("@vue/cli-plugin-eslint", "^5"), odd("eslint", "^7"), odd("eslint-plugin-vue", "^8")),
		app("karma", odd("karma", "^6"), odd("karma-jasmine", "^5"), odd("jasmine-core", "^5"), odd("karma-chrome-launcher", "^3"), odd("karma-webpack", "^5"), odd("webpack", "^5")),
		app("electron", odd("electron", "^30"), odd("electron-builder", "^24"), odd("electron-vite", "^2"), od("electron-updater", "^6")),
		app("eslint9", odd("eslint", "^9"), odd("@eslint/js", "^9"), odd("eslint-plugin-react", "^7"), odd("eslint-plugin-react-hooks", "^5"), odd("eslint-plugin-import", "^2"), odd("typescript-eslint", "^8"), odd("globals", "^15"), odd("eslint-plugin-jsx-a11y", "^6"), odd("typescript", "^5")),
		app("cdk", od("aws-cdk-lib", "^2"), od("constructs", "^10"), odd("aws-cdk", "^2"), odd("ts-node", "^10"), odd("typescript", "~5.4")),
		{name: "eresolve", fails: true, importers: []ogImporter{{dir: "app", kind: "app", deps: []ogDep{od("react", "^17"), od("react-dom", "^18")}}}},
		{name: "eresolve-hooks", fails: true, importers: []ogImporter{{dir: "app", kind: "app", deps: []ogDep{od("react", "^18"), od("@testing-library/react-hooks", "^8")}}}},
		{name: "ws-conflict", importers: []ogImporter{
			{dir: ".", kind: "app", deps: []ogDep{od("lodash", "^4"), od("debug", "^2"), od("react", "^18")}},
			{dir: "a", kind: "app", deps: []ogDep{od("lodash", "^3"), od("debug", "^4"), od("react-dom", "^18")}},
			{dir: "b", kind: "app", deps: []ogDep{od("lodash", "^4"), od("react", "^17"), od("react-dom", "^17"), od("mkdirp", "^0.5")}},
			{dir: "c", kind: "lib", deps: []ogDep{od("semver", "^5")}, peers: []ogDep{od("react", ">=16")}},
		}},
	}
}
