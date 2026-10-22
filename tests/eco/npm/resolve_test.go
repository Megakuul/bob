package npm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// npmLock is package-lock.json (lockfileVersion 3) as far as the oracle comparison needs it.
type npmLock struct {
	Packages map[string]struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Link         bool              `json:"link"`
		InBundle     bool              `json:"inBundle"`
		Deps         map[string]string `json:"dependencies"`
		OptionalDeps map[string]string `json:"optionalDependencies"`
		PeerDeps     map[string]string `json:"peerDependencies"`
		PeerMeta     map[string]struct {
			Optional bool `json:"optional"`
		} `json:"peerDependenciesMeta"`
	} `json:"packages"`
}

// resolution is a lock reduced to what both resolvers must agree on: the name@version set and
// the edges (instance → dependency), plus the direct dependencies of each importer.
type resolution struct {
	pkgs  map[string]bool
	edges map[string]bool // "from@v|alias|to@v"
}

// bobResolution reads the npm section of bob.lock.
func bobResolution(l lockNpm) resolution {
	r := resolution{pkgs: map[string]bool{}, edges: map[string]bool{}}
	id := func(key string) string {
		if strings.HasPrefix(key, "link:") {
			return key
		}
		p := l.Packages[key]
		return p.Name + "@" + p.Version
	}
	for _, p := range l.Packages {
		r.pkgs[p.Name+"@"+p.Version] = true
	}
	for _, p := range l.Packages {
		for alias, to := range p.Deps {
			r.edges[p.Name+"@"+p.Version+"|"+alias+"|"+id(to)] = true
		}
	}
	for dir, im := range l.Importers {
		for alias, to := range im.Deps {
			r.edges["importer:"+dir+"|"+alias+"|"+id(to)] = true
		}
	}
	return r
}

// npmResolution reduces package-lock.json: every entry that is not a link and not bundled is a
// package; the version Node would load for a dependency is found by walking up node_modules.
// importers maps workspace dir (and "." for the root) as in the graph.
func npmResolution(t *testing.T, raw []byte, importers []string) resolution {
	t.Helper()
	var l npmLock
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatalf("harness: package-lock.json: %v", err)
	}
	r := resolution{pkgs: map[string]bool{}, edges: map[string]bool{}}
	nameOf := func(key string) string {
		p := l.Packages[key]
		if p.Name != "" {
			return p.Name
		}
		_, n, _ := strings.Cut(key[strings.LastIndex(key, "node_modules/"):], "/")
		return n
	}
	// lookup finds the entry dep resolves to from the package at location loc.
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
			} else if strings.HasPrefix(loc, "node_modules/") {
				loc = ""
			} else {
				loc = "" // a workspace dir: continue at the root
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
		for n := range p.PeerMeta { // an optional peer counts only when present
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
		edges("importer:"+dir, key, p.Deps, p.OptionalDeps)
	}
	return r
}

// diff lists the elements of a not in b.
func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// runNpmOracle runs the pinned npm in a copy of the workspace layout of g against the mirror at
// registry (the same snapshot bob resolved) and returns package-lock.json.
func runNpmOracle(t *testing.T, g graph, registry string) []byte {
	t.Helper()
	dir := t.TempDir()
	var workspaces []string
	var root map[string]any
	for _, im := range g.importers {
		pj := oraclePackageJSON(g, im)
		if im.dir == "." {
			root = pj
			continue
		}
		workspaces = append(workspaces, im.dir)
		b, _ := json.MarshalIndent(pj, "", "  ")
		ecotest.WriteFile(t, filepath.Join(dir, filepath.FromSlash(im.dir), "package.json"), string(b))
	}
	if root == nil {
		root = map[string]any{"name": "oracle-root", "version": "0.0.0", "private": true}
	}
	if len(workspaces) > 0 {
		root["workspaces"] = workspaces
	}
	b, _ := json.MarshalIndent(root, "", "  ")
	ecotest.WriteFile(t, filepath.Join(dir, "package.json"), string(b))
	node := nodeTool(t, "node")
	r := harness.RunPath(t, nodeTool(t, "npm"), harness.Spec{Dir: dir, Env: []string{
		"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin", "npm_config_update_notifier=false",
		"npm_config_cache=" + filepath.Join(dir, ".npm-cache"),
	}}, "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund", "--registry", registry)
	if r.Exit != 0 {
		t.Fatalf("harness: npm install --package-lock-only for %s exited %d:\n%s%s", g.name, r.Exit, r.Stdout, r.Stderr)
	}
	return []byte(ecotest.ReadFile(t, filepath.Join(dir, "package-lock.json")))
}

