// Package gen implements engine.Ecosystem for pkg/output/appimage: one bob-builder "appimage"
// derivation per bundle (SPEC R-OUT-006, R-OUT-015), with the bob AppImage runtime embedded in
// bob (runtime/, SPEC O-04-2) as a source input.
package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/output/appimage/internal/aispec"

	// The AppImage API registers the appimage.bundle kind the IR names.
	_ "github.com/megakuul/bob/pkg/output/appimage"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem plans AppImages. It has no bob.lock section.
type Ecosystem struct{}

// Name is the kind prefix.
func (Ecosystem) Name() string { return "appimage" }

// Lock is never called (no lock section).
func (Ecosystem) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) { return nil, nil }

// Native writes no files.
func (Ecosystem) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

// Plan returns one derivation per bundle.
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("appimage planner: internal error: %v", v)
		}
	}()
	if req.Graph == nil || req.Store == nil {
		return res, fmt.Errorf("appimage planner: no graph or store")
	}
	res.Nodes = map[string]engine.Resolved{}
	runtimes := map[string]string{}
	lic := lookupLicences(ctx, req)
	for _, n := range req.Nodes {
		if n.Kind != aispec.KindBundle {
			return res, fmt.Errorf("appimage planner: unknown kind %s", n.Kind)
		}
		d, err := planBundle(ctx, req, n, runtimes, lic.table(n))
		if err != nil {
			return res, fmt.Errorf("%s: %w", n.Label, err)
		}
		p, err := d.Path()
		if err != nil {
			return res, err
		}
		res.Drvs = append(res.Drvs, d)
		out := ""
		for _, o := range d.Outputs {
			if o.Name == "out" {
				out = o.Path
			}
		}
		res.Nodes[n.ID] = engine.Resolved{Drv: p, Outputs: map[string]string{"out": out}}
	}
	return res, nil
}

// arch is the AppImage architecture name of a platform.
func arch(p bob.Platform) (string, error) {
	switch p {
	case bob.LinuxAMD64:
		return "x86_64", nil
	case bob.LinuxARM64:
		return "aarch64", nil
	}
	return "", fmt.Errorf("no AppImage runtime for platform %q", p)
}

// inputs collects a derivation's input derivations and sources.
type inputs struct {
	drvs map[string]map[string]bool
	srcs map[string]bool
}

// add records output o of rv and returns its store path.
func (in *inputs) add(rv engine.Resolved, o string) (string, error) {
	sp := rv.Outputs[o]
	if sp == "" {
		return "", fmt.Errorf("input has no output %q", o)
	}
	if rv.Drv == "" {
		in.srcs[sp] = true
		return sp, nil
	}
	if in.drvs[rv.Drv] == nil {
		in.drvs[rv.Drv] = map[string]bool{}
	}
	in.drvs[rv.Drv][o] = true
	return sp, nil
}

func (in *inputs) list() ([]drv.InputDrv, []string) {
	var ds []drv.InputDrv
	for p, outs := range in.drvs {
		d := drv.InputDrv{Path: p}
		for o := range outs {
			d.Outputs = append(d.Outputs, o)
		}
		sort.Strings(d.Outputs)
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].Path < ds[j].Path })
	srcs := make([]string, 0, len(in.srcs))
	for s := range in.srcs {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	return ds, srcs
}

// args mirrors bop.Args (pkg/output/appimage/bop; generators cannot import the builder layer).
type args struct {
	Name         string                 `json:"name"`
	Arch         string                 `json:"arch"`
	Runtime      string                 `json:"runtime"`
	Mksquashfs   string                 `json:"mksquashfs"`
	Entry        string                 `json:"entry"`
	Entries      map[string]string      `json:"entries"`
	Closure      string                 `json:"closure"`
	CACert       string                 `json:"cacert"`
	Env          map[string]string      `json:"env"`
	NoIconv      bool                   `json:"noIconv"`
	Locales      []string               `json:"locales"`
	Localedef    string                 `json:"localedef"`
	I18n         string                 `json:"i18n"`
	HostGraphics bool                   `json:"hostGraphics"`
	Mode         string                 `json:"mode"`
	Prune        []string               `json:"prune"`
	Desktop      desktop                `json:"desktop"`
	Licences     map[string]licenceInfo `json:"licences"`
}

