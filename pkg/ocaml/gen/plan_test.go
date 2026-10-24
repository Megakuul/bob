package gen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/ocaml/bop"
	"github.com/megakuul/bob/pkg/ocaml/internal/mlspec"
)

// fakeResolver gives every foreign derivation a fixed modulo hash.
type fakeResolver struct{}

func (fakeResolver) Modulo(p string) ([32]byte, error) { return sha256.Sum256([]byte(p)), nil }

// fakeStore serves the opam repository snapshot from a directory.
type fakeStore struct{ repo string }

func (s fakeStore) HostPath(p string) string { return s.repo }
func (fakeStore) ImportSources(context.Context, []srcs.Source) ([]string, error) {
	return nil, errors.New("not in this test")
}
func (fakeStore) Ensure(_ context.Context, r fetch.Request) (string, error) {
	return "/nix/store/0123456789abcdfghijklmnpqrsvwxyz-" + r.Name, nil
}

const builderP = "/nix/store/541snxzk5chsnfbiq1mskx7595mxi267-bob-builder-7"

// opamFiles of the fixture's snapshot: fmt (topkg build with a depopt), a virtual package and
// a conf package.
var opamFixture = map[string]string{
	"fmt.0.11.0": `opam-version: "2.0"
depends: ["ocaml" "topkg" {build} "base-bytes"]
depopts: ["cmdliner"]
build: [
  "ocaml" "pkg/pkg.ml" "build" "--dev-pkg" "%{dev}%"
  "--with-cmdliner" "%{cmdliner:installed}%" "--jobs" jobs
]
url {
  src: "https://erratique.ch/software/fmt/releases/fmt-0.11.0.tbz"
  checksum: "sha512=3f4015f1a6a7316202e410595964304c0b5d4fd78819ac00f924366fed9da1a8c0b8dee39fd7ba8e2d5e2f8ff6c48b0f8a3dc7d17580af31dca6a10f9b5ef4ad"
}
`,
	"topkg.1.1.1": `opam-version: "2.0"
depends: ["ocaml" "ocamlfind" {build}]
build: [["ocaml" "pkg/pkg.ml" "build" "--pkg-name" name] ["true"] {with-test}]
install: [make "install" "PREFIX=%{prefix}%" "LIBDIR=%{_:lib}%" "DUNE=%{dune:lib}%"]
url {
  src: "https://erratique.ch/software/topkg/releases/topkg-1.1.1.tbz"
  checksum: "sha256=e1f1d45e1f8d1d02a5fd5ed6ec08ca94a3e2ff4b4e4a45a9a8c2c9a64b6f4f1e"
}
`,
	"base-bytes.base": `opam-version: "2.0"
depends: ["ocaml" "ocamlfind"]
`,
}

