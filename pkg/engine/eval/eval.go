package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/engine/eval/evalbox"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// DisabledLayer names the evaluator sandbox layer a test build removed (HARNESS.md §7.4
// layer-removal runs, build tags bob_nolandlock and bob_noseccomp). Empty in real builds.
const DisabledLayer = evalbox.DisabledLayer

// Request is one evaluation of a repository's bob.go files (SPEC §1.2, R-FW-043).
type Request struct {
	Root     string   // repository root: the directory holding bob.lock
	Patterns []string // label patterns, optionally with "@<platform>[+variant…]"; the IR roots
	Limits   Limits   // zero fields take the local defaults (R-FW-045)
	// GoRoot is the pinned Go toolchain (its GOROOT, e.g. a store path realised from the
	// nixpkgs pin). Empty: the go on PATH, which must be at least the framework's Go version.
	GoRoot string
	// Untrusted marks a repository bob does not trust (a worker evaluating pushed commits):
	// directory replace targets of its go.mod must stay inside Root (review F2).
	Untrusted bool
	// Strict runs the evaluator sandbox in worker mode (evalbox.Spec.Strict, S13-3): no
	// evaluation without a delegated cgroup and Landlock ABI >= 4.
	Strict bool
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

func (l Limits) box() evalbox.Limits {
	return evalbox.Limits{WallClock: l.WallClock, IROutput: l.IROutput, StderrKept: l.StderrKept,
		Pids: l.Pids, MemoryBytes: l.MemoryBytes, WorkBytes: l.WorkBytes}
}

// Result is a finished evaluation.
type Result struct {
	IR     []byte // the IR JSON the evaluator wrote to fd 3, canonical (SPEC §1.6)
	Cached bool   // the eval key matched .bob/eval/key, so go build did not run
	Stderr []byte // evaluator diagnostics, capped at 1 MiB
	// Scan is the time of the repository scan and eval key (R-FW-002 walk, hashes): the
	// "scan" stage of R-FW-049; the rest of Evaluate is the "eval" stage.
	Scan time.Duration
}

// Sandbox paths of the evaluation (S13 §5.2).
const (
	srcDir  = "/src"     // the repository, read-only
	binDir  = "/evalbin" // the evaluator binary
	modDir  = "/gomod"   // the evaluator's module cache (the framework)
	evalBin = "bob-eval"
)

// Files below .bob/eval.
const (
	keyFile   = "key"
	lockFile  = "lock"
	indexFile = "index.json"
	binSub    = "bin"
	gomodSub  = "gomod"
	cacheSub  = "gocache"
)

// Evaluate generates (or reuses) the evaluator of r.Root and runs it in the evaluator sandbox
// (R-FW-045). Definition errors (rule violations, compile errors, the evaluator's own
// diagnostics) are returned as ir.Errors; sandbox limits as *evalbox.Error.
func Evaluate(ctx context.Context, r Request) (*Result, error) {
	root, err := filepath.Abs(r.Root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return nil, fmt.Errorf("%w: %v", errNoGoMod, err)
	}
	tc, err := findToolchain(ctx, r.GoRoot)
	if err != nil {
		return nil, err
	}
	fw, err := loadFramework()
	if err != nil {
		return nil, err
	}
	if err := tc.supports(fw.goDir); err != nil {
		return nil, err
	}
	gen := filepath.Join(root, genDir)
	if err := checkGenDir(root); err != nil {
		return nil, err
	}
	// Concurrent evaluations of one repository (a build and an editor's shim): one generates and
	// compiles at a time (the GOCACHE upper layer can be mounted only once); runs share. The
	// lock is taken before anything below .bob/eval is read: claimGenDir discards state that
	// bob did not write there (a committed .bob/, A201).
	if err := os.MkdirAll(gen, 0o755); err != nil {
		return nil, err
	}
	lf, err := claimGenDir(gen)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lf.Close() }()
	scanStart := time.Now()
	scan, err := scanRepo(root)
	if err != nil {
		return nil, err
	}
	prevIndex := readIndex(filepath.Join(gen, indexFile))
	index, err := scan.hashes(root, prevIndex, false)
	if err != nil {
		return nil, err
	}
	if err := addReplaceKeys(root, prevIndex, index, r.Untrusted); err != nil {
		return nil, err
	}
	key := evalKey(index, fw.version, fw.h1, tc.version, runtime.GOARCH)
	scanTime := time.Since(scanStart)
	cached := false
	if old, err := os.ReadFile(filepath.Join(gen, keyFile)); err == nil && string(old) == key+"\n" {
		if fi, err := os.Stat(filepath.Join(gen, binSub, evalBin)); err == nil && fi.Mode().IsRegular() {
			cached = true
		}
	}
	if !cached {
		if err := compile(ctx, r, root, tc, fw, scan); err != nil {
			return nil, err
		}
		if err := writeIfChanged(filepath.Join(gen, keyFile), []byte(key+"\n")); err != nil {
			return nil, err
		}
	}
	if err := writeIndex(filepath.Join(gen, indexFile), prevIndex, index); err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil { // other runs may start
		return nil, fmt.Errorf("locking %s: %w", lf.Name(), err)
	}
	out, err := run(ctx, r, root, tc)
	if err != nil {
		return nil, err
	}
	if v := irBobVersion(out.Graph); v != fw.version {
		// R-FW-044: only a broken R-FW-043.4 can get here.
		return nil, ir.Errors{ir.EvaluatorBug("the evaluator is bob %s, this is bob %s", v, fw.version)}
	}
	return &Result{IR: out.Graph, Cached: cached, Stderr: out.Stderr, Scan: scanTime}, nil
}

