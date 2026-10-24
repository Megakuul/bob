package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"bobspike/s03/crates"
	"bobspike/s03/drv"
	"bobspike/s03/nixidx"
	"bobspike/s03/run"
	"bobspike/s03/rustgen"
)

const nixpkgsRev = "cd4ea8d57de16f4810bc5f14207bac45fbf19f67"

type timer struct {
	on       bool
	t0, last time.Time
}

func (t *timer) mark(what string) {
	if t.on {
		n := time.Now()
		fmt.Fprintf(os.Stderr, "[time] %-24s %7.3fs (total %.3fs)\n", what, n.Sub(t.last).Seconds(), n.Sub(t.t0).Seconds())
		t.last = n
	}
}

// builderPath is the bob-builder binary next to bob-rs (built by the Makefile/measure.sh).
func builderPath() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "bob-builder")
}

func cmdBuild(sub, wsPath, lockPath, target string, verbose, timing bool) error {
	now := time.Now()
	t := &timer{on: timing, t0: now, last: now}
	w, err := crates.ReadWorkspace(wsPath)
	if err != nil {
		return err
	}
	l, err := crates.ReadLock(lockPath)
	if err != nil {
		return err
	}
	pl := crates.Platforms["aarch64-unknown-linux-gnu"]
	var roots []string // rust crates to plan
	var ccT *crates.WsCC
	var goT *crates.WsGo
	switch {
	case w.Get(target) != nil:
		roots = []string{target}
	default:
		for _, c := range w.CC {
			if c.Name == target {
				ccT, roots = c, c.RustDeps
			}
		}
		for _, g := range w.Go {
			if g.Name == target {
				goT, roots = g, g.RustDeps
			}
		}
		if ccT == nil && goT == nil {
			return fmt.Errorf("✗ unknown target %s", target)
		}
	}
	var plans []*crates.Plan
	for _, r := range roots {
		p, err := crates.MakePlan(w, l, r, pl, pl, "rustc-1.95.0")
		if err != nil {
			return fmt.Errorf("✗ %v", err)
		}
		plans = append(plans, p)
	}
	t.mark("plan")
	ix, err := nixidx.Open(nixpkgsRev, "aarch64-linux")
	if err != nil {
		return fmt.Errorf("✗ nixpkgs index: %v", err)
	}
	need := append(append([]string{}, rustgen.Toolchain...), "go", "cacert")
	for _, as := range w.Native {
		need = append(need, as...)
	}
	if err := ix.Resolve(need); err != nil {
		return fmt.Errorf("✗ nixpkgs resolve: %v", err)
	}
	t.mark("nixpkgs index")
	// local sources + the builder binary, one nix-store --add
	dirs := map[string]string{}
	for _, c := range w.Crates {
		dirs[c.Name] = filepath.Join(w.Root, c.Src)
	}
	for _, c := range w.CC {
		dirs["cc:"+c.Name] = filepath.Join(w.Root, c.Src)
	}
	for _, g := range w.Go {
		dirs["go:"+g.Name] = filepath.Join(w.Root, g.Src)
	}
	var list []string
	for _, d := range dirs {
		list = append(list, d)
	}
	list = append(list, builderPath())
	sort.Strings(list)
	imported, err := run.ImportSources(uniq(list))
	if err != nil {
		return err
	}
	srcs := map[string]string{}
	for k, d := range dirs {
		srcs[k] = imported[d]
	}
	t.mark("import sources")
	g := rustgen.New(ix, "aarch64-linux", imported[builderPath()], srcs, w, "1.95.0")
	var rootDrvs []*drv.Derivation
	for _, p := range plans {
		o, err := g.Unit(p.Root, p)
		if err != nil {
			return fmt.Errorf("✗ %v", err)
		}
		rootDrvs = append(rootDrvs, o.Compile)
	}
	if ccT != nil {
		d, _, err := g.CC(ccT.Name)
		if err != nil {
			return fmt.Errorf("✗ %v", err)
		}
		rootDrvs = []*drv.Derivation{d}
	}
	if goT != nil {
		d, err := g.GoBin(goT)
		if err != nil {
			return fmt.Errorf("✗ %v", err)
		}
		rootDrvs = []*drv.Derivation{d}
	}
	t.mark("compute drvs")
	if sub == "outs" {
		for _, d := range g.Order {
			k := "build"
			if d.IsFixed() {
				k = "fetch"
			}
			fmt.Println(k, d.OutPath)
		}
		return nil
	}
	n, err := run.AddDerivations(g.Order, "import")
	if err != nil {
		return err
	}
	t.mark(fmt.Sprintf("add %d drvs", n))
	var ks []string
	for k, v := range g.Stats {
		ks = append(ks, fmt.Sprintf("%s=%d", k, v))
	}
	sort.Strings(ks)
	units := 0
	for _, p := range plans {
		units += len(p.Units)
	}
	fmt.Fprintf(os.Stderr, "%d units, %d derivations (%s)\n", units, len(g.Order), strings.Join(ks, " "))
	if sub == "drvs" {
		for _, d := range rootDrvs {
			fmt.Println(d.DrvPath)
		}
		return nil
	}
	var paths []string
	for _, d := range rootDrvs {
		paths = append(paths, d.DrvPath)
	}
	res, err := run.Build(paths)
	if err != nil {
		return err
	}
	t.mark("nix build")
	if res.OK {
		for _, p := range res.Built {
			fmt.Println(p)
		}
		return nil
	}
	if verbose {
		fmt.Fprint(os.Stderr, res.Raw)
	}
	return translate(g, res, srcs, w)
}

var rustErr = regexp.MustCompile(`^(error|warning)(\[E\d+\])?:|^\s+--> |bob-builder:`)

func translate(g *rustgen.Gen, res *run.Result, srcs map[string]string, w *crates.Workspace) error {
	var out []string
	for _, f := range res.Failures {
		label := g.Labels[f.Drv]
		if label == "" {
			out = append(out, "✗ a nixpkgs dependency failed to build: "+f.Drv+" (run with -v)")
			continue
		}
		if f.Kind == "hash" {
			out = append(out, fmt.Sprintf("✗ %s: download does not match bob.lock\n  hint: run bob lock if the crate was legitimately re-published; otherwise the source was tampered with", label))
			continue
		}
		var ex []string
		for _, ln := range f.Log {
			ln = run.StripANSI(ln)
			for name, sp := range srcs { // store paths of local sources -> repo paths
				for _, c := range w.Crates {
					if c.Name == name {
						ln = strings.ReplaceAll(ln, sp, c.Src)
					}
				}
			}
			ln = strings.ReplaceAll(ln, "/rust/", "")
			if rustErr.MatchString(ln) || len(ex) > 0 && len(ex) < 30 {
				ex = append(ex, "  │ "+ln)
			}
		}
		if len(ex) == 0 && len(f.Log) > 0 {
			for _, ln := range f.Log[max(0, len(f.Log)-15):] {
				ex = append(ex, "  │ "+run.StripANSI(ln))
			}
		}
		out = append(out, fmt.Sprintf("✗ %s failed\n%s", label, strings.Join(ex, "\n")))
	}
	if len(out) == 0 {
		return fmt.Errorf("✗ build failed (run with -v)")
	}
	return fmt.Errorf("%s", strings.Join(out, "\n"))
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var o []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			o = append(o, x)
		}
	}
	return o
}
