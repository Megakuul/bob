package drv

import (
	"github.com/megakuul/bob/pkg/bob/diag"
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

// Finalize fills the output paths and the matching environment variables of an input-addressed
// derivation (SPEC R-NIX-006 "Output paths").
func (d *Derivation) Finalize(r Resolver) error { return diag.NotImplemented("drv.Finalize") }

// Modulo is hashDerivationModulo of d.
func (d *Derivation) Modulo(r Resolver) ([32]byte, error) {
	return [32]byte{}, diag.NotImplemented("drv.Modulo")
}

// ATerm returns the canonical ATerm of d (sorted, escaped; output paths as set).
func (d *Derivation) ATerm() ([]byte, error) { return nil, diag.NotImplemented("drv.ATerm") }

// Path is the store path of the .drv file of d.
func (d *Derivation) Path() (string, error) { return "", diag.NotImplemented("drv.Path") }

// Parse parses an ATerm derivation (nixpkgs drvs are parsed recursively by callers).
func Parse(aterm []byte) (*Derivation, error) { return nil, diag.NotImplemented("drv.Parse") }

// MakeStorePath is makeStorePath(type, h, name) of SPEC R-NIX-006.
func MakeStorePath(typ string, h [32]byte, name string) (string, error) {
	return "", diag.NotImplemented("drv.MakeStorePath")
}

// FixedOutputPath is the store path of a fixed-output result: flat files (recursive false) or
// NAR trees (recursive true) with the given algorithm and lowercase hex hash.
func FixedOutputPath(algo, hexHash string, recursive bool, name string) (string, error) {
	return "", diag.NotImplemented("drv.FixedOutputPath")
}

// Name joins parts into a store name (SPEC R-NIX-006 "Store names").
func Name(parts ...string) string { return "" }

// Job describes a non-nixpkgs derivation of bob (SPEC R-NIX-016): bob-builder with args
// [Kind] and structured attributes.
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

// Derivation builds the structured-attributes derivation of the job.
func (j Job) Derivation() (*Derivation, error) { return nil, diag.NotImplemented("drv.Job") }