// compile checks the rules, generates the evaluator's sources and compiles them in the sandbox
// into .bob/eval/bin/bob-eval.
func compile(ctx context.Context, r Request, root string, tc toolchain, fw *framework, scan *repoScan) error {
	if err := scan.load(root); err != nil {
		return err
	}
	rootMod := parseGoMod(scan.content["go.mod"])
	if rootMod.module == "" {
		return fmt.Errorf("%s has no module line", filepath.Join(root, "go.mod"))
	}
	own := map[string]*bobFile{}
	for _, d := range scan.bobDirs {
		own[d] = parseBob(d, bobPath(d), scan.content[bobPath(d)])
	}
	if err := addBobImports(&rootMod, own); err != nil {
		return err
	}
	deps, err := resolveDeps(ctx, tc, root, rootMod, own, fw.version, r.Untrusted)
	if err != nil {
		return err
	}
	fwPkgs := map[string]bool{}
	for _, p := range fw.packages() {
		fwPkgs[p] = true
	}
	rc := &ruleCheck{root: root, module: rootMod.module, scan: scan, own: own, deps: deps, fwPkgs: fwPkgs}
	violations, stubs := rc.check()
	if len(violations) > 0 {
		return violations
	}
	for _, d := range stubs {
		if err := writeCgoStub(root, d, own[d].pkg); err != nil {
			return err
		}
	}
	// A root go.mod may name a patch release above the pinned Go (a hand-written "go 1.26.9"
	// with nixpkgs go 1.26.8): the evaluator's go.mod then names the toolchain's own version,
	// which GOTOOLCHAIN=local accepts (A157).
	if have := strings.TrimPrefix(tc.version, "go"); rootMod.goDir != "" && versionLess(have, rootMod.goDir) {
		rootMod.goDir = have
	}
	g, err := generate(root, rootMod, scan, own, deps, fw)
	if err != nil {
		return fmt.Errorf("generating the evaluator: %w", err)
	}
	gen := filepath.Join(root, genDir)
	modcache := filepath.Join(gen, gomodSub)
	if err := fw.install(modcache); err != nil {
		return fmt.Errorf("installing the bob framework for the evaluator: %w", err)
	}
	seed, err := ensureSeed(ctx, tc, fw, modcache)
	if err != nil {
		return err
	}
	bin := filepath.Join(gen, binSub)
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(bin, evalBin))
	cache := filepath.Join(gen, cacheSub)
	out, err := evalbox.Run(ctx, evalbox.Spec{
		Binds: []evalbox.Bind{
			{Host: tc.goroot, Sandbox: tc.goroot, Exec: true},
			{Host: root, Sandbox: srcDir},
			{Host: modcache, Sandbox: modDir},
			{Host: bin, Sandbox: binDir, Writable: true},
		},
		Seed:     seed,
		CacheDir: cache,
		Dir:      srcDir,
		Strict:   r.Strict,
		Steps: []evalbox.Step{{
			Args: []string{tc.goroot + "/bin/go", "build", "-tags=bob", "-trimpath",
				"-modfile=" + sandboxPath(genDir, "go.mod"), "-overlay=" + sandboxPath(genDir, "overlay.json"),
				"-o", binDir + "/" + evalBin, "./" + mainDir},
			Env: tc.buildEnv(),
		}},
		Limits: r.Limits.box(),
	})
	if err != nil {
		var be *evalbox.Error
		if errors.As(err, &be) && out != nil && strings.HasPrefix(be.Msg, "evaluating bob.go failed (exit") {
			return compileErrors(out.Stderr, g, deps)
		}
		return err
	}
	return nil
}

