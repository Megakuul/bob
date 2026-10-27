package bob

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/internal/graph"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

func init() {
	graph.Register(graph.Kind{Name: "test.thing", Axes: []int{graph.AxisPlatform}})
	graph.Register(graph.Kind{Name: "test.prof", Axes: []int{graph.AxisPlatform, graph.AxisProfile, graph.AxisSanitizers}})
	graph.Register(graph.Kind{Name: "test.tool", Axes: []int{graph.AxisPlatform}, Host: true})
	graph.Register(graph.Kind{Name: "nixpkgs.lib", Axes: []int{graph.AxisPlatform}})
}

// testDir is the directory of this file; test files play the role of the repository's bob.go
// files (module example.com/t, this directory is its root).
func testDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// isolate gives the test a fresh registry and an environment in which _test.go files are bob.go
// files and Define may be called anywhere outside evaluation.
func isolate(t *testing.T) {
	t.Helper()
	env()
	oldEnv, oldReg := envVal, reg
	envVal = environment{
		mainModule: testDir(),
		bobVersion: "0.1.0",
		isBobFile:  func(f string) bool { return strings.HasSuffix(f, "_test.go") },
		isVarInit:  func(string) bool { return true },
	}
	reg = &registry{}
	t.Cleanup(func() { envVal, reg = oldEnv, oldReg })
}

type thing struct{ Node }

func mk(p *P, kind, name string, deps ...Package) *thing {
	return &thing{Node: newNode(p, kind, name, deps, nil, map[string]string{"name": name})}
}

// evalOK runs the registry for targets and returns the IR, failing t on errors.
func evalOK(t *testing.T, targets ...string) []byte {
	t.Helper()
	ir, diags := run(reg, env(), request{Targets: targets, Host: "linux-arm64", Root: testDir()})
	if len(diags) > 0 {
		t.Fatalf("evaluation failed: %+v", diags)
	}
	return ir
}

// evalErr runs the registry for targets and returns the messages of the errors.
func evalErr(t *testing.T, targets ...string) []string {
	t.Helper()
	ir, diags := run(reg, env(), request{Targets: targets, Host: "linux-arm64", Root: testDir()})
	if ir != nil {
		t.Fatalf("evaluation succeeded, want errors:\n%s", ir)
	}
	var out []string
	for _, d := range diags {
		out = append(out, d.msg)
	}
	return out
}

type doc struct {
	Configs []struct{ ID string }
	Nodes   []struct {
		ID, Kind, Label string
		DefinedAt       struct {
			File string
			Line int
		}
		Proj map[string]any
		Deps []string
		Spec json.RawMessage
		Srcs []struct {
			Dir      string
			Patterns []string
		}
	}
	Roots []string
}

func decode(t *testing.T, ir []byte) doc {
	t.Helper()
	var d doc
	if err := json.Unmarshal(ir, &d); err != nil {
		t.Fatalf("IR is not JSON: %v\n%s", err, ir)
	}
	return d
}

func lineOf(t *testing.T, needle string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testDir(), "eval_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, needle) && !strings.Contains(l, "lineOf(") {
			return i + 1
		}
	}
	t.Fatalf("no line contains %q", needle)
	return 0
}

// R-FW-011: one constructor call per (definition, P); Host().Host() == Host().
func TestDefineMemoisesPerP(t *testing.T) {
	isolate(t)
	calls := 0
	lib := Define(func(p *P) *thing { calls++; return mk(p, "test.thing", "lib") })
	var hosts []*P
	use := Define(func(p *P) *thing {
		a, b := lib(p), lib(p)
		if a != b {
			t.Errorf("lib(p) returned two values for one P")
		}
		hosts = append(hosts, p.Host(), p.Host().Host())
		return mk(p, "test.thing", "use", a)
	})
	_ = use
	ir := evalOK(t, "//:use@linux-amd64", "//:use@linux-arm64")
	if calls != 2 {
		t.Errorf("constructor ran %d times for two P, want 2", calls)
	}
	for i := 0; i+1 < len(hosts); i += 2 {
		if hosts[i] != hosts[i+1] || hosts[i].Platform() != LinuxARM64 || hosts[i].Variant().Profile != Release {
			t.Errorf("Host() = %+v, Host().Host() = %+v", hosts[i], hosts[i+1])
		}
	}
	d := decode(t, ir)
	if len(d.Nodes) != 4 || len(d.Roots) != 2 || len(d.Configs) != 2 {
		t.Errorf("want 4 nodes, 2 roots, 2 configs:\n%s", ir)
	}
}

