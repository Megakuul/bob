package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/megakuul/bob/pkg/engine/build/testrun"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/store"
	"github.com/megakuul/bob/pkg/engine/translate"
)

// substitute is bob's batch check of its own caches (A71): one narinfo HEAD per missing bob
// output and cache (in parallel), then one `nix-store --realise` of the hits. bob derivations
// carry allowSubstitutes = false, so Nix itself never asks a cache about them.
func (r *runner) substitute(ctx context.Context, st *store.Store, p *planner) error {
	if len(r.o.Caches) == 0 || r.o.Offline {
		return nil
	}
	var outs []string
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
		Requested: requested, Limits: r.o.Limits}
	c.Limits.LocalBuilds = true
	pkgOf := func(n *ir.Node, role string) translate.Pkg {
		pk := translate.Pkg{Label: n.Label, DefinedAt: n.DefinedAt.String(), Kind: n.Kind, Role: role}
		if len(n.Srcs) > 0 {
			pk.SrcDir = n.Srcs[0].Dir
		}
		return pk
	}
	for dp, id := range p.drvNode {
		if p.byPath[dp] != nil {
			c.Packages[dp] = pkgOf(byID[id], "")
		}
	}
	// Helper derivations an ecosystem added belong to the package that uses them.
	for changed := true; changed; {
		changed = false
		for dp, d := range p.byPath {
			pk, ok := c.Packages[dp]
			if !ok {
				continue
			}
			for _, in := range d.InputDrvs {
				if _, known := c.Packages[in.Path]; !known && p.byPath[in.Path] != nil {
					pk.Role = "unpack"
					c.Packages[in.Path] = pk
					changed = true
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
	return c
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
	for _, u := range units { // a --rerun unit is a new drv of the same package
		if _, ok := tctx.Packages[u.DrvPath]; !ok {
			if pk, ok := tctx.Packages[testDrvOf(p, u.Label)]; ok {
				tctx.Packages[u.DrvPath] = pk
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
	var outcome translate.Outcome
	outcome.NixVersion = nixVersion
	err := r.stage(StageNix, func() error {
		if len(installables) == 0 {
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
		code, sig, err := runTranslated(ctx, st, args, tr)
		outcome.ExitCode, outcome.Signal = code, sig
		outcome.DurationMs = time.Since(start).Milliseconds()
		outcome.Interrupt = ctx.Err() != nil
		return err
	})
	if err != nil {
		return nil, err
	}
	err = r.stage(StageValidity, func() error {
		var outs []string
		for _, d := range p.byPath {
			for _, o := range d.Outputs {
				outs = append(outs, o.Path)
			}
		}
		for _, u := range units {
			outs = append(outs, u.Out)
		}
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
func runTranslated(ctx context.Context, st *store.Store, args []string, tr *translate.Translator) (int, string, error) {
	pr, pw := io.Pipe()
	c := st.Command(ctx, "nix", args...)
	var stdout bytes.Buffer
	c.Stdout, c.Stderr = &stdout, pw
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
		tr := TestResult{Label: u.Label, Status: string(testrun.StatusFail)}
		if res, err := testrun.ReadResult(hostPath(st, u.Out)); err == nil {
			tr.DurationMs, tr.Failed = res.DurationMs(), res.Failed()
			tr.Status = string(testrun.StatusPass)
			if cached[u.Label] {
				tr.Status = string(testrun.StatusCached)
			}
		} else if res, ok := failedResult(ctx, st, u.DrvPath); ok {
			tr.DurationMs, tr.Failed = res.DurationMs(), res.Failed()
		}
		r.emit(Event{Event: "test.result", Label: tr.Label, Status: tr.Status, DurationMs: tr.DurationMs, Failed: tr.Failed})
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
