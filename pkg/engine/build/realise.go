package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/bob/progress"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/build/testrun"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// substitute is bob's batch check of its own caches (A71): one narinfo HEAD per missing bob
// output and cache (in parallel), then one `nix-store --realise` of the hits. bob derivations
// carry allowSubstitutes = false, so Nix itself never asks a cache about them.
func (r *runner) substitute(ctx context.Context, st *store.Store, p *planner, extra []string) error {
	if len(r.o.Caches) == 0 || r.o.Offline {
		return nil
	}
	defer progress.Phase(ctx, "checking binary caches")()
	outs := append([]string(nil), extra...) // test results of the selected units
	for _, d := range p.drvs {
		for _, o := range d.Outputs {
			outs = append(outs, o.Path)
		}
	}
	missing, err := st.Invalid(ctx, outs)
	if err != nil || len(missing) == 0 {
		return err
	}
	hits := narinfoHits(ctx, r.o.Caches, missing)
	if len(hits) == 0 {
		return nil
	}
	// A failed substitution is not an error: Nix builds what is still missing.
	c := st.Command(ctx, "nix-store", append([]string{"--realise"}, hits...)...)
	_ = c.Run()
	return nil
}

// narinfoHits returns the paths of which some cache has a narinfo.
func narinfoHits(ctx context.Context, caches []store.Substituter, paths []string) []string {
	client := &http.Client{Timeout: 10 * time.Second}
	var (
		mu   sync.Mutex
		hits []string
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, 16)
	for _, sp := range paths {
		hash := strings.TrimPrefix(sp, "/nix/store/")
		if i := strings.IndexByte(hash, '-'); i > 0 {
			hash = hash[:i]
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() { _ = recover() }() // a broken cache answer only costs the hit
			for _, c := range caches {
				req, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimSuffix(c.URL, "/")+"/"+hash+".narinfo", nil)
				if err != nil {
					continue
				}
				if c.NetrcPassword != "" {
					req.SetBasicAuth("bob", c.NetrcPassword)
				}
				resp, err := client.Do(req)
				if err != nil {
					continue
				}
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					mu.Lock()
					hits = append(hits, sp)
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	sort.Strings(hits)
	return hits
}

// translatorContext is everything the translator knows about this run (ADR 0017 Decision 2).
func (r *runner) translatorContext(g *ir.Graph, p *planner, srcPaths map[string]string, requested []string) *translate.Context {
	byID := map[string]*ir.Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	c := &translate.Context{Packages: map[string]translate.Pkg{}, Outputs: map[string]string{},
		Sources: srcDirs(srcPaths), Nixpkgs: map[string]string{}, Users: map[string][]string{},
		Requested: requested, Limits: r.o.Limits, Root: r.o.Root}
	c.Limits.LocalBuilds = true
	pkgOf := func(n *ir.Node, role string) translate.Pkg {
		pk := translate.Pkg{Label: n.Label, DefinedAt: definedAt(n.DefinedAt), Kind: n.Kind, Role: role,
			Codegen: engine.IsCodegenKind(n.Kind)}
		if len(n.Srcs) > 0 {
			pk.SrcDir = n.Srcs[0].Dir
		}
		return pk
	}
	for _, n := range g.Nodes {
		if inc := engine.IncludeDirOf(n); inc != "" && strings.HasPrefix(n.Label, "//") {
			dir, _, _ := strings.Cut(strings.TrimPrefix(n.Label, "//"), ":")
			if c.Exports == nil {
				c.Exports = map[string]translate.Export{}
			}
			c.Exports[n.Label] = translate.Export{Dir: dir, Include: path.Join(dir, inc)}
		}
	}
	if len(c.Exports) > 0 {
		c.Deps = map[string][]string{}
		for _, n := range g.Nodes {
			for _, d := range n.Deps {
				if dn := byID[d]; dn != nil {
					c.Deps[n.Label] = append(c.Deps[n.Label], dn.Label)
				}
			}
		}
	}
	for dp, id := range p.drvNode {
		if p.byPath[dp] != nil {
			c.Packages[dp] = pkgOf(byID[id], "")
		}
	}
	// Helper derivations an ecosystem added belong to a package that uses them: first to the
	// packages this run realises (so a helper's build never touches a package the run leaves
	// alone, e.g. a test unit in bob build), then to any other; deterministic by drv path.
	realised := map[string]bool{}
	for _, l := range requested {
		realised[l] = true
	}
	paths := make([]string, 0, len(p.byPath))
	for dp := range p.byPath {
		paths = append(paths, dp)
	}
	sort.Strings(paths)
	for _, pass := range []bool{true, false} {
		for changed := true; changed; {
			changed = false
			for _, dp := range paths {
				pk, ok := c.Packages[dp]
				if !ok || pass && !realised[pk.Label] {
					continue
				}
				for _, in := range p.byPath[dp].InputDrvs {
					if _, known := c.Packages[in.Path]; !known && p.byPath[in.Path] != nil {
						pk.Role = helperRole(p.byPath[in.Path])
						c.Packages[in.Path] = pk
						changed = true
					}
				}
			}
		}
	}
	for dp, d := range p.byPath {
		for _, o := range d.Outputs {
			c.Outputs[o.Path] = dp
		}
	}
	for _, n := range g.Nodes {
		if n.Nix == nil {
			continue
		}
		rv := p.nodes[n.ID]
		if rv.Drv == "" {
			continue
		}
		c.Nixpkgs[rv.Drv] = n.Nix.Attr
	}
	// Users: the bob packages that use a nixpkgs drv directly.
	for dp, d := range p.byPath {
		pk, ok := c.Packages[dp]
		if !ok {
			continue
		}
		for _, in := range d.InputDrvs {
			if _, nix := c.Nixpkgs[in.Path]; nix && !contains(c.Users[in.Path], pk.Label) {
				c.Users[in.Path] = append(c.Users[in.Path], pk.Label)
			}
		}
	}
	for k := range c.Users {
		sort.Strings(c.Users[k])
	}
	direct := make([]string, 0, len(c.Nixpkgs))
	for d := range c.Nixpkgs {
		direct = append(direct, d)
	}
	sort.Strings(direct)
	for dp := range p.builtWhilePlanning {
		c.PreBuilt = append(c.PreBuilt, dp)
	}
	sort.Strings(c.PreBuilt)
	home := p.st.Home()
	c.ChainOf = func(target string) []string { return chainOf(home, direct, target) }
	return c
}

// helperRole is the translator role of a helper derivation: "unpack" for bob-builder unpack
// jobs (a failure is a broken download), "helper" for every other build step of the package.
func helperRole(d *drv.Derivation) string {
	if spec, ok := d.Structured["bobSpec"].(map[string]any); ok {
		if k, _ := spec["kind"].(string); k == "unpack" || strings.HasSuffix(k, "-unpack") {
			return "unpack"
		}
	}
	return "helper"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// realise runs `nix build` on the requested derivations (ADR 0017 invocation), translates its
// internal-json log live, checks the outputs' validity (R-NIX-018) and returns the report.
func (r *runner) realise(ctx context.Context, st *store.Store, g *ir.Graph, p *planner, srcPaths map[string]string,
	requested []string, units []testrun.Unit, cached map[string]bool) (*translate.Report, error) {
	tctx := r.translatorContext(g, p, srcPaths, requested)
	if r.o.Command == CmdGen {
		r.genContext(tctx, g, p)
	}
	for _, u := range units { // a --rerun unit is a new drv of the same package, replacing it
		if _, ok := tctx.Packages[u.DrvPath]; !ok {
			orig := testDrvOf(p, u.Label)
			if pk, ok := tctx.Packages[orig]; ok {
				tctx.Packages[u.DrvPath] = pk
				delete(tctx.Packages, orig)
			}
			tctx.Outputs[u.Out] = u.DrvPath
		}
	}
	tr := translate.New(tctx)
	tr.Sink = func(e translate.Event) {
		if e.Event == "log" && !r.o.JSONLog {
			return
		}
		r.emit(fromTranslate(e))
	}

	installables := r.installables(g, p, units, cached)
	var outs []string // every planned output: the validity stage checks them all
	for _, d := range p.byPath {
		for _, o := range d.Outputs {
			outs = append(outs, o.Path)
		}
	}
	for _, u := range units {
		outs = append(outs, u.Out)
	}
	var outcome translate.Outcome
	outcome.NixVersion = nixVersion
	err := r.stage(StageNix, func() error {
		if len(installables) == 0 {
			return nil
		}
		if done, err := allValid(ctx, st, p, units, installables, outs); err != nil {
			return err
		} else if done {
			// Nix would have nothing to do (A311): skip its start; the validity stage below
			// still decides success (R-NIX-018).
			return nil
		}
		args := []string{"build", "--no-link", "--print-out-paths", "--log-format", "internal-json", "-L"}
		if r.o.KeepGoing == nil || *r.o.KeepGoing {
			args = append(args, "--keep-going")
		}
		if r.o.MaxJobs > 0 {
			args = append(args, "--max-jobs", strconv.Itoa(r.o.MaxJobs))
		}
		if r.o.Limits.TimeoutS > 0 {
			args = append(args, "--timeout", strconv.Itoa(r.o.Limits.TimeoutS))
		}
		if r.o.Limits.MaxSilentS > 0 {
			args = append(args, "--max-silent-time", strconv.Itoa(r.o.Limits.MaxSilentS))
		}
		if r.o.Offline {
			args = append(args, "--option", "substituters", "")
		}
		args = append(args, installables...)
		start := time.Now()
		code, sig, err := runTranslated(ctx, st, args, tr, r.o.Debug)
		outcome.ExitCode, outcome.Signal = code, sig
		outcome.DurationMs = time.Since(start).Milliseconds()
		outcome.Interrupt = ctx.Err() != nil
		return err
	})
	if err != nil {
		return nil, err
	}
	err = r.stage(StageValidity, func() error {
		invalid, err := st.Invalid(ctx, outs)
		if err != nil {
			return err
		}
		bad := map[string]bool{}
		for _, o := range invalid {
			bad[tctx.Outputs[o]] = true
		}
		for dp := range tctx.Packages {
			if !bad[dp] {
				outcome.Valid = append(outcome.Valid, dp)
			}
		}
		sort.Strings(outcome.Valid)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var rep *translate.Report
	_ = r.stage(StageTranslate, func() error { rep = tr.Finish(outcome); return nil })
	return rep, nil
}

// allValid reports whether every output the installables name is valid already, so that
// `nix build` would build and substitute nothing. It asks about all of outs in the same
// batch (the validity stage then needs no new Nix call for the valid ones). An installable
// whose outputs are not known here counts as not valid.
func allValid(ctx context.Context, st *store.Store, p *planner, units []testrun.Unit, installables, outs []string) (bool, error) {
	unitOut := map[string]string{}
	for _, u := range units {
		unitOut[u.DrvPath] = u.Out
	}
	var want []string
	for _, in := range installables {
		dp, names, ok := strings.Cut(in, "^")
		if !ok {
			return false, nil
		}
		for _, name := range strings.Split(names, ",") {
			sp := ""
			if d := p.byPath[dp]; d != nil {
				for _, o := range d.Outputs {
					if o.Name == name {
						sp = o.Path
					}
				}
			} else if name == "out" {
				sp = unitOut[dp]
			}
			if sp == "" {
				return false, nil
			}
			want = append(want, sp)
		}
	}
	invalid, err := st.Invalid(ctx, append(want, outs...))
	if err != nil {
		return false, err
	}
	bad := map[string]bool{}
	for _, o := range invalid {
		bad[o] = true
	}
	for _, sp := range want {
		if bad[sp] {
			return false, nil
		}
	}
	return true, nil
}

// testDrvOf is the planned test-run derivation of label (for a --rerun unit).
func testDrvOf(p *planner, label string) string {
	for dp, d := range p.byPath {
		if isTestDrv(d) && p.drvNode[dp] != "" {
			if spec, ok := d.Structured["bobSpec"].(map[string]any); ok && spec["label"] == label {
				return dp
			}
		}
	}
	return ""
}

// installables are the `<drv>^<outputs>` arguments: the requested packages (bob build) or
// the test units that are not cached yet (bob test).
func (r *runner) installables(g *ir.Graph, p *planner, units []testrun.Unit, cached map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(dp string, outs []string) {
		sort.Strings(outs)
		s := dp + "^" + strings.Join(outs, ",")
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if r.o.Command == CmdTest {
		for _, u := range units {
			if !cached[u.Label] {
				add(u.DrvPath, []string{"out"})
			}
		}
		return out
	}
	if r.o.Command == CmdGen {
		for _, m := range r.genSet(g, p) {
			add(m.Drv, []string{outputName(p, m)})
		}
		return out
	}
	roots := map[string]bool{}
	for _, id := range g.Roots {
		roots[id] = true
	}
	for _, n := range g.Nodes {
		rv := p.nodes[n.ID]
		if !roots[n.ID] || rv.Drv == "" {
			continue
		}
		if d := p.byPath[rv.Drv]; d != nil && isTestDrv(d) {
			continue // tests run with bob test
		}
		var outs []string
		if n.Kind == "bob.file" { // its output is a path inside the step's "out"
			outs = []string{"out"}
		} else {
			for o := range rv.Outputs {
				outs = append(outs, o)
			}
		}
		if len(outs) > 0 {
			add(rv.Drv, outs)
		}
	}
	return out
}

// runTranslated runs nix with args inside bob's namespace and feeds its stderr to tr line by
// line as it arrives. It returns Nix's exit code and the signal that killed it, if any.
func runTranslated(ctx context.Context, st *store.Store, args []string, tr *translate.Translator, debug io.Writer) (int, string, error) {
	pr, pw := io.Pipe()
	c := st.Command(ctx, "nix", args...)
	var stdout bytes.Buffer
	c.Stdout, c.Stderr = &stdout, pw
	if debug != nil { // -vv: the raw Nix log as it arrives (SPEC §02.1 "bob's debug log")
		c.Stderr = io.MultiWriter(pw, debug)
	}
	fed := make(chan error, 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				fed <- fmt.Errorf("translating the Nix log: panic: %v", v)
				_, _ = io.Copy(io.Discard, pr) // keep Nix from blocking on a full pipe
			}
		}()
		fed <- translate.FeedAll(tr, pr)
	}()
	runErr := c.Run()
	_ = pw.Close()
	feedErr := <-fed
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return 128 + int(ws.Signal()), signalName(ws.Signal()), feedErr
			}
			return ee.ExitCode(), "", feedErr
		}
		if ctx.Err() != nil {
			return 130, "interrupt", feedErr
		}
		return 0, "", runErr
	}
	return 0, "", feedErr
}

func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGKILL:
		return "killed"
	case syscall.SIGTERM:
		return "terminated"
	case syscall.SIGINT:
		return "interrupt"
	}
	return s.String()
}