// R-FW-046 projection: release and debug project to one node for a platform-only kind; a kind
// that reads the profile gets two, and its dependants inherit the axis.
func TestProjection(t *testing.T) {
	isolate(t)
	plat := Define(func(p *P) *thing { return mk(p, "test.thing", "plat") })
	prof := Define(func(p *P) *thing { return mk(p, "test.prof", "prof") })
	Define(func(p *P) *thing { return mk(p, "test.thing", "top", plat(p), prof(p)) })
	ir := evalOK(t, "//:top@linux-arm64", "//:top@linux-arm64+debug")
	var ids []string
	for _, n := range decode(t, ir).Nodes {
		ids = append(ids, n.ID)
	}
	want := []string{"//:plat@linux-arm64", "//:prof@linux-arm64", "//:prof@linux-arm64+debug", "//:top@linux-arm64", "//:top@linux-arm64+debug"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Errorf("node ids %v, want %v", ids, want)
	}
}

// R-FW-016: definedAt is the line of the call into the framework in the bob.go (here: this
// test file), and the label directory is that file's directory.
func TestDefinedAtIsCallLine(t *testing.T) {
	isolate(t)
	Define(func(p *P) *Step {
		return Run(p, "step", RunOpts{ // definedAt: this line
			Cmd: []string{"true"},
		})
	})
	d := decode(t, evalOK(t, "//:step"))
	n := d.Nodes[0]
	if n.DefinedAt.File != "eval_test.go" || n.DefinedAt.Line != lineOf(t, "definedAt: this line") {
		t.Errorf("definedAt %+v, want eval_test.go:%d", n.DefinedAt, lineOf(t, "definedAt: this line"))
	}
	if n.Label != "//:step" {
		t.Errorf("label %q", n.Label)
	}
}

// R-FW-015: a second package with one label is reported with both positions.
func TestDuplicateLabel(t *testing.T) {
	isolate(t)
	Define(func(p *P) *Step { return Run(p, "x", RunOpts{Cmd: []string{"true"}}) })
	Define(func(p *P) *Step { return Run(p, "x", RunOpts{Cmd: []string{"false"}}) })
	msgs := evalErr(t, "//:x")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "package //:x is already defined at eval_test.go:") {
		t.Errorf("errors %q", msgs)
	}
	// Identical packages of two calls are one node, not a duplicate.
	isolate(t)
	tool := func(p *P) *thing { return mk(p, "nixpkgs.lib", "zlib") }
	Define(func(p *P) *thing { return mk(p, "test.thing", "a", tool(p)) })
	Define(func(p *P) *thing { return mk(p, "test.thing", "b", tool(p)) })
	evalOK(t, "//...")
}

// R-FW-011: a cycle between constructors is BOB-E-1006 naming the chain by labels.
func TestCycle(t *testing.T) {
	isolate(t)
	var bRef func(*P) *Step
	a := Define(func(p *P) *Step {
		return Run(p, "a", RunOpts{Cmd: []string{"true"}, Inputs: map[string]Tree{"b": bRef(p)}})
	})
	bRef = Define(func(p *P) *Step {
		return Run(p, "b", RunOpts{Cmd: []string{"true"}, Inputs: map[string]Tree{"a": a(p)}})
	})
	msgs := evalErr(t, "//:a")
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], "dependency cycle: //:a (eval_test.go:") || !strings.Contains(msgs[0], "→ //:b (eval_test.go:") {
		t.Errorf("errors %q", msgs)
	}
}

