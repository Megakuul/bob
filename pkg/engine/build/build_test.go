package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/store"
	_ "github.com/megakuul/bob/pkg/nixpkgs" // the nixpkgs kinds (the CLI links them the same way)
)

// demo is a test ecosystem (kind demo.thing): one "run" job per node that writes
// "<label> built" plus its dependencies' results into $out/result.txt.
// seen keeps the demo derivations planned in the same Plan call (the engine's resolver knows
// only those of earlier waves).
type demo struct{ seen modMap }

func init() { engine.Register(demo{seen: modMap{}}) }

// chain resolves the demo's own drvs first, everything else through the engine.
type chain struct {
	own  modMap
	rest drv.Resolver
}

func (c chain) Modulo(p string) ([32]byte, error) {
	if d, ok := c.own[p]; ok {
		return d.Modulo(c)
	}
	return c.rest.Modulo(p)
}

func (demo) Name() string { return "demo" }
func (demo) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (demo) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

func (e demo) Plan(_ context.Context, req engine.PlanRequest) (engine.PlanResult, error) {
	res := engine.PlanResult{Nodes: map[string]engine.Resolved{}}
	for _, n := range req.Nodes {
		if n.Kind == "demo.test" {
			if err := e.planTest(req, n, &res); err != nil {
				return res, err
			}
			continue
		}
		out := drv.Placeholder("out")
		steps := []any{step("write", map[string]any{"path": out + "/result.txt", "content": n.Label + " built\n"}),
			step("write", map[string]any{"path": out + "/spec.json", "content": string(n.Spec)})}
		job := drv.Job{Name: drv.Name("demo", n.Label), System: store.HostSystem(), Builder: req.Builder,
			InputSrcs: []string{req.Builder}, ExtraAttribute: map[string]any{"allowSubstitutes": false},
			Spec: map[string]any{"v": 1, "kind": "run", "label": n.Label, "outputs": []any{"out"},
				"args": map[string]any{"steps": steps}, "fixup": emptyFixup()}}
		for i, dep := range n.Deps {
			in, ok := req.Inputs[dep]
			if !ok {
				return res, fmt.Errorf("demo: dependency %s of %s was not planned", dep, n.ID)
			}
			job.InputDrvs = append(job.InputDrvs, drv.InputDrv{Path: in.Drv, Outputs: []string{"out"}})
			steps = append(steps, step("copy", map[string]any{"from": in.Outputs["out"] + "/result.txt", "to": fmt.Sprintf("%s/dep%d.txt", out, i)}))
		}
		job.Spec.(map[string]any)["args"] = map[string]any{"steps": steps}
		d, err := job.Derivation()
		if err != nil {
			return res, err
		}
		if err := d.Finalize(chain{e.seen, req.Resolver}); err != nil {
			return res, err
		}
		p, err := d.Path()
		if err != nil {
			return res, err
		}
		e.seen[p] = d
		res.Drvs = append(res.Drvs, d)
		res.Nodes[n.ID] = engine.Resolved{Drv: p, Outputs: map[string]string{"out": d.Outputs[0].Path}}
	}
	return res, nil
}

// planTest plans a demo.test node: a test-run unit running `bash -c <cmd>` (deps[0] = bash).
func (e demo) planTest(req engine.PlanRequest, n *ir.Node, res *engine.PlanResult) error {
	var s struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return err
	}
	bash := req.Inputs[n.Deps[0]]
	job := drv.Job{Name: drv.Name("demo-test", n.Label), System: store.HostSystem(), Builder: req.Builder,
		InputSrcs: []string{req.Builder}, InputDrvs: []drv.InputDrv{{Path: bash.Drv, Outputs: []string{"out"}}},
		ExtraAttribute: map[string]any{"allowSubstitutes": false},
		Spec: map[string]any{"v": 1, "kind": "test-run", "label": n.Label, "outputs": []any{"out"},
			"args":  map[string]any{"argv": []any{"bash", "-c", s.Cmd}, "path": []any{bash.Outputs["out"] + "/bin"}},
			"fixup": emptyFixup()}}
	d, err := job.Derivation()
	if err != nil {
		return err
	}
	if err := d.Finalize(chain{e.seen, req.Resolver}); err != nil {
		return err
	}
	p, err := d.Path()
	if err != nil {
		return err
	}
	e.seen[p] = d
	res.Drvs = append(res.Drvs, d)
	res.Nodes[n.ID] = engine.Resolved{Drv: p, Outputs: map[string]string{"out": d.Outputs[0].Path}}
	return nil
}

