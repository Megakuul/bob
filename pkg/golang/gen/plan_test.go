package gen

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/golang/bop"
	"github.com/megakuul/bob/pkg/golang/internal/gospec"
)

// fakeStore computes source paths without a store.
type fakeStore struct{ imported int }

func (fakeStore) HostPath(p string) string { return p }

func (f *fakeStore) ImportSources(_ context.Context, trees []srcs.Source) ([]string, error) {
	out := make([]string, len(trees))
	for i, t := range trees {
		p, err := srcs.Path(t.Name, t.Tree)
		if err != nil {
			return nil, err
		}
		out[i] = p
		f.imported++
	}
	return out, nil
}

func (fakeStore) Ensure(context.Context, fetch.Request) (string, error) {
	return "", errors.New("no network in this test")
}

// fakeResolver gives every foreign derivation a fixed modulo hash.
type fakeResolver struct{}

func (fakeResolver) Modulo(p string) ([32]byte, error) { return sha256.Sum256([]byte(p)), nil }

const (
	goOut    = "/nix/store/x1qs8rk2sry6ph7jfgwwiaw1lcrq3346-go-1.26.8"
	goDrv    = "/nix/store/qn7vkgbl057risx25si7zqbwnlm6iw7g-go-1.26.8.drv"
	gccOut   = "/nix/store/23jyj7s1b10awhkkilbfxsaq3g9i1gdi-gcc-wrapper-15.3.0"
	gccDrv   = "/nix/store/0kv4jl8i2a71wf3y2297qdgq57x92kyk-gcc-14.3.0.drv"
	builderP = "/nix/store/sm9f61327ycyq4jhdfglc6yk7hd74xjf-bob-builder-2"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// testGraph is a graph with module example.com/m at m/, binary m/cmd/x and a test unit of
// m/lib/a, on the host platform.
func testGraph(t *testing.T) *ir.Graph {
	plat := rawJSON(t, hostPlatformName())
	mod := &ir.Node{ID: "//m:module", Kind: gospec.KindModule, Label: "//m:module", DefinedAt: ir.Pos{File: "m/bob.go", Line: 9},
		Deps: []string{"nixpkgs:go_1_26"}, Spec: rawJSON(t, gospec.Module{Path: "example.com/m", Go: "1.26",
			Tags: []string{}, Require: []gospec.Requirement{}, Generated: []gospec.Generated{}})}
	build := gospec.Build{Module: 0, CC: 1, Cgo: []int{}, Tags: []string{}, CGo: gospec.CgoAuto}
	bin := &ir.Node{ID: "//m/cmd/x:x", Kind: gospec.KindBinary, Label: "//m/cmd/x:x", DefinedAt: ir.Pos{File: "m/bob.go", Line: 13},
		Proj: map[string]json.RawMessage{"platform": plat}, Deps: []string{"//m:module", "nixpkgs:gcc"},
		Spec: rawJSON(t, gospec.Binary{Build: build, Dir: "cmd/x", Name: "x", X: map[string]string{"main.version": "7"}, Mode: gospec.ModeExe})}
	unit := &ir.Node{ID: "//m/lib/a:go_test", Kind: gospec.KindTest, Label: "//m/lib/a:go_test", DefinedAt: ir.Pos{File: "m/bob.go", Line: 17},
		Proj: map[string]json.RawMessage{"platform": plat}, Deps: []string{"//m:module", "nixpkgs:gcc"},
		Spec: rawJSON(t, gospec.Test{Build: build, Dir: "lib/a", Args: []string{}})}
	return &ir.Graph{Module: "example.com/r", Nodes: []*ir.Node{mod, bin, unit}}
}

func testRequest(t *testing.T, root string, g *ir.Graph, st engine.PlanStore) engine.PlanRequest {
	return engine.PlanRequest{Graph: g, Nodes: g.Nodes, Root: root, Store: st, Builder: builderP, Resolver: fakeResolver{},
		Inputs: map[string]engine.Resolved{
			"nixpkgs:go_1_26": {Drv: goDrv, Outputs: map[string]string{"out": goOut}},
			"nixpkgs:gcc":     {Drv: gccDrv, Outputs: map[string]string{"out": gccOut}},
			"//m:module":      {},
		}}
}

func testRepo(t *testing.T) string {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"m/lib/a/a.go":       "package a\n\nimport \"strings\"\n\nfunc Up(s string) string { return strings.ToUpper(s) }\n",
		"m/lib/a/a_test.go":  "package a\n\nimport \"testing\"\n\nfunc TestUp(t *testing.T) {}\n\nfunc Example() {\n\t// Output:\n}\n",
		"m/lib/a/x_test.go":  "package a_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/m/lib/a\"\n)\n\nfunc TestX(t *testing.T) { _ = a.Up(\"x\") }\n",
		"m/cmd/x/main.go":    "package main\n\nimport (\n\t_ \"embed\"\n\t\"fmt\"\n\n\t\"example.com/m/lib/a\"\n)\n\n//go:embed banner.txt\nvar banner string\n\nvar version = \"dev\"\n\nfunc main() { fmt.Println(a.Up(banner), version) }\n",
		"m/cmd/x/banner.txt": "hi\n",
		"m/cmd/x/notes.md":   "not a source\n",
	})
	return root
}

