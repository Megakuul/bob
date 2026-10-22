package drv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Output is one derivation output. Input-addressed outputs have an empty Algo and Hash; fixed
// outputs have Algo "r:sha256" (recursive) or "sha256" (flat) and the lowercase hex Hash.
type Output struct {
	Name, Path, Algo, Hash string
}

// InputDrv is a dependency on named outputs of another derivation.
type InputDrv struct {
	Path    string
	Outputs []string
}

// Derivation is a Nix derivation as bob computes it (SPEC R-NIX-006). With Structured set, the
// ATerm environment holds `__json` (canonical JSON of Structured) plus one variable per output
// (R-NIX-016).
type Derivation struct {
	Name       string
	System     string
	Builder    string
	Args       []string
	Outputs    []Output
	InputDrvs  []InputDrv
	InputSrcs  []string
	Env        map[string]string
	Structured map[string]any
}

// Resolver returns hashDerivationModulo of an input derivation (parsed nixpkgs drvs are
// resolved recursively by the caller; bob's own drvs by their own Modulo).
type Resolver interface {
	Modulo(drvPath string) ([32]byte, error)
}

// fixed reports whether d is a fixed-output derivation (its outputs carry a hash).
func (d *Derivation) fixed() bool {
	for _, o := range d.Outputs {
		if o.Algo != "" || o.Hash != "" {
			return true
		}
	}
	return false
}

// fixedPath is the output path of a fixed-output derivation (exactly one output, "out").
func (d *Derivation) fixedPath() (string, error) {
	if len(d.Outputs) != 1 || d.Outputs[0].Name != "out" {
		return "", fmt.Errorf("fixed-output derivation %s must have exactly the output \"out\"", d.Name)
	}
	o := d.Outputs[0]
	algo, rec, err := fixedOutput(o.Algo)
	if err != nil {
		return "", fmt.Errorf("derivation %s: %w", d.Name, err)
	}
	p, err := FixedOutputPath(algo, o.Hash, rec, d.Name)
	if err != nil {
		return "", fmt.Errorf("derivation %s: %w", d.Name, err)
	}
	return p, nil
}

// outputName is the store name of output o of a derivation called name.
func outputName(name, o string) string {
	if o == "out" {
		return name
	}
	return name + "-" + o
}

// clone copies d deeply enough that Finalize on the copy leaves d untouched.
func (d *Derivation) clone() *Derivation {
	c := *d
	c.Outputs = append([]Output(nil), d.Outputs...)
	c.Env = make(map[string]string, len(d.Env))
	for k, v := range d.Env {
		c.Env[k] = v
	}
	return &c
}

// Finalize fills the output paths and the matching environment variables of an input-addressed
// derivation (SPEC R-NIX-006 "Output paths"). For a fixed-output derivation it fills the fixed
// path.
func (d *Derivation) Finalize(r Resolver) error {
	if d.Env == nil {
		d.Env = map[string]string{}
	}
	if len(d.Outputs) == 0 {
		return fmt.Errorf("derivation %s has no outputs", d.Name)
	}
	if d.fixed() {
		p, err := d.fixedPath()
		if err != nil {
			return err
		}
		d.Outputs[0].Path = p
		d.Env["out"] = p
		return nil
	}
	for i := range d.Outputs {
		d.Outputs[i].Path = ""
		d.Env[d.Outputs[i].Name] = ""
	}
	h, err := d.moduloOf(r)
	if err != nil {
		return err
	}
	for i := range d.Outputs {
		o := &d.Outputs[i]
		p, err := MakeStorePath("output:"+o.Name, h, outputName(d.Name, o.Name))
		if err != nil {
			return fmt.Errorf("derivation %s output %s: %w", d.Name, o.Name, err)
		}
		o.Path = p
		d.Env[o.Name] = p
	}
	return nil
}

// Modulo is hashDerivationModulo of d. A derivation whose outputs are not filled in yet is
// finalised on a copy first, so the result does not depend on the order callers finalise in.
func (d *Derivation) Modulo(r Resolver) ([32]byte, error) {
	if d.fixed() {
		p, err := d.fixedPath()
		if err != nil {
			return [32]byte{}, err
		}
		o := d.Outputs[0]
		return sha256.Sum256([]byte("fixed:out:" + o.Algo + ":" + o.Hash + ":" + p)), nil
	}
	for _, o := range d.Outputs {
		if o.Path == "" {
			c := d.clone()
			if err := c.Finalize(r); err != nil {
				return [32]byte{}, err
			}
			return c.moduloOf(r)
		}
	}
	return d.moduloOf(r)
}

// moduloOf hashes the ATerm of d as it is, with every input derivation replaced by the hex of
// its own hashDerivationModulo (inputs with equal modulo merge, as in Nix's map).
func (d *Derivation) moduloOf(r Resolver) ([32]byte, error) {
	inputs := make([]InputDrv, 0, len(d.InputDrvs))
	for _, in := range d.InputDrvs {
		if r == nil {
			return [32]byte{}, fmt.Errorf("derivation %s: no resolver for input %s", d.Name, in.Path)
		}
		h, err := r.Modulo(in.Path)
		if err != nil {
			return [32]byte{}, fmt.Errorf("derivation %s: input %s: %w", d.Name, in.Path, err)
		}
		inputs = append(inputs, InputDrv{Path: hex.EncodeToString(h[:]), Outputs: in.Outputs})
	}
	if len(inputs) == 0 {
		inputs = []InputDrv{} // non-nil: aterm must not fall back to d.InputDrvs
	}
	a, err := d.aterm(inputs)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(a), nil
}