// R-FW-011: bob.Define inside a constructor and a constructor called before evaluation.
func TestDefineMisuse(t *testing.T) {
	isolate(t)
	Define(func(p *P) *Step {
		inner := Define(func(q *P) *Step { return Run(q, "inner", RunOpts{Cmd: []string{"true"}}) }) // nested Define
		_ = inner(p)
		return Run(p, "outer", RunOpts{Cmd: []string{"true"}})
	})
	msgs := evalErr(t, "//:outer")
	want := "eval_test.go:" + itoa(lineOf(t, "// nested Define")) + ": bob.Define must be used in a package-level var declaration"
	if len(msgs) != 1 || msgs[0] != want {
		t.Errorf("errors %q, want %q", msgs, want)
	}

	isolate(t)
	lib := Define(func(p *P) *Step { return Run(p, "lib", RunOpts{Cmd: []string{"true"}}) })
	if v := lib(nil); v != nil { // early call
		t.Errorf("an early call returned %v", v)
	}
	msgs = evalErr(t, "//:lib")
	want = "bob: eval_test.go:" + itoa(lineOf(t, "// early call")) + ": lib was called before evaluation; call it only inside another bob.Define function"
	if len(msgs) != 1 || msgs[0] != want {
		t.Errorf("errors %q, want %q", msgs, want)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// Errors of constructors that the targets do not need are not reported.
func TestIrrelevantErrorsAreIgnored(t *testing.T) {
	isolate(t)
	Define(func(p *P) *Step { return Run(p, "good", RunOpts{Cmd: []string{"true"}}) })
	Define(func(p *P) *Step { return Run(p, "bad", RunOpts{}) }) // empty Cmd: BOB-E-1020
	evalOK(t, "//:good")
	msgs := evalErr(t, "//...")
	if len(msgs) != 1 || msgs[0] != "//:bad: invalid RunOpts: Cmd is empty" {
		t.Errorf("errors %q", msgs)
	}
	// A label that matches nothing may be the package of a failed constructor: the errors of
	// its directory are reported with it.
	msgs = evalErr(t, "//:nothing")
	if len(msgs) != 2 || !strings.Contains(msgs[0], "pattern //:nothing matches no package") {
		t.Errorf("errors %q", msgs)
	}
}

// A panic in a constructor is a definition error of that constructor.
func TestConstructorPanic(t *testing.T) {
	isolate(t)
	Define(func(p *P) *Step {
		panic("boom")
	})
	msgs := evalErr(t, "//...")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "panicked: boom") {
		t.Errorf("errors %q", msgs)
	}
}

// Golden IR of a small graph: run steps, a file output, a fetch, a host tool, sources, a named
// variant and a pipeline. The IR is canonical JSON.
func TestGoldenIR(t *testing.T) {
	isolate(t)
	Repo(RepoOpts{Nixpkgs: NixOS2605, Variants: []Named{NamedVariant("fast", WithProfile(Debug), WithSanitize(UBSan))}})
	Pipeline("ci", PipelineOpts{Triggers: []Trigger{OnPush("main"), OnPullRequest()}, Concurrency: Supersede, Targets: []Pattern{"//..."}})
	tool := Define(func(p *P) *thing { return mk(p, "test.tool", "gen") })
	src := Define(func(p *P) Tree {
		return Fetch(p, FetchOpts{URL: "https://example.org/zlib-1.3.1.tar.gz", Archive: true, Strip: 1})
	})
	step := Define(func(p *P) *Step {
		return Run(p, "version", RunOpts{
			Tools:   []Tool{tool(p.Host())},
			Srcs:    Glob("*.go", "!*_test.go").Union(Files("go.mod"), Dir("testdata")),
			Inputs:  map[string]Tree{"zlib": src(p)},
			Cmd:     []string{"gen", "${BOB_OUT}/v.txt"},
			Env:     map[string]string{"A": "<b>"},
			Outputs: []string{"v.txt"},
		})
	})
	Define(func(p *P) *thing { return mk(p, "test.prof", "lib", step(p).File("v.txt")) })
	ir := evalOK(t, "//...", "//:lib@linux-amd64+fast")
	if c, err := canonicalJSON(ir); err != nil || !bytes.Equal(c, ir) {
		t.Errorf("IR is not canonical: %v", err)
	}
	got := bytes.ReplaceAll(ir, []byte(testDir()), []byte("example.com/t"))
	golden := filepath.Join(testDir(), "testdata", "ir.golden.json")
	if *update {
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), got) {
		t.Errorf("IR differs from %s (go test -run TestGoldenIR -update):\n%s", golden, got)
	}
}

// A nil tool or input that no earlier error explains is reported.
func TestNilInput(t *testing.T) {
	isolate(t)
	Define(func(p *P) *Step {
		return Run(p, "s", RunOpts{Cmd: []string{"true"}, Inputs: map[string]Tree{"x": (*Step)(nil)}})
	})
	msgs := evalErr(t, "//:s")
	if len(msgs) != 1 || !strings.HasSuffix(msgs[0], `: bob.Run "s": input "x" is nil`) || !strings.HasPrefix(msgs[0], "eval_test.go:") {
		t.Errorf("errors %q", msgs)
	}
}
