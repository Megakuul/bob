package ext

import "github.com/megakuul/bob/pkg/bob"

// Kind describes one node kind, e.g. "cc.library". Registered in an init() of the API package.
type Kind struct {
	Name string // "<ecosystem>.<kind>", [a-z0-9.]+
	Axes []Axis // variant axes this kind's derivation depends on
	Host bool   // always built for p.Host() (codegen tools, proc-macros)
}

// Axis is one variant axis a kind may read.
type Axis int

const (
	AxisPlatform Axis = iota
	AxisProfile
	AxisSanitizers
	AxisGoRace
	AxisFeatures
)

// Register registers a kind.
func Register(k Kind) {}

// Config is the projected configuration a node sees.
type Config struct{ axes []Axis }

// Platform returns the node's platform; it panics if AxisPlatform is not declared.
func (c Config) Platform() bob.Platform { return "" }

// Profile returns the node's profile; it panics if AxisProfile is not declared.
func (c Config) Profile() bob.Profile { return "" }

// Sanitizers returns the node's sanitisers; it panics if AxisSanitizers is not declared.
func (c Config) Sanitizers() []bob.Sanitizer { return nil }

// Race reports the Go race setting; it panics if AxisGoRace is not declared.
func (c Config) Race() bool { return false }

// Features returns the Rust features of l; it panics if AxisFeatures is not declared.
func (c Config) Features(l bob.Label) []string { return nil }

// New creates a node of kind for p. name is the label name; deps are the node's direct
// package dependencies (order is kept); spec is the kind's payload, serialised into the IR.
func New(p *bob.P, kind, name string, deps []bob.Package, srcs []bob.Sources, spec any) bob.Node {
	return bob.Node{}
}

// ConfigOf returns the projected configuration of a node.
func ConfigOf(n *bob.Node) Config { return Config{} }
