package engine

import (
	"fmt"
	"slices"
	"sort"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// The bob-builder job plumbing the generators share: the input collector, the fixup contract,
// finalising a job into its derivation, and the errors every planner reports. Generators cannot
// import pkg/builder/bspec (R-ARCH-003), so Fixup mirrors bspec.Fixup field for field.

// Inputs collects the input derivations and input sources of one bob-builder job.
type Inputs struct {
	drvs map[string]map[string]bool // drv path → outputs
	srcs map[string]bool
}

// NewInputs returns a collector holding the bob-builder store path as a source.
func NewInputs(builder string) *Inputs {
	return &Inputs{drvs: map[string]map[string]bool{}, srcs: map[string]bool{builder: true}}
}

// Add records output o of rv and returns its store path. An output rv does not have records
// nothing and returns "".
func (in *Inputs) Add(rv Resolved, o string) string {
	sp := rv.Outputs[o]
	if sp == "" {
		return ""
	}
	if rv.Drv == "" {
		in.srcs[sp] = true
		return sp
	}
	in.AddDrv(rv.Drv, o)
	return sp
}

// AddCutoff is Add for an input that may be a codegen output with early cutoff (§03.6.3): the
// job then depends on the content-addressed copy, not on the generating derivation.
func (in *Inputs) AddCutoff(rv Resolved, o string) string {
	if c, ok := rv.Cutoff[o]; ok {
		in.srcs[c] = true
		return c
	}
	return in.Add(rv, o)
}

// AddAll records every output of rv.
func (in *Inputs) AddAll(rv Resolved) {
	for o := range rv.Outputs {
		in.Add(rv, o)
	}
}

// AddDrv records outputs of the derivation at path.
func (in *Inputs) AddDrv(path string, outputs ...string) {
	if in.drvs[path] == nil {
		in.drvs[path] = map[string]bool{}
	}
	for _, o := range outputs {
		in.drvs[path][o] = true
	}
}

// AddSrc records a plain store path.
func (in *Inputs) AddSrc(p string) { in.srcs[p] = true }

// List returns the input derivations (by path, outputs sorted) and the sorted input sources.
func (in *Inputs) List() ([]drv.InputDrv, []string) {
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

// Fixup is the fixup contract of a job (R-NIX-019), bspec.Fixup's fields.
type Fixup struct {
	Strip       bool
	DebugOut    bool
	ShrinkRPath bool
	Forbidden   []string // store paths the output must not reference
	Tools       FixupTools
}

// FixupTools are the store paths of the fixup tools (bspec.FixupTools).
type FixupTools struct {
	Strip, Objcopy, Patchelf string
}

// NoFixup is the contract of a job whose output is not an ELF tree bob controls: no stripping,
// no RUNPATH change, a reference check against nothing.
func NoFixup() *Fixup { return &Fixup{} }

// spec is the bspec.Fixup JSON tree, built directly: a struct would take drv's JSON round
// trip on every job (A315).
func (f *Fixup) spec() map[string]any {
	forbidden := make([]any, len(f.Forbidden))
	for i, p := range f.Forbidden {
		forbidden[i] = p
	}
	return map[string]any{"strip": f.Strip, "debugOut": f.DebugOut, "shrinkRpath": f.ShrinkRPath, "forbidden": forbidden,
		"tools": map[string]any{"strip": f.Tools.Strip, "objcopy": f.Tools.Objcopy, "patchelf": f.Tools.Patchelf}}
}

// BuilderJob is one bob-builder job of a generator, before it is a derivation.
type BuilderJob struct {
	Name    string // store name of the derivation
	System  string
	Builder string // bob-builder store path
	Kind    string // bob-builder subcommand
	Label   string // package label for log lines
	Args    any    // the kind's Args
	Fixup   *Fixup // nil: NoFixup
	Inputs  *Inputs
	Discard bool // unsafeDiscardReferences.out (R-OUT-011)
}

// Finalize turns the job into its finalised derivation and returns the derivation's path. The
// forbidden paths of the fixup are sorted and deduplicated in place; when there are any they
// are also the output's disallowed references.
func (j BuilderJob) Finalize(r drv.Resolver) (string, *drv.Derivation, error) {
	fx := j.Fixup
	if fx == nil {
		fx = NoFixup()
	}
	slices.Sort(fx.Forbidden)
	fx.Forbidden = slices.Compact(fx.Forbidden)
	spec := map[string]any{"v": 1, "kind": j.Kind, "label": j.Label, "outputs": []any{"out"}, "args": j.Args, "fixup": fx.spec()}
	ds, ss := j.Inputs.List()
	job := drv.Job{Name: j.Name, System: j.System, Builder: j.Builder, Spec: spec, InputDrvs: ds, InputSrcs: ss,
		// A71: bob checks its own caches in one batch before Nix runs; Nix must not ask a
		// substituter about every bob output (≈ 0.12 s per derivation).
		ExtraAttribute: map[string]any{"allowSubstitutes": false}}
	if len(fx.Forbidden) > 0 {
		job.Disallowed = map[string][]string{"out": fx.Forbidden}
	}
	if j.Discard {
		job.DiscardRefs = map[string]bool{"out": true}
	}
	d, err := job.Derivation()
	if err != nil {
		return "", nil, err
	}
	if err := d.Finalize(r); err != nil {
		return "", nil, err
	}
	p, err := d.Path()
	if err != nil {
		return "", nil, err
	}
	return p, d, nil
}

// OutPath is the store path of output "out" of d ("" when d has none).
func OutPath(d *drv.Derivation) string {
	for _, o := range d.Outputs {
		if o.Name == "out" {
			return o.Path
		}
	}
	return ""
}

// PickOutput returns the first of prefer that outs has, else its first output by name.
func PickOutput(outs map[string]string, prefer ...string) string {
	for _, o := range prefer {
		if _, ok := outs[o]; ok {
			return o
		}
	}
	names := make([]string, 0, len(outs))
	for o := range outs {
		names = append(names, o)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

// JoinErrors joins the errors of a planner so that the engine still sees each diagnostic
// (Unwrap []error): nil for none, the error itself for one.
func JoinErrors(errs []error) error {
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	}
	return errorList(errs)
}

// Invalid is BOB-E-1020 for node n: an option that is wrong for the files or deps it names.
func Invalid(n *ir.Node, option, reason string) error {
	e := diag.New(1020, "label", n.Label, "option", option, "reason", reason)
	e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
	return e
}

// Internal is an error of node n that is bob's bug (the engine reports it as such).
func Internal(n *ir.Node, format string, a ...any) error {
	return fmt.Errorf("%s: %s", n.Label, fmt.Sprintf(format, a...))
}
