package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bobspike/s04/drv"
	"bobspike/s04/gen"
	"bobspike/s04/nixidx"
	"bobspike/s04/npm"
	"bobspike/s04/run"
)

const rev = "cd4ea8d57de16f4810bc5f14207bac45fbf19f67"

// Project is the spike's stand-in for the build part of bob.go (bob.json next to package.json).
type Project struct {
	Allow   []string `json:"allow"` // lifecycle allowlist
	Libs    []struct {
		Importer, Name, Consumer string
	} `json:"libs"`
	Targets []struct {
		Name     string   `json:"name"`
		Importer string   `json:"importer"`
		Layout   string   `json:"layout"` // isolated (default) | hoisted
		Tools    []string `json:"tools"`
		Cmd      string   `json:"cmd"`
	} `json:"targets"`
}

type timer struct {
	t0    time.Time
	marks []string
}

func (t *timer) mark(what string) {
	now := time.Now()
	t.marks = append(t.marks, fmt.Sprintf("%s %.2fs", what, now.Sub(t.t0).Seconds()))
	t.t0 = now
}

func spikeDir() string {
	exe, _ := os.Executable()
	return filepath.Dir(filepath.Dir(exe))
}

// plan computes all derivations for a project dir; returns target name -> drv, plus extra outputs.
func plan(dir string, t *timer, only string) (*gen.Gen, map[string]*drv.Derivation, error) {
	var lock npm.Lock
	if err := readJSON(filepath.Join(dir, "bob.lock.json"), &lock); err != nil {
		return nil, nil, fmt.Errorf("read bob.lock: %w", err)
	}
	var pj Project
	if err := readJSON(filepath.Join(dir, "bob.json"), &pj); err != nil {
		return nil, nil, err
	}
	t.mark("read lock")
	ix, err := nixidx.Open(rev, "aarch64-linux")
	if err != nil {
		return nil, nil, err
	}
	attrs := map[string]bool{"bash": true, "coreutils": true, "gnutar": true, "gzip": true, "findutils": true,
		"gnused": true, "nodejs": true, "python3": true, "gnumake": true, "stdenv.cc": true, "gnugrep": true, "nodejs-slim": true}
	for _, tg := range pj.Targets {
		for _, a := range tg.Tools {
			attrs[a] = true
		}
	}
	var al []string
	for a := range attrs {
		al = append(al, a)
	}
	sort.Strings(al)
	if err := ix.Resolve(al); err != nil {
		return nil, nil, err
	}
	for _, a := range al {
		if ix.Attrs[a] == nil || ix.Attrs[a].DrvPath == "" {
			return nil, nil, fmt.Errorf("nixpkgs has no package %q", a)
		}
	}
	t.mark("nixpkgs index")
	srcs := []run.Src{{Key: "builder", Dir: filepath.Join(spikeDir(), "builder"), Name: "bob-npm-builder"}}
	skip := func(rel string, isDir bool) bool {
		b := path.Base(rel)
		return !(b == "node_modules" || b == "dist" || b == ".bob" || b == "bob.lock.json" || b == "result" || b == ".vite")
	}
	for imp := range lock.Importers {
		srcs = append(srcs, run.Src{Key: imp, Dir: filepath.Join(dir, imp), Name: gen.StoreName("src-" + filepath.Base(filepath.Join(dir, imp))), Include: skip})
	}
	sp, err := run.ImportSources(srcs)
	if err != nil {
		return nil, nil, err
	}
	t.mark("import sources")
	allow := map[string]bool{}
	for _, a := range pj.Allow {
		allow[a] = true
	}
	g := gen.New(ix, &lock, npm.Platform{OS: "linux", CPU: "arm64", Libc: "glibc"}, allow, sp["builder"])
	links := map[string]*drv.Derivation{}
	for _, l := range pj.Libs {
		d, err := g.WorkspaceLib(l.Importer, l.Name, sp[l.Importer], l.Consumer)
		if err != nil {
			return nil, nil, err
		}
		links[l.Importer] = d
	}
	outs := map[string]*drv.Derivation{}
	for _, tg := range pj.Targets {
		if only != "" && tg.Name != only {
			continue
		}
		var nm *drv.Derivation
		nmName := gen.StoreName("node_modules-" + filepath.Base(filepath.Join(dir, tg.Importer)))
		if tg.Layout == "hoisted" {
			nm, err = g.Hoisted(tg.Importer, nmName+"-hoisted")
		} else {
			nm, err = g.NodeModules(tg.Importer, links, nmName)
		}
		if err != nil {
			return nil, nil, err
		}
		outs[tg.Name+":node_modules"] = nm
		if tg.Cmd == "" {
			continue
		}
		d, err := g.Build(gen.StoreName(tg.Name), "//"+tg.Importer+":"+tg.Name, sp[tg.Importer], nm, tg.Tools, tg.Cmd)
		if err != nil {
			return nil, nil, err
		}
		outs[tg.Name] = d
	}
	t.mark(fmt.Sprintf("compute %d drvs", len(g.Order)))
	n, err := run.AddDerivations(g.Order, "import")
	if err != nil {
		return nil, nil, err
	}
	t.mark(fmt.Sprintf("add %d new drvs", n))
	return g, outs, nil
}

