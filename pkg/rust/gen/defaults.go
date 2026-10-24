package gen

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/rust/resolve"
)

// NativeDefault maps a -sys crate to the nixpkgs packages its build script needs (SPEC §03.3.6).
// It applies to every unit of the crate whose version is in Range when no rust.Uses of the graph
// names the crate. Admission (§03.0.6): every entry has a Why and a Test, a crate of bob's CI
// corpus that builds and runs with it.
type NativeDefault struct {
	Crate string   // exact crate name
	Range string   // semver range (Cargo requirement syntax)
	Libs  []string // nixpkgs attributes (target set)
	Why   string
	Test  string // crate of bob's CI corpus that must build and run with this entry
}

// nativeDefaults is the shipped table.
var nativeDefaults = []NativeDefault{
	{Crate: "openssl-sys", Range: ">=0.9", Libs: []string{"openssl"},
		Why:  "openssl-sys finds OpenSSL with pkg-config (or OPENSSL_DIR); without it the build script fails (S3 §3.3)",
		Test: "openssl"},
}

// nixLookup is the part of the nixpkgs index the planner uses (*nixidx.Index).
type nixLookup interface {
	Lookup(ctx context.Context, attrs []string) (map[string]nixidx.Entry, error)
}

// pkgConfigAttr is the pkg-config tool (host set) a default's build script gets.
const pkgConfigAttr = "pkg-config"

// nativeDefault returns the table entry of crate name@version, or nil.
func nativeDefault(name, version string) *NativeDefault {
	v, err := resolve.ParseVersion(version)
	if err != nil {
		return nil
	}
	for i := range nativeDefaults {
		d := &nativeDefaults[i]
		if d.Crate != name {
			continue
		}
		if r, err := resolve.ParseReq(d.Range); err == nil && r.Matches(v) {
			return d
		}
	}
	return nil
}

// usesNames reports whether a rust.Uses of the graph names crate.
func (pl *planner) usesNames(crate string) bool {
	for _, name := range pl.ws.names {
		for _, u := range pl.ws.members[name].spec.Uses {
			if u.Crate == crate {
				return true
			}
		}
	}
	return false
}

// defaultNatives resolves the packages of a native default for a unit built for target (nil
// natively): the target's own set is not reachable from the host's nixpkgs index at plan time,
// so a cross unit takes the attribute from the host's pkgsCross set (R-FW-036's cc.CrossLibs
// source), the native set otherwise. The second result is pkg-config (host).
func (pl *planner) defaultNatives(ctx context.Context, d *NativeDefault, target bob.Platform, label string) ([]engine.Resolved, engine.Resolved, error) {
	if pl.nix == nil {
		return nil, engine.Resolved{}, fmt.Errorf("%s: no nixpkgs index for the native default of %s", label, d.Crate)
	}
	attrs := []string{pkgConfigAttr}
	for _, a := range d.Libs {
		if target != "" {
			a = "pkgsCross." + target.CrossSet() + "." + a
		}
		attrs = append(attrs, a)
	}
	es, err := pl.nix.Lookup(ctx, attrs)
	if err != nil {
		return nil, engine.Resolved{}, err
	}
	var libs []engine.Resolved
	var pc engine.Resolved
	for _, a := range attrs {
		e, ok := es[a]
		switch {
		case !ok || e.Missing || e.DrvPath == "":
			return nil, engine.Resolved{}, diag.New(1001, "attr", a, "suggest", "rust.Uses(\""+d.Crate+"\", nixpkgs.Lib(p, \"<attr>\"))")
		case e.Broken:
			return nil, engine.Resolved{}, diag.New(1002, "attr", a, "version", e.Version)
		case e.Insecure:
			return nil, engine.Resolved{}, diag.New(1003, "attr", a, "version", e.Version, "cves", strings.Join(e.KnownVulnerabilities, ", "))
		}
		rv := engine.Resolved{Drv: e.DrvPath, Outputs: e.Outputs}
		if a == pkgConfigAttr {
			pc = rv
		} else {
			libs = append(libs, rv)
		}
	}
	return libs, pc, nil
}

// addNative puts a native package's bin/ on PATH and its pkg-config directories on pcs.
func addNative(in *engine.Inputs, rv engine.Resolved, r *buildRunJSON, pcs []string) []string {
	outs := make([]string, 0, len(rv.Outputs))
	for o := range rv.Outputs {
		outs = append(outs, o)
	}
	sort.Strings(outs)
	for _, o := range outs {
		if o != "out" && o != "dev" && o != "lib" && o != "bin" {
			continue
		}
		p := in.Add(rv, o)
		r.Path = append(r.Path, p+"/bin")
		pcs = append(pcs, p+"/lib/pkgconfig", p+"/share/pkgconfig")
	}
	return pcs
}
