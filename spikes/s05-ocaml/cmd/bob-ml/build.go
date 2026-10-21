package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"bobspike/s05/drv"
	"bobspike/s05/nixidx"
	"bobspike/s05/ocamlgen"
	"bobspike/s05/run"
)

type timer struct {
	on   bool
	last time.Time
}

func (t *timer) mark(what string) {
	if t.on {
		fmt.Fprintf(os.Stderr, "  %-22s %6.3fs\n", what, time.Since(t.last).Seconds())
	}
	t.last = time.Now()
}

// setup loads the workspace, lock and nixpkgs index and imports local sources.
func setup(wsPath, lockPath string, mode ocamlgen.Mode, t *timer) (*Workspace, *ocamlgen.Gen, *ocamlgen.Local, error) {
	w, err := readWS(wsPath)
	if err != nil {
		return nil, nil, nil, err
	}
	l, err := readLock(lockPath)
	if err != nil {
		return nil, nil, nil, err
	}
	ix, err := nixidx.Open(nixpkgsRev, "aarch64-linux")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("✗ nixpkgs index: %v", err)
	}
	need := append([]string{}, ideTools...)
	for _, a := range ocamlgen.Toolchain {
		a, _, _ = strings.Cut(a, ":")
		need = append(need, a)
	}
	for n := range l.Packages {
		for _, a := range ocamlgen.ConfMap[n] {
			_ = 0
			a, _, _ = strings.Cut(a, ".")
			need = append(need, a)
		}
	}
	if err := ix.Resolve(need); err != nil {
		return nil, nil, nil, fmt.Errorf("✗ nixpkgs resolve: %v", err)
	}
	t.mark("nixpkgs index")
	dirs := map[string]string{}
	for _, c := range w.C {
		dirs["c:"+c.Name] = filepath.Join(w.Root, c.Src)
	}
	for _, x := range w.Libs {
		dirs["ml:"+x.Name] = filepath.Join(w.Root, x.Src)
	}
	for _, x := range w.Exes {
		dirs["ml:"+x.Name] = filepath.Join(w.Root, x.Src)
	}
	var list []string
	for _, d := range dirs {
		list = append(list, d)
	}
	sort.Strings(list)
	imported, err := run.ImportSources(list)
	if err != nil {
		return nil, nil, nil, err
	}
	srcs := map[string]string{}
	for k, d := range dirs {
		srcs[k] = imported[d]
	}
	t.mark("import sources")
	g := ocamlgen.New(ix, repoDir(), l, srcs)
	return w, g, ocamlgen.NewLocal(mode, w.Libs), nil
}

func buildCmd(sub string, args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	mode := fs.String("mode", "direct", "direct|dune for local OCaml code")
	timing := fs.Bool("t", false, "print phase timings")
	verbose := fs.Bool("v", false, "raw nix log on failure")
	fs.Parse(args)
	if fs.NArg() != 3 {
		return fmt.Errorf("usage: bob-ml build [-mode direct|dune] ws.json bob.lock target")
	}
	t := &timer{on: *timing, last: time.Now()}
	w, g, l, err := setup(fs.Arg(0), fs.Arg(1), ocamlgen.Mode(*mode), t)
	if err != nil {
		return err
	}
	target := fs.Arg(2)
	var root *drv.Derivation
	switch {
	case strings.HasPrefix(target, "opam:"):
		root, err = g.Package(strings.TrimPrefix(target, "opam:"))
	default:
		for _, x := range w.Exes {
			if x.Name == target {
				root, err = g.Exe(l, x)
			}
		}
		for _, x := range w.Libs {
			if x.Name == target {
				root, err = g.LocalLib(l, x)
			}
		}
		if root == nil && err == nil {
			err = fmt.Errorf("unknown target %s", target)
		}
	}
	if err != nil {
		return fmt.Errorf("✗ %v", err)
	}
	t.mark("compute drvs")
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
	fmt.Fprintf(os.Stderr, "%d derivations (%s)\n", len(g.Order), strings.Join(ks, " "))
	if sub == "outs" {
		for _, d := range g.Order {
			if !d.IsFixed() {
				fmt.Println(d.OutPath)
			}
		}
		return nil
	}
	if sub == "drvs" {
		fmt.Println(root.DrvPath)
		return nil
	}
	res, err := run.Build([]string{root.DrvPath})
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
	if *verbose {
		fmt.Fprint(os.Stderr, res.Raw)
	}
	return translate(g, res)
}

var mlErr = regexp.MustCompile(`^File "|^Error|^\s*Error:|bob-ml: command failed|undefined reference|^\S+\.c:\d+`)

func translate(g *ocamlgen.Gen, res *run.Result) error {
	var b strings.Builder
	for _, f := range res.Failures {
		label := g.Labels[f.Drv]
		if label == "" {
			label = f.Drv
		}
		fmt.Fprintf(&b, "✗ %s build failed\n", label)
		n := 0
		for i := range f.Log {
			f.Log[i] = run.StripANSI(f.Log[i])
		}
		for i, line := range f.Log {
			if strings.HasPrefix(line, "bob-ml: command failed") && n == 0 {
				for _, pv := range f.Log[max(0, i-6):i] {
					fmt.Fprintf(&b, "  %s\n", pv)
				}
			}
			if mlErr.MatchString(line) && n < 12 {
				fmt.Fprintf(&b, "  %s\n", line)
				if strings.HasPrefix(line, "File \"") && i+1 < len(f.Log) {
					for _, nx := range f.Log[i+1 : min(i+4, len(f.Log))] {
						fmt.Fprintf(&b, "  %s\n", nx)
					}
				}
				n++
			}
		}
		if n == 0 {
			for _, line := range f.Log[max(0, len(f.Log)-8):] {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