func build(dir string, args []string) error {
	only := ""
	if len(args) > 0 {
		only = args[0]
	}
	t := &timer{t0: time.Now()}
	start := t.t0
	g, outs, err := plan(dir, t, only)
	if err != nil {
		return err
	}
	var paths []string
	names := sortedKeys(outs)
	for _, k := range names {
		paths = append(paths, outs[k].DrvPath)
	}
	res, err := run.Build(paths)
	if err != nil {
		return err
	}
	t.mark(fmt.Sprintf("nix build (%d built)", len(res.Builds)))
	for _, n := range g.Notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}
	if !res.OK {
		for _, f := range res.Failures {
			label := g.Labels[f.Drv]
			fmt.Fprintf(os.Stderr, "✗ %s failed (%s)\n", label, f.Kind)
			lg := f.Log
			if len(lg) > 25 {
				lg = lg[len(lg)-25:]
			}
			for _, l := range lg {
				fmt.Fprintln(os.Stderr, "│ "+run.StripANSI(l))
			}
		}
		return fmt.Errorf("build failed")
	}
	for _, k := range names {
		fmt.Printf("%s %s\n", k, outs[k].OutPath)
	}
	fmt.Fprintf(os.Stderr, "timing: %s | total %.2fs\n", strings.Join(t.marks, ", "), time.Since(start).Seconds())
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

var _ = json.Marshal

// materialise builds the importer's node_modules and installs it into the working tree as a
// writable directory of symlinks into the store (tool caches like node_modules/.vite survive).
func materialise(dir, target string) error {
	t := &timer{t0: time.Now()}
	start := t.t0
	_, outs, err := plan(dir, t, target)
	if err != nil {
		return err
	}
	nm := outs[target+":node_modules"]
	if nm == nil {
		return fmt.Errorf("no target %s", target)
	}
	res, err := run.Build([]string{nm.DrvPath})
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("node_modules build failed: %v", res.Failures)
	}
	t.mark("build node_modules")
	var pj Project
	_ = readJSON(filepath.Join(dir, "bob.json"), &pj)
	imp := "."
	for _, tg := range pj.Targets {
		if tg.Name == target {
			imp = tg.Importer
		}
	}
	wd := filepath.Join(dir, imp)
	dst := filepath.Join(wd, "node_modules")
	tmp := dst + ".bob-new"
	_ = os.RemoveAll(tmp)
	if out, err := exec.Command("cp", "-a", nm.OutPath+"/node_modules", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("cp: %v: %s", err, out)
	}
	_ = exec.Command("chmod", "-R", "u+w", tmp).Run()
	for _, keep := range []string{".vite", ".cache"} {
		if _, err := os.Stat(filepath.Join(dst, keep)); err == nil {
			_ = os.Rename(filepath.Join(dst, keep), filepath.Join(tmp, keep))
		}
	}
	old := dst + ".bob-old"
	_ = os.RemoveAll(old)
	_ = os.Rename(dst, old)
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	_ = os.RemoveAll(old)
	_ = os.MkdirAll(filepath.Join(wd, ".bob"), 0o755)
	if err := os.WriteFile(filepath.Join(wd, ".bob", "node_modules.path"), []byte(nm.OutPath+"\n"), 0o644); err != nil {
		return err
	}
	t.mark("materialise")
	fmt.Println(nm.OutPath)
	fmt.Fprintf(os.Stderr, "timing: %s | total %.2fs\n", strings.Join(t.marks, ", "), time.Since(start).Seconds())
	return nil
}

// paths prints the output paths of all derivations of a target ("fod" = only fetches, "built" = the rest).
func paths(dir, target, kind string) error {
	t := &timer{t0: time.Now()}
	g, _, err := plan(dir, t, target)
	if err != nil {
		return err
	}
	for _, d := range g.Order {
		if (kind == "fod") == d.IsFixed() || kind == "all" {
			fmt.Println(d.OutPath)
		}
	}
	return nil
}