func registryURL(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "BOB_NPM_REGISTRY="); ok {
			return v
		}
	}
	return ""
}

// R-NPM-001a: on the ten graphs (the six S4 graphs of 69-411 packages, SvelteKit, Vite+Svelte,
// hono and the peer-split monorepo) bob's name@version set and edges equal
// `npm install --package-lock-only` against the same registry snapshot, except the peer-split
// edges, which TestRNPM001aPeerSplit asserts explicitly.
func TestRNPM001aMatchesNpm(t *testing.T) {
	parity.Covers(t, "R-NPM-001a")
	for _, g := range graphs() {
		t.Run(g.name, func(t *testing.T) {
			home := t.TempDir()
			env := ecotest.Registry(t, home)
			dir := writeGraph(t, g)
			r := ecotest.BobEnv(t, dir, env, "lock")
			if !ecotest.OK(t, r) {
				return
			}
			bob := bobResolution(readLock(t, dir))
			var dirs []string
			for _, im := range g.importers {
				dirs = append(dirs, im.dir)
			}
			want := npmResolution(t, runNpmOracle(t, g, registryURL(env)), dirs)
			if d := diff(want.pkgs, bob.pkgs); len(d) > 0 {
				t.Errorf("npm resolves packages bob does not: %v", d)
			}
			if d := diff(bob.pkgs, want.pkgs); len(d) > 0 {
				t.Errorf("bob resolves packages npm does not: %v", d)
			}
			// Peer-split edges are asserted in TestRNPM001aPeerSplit.
			skip := func(e string) bool {
				return g.peerSplit && strings.Contains(e, "use-sync-external-store")
			}
			for _, e := range diff(want.edges, bob.edges) {
				if !skip(e) {
					t.Errorf("edge npm has and bob lacks: %s", e)
				}
			}
			for _, e := range diff(bob.edges, want.edges) {
				if !skip(e) {
					t.Errorf("edge bob has and npm lacks: %s", e)
				}
			}
			if len(bob.pkgs) == 0 {
				t.Errorf("bob.lock has no npm packages")
			}
		})
	}
}

// R-NPM-001a (peer split): where two importers need the same package with different peers,
// bob's lock gives each importer its own instance with its own peer (npm's lock shares one and
// resolves the wrong peer for one importer).
func TestRNPM001aPeerSplit(t *testing.T) {
	parity.Covers(t, "R-NPM-001a")
	var g graph
	for _, c := range graphs() {
		if c.peerSplit {
			g = c
		}
	}
	home := t.TempDir()
	dir := writeGraph(t, g)
	if r := ecotest.BobEnv(t, dir, ecotest.Registry(t, home), "lock"); !ecotest.OK(t, r) {
		return
	}
	l := readLock(t, dir)
	root, b := l.Importers["."].Deps["use-sync-external-store"], l.Importers["b"].Deps["use-sync-external-store"]
	if !strings.Contains(root, "(react@18.") || !strings.Contains(b, "(react@19.") || root == b {
		t.Errorf("importer . uses %q and importer b uses %q; want instances with the react@18 and react@19 peer", root, b)
	}
	var instances []string
	for k, p := range l.Packages {
		if p.Name == "use-sync-external-store" {
			instances = append(instances, k)
			if len(p.Peers) == 0 || p.Deps["react"] == "" {
				t.Errorf("instance %s does not record its resolved peer react: %+v", k, p)
			}
		}
	}
	if len(instances) != 2 {
		t.Errorf("use-sync-external-store instances = %v, want exactly two (one per peer)", instances)
	}
}