type modMap map[string]*drv.Derivation

func (m modMap) Modulo(p string) ([32]byte, error) {
	d, ok := m[p]
	if !ok {
		return [32]byte{}, fmt.Errorf("unknown drv %s", p)
	}
	return d.Modulo(m)
}

func demoRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo")
	files := map[string]string{
		"go.mod":   "module example.com/demo\n\ngo 1.26.0\n",
		"bob.lock": "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":   "//go:build bob\n\npackage demo\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n",
		"d/bob.go": `//go:build bob

package d

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
)

func init() { ext.Register(ext.Kind{Name: "demo.thing", Axes: []ext.Axis{ext.AxisPlatform}}) }

type Thing struct{ bob.Node }

type spec struct {
	Salt string ` + "`json:\"salt\"`" + `
}

func thing(p *bob.P, name string, deps ...bob.Package) *Thing {
	return &Thing{Node: ext.New(p, "demo.thing", name, deps, nil, spec{Salt: "SALT"})}
}

var A = bob.Define(func(p *bob.P) *Thing { return thing(p, "a") })
var B = bob.Define(func(p *bob.P) *Thing { return thing(p, "b", A(p)) })
`,
	}
	salt := fmt.Sprint(time.Now().UnixNano())
	for p, c := range files {
		c = strings.ReplaceAll(c, "SALT", salt)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	home := os.Getenv("BOB_TEST_BUILD_HOME") // reuse a warm home between runs (nixpkgs source)
	if home == "" {
		home = filepath.Join(t.TempDir(), "bobhome")
	}
	st, err := store.Open(context.Background(), store.Config{Home: home})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return st
}

