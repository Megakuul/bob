package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/lock"
	"github.com/megakuul/bob/pkg/rust/internal/rsspec"
	"github.com/megakuul/bob/pkg/rust/resolve"
)

// devTools are the nixpkgs tools of the Rust share of a dev shell besides the toolchain:
// cargo for cargo-based tools (never for builds, R-RS-001a), rust-analyzer, and the standard
// library sources rust-analyzer reads.
var devTools = []string{"cargo", "rust-analyzer", "rustPlatform.rustLibSrc"}

// PrepareDev is the Rust share of a dev shell (SPEC §03.3.4, engine.DevPreparer): rustc of the
// repository's crates, cargo and rust-analyzer on PATH, and rust-project.json (the crate graph
// of bob.lock: registry crates from their store sources, local crates from the working tree).
func (Ecosystem) PrepareDev(ctx context.Context, req engine.DevRequest) (prep engine.DevPrep, err error) {
	defer func() { // a bug here must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("rust dev shell: internal error: %v", v)
		}
	}()
	ws, err := readWorkspace(req.Graph)
	if err != nil {
		return prep, err
	}
	var local []*member
	for _, name := range ws.names {
		if m := ws.members[name]; strings.HasPrefix(m.node.Label, "//") {
			local = append(local, m)
		}
	}
	if len(local) == 0 {
		return prep, nil
	}
	m := local[0]
	if m.spec.Toolchain < 0 || m.spec.Toolchain >= len(m.node.Deps) {
		return prep, fmt.Errorf("%s has no Rust toolchain", m.node.Label)
	}
	tc, ok := req.Inputs[m.node.Deps[m.spec.Toolchain]]
	if !ok || tc.Outputs["out"] == "" {
		return prep, fmt.Errorf("the Rust toolchain of %s is not resolved", m.node.Label)
	}
	version := ""
	if v := rustcName().FindStringSubmatch(tc.Outputs["out"]); v != nil {
		version = v[1]
	}
	prep.Packages = append(prep.Packages, engine.DevPackage{Name: "rustc", Version: version, Path: tc.Outputs["out"], Drv: tc.Drv})
	libSrc := ""
	if req.Nixpkgs != nil {
		entries, err := req.Nixpkgs.Lookup(ctx, devTools)
		if err != nil {
			return prep, err
		}
		for _, attr := range devTools {
			e, ok := entries[attr]
			if !ok || e.DrvPath == "" {
				continue
			}
			p := e.Outputs["out"]
			if b, ok := e.Outputs["bin"]; ok {
				p = b
			}
			if attr == "rustPlatform.rustLibSrc" {
				libSrc = e.Outputs["out"]
			}
			prep.Packages = append(prep.Packages, engine.DevPackage{Name: attr, Version: e.Version, Path: p, Drv: e.DrvPath})
		}
	}
	if libSrc != "" {
		prep.InsideEnv = map[string]string{"RUST_SRC_PATH": libSrc}
	}
	var sec *lock.RustSection
	if len(req.Lock) > 0 {
		v, err := lock.DecodeSection(lock.SectionRust, req.Lock)
		if err != nil {
			return prep, err
		}
		sec = v.(*lock.RustSection)
	}
	pj, err := rustProject(ws, sec, req.Root, libSrc)
	if err != nil {
		return prep, err
	}
	prep.Files = append(prep.Files, engine.GeneratedFile{Path: "rust-project.json", Content: pj, Mode: 0o444, Owner: m.node.Label})
	return prep, nil
}

var _ engine.DevPreparer = Ecosystem{}

// projectCrate is one crate of rust-project.json (rust-analyzer's "non-Cargo based projects").
type projectCrate struct {
	DisplayName string            `json:"display_name"`
	RootModule  string            `json:"root_module"`
	Edition     string            `json:"edition"`
	Deps        []projectDep      `json:"deps"`
	Cfg         []string          `json:"cfg"`
	Env         map[string]string `json:"env"`
	IsMember    bool              `json:"is_workspace_member"`
	IsProcMacro bool              `json:"is_proc_macro"`
	Source      projectSource     `json:"source"`
}

type projectDep struct {
	Crate int    `json:"crate"`
	Name  string `json:"name"`
}

type projectSource struct {
	IncludeDirs []string `json:"include_dirs"`
	ExcludeDirs []string `json:"exclude_dirs"`
}

