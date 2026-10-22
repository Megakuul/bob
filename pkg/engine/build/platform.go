package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/build/testrun"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/nixidx"
	"github.com/megakuul/bob/pkg/engine/store"
)

// Platforms in the build (SPEC §04.1-§04.2): nixpkgs packages come from the package set of the
// system their node selects (R-FW-032, R-FW-036), so a cross build asks the index of the target
// system too; every derivation bob generates keeps the host system (R-PLAT-004).

// indexes are the nixpkgs indexes of one run: the host system's and, opened on demand, those
// of cross targets.
type indexes struct {
	st     *store.Store
	src    string // nixpkgs source path
	rev    string
	host   *nixidx.Index
	others map[string]*nixidx.Index // Nix system → index
}

// get returns the index of system.
func (ix *indexes) get(ctx context.Context, system string) (*nixidx.Index, error) {
	if system == store.HostSystem() {
		return ix.host, nil
	}
	if i, ok := ix.others[system]; ok {
		return i, nil
	}
	i, err := nixidx.Open(ctx, ix.st, nixidx.Config{Source: ix.src, Rev: ix.rev, System: system})
	if err != nil {
		return nil, err
	}
	if ix.others == nil {
		ix.others = map[string]*nixidx.Index{}
	}
	ix.others[system] = i
	return i, nil
}

// entryKey is the key of an index entry in the planner's entries: the attribute for the host
// set (what bob dev and the core planners look up), "<system>:<attr>" for another system's set.
func entryKey(system, attr string) string {
	if system == store.HostSystem() {
		return attr
	}
	return system + ":" + attr
}