func runBuild(t *testing.T, o Options) (*Result, []Event) {
	t.Helper()
	var evs []Event
	o.Events = func(e Event) { evs = append(evs, e) }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	res, err := Run(ctx, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, evs
}

func named(evs []Event, name string) []Event {
	var out []Event
	for _, e := range evs {
		if e.Event == name {
			out = append(out, e)
		}
	}
	return out
}

func describe(res *Result) string {
	var b strings.Builder
	for _, e := range res.Report.Errors {
		fmt.Fprintf(&b, "%s | cause %s | %v | raw %v\n", e.Error(), e.Cause, e.Excerpt, e.Raw)
	}
	return b.String()
}

// The whole pipeline with an ecosystem from the registry: stage events in R-FW-049 order,
// both packages built with the expected outputs; a second run is cached and imports no drv.
func TestRunEcosystemEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates and builds in a real store")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go for the evaluator")
	}
	st := testStore(t)
	dir := demoRepo(t)
	res, evs := runBuild(t, Options{Root: dir, Patterns: []string{"//d:b"}, Store: st, PrintPaths: true})
	if res.ExitCode != 0 {
		t.Fatalf("exit %d:\n%s", res.ExitCode, describe(res))
	}
	var stages []string
	isStage := map[string]bool{}
	for _, s := range []string{StageScan, StageEval, StageIR, StageLock, StageIndex, StageSources, StagePlan, StageFetch, StageDrv, StageNix, StageValidity, StageTranslate} {
		isStage[s] = true
	}
	for _, e := range evs {
		if isStage[e.Event] {
			stages = append(stages, e.Event)
		}
	}
	if got, want := strings.Join(stages, ","), "scan,eval,ir,lock,index,sources,plan,fetch,drv,nix,validity,translate"; got != want {
		t.Errorf("stages %s, want %s", got, want)
	}
	if evs[len(evs)-1].Event != "run.finish" || evs[len(evs)-1].ExitCode != 0 {
		t.Errorf("last event %+v", evs[len(evs)-1])
	}
	if rs := named(evs, "run.start"); len(rs) != 1 || strings.Join(rs[0].Requested, ",") != "//d:b" {
		t.Errorf("run.start %+v", rs)
	}
	if im := named(evs, "drv.imported"); len(im) != 1 || im[0].Count != 2 {
		t.Errorf("drv.imported %+v, want count 2", im)
	}
	paths := map[string]string{}
	for _, e := range named(evs, "package.finish") {
		if e.Status != "built" {
			t.Errorf("%s: %s, want built", e.Label, e.Status)
		}
		if len(e.Paths) == 1 {
			paths[e.Label] = e.Paths[0]
		}
	}
	for _, l := range []string{"//d:a", "//d:b"} {
		b, err := os.ReadFile(filepath.Join(paths[l], "result.txt"))
		if err != nil || string(b) != l+" built\n" {
			t.Errorf("%s result.txt = %q (%v)", l, b, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(paths["//d:b"], "dep0.txt")); err != nil || string(b) != "//d:a built\n" {
		t.Errorf("//d:b dep0.txt = %q (%v)", b, err)
	}
	if len(res.Packages) != 1 || res.Packages[0].Label != "//d:b" || res.Packages[0].Status != "built" {
		t.Errorf("packages %+v", res.Packages)
	}

	res, evs = runBuild(t, Options{Root: dir, Patterns: []string{"//d:b"}, Store: st})
	if res.ExitCode != 0 {
		t.Fatalf("second run exit %d:\n%s", res.ExitCode, describe(res))
	}
	if im := named(evs, "drv.imported"); len(im) != 1 || im[0].Count != 0 {
		t.Errorf("second run drv.imported %+v, want 0", im)
	}
	for _, e := range named(evs, "package.finish") {
		if e.Status != "cached" {
			t.Errorf("second run %s: %s, want cached", e.Label, e.Status)
		}
	}
}

// writeRepo writes files below a new repository directory.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	for p, c := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// bob.Run steps with nixpkgs tools through the whole pipeline: nixpkgs is pinned on demand,
// the index resolves the tools, sources are copied to /build/src, an input of another step is
// at $BOB_IN_<NAME>, and a failing step is a build error of its label (exit 1).
// Needs network for nixpkgs; set BOB_TEST_NIXPKGS=1 (and BOB_TEST_BUILD_HOME to keep a warm
// home between runs).
func TestRunStepsWithNixpkgsTools(t *testing.T) {
	if os.Getenv("BOB_TEST_NIXPKGS") == "" {
		t.Skip("set BOB_TEST_NIXPKGS=1 (downloads nixpkgs)")
	}
	st := testStore(t)
	salt := fmt.Sprint(time.Now().UnixNano())
	dir := writeRepo(t, map[string]string{
		"go.mod":        "module example.com/st\n\ngo 1.26.0\n",
		"bob.lock":      "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":        "//go:build bob\n\npackage st\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n",
		"s/data/in.txt": "hello from the source tree\n",
		"s/bob.go": `//go:build bob

package s

import (
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

func tools(p *bob.P) []bob.Tool { return []bob.Tool{nixpkgs.Tool(p, "bash"), nixpkgs.Tool(p, "coreutils")} }

var First = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "first", bob.RunOpts{
		Tools: tools(p), Srcs: bob.Glob("data/*.txt"), Env: map[string]string{"SALT": "` + salt + `"},
		Cmd:     []string{"bash", "-c", "cat s/data/in.txt > \"$BOB_OUT/copy.txt\"; pwd > \"$BOB_OUT/pwd.txt\"; echo ${SALT} > \"$BOB_OUT/salt.txt\""},
		Outputs: []string{"copy.txt"},
	})
})

var Second = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "second", bob.RunOpts{
		Tools: tools(p), Inputs: map[string]bob.Tree{"prev": First(p).File("copy.txt")},
		Cmd:   []string{"bash", "-c", "tr a-z A-Z < \"$BOB_IN_PREV\" > \"$BOB_OUT/upper.txt\""},
	})
})

var Missing = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "missing", bob.RunOpts{
		Tools: tools(p), Env: map[string]string{"SALT": "` + salt + `"},
		Cmd:   []string{"bash", "-c", "echo hi > \"$BOB_OUT/other.txt\""}, Outputs: []string{"promised.txt"},
	})
})

var Slow = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "slow", bob.RunOpts{
		Tools: tools(p), Env: map[string]string{"SALT": "` + salt + `"},
		Cmd:   []string{"sleep", "60"}, Timeout: time.Second,
	})
})

var Fails = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "fails", bob.RunOpts{
		Tools: tools(p), Env: map[string]string{"SALT": "` + salt + `"},
		Cmd:   []string{"bash", "-c", "echo broken step >&2; exit 3"},
	})
})
`,
	})
	res, evs := runBuild(t, Options{Root: dir, Patterns: []string{"//s:second"}, Store: st, PrintPaths: true})
	if res.ExitCode != 0 {
		t.Fatalf("exit %d:\n%s", res.ExitCode, describe(res))
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "bob.lock")); !strings.Contains(string(b), `"rev"`) {
		t.Errorf("nixpkgs was not pinned on demand:\n%s", b)
	}
	paths := map[string]string{}
	for _, e := range named(evs, "package.finish") {
		if len(e.Paths) > 0 {
			paths[e.Label] = e.Paths[0]
		}
	}
	read := func(label, f string) string {
		b, err := os.ReadFile(filepath.Join(paths[label], f))
		if err != nil {
			t.Errorf("%s %s: %v", label, f, err)
		}
		return string(b)
	}
	if got := read("//s:second", "upper.txt"); got != "HELLO FROM THE SOURCE TREE\n" {
		t.Errorf("upper.txt = %q", got)
	}
	if got := read("//s:first", "pwd.txt"); got != "/build/src\n" {
		t.Errorf("pwd.txt = %q", got)
	}
	if got := read("//s:first", "salt.txt"); got != salt+"\n" {
		t.Errorf("salt.txt = %q (${VAR} expansion)", got)
	}

	res, _ = runBuild(t, Options{Root: dir, Patterns: []string{"//s:fails"}, Store: st})
	t.Logf("failing step:\n%s", describe(res))
	if res.ExitCode != 1 || len(res.Report.Errors) == 0 || res.Report.Errors[0].Label != "//s:fails" {
		t.Errorf("failing step: exit %d, errors:\n%s", res.ExitCode, describe(res))
	}
	res, _ = runBuild(t, Options{Root: dir, Patterns: []string{"//s:missing"}, Store: st})
	if res.ExitCode != 1 || len(res.Report.Errors) == 0 || res.Report.Errors[0].Cause != "//s:missing: step finished but did not create promised.txt" {
		t.Errorf("missing declared output: exit %d, errors:\n%s", res.ExitCode, describe(res))
	}
	start := time.Now()
	res, _ = runBuild(t, Options{Root: dir, Patterns: []string{"//s:slow"}, Store: st})
	if res.ExitCode != 1 || len(res.Report.Errors) == 0 || res.Report.Errors[0].Code != 2007 || time.Since(start) > 40*time.Second {
		t.Errorf("step timeout: exit %d after %s, errors:\n%s", res.ExitCode, time.Since(start), describe(res))
	}
}