type fixture struct {
	root  string
	g     *ir.Graph
	in    map[string]engine.Resolved
	srcs  map[string]string
	lockf json.RawMessage
	repo  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{root: t.TempDir(), repo: t.TempDir(), g: &ir.Graph{}, in: map[string]engine.Resolved{}, srcs: map[string]string{}}
	for rel, body := range map[string]string{
		"geom/geom.ml": "module Vec = Vec\n", "geom/vec.ml": "let x = 1\n", "geom/vec.mli": "val x : int\n",
		"geom/geom_stubs.c": "\n", "app/main.ml": "let () = ()\n", "test/test.ml": "let () = ()\n",
	} {
		write(t, filepath.Join(f.root, rel), body)
	}
	sec := lock.OpamSection{Inputs: strings.Repeat("0", 64), Repository: lock.OpamRepository{URL: RepositoryURL,
		Commit: DefaultCommit, NarHash: "sha256-3LqTtkQ9glbAQ6bK3z7MmZD1kosfNZ2H0oq2VGJf0Qg="}, Packages: map[string]lock.OpamPackage{}}
	deps := map[string][]string{"fmt": {"base-bytes", "cmdliner", "topkg"}}
	for nv, body := range opamFixture {
		name, ver, _ := strings.Cut(nv, ".")
		write(t, filepath.Join(f.repo, "packages", name, nv, "opam"), body)
		sum := sha256.Sum256([]byte(body))
		p := lock.OpamPackage{Version: ver, Opam: hex.EncodeToString(sum[:]), Deps: deps[name]}
		if strings.Contains(body, "url {") {
			p.URL = "https://erratique.ch/software/" + name + "/releases/" + name + "-" + ver + ".tbz"
			p.Hash = "sha256-3LqTtkQ9glbAQ6bK3z7MmZD1kosfNZ2H0oq2VGJf0Qg="
		}
		sec.Packages[name] = p
	}
	// cmdliner is a locked dependency whose opam file the fixture leaves out on purpose for
	// TestMissingOpamFile; give it one here.
	cm := "opam-version: \"2.0\"\nbuild: [make \"all\" \"PREFIX=%{prefix}%\"]\n"
	write(t, filepath.Join(f.repo, "packages", "cmdliner", "cmdliner.2.1.1", "opam"), cm)
	sum := sha256.Sum256([]byte(cm))
	sec.Packages["cmdliner"] = lock.OpamPackage{Version: "2.1.1", Opam: hex.EncodeToString(sum[:])}
	b, err := json.Marshal(sec)
	if err != nil {
		t.Fatal(err)
	}
	f.lockf = b

	proj := map[string]json.RawMessage{"platform": json.RawMessage(`"` + hostPlatform() + `"`)}
	var toolIDs []string
	tools := map[string]int{}
	for i, a := range mlspec.ToolAttrs {
		id := "nixpkgs:" + a
		name := a[strings.LastIndex(a, ".")+1:]
		if a == mlspec.AttrOCaml {
			name = "ocaml-" + OCamlVersion
		}
		f.g.Nodes = append(f.g.Nodes, &ir.Node{ID: id, Kind: "nixpkgs.tool", Label: "nixpkgs:" + a, Nix: &ir.Nix{Attr: a, Set: "host"}})
		f.in[id] = engine.Resolved{Drv: "/nix/store/0m1nl6fyb2zn779if6aaswmbwmmdnlwr-" + name + ".drv",
			Outputs: map[string]string{"out": "/nix/store/0m1nl6fyb2zn779if6aaswmbwmmdnlwr-" + name}}
		toolIDs = append(toolIDs, id)
		tools[a] = i
	}
	f.g.Nodes = append(f.g.Nodes, &ir.Node{ID: "cfoo", Kind: "cc.library", Label: "//cfoo:cfoo"})
	f.in["cfoo"] = engine.Resolved{Drv: "/nix/store/x4aymyirkcksp9wa7v72r9yxzsk6yfwp-cc-cfoo.drv",
		Outputs: map[string]string{"out": "/nix/store/x4aymyirkcksp9wa7v72r9yxzsk6yfwp-cc-cfoo"}}
	f.in["cfoo"] = engine.Resolved{Drv: f.in["cfoo"].Drv, Outputs: f.in["cfoo"].Outputs,
		LinkInfo: &bob.LinkInfo{IncludeDirs: []string{f.in["cfoo"].Outputs["out"] + "/include"},
			Archives: []string{f.in["cfoo"].Outputs["out"] + "/lib/libcfoo.a"}}}
	target := func(id, kind, label string, s mlspec.Target, deps []string, patterns ...string) {
		s.Tools = tools
		s.Conf = []mlspec.Conf{}
		if s.Opam == nil {
			s.Opam = []mlspec.Opam{}
		}
		if s.Local == nil {
			s.Local = []int{}
		}
		if s.Native == nil {
			s.Native = []int{}
		}
		spec, _ := json.Marshal(s)
		f.g.Nodes = append(f.g.Nodes, &ir.Node{ID: id, Kind: kind, Label: label, Proj: proj, Deps: append(slices.Clone(toolIDs), deps...),
			Srcs: []ir.Srcs{{Dir: labelDir(label), Patterns: patterns}}, Spec: spec, DefinedAt: ir.Pos{File: labelDir(label) + "/bob.go", Line: 9}})
		f.srcs[id] = "/nix/store/x4aymyirkcksp9wa7v72r9yxzsk6yfwp-" + labelDir(label) + "-src"
	}
	nt := len(toolIDs)
	target("geom", mlspec.KindLibrary, "//geom:geom", mlspec.Target{Name: "geom", Type: mlspec.TypeLibrary, Wrapped: true,
		Opam: []mlspec.Opam{{Name: "fmt", Lib: "fmt"}}, Native: []int{nt}}, []string{"cfoo"}, "*.ml", "*.mli", "geom_stubs.c")
	target("demo", mlspec.KindExecutable, "//app:demo", mlspec.Target{Name: "demo", Type: mlspec.TypeExecutable, Main: "main",
		Opam: []mlspec.Opam{{Name: "fmt", Lib: "fmt.tty"}}, Local: []int{nt}}, []string{"geom"}, "*.ml")
	target("geom_test", mlspec.KindTest, "//test:geom_test", mlspec.Target{Name: "geom_test", Type: mlspec.TypeTest, Main: "test",
		Local: []int{nt}}, []string{"geom"}, "*.ml")
	return f
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hostPlatform() string {
	if hostSystem() == "x86_64-linux" {
		return "linux-amd64"
	}
	return "linux-arm64"
}

func (f *fixture) node(id string) *ir.Node {
	for _, n := range f.g.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// plan plans the nodes ids in order (one wave each), feeding each result into the next.
func (f *fixture) plan(t *testing.T, ids ...string) (map[string]*drv.Derivation, []fetch.Request, error) {
	t.Helper()
	drvs := map[string]*drv.Derivation{}
	var fetches []fetch.Request
	for _, id := range ids {
		res, err := Ecosystem{}.Plan(context.Background(), engine.PlanRequest{Graph: f.g, Nodes: []*ir.Node{f.node(id)},
			Lock: f.lockf, Inputs: f.in, Sources: f.srcs, Builder: builderP, Resolver: fakeResolver{}, Root: f.root,
			Store: fakeStore{repo: f.repo}})
		if err != nil {
			return drvs, fetches, err
		}
		for _, d := range res.Drvs {
			drvs[d.Name] = d
		}
		for nid, rv := range res.Nodes {
			f.in[nid] = rv
		}
		fetches = append(fetches, res.Fetches...)
	}
	return drvs, fetches, nil
}

// job decodes a derivation's bobSpec strictly into bspec.Job and its args into v.
func job(t *testing.T, d *drv.Derivation, v any) bspec.Job {
	t.Helper()
	b, err := json.Marshal(d.Structured["bobSpec"])
	if err != nil {
		t.Fatal(err)
	}
	var j bspec.Job
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		t.Fatalf("%s: bobSpec does not decode into bspec.Job: %v", d.Name, err)
	}
	dec = json.NewDecoder(bytes.NewReader(j.Args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: args do not decode into %T: %v", d.Name, v, err)
	}
	return j
}

func keys(m map[string]*drv.Derivation) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestGenJobsDecode: every job decodes strictly into the builder's types; one derivation per
// opam package, the opam commands are rendered, and the local packages see their closure.
func TestGenJobsDecode(t *testing.T) {
	f := newFixture(t)
	drvs, fetches, err := f.plan(t, "geom", "demo", "geom_test")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"opam-fmt-0.11.0", "opam-topkg-1.1.1", "opam-base-bytes-base", "opam-cmdliner-2.1.1",
		"ml-geom", "ml-demo", "ml-geom_test", "mlrun-geom_test"} {
		if drvs[name] == nil {
			t.Fatalf("no derivation %s; have %v", name, keys(drvs))
		}
	}
	var fm bop.OpamBuildArgs
	j := job(t, drvs["opam-fmt-0.11.0"], &fm)
	if j.Kind != "opam-build" || j.Label != "opam:fmt.0.11.0" {
		t.Errorf("fmt job header: %+v", j)
	}
	want := [][]string{{"ocaml", "pkg/pkg.ml", "build", "--dev-pkg", "false", "--with-cmdliner", "true", "--jobs", bop.JobsMarker}}
	if !jsonEqual(fm.Commands, want) {
		t.Errorf("fmt commands:\n got %q\nwant %q", fm.Commands, want)
	}
	if !strings.HasSuffix(fm.Src, "-opam-src-fmt-0.11.0") {
		t.Errorf("fmt src %q", fm.Src)
	}
	if p := fm.Env["OCAMLPATH"]; !strings.Contains(p, "opam-topkg-1.1.1/lib") || !strings.Contains(p, "opam-cmdliner-2.1.1/lib") {
		t.Errorf("fmt OCAMLPATH %q lacks its dependencies", p)
	}
	var tk bop.OpamBuildArgs
	job(t, drvs["opam-topkg-1.1.1"], &tk)
	out := drv.Placeholder("out")
	wantTk := [][]string{{"ocaml", "pkg/pkg.ml", "build", "--pkg-name", "topkg"},
		{"make", "install", "PREFIX=" + out, "LIBDIR=" + out + "/lib/topkg", "DUNE=/nix/store/0m1nl6fyb2zn779if6aaswmbwmmdnlwr-dune_3/lib/dune"}}
	if !jsonEqual(tk.Commands, wantTk) {
		t.Errorf("topkg commands:\n got %q\nwant %q", tk.Commands, wantTk)
	}
	var bb bop.OpamBuildArgs
	job(t, drvs["opam-base-bytes-base"], &bb)
	if bb.Src != "" || len(bb.Commands) != 0 {
		t.Errorf("base-bytes: %+v", bb)
	}
	urls := 0
	for _, r := range fetches {
		if r.Name == "opam-src-fmt-0.11.0" {
			urls = len(r.URLs)
			if r.Kind != fetch.File || r.URLs[0] != "https://erratique.ch/software/fmt/releases/fmt-0.11.0.tbz" {
				t.Errorf("fmt fetch %+v", r)
			}
		}
	}
	if urls != 2 { // origin and opam's archive cache
		t.Errorf("fmt fetch has %d URLs", urls)
	}

	var geom bop.CompileArgs
	j = job(t, drvs["ml-geom"], &geom)
	if j.Kind != "ml-compile" || geom.Dir != "geom" || !geom.Wrapped || !slices.Equal(geom.Stubs, []string{"geom_stubs.c"}) ||
		!slices.Equal(geom.Modules, []string{"geom.ml", "vec.ml", "vec.mli"}) || !slices.Equal(geom.Packages, []string{"fmt"}) {
		t.Errorf("geom: %+v", geom)
	}
	cfoo := f.in["cfoo"].Outputs["out"]
	if !slices.Equal(geom.CFlags, []string{"-I" + cfoo + "/include"}) || !slices.Equal(geom.CLibs, []string{cfoo + "/lib/libcfoo.a"}) {
		t.Errorf("geom native: cflags %q clibs %q", geom.CFlags, geom.CLibs)
	}
	var demo bop.LinkArgs
	j = job(t, drvs["ml-demo"], &demo)
	geomOut := f.in["geom"].Outputs["out"]
	if j.Kind != "ml-link" || !slices.Equal(demo.Packages, []string{"fmt.tty", "geom"}) || !strings.Contains(demo.Env["OCAMLPATH"], geomOut+"/lib") ||
		!strings.Contains(demo.Env["OCAMLPATH"], "opam-fmt-0.11.0/lib") || len(demo.Scrub) == 0 || !j.Fixup.ShrinkRPath {
		t.Errorf("demo: %+v %+v", demo, j.Fixup)
	}
	if !slices.Contains(j.Fixup.Forbidden, "/nix/store/0m1nl6fyb2zn779if6aaswmbwmmdnlwr-ocaml-"+OCamlVersion) {
		t.Errorf("demo fixup does not forbid the compiler: %v", j.Fixup.Forbidden)
	}
	var run bspec.TestRunArgs
	job(t, drvs["mlrun-geom_test"], &run)
	if run.Format != "tap" || !strings.HasSuffix(run.Argv[0], "/bin/geom_test") {
		t.Errorf("test run: %+v", run)
	}
	if rv := f.in["demo"]; rv.MainProgram != "bin/demo" {
		t.Errorf("demo resolved: %+v", rv)
	}
}

// TestNotLocked: a package bob.lock lacks is BOB-E-1012 naming the package and the label.
func TestNotLocked(t *testing.T) {
	f := newFixture(t)
	var sec lock.OpamSection
	if err := json.Unmarshal(f.lockf, &sec); err != nil {
		t.Fatal(err)
	}
	delete(sec.Packages, "fmt")
	f.lockf, _ = json.Marshal(sec)
	_, _, err := f.plan(t, "geom")
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 1012 || !strings.Contains(d.Error(), "fmt") || !strings.Contains(d.Error(), "//geom:geom") {
		t.Fatalf("want BOB-E-1012 for fmt, got %v", err)
	}
}

// TestOpamDigestChecked: an opam file that differs from bob.lock's digest is refused.
func TestOpamDigestChecked(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.repo, "packages", "topkg", "topkg.1.1.1", "opam"), "opam-version: \"2.0\"\n")
	if _, _, err := f.plan(t, "geom"); err == nil || !strings.Contains(err.Error(), "does not match bob.lock") {
		t.Fatalf("want a digest mismatch, got %v", err)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