type desktop struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Terminal   bool     `json:"terminal"`
	Icon       string   `json:"icon"`
}

// licenceInfo mirrors bspec.LicenceInfo.
type licenceInfo struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Licenses []string `json:"licenses"`
}

const closureAttr = "closure"

func planBundle(ctx context.Context, req engine.PlanRequest, n *ir.Node, runtimes map[string]string, licences map[string]licenceInfo) (*drv.Derivation, error) {
	var s aispec.Bundle
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return nil, fmt.Errorf("bundle spec: %w", err)
	}
	dep := func(i int) (engine.Resolved, error) {
		if i < 0 || i >= len(n.Deps) {
			return engine.Resolved{}, fmt.Errorf("dependency index %d out of range", i)
		}
		rv, ok := req.Inputs[n.Deps[i]]
		if !ok {
			return engine.Resolved{}, fmt.Errorf("dependency %s was not planned", n.Deps[i])
		}
		return rv, nil
	}
	a, err := arch(bob.Platform(s.Platform))
	if err != nil {
		return nil, err
	}
	in := &inputs{drvs: map[string]map[string]bool{}, srcs: map[string]bool{req.Builder: true}}
	x := args{Name: s.Name, Arch: a, Entries: map[string]string{}, Closure: closureAttr, Env: s.Env,
		NoIconv: s.NoIconv, Locales: s.Locales, HostGraphics: s.HostGraphics, Mode: s.Mode, Prune: s.Prune,
		Desktop:  desktop{Name: s.Desktop.Name, Categories: s.Desktop.Categories, Terminal: s.Desktop.Terminal},
		Licences: map[string]licenceInfo{}}
	var roots []string
	for i, e := range s.Entries {
		rv, err := dep(e.Dep)
		if err != nil {
			return nil, err
		}
		// nixpkgs keeps the programs of a multi-output package in its "bin" output
		o := "out"
		if _, ok := rv.Outputs["bin"]; ok {
			o = "bin"
		}
		out, err := in.add(rv, o)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", e.Name, err)
		}
		roots = append(roots, out)
		if i == 0 {
			x.Entry = out + "/" + e.Main
		} else {
			x.Entries[e.Name] = out + "/" + e.Main
		}
	}
	if x.Entry == "" {
		return nil, fmt.Errorf("the bundle has no program")
	}
	rv, err := dep(s.Squashfs)
	if err != nil {
		return nil, err
	}
	sq, err := in.add(rv, "out")
	if err != nil {
		return nil, err
	}
	x.Mksquashfs = sq + "/bin/mksquashfs"
	if s.CACert >= 0 {
		rv, err := dep(s.CACert)
		if err != nil {
			return nil, err
		}
		if x.CACert, err = in.add(rv, "out"); err != nil {
			return nil, err
		}
		roots = append(roots, x.CACert)
	}
	if len(s.Locales) > 0 {
		rv, err := dep(s.Glibc)
		if err != nil {
			return nil, err
		}
		bin, err := in.add(rv, "bin")
		if err != nil {
			return nil, err
		}
		out, err := in.add(rv, "out")
		if err != nil {
			return nil, err
		}
		x.Localedef, x.I18n = bin+"/bin/localedef", out+"/share/i18n"
	}
	if s.Desktop.Icon {
		src := req.Sources[n.ID]
		if src == "" {
			return nil, fmt.Errorf("the desktop icon was not imported")
		}
		icon, err := findIcon(req.Store.HostPath(src))
		if err != nil {
			return nil, err
		}
		in.srcs[src] = true
		x.Desktop.Icon = src + "/" + icon
	}
	rt, ok := runtimes[a]
	if !ok {
		if rt, err = importRuntime(ctx, req.Store, a); err != nil {
			return nil, err
		}
		runtimes[a] = rt
	}
	in.srcs[rt] = true
	x.Runtime = rt + "/bin/bobrun"
	for p, l := range licences {
		x.Licences[p] = l
	}
	sort.Strings(roots)
	ds, ss := in.list()
	spec := map[string]any{"v": 1, "kind": "appimage", "label": n.Label, "outputs": []any{"out"}, "args": x,
		"fixup": map[string]any{"strip": false, "debugOut": false, "shrinkRpath": false, "forbidden": []any{},
			"tools": map[string]any{"strip": "", "objcopy": "", "patchelf": ""}}}
	name := []string{"appimage", s.Name}
	if bob.Platform(s.Platform) != engine.HostPlatform() {
		name = append(name, s.Platform)
	}
	j := drv.Job{Name: drv.Name(name...), System: engine.HostSystem(), Builder: req.Builder, Spec: spec,
		InputDrvs: ds, InputSrcs: ss, DiscardRefs: map[string]bool{"out": true},
		ExtraAttribute: map[string]any{
			"allowSubstitutes":      false,
			"exportReferencesGraph": map[string]any{closureAttr: roots},
		}}
	d, err := j.Derivation()
	if err != nil {
		return nil, err
	}
	if err := d.Finalize(req.Resolver); err != nil {
		return nil, err
	}
	return d, nil
}

