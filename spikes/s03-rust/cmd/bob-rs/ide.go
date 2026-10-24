package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"bobspike/s03/bspec"
	"bobspike/s03/crates"
	"bobspike/s03/drv"
	"bobspike/s03/nixidx"
	"bobspike/s03/run"
	"bobspike/s03/rustgen"
)

type rpCrate struct {
	DisplayName       string            `json:"display_name"`
	RootModule        string            `json:"root_module"`
	Edition           string            `json:"edition"`
	Version           string            `json:"version,omitempty"`
	Deps              []rpDep           `json:"deps"`
	IsWorkspaceMember bool              `json:"is_workspace_member"`
	Source            *rpSource         `json:"source,omitempty"`
	Cfg               []string          `json:"cfg"`
	Target            string            `json:"target"`
	Env               map[string]string `json:"env"`
	IsProcMacro       bool              `json:"is_proc_macro"`
	ProcMacroDylib    string            `json:"proc_macro_dylib_path,omitempty"`
}

type rpDep struct {
	Crate int    `json:"crate"`
	Name  string `json:"name"`
}

type rpSource struct {
	IncludeDirs []string `json:"include_dirs"`
	ExcludeDirs []string `json:"exclude_dirs"`
}

// cmdIDE writes rust-project.json for rust-analyzer (bob dev). It realises only what the IDE
// needs: crate sources, build-script runs (cfgs, OUT_DIR) and proc-macro dylibs.
func cmdIDE(wsPath, lockPath, target, out string) error {
	w, err := crates.ReadWorkspace(wsPath)
	if err != nil {
		return err
	}
	l, err := crates.ReadLock(lockPath)
	if err != nil {
		return err
	}
	pl := crates.Platforms["aarch64-unknown-linux-gnu"]
	p, err := crates.MakePlan(w, l, target, pl, pl, "rustc-1.95.0")
	if err != nil {
		return err
	}
	ix, err := nixidx.Open(nixpkgsRev, "aarch64-linux")
	if err != nil {
		return err
	}
	if err := ix.Resolve(append(append([]string{}, rustgen.Toolchain...), "cacert", "rustPlatform.rustLibSrc")); err != nil {
		return err
	}
	dirs := map[string]string{}
	for _, c := range w.Crates {
		dirs[c.Name] = filepath.Join(w.Root, c.Src)
	}
	list := []string{builderPath()}
	for _, d := range dirs {
		list = append(list, d)
	}
	sort.Strings(list)
	imported, err := run.ImportSources(uniq(list))
	if err != nil {
		return err
	}
	srcs := map[string]string{}
	for k, d := range dirs {
		srcs[k] = imported[d]
	}
	g := rustgen.New(ix, "aarch64-linux", imported[builderPath()], srcs, w, "1.95.0")
	if _, err := g.Unit(p.Root, p); err != nil {
		return err
	}
	if _, err := run.AddDerivations(g.Order, "import"); err != nil {
		return err
	}
	// realise sources, build-script runs and proc-macros only
	var need []string
	outs := map[string]*rustgen.Out{}
	for _, u := range p.Units {
		o, _ := g.Unit(u, p)
		outs[u.Key] = o
		if o.BSRun != nil {
			need = append(need, o.BSRun.DrvPath)
		}
		if u.ProcMacro() {
			need = append(need, o.Compile.DrvPath)
		}
	}
	for _, d := range g.Order {
		if d.IsFixed() {
			need = append(need, d.DrvPath)
		}
	}
	if len(need) > 0 {
		res, err := run.Build(need)
		if err != nil {
			return err
		}
		if !res.OK {
			return fmt.Errorf("✗ building IDE inputs failed (%d failures)", len(res.Failures))
		}
	}
	idx := map[string]int{}
	var cs []rpCrate
	for _, u := range p.Units { // dependency order: deps get lower indices
		o := outs[u.Key]
		c := rpCrate{DisplayName: u.Name, Edition: u.Edition(), Version: u.Version, Target: pl.Triple, Env: map[string]string{},
			IsProcMacro: u.ProcMacro()}
		var spec bspec.Rustc
		_ = json.Unmarshal([]byte(o.Compile.Env["bobSpec"]), &spec)
		for k, v := range spec.Env {
			c.Env[k] = v
		}
		if u.Ws != nil {
			dir := filepath.Join(w.Root, u.Ws.Src)
			c.RootModule = filepath.Join(dir, u.Ws.Root)
			c.IsWorkspaceMember = true
			c.Env["CARGO_MANIFEST_DIR"] = dir
		} else {
			c.RootModule = filepath.Join(o.SrcPath, spec.Root)
			c.Env["CARGO_MANIFEST_DIR"] = o.SrcPath
			c.Source = &rpSource{IncludeDirs: []string{o.SrcPath}, ExcludeDirs: []string{}}
		}
		for k, vs := range pl.Cfg {
			if len(vs) == 0 {
				c.Cfg = append(c.Cfg, k)
			}
			for _, v := range vs {
				c.Cfg = append(c.Cfg, fmt.Sprintf("%s=%q", k, v))
			}
		}
		for _, f := range u.Features {
			c.Cfg = append(c.Cfg, fmt.Sprintf("feature=%q", f))
		}
		if o.BSRun != nil {
			c.Env["OUT_DIR"] = filepath.Join(o.BSRun.OutPath, "out-dir")
			var d bspec.Directives
			if b, err := os.ReadFile(filepath.Join(o.BSRun.OutPath, "directives.json")); err == nil {
				_ = json.Unmarshal(b, &d)
			}
			c.Cfg = append(c.Cfg, d.Cfgs...)
			for k, v := range d.Env {
				c.Env[k] = v
			}
		}
		sort.Strings(c.Cfg)
		if u.ProcMacro() {
			c.ProcMacroDylib = filepath.Join(o.Compile.OutPath, "lib", o.LibFile)
		}
		for _, d := range u.Deps {
			c.Deps = append(c.Deps, rpDep{Crate: idx[d.Unit.Key], Name: d.Extern})
		}
		if c.Deps == nil {
			c.Deps = []rpDep{}
		}
		idx[u.Key] = len(cs)
		cs = append(cs, c)
	}
	rustc := ix.Attrs["rustc"].Outputs["out"]
	_ = rustc
	doc := map[string]any{
		"sysroot_src": ix.Attrs["rustPlatform.rustLibSrc"].Outputs["out"],
		"crates":      cs,
	}
	if sr := os.Getenv("BOB_RUST_SYSROOT"); sr != "" {
		doc["sysroot"] = sr
	}
	b, _ := json.MarshalIndent(doc, "", " ")
	if out == "" {
		out = filepath.Join(w.Root, "rust-project.json")
	}
	fmt.Fprintf(os.Stderr, "%d crates in rust-project.json\n", len(cs))
	return os.WriteFile(out, b, 0o644)
}

var _ = drv.StoreDir
