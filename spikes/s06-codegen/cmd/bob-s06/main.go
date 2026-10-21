// Command bob-s06 is spike S6's driver: codegen packages built as derivations (backend B) and
// materialised into the working tree.
//
//	bob-s06 build  [-root DIR] [-no-gen] [LABEL...]   build targets (default: all), materialise their codegen
//	bob-s06 gen    [-root DIR] [-force] [-check]      build every codegen package, materialise, prune orphans
//	bob-s06 status [-root DIR]                        compare tree with the graph without building
//	bob-s06 dev    [-root DIR] [-poll 100ms]          gen, then regenerate on every input change
//	bob-s06 outs   [-root DIR] [-kind codegen|all]    output paths (for measure.sh)
//	bob-s06 e2e    [-root DIR]                        run server, Go client and TS client
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"bobspike/s06/cg"
	"bobspike/s06/mat"
	"bobspike/s06/nixidx"
	"bobspike/s06/run"
)

const rev = "cd4ea8d57de16f4810bc5f14207bac45fbf19f67"

type world struct {
	root  string
	b     *cg.B
	c     *cg.Ctx
	times []string
}

func spikeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: bob-s06 build|gen|status|dev|outs|e2e [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	root := fs.String("root", filepath.Join(spikeDir(), "fixture"), "repo root")
	noGen := fs.Bool("no-gen", false, "build: do not materialise")
	cutoff := fs.Bool("cutoff", false, "build: early cutoff (dependents use content-addressed codegen outputs)")
	force := fs.Bool("force", false, "gen: back up edited files and overwrite")
	check := fs.Bool("check", false, "gen: report only, exit 1 if anything is not fresh")
	verbose := fs.Bool("v", false, "raw Nix output on failure")
	timing := fs.Bool("timing", false, "phase timings on stderr")
	poll := fs.Duration("poll", 100*time.Millisecond, "dev: poll interval")
	kind := fs.String("kind", "all", "outs: codegen|all")
	_ = fs.Parse(os.Args[2:])
	abs, _ := filepath.Abs(*root)
	var err error
	switch os.Args[1] {
	case "build":
		err = cmdBuild(abs, fs.Args(), *noGen, *cutoff, *verbose, *timing)
	case "gen":
		err = cmdGen(abs, *force, *check, *verbose, *timing)
	case "status":
		err = cmdStatus(abs)
	case "dev":
		err = cmdDev(abs, *poll, *verbose)
	case "outs":
		err = cmdOuts(abs, *kind)
	case "e2e":
		err = cmdE2E(abs, *verbose)
	default:
		err = fmt.Errorf("unknown command %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (w *world) mark(t0 *time.Time, what string) {
	n := time.Now()
	w.times = append(w.times, fmt.Sprintf("[time] %-26s %7.3fs", what, n.Sub(*t0).Seconds()))
	*t0 = n
}

// eval runs bob.go, checks it, imports sources and computes every derivation (no build).
func eval(root string, timing bool) (*world, error) {
	t := time.Now()
	w := &world{root: root}
	lockb, err := os.ReadFile(filepath.Join(spikeDir(), "lock.json"))
	if err != nil {
		return nil, err
	}
	var lock map[string]string
	if err := json.Unmarshal(lockb, &lock); err != nil {
		return nil, err
	}
	w.b = cg.New(root, filepath.Join(spikeDir(), "tools"), lock)
	if err := define(w.b, spikeDir()); err != nil {
		return nil, err
	}
	w.mark(&t, "eval bob.go")
	ix, err := nixidx.Open(rev, "aarch64-linux")
	if err != nil {
		return nil, fmt.Errorf("✗ cannot open nixpkgs (internal error in bob's nixpkgs lookup; run with -v): %v", err)
	}
	need := map[string]bool{"bash": true, "coreutils": true, "gnused": true, "gnugrep": true, "findutils": true}
	for _, p := range w.b.Pkgs {
		for _, a := range p.Tools {
			need[a] = true
		}
	}
	var attrs []string
	for a := range need {
		attrs = append(attrs, a)
	}
	sort.Strings(attrs)
	if err := ix.Resolve(attrs); err != nil {
		return nil, fmt.Errorf("✗ cannot resolve nixpkgs packages: %v", err)
	}
	if err := precheck(w.b, ix); err != nil {
		return nil, err
	}
	w.mark(&t, "nixpkgs index + checks")
	seen := map[string]bool{}
	var srcs []run.Src
	for _, p := range w.b.Pkgs {
		for _, s := range p.Srcs {
			if seen[s.Key] {
				continue
			}
			seen[s.Key] = true
			dir := s.Dir
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(root, dir)
			}
			srcs = append(srcs, run.Src{Key: s.Key, Dir: dir, Name: storeNameOf(s.Key), Include: s.Include})
		}
	}
	imported, err := run.ImportSources(srcs)
	if err != nil {
		return nil, err
	}
	w.mark(&t, "import sources")
	w.c = &cg.Ctx{B: w.b, Ix: ix, Srcs: imported, Labels: map[string]string{}}
	for _, p := range w.b.Pkgs {
		if _, err := w.c.Drv(p); err != nil {
			return nil, fmt.Errorf("✗ %v", err)
		}
	}
	if _, err := run.AddDerivations(w.c.Order, "import"); err != nil {
		return nil, err
	}
	_ = ix.Save()
	w.mark(&t, fmt.Sprintf("compute+add %d drvs", len(w.c.Order)))
	return w, nil
}

func storeNameOf(key string) string {
	r := strings.NewReplacer(":", "-", "/", "-", "@", "-")
	return "src-" + r.Replace(key)
}

func versionCmp(a, b string) int {
	as := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bs := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(strings.SplitN(as[i], "-", 2)[0])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(strings.SplitN(bs[i], "-", 2)[0])
		}
		if x != y {
			return x - y
		}
	}
	return 0
}