// rustProject writes rust-project.json for the host platform: one entry per crate unit of the
// unified graph of every local crate (with its tests' dev-dependencies).
func rustProject(ws *workspace, sec *lock.RustSection, root, libSrc string) ([]byte, error) {
	members, err := ws.resolveMembers()
	if err != nil {
		return nil, err
	}
	var crates []resolve.Crate
	if sec != nil {
		if crates, err = fromLockCrates(sec.Crates); err != nil {
			return nil, err
		}
	}
	byKey := map[string]*resolve.Crate{}
	for i := range crates {
		byKey[crates[i].Name+"@"+crates[i].Version] = &crates[i]
	}
	triple := map[string]string{"amd64": "x86_64-unknown-linux-gnu", "arm64": "aarch64-unknown-linux-gnu"}[runtime.GOARCH]
	plat, err := resolve.NewPlatform(triple)
	if err != nil {
		return nil, err
	}
	var roots []resolve.Root
	for _, name := range ws.names {
		roots = append(roots, resolve.Root{Member: name, Features: ws.members[name].spec.Enable, Default: true, Dev: true})
	}
	units, err := resolve.Unify(members, crates, roots, plat, plat)
	if err != nil {
		if len(crates) == 0 {
			units = nil // not locked yet: local crates only
		} else {
			return nil, lockMiss(err)
		}
	}
	// One entry per crate (the target role; host-only crates keep their host unit).
	index := map[string]int{}
	var order []resolve.Unit
	for _, u := range units {
		k := u.Key.Crate
		if u.Key.Local {
			k = "local:" + k
		}
		if i, seen := index[k]; seen {
			if order[i].Key.Host && !u.Key.Host {
				order[i] = u
			}
			continue
		}
		index[k] = len(order)
		order = append(order, u)
	}
	keyOf := func(k resolve.UnitKey) string {
		if k.Local {
			return "local:" + k.Crate
		}
		return k.Crate
	}
	out := struct {
		SysrootSrc string         `json:"sysroot_src,omitempty"`
		Crates     []projectCrate `json:"crates"`
	}{SysrootSrc: libSrc, Crates: []projectCrate{}}
	for _, u := range order {
		name, version, _ := strings.Cut(u.Key.Crate, "@")
		pc := projectCrate{DisplayName: name, Deps: []projectDep{}, Cfg: []string{}, Env: map[string]string{},
			Source: projectSource{IncludeDirs: []string{}, ExcludeDirs: []string{}}}
		var dir string
		if u.Key.Local {
			m := ws.members[name]
			dir = filepath.Join(root, filepath.FromSlash(m.dir))
			pc.RootModule = filepath.Join(dir, filepath.FromSlash(m.spec.Root))
			pc.Edition, pc.IsMember, pc.IsProcMacro = m.spec.Edition, true, m.spec.Type == rsspec.TypeProcMacro
			version = "0.0.0"
		} else {
			c := byKey[u.Key.Crate]
			if c == nil {
				return nil, fmt.Errorf("%s is not in bob.lock", u.Key.Crate)
			}
			hexNar, err := resolve.HexOfSRI(c.NarHash)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", u.Key.Crate, err)
			}
			if dir, err = drv.FixedOutputPath("sha256", hexNar, true, sourceName(c.Name, c.Version)); err != nil {
				return nil, err
			}
			lib := c.Meta.LibPath
			if lib == "" {
				lib = "src/lib.rs"
			}
			pc.RootModule = path.Join(dir, lib)
			pc.Edition, pc.IsProcMacro = c.Meta.Edition, c.Meta.ProcMacro
			if pc.Edition == "" {
				pc.Edition = "2015"
			}
		}
		pc.Source.IncludeDirs = []string{dir}
		for _, f := range u.Features {
			pc.Cfg = append(pc.Cfg, "feature=\""+f+"\"")
		}
		pc.Env["CARGO_PKG_NAME"], pc.Env["CARGO_PKG_VERSION"], pc.Env["CARGO_MANIFEST_DIR"] = name, version, dir
		pc.Env["CARGO_CRATE_NAME"] = codeName(name)
		seen := map[string]bool{}
		for _, d := range u.Deps {
			if d.Kind == "build" {
				continue
			}
			i, ok := index[keyOf(d.To)]
			if !ok || seen[d.Name] {
				continue
			}
			seen[d.Name] = true
			pc.Deps = append(pc.Deps, projectDep{Crate: i, Name: codeName(d.Name)})
		}
		sort.Slice(pc.Deps, func(i, j int) bool { return pc.Deps[i].Name < pc.Deps[j].Name })
		out.Crates = append(out.Crates, pc)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
