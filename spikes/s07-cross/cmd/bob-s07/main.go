// Command bob-s07 is the spike driver for backend B (direct derivations).
//
//	bob-s07 index [-rev REV]
//	bob-s07 drvs  -graph FILE [-target LABEL] [-add import|add]   graph -> derivations in store, no build
//	bob-s07 build -graph FILE [-target LABEL] [-add import|add] [-v]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"bobspike/s07/drv"
	"bobspike/s07/gen"
	"bobspike/s07/gopkg"
	"bobspike/s07/ir"
	"bobspike/s07/nixidx"
	"bobspike/s07/run"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	graph := fs.String("graph", "", "graph JSON")
	target := fs.String("target", "", "label (default: whole graph)")
	add := fs.String("add", "import", "how derivations enter the store: import | add")
	verbose := fs.Bool("v", false, "show raw Nix output")
	rev := fs.String("rev", "cd4ea8d57de16f4810bc5f14207bac45fbf19f67", "nixpkgs rev (index)")
	timing := fs.Bool("timing", false, "print phase timings to stderr")
	gomode := fs.String("go", "hybrid", "Go granularity: module | package | hybrid")
	plat := fs.String("platform", "linux-arm64", "target platform: linux-arm64 | linux-amd64")
	profile := fs.String("profile", "release", "release | debug")
	san := fs.String("sanitize", "", "comma list: address,undefined")
	race := fs.Bool("race", false, "Go -race")
	feats := fs.String("features", "", "label=f1+f2;label2=f3 (Rust features)")
	libs := fs.String("libs", "native", "cross target nixpkgs libraries: native | cross")
	_ = fs.Parse(os.Args[2:])
	tp, ok := gen.Platforms[*plat]
	if !ok {
		fmt.Fprintf(os.Stderr, "✗ unknown platform %q (known: linux-amd64, linux-arm64)\n", *plat)
		os.Exit(2)
	}
	cfg = buildCfg{Tgt: tp, V: gen.Variant{Profile: *profile, GoRace: *race, TargetLibs: *libs, Features: map[string][]string{}}}
	if *san != "" {
		cfg.V.Sanitize = strings.Split(*san, ",")
	}
	if *feats != "" {
		for _, kv := range strings.Split(*feats, ";") {
			k, v, _ := strings.Cut(kv, "=")
			cfg.V.Features[k] = strings.Split(v, "+")
		}
	}

	var err error
	switch os.Args[1] {
	case "index":
		err = cmdIndex(*rev)
	case "drvs", "build", "outs", "list":
		err = cmdBuild(os.Args[1], *graph, *target, *add, *verbose, *timing, *gomode)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type buildCfg struct {
	Tgt gen.Platform
	V   gen.Variant
}

var cfg buildCfg

func usage() {
	fmt.Fprintln(os.Stderr, "usage: bob-s07 index|drvs|build [flags]")
	os.Exit(2)
}

var toolAttrs = []string{"bash", "coreutils", "gnused", "gnugrep", "findutils", "gnutar", "gzip", "unzip",
	"stdenv.cc", "pkg-config", "rustc", "go", "protobuf", "protoc-gen-go"}

func cmdIndex(rev string) error {
	t0 := time.Now()
	ix, err := nixidx.Open(rev, "aarch64-linux")
	if err != nil {
		return bobNixErr("open nixpkgs index", err, os.Getenv("BOB_DEBUG") != "")
	}
	t1 := time.Now()
	if err := ix.Resolve(toolAttrs); err != nil {
		return bobNixErr("resolve toolchain", err, false)
	}
	t2 := time.Now()
	for _, a := range toolAttrs {
		if at := ix.Attrs[a]; at != nil && at.DrvPath != "" {
			ix.Cache.Modulo(at.DrvPath)
		}
	}
	t3 := time.Now()
	if err := ix.Save(); err != nil {
		return err
	}
	fmt.Printf("index %s: names=%d open=%v resolve=%v modulo=%v (parsed %d drvs) total=%v\n", rev[:12], len(ix.Names),
		t1.Sub(t0).Round(time.Millisecond), t2.Sub(t1).Round(time.Millisecond), t3.Sub(t2).Round(time.Millisecond),
		ix.Cache.Parsed, time.Since(t0).Round(time.Millisecond))
	return nil
}

// loadRaw reads a graph WITHOUT ir.Validate so bob's own checks (cycles etc.) run and report.
func loadRaw(path string) (*ir.Graph, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g ir.Graph
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, fmt.Errorf("✗ %s is not a valid graph: %v", path, err)
	}
	return &g, nil
}