func TestPlanBinaryAndTest(t *testing.T) {
	root := testRepo(t)
	g := testGraph(t)
	st := &fakeStore{}
	res, err := Ecosystem{}.Plan(context.Background(), testRequest(t, root, g, st))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	names := map[string]*drv.Derivation{}
	for _, d := range res.Drvs {
		if names[d.Name] != nil {
			t.Errorf("two derivations named %s", d.Name)
		}
		names[d.Name] = d
	}
	var got []string
	for n := range names {
		got = append(got, n)
	}
	sort.Strings(got)
	want := []string{"go-example.com-m-cmd-x", "go-example.com-m-lib-a", "go-link-x", "go-std-1.26.8-" + strings.ReplaceAll(hostPlatformName(), "-", "-"),
		"go-test-example.com-m-lib-a", "go-test-example.com-m-lib-a-run"}
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("derivations\n got %v\nwant %v", got, want)
	}
	bin := res.Nodes["//m/cmd/x:x"]
	if bin.MainProgram != "bin/x" || bin.Drv == "" || !strings.HasSuffix(bin.Outputs["out"], "-go-link-x") {
		t.Errorf("binary resolved to %+v", bin)
	}
	unit := res.Nodes["//m/lib/a:go_test"]
	if d := names["go-test-example.com-m-lib-a-run"]; d == nil || unit.Outputs["out"] != engine.OutPath(d) {
		t.Errorf("test unit resolved to %+v", unit)
	}
	// The main package's job: -p main, embed of banner.txt only, a pure-Go group without cc.
	spec := jobJSON(t, names["go-example.com-m-cmd-x"])
	for _, want := range []string{`"flags":["-p","main","-lang=go1.26"]`, `"banner.txt"`, `"kind":"go-compile"`, `"cc":""`} {
		if !strings.Contains(spec, want) {
			t.Errorf("main package job lacks %s:\n%s", want, spec)
		}
	}
	if strings.Contains(spec, "notes.md") {
		t.Errorf("the main package job names a file that is not a source: %s", spec)
	}
	// The link names -X and the archives; the binary is pure Go (no external link).
	link := jobJSON(t, names["go-link-x"])
	for _, want := range []string{`"x":["main.version=7"]`, `"external":false`, `"mode":"exe"`} {
		if !strings.Contains(link, want) {
			t.Errorf("link job lacks %s:\n%s", want, link)
		}
	}
	// The test job compiles the package with its tests, the external test package and the test main.
	tj := jobJSON(t, names["go-test-example.com-m-lib-a"])
	for _, want := range []string{`"importPath":"example.com/m/lib/a_test"`, `"_testmain.go"`, `{\"TestUp\", _test.TestUp}`,
		`{\"TestX\", _xtest.TestX}`, `"kind":"go-testmain"`} {
		if !strings.Contains(tj, want) {
			t.Errorf("test job lacks %s:\n%s", want, tj)
		}
	}
	run := jobJSON(t, names["go-test-example.com-m-lib-a-run"])
	if !strings.Contains(run, `"format":"go"`) || !strings.Contains(run, `"label":"//m/lib/a:go_test"`) || !strings.Contains(run, `a.test","-test.v=true"`) {
		t.Errorf("test-run job:\n%s", run)
	}
}

