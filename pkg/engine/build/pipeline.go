package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/megakuul/bob/pkg/engine/build/testrun"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/eval"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// run is the pipeline; it returns the partial result with the first failure.
func (r *runner) run(ctx context.Context) (*Result, error) {
	o := r.o
	if o.Replay != "" {
		return r.replay()
	}
	res := &Result{}
	if o.Root == "" {
		return res, errors.New("build: Options.Root is empty")
	}
	st, err := openStore(ctx, o.Store, o.Home, o.Caches)
	if err != nil {
		return res, err
	}

	f := fetch.New(st, fetch.Policy{})
	// scan + eval: eval.Evaluate scans and evaluates in one call and reports the scan's share.
	t := time.Now()
	goRoot, err := r.evalGoRoot(ctx, st, f)
	if err != nil {
		r.emit(Event{Event: StageScan})
		r.emit(Event{Event: StageEval, DurationMs: time.Since(t).Milliseconds()})
		return res, err
	}
	ev, err := eval.Evaluate(ctx, evalRequest(o, goRoot))
	total := time.Since(t)
	scanE, evalE := Event{Event: StageScan}, Event{Event: StageEval, DurationMs: total.Milliseconds()}
	if ev != nil {
		scanE.DurationMs = ev.Scan.Milliseconds()
		evalE.DurationMs = (total - ev.Scan).Milliseconds()
		evalE.Cached = ev.Cached
	}
	r.emit(scanE)
	r.emit(evalE)
	if err != nil {
		return res, err
	}
	var g *ir.Graph
	if err := r.stage(StageIR, func() (err error) { g, err = ir.Parse(ev.IR, eval.BobVersion()); return }); err != nil {
		return res, err
	}
	res.Requested = requestedLabels(g)
	r.emit(Event{Event: "run.start", Requested: res.Requested, Nix: nixVersion})
	if err := crossRefusals(g); err != nil {
		return res, err
	}
	r.noteDropped(g)

	var lf *lock.File
	if err := r.stage(StageLock, func() (err error) { lf, err = r.lockStage(ctx, st, f, g); return }); err != nil {
		return res, err
	}

	var idx *nixidx.Index
	entries := map[string]nixidx.Entry{}
	// R-FW-061 runs in the index stage (it needs the index): generated files edited by hand stop
	// the run before anything is built; missing or outdated ones are rewritten. Only a run of
	// the whole repository sees whole modules (nativeScope).
	if err := r.stage(StageIndex, func() (err error) {
		if idx, entries, err = r.indexStage(ctx, st, f, g, lf); err != nil {
			return err
		}
		if nativeScope(o) {
			res.Generated, err = generate(ctx, o.Root, g, lf, idx, false, time.Now())
		}
		return err
	}); err != nil {
		return res, err
	}

	var srcPaths map[string]string
	if err := r.stage(StageSources, func() (err error) { srcPaths, err = importSources(ctx, st, o.Root, g); return }); err != nil {
		return res, err
	}

	var p *planner
	if err := r.stage(StagePlan, func() error {
		builder, err := st.Builder(ctx)
		if err != nil {
			return err
		}
		p = newPlanner(g, st, builder, srcPaths, entries, lf, idx)
		p.root, p.fetcher = o.Root, f
		p.offline, p.maxJobs = o.Offline, o.MaxJobs
		if o.Command == CmdTest {
			p.testArgs = o.TestArgs
		}
		return p.plan(ctx)
	}); err != nil {
		return res, err
	}

	// Test units: the requested test-run derivations (bob test); bob build leaves them out.
	units, err := r.selectUnits(p, res.Requested)
	if err != nil {
		return res, err
	}
	units = r.skipForeign(g, p, units)

	if err := r.stage(StageFetch, func() error { return ensureAll(ctx, f, p.fetches) }); err != nil {
		return res, err
	}

	var imported int
	if err := r.stage(StageDrv, func() (err error) {
		drvs := p.drvs
		for _, u := range units {
			if p.byPath[u.DrvPath] == nil {
				drvs = append(drvs, u.Drv)
			}
		}
		imported, err = st.ImportDrvs(ctx, drvs)
		recordDrvCache(st, g, p)
		return err
	}); err != nil {
		return res, err
	}
	r.emit(Event{Event: "drv.imported", Count: imported})

	cached := map[string]bool{}
	if o.Command == CmdTest && len(units) > 0 {
		if cached, err = testrun.Cached(ctx, st, units); err != nil {
			return res, err
		}
	}
	if err := r.substitute(ctx, st, p); err != nil {
		return res, err
	}

	targets := r.withoutSkipped(r.targetLabels(g, p, res.Requested))
	rep, err := r.realise(ctx, st, g, p, srcPaths, targets, units, cached)
	if err != nil {
		return res, err
	}
	rep.Counts.Built += p.planBuilt // derivations built while planning (early cutoff)
	res.Report = rep
	r.collect(st, g, p, res, targets)
	if o.Command == CmdTest {
		res.Tests = append(r.testResults(ctx, st, units, cached), r.skippedResults()...)
	}
	if err := r.materialise(ctx, st, p, res); err != nil {
		return res, err
	}
	return res, nil
}