// order topologically sorts the closure of target, reporting cycles with their full path.
func order(g *ir.Graph, target string) ([]*ir.Package, error) {
	byLabel := map[string]*ir.Package{}
	for _, p := range g.Packages {
		byLabel[p.Label] = p
	}
	state := map[string]int{}
	var out []*ir.Package
	var stack []string
	var visit func(l string, from *ir.Package) error
	visit = func(l string, from *ir.Package) error {
		p := byLabel[l]
		if p == nil {
			return fmt.Errorf("✗ %s depends on unknown package %s\n  defined at %s", from.Label, l, from.DefinedAt)
		}
		switch state[l] {
		case 1:
			i := 0
			for i < len(stack) && stack[i] != l {
				i++
			}
			cyc := append(append([]string{}, stack[i:]...), l)
			var lines []string
			for _, c := range cyc[:len(cyc)-1] {
				lines = append(lines, fmt.Sprintf("    %s  (defined at %s)", c, byLabel[c].DefinedAt))
			}
			return fmt.Errorf("✗ dependency cycle: %s\n%s\n  hint: a package cannot depend on itself; move the shared code into its own package",
				strings.Join(cyc, " → "), strings.Join(lines, "\n"))
		case 2:
			return nil
		}
		state[l] = 1
		stack = append(stack, l)
		deps := append([]string{}, p.Deps...)
		if p.Src != nil && p.Src.FromLabel != "" {
			deps = append(deps, p.Src.FromLabel)
		}
		for _, d := range deps {
			if err := visit(d, p); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[l] = 2
		out = append(out, p)
		return nil
	}
	roots := []string{target}
	if target == "" {
		roots = nil
		for _, p := range g.Packages {
			roots = append(roots, p.Label)
		}
	}
	for _, r := range roots {
		if byLabel[r] == nil {
			return nil, fmt.Errorf("✗ unknown target %s", r)
		}
		if err := visit(r, byLabel[r]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// precheck validates nixpkgs attributes and toolchain versions before any derivation exists.
func precheck(g *ir.Graph, ix *nixidx.Index, pkgs []*ir.Package, gn *gen.Gen) error {
	var errs []string
	for _, p := range pkgs {
		for _, a := range p.Nixpkgs {
			la := a
			if p.Kind == ir.KindCCLibrary {
				la = gn.LibAttr(a)
			}
			at := ix.Attrs[la]
			where := fmt.Sprintf("  in %s (defined at %s)", p.Label, p.DefinedAt)
			switch {
			case at == nil || at.Missing:
				msg := fmt.Sprintf("✗ nixpkgs has no package %q (nixpkgs %s)\n%s", a, g.Nixpkgs.Rev[:12], where)
				if s := ix.Suggest(a); len(s) > 0 {
					msg += "\n  hint: did you mean " + strings.Join(s, ", ") + "?"
				}
				errs = append(errs, msg)
			case at.Broken:
				errs = append(errs, fmt.Sprintf("✗ nixpkgs package %q (%s) is marked broken in nixpkgs %s\n%s\n  hint: pin a nixpkgs revision where it works, or use another package", a, at.Version, g.Nixpkgs.Rev[:12], where))
			case at.Insecure:
				errs = append(errs, fmt.Sprintf("✗ nixpkgs package %q (%s) is marked insecure: %s\n%s\n  hint: upgrade to a fixed version; bob refuses known-vulnerable packages by default", a, at.Version, strings.Join(at.KnownVulnerabilities, ", "), where))
			case at.Unfree:
				errs = append(errs, fmt.Sprintf("✗ nixpkgs package %q has an unfree licence\n%s", a, where))
			case !at.Available:
				errs = append(errs, fmt.Sprintf("✗ nixpkgs package %q is not available for %s\n%s", a, g.System, where))
			case at.EvalRefused || at.DrvPath == "":
				errs = append(errs, fmt.Sprintf("✗ nixpkgs package %q cannot be used (nixpkgs refused to evaluate it)\n%s", a, where))
			}
		}
		if p.Kind == ir.KindGoBinary {
			if at := ix.Attrs["go"]; at != nil && at.Version != "" && versionLess(at.Version, p.Go.GoVersion) {
				errs = append(errs, fmt.Sprintf("✗ %s needs Go %s but nixpkgs %s provides Go %s\n  defined at %s\n  hint: lower GoVersion or pin a newer nixpkgs", p.Label, p.Go.GoVersion, g.Nixpkgs.Rev[:12], at.Version, p.DefinedAt))
			}
		}
		if p.Kind == ir.KindFetch && !strings.HasPrefix(p.Fetch.Hash, "sha256-") {
			errs = append(errs, fmt.Sprintf("✗ %s has no pinned hash in bob.lock\n  defined at %s\n  hint: run bob lock", p.Label, p.DefinedAt))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}
	return nil
}

func versionLess(have, want string) bool {
	h, w := strings.Split(have, "."), strings.Split(want, ".")
	for i := 0; i < len(w); i++ {
		if i >= len(h) {
			return true
		}
		a, _ := strconv.Atoi(h[i])
		b, _ := strconv.Atoi(w[i])
		if a != b {
			return a < b
		}
	}
	return false
}

func cmdBuild(sub, graph, target, mode string, verbose, timing bool, gomode string) error {
	build := sub == "build"
	fine := gomode != "single"
	dryWaves = sub == "outs"
	t := newTimer(timing)
	g, err := loadRaw(graph)
	if err != nil {
		return err
	}
	pkgs, err := order(g, target)
	if err != nil {
		return err
	}
	t.mark("load+order")
	ix, err := nixidx.Open(g.Nixpkgs.Rev, g.System)
	if err != nil {
		return bobNixErr("cannot open nixpkgs "+g.Nixpkgs.Rev, err, verbose)
	}
	gn := gen.New(g, ix, nil)
	gn.Host, gn.Tgt, gn.V = gen.Platforms["linux-arm64"], cfg.Tgt, cfg.V
	need := map[string]bool{"go": true, "stdenv.cc": true, gn.CCAttr(): true}
	other := map[string]bool{} // attrs of the target system's own nixpkgs
	for _, p := range pkgs {
		for _, a := range gn.Toolchain(p) {
			need[a] = true
		}
		for _, a := range p.Nixpkgs {
			la := a
			if p.Kind == ir.KindCCLibrary {
				la = gn.LibAttr(a)
			}
			if strings.HasPrefix(la, "@") {
				other[a] = true
			} else {
				need[la] = true
			}
		}
		if p.Kind == ir.KindRustCrate && gn.Cross() {
			other["rustc.unwrapped"] = true
		}
	}
	var attrs []string
	for a := range need {
		attrs = append(attrs, a)
	}
	sort.Strings(attrs)
	if err := ix.Resolve(attrs); err != nil {
		return bobNixErr("cannot resolve nixpkgs packages", err, verbose)
	}
	if len(other) > 0 {
		ix2, err := nixidx.Open(g.Nixpkgs.Rev, cfg.Tgt.Nix)
		if err != nil {
			return bobNixErr("cannot open nixpkgs for "+cfg.Tgt.Nix, err, verbose)
		}
		var oa []string
		for a := range other {
			oa = append(oa, a)
		}
		sort.Strings(oa)
		if err := ix2.Resolve(oa); err != nil {
			return bobNixErr("cannot resolve target nixpkgs packages", err, verbose)
		}
		_ = ix2.Save()
		for _, a := range oa {
			ix.Attrs["@"+cfg.Tgt.Nix+"/"+a] = ix2.Attrs[a]
		}
	}
	t.mark("index")
	if err := precheck(g, ix, pkgs, gn); err != nil {
		return err
	}
	var dirs []string
	for _, p := range pkgs {
		if p.Src != nil && p.Src.Path != "" && !(fine && p.Kind == ir.KindGoBinary) {
			dirs = append(dirs, filepath.Join(g.Root, p.Src.Path))
		}
	}
	imported, err := run.ImportSources(dirs)
	if err != nil {
		return err
	}
	srcs := map[string]string{}
	for _, p := range pkgs {
		if p.Src != nil && p.Src.Path != "" && !(fine && p.Kind == ir.KindGoBinary) {
			srcs[p.Label] = imported[filepath.Join(g.Root, p.Src.Path)]
		}
	}
	t.mark("import sources")
	gn.Srcs = srcs
	var gb *gen.GoBuild
	if !fine {
		if err := gn.Generate(pkgs); err != nil {
			return fmt.Errorf("✗ %v", err)
		}
	} else {
		var other []*ir.Package
		var gps []*ir.Package
		for _, p := range pkgs {
			if p.Kind == ir.KindGoBinary {
				gps = append(gps, p)
			} else {
				other = append(other, p)
			}
		}
		if len(gps) > 1 {
			return fmt.Errorf("✗ fine-grained Go mode needs exactly one go_binary in the closure (got %d)", len(gps))
		}
		if err := gn.Generate(other); err != nil {
			return fmt.Errorf("✗ %v", err)
		}
		if len(gps) == 1 {
			if gb, err = planGo(g, gn, gps[0], gomode, mode, t); err != nil {
				return err
			}
		}
	}
	t.mark("compute drvs")
	if sub == "list" { // every derivation of the closure: name and .drv path
		for _, d := range gn.Order {
			fmt.Println(d.Name, d.DrvPath)
		}
		return nil
	}
	if sub == "outs" { // output paths of every bob-generated derivation (for measure.sh cleanup)
		for _, d := range gn.Order {
			kind := "build"
			if q := g.Get(gn.Labels[d.DrvPath]); q != nil && q.Kind == ir.KindGoBinary {
				kind = "go" // bob's Go package/module/binary derivations
			} else if d.IsFixed() {
				kind = "fetch"
			} else if strings.HasPrefix(d.Name, "go-std-") {
				kind = "std"
			} else if strings.HasSuffix(d.Name, "-src") {
				kind = "unpack"
			}
			fmt.Println(kind, d.OutPath)
		}
		return nil
	}
	missing := 0
	for _, d := range gn.Order {
		if _, err := os.Stat(d.OutPath); err != nil {
			missing++
		}
	}
	if timing {
		fmt.Fprintf(os.Stderr, "[count] derivations=%d to-build=%d\n", len(gn.Order), missing)
	}
	n, err := run.AddDerivations(gn.Order, mode)
	if err != nil {
		return err
	}
	t.mark(fmt.Sprintf("add %d drvs (%s)", n, mode))
	_ = ix.Save()
	var roots []string
	if target != "" {
		roots = []string{gn.Built[target].Drv.DrvPath}
	} else {
		for _, p := range pkgs {
			roots = append(roots, gn.Built[p.Label].Drv.DrvPath)
		}
	}
	if !build {
		fmt.Printf("%d packages, %d derivations", len(pkgs), len(gn.Order))
		if gb != nil {
			fmt.Printf(" (go packages %d, go groups %d)", len(gb.Plan.Order), len(gb.Groups))
		}
		fmt.Println()
		for _, r := range roots[max(0, len(roots)-3):] {
			fmt.Println(r)
		}
		return nil
	}
	res, err := run.Build(roots)
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
	return translate(g, gn, res)
}

// planGo realises what the planner must read (module sources, generated code), plans the Go
// package graph in-process and creates the fine-grained derivations.
func planGo(g *ir.Graph, gn *gen.Gen, gp *ir.Package, gomode, addMode string, t *timer) (*gen.GoBuild, error) {
	mods, gens, err := gn.GoPlanInputs(gp)
	if err != nil {
		return nil, fmt.Errorf("✗ %v", err)
	}
	var pre []string
	for _, d := range append(mapVals(mods), mapVals(gens)...) {
		if _, err := os.Stat(d.OutPath); err != nil {
			pre = append(pre, d.DrvPath)
		}
	}
	if len(pre) > 0 {
		if _, err := run.AddDerivations(gn.Order, addMode); err != nil {
			return nil, err
		}
		res, err := run.Build(pre)
		if err != nil {
			return nil, err
		}
		if !res.OK {
			return nil, translate(g, gn, res)
		}
	}
	t.mark(fmt.Sprintf("plan inputs (%d built)", len(pre)))
	goroot := gn.Ix.Attrs["go"].Outputs["out"] + "/share/go"
	pl := gopkg.NewPlanner(goroot, gn.Tgt.GOARCH, gp.Go.CGo, gn.GoToolTags())
	pl.Modules = append(pl.Modules, &gopkg.Module{Path: gp.Go.ModulePath, Dir: filepath.Join(g.Root, gp.Src.Path), Lang: gp.Go.GoVersion, Main: true})
	for _, r := range gp.Go.Requires {
		dir := filepath.Join(mods[r.Path].OutPath, r.Path+"@"+r.Version)
		pl.Modules = append(pl.Modules, &gopkg.Module{Path: r.Path, Version: r.Version, Dir: dir, Lang: gopkg.GoDirective(filepath.Join(dir, "go.mod"))})
	}
	for ip, d := range gens {
		pl.Overrides[ip] = d.OutPath
	}
	mainIP := strings.TrimSuffix(gp.Go.ModulePath+"/"+strings.TrimPrefix(strings.TrimPrefix(gp.Go.Main, "."), "/"), "/")
	plan, err := pl.Load(mainIP)
	if err != nil {
		return nil, fmt.Errorf("✗ %s: %v\n  defined at %s", gp.Label, err, gp.DefinedAt)
	}
	t.mark(fmt.Sprintf("plan %d go packages", len(plan.Order)))
	var sets []run.FileSet
	for _, p := range plan.Order {
		if p.Mod.Main && !p.Generated {
			sets = append(sets, run.FileSet{Key: p.ImportPath, Dir: p.Dir, Name: "gosrc-" + strings.ReplaceAll(p.ImportPath, "/", "-"), Files: p.Files()})
		}
	}
	srcStore, err := run.ImportFiles(sets)
	if err != nil {
		return nil, err
	}
	t.mark(fmt.Sprintf("import %d package sources", len(sets)))
	gb := &gen.GoBuild{Gran: gomode, Plan: plan, SrcStore: srcStore, GenDrv: gens, ModDrv: mods, CGo: gp.Go.CGo, Lib: gp, GOARCH: gn.Tgt.GOARCH}
	var final *drv.Derivation
	if gomode == "cutoff" {
		gb.Gran, gb.Cutoff, gb.Exports = "hybrid", true, map[string]string{}
		final, err = goWaves(g, gn, gb, addMode, t, dryWaves)
		if err == errDry {
			gn.Built[gp.Label] = &gen.Built{Pkg: gp, Drv: &drv.Derivation{}}
			return gb, nil
		}
	} else {
		final, err = gn.GoFine(gb)
	}
	if err != nil {
		return nil, fmt.Errorf("✗ %v", err)
	}
	gn.Built[gp.Label] = &gen.Built{Pkg: gp, Drv: final}
	gn.Labels[final.DrvPath] = gp.Label
	return gb, nil
}

func mapVals(m map[string]*drv.Derivation) []*drv.Derivation {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var out []*drv.Derivation
	for _, k := range ks {
		out = append(out, m[k])
	}
	return out
}

func translate(g *ir.Graph, gn *gen.Gen, res *run.Result) error {
	var out []string
	for _, f := range res.Failures {
		label := gn.Labels[f.Drv]
		p := g.Get(label)
		if p == nil {
			out = append(out, fmt.Sprintf("✗ nixpkgs dependency failed to build: %s\n  (this is not your code; run with -v for the full log)", f.Drv))
			continue
		}
		if f.Kind == "hash" {
			got := regexp.MustCompile(`got:\s+(sha256-\S+)`).FindStringSubmatch(f.Msg)
			msg := fmt.Sprintf("✗ %s  download does not match bob.lock  (defined at %s)\n  url: %s\n  bob.lock: %s", label, p.DefinedAt, p.Fetch.URL, p.Fetch.Hash)
			if len(got) > 1 {
				msg += "\n  downloaded: " + got[1]
			}
			out = append(out, msg+"\n  hint: if the upstream file legitimately changed, run bob lock; otherwise the source was tampered with")
			continue
		}
		var lines []string
		for _, l := range f.Log {
			l = run.StripANSI(l)
			for lbl, sp := range gn.Srcs { // show repo paths, not store paths
				if q := g.Get(lbl); q != nil && q.Src != nil {
					l = strings.ReplaceAll(l, sp, q.Src.Path)
				}
			}
			lines = append(lines, l)
		}
		ex, hint := excerpt(lines)
		msg := fmt.Sprintf("✗ %s  build failed  (defined at %s)\n%s", label, p.DefinedAt, indent(ex))
		if hint != "" {
			msg += "\n  hint: " + hint
		}
		out = append(out, msg+"\n  full log: bob-s07 build -v …")
	}
	if len(out) == 0 {
		return fmt.Errorf("✗ build failed (no failing package could be identified; run with -v)")
	}
	return fmt.Errorf("%s", strings.Join(out, "\n"))
}

var (
	undefRef = regexp.MustCompile("undefined reference to `([^']+)'")
	noHeader = regexp.MustCompile(`fatal error: ([^:]+): No such file or directory`)
	goTooOld = regexp.MustCompile(`go: (\S+) requires go >= (\S+) \(running go (\S+);`)
)

func excerpt(log []string) ([]string, string) {
	var hits []string
	hint := ""
	for _, l := range log {
		if strings.Contains(l, "error") || strings.Contains(l, "undefined reference") {
			hits = append(hits, strings.TrimSpace(l))
		}
		if m := undefRef.FindStringSubmatch(l); m != nil && hint == "" {
			hint = fmt.Sprintf("symbol %s is not provided by any linked library — a dependency is probably missing from Deps", m[1])
		}
		if m := goTooOld.FindStringSubmatch(l); m != nil && hint == "" {
			hint = fmt.Sprintf("dependency %s needs Go %s but the toolchain is Go %s — use an older version of it or a newer nixpkgs", m[1], m[2], m[3])
			hits = append(hits, strings.TrimSpace(l))
		}
		if m := noHeader.FindStringSubmatch(l); m != nil && hint == "" {
			hint = fmt.Sprintf("header %s not found — add the package that provides it to Deps", m[1])
		}
	}
	if len(hits) == 0 {
		hits = log[max(0, len(log)-10):]
	}
	if len(hits) > 10 {
		hits = hits[:10]
	}
	return hits, hint
}

func indent(xs []string) string {
	for i := range xs {
		xs[i] = "  │ " + xs[i]
	}
	return strings.Join(xs, "\n")
}

func bobNixErr(what string, err error, verbose bool) error {
	msg := "✗ " + what
	if ne, ok := err.(*nixidx.NixError); ok {
		if verbose {
			msg += "\n" + ne.Stderr
		} else {
			msg += " (internal error in bob's nixpkgs lookup; run with -v)"
		}
		return fmt.Errorf("%s", msg)
	}
	return fmt.Errorf("%s: %v", msg, err)
}

type timer struct {
	on   bool
	last time.Time
	t0   time.Time
}

func newTimer(on bool) *timer { n := time.Now(); return &timer{on: on, last: n, t0: n} }

func (t *timer) mark(what string) {
	if t.on {
		n := time.Now()
		fmt.Fprintf(os.Stderr, "[time] %-28s %8.3fs (total %.3fs)\n", what, n.Sub(t.last).Seconds(), n.Sub(t.t0).Seconds())
		t.last = n
	}
}

// goWaves builds Go groups level by level. After each level the export data of its packages is
// added to the store by content, and dependents reference those content-addressed paths: when a
// change leaves a package's export data unchanged, the dependents' derivations stay the same
// (early cutoff with input-addressed derivations only).
var dryWaves bool // `outs` must not build: stop at the first level whose outputs are missing

var errDry = fmt.Errorf("dry run stopped")

func goWaves(g *ir.Graph, gn *gen.Gen, gb *gen.GoBuild, addMode string, t *timer, dry bool) (*drv.Derivation, error) {
	order, err := gn.GoPrepare(gb)
	if err != nil {
		return nil, err
	}
	cachePath := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "bob-s07", "exports.json")
	cache := map[string]string{}
	if b, err := os.ReadFile(cachePath); err == nil {
		_ = json.Unmarshal(b, &cache)
	}
	maxL := 0
	for _, gr := range order {
		maxL = max(maxL, gr.Level)
	}
	built, waves := 0, 0
	for l := 0; l <= maxL; l++ {
		var lv []*gen.Group
		var need []string
		for _, gr := range order {
			if gr.Level != l {
				continue
			}
			if err := gn.GroupDrv(gb, gr); err != nil {
				return nil, err
			}
			lv = append(lv, gr)
			if _, err := os.Stat(gr.Drv.OutPath); err != nil {
				need = append(need, gr.Drv.DrvPath)
			}
		}
		if len(need) > 0 && dry {
			return nil, errDry
		}
		if len(need) > 0 {
			waves++
			built += len(need)
			if _, err := run.AddDerivations(gn.Order, addMode); err != nil {
				return nil, err
			}
			res, err := run.Build(need)
			if err != nil {
				return nil, err
			}
			if !res.OK {
				return nil, translate(g, gn, res)
			}
		}
		var files, keys []string
		for _, gr := range lv {
			for _, p := range gr.Pkgs {
				f := gr.Drv.OutPath + "/" + p.ImportPath + ".x"
				if sp, ok := cache[f]; ok {
					if _, err := os.Stat(sp); err == nil {
						gb.Exports[p.ImportPath] = sp
						continue
					}
				}
				files = append(files, f)
				keys = append(keys, p.ImportPath)
			}
		}
		if len(files) > 0 {
			out, err := exec.Command("nix-store", append([]string{"--add"}, files...)...).Output()
			if err != nil {
				return nil, fmt.Errorf("import export data: %v", err)
			}
			sps := strings.Fields(string(out))
			if len(sps) != len(files) {
				return nil, fmt.Errorf("import export data: got %d paths for %d files", len(sps), len(files))
			}
			for i, f := range files {
				gb.Exports[keys[i]] = sps[i]
				cache[f] = sps[i]
			}
		}
	}
	if b, err := json.Marshal(cache); err == nil {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
		_ = os.WriteFile(cachePath, b, 0o644)
	}
	t.mark(fmt.Sprintf("go waves: %d levels, %d groups built in %d waves", maxL+1, built, waves))
	return gn.LinkDrv(gb)
}