// precheck: generator and runtime library versions must fit before anything is built.
func precheck(b *cg.B, ix *nixidx.Index) error {
	var errs []string
	for _, p := range b.Pkgs {
		for _, c := range p.Checks {
			tv := ix.Attrs[c.ToolAttr].Version
			rv := c.RuntimeVer
			bad := ""
			switch {
			case rv == "":
				bad = fmt.Sprintf("its runtime library %s is not a dependency in %s", c.Runtime, c.Where)
			case c.Rule == "equal" && versionCmp(tv, rv) != 0:
				bad = fmt.Sprintf("the generated code needs %s %s, but %s has %s", c.Runtime, tv, c.Where, rv)
			case c.Rule == "atleast" && versionCmp(rv, tv) < 0:
				bad = fmt.Sprintf("the generated code needs %s >= %s, but %s has %s", c.Runtime, tv, c.Where, rv)
			case c.Rule == "atleast-minor13" && versionCmp(rv, "1.13.0") < 0:
				bad = fmt.Sprintf("the generated code needs %s >= v1.13.0, but %s has %s", c.Runtime, c.Where, rv)
			}
			if bad != "" {
				errs = append(errs, fmt.Sprintf("✗ %s: generator %s %s (nixpkgs %s) does not match its runtime: %s\n  defined at %s\n  hint: bob pins the runtime to the generator; set %s to %s in bob.go, or pin a nixpkgs with the generator you need",
					p.Label, c.Tool, tv, rev[:12], bad, p.DefinedAt, c.Runtime, tv))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

func closure(ps []*cg.Pkg) []*cg.Pkg {
	seen := map[*cg.Pkg]bool{}
	var out []*cg.Pkg
	var walk func(p *cg.Pkg)
	walk = func(p *cg.Pkg) {
		if seen[p] {
			return
		}
		seen[p] = true
		for _, d := range p.Deps {
			walk(d)
		}
		out = append(out, p)
	}
	for _, p := range ps {
		walk(p)
	}
	return out
}

// cutoff builds every codegen package, imports each output by content and recomputes the
// derivations of their consumers, so an unchanged generated tree does not rebuild them.
func (w *world) cutoff(verbose bool) error {
	gens := w.codegen()
	if err := w.build(gens, verbose); err != nil {
		return err
	}
	var srcs []run.Src
	for _, p := range gens {
		srcs = append(srcs, run.Src{Key: p.Label, Dir: p.Drv.OutPath, Name: "gen-" + storeNameOf(strings.TrimPrefix(p.Label, "//"))})
	}
	ca, err := run.ImportSources(srcs)
	if err != nil {
		return err
	}
	w.c.CA = map[*cg.Pkg]string{}
	for _, p := range gens {
		w.c.CA[p] = ca[p.Label]
	}
	for _, p := range w.b.Pkgs {
		if p.Kind == "go_binaries" || p.Kind == "ts_build" {
			p.Drv = nil
		}
	}
	for _, p := range w.b.Pkgs {
		if _, err := w.c.Drv(p); err != nil {
			return err
		}
	}
	_, err = run.AddDerivations(w.c.Order, "import")
	return err
}

func (w *world) build(targets []*cg.Pkg, verbose bool) error {
	var drvs, outs []string
	for _, p := range targets {
		drvs = append(drvs, p.Drv.DrvPath)
		outs = append(outs, p.Drv.OutPath)
	}
	if run.AllValid(outs) { // no-op fast path: skip starting a nix build
		return nil
	}
	res, err := run.Build(drvs)
	if err != nil {
		return err
	}
	var built []string
	for _, d := range res.Builds {
		if l := w.c.Labels[d]; l != "" {
			built = append(built, l)
		} else {
			built = append(built, filepath.Base(d))
		}
	}
	if len(built) > 0 {
		sort.Strings(built)
		fmt.Fprintf(os.Stderr, "built %d: %s\n", len(built), strings.Join(built, " "))
	}
	if res.OK {
		return nil
	}
	if verbose {
		fmt.Fprint(os.Stderr, res.Raw)
	}
	var out []string
	for _, f := range res.Failures {
		label := w.c.Labels[f.Drv]
		p := w.b.Get(label)
		if p == nil {
			out = append(out, "✗ nixpkgs dependency failed to build: "+f.Drv+"\n  (run with -v for the full log)")
			continue
		}
		if f.Kind == "hash" {
			out = append(out, fmt.Sprintf("✗ %s download does not match bob.lock\n  hint: run bob lock if upstream legitimately changed", label))
			continue
		}
		var lines []string
		for _, l := range f.Log {
			l = run.StripANSI(l)
			if strings.Contains(l, "bob-builder: command failed") || strings.HasPrefix(l, "go: downloading ") {
				continue
			}
			lines = append(lines, "  │ "+l)
		}
		if len(lines) > 15 {
			lines = lines[len(lines)-15:]
		}
		what := "build failed"
		if p.Kind == "codegen" {
			what = "code generation failed"
		}
		out = append(out, fmt.Sprintf("✗ %s %s (defined at %s)\n%s", label, what, p.DefinedAt, strings.Join(lines, "\n")))
	}
	if len(out) == 0 {
		return fmt.Errorf("✗ build failed (run with -v)")
	}
	return fmt.Errorf("%s", strings.Join(out, "\n"))
}

func (w *world) codegen() []*cg.Pkg {
	var out []*cg.Pkg
	for _, p := range w.b.Pkgs {
		if p.Kind == "codegen" {
			out = append(out, p)
		}
	}
	return out
}

func wantsOf(ps []*cg.Pkg) []mat.Want {
	var ws []mat.Want
	for _, p := range ps {
		if p.Kind == "codegen" {
			ws = append(ws, mat.Want{Dir: p.Out, Label: p.Label, Source: p.Drv.OutPath})
		}
	}
	return ws
}

func report(r *mat.Result, t time.Duration) {
	if r == nil {
		return
	}
	var changed []string
	for _, s := range r.Roots {
		if s.State != "fresh" && s.State != "conflict" {
			changed = append(changed, fmt.Sprintf("%s (%s: %d written, %d removed)", s.Dir, s.State, len(s.Writes), len(s.Removes)))
		}
	}
	fmt.Printf("generated: %d dirs, %d files written, %d removed, %d unchanged, %d hashed in %.3fs\n",
		len(r.Roots), r.Written, r.Removed, r.Unchanged, r.Hashed, t.Seconds())
	for _, c := range changed {
		fmt.Println("  " + c)
	}
	for _, b := range r.Backups {
		fmt.Println("  backed up edited file to " + b)
	}
}

func cmdBuild(root string, labels []string, noGen, cutoff, verbose, timing bool) error {
	w, err := eval(root, timing)
	if err != nil {
		return err
	}
	t := time.Now()
	if cutoff {
		if err := w.cutoff(verbose); err != nil {
			return err
		}
		w.mark(&t, "codegen + cutoff import")
	}
	var targets []*cg.Pkg
	if len(labels) == 0 {
		for _, p := range w.b.Pkgs {
			if p.Kind == "go_binaries" || p.Kind == "ts_build" {
				targets = append(targets, p)
			}
		}
	}
	for _, l := range labels {
		p := w.b.Get(l)
		if p == nil {
			return fmt.Errorf("✗ unknown target %s", l)
		}
		targets = append(targets, p)
	}
	if err := w.build(targets, verbose); err != nil {
		return err
	}
	w.mark(&t, "nix build")
	for _, p := range targets {
		fmt.Println(p.Label, p.Drv.OutPath)
	}
	if noGen {
		return w.printTimes(timing)
	}
	cl := closure(targets)
	inClosure := map[*cg.Pkg]bool{}
	for _, p := range cl {
		inClosure[p] = true
	}
	var keep, stale []string
	for _, p := range w.codegen() {
		if !inClosure[p] {
			keep = append(keep, p.Out)
			if mat.SourceOf(root, p.Out) != p.Drv.OutPath {
				stale = append(stale, p.Out+"/ ("+p.Label+")")
			}
		}
	}
	r, err := mat.Apply(root, wantsOf(cl), mat.Options{Prune: true, Keep: keep, Git: true})
	report(r, time.Since(t))
	w.mark(&t, "materialise")
	if len(stale) > 0 {
		fmt.Printf("note: %d generated directories are out of date but not needed by this build:\n  %s\n  run `bob gen` to update them\n", len(stale), strings.Join(stale, "\n  "))
	}
	if err != nil {
		return err
	}
	return w.printTimes(timing)
}

func (w *world) printTimes(on bool) error {
	if on {
		for _, l := range w.times {
			fmt.Fprintln(os.Stderr, l)
		}
	}
	return nil
}

func cmdGen(root string, force, check, verbose, timing bool) error {
	if check {
		return cmdStatusExit(root, true)
	}
	w, err := eval(root, timing)
	if err != nil {
		return err
	}
	t := time.Now()
	gens := w.codegen()
	if err := w.build(gens, verbose); err != nil {
		return err
	}
	w.mark(&t, "nix build")
	t1 := time.Now()
	r, err := mat.Apply(root, wantsOf(gens), mat.Options{Prune: true, Force: force, Git: true})
	report(r, time.Since(t1))
	w.mark(&t, "materialise")
	_ = w.printTimes(timing)
	return err
}

func cmdStatus(root string) error { return cmdStatusExit(root, false) }

func cmdStatusExit(root string, failIfNotFresh bool) error {
	w, err := eval(root, false)
	if err != nil {
		return err
	}
	outs := map[string][2]string{}
	for _, p := range w.codegen() {
		outs[p.Out] = [2]string{p.Label, p.Drv.OutPath}
	}
	st, err := mat.Status(root, outs)
	if err != nil {
		return err
	}
	bad := 0
	for _, s := range st {
		fmt.Printf("%-8s %-28s %s\n", s.State, s.Dir+"/", s.Label)
		for _, d := range s.Detail {
			fmt.Println("         • " + d)
		}
		if s.State != "fresh" {
			bad++
		}
	}
	if failIfNotFresh && bad > 0 {
		return fmt.Errorf("✗ %d generated directories are not up to date; run `bob gen`", bad)
	}
	return nil
}

// inputsStamp summarises the inputs of every codegen package (and bob.go) by path+size+mtime.
func inputsStamp(root string, w *world) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, p := range w.codegen() {
		for _, s := range p.Srcs {
			dir := s.Dir
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(root, dir)
			}
			if seen[s.Key] {
				continue
			}
			seen[s.Key] = true
			_ = filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				rel, _ := filepath.Rel(dir, path)
				if rel == "." {
					return nil
				}
				if s.Include != nil && !s.Include(filepath.ToSlash(rel), fi.IsDir()) {
					if fi.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if !fi.IsDir() {
					fmt.Fprintf(&b, "%s %d %d\n", path, fi.Size(), fi.ModTime().UnixNano())
				}
				return nil
			})
		}
	}
	return b.String()
}

func cmdDev(root string, poll time.Duration, verbose bool) error {
	gen := func() (*world, error) {
		t := time.Now()
		w, err := eval(root, false)
		if err != nil {
			return nil, err
		}
		gens := w.codegen()
		if err := w.build(gens, verbose); err != nil {
			return w, err
		}
		r, err := mat.Apply(root, wantsOf(gens), mat.Options{Prune: true, Git: true})
		fmt.Printf("[dev] %s regenerated in %.3fs: ", time.Now().Format("15:04:05.000"), time.Since(t).Seconds())
		report(r, time.Since(t))
		return w, err
	}
	w, err := gen()
	if err != nil && w == nil {
		return err
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	stamp := inputsStamp(root, w)
	fmt.Println("[dev] watching codegen inputs")
	for {
		time.Sleep(poll)
		if s := inputsStamp(root, w); s != stamp {
			stamp = s
			nw, err := gen()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
			if nw != nil {
				w = nw
			}
		}
	}
}

func cmdOuts(root, kind string) error {
	w, err := eval(root, false)
	if err != nil {
		return err
	}
	for _, p := range w.b.Pkgs {
		if kind == "codegen" && p.Kind != "codegen" {
			continue
		}
		if kind == "all" && p.Kind == "fetch" {
			continue
		}
		fmt.Println(p.Kind, p.Label, p.Drv.OutPath)
	}
	return nil
}

func cmdE2E(root string, verbose bool) error {
	w, err := eval(root, false)
	if err != nil {
		return err
	}
	bin, web := w.b.Get("//svc:bin"), w.b.Get("//web:client")
	if err := w.build([]*cg.Pkg{bin, web}, verbose); err != nil {
		return err
	}
	addr := "127.0.0.1:20600"
	srv := exec.Command(bin.Drv.OutPath+"/bin/server", "-addr", addr)
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		return err
	}
	defer func() { _ = srv.Process.Kill(); _, _ = srv.Process.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		if ctx.Err() != nil {
			return fmt.Errorf("server did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	steps := [][]string{
		{bin.Drv.OutPath + "/bin/client", "-url", "http://" + addr},
		{w.c.Ix.Attrs["nodejs"].Outputs["out"] + "/bin/node", web.Drv.OutPath + "/dist/src/client.js", "http://" + addr},
	}
	for _, s := range steps {
		cmd := exec.Command(s[0], s[1:]...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %v", s[0], err)
		}
	}
	resp, err := http.Get("http://" + addr + "/rest/items/42")
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("rest (oapi-codegen server): %s", body)
	return nil
}
