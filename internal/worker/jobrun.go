package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/megakuul/bob/internal/build/jobspec"
	"github.com/megakuul/bob/internal/cache"
	"github.com/megakuul/bob/internal/fleet"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/build"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/store"
)

// The job directory of a job container (bound at /work, SPEC §05.8): the worker process
// prepares the source in src/ before the container starts; the runner leaves its result in
// out/ for the worker process, which stages it (the container holds no credentials).
const (
	workSrc    = "src"
	workOut    = "out"
	resultFile = "result.json"
	reportFile = "eval.json"
	// maxResult bounds out/result.json as the worker process reads it.
	maxResult = 64 << 20
	// maxStagedPaths bounds the paths of one job's closure (COMPLETE.json allows 100,000).
	maxStagedPaths = 100_000
)

// RunResult is out/result.json: what the runner built, for the worker process to stage.
type RunResult struct {
	Failed []string   `json:"failed,omitempty"` // failing labels
	Paths  []PathInfo `json:"paths,omitempty"`  // the closure of the built outputs
	// Outputs are the outputs of the built (or cached) requested packages: the worker process
	// stages the OCI images and releases among them (R-OUT-013, §6.8).
	Outputs []Output `json:"outputs,omitempty"`
}

// Output is one output of a requested package.
type Output struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// PathInfo is one store path of the closure, as Nix reports it.
type PathInfo struct {
	Path       string   `json:"path"`
	NarHash    string   `json:"narHash"` // sha256:<nix32>
	NarSize    uint64   `json:"narSize"`
	References []string `json:"references,omitempty"`
}

// EngineRunner runs a job inside its job container with pkg/engine/build: evaluate the
// repository untrusted in the strict evaluator sandbox, match the pipelines of the build's
// selection, build (and test) their targets with the worker's store at /nix, and leave the
// evaluation report and the output closure in /work/out.
type EngineRunner struct {
	Work string // the job directory ("" = /work)
	Home string // the engine's state home ("" = JobHome)
}

// JobHome is where a job container sees the engine state home of its slot.
const JobHome = "/bobhome"

var _ Runner = EngineRunner{}

// Run implements Runner.
func (r EngineRunner) Run(ctx context.Context, j Job, out Output) Outcome {
	work := r.Work
	if work == "" {
		work = "/work"
	}
	spec, ok := fleet.DecodeSpec(j.Spec)
	if !ok {
		return Outcome{Status: "failed", Error: "job spec unreadable"}
	}
	sel, err := jobspec.ParseSelection(spec.Targets)
	if err != nil {
		return Outcome{Status: "failed", Error: err.Error()}
	}
	if err := os.MkdirAll(filepath.Join(work, workOut), 0o755); err != nil {
		return Outcome{Status: "infra", Error: err.Error()}
	}
	home := r.Home
	if home == "" {
		home = JobHome
	}
	st, err := openJobStore(ctx, home)
	if err != nil {
		return Outcome{Status: "infra", Error: "worker store: " + err.Error()}
	}
	ev := &events{out: out, start: map[string]time.Time{}}
	o := build.Options{Root: filepath.Join(work, workSrc), Store: st, Untrusted: true, Strict: true,
		Variants: sel.Variants, JSONLog: true, Events: ev.handle, Version: sel.Version}
	if p, _, _ := strings.Cut(spec.Class, "."); p != "" && p != jobspec.HostPlatform() {
		o.Platform = p
	}
	rep := &jobspec.Report{V: 1, Pipelines: []jobspec.Pipeline{}, Matched: []string{}, Targets: []string{}, Tests: []string{},
		Packages: []jobspec.PackageReport{}, Errors: []jobspec.ErrorReport{}}
	targets, tests := sel.Labels, []string(nil)
	if sel.Test {
		targets, tests = nil, sel.Labels
	}
	var releases []string // release labels of the matched release pipelines (built, not reported as targets)
	if sel.RunsPipelines() {
		eo := o
		eo.Patterns = []string{"//..."}
		g, err := build.Evaluate(ctx, eo)
		if err != nil {
			return r.finish(work, rep, nil, failure(err))
		}
		for _, raw := range g.Pipelines {
			var p jobspec.Pipeline
			if err := json.Unmarshal(raw, &p); err != nil {
				return r.finish(work, rep, nil, Outcome{Status: "failed", Error: "pipeline definition unreadable: " + err.Error()})
			}
			rep.Pipelines = append(rep.Pipelines, p)
		}
		matched, err := jobspec.Match(rep.Pipelines, sel)
		if err != nil {
			return r.finish(work, rep, nil, Outcome{Status: "failed", Error: err.Error()})
		}
		rep.Matched = append(rep.Matched, matched...)
		targets, tests = jobspec.Targets(rep.Pipelines, matched)
		releases = releaseLabels(g, rep.Pipelines, matched)
	}
	rep.Targets = append(rep.Targets, targets...)
	rep.Tests = append(rep.Tests, tests...)
	res := &RunResult{}
	outcome := Outcome{Status: "ok"}
	var outputs []string
	for _, step := range []struct {
		cmd      build.Command
		patterns []string
	}{{build.CmdBuild, union(targets, releases)}, {build.CmdTest, tests}} {
		if len(step.patterns) == 0 {
			continue
		}
		bo := o
		bo.Command, bo.Patterns = step.cmd, step.patterns
		result, err := build.Run(ctx, bo)
		if err != nil {
			return r.finish(work, rep, res, Outcome{Status: "infra", Error: err.Error()})
		}
		for _, p := range result.Packages {
			rep.Packages = append(rep.Packages, jobspec.PackageReport{Label: p.Label, Status: p.Status, Outputs: sortedValues(p.Outputs)})
			if p.Status == "built" || p.Status == "cached" {
				for _, path := range sortedValues(p.Outputs) {
					outputs = append(outputs, path)
					res.Outputs = append(res.Outputs, Output{Label: p.Label, Path: path})
				}
			}
			if p.Status == "failed" {
				res.Failed = append(res.Failed, p.Label)
			}
		}
		for _, t := range result.Tests {
			rep.Packages = append(rep.Packages, jobspec.PackageReport{Label: t.Label, Status: t.Status})
			if t.Status == "fail" {
				res.Failed = append(res.Failed, t.Label)
			}
		}
		for _, e := range result.Report.Errors {
			if len(rep.Errors) < jobspec.MaxReportErrors {
				rep.Errors = append(rep.Errors, errorReport(e, result.Report.Hint(e)))
			}
		}
		if result.ExitCode != 0 && outcome.Status == "ok" {
			outcome = Outcome{Status: "failed", Error: firstError(result)}
		}
	}
	if len(rep.Packages) > jobspec.MaxReportPackages {
		rep.Packages = rep.Packages[:jobspec.MaxReportPackages]
	}
	if outcome.Status == "ok" && len(outputs) > 0 {
		if res.Paths, err = closure(ctx, st, outputs); err != nil {
			return r.finish(work, rep, res, Outcome{Status: "infra", Error: "closure of the outputs: " + err.Error()})
		}
	}
	return r.finish(work, rep, res, outcome)
}