// R-FW-020 at the engine level: a chain of 100 trivial steps builds cold in ≤ 12 s once the
// tool closure is present (the tests/core acceptance runs it through the CLI).
func TestRunChainOf100Steps(t *testing.T) {
	if os.Getenv("BOB_TEST_NIXPKGS") == "" {
		t.Skip("set BOB_TEST_NIXPKGS=1 (downloads nixpkgs)")
	}
	st := testStore(t)
	salt := fmt.Sprint(time.Now().UnixNano())
	var b strings.Builder
	b.WriteString("//go:build bob\n\npackage chain\n\nimport (\n\t\"github.com/megakuul/bob/pkg/bob\"\n\t\"github.com/megakuul/bob/pkg/nixpkgs\"\n)\n\n")
	for i := 0; i < 100; i++ {
		in := "nil"
		if i > 0 {
			in = fmt.Sprintf("map[string]bob.Tree{\"prev\": s%03d(p)}", i-1)
		}
		fmt.Fprintf(&b, "var s%03d = bob.Define(func(p *bob.P) *bob.Step {\n\treturn bob.Run(p, \"s%03d\", bob.RunOpts{Tools: []bob.Tool{nixpkgs.Tool(p, \"coreutils\")}, Cmd: []string{\"true\"}, Inputs: %s, Env: map[string]string{\"N\": \"%s-%d\"}})\n})\n\n", i, i, in, salt, i)
	}
	fmt.Fprintf(&b, "var warm = bob.Define(func(p *bob.P) *bob.Step {\n\treturn bob.Run(p, \"warm\", bob.RunOpts{Tools: []bob.Tool{nixpkgs.Tool(p, \"coreutils\")}, Cmd: []string{\"true\"}, Env: map[string]string{\"N\": \"%s-warm\"}})\n})\n", salt)
	dir := writeRepo(t, map[string]string{
		"go.mod":       "module example.com/chain\n\ngo 1.26.0\n",
		"bob.lock":     "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":       "//go:build bob\n\npackage chain\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n",
		"chain/bob.go": b.String(),
	})
	if res, _ := runBuild(t, Options{Root: dir, Patterns: []string{"//chain:warm"}, Store: st}); res.ExitCode != 0 {
		t.Fatalf("warm-up exit %d:\n%s", res.ExitCode, describe(res))
	}
	start := time.Now()
	res, evs := runBuild(t, Options{Root: dir, Patterns: []string{"//chain:s099"}, Store: st})
	d := time.Since(start)
	if res.ExitCode != 0 {
		t.Fatalf("exit %d:\n%s", res.ExitCode, describe(res))
	}
	for _, e := range evs {
		if e.DurationMs > 0 && e.Label == "" && e.Event != "run.finish" {
			t.Logf("stage %s: %d ms", e.Event, e.DurationMs)
		}
	}
	t.Logf("cold chain of 100 steps: %s, built %d", d.Round(time.Millisecond), res.Report.Counts.Built)
	if d > 12*time.Second {
		t.Errorf("cold chain of 100 steps took %s, want ≤ 12 s", d.Round(time.Millisecond))
	}
}

