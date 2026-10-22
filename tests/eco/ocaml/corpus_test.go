package ocaml

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/eco/internal/ecotest"
	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
)

// The toolchain bob fixes (SPEC §03.5.2): OCaml, dune and ocamlfind come from nixpkgs, not from
// the repository. S5 measured these versions at the nixpkgs pin; opam fake-installs them.
const (
	ocamlVersion     = "5.4.1"
	duneVersion      = "3.21.1"
	ocamlfindVersion = "1.9.8"
)

// graph is one corpus entry: root packages with an opam-style constraint ("" = any).
type graph struct {
	name  string
	roots [][2]string
}

func g(name string, roots ...string) graph {
	gr := graph{name: name}
	for _, r := range roots {
		gr.roots = append(gr.roots, [2]string{r, ""})
	}
	return gr
}

// corpus is the 20-graph corpus of R-ML-002: the 8 S5 graphs (docs/spikes/s05-ocaml.md) and 12
// more (decision: the SPEC fixes only the count; these cover PPX, parsers, test frameworks,
// formatters, web and crypto stacks).
func corpus() []graph {
	return []graph{
		g("demo", "cmdliner", "fmt", "yojson", "lwt"),
		g("ppx", "ppx_deriving", "ppx_deriving_yojson", "logs"),
		g("cohttp", "cohttp-lwt-unix"),
		g("core", "core", "core_unix"),
		g("irmin", "irmin-fs"),
		g("eio", "eio_main"),
		g("dream", "dream"),
		g("lsp", "ocaml-lsp-server"),
		g("alcotest", "alcotest"),
		g("re", "re"),
		g("angstrom", "angstrom"),
		g("sexplib", "sexplib"),
		g("lwt-ppx", "lwt_ppx", "lwt"),
		g("containers", "containers"),
		g("base-stdio", "base", "stdio"),
		g("time", "ptime", "mtime"),
		g("ocamlformat", "ocamlformat"),
		g("qcheck", "qcheck"),
		g("tyxml", "tyxml"),
		g("digestif", "digestif"),
	}
}

// provided are the packages bob never chooses from the repository; they are excluded from the
// comparison with opam.
func provided(name string) bool {
	switch name {
	case "ocaml", "ocaml-base-compiler", "ocaml-compiler", "ocaml-config", "ocaml-options-vanilla", "dune", "ocamlfind":
		return true
	}
	return strings.HasPrefix(name, "base-")
}

// writeRepo writes a repository with one executable depending on the roots.
func writeRepo(t *testing.T, gr graph) string {
	t.Helper()
	var deps []string
	for _, r := range gr.roots {
		deps = append(deps, fmt.Sprintf("ocaml.Opam(%q, %q)", r[0], r[1]))
	}
	files := map[string]string{
		"go.mod":      "module example.com/corpus\n\ngo 1.26\n",
		"bob.lock":    "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":      "//go:build bob\n\npackage root\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n",
		"app/main.ml": "let () = ()\n",
		"app/bob.go": "//go:build bob\n\npackage app\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/ocaml\"\n)\n\n" +
			"var App = bob.Define(func(p *bob.P) *ocaml.Exe {\n\treturn ocaml.Executable(p, ocaml.ExeOpts{Name: \"app\", Deps: []ocaml.Dep{" + strings.Join(deps, ", ") + "}})\n})\n",
	}
	dir := filepath.Join(t.TempDir(), "root") // R2: root package = directory name
	for rel, body := range files {
		ecotest.WriteFile(t, filepath.Join(dir, filepath.FromSlash(rel)), body)
	}
	return dir
}

// opamOracle is the pinned opam over the repository snapshot of the mirror, with the toolchain
// fake-installed in an empty switch (the S5 method). All state lives under the test's temp dir.
type opamOracle struct {
	t    *testing.T
	opam string
	root string
}