// releaseLabels returns the labels of the releases the matched pipelines publish: an entry
// is a label ("//rel:api") or a release name ("api", the name of a release.release node).
// An entry without a node is left to the build controller, which fails the build with
// "release <entry> of pipeline <p> was not built".
func releaseLabels(g *ir.Graph, ps []jobspec.Pipeline, matched []string) []string {
	var out []string
	for _, p := range ps {
		if !slices.Contains(matched, p.Name) {
			continue
		}
		for _, entry := range p.Releases {
			for _, n := range g.Nodes {
				if n.Kind != build.KindRelease {
					continue
				}
				_, name, _ := strings.Cut(n.Label, ":")
				if n.Label == entry || !strings.HasPrefix(entry, "//") && name == entry {
					out = union(out, []string{n.Label})
				}
			}
		}
	}
	return out
}

// union returns a followed by the entries of b it does not contain.
func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// finish writes out/eval.json and out/result.json and returns o (infra when they cannot be
// written: the worker could not stage anything).
func (r EngineRunner) finish(work string, rep *jobspec.Report, res *RunResult, o Outcome) Outcome {
	if res == nil {
		res = &RunResult{}
	}
	if o.Status != "ok" && len(rep.Errors) == 0 && o.Error != "" {
		rep.Errors = append(rep.Errors, jobspec.ErrorReport{Code: codeOf(o.Error), Message: firstLineOf(o.Error)})
	}
	for name, v := range map[string]any{reportFile: rep, resultFile: res} {
		b, err := json.Marshal(v)
		if err == nil {
			err = os.WriteFile(filepath.Join(work, workOut, name), b, 0o644)
		}
		if err != nil {
			return Outcome{Status: "infra", Error: "write " + name + ": " + err.Error()}
		}
	}
	return o
}

// failure turns an evaluation error into a failed outcome with the §02 error text.
func failure(err error) Outcome {
	var de *diag.Error
	if errors.As(err, &de) {
		return Outcome{Status: "failed", Error: de.Code.ID() + " " + de.Error()}
	}
	return Outcome{Status: "failed", Error: err.Error()}
}

func errorReport(e *diag.Error, hint string) jobspec.ErrorReport {
	return jobspec.ErrorReport{Code: e.Code.ID(), Message: clip(e.Error()), Label: clip(e.Label), Hint: clip(hint)}
}

func firstError(r *build.Result) string {
	if len(r.Report.Errors) > 0 {
		e := r.Report.Errors[0]
		return e.Code.ID() + " " + e.Error()
	}
	return fmt.Sprintf("build exited %d", r.ExitCode)
}

func clip(s string) string {
	if len(s) > 4000 {
		return s[:4000]
	}
	return s
}

func codeOf(s string) string {
	if i := strings.Index(s, "BOB-E-"); i >= 0 && len(s) >= i+10 {
		return s[i : i+10]
	}
	return ""
}