// testResults reads the outcome of every unit: the cached or new test-result.json, or the
// result the builder logged for a failed test (test.result events).
func (r *runner) testResults(ctx context.Context, st *store.Store, units []testrun.Unit, cached map[string]bool) []TestResult {
	var out []TestResult
	for _, u := range units {
		tr := TestResult{Label: u.Label, Status: string(testrun.StatusFail), Runner: unitRunner(u)}
		if res, err := testrun.ReadResult(hostPath(st, u.Out)); err == nil {
			tr.DurationMs, tr.Failed = res.DurationMs(), res.Failed()
			tr.Status = string(testrun.StatusPass)
			if cached[u.Label] {
				tr.Status = string(testrun.StatusCached)
			}
		} else if res, ok := failedResult(ctx, st, u.DrvPath); ok {
			tr.DurationMs, tr.Failed = res.DurationMs(), res.Failed()
		}
		r.emit(Event{Event: "test.result", Label: tr.Label, Status: tr.Status, DurationMs: tr.DurationMs, Failed: tr.Failed, Runner: tr.Runner})
		out = append(out, tr)
	}
	return out
}

// failedResult reads the builder's test-result line from the build log of a failed unit.
func failedResult(ctx context.Context, st *store.Store, drvPath string) (testrun.Result, bool) {
	var log bytes.Buffer
	c := st.Command(ctx, "nix-store", "--read-log", drvPath)
	c.Stdout = &log
	if err := c.Run(); err != nil {
		return testrun.Result{}, false
	}
	return testrun.FailedResult(&log)
}