// skipForeign takes the units that cannot run on this machine out of units (R-FW-039); they
// are reported skipped by skippedResults.
func (r *runner) skipForeign(g *ir.Graph, p *planner, units []testrun.Unit) []testrun.Unit {
	var keep []testrun.Unit
	byID := nodesByID(g)
	for _, u := range units {
		if why := skipReason(byID, p, u); why != "" {
			r.skipped = append(r.skipped, TestResult{Label: u.Label, Status: string(testrun.StatusSkipped), Reason: why})
			continue
		}
		keep = append(keep, u)
	}
	return keep
}

// withoutSkipped removes the labels of skipped units from labels: nothing is realised for them.
func (r *runner) withoutSkipped(labels []string) []string {
	if len(r.skipped) == 0 {
		return labels
	}
	skip := map[string]bool{}
	for _, t := range r.skipped {
		skip[t.Label] = true
	}
	var out []string
	for _, l := range labels {
		if !skip[l] {
			out = append(out, l)
		}
	}
	return out
}

// skippedResults emits the test.result events of the skipped units.
func (r *runner) skippedResults() []TestResult {
	for _, t := range r.skipped {
		r.emit(Event{Event: "test.result", Label: t.Label, Status: t.Status, Line: t.Reason})
	}
	return r.skipped
}

// replay translates a recorded Nix run (BOB_TEST_NIX_REPLAY) instead of building.
func (r *runner) replay() (*Result, error) {
	rep, evs, err := translate.Replay(r.o.Replay)
	if err != nil {
		return nil, err
	}
	res := &Result{Report: rep}
	for _, e := range evs {
		switch e.Event {
		case "run.start":
			res.Requested = e.Requested
			r.emit(Event{Event: "run.start", Requested: e.Requested, Nix: nixVersion})
		case "package.finish", "run.finish": // Run emits these from the report
		default:
			r.emit(fromTranslate(e))
		}
	}
	return res, nil
}

// indexStage opens the nixpkgs index when the graph uses nixpkgs and runs the pre-checks of
// R-NIX-011 on every nixpkgs node.
func (r *runner) indexStage(ctx context.Context, st *store.Store, f fetch.Fetcher, g *ir.Graph, lf *lock.File) (*nixidx.Index, map[string]nixidx.Entry, error) {
	// Ecosystems take their toolchains from nixpkgs; a graph of core steps without nixpkgs
	// tools needs no index (and no nixpkgs download).
	if lf.Nixpkgs.Rev == "" || !usesNixpkgs(g) && !hasEcoNodes(g) {
		return nil, map[string]nixidx.Entry{}, nil
	}
	src, err := f.Ensure(ctx, lock.NixpkgsRequest(lf.Nixpkgs))
	if err != nil {
		return nil, nil, err
	}
	idx, err := nixidx.Open(ctx, st, nixidx.Config{Source: src, Rev: lf.Nixpkgs.Rev, System: store.HostSystem()})
	if err != nil {
		return nil, nil, err
	}
	ix := &indexes{st: st, src: src, rev: lf.Nixpkgs.Rev, host: idx}
	entries, err := ix.check(ctx, g)
	if err != nil {
		return idx, entries, err
	}
	if err := ix.precheck(ctx, g); err != nil {
		return idx, entries, err
	}
	r.noteUncached(ctx, ix, g, entries)
	return idx, entries, nil
}

// ensureAll makes every fetched input valid, at most 8 downloads at a time (R-FW-049).
func ensureAll(ctx context.Context, f fetch.Fetcher, reqs []fetch.Request) error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	sem := make(chan struct{}, 8)
	for _, req := range reqs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if v := recover(); v != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("fetching %s: panic: %v", req.Label, v))
					mu.Unlock()
				}
			}()
			if _, err := f.Ensure(ctx, req); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// recordDrvCache records each planned node's drv path under its drv key (R-NIX-007), the
