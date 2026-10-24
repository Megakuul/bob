package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bobspike/s15/b/drv"
	"bobspike/s15/b/gen"
	"bobspike/s15/b/nixidx"
	"bobspike/s15/b/run"
	"bobspike/s15/ir"
	"bobspike/s15/translate"
)

type Meta = translate.Fixture

func cmdCapture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	name := fs.String("name", "", "fault name")
	nixv := fs.String("nix", "2.34.9", "Nix version (dir under nixbin/)")
	graph := fs.String("graph", "", "graph JSON")
	target := fs.String("target", "", "label to build (default: all)")
	store := fs.String("store", "", "store URI (NIX_REMOTE) for an isolated test store")
	sigint := fs.Duration("sigint", 0, "send SIGINT to Nix after this long")
	sigkill := fs.Duration("sigkill", 0, "send SIGKILL to Nix after this long")
	wrap := fs.String("wrap", "", "command prefix (e.g. systemd-run scope)")
	nixexe := fs.String("nixexe", "", "nix executable for the build step (default nixbin/<nix>/nix)")
	outDir := fs.String("out", "fixtures", "fixture root")
	note := fs.String("note", "", "note stored in meta.json")
	copyDrvs := fs.Bool("copydrvs", false, "copy nixpkgs .drv closures into -store first (cold-store captures)")
	var nixargs, inject, env multi
	fs.Var(&nixargs, "nixarg", "extra argument for nix build (repeatable)")
	fs.Var(&inject, "inject", "attr=EXPR: add a fake nixpkgs attribute evaluated from EXPR (fault injection)")
	fs.Var(&env, "env", "K=V for the Nix process (repeatable)")
	_ = fs.Parse(args)
	if *name == "" || *graph == "" {
		return errors.New("-name and -graph are required")
	}
	here, _ := os.Getwd()
	run.NixDir = filepath.Join(here, "nixbin", *nixv)
	if *store != "" {
		run.ExtraEnv = append(run.ExtraEnv, "NIX_REMOTE="+*store)
		run.AlwaysImport = true
	}
	run.ExtraEnv = append(run.ExtraEnv, env...)
	dir := filepath.Join(*outDir, *name, *nixv)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	meta := &Meta{Name: *name, Nix: *nixv, Note: *note}

	g, err := loadRaw(*graph)
	if err != nil {
		return err
	}
	pkgs, perr := order(g, *target)
	if perr != nil {
		meta.Precheck = []*translate.Error{perr}
		meta.Outcome.ExitCode = -1
		return writeJSON(filepath.Join(dir, "meta.json"), meta)
	}
	ix, err := nixidx.Open(g.Nixpkgs.Rev, g.System)
	if err != nil {
		return err
	}
	for _, in := range inject {
		a, expr, _ := strings.Cut(in, "=")
		if err := injectAttr(ix, a, expr); err != nil {
			return fmt.Errorf("inject %s: %w", a, err)
		}
	}
	need := map[string]bool{}
	for _, p := range pkgs {
		for _, a := range append(gen.Toolchain(p), p.Nixpkgs...) {
			need[a] = true
		}
	}
	if err := ix.Resolve(sortedKeys(need)); err != nil {
		return err
	}
	if errs := precheck(g, ix, pkgs); len(errs) > 0 {
		meta.Precheck = errs
		meta.Outcome.ExitCode = -1
		return writeJSON(filepath.Join(dir, "meta.json"), meta)
	}
	var dirs []string
	for _, p := range pkgs {
		if p.Src != nil && p.Src.Path != "" {
			dirs = append(dirs, filepath.Join(g.Root, p.Src.Path))
		}
	}
	imported, err := run.ImportSources(dirs)
	if err != nil {
		return err
	}
	srcs := map[string]string{}
	for _, p := range pkgs {
		if p.Src != nil && p.Src.Path != "" {
			srcs[p.Label] = imported[filepath.Join(g.Root, p.Src.Path)]
		}
	}
	gn := gen.New(g, ix, srcs)
	if err := gn.Generate(pkgs); err != nil {
		return err
	}
	if *copyDrvs && *store != "" {
		var nps []string
		for _, d := range gn.Order {
			for in := range d.InputDrvs {
				if _, mine := gn.Labels[in]; !mine {
					nps = append(nps, in)
				}
			}
		}
		c := exec.Command(run.Bin("nix"), append([]string{"copy", "--no-check-sigs", "--to", *store}, nps...)...)
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("copy drvs: %v: %s", err, out)
		}
	}
	if _, err := run.AddDerivations(gn.Order, "import"); err != nil {
		return err
	}
	ctx := buildContext(g, ix, gn, pkgs)
	var roots []string
	if *target != "" {
		roots = []string{gn.Built[*target].Drv.DrvPath}
		ctx.Requested = []string{*target}
	} else {
		for _, p := range pkgs {
			roots = append(roots, gn.Built[p.Label].Drv.DrvPath)
			ctx.Requested = append(ctx.Requested, p.Label)
		}
	}
	meta.Context = ctx
	ctx.Limits.LocalBuilds = true
	for i := 0; i < len(nixargs); i++ {
		a := nixargs[i]
		next := func() string {
			if i+1 < len(nixargs) {
				i++
				return nixargs[i]
			}
			return ""
		}
		switch a {
		case "--timeout":
			ctx.Limits.TimeoutS, _ = strconv.Atoi(next())
		case "--max-silent-time":
			ctx.Limits.MaxSilentS, _ = strconv.Atoi(next())
		case "--option":
			k, v := next(), next()
			switch k {
			case "max-build-log-size":
				ctx.Limits.MaxLogBytes, _ = strconv.ParseInt(v, 10, 64)
			case "max-jobs":
				ctx.Limits.LocalBuilds = v != "0"
			case "substituters":
				for _, u := range strings.Fields(v) {
					ctx.Substituters = append(ctx.Substituters, translate.Substituter{URL: u, Name: u, Auth: strings.HasPrefix(u, "http://127.0.0.1")})
				}
			}
		}
	}
	if ctx.Substituters == nil {
		ctx.Substituters = []translate.Substituter{{URL: "https://cache.nixos.org", Name: "cache.nixos.org"}}
	}

	cargs := []string{run.Bin("nix"), "build", "--no-link", "--print-out-paths", "--log-format", "internal-json", "-L", "--keep-going"}
	if *nixexe != "" {
		cargs[0] = *nixexe
	}
	cargs = append(cargs, nixargs...)
	for _, r := range roots {
		cargs = append(cargs, r+"^out")
	}
	if *wrap != "" {
		cargs = append(strings.Fields(*wrap), cargs...)
	}
	meta.Command = cargs
	cmd := exec.Command(cargs[0], cargs[1:]...)
	cmd.Env = append(os.Environ(), run.ExtraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		return err
	}
	if *sigint > 0 {
		time.AfterFunc(*sigint, func() { _ = cmd.Process.Signal(syscall.SIGINT) })
		meta.Outcome.Interrupt = true
	}
	if *sigkill > 0 {
		time.AfterFunc(*sigkill, func() { _ = cmd.Process.Kill() })
	}
	werr := cmd.Wait()
	meta.Outcome.DurationMs = time.Since(t0).Milliseconds()
	meta.Outcome.NixVersion = *nixv
	if werr != nil {
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			ws := ee.Sys().(syscall.WaitStatus)
			if ws.Signaled() {
				meta.Outcome.Signal = ws.Signal().String()
				meta.Outcome.ExitCode = 128 + int(ws.Signal())
			} else {
				meta.Outcome.ExitCode = ws.ExitStatus()
			}
		} else {
			return werr
		}
	}
	meta.Stdout = strings.Fields(stdout.String())
	// bob checks which outputs are valid after the run (store database, not file existence:
	// a full disk can leave a partial output file that is not registered)
	var outs []string
	for _, d := range gn.Order {
		outs = append(outs, d.OutPath)
	}
	vc := exec.Command(run.Bin("nix-store"), append([]string{"--check-validity", "--print-invalid"}, outs...)...)
	vc.Env = append(os.Environ(), run.ExtraEnv...)
	invOut, _ := vc.Output()
	invalid := map[string]bool{}
	for _, p := range strings.Fields(string(invOut)) {
		invalid[p] = true
	}
	for _, d := range gn.Order {
		if !invalid[d.OutPath] {
			meta.Outcome.Valid = append(meta.Outcome.Valid, d.DrvPath)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.log"), stderr.Bytes(), 0o644); err != nil {
		return err
	}
	ctx.Chains = chains(ctx, stderr.Bytes())
	fmt.Printf("%s/%s: exit=%d signal=%s %dms, %d stderr lines\n", *name, *nixv, meta.Outcome.ExitCode, meta.Outcome.Signal,
		meta.Outcome.DurationMs, bytes.Count(stderr.Bytes(), []byte("\n")))
	return writeJSON(filepath.Join(dir, "meta.json"), meta)
}