// fromTranslate converts a translator event.
func fromTranslate(e translate.Event) Event {
	return Event{Event: e.Event, Label: e.Label, Action: e.Action, Cache: e.Cache, Name: e.Name,
		Reason: e.Reason, Line: e.Line, Status: string(e.Status), Error: e.Error, Hint: e.Hint,
		Requested: e.Requested, ExitCode: e.ExitCode, Counts: e.Counts, Drift: e.Drift}
}

// maxChainDrvs bounds the walk of chainOf through nixpkgs' input derivations.
const maxChainDrvs = 20000

// chainOf returns the shortest path from a direct nixpkgs input of a bob package down to the
// failing derivation target (S15 §5.7), walking the .drv files of the store breadth first.
func chainOf(home string, direct []string, target string) []string {
	parent := map[string]string{}
	queue := []string{}
	for _, d := range direct {
		if _, seen := parent[d]; !seen {
			parent[d] = ""
			queue = append(queue, d)
		}
	}
	for len(queue) > 0 && len(parent) <= maxChainDrvs {
		cur := queue[0]
		queue = queue[1:]
		if cur == target {
			var chain []string
			for p := cur; p != ""; p = parent[p] {
				chain = append([]string{p}, chain...)
			}
			return chain
		}
		b, err := os.ReadFile(filepath.Join(home, cur))
		if err != nil {
			continue
		}
		d, err := drv.Parse(b)
		if err != nil {
			continue
		}
		for _, in := range d.InputDrvs {
			if _, seen := parent[in.Path]; !seen {
				parent[in.Path] = cur
				queue = append(queue, in.Path)
			}
		}
	}
	return nil
}

// definedAt is the "package defined at" position of a node for the translator: <file>:<line>:<col>
// (SPEC §02.5, R-NIX-012), <file>:<line> when the evaluator recorded no column.
func definedAt(p ir.Pos) string {
	if p.Col > 0 {
		return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
	}
	return p.String()
}