// check runs the R-NIX-011 pre-checks of every nixpkgs node against the index of the set it
// comes from and returns the entries by entryKey.
func (ix *indexes) check(ctx context.Context, g *ir.Graph) (map[string]nixidx.Entry, error) {
	needs := map[string][]nixidx.Need{}
	for _, n := range g.Nodes {
		if n.Nix == nil {
			continue
		}
		sys, attr, err := engine.NixpkgsSource(n)
		if err != nil {
			return nil, internalf(n.Label, "%v", err)
		}
		needs[sys] = append(needs[sys], nixidx.Need{Attr: attr, Label: n.Label,
			DefinedAt:   diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line},
			AllowUnfree: n.Nix.Allow.Unfree, AllowInsecure: n.Nix.Allow.Insecure})
	}
	entries := map[string]nixidx.Entry{}
	systems := make([]string, 0, len(needs))
	for s := range needs {
		systems = append(systems, s)
	}
	sort.Strings(systems)
	var errs []error
	for _, sys := range systems {
		idx, err := ix.get(ctx, sys)
		if err != nil {
			return entries, err
		}
		es, err := idx.Check(ctx, needs[sys])
		for attr, e := range es {
			entries[entryKey(sys, attr)] = e
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return entries, errors.Join(errs...)
}

// crossTargets returns the platforms other than the host's that nodes of g are built for.
func crossTargets(g *ir.Graph) []bob.Platform {
	seen := map[bob.Platform]bool{}
	var out []bob.Platform
	for _, n := range g.Nodes {
		p, err := engine.NodePlatform(n)
		if err != nil || p == engine.HostPlatform() || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// precheck is R-PLAT-007: before any derivation of a cross target is generated, the glibc of
// the target system's own set (when target libraries come from it) must equal the cross
// toolchain's, and the target system's rustc the host's (when Rust code is built for it).
func (ix *indexes) precheck(ctx context.Context, g *ir.Graph) error {
	targets := crossTargets(g)
	if len(targets) == 0 {
		return nil
	}
	testNative, testCross, useTest, err := engine.TestVersions()
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range targets {
		libs, rust := false, false
		for _, n := range g.Nodes {
			p, err := engine.NodePlatform(n)
			if err != nil || p != t {
				continue
			}
			libs = libs || n.Nix != nil && n.Nix.Set == "target"
			rust = rust || strings.HasPrefix(n.Kind, "rust.")
		}
		if !libs && !rust {
			continue
		}
		native, cross := testNative, testCross
		if !useTest {
			if native, cross, err = ix.versions(ctx, t, libs, rust); err != nil {
				return err
			}
		}
		if err := engine.CheckVersions(t, native, cross, libs, rust); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// versions looks up the facts of the pre-check in the indexes: native = the target system's
// own glibc and rustc, cross = the cross toolchain's glibc and the host's rustc.
func (ix *indexes) versions(ctx context.Context, t bob.Platform, libs, rust bool) (native, cross engine.Versions, err error) {
	tidx, err := ix.get(ctx, t.NixSystem())
	if err != nil {
		return native, cross, err
	}
	var tattrs, hattrs []string
	crossLibc := bob.ToolchainFor(engine.HostPlatform(), t).CC + ".libc"
	if libs {
		tattrs, hattrs = append(tattrs, "glibc"), append(hattrs, crossLibc)
	}
	if rust {
		tattrs, hattrs = append(tattrs, "rustc"), append(hattrs, "rustc")
	}
	te, err := tidx.Lookup(ctx, tattrs)
	if err != nil {
		return native, cross, err
	}
	he, err := ix.host.Lookup(ctx, hattrs)
	if err != nil {
		return native, cross, err
	}
	native.Glibc, cross.Glibc = te["glibc"].Version, he[crossLibc].Version
	if rust {
		native.Rustc, cross.Rustc = te["rustc"].Version, he["rustc"].Version
	}
	return native, cross, nil
}

// noteUncached prints, before anything is built, the target libraries of cross builds that the
// binary caches do not have (R-FW-036): they will be built from source. Best effort: an
// unreachable cache only loses the note.
func (r *runner) noteUncached(ctx context.Context, ix *indexes, g *ir.Graph, entries map[string]nixidx.Entry) {
	seen := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Nix == nil || n.Nix.Set == "host" {
			continue
		}
		p, err := engine.NodePlatform(n)
		if err != nil || p == engine.HostPlatform() {
			continue
		}
		sys, attr, err := engine.NixpkgsSource(n)
		if err != nil || seen[sys+":"+attr+"@"+string(p)] {
			continue
		}
		seen[sys+":"+attr+"@"+string(p)] = true
		e, ok := entries[entryKey(sys, attr)]
		if !ok || len(e.Outputs) == 0 {
			continue
		}
		idx, err := ix.get(ctx, sys)
		if err != nil {
			continue
		}
		out := e.Outputs[pickOutput(e.Outputs, "out")]
		if cached, err := idx.Cached(ctx, out); err != nil || cached {
			continue
		}
		r.emit(Event{Event: "note", Note: &Note{Code: "uncached-target-lib", Attr: n.Nix.Attr, Platform: string(p),
			Text: fmt.Sprintf("%s for %s is not in the binary cache and will be built from source (several minutes)", n.Nix.Attr, p)}})
	}
}

// Note is a "note:" line of a run (SPEC R-FW-036, R-FW-038): never an error.
type Note struct {
	Code     string // "uncached-target-lib", "variant-dropped"
	Text     string // the human text after "note: "
	Label    string // variant-dropped: the package
	Attr     string // uncached-target-lib: the nixpkgs attribute
	Platform string // uncached-target-lib: the target platform
	Field    string // variant-dropped: the dropped variant field ("asan")
}

// ecosystemNames are the names of ecosystems in notes.
var ecosystemNames = map[string]string{"cc": "C/C++", "go": "Go", "rust": "Rust", "npm": "npm", "ocaml": "OCaml"}

// noteDropped prints a note for each requested package that is built without a requested
// variant field (R-FW-038: its projection drops the field; a note, never an error). A package
// is built with a field when its projection holds it, its own or through a dependency.
func (r *runner) noteDropped(g *ir.Graph) {
	byID := nodesByID(g)
	for _, id := range g.Roots {
		n := byID[id]
		if n == nil || isCoreKind(n.Kind) {
			continue
		}
		p, _ := engine.NodePlatform(n)
		_, hasSan := n.Proj["sanitizers"]
		_, hasRace := n.Proj["race"]
		var dropped []string
		for _, c := range g.Configs {
			if c.Platform != "" && bob.Platform(c.Platform) != p {
				continue
			}
			if !hasSan {
				dropped = append(dropped, c.Sanitizers...)
			}
			if c.Race && !hasRace {
				dropped = append(dropped, "race")
			}
		}
		sort.Strings(dropped)
		for i, f := range dropped {
			if i > 0 && dropped[i-1] == f {
				continue
			}
			eco, _, _ := strings.Cut(n.Kind, ".")
			name := ecosystemNames[eco]
			if name == "" {
				name = eco
			}
			r.emit(Event{Event: "note", Label: n.Label, Note: &Note{Code: "variant-dropped", Label: n.Label, Field: f,
				Text: fmt.Sprintf("%s is built without %s (not supported for %s in v1)", n.Label, f, name)}})
		}
	}
}

// crossRefusals returns the errors of nodes of native-only ecosystems requested for another
// platform (R-PLAT-005), before anything is planned.
func crossRefusals(g *ir.Graph) error {
	var errs []error
	seen := map[string]bool{}
	for _, n := range g.Nodes {
		if err := engine.CrossRefused(n); err != nil && !seen[n.Label] {
			seen[n.Label] = true
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// unitRunner returns the emulator a test unit runs its program under ("qemu-x86_64"), or ""
// when it runs natively (R-FW-039: the test.result event names it).
func unitRunner(u testrun.Unit) string {
	if u.Drv == nil {
		return ""
	}
	spec, _ := u.Drv.Structured["bobSpec"].(map[string]any)
	b, err := json.Marshal(spec["args"])
	if err != nil {
		return ""
	}
	var a struct{ Argv []string }
	if json.Unmarshal(b, &a) != nil || len(a.Argv) == 0 {
		return ""
	}
	if base := path.Base(a.Argv[0]); strings.HasPrefix(base, "qemu-") {
		return base
	}
	return ""
}

// skipReason is why a test unit cannot run on this machine, or "": a sanitised or race test
// for another platform needs a native machine of that platform (R-FW-039; sanitiser runtimes
// do not run under qemu-user, S7 §3.2).
func skipReason(g *ir.Graph, p *planner, u testrun.Unit) string {
	n := nodesByID(g)[p.drvNode[u.DrvPath]]
	if n == nil {
		return ""
	}
	plat, err := engine.NodePlatform(n)
	if err != nil || plat == engine.HostPlatform() {
		return ""
	}
	var sans []string
	race := false
	if raw, ok := n.Proj["sanitizers"]; ok {
		_ = json.Unmarshal(raw, &sans)
	}
	if raw, ok := n.Proj["race"]; ok {
		_ = json.Unmarshal(raw, &race)
	}
	if len(sans) == 0 && !race {
		return ""
	}
	return "not run: needs a native " + string(plat) + " machine"
}
