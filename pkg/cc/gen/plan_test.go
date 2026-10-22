package gen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/cc/bop"
	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
)

// fakeResolver gives every foreign derivation a fixed modulo hash.
type fakeResolver struct{}

func (fakeResolver) Modulo(p string) ([32]byte, error) { return sha256.Sum256([]byte(p)), nil }

const (
	builderP = "/nix/store/541snxzk5chsnfbiq1mskx7595mxi267-bob-builder-5"
	narHash  = "sha256-3LqTtkQ9glbAQ6bK3z7MmZD1kosfNZ2H0oq2VGJf0Qg="
	zstdURL  = "https://example.com/zstd-1.5.7.tar.gz"
)

// nix is a planned nixpkgs package of the fixture graph.
type nix struct{ id, attr, drv string }

var nixpkgs = []nix{
	{"gcc", "gcc", "/nix/store/52dk32w642kazfdmrydn6fm99q5x44zs-gcc-wrapper-15.3.0.drv"},
	{"binutils", "binutils", "/nix/store/i20mrcslg259vlm2mz4shkqbncq64f6y-binutils-wrapper-2.46.drv"},
	{"patchelf", "patchelf", "/nix/store/kqyrj5v04pskz07dfj8wmihqywklg9jq-patchelf-0.15.2.drv"},
	{"bash", "bash", "/nix/store/0m1nl6fyb2zn779if6aaswmbwmmdnlwr-bash-5.3p9.drv"},
	{"gnumake", "gnumake", "/nix/store/3yw5mmihkc3abmrq2rigkd8117vvqzgm-gnumake-4.4.1.drv"},
	{"zlib", "zlib", "/nix/store/nm3pxakd81mhkc73n1pd73gs7h5yxm70-zlib-1.3.2.drv"},
}

func storeOut(drvPath, out string) string {
	p := strings.TrimSuffix(drvPath, ".drv")
	if out != "out" {
		p += "-" + out
	}
	return p
}