// run runs the evaluator binary in a fresh sandbox; it sees the repository and the toolchain
// (read-only), its own binary and an empty /work, with the S13 §5.2 environment.
func run(ctx context.Context, r Request, root string, tc toolchain) (*evalbox.Outcome, error) {
	req, err := json.Marshal(struct {
		Targets []string `json:"targets"`
	}{r.Patterns})
	if err != nil {
		return nil, err
	}
	out, err := evalbox.Run(ctx, evalbox.Spec{
		Binds: []evalbox.Bind{
			{Host: tc.goroot, Sandbox: tc.goroot},
			{Host: root, Sandbox: srcDir},
			{Host: filepath.Join(root, genDir, binSub), Sandbox: binDir, Exec: true},
		},
		Dir:    srcDir,
		Steps:  []evalbox.Step{{Args: []string{binDir + "/" + evalBin, string(req)}, Env: tc.env()}},
		Limits: r.Limits.box(),
		Strict: r.Strict,
	})
	if err != nil {
		return nil, err
	}
	if len(out.Graph) == 0 {
		// The evaluator ran to the end but wrote no graph: its diagnostics say why.
		errs, other := ir.Diagnostics(out.Stderr, func(f string) string { return f })
		if len(errs) == 0 {
			msg := "the evaluator stopped without writing the build graph"
			if len(other) > 0 {
				msg += ": " + other[0]
			}
			errs = ir.Errors{ir.DefinitionError("", 0, 0, "", msg)}
			errs[0].Excerpt = tail(other, 12)
		}
		return nil, errs
	}
	return out, nil
}

// irBobVersion reads the bobVersion field of an IR document ("" if there is none). Canonical
// IR has it first, so the scan stops early.
func irBobVersion(b []byte) string {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return ""
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return ""
		}
		if k == "bobVersion" {
			var v string
			if dec.Decode(&v) != nil {
				return ""
			}
			return v
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return ""
		}
	}
	return ""
}

// writeCgoStub materialises <dir>/zz_bob_cgo.go (rule R5) and keeps it out of git.
func writeCgoStub(root, dir, pkg string) error {
	rel := filepath.ToSlash(filepath.Join(dir, "zz_bob_cgo.go"))
	content := []byte("//go:build bob && cgo\n\npackage " + pkg + "\n\nimport \"C\"\n")
	if err := writeIfChanged(filepath.Join(root, filepath.FromSlash(rel)), content); err != nil {
		return err
	}
	return excludeFromGit(root, rel)
}

// lock opens p and takes an exclusive flock lock on it; closing the file releases it. The
// lock belongs to the open file, so it is converted to shared on the same descriptor.
func lock(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o644) // never a committed link's target
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking %s: %w", p, err)
	}
	return f, nil
}

// tail returns the last n elements of s.
func tail(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