// findIcon returns the icon file (SVG or PNG) of an imported source tree, relative to it.
func findIcon(root string) (string, error) {
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ext := strings.ToLower(path.Ext(d.Name())); d.Type().IsRegular() && (ext == ".svg" || ext == ".png") {
			rel, _ := filepath.Rel(root, p)
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", fmt.Errorf("Desktop.Icon must select exactly one .svg or .png file (found %d)", len(found))
	}
	return found[0], nil
}

// licenceAttrs are looked up besides the bundle's own nixpkgs packages: the libraries every
// toolchain puts into a closure, CA certificates and time zones.
var licenceAttrs = []string{"glibc", "gcc-unwrapped", "cacert", "tzdata"}

// licences holds the nixpkgs facts a plan needs for licences.json (R-OUT-008).
type licences struct {
	attrs   map[string][]string // node id → nixpkgs attributes of its dependency closure (host set)
	entries map[string]nixidx.Entry
}

// lookupLicences finds the nixpkgs attributes each bundle depends on (plus licenceAttrs) and
// looks them all up in one query. Licence data is best effort: what is not known is listed by
// bob-builder with the store name and "unknown".
func lookupLicences(ctx context.Context, req engine.PlanRequest) licences {
	l := licences{attrs: map[string][]string{}, entries: map[string]nixidx.Entry{}}
	byID := map[string]*ir.Node{}
	for _, n := range req.Graph.Nodes {
		byID[n.ID] = n
	}
	host := engine.HostSystem()
	all := map[string]bool{}
	for _, root := range req.Nodes {
		attrs := map[string]bool{}
		for _, a := range licenceAttrs {
			attrs[a] = true
		}
		seen := map[string]bool{}
		var walk func(id string)
		walk = func(id string) {
			n := byID[id]
			if seen[id] || n == nil {
				return
			}
			seen[id] = true
			if n.Nix != nil {
				if sys, attr, err := engine.NixpkgsSource(n); err == nil && sys == host {
					attrs[attr] = true
				}
			}
			for _, d := range n.Deps {
				walk(d)
			}
		}
		walk(root.ID)
		for a := range attrs {
			l.attrs[root.ID] = append(l.attrs[root.ID], a)
			all[a] = true
		}
		sort.Strings(l.attrs[root.ID])
	}
	if req.Nixpkgs == nil {
		return l
	}
	list := make([]string, 0, len(all))
	for a := range all {
		list = append(list, a)
	}
	sort.Strings(list)
	if entries, _ := req.Nixpkgs.Lookup(ctx, list); entries != nil {
		l.entries = entries
	}
	return l
}

// table maps every output path of the nixpkgs packages bundle n depends on to its pname,
// version and licences.
func (l licences) table(n *ir.Node) map[string]licenceInfo {
	out := map[string]licenceInfo{}
	for _, a := range l.attrs[n.ID] {
		e, ok := l.entries[a]
		if !ok || e.Missing || len(e.Licenses) == 0 {
			continue
		}
		for _, p := range e.Outputs {
			out[p] = licenceInfo{Name: e.Pname, Version: e.Version, Licenses: e.Licenses}
		}
	}
	return out
}