// bob test: test units run once and are cached afterwards, --rerun runs them again, and a
// failing unit fails the run with exit 1 and a test.result "fail".
func TestRunTests(t *testing.T) {
	if os.Getenv("BOB_TEST_NIXPKGS") == "" {
		t.Skip("set BOB_TEST_NIXPKGS=1 (downloads nixpkgs)")
	}
	st := testStore(t)
	salt := fmt.Sprint(time.Now().UnixNano())
	dir := writeRepo(t, map[string]string{
		"go.mod":   "module example.com/tt\n\ngo 1.26.0\n",
		"bob.lock": "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":   "//go:build bob\n\npackage tt\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n",
		"t/bob.go": `//go:build bob

package t

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

func init() { ext.Register(ext.Kind{Name: "demo.test", Axes: []ext.Axis{ext.AxisPlatform}}) }

type spec struct {
	Cmd string ` + "`json:\"cmd\"`" + `
}

func test(p *bob.P, name, cmd string) bob.Node {
	return ext.New(p, "demo.test", name, []bob.Package{nixpkgs.Tool(p, "bash")}, nil, spec{Cmd: cmd})
}

var Ok = bob.Define(func(p *bob.P) bob.Node { return test(p, "ok", "echo ok ` + salt + `") })
var Bad = bob.Define(func(p *bob.P) bob.Node { return test(p, "bad", "echo failing ` + salt + `; exit 1") })
`,
	})
	o := Options{Command: CmdTest, Root: dir, Patterns: []string{"//t:ok"}, Store: st}
	status := func(evs []Event) string {
		var s []string
		for _, e := range named(evs, "test.result") {
			s = append(s, e.Label+"="+e.Status)
		}
		return strings.Join(s, ",")
	}
	res, evs := runBuild(t, o)
	if res.ExitCode != 0 || status(evs) != "//t:ok=pass" {
		t.Fatalf("first run: exit %d, %s\n%s", res.ExitCode, status(evs), describe(res))
	}
	res, evs = runBuild(t, o)
	if res.ExitCode != 0 || status(evs) != "//t:ok=cached" {
		t.Errorf("second run: exit %d, %s\n%s", res.ExitCode, status(evs), describe(res))
	}
	o.Rerun = true
	res, evs = runBuild(t, o)
	if res.ExitCode != 0 || status(evs) != "//t:ok=pass" {
		t.Errorf("--rerun: exit %d, %s\n%s", res.ExitCode, status(evs), describe(res))
	}
	res, evs = runBuild(t, Options{Command: CmdTest, Root: dir, Patterns: []string{"//t:bad"}, Store: st})
	if res.ExitCode != 1 || status(evs) != "//t:bad=fail" {
		t.Errorf("failing test: exit %d, %s\n%s", res.ExitCode, status(evs), describe(res))
	}
	// bob build does not run tests.
	res, evs = runBuild(t, Options{Root: dir, Patterns: []string{"//t:bad"}, Store: st})
	if res.ExitCode != 0 || len(named(evs, "test.result")) != 0 {
		t.Errorf("bob build of a test: exit %d\n%s", res.ExitCode, describe(res))
	}
}

