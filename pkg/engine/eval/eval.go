package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/engine/eval/evalbox"
)

// DisabledLayer names the evaluator sandbox layer a test build removed (HARNESS.md §7.4
// layer-removal runs, build tags bob_nolandlock and bob_noseccomp). Empty in real builds.
const DisabledLayer = evalbox.DisabledLayer

// Request is one evaluation of a repository's bob.go files (SPEC §1.2, R-FW-043).
type Request struct {
	Root     string   // repository root: the directory holding bob.lock
	Patterns []string // label patterns; they become the IR roots
	Limits   Limits   // zero fields take the local defaults (R-FW-045)
}

// Limits bound one evaluation inside the evaluator sandbox (R-FW-045, S13 §5.3). Wall clock,
// IR output, stderr and memory limits always apply; with a delegated cgroup they are enforced
// by it (and Pids only there, otherwise by RLIMIT_NPROC).
type Limits struct {
	WallClock   time.Duration // default 120 s
	IROutput    int64         // bytes written to fd 3, default 64 MiB
	StderrKept  int64         // default 1 MiB
	Pids        int           // pids.max, default 512
	MemoryBytes int64         // memory.max incl. the /work tmpfs, default 2 GiB
	WorkBytes   int64         // /work tmpfs size, default 1 GiB
}

// Result is a finished evaluation.
type Result struct {
	IR     []byte // the IR JSON the evaluator wrote to fd 3, canonical (SPEC §1.6)
	Cached bool   // the eval key matched .bob/eval/key, so go build did not run
	Stderr []byte // evaluator diagnostics, capped at 1 MiB
}

// Sandbox paths of the evaluation (S13 §5.2).
const (
	srcDir       = "/src"
	frameworkDir = "/fw"
	evalBinary   = "/work/bob-eval"
)

// Evaluate generates (or reuses) the evaluator of r.Root and runs it in the evaluator sandbox
// (R-FW-045): go build and the evaluator both run inside it.
func Evaluate(ctx context.Context, r Request) (*Result, error) {
	root, err := filepath.Abs(r.Root)
	if err != nil {
		return nil, err
	}
	tc, err := findToolchain(ctx)
	if err != nil {
		return nil, err
	}
	fw, fwGo, err := findFramework()
	if err != nil {
		return nil, err
	}
	if err := generate(root, fwGo); err != nil {
		return nil, fmt.Errorf("generating the evaluator: %w", err)
	}
	seed, err := ensureSeed(ctx, tc, fw, fwGo)
	if err != nil {
		return nil, err
	}
	// The evaluator's request (pkg/bob Main protocol, R-FW-043.6): one JSON argument.
	req, err := json.Marshal(struct {
		Targets []string `json:"targets"`
	}{r.Patterns})
	if err != nil {
		return nil, err
	}
	l := r.Limits
	env := tc.env()
	build := evalbox.Step{
		Args: []string{tc.goroot + "/bin/go", "build", "-tags=bob", "-trimpath",
			"-modfile=" + srcDir + "/" + genDir + "/go.mod", "-overlay=" + srcDir + "/" + genDir + "/overlay.json",
			"-o", evalBinary, "./" + mainDir},
		Env: append(env[:len(env):len(env)], "GOWORK=off"),
	}
	out, err := evalbox.Run(ctx, evalbox.Spec{
		Binds: []evalbox.Bind{
			{Host: tc.goroot, Sandbox: tc.goroot, Exec: true},
			{Host: root, Sandbox: srcDir},
			{Host: fw, Sandbox: frameworkDir},
		},
		Seed:  seed,
		Dir:   srcDir,
		Steps: []evalbox.Step{build, {Args: []string{evalBinary, string(req)}, Env: env}},
		Limits: evalbox.Limits{WallClock: l.WallClock, IROutput: l.IROutput, StderrKept: l.StderrKept,
			Pids: l.Pids, MemoryBytes: l.MemoryBytes, WorkBytes: l.WorkBytes},
	})
	if err != nil {
		return nil, err
	}
	if len(out.Graph) == 0 {
		// The evaluator ran to the end but wrote no graph: its diagnostics say why.
		// R-FW-043 maps them to bob errors; until then they are shown as they are.
		return nil, &evalbox.Error{Msg: "evaluating bob.go failed", Detail: strings.TrimSpace(string(out.Stderr))}
	}
	return &Result{IR: out.Graph, Stderr: out.Stderr}, nil
}