func newOpamOracle(t *testing.T) *opamOracle {
	t.Helper()
	commit := ecotest.OpamCommit(t)
	if commit == "" {
		t.Fatalf("harness: tests/fixtures/registry/index.json has no opam commit (run the manual -refresh-registry)")
	}
	blobs := os.Getenv("BOB_REGISTRY_BLOBS")
	if blobs == "" {
		cache, _ := os.UserCacheDir()
		blobs = filepath.Join(cache, "bob-test", "registry-blobs")
	}
	tarball := filepath.Join(blobs, "opam", "repository", commit+".tar.gz")
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if b, err := exec.Command("tar", "-xzf", tarball, "-C", repo, "--strip-components=1").CombinedOutput(); err != nil {
		t.Fatalf("harness: unpack %s: %v: %s", tarball, err, b)
	}
	o := &opamOracle{t: t, opam: filepath.Join(ecotest.Tool(t, "opam"), "bin", "opam"), root: filepath.Join(t.TempDir(), "opamroot")}
	for _, args := range [][]string{
		{"init", "--bare", "--no-setup", "--disable-sandboxing", "--bypass-checks", "--yes", "default", "file://" + repo},
		{"switch", "create", "oracle", "--empty", "--yes"},
		{"install", "--fake", "--yes", "ocaml-base-compiler." + ocamlVersion, "ocaml." + ocamlVersion, "dune." + duneVersion, "ocamlfind." + ocamlfindVersion},
	} {
		if out, err := o.run(args...); err != nil {
			t.Fatalf("harness: opam %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return o
}

func (o *opamOracle) run(args ...string) (string, error) {
	cmd := exec.Command(o.opam, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + o.root, "OPAMROOT=" + o.root, "OPAMYES=1", "OPAMCOLOR=never", "OPAMSWITCH=oracle"}
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// installLine matches opam's action lines (`  - install <name> <version>`), not its header
// (`=== install N packages`).
var installLine = regexp.MustCompile(`(?m)^\s*\S\s+install\s+(\S+)\s+(\S+)`)

// toolchain pins the packages bob fixes (SPEC §03.5.2) in every request, as S5 did: opam would
// otherwise upgrade dune, which bob cannot choose.
var toolchain = []string{"ocaml-base-compiler." + ocamlVersion, "dune." + duneVersion, "ocamlfind." + ocamlfindVersion}

// solve returns name to version of `opam install --dry-run specs...` without the provided set.
// --assume-depexts: opam stops at the system-package prompt (conf-gmp) otherwise.
func (o *opamOracle) solve(specs ...string) (map[string]string, error) {
	args := append([]string{"install", "--dry-run", "--yes", "--assume-depexts"}, specs...)
	out, err := o.run(append(args, toolchain...)...)
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, out)
	}
	sol := map[string]string{}
	for _, m := range installLine.FindAllStringSubmatch(out, -1) {
		if !provided(m[1]) {
			sol[m[1]] = m[2]
		}
	}
	return sol, nil
}

func versions(l lockOpam) map[string]string {
	out := map[string]string{}
	for n, p := range l.Packages {
		if !provided(n) {
			out[n] = p.Version
		}
	}
	return out
}

func same(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// R-ML-002 (corpus): on the 20-graph corpus every bob solution is valid and at least 70 % equal
// `opam install --dry-run` against the same repository commit. Validity is checked by the
// independent opam: installing exactly bob's name.version set must succeed and must not pull
// in any package outside the set (no missing dependency, no violated constraint).
func TestRML002CorpusValidAndCloseToOpam(t *testing.T) {
	parity.Covers(t, "R-ML-002")
	if n := len(corpus()); n != 20 {
		t.Fatalf("harness: the corpus has %d graphs, SPEC says 20", n)
	}
	var oracle *opamOracle
	equal := 0
	var lockedAny bool
	for _, gr := range corpus() {
		t.Run(gr.name, func(t *testing.T) {
			dir := writeRepo(t, gr)
			r := ecotest.BobEnv(t, dir, ecotest.Registry(t, t.TempDir()), "lock")
			if !ecotest.OK(t, r) {
				return
			}
			lockedAny = true
			if oracle == nil {
				oracle = newOpamOracle(t)
			}
			got := versions(readOpam(t, dir))
			var specs []string
			for n, v := range got {
				specs = append(specs, n+"."+v)
			}
			sort.Strings(specs)
			valid, err := oracle.solve(specs...)
			if err != nil {
				t.Errorf("bob's solution is not installable: %v", err)
				return
			}
			if !same(valid, got) {
				t.Errorf("bob's solution is not closed: opam adds or changes %v", diffSets(valid, got))
				return
			}
			var roots []string
			for _, rt := range gr.roots {
				roots = append(roots, rt[0])
			}
			want, err := oracle.solve(roots...)
			if err != nil {
				t.Logf("opam itself cannot solve %v: %v", roots, err)
				return
			}
			if same(want, got) {
				equal++
			} else {
				t.Logf("differs from opam: %v", diffSets(want, got))
			}
		})
	}
	if !lockedAny {
		return // every graph was red: not implemented
	}
	if min := 14; equal < min { // 70 % of 20
		t.Errorf("bob equals opam on %d of 20 graphs, want >= %d", equal, min)
	}
}

func diffSets(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if b[k] != v {
			out = append(out, fmt.Sprintf("%s: %s vs %s", k, v, b[k]))
		}
	}
	for k, v := range b {
		if _, ok := a[k]; !ok {
			out = append(out, fmt.Sprintf("%s: - vs %s", k, v))
		}
	}
	sort.Strings(out)
	return out
}

// R-ML-002 (unsatisfiable): ocaml-lsp-server 1.26.0 needs dune-rpc >= 3.22.0 but dune is fixed
// to the toolchain's version, so bob lock fails with E-RESOLVE (BOB-E-1015), exit 3, and names
// the conflicting constraint chain.
func TestRML002UnsatisfiableInput(t *testing.T) {
	parity.Covers(t, "R-ML-002")
	gr := graph{name: "unsat", roots: [][2]string{{"ocaml-lsp-server", "= 1.26.0"}}}
	dir := writeRepo(t, gr)
	env := ecotest.Registry(t, t.TempDir())
	r := ecotest.BobEnv(t, dir, env, "lock", "--json")
	if harness.Red(t, r) || !ecotest.ExitIs(t, r, 3) {
		return
	}
	found := false
	for _, c := range ecotest.ErrorCodes(t, r) {
		found = found || c == "BOB-E-1015"
	}
	if !found {
		t.Errorf("no BOB-E-1015 error event; codes %v", ecotest.ErrorCodes(t, r))
	}
	human := ecotest.BobEnv(t, dir, env, "lock")
	for _, want := range []string{
		"✗ cannot resolve opam packages:",
		`ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0": no available version matches (dune is fixed to ` + duneVersion + ` by the toolchain)`,
	} {
		if !strings.Contains(human.Stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, human.Stderr)
		}
	}
}