// Without go on PATH the evaluator uses nixpkgs' go (ADR 0003): from the default channel
// before bob.lock has a pin (that pin is then recorded), from the pin afterwards.
func TestRunPinnedGo(t *testing.T) {
	if os.Getenv("BOB_TEST_NIXPKGS") == "" {
		t.Skip("set BOB_TEST_NIXPKGS=1 (downloads nixpkgs)")
	}
	st := testStore(t)
	dir := demoRepo(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	for i := 0; i < 2; i++ {
		res, _ := runBuild(t, Options{Root: dir, Patterns: []string{"//d:b"}, Store: st})
		if len(res.Report.Errors) > 0 && res.Report.Errors[0].Code == 1021 {
			// nixpkgs' go was used but is older than the framework's go directive (I-M4d NEEDS).
			t.Skipf("nixpkgs go is older than the framework needs: %v", res.Report.Errors[0])
		}
		if res.ExitCode != 0 {
			t.Fatalf("run %d: exit %d:\n%s", i, res.ExitCode, describe(res))
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "bob.lock")); !strings.Contains(string(b), `"rev"`) {
		t.Errorf("the nixpkgs pin used for go was not recorded:\n%s", b)
	}
}

// SourcePaths: one <name>-src store path per package with sources; ignored files and bob.go
// edits do not move it, a source edit does.
func TestSourcePaths(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates in a real sandbox")
	}
	st := testStore(t)
	dir := writeRepo(t, map[string]string{
		"go.mod":       "module example.com/sp\n\ngo 1.26.0\n",
		"bob.lock":     "{\n  \"lockVersion\": 1\n}\n",
		"bob.go":       "//go:build bob\n\npackage sp\n\nimport \"github.com/megakuul/bob/pkg/bob\"\n\nvar _ = bob.Repo(bob.RepoOpts{Nixpkgs: \"nixos-26.05\"})\n",
		"a/.gitignore": "ignored.txt\n",
		"a/x.txt":      "x\n",
		"a/bob.go": `//go:build bob

package a

import "github.com/megakuul/bob/pkg/bob"

// S has sources.
var S = bob.Define(func(p *bob.P) *bob.Step {
	return bob.Run(p, "s", bob.RunOpts{Srcs: bob.Glob("*.txt"), Cmd: []string{"true"}})
})
`,
	})
	get := func() map[string]string {
		t.Helper()
		m, err := SourcePaths(context.Background(), Request{Root: dir, Patterns: []string{"//..."}, Home: st.Home()})
		if err != nil {
			t.Fatalf("SourcePaths: %v", err)
		}
		return m
	}
	before := get()
	if len(before) != 1 {
		t.Fatalf("source paths %v, want one", before)
	}
	for _, p := range before {
		if !strings.HasPrefix(p, "/nix/store/") || !strings.HasSuffix(p, "-a-s-src") {
			t.Errorf("source path %q", p)
		}
		if _, err := os.Stat(filepath.Join(st.Home(), p, "a", "x.txt")); err != nil {
			t.Errorf("imported tree lacks a/x.txt: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "ignored.txt"), []byte("i\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := get(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("an ignored file moved the source path: %v → %v", before, after)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "x.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := get(); fmt.Sprint(after) == fmt.Sprint(before) {
		t.Errorf("a source edit did not move the source path")
	}
}

// emptyFixup is the fixup contract of the demo jobs: no binary fixup.
func emptyFixup() map[string]any {
	return map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []any{},
		"tools": map[string]any{"strip": "", "objcopy": "", "patchelf": ""}}
}