// R-GO-002a: a change of the main package changes only the main package's and the link's
// derivations; a library change also changes its importers.
func TestPlanGranularity(t *testing.T) {
	plan := func(root string) map[string]string {
		res, err := Ecosystem{}.Plan(context.Background(), testRequest(t, root, testGraph(t), &fakeStore{}))
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		out := map[string]string{}
		for _, d := range res.Drvs {
			p, _ := d.Path()
			out[d.Name] = p
		}
		return out
	}
	root := testRepo(t)
	before := plan(root)
	changed := func(after map[string]string) []string {
		var c []string
		for n, p := range after {
			if before[n] != p {
				c = append(c, n)
			}
		}
		sort.Strings(c)
		return c
	}
	writeTree(t, root, map[string]string{"m/cmd/x/main.go": "package main\n\nimport (\n\t_ \"embed\"\n\t\"fmt\"\n\n\t\"example.com/m/lib/a\"\n)\n\n//go:embed banner.txt\nvar banner string\n\nvar version = \"dev\"\n\nfunc main() { fmt.Println(a.Up(banner), version, 1) }\n"})
	if c := changed(plan(root)); strings.Join(c, " ") != "go-example.com-m-cmd-x go-link-x" {
		t.Errorf("main.go change: changed %v, want the main package and the link", c)
	}
	before = plan(root)
	writeTree(t, root, map[string]string{"m/lib/a/a.go": "package a\n\nimport \"strings\"\n\nfunc Up(s string) string { return strings.ToUpper(s) + \"\" }\n"})
	want := "go-example.com-m-cmd-x go-example.com-m-lib-a go-link-x go-test-example.com-m-lib-a go-test-example.com-m-lib-a-run"
	if c := changed(plan(root)); strings.Join(c, " ") != want {
		t.Errorf("a.go change: changed %v, want %s", c, want)
	}
}

func TestPlanImportWithoutModule(t *testing.T) {
	root := testRepo(t)
	writeTree(t, root, map[string]string{"m/lib/a/a.go": "package a\n\nimport \"github.com/nobody/nothing\"\n\nvar _ = nothing.X\n"})
	_, err := Ecosystem{}.Plan(context.Background(), testRequest(t, root, testGraph(t), &fakeStore{}))
	if err == nil || !strings.Contains(err.Error(), "github.com/nobody/nothing") || !strings.Contains(err.Error(), "golang.Require") {
		t.Fatalf("want a resolve error naming the import and golang.Require, got %v", err)
	}
}

func jobJSON(t *testing.T, d *drv.Derivation) string {
	t.Helper()
	if d == nil {
		t.Fatal("derivation missing")
	}
	b, err := json.Marshal(d.Structured["bobSpec"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hostPlatformName() string {
	goos, goarch := hostGo()
	return goos + "-" + goarch
}

// TestJobsDecode decodes every job the planner writes with bob-builder's own types, strictly:
// the generator cannot import the builder layer (R-ARCH-003), so this test keeps the two in
// step.
func TestJobsDecode(t *testing.T) {
	res, err := Ecosystem{}.Plan(context.Background(), testRequest(t, testRepo(t), testGraph(t), &fakeStore{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, d := range res.Drvs {
		spec, _ := d.Structured["bobSpec"].(map[string]any)
		kind, _ := spec["kind"].(string)
		raw, err := json.Marshal(spec["args"])
		if err != nil {
			t.Fatal(err)
		}
		var v any
		switch kind {
		case "go-std":
			v = &bop.StdArgs{}
		case "go-tools":
			v = &bop.ToolsArgs{}
		case "go-compile", "go-cgo", "go-link", "go-testmain":
			v = &bop.BuildArgs{}
		case "test-run":
			v = &bspec.TestRunArgs{}
		default:
			t.Errorf("%s: unknown kind %q", d.Name, kind)
			continue
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			t.Errorf("%s (%s): %v\n%s", d.Name, kind, err, raw)
		}
		if _, ok := bop.Kinds()[kind]; !ok && kind != "test-run" {
			t.Errorf("%s: bob-builder has no kind %s", d.Name, kind)
		}
	}
}

// The library package is one derivation for the binary and for the test that both use it.
func TestPlanSharesPackages(t *testing.T) {
	root := testRepo(t)
	writeTree(t, root, map[string]string{
		"m/lib/b/b.go":      "package b\n\nimport \"example.com/m/lib/a\"\n\n// B uses a.\nfunc B() string { return a.Up(\"b\") }\n",
		"m/lib/b/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) { _ = B() }\n",
		"m/cmd/x/main.go":   "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/m/lib/b\"\n)\n\nfunc main() { fmt.Println(b.B()) }\n",
	})
	g := testGraph(t)
	unit := *g.Nodes[2]
	unit.ID, unit.Label = "//m/lib/b:go_test", "//m/lib/b:go_test"
	unit.Spec = rawJSON(t, gospec.Test{Build: gospec.Build{Module: 0, CC: 1, Cgo: []int{}, Tags: []string{}, CGo: gospec.CgoAuto}, Dir: "lib/b", Args: []string{}})
	g.Nodes = append(g.Nodes, &unit)
	res, err := Ecosystem{}.Plan(context.Background(), testRequest(t, root, g, &fakeStore{}))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	n := 0
	for _, d := range res.Drvs {
		if d.Name == "go-example.com-m-lib-a" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("package example.com/m/lib/a has %d derivations, want 1 shared by the binary and both tests", n)
	}
}