// Path is the store path of the .drv file of d.
func (d *Derivation) Path() (string, error) {
	for _, o := range d.Outputs {
		if o.Path == "" {
			return "", fmt.Errorf("derivation %s is not finalised (output %s has no path)", d.Name, o.Name)
		}
	}
	a, err := d.ATerm()
	if err != nil {
		return "", err
	}
	refs := append([]string(nil), d.InputSrcs...)
	for _, in := range d.InputDrvs {
		refs = append(refs, in.Path)
	}
	refs = sortedSet(refs)
	typ := "text"
	if len(refs) > 0 {
		typ += ":" + strings.Join(refs, ":")
	}
	return MakeStorePath(typ, sha256.Sum256(a), d.Name+".drv")
}

// Job describes a non-nixpkgs derivation of bob (SPEC R-NIX-016): bob-builder with args
// [Kind] and structured attributes. The builder's store path is not added to the inputs: the
// caller lists it in InputSrcs (the builder is an imported source path, R-NIX-015).
type Job struct {
	Name           string
	System         string
	Builder        string              // store path of bob-builder (the derivation's builder is Builder/bin/bob-builder)
	Spec           any                 // the bspec.Job, serialised as attribute bobSpec (drv must not import the builder layer)
	Disallowed     map[string][]string // outputChecks.<out>.disallowedReferences
	DiscardRefs    map[string]bool     // unsafeDiscardReferences.<out>
	InputDrvs      []InputDrv
	InputSrcs      []string
	ExtraAttribute map[string]any
}

// reservedAttr reports whether Job.Derivation sets the structured attribute k itself.
func reservedAttr(k string) bool {
	switch k {
	case "bobSpec", "builder", "name", "system", "outputs", "outputChecks", "unsafeDiscardReferences",
		"args", "__structuredAttrs":
		return true
	}
	return false
}

// Derivation builds the structured-attributes derivation of the job.
func (j Job) Derivation() (*Derivation, error) {
	if j.Name == "" || j.System == "" || j.Builder == "" {
		return nil, fmt.Errorf("job %q: name, system and builder are required", j.Name)
	}
	spec, err := normalise(j.Spec)
	if err != nil {
		return nil, fmt.Errorf("job %s: spec: %w", j.Name, err)
	}
	sm, _ := spec.(map[string]any)
	kind, _ := sm["kind"].(string)
	if kind == "" {
		return nil, fmt.Errorf("job %s: spec has no kind", j.Name)
	}
	outs, err := specOutputs(sm["outputs"])
	if err != nil {
		return nil, fmt.Errorf("job %s: %w", j.Name, err)
	}
	declared := map[string]bool{}
	outList := make([]any, len(outs))
	for i, o := range outs {
		if declared[o] {
			return nil, fmt.Errorf("job %s: output %q declared twice", j.Name, o)
		}
		declared[o] = true
		outList[i] = o
	}
	builder := j.Builder + "/bin/bob-builder"
	attrs := map[string]any{
		"bobSpec": spec, "builder": builder, "name": j.Name, "system": j.System, "outputs": outList,
	}
	if checks := map[string]any{}; len(j.Disallowed) > 0 {
		for o, refs := range j.Disallowed {
			if !declared[o] {
				return nil, fmt.Errorf("job %s: disallowed references for undeclared output %q", j.Name, o)
			}
			list := make([]any, len(refs))
			for i, r := range refs {
				list[i] = r
			}
			checks[o] = map[string]any{"disallowedReferences": list}
		}
		attrs["outputChecks"] = checks
	}
	if discard := map[string]any{}; len(j.DiscardRefs) > 0 {
		for o, v := range j.DiscardRefs {
			if !declared[o] {
				return nil, fmt.Errorf("job %s: unsafeDiscardReferences for undeclared output %q", j.Name, o)
			}
			if v {
				discard[o] = true
			}
		}
		if len(discard) > 0 {
			attrs["unsafeDiscardReferences"] = discard
		}
	}
	for k, v := range j.ExtraAttribute {
		if reservedAttr(k) {
			return nil, fmt.Errorf("job %s: extra attribute %q is reserved", j.Name, k)
		}
		nv, err := normalise(v)
		if err != nil {
			return nil, fmt.Errorf("job %s: attribute %s: %w", j.Name, k, err)
		}
		attrs[k] = nv
	}
	// Fail here, not at ATerm time, when an attribute cannot be written canonically.
	if _, err := canonicalJSON(attrs); err != nil {
		return nil, fmt.Errorf("job %s: %w", j.Name, err)
	}
	d := &Derivation{
		Name: j.Name, System: j.System, Builder: builder, Args: []string{kind},
		InputDrvs: append([]InputDrv(nil), j.InputDrvs...), InputSrcs: append([]string(nil), j.InputSrcs...),
		Env: map[string]string{}, Structured: attrs,
	}
	for _, o := range outs {
		d.Outputs = append(d.Outputs, Output{Name: o})
		d.Env[o] = ""
	}
	sort.Slice(d.Outputs, func(a, b int) bool { return d.Outputs[a].Name < d.Outputs[b].Name })
	return d, nil
}

// specOutputs reads the output names of a normalised spec (default ["out"]).
func specOutputs(v any) ([]string, error) {
	if v == nil {
		return []string{"out"}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("spec outputs is not a list")
	}
	if len(list) == 0 {
		return []string{"out"}, nil
	}
	out := make([]string, len(list))
	for i, e := range list {
		s, ok := e.(string)
		if !ok || s == "" || validName(s) != "" {
			return nil, fmt.Errorf("spec output %v is not a valid output name", e)
		}
		out[i] = s
	}
	return out, nil
}