// faultRegistry serves one hostile document for package "evil" and a valid-looking default for
// anything else; mode selects the fault.
func faultRegistry(t *testing.T, mode string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch mode {
		case "oversize":
			// 129 MiB of JSON: over the 128 MB cap of registry documents.
			_, _ = io.WriteString(w, `{"name":"evil","dist-tags":{"latest":"1.0.0"},"junk":"`)
			chunk := strings.Repeat("a", 1<<20)
			for i := 0; i < 129; i++ {
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
			}
			_, _ = io.WriteString(w, `","versions":{}}`)
		case "badname":
			fmt.Fprintf(w, `{"name":"evil","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"evil","version":"1.0.0","dependencies":{"../../etc/passwd":"1.0.0"},"dist":{"tarball":"http://%s/evil/-/evil-1.0.0.tgz","integrity":"sha512-AAAA"}}}}`, r.Host)
		case "integrity":
			fmt.Fprintf(w, `{"name":"evil","dist-tags":{"latest":"1.0.0"},"versions":{"1.0.0":{"name":"evil","version":"1.0.0","dist":{"tarball":"http://%s/evil/-/evil-1.0.0.tgz","integrity":"md4-AAAAAAAAAAAAAAAAAAAAAA=="}}}}`, r.Host)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

// R-NPM-001a (hostile registry input): registry documents over 128 MB, invalid names and
// unknown integrity algorithms are rejected with E-RESOLVE (BOB-E-1015), exit 3.
func TestRNPM001aHostileRegistryDocuments(t *testing.T) {
	parity.Covers(t, "R-NPM-001a")
	for _, mode := range []string{"oversize", "badname", "integrity"} {
		t.Run(mode, func(t *testing.T) {
			g := graph{name: "hostile", importers: []importer{{dir: "app", kind: "app", deps: []dep{d("evil", "^1")}}}}
			dir := writeGraph(t, g)
			home := t.TempDir()
			env := []string{"BOB_HOME=" + home, "BOB_TEST_HTTP_LOOPBACK=1", "BOB_NPM_REGISTRY=" + faultRegistry(t, mode)}
			r := ecotest.BobEnv(t, dir, env, "lock", "--json")
			if harness.Red(t, r) || !ecotest.ExitIs(t, r, 3) {
				return
			}
			found := false
			for _, c := range ecotest.ErrorCodes(t, r) {
				found = found || c == "BOB-E-1015"
			}
			if !found {
				t.Errorf("no BOB-E-1015 error event; codes %v\n%s", ecotest.ErrorCodes(t, r), r.Stdout)
			}
			if b, err := os.ReadFile(filepath.Join(dir, "bob.lock")); err == nil && strings.Contains(string(b), "evil") {
				t.Errorf("bob.lock mentions the rejected package: %s", b)
			}
		})
	}
	// An invalid package name in bob.go itself is rejected the same way.
	t.Run("name in bob.go", func(t *testing.T) {
		g := graph{name: "badroot", importers: []importer{{dir: "app", kind: "app", deps: []dep{d("../escape", "^1")}}}}
		dir := writeGraph(t, g)
		r := ecotest.BobEnv(t, dir, ecotest.Registry(t, t.TempDir()), "lock", "--json")
		if harness.Red(t, r) || !ecotest.ExitIs(t, r, 3) {
			return
		}
		if codes := ecotest.ErrorCodes(t, r); len(codes) == 0 || codes[0] != "BOB-E-1015" {
			t.Errorf("error codes %v, want BOB-E-1015 first", codes)
		}
	})
}