// fixture is the graph of a repository like tests/fixtures/repos/cc: a static C library with a
// TAP test, a shared C++ library linking nixpkgs zlib, a program linking both and an External.
type fixture struct {
	root  string
	g     *ir.Graph
	in    map[string]engine.Resolved
	srcs  map[string]string
	lockf json.RawMessage
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{root: t.TempDir(), g: &ir.Graph{}, in: map[string]engine.Resolved{}, srcs: map[string]string{}}
	for rel, body := range map[string]string{
		"libs/mathx/include/mathx.h":     "long mathx_gcd(long, long);\n",
		"libs/mathx/src/mathx.c":         "long mathx_gcd(long a, long b) { return b ? mathx_gcd(b, a % b) : a; }\n",
		"libs/mathx/test/mathx_test.c":   "int main(void) { return 0; }\n",
		"libs/pack/include/pack.h":       "int pack(void);\n",
		"libs/pack/src/pack.cpp":         "int pack() { return 1; }\n",
		"libs/pack/src/internal.h":       "\n",
		"app/main.c":                     "int main(void) { return 0; }\n",
		"third_party/zstd/placeholder.h": "\n",
	} {
		p := filepath.Join(f.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proj := map[string]json.RawMessage{"platform": json.RawMessage(`"` + hostPlatform() + `"`), "profile": json.RawMessage(`"release"`)}
	for _, n := range nixpkgs {
		node := &ir.Node{ID: n.id, Kind: "nixpkgs.tool", Label: "nixpkgs:" + n.attr, Nix: &ir.Nix{Attr: n.attr, Set: "host"}}
		outs := map[string]string{"out": storeOut(n.drv, "out")}
		if n.attr == "zlib" {
			node.Kind = "nixpkgs.lib"
			outs["dev"] = storeOut(n.drv, "dev")
		}
		f.g.Nodes = append(f.g.Nodes, node)
		f.in[n.id] = engine.Resolved{Drv: n.drv, Outputs: outs}
	}
	tools := []string{"gcc", "binutils", "patchelf"}
	target := func(id, kind, label string, t ccspec.Target, deps []string, srcs ...ir.Srcs) {
		t.Tools = ccspec.Tools{CC: 0, Binutils: 1, Patchelf: 2}
		if t.Deps == nil {
			t.Deps = []int{}
		}
		if t.PrivDeps == nil {
			t.PrivDeps = []int{}
		}
		spec, _ := json.Marshal(t)
		f.g.Nodes = append(f.g.Nodes, &ir.Node{ID: id, Kind: kind, Label: label, Proj: proj, Deps: append(slices.Clone(tools), deps...),
			Srcs: srcs, Spec: spec, DefinedAt: ir.Pos{File: labelDir(label) + "/bob.go", Line: 12}})
		f.srcs[id] = "/nix/store/x4aymyirkcksp9wa7v72r9yxzsk6yfwp-" + strings.ReplaceAll(labelDir(label), "/", "-") + "-src"
	}
	target("mathx", ccspec.KindLibrary, "//libs/mathx:mathx",
		ccspec.Target{Name: "mathx", Type: ccspec.TypeStatic, Std: "c17", Hdrs: "include"}, nil,
		ir.Srcs{Dir: "libs/mathx", Patterns: []string{"src/*.c", "include/**"}})
	target("mathx_test", ccspec.KindTest, "//libs/mathx:mathx_test",
		ccspec.Target{Name: "mathx_test", Type: ccspec.TypeTest, Deps: []int{3}}, []string{"mathx"},
		ir.Srcs{Dir: "libs/mathx", Patterns: []string{"test/mathx_test.c"}})
	target("pack", ccspec.KindLibrary, "//libs/pack:pack",
		ccspec.Target{Name: "pack", Type: ccspec.TypeShared, Std: "c++20", Hdrs: "include", Private: true,
			Defines: map[string]string{"B": "", "A": "1"}, Flags: []string{"-Wall"}, PrivDeps: []int{3}}, []string{"zlib"},
		ir.Srcs{Dir: "libs/pack", Patterns: []string{"src/*.cpp", "src/*.h", "include/**"}})
	ext := ccspec.External{Name: "zstd", URL: zstdURL, Strip: 1, Build: ccspec.BuildMake, Args: []string{"-C", "lib"},
		Libs: []string{"zstd"}, Include: "include", Tools: []int{3, 4}, Deps: []int{}, CC: ccspec.Tools{CC: 0, Binutils: 1, Patchelf: 2}}
	spec, _ := json.Marshal(ext)
	f.g.Nodes = append(f.g.Nodes, &ir.Node{ID: "zstd", Kind: ccspec.KindExternal, Label: "//third_party/zstd:zstd", Proj: proj,
		Deps: append(slices.Clone(tools), "bash", "gnumake"), Spec: spec, DefinedAt: ir.Pos{File: "third_party/zstd/bob.go", Line: 11}})
	target("app", ccspec.KindBinary, "//app:app",
		ccspec.Target{Name: "app", Type: ccspec.TypeBinary, Deps: []int{3, 4, 5}}, []string{"mathx", "pack", "zstd"},
		ir.Srcs{Dir: "app", Patterns: []string{"*.c"}})
	sec, err := lock.EncodeSection(lock.SectionCC, &lock.CCSection{Inputs: strings.Repeat("0", 64), Externals: map[string]lock.CExternal{
		"//third_party/zstd:zstd": {URL: zstdURL, Strip: 1, NarHash: narHash}}})
	if err != nil {
		t.Fatal(err)
	}
	f.lockf = sec
	return f
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

// plan plans the nodes ids in order (one wave each, as the engine's dependency waves would),
// feeding each result into the inputs of the next.
func (f *fixture) plan(t *testing.T, ids ...string) (map[string]*drv.Derivation, []fetch.Request, error) {
	t.Helper()
	drvs := map[string]*drv.Derivation{}
	var fetches []fetch.Request
	for _, id := range ids {
		res, err := Ecosystem{}.Plan(context.Background(), engine.PlanRequest{Graph: f.g, Nodes: []*ir.Node{f.node(id)},
			Lock: f.lockf, Inputs: f.in, Sources: f.srcs, Builder: builderP, Resolver: fakeResolver{}, Root: f.root})
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

// TestGenJobsDecode: every job decodes strictly into the builder's types, and the flags follow
// the order of §03.1.3.
func TestGenJobsDecode(t *testing.T) {
	f := newFixture(t)
	drvs, fetches, err := f.plan(t, "mathx", "pack", "zstd", "app", "mathx_test")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cc-mathx", "cc-pack", "cc-app", "cc-mathx_test", "ccrun-mathx_test", "ccx-zstd"} {
		if drvs[name] == nil {
			t.Fatalf("no derivation %s; have %v", name, keys(drvs))
		}
	}

	var pack bop.CompileLinkArgs
	j := job(t, drvs["cc-pack"], &pack)
	if j.Kind != "cc-compile-link" || j.Label != "//libs/pack:pack" || !j.Fixup.Strip || !j.Fixup.ShrinkRPath {
		t.Errorf("pack job header: %+v", j)
	}
	src := f.srcs["pack"]
	want := []string{"g++", "-std=c++20", "-O2", "-fPIC", "-ffile-prefix-map=" + src + "=.", "-DA=1", "-DB",
		"-Ilibs/pack/include", "-Ilibs/pack", "@cflags:0", "-Wall", "-c", "libs/pack/src/pack.cpp", "-o", "/build/obj/libs/pack/src/pack.cpp.o"}
	if len(pack.Compile) != 1 || !slices.Equal(pack.Compile[0].Argv, want) {
		t.Errorf("pack compile:\n got %q\nwant %q", pack.Compile, want)
	}
	if len(pack.PkgConfig) != 1 || pack.PkgConfig[0].Roots[0] != storeOut(nixpkgs[5].drv, "dev") {
		t.Errorf("pack pkg-config: %+v", pack.PkgConfig)
	}
	if l := pack.Link.Argv; !slices.Contains(l, "-shared") || !slices.Contains(l, "-Wl,-soname,libpack.so") || l[len(l)-1] != "@libs:0" {
		t.Errorf("pack link: %q", l)
	}
	checks, _ := json.Marshal(drvs["cc-pack"].Structured["outputChecks"])
	if !slices.Contains(j.Fixup.Forbidden, src) || !strings.Contains(string(checks), src) {
		t.Errorf("pack fixup does not forbid its sources: %v", j.Fixup.Forbidden)
	}

	var app bop.CompileLinkArgs
	job(t, drvs["cc-app"], &app)
	mathxOut, packOut, zstdOut := f.in["mathx"].Outputs["out"], f.in["pack"].Outputs["out"], f.in["zstd"].Outputs["out"]
	wantInc := []string{"-I" + mathxOut + "/include", "-I" + packOut + "/include", "-I" + zstdOut + "/include"}
	if a := app.Compile[0].Argv; !slices.Equal(a[4:7], wantInc) || a[1] != "-std=c17" || slices.Contains(a, "-fPIC") {
		t.Errorf("app compile: %q", a)
	}
	wantLink := []string{"gcc", "-o", drv.Placeholder("out") + "/bin/app", "/build/obj/app/main.c.o", mathxOut + "/lib/libmathx.a",
		"-L" + packOut + "/lib", "-lpack", "-Wl,-rpath," + packOut + "/lib",
		"-L" + zstdOut + "/lib", "-lzstd", "-Wl,-rpath," + zstdOut + "/lib"}
	if !slices.Equal(app.Link.Argv, wantLink) {
		t.Errorf("app link:\n got %q\nwant %q", app.Link.Argv, wantLink)
	}

	var mathx bop.CompileLinkArgs
	job(t, drvs["cc-mathx"], &mathx)
	if a := mathx.Link.Argv; a[0] != "ar" || len(mathx.Headers) != 1 || !strings.HasSuffix(mathx.Headers[0].From, "/libs/mathx/include") {
		t.Errorf("mathx: link %q headers %+v", a, mathx.Headers)
	}
	if li := f.in["mathx"].LinkInfo; li == nil || len(li.Archives) != 1 || li.IncludeDirs[0] != mathxOut+"/include" {
		t.Errorf("mathx LinkInfo: %+v", li)
	}
	if li := f.in["pack"].LinkInfo; li == nil || !slices.Equal(li.Libs, []string{"pack"}) || li.RuntimeDirs[0] != packOut+"/lib" {
		t.Errorf("pack LinkInfo: %+v", li)
	}

	var run bspec.TestRunArgs
	j = job(t, drvs["ccrun-mathx_test"], &run)
	if j.Kind != "test-run" || run.Format != "tap" || run.Argv[0] != outPath(drvs["cc-mathx_test"])+"/bin/mathx_test" {
		t.Errorf("test run: %+v %+v", j, run)
	}

	var x bop.ExternalArgs
	j = job(t, drvs["ccx-zstd"], &x)
	if j.Kind != "cc-external" || x.Build != "make" || !slices.Equal(x.Args, []string{"-C", "lib"}) || !strings.HasSuffix(x.Shell, "/bin/bash") {
		t.Errorf("external: %+v", x)
	}
	if len(fetches) != 1 || fetches[0].NarHash != narHash || fetches[0].Kind != fetch.Archive || x.Src == "" ||
		!strings.HasPrefix(fetches[0].Name, "ccx-src-zstd-") {
		t.Errorf("external fetch: %+v (src %s)", fetches, x.Src)
	}
}

// TestExternalNotPinned: an External without a lock entry, or with an entry for another URL,
// is BOB-E-1009 at its bob.go position (R-CC-005a).
func TestExternalNotPinned(t *testing.T) {
	for name, sec := range map[string]*lock.CCSection{
		"missing":   {Inputs: strings.Repeat("0", 64), Externals: map[string]lock.CExternal{}},
		"other url": {Inputs: strings.Repeat("0", 64), Externals: map[string]lock.CExternal{"//third_party/zstd:zstd": {URL: zstdURL + "x", Strip: 1, NarHash: narHash}}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			raw, err := lock.EncodeSection(lock.SectionCC, sec)
			if err != nil {
				t.Fatal(err)
			}
			f.lockf = raw
			_, _, err = f.plan(t, "zstd")
			var d *diag.Error
			if !errors.As(err, &d) || d.Code != 1009 || d.DefinedAt.File != "third_party/zstd/bob.go" || !strings.Contains(d.Error(), "//third_party/zstd:zstd") {
				t.Fatalf("got %v, want BOB-E-1009 for //third_party/zstd:zstd", err)
			}
		})
	}
}

// TestNoSources: Srcs matching no C/C++ file is BOB-E-1020.
func TestNoSources(t *testing.T) {
	f := newFixture(t)
	f.node("app").Srcs = []ir.Srcs{{Dir: "libs/pack", Patterns: []string{"src/*.h"}}}
	_, _, err := f.plan(t, "mathx", "pack", "zstd", "app")
	var d *diag.Error
	if !errors.As(err, &d) || d.Code != 1020 {
		t.Fatalf("got %v, want BOB-E-1020", err)
	}
}

// TestCompileCommands: one entry per source file, own headers in the working tree, sorted by
// file (§03.1.4).
func TestCompileCommands(t *testing.T) {
	f := newFixture(t)
	files, err := Ecosystem{}.Native(context.Background(), engine.PlanRequest{Graph: f.g, Root: f.root})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "compile_commands.json" || files[0].Committed {
		t.Fatalf("files: %+v", files)
	}
	var entries []compileCommand
	if err := json.Unmarshal(files[0].Content, &entries); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.File)
		if e.Directory != f.root || e.Output != "/dev/null" {
			t.Errorf("entry %+v", e)
		}
	}
	want := []string{"app/main.c", "libs/mathx/src/mathx.c", "libs/mathx/test/mathx_test.c", "libs/pack/src/pack.cpp"}
	if !slices.Equal(got, want) {
		t.Fatalf("files %q, want %q", got, want)
	}
	app := entries[0].Arguments
	wantApp := []string{"gcc", "-std=c17", "-O2", "-Ilibs/mathx/include", "-Ilibs/pack/include", "-c", "app/main.c", "-o", "/dev/null"}
	if !slices.Equal(app, wantApp) {
		t.Errorf("app/main.c:\n got %q\nwant %q", app, wantApp)
	}
	if none, _ := (Ecosystem{}).Native(context.Background(), engine.PlanRequest{Graph: f.g}); len(none) != 0 {
		t.Errorf("without a repository root Native wrote %d files", len(none))
	}
}

func keys(m map[string]*drv.Derivation) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