func buildContext(g *ir.Graph, ix *nixidx.Index, gn *gen.Gen, pkgs []*ir.Package) *translate.Context {
	ctx := &translate.Context{Packages: map[string]translate.Pkg{}, Outputs: map[string]string{},
		Sources: map[string]string{}, Nixpkgs: map[string]string{}, Users: map[string][]string{}}
	for d, l := range gn.Labels {
		p := g.Get(l)
		tp := translate.Pkg{Label: l, DefinedAt: p.DefinedAt, Kind: string(p.Kind)}
		if b := gn.Built[l]; b.Unpk != nil && b.Unpk.DrvPath == d {
			tp.Role = "unpack"
		}
		if p.Fetch != nil {
			tp.URL, tp.LockHash = p.Fetch.URL, p.Fetch.Hash
		}
		if p.Src != nil && p.Src.Path != "" {
			tp.SrcDir = p.Src.Path
		}
		ctx.Packages[d] = tp
	}
	for _, d := range gn.Order {
		ctx.Outputs[d.OutPath] = d.DrvPath
	}
	for l, sp := range gn.Srcs {
		if p := g.Get(l); p != nil && p.Src != nil {
			ctx.Sources[sp] = p.Src.Path
		}
	}
	for _, p := range pkgs {
		for _, a := range append(gen.Toolchain(p), p.Nixpkgs...) {
			if at := ix.Attrs[a]; at != nil && at.DrvPath != "" {
				ctx.Nixpkgs[at.DrvPath] = a
				if !contains(ctx.Users[at.DrvPath], p.Label) {
					ctx.Users[at.DrvPath] = append(ctx.Users[at.DrvPath], p.Label)
				}
			}
		}
	}
	return ctx
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

var drvRe = regexp.MustCompile(`/nix/store/[0-9a-z]{32}-[^'"\s]+\.drv`)

// chains finds, for every drv named in an error message that bob did not generate, the path
// from a direct nixpkgs input of a bob package down to it (bob does this lazily from .drv files).
func chains(ctx *translate.Context, stream []byte) map[string][]string {
	out := map[string][]string{}
	for _, d := range drvRe.FindAllString(string(stream), -1) {
		if _, ok := ctx.Packages[d]; ok || out[d] != nil {
			continue
		}
		if _, ok := ctx.Nixpkgs[d]; ok {
			out[d] = []string{d}
			continue
		}
		for root := range ctx.Nixpkgs {
			if p := findPath(root, d, map[string]bool{}); p != nil {
				out[d] = p
				break
			}
		}
	}
	return out
}

func findPath(from, to string, seen map[string]bool) []string {
	if from == to {
		return []string{to}
	}
	if seen[from] {
		return nil
	}
	seen[from] = true
	b, err := os.ReadFile(from)
	if err != nil {
		return nil
	}
	p, err := drv.Parse(string(b))
	if err != nil {
		return nil
	}
	for _, in := range p.InputDrvs {
		if rest := findPath(in.Path, to, seen); rest != nil {
			return append([]string{from}, rest...)
		}
	}
	return nil
}

// injectAttr evaluates EXPR (must yield a derivation) and registers it as a nixpkgs attribute.
func injectAttr(ix *nixidx.Index, attr, expr string) error {
	full := fmt.Sprintf("let d = (%s); in { drvPath = d.drvPath; out = d.outPath; version = d.version or \"\"; }", expr)
	c := exec.Command("nix", "eval", "--json", "--impure", "--expr", full)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, err := c.Output()
	if err != nil {
		return fmt.Errorf("%v: %s", err, stderr.String())
	}
	var r struct{ DrvPath, Out, Version string }
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	ix.Attrs[attr] = &nixidx.Attr{Available: true, Version: r.Version, DrvPath: r.DrvPath, Outputs: map[string]string{"out": r.Out}}
	return nil
}

func loadRaw(path string) (*ir.Graph, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g ir.Graph
	return &g, json.Unmarshal(b, &g)
}

// order topologically sorts the closure of target (from backend B), reporting cycles.
func order(g *ir.Graph, target string) ([]*ir.Package, *translate.Error) {
	byLabel := map[string]*ir.Package{}
	for _, p := range g.Packages {
		byLabel[p.Label] = p
	}
	state := map[string]int{}
	var out []*ir.Package
	var stack []string
	var visit func(l string, from *ir.Package) *translate.Error
	visit = func(l string, from *ir.Package) *translate.Error {
		p := byLabel[l]
		if p == nil {
			e := translate.NewError("BOB-E-1007", map[string]string{"label": from.Label, "dep": l})
			e.DefinedAt = from.DefinedAt
			return e
		}
		switch state[l] {
		case 1:
			i := 0
			for i < len(stack) && stack[i] != l {
				i++
			}
			cyc := append(append([]string{}, stack[i:]...), l)
			e := translate.NewError("BOB-E-1006", map[string]string{"cycle": strings.Join(cyc, " → ")})
			for _, c := range cyc[:len(cyc)-1] {
				e.Excerpt = append(e.Excerpt, fmt.Sprintf("%s  defined at %s", c, byLabel[c].DefinedAt))
			}
			e.Label, e.DefinedAt = cyc[0], byLabel[cyc[0]].DefinedAt
			return e
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
		if err := visit(r, byLabel[r]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// precheck validates nixpkgs attributes and toolchain versions before any derivation exists (B).
func precheck(g *ir.Graph, ix *nixidx.Index, pkgs []*ir.Package) []*translate.Error {
	var errs []*translate.Error
	add := func(c translate.Code, p *ir.Package, args map[string]string) {
		args["label"] = p.Label
		e := translate.NewError(c, args)
		e.DefinedAt = p.DefinedAt
		errs = append(errs, e)
	}
	for _, p := range pkgs {
		for _, a := range p.Nixpkgs {
			at := ix.Attrs[a]
			switch {
			case at == nil || at.Missing:
				args := map[string]string{"attr": a}
				if s := ix.Suggest(a); len(s) > 0 {
					args["suggest"] = strings.Join(s, ", ")
				}
				add("BOB-E-1001", p, args)
				if errs[len(errs)-1].Args["suggest"] == "" {
					errs[len(errs)-1].Hint = ""
				}
			case at.Broken:
				add("BOB-E-1002", p, map[string]string{"attr": a, "version": at.Version})
			case at.Insecure:
				add("BOB-E-1003", p, map[string]string{"attr": a, "version": at.Version, "cves": strings.Join(at.KnownVulnerabilities, ", ")})
			case at.Unfree:
				add("BOB-E-1004", p, map[string]string{"attr": a})
			case !at.Available:
				add("BOB-E-1005", p, map[string]string{"attr": a, "system": g.System})
			}
		}
		if p.Kind == ir.KindGoBinary {
			if at := ix.Attrs["go"]; at != nil && at.Version != "" && versionLess(at.Version, p.Go.GoVersion) {
				add("BOB-E-1008", p, map[string]string{"want": p.Go.GoVersion, "have": at.Version, "rev": g.Nixpkgs.Rev[:12]})
			}
		}
		if p.Kind == ir.KindFetch && !strings.HasPrefix(p.Fetch.Hash, "sha256-") {
			add("BOB-E-1009", p, map[string]string{})
		}
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Code < errs[j].Code })
	return errs
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

var _ = bufio.NewScanner