func firstLineOf(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return clip(s)
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// events forwards build events to the job's output: log lines per package and package ends.
type events struct {
	out   Output
	mu    sync.Mutex
	start map[string]time.Time
}

func (e *events) handle(ev build.Event) {
	if e.out == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch ev.Event {
	case "package.start":
		e.start[ev.Label] = time.Now()
		e.out.Line(ev.Label, 3, ev.Action+" "+ev.Label)
	case "log":
		e.out.Line(ev.Label, 1, ev.Line)
	case "error":
		if ev.Error != nil {
			e.out.Line(ev.Label, 3, "✗ "+ev.Error.Code.ID()+" "+ev.Error.Error())
		}
	case "package.finish":
		status := ev.Status
		if status == "built" {
			status = "ok"
		}
		start, ok := e.start[ev.Label]
		if !ok {
			start = time.Now()
		}
		e.out.EndPackage(ev.Label, PackageEnd{Status: status, Start: start, End: time.Now()})
	}
}

// openJobStore opens the engine store inside a job container on the worker's persistent
// store: /nix is bound at <home>/nix (so the engine sees itself inside its namespace) and the
// root-mode nix.conf of the container (/etc/nix/nix.conf, SPEC R-FLEET-008) replaces the
// developer profile the engine writes (decision D-IB11-7).
func openJobStore(ctx context.Context, home string) (*store.Store, error) {
	nixDir := filepath.Join(home, "nix")
	if err := os.MkdirAll(nixDir, 0o755); err != nil {
		return nil, err
	}
	var a, b syscall.Stat_t
	if syscall.Stat("/nix", &a) != nil {
		return nil, errors.New("/nix is not bound into the job container")
	}
	if syscall.Stat(nixDir, &b) != nil || a.Dev != b.Dev || a.Ino != b.Ino {
		if err := syscall.Mount("/nix", nixDir, "", syscall.MS_BIND, ""); err != nil {
			return nil, fmt.Errorf("bind /nix: %w", err)
		}
	}
	st, err := store.Open(ctx, store.Config{Home: home})
	if err != nil {
		return nil, err
	}
	conf, err := os.ReadFile("/etc/nix/nix.conf")
	if err != nil {
		return nil, fmt.Errorf("root-mode nix.conf: %w", err)
	}
	if err := os.WriteFile(filepath.Join(home, "etc", "nix", "nix.conf"), conf, 0o644); err != nil {
		return nil, err
	}
	return st, nil
}

// closure lists the closure of outputs with their NAR hashes and references.
func closure(ctx context.Context, st *store.Store, outputs []string) ([]PathInfo, error) {
	var stdout, stderr bytes.Buffer
	c := st.Command(ctx, "nix", append([]string{"--extra-experimental-features", "nix-command", "path-info", "--json", "--recursive"}, outputs...)...)
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, firstLineOf(stderr.String()))
	}
	return parsePathInfo(stdout.Bytes())
}

// parsePathInfo reads `nix path-info --json` in both of its shapes (an array of objects with
// "path", or an object keyed by path) and normalises NAR hashes to sha256:<nix32>.
func parsePathInfo(b []byte) ([]PathInfo, error) {
	type raw struct {
		Path       string   `json:"path"`
		NarHash    string   `json:"narHash"`
		NarSize    uint64   `json:"narSize"`
		References []string `json:"references"`
	}
	var list []raw
	if err := json.Unmarshal(b, &list); err != nil {
		var m map[string]*raw
		if err2 := json.Unmarshal(b, &m); err2 != nil {
			return nil, fmt.Errorf("nix path-info output: %w", err)
		}
		for p, r := range m {
			if r == nil {
				continue
			}
			r.Path = p
			list = append(list, *r)
		}
	}
	if len(list) > maxStagedPaths {
		return nil, fmt.Errorf("closure of %d paths, the limit is %d", len(list), maxStagedPaths)
	}
	out := make([]PathInfo, 0, len(list))
	for _, r := range list {
		h, err := nix32Hash(r.NarHash)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Path, err)
		}
		refs := make([]string, 0, len(r.References))
		for _, ref := range r.References {
			if !strings.HasPrefix(ref, cache.StoreDir+"/") {
				ref = cache.StoreDir + "/" + ref
			}
			refs = append(refs, ref)
		}
		out = append(out, PathInfo{Path: r.Path, NarHash: h, NarSize: r.NarSize, References: refs})
	}
	slices.SortFunc(out, func(a, b PathInfo) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// nix32Hash turns "sha256-<base64>" (SRI) or "sha256:<nix32>" into "sha256:<nix32>".
func nix32Hash(h string) (string, error) {
	switch {
	case strings.HasPrefix(h, "sha256:") && len(h) == 7+52:
		return h, nil
	case strings.HasPrefix(h, "sha256-"):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "sha256-"))
		if err != nil || len(b) != 32 {
			return "", fmt.Errorf("NAR hash %q", h)
		}
		return "sha256:" + cache.Nix32(b), nil
	}
	return "", fmt.Errorf("NAR hash %q", h)
}