// basis for skipping Plan of unchanged nodes. Failures only cost the cache.
func recordDrvCache(st *store.Store, g *ir.Graph, p *planner) {
	c := st.DrvCache()
	for _, n := range g.Nodes {
		rv := p.nodes[n.ID]
		d := p.byPath[rv.Drv]
		if d == nil || n.Kind == "bob.file" {
			continue
		}
		canon, err := json.Marshal(n)
		if err != nil {
			continue
		}
		var ins []string
		for _, in := range d.InputDrvs {
			ins = append(ins, in.Path)
		}
		_ = c.Put(store.DrvKey(canon, ins, eval.BobVersion(), p.builder), rv.Drv)
	}
}

// selectUnits returns the test units of a bob test run: the planned test-run derivations of
// requested packages (with a fresh nonce under --rerun). bob build selects none.
func (r *runner) selectUnits(p *planner, requested []string) ([]testrun.Unit, error) {
	if r.o.Command != CmdTest {
		return nil, nil
	}
	all, err := testrun.Units(p.drvs)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, l := range requested {
		want[l] = true
	}
	var out []testrun.Unit
	nonce := fmt.Sprint(time.Now().UnixNano())
	for _, u := range all {
		if !want[u.Label] {
			continue
		}
		if r.o.Rerun {
			if u, err = testrun.Rerun(u, nonce, p.res); err != nil {
				return nil, err
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// isTestDrv reports whether d is a test unit.
func isTestDrv(d *drv.Derivation) bool {
	return len(d.Args) == 1 && d.Args[0] == testrun.Kind
}

// collect fills the per-package results and the host paths of their outputs.
func (r *runner) collect(st *store.Store, g *ir.Graph, p *planner, res *Result, targets []string) {
	status := map[string]string{}
	for _, ps := range res.Report.Packages {
		status[ps.Label] = string(ps.Status)
	}
	r.hostPaths = map[string][]string{}
	byLabel := map[string]map[string]string{}
	drvOf := map[string]string{} // label → derivation of its first configuration (bob log)
	for _, n := range g.Nodes {
		rv := p.nodes[n.ID]
		if len(rv.Outputs) == 0 {
			continue
		}
		if drvOf[n.Label] == "" {
			drvOf[n.Label] = rv.Drv
		}
		if byLabel[n.Label] == nil {
			byLabel[n.Label] = map[string]string{}
		}
		for o, sp := range rv.Outputs {
			byLabel[n.Label][o] = sp
		}
	}
	for label, outs := range byLabel {
		names := make([]string, 0, len(outs))
		for o := range outs {
			names = append(names, o)
		}
		sort.Strings(names)
		for _, o := range names {
			r.hostPaths[label] = append(r.hostPaths[label], hostPath(st, outs[o]))
		}
	}
	for _, l := range targets {
		pkg := Package{Label: l, Status: status[l], Outputs: byLabel[l], Drv: drvOf[l]}
		if pkg.Status == "" {
			// Nix had nothing to do for it: valid already (or nothing to build).
			st := translate.StCached
			if res.Report.ExitCode != 0 {
				st = translate.StSkipped
			}
			pkg.Status = string(st)
			res.Report.Packages = append(res.Report.Packages, &translate.PkgState{Label: l, Status: st})
		}
		if r.o.PrintPaths {
			pkg.HostPaths = r.hostPaths[l]
		}
		res.Packages = append(res.Packages, pkg)
	}
}

// targetLabels are the requested labels this command realises: bob build leaves test units
// out (they run with bob test), bob test keeps only them.
func (r *runner) targetLabels(g *ir.Graph, p *planner, requested []string) []string {
	if r.o.Command == CmdGen {
		return nil // bob gen realises only working-tree outputs (installables)
	}
	tests := map[string]bool{}
	for _, n := range g.Nodes {
		if d := p.byPath[p.nodes[n.ID].Drv]; d != nil && isTestDrv(d) && n.Kind != "bob.file" {
			tests[n.Label] = true
		}
	}
	var out []string
	for _, l := range requested {
		if tests[l] == (r.o.Command == CmdTest) {
			out = append(out, l)
		}
	}
	return out
}
