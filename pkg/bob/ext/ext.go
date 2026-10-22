package ext

import (
	"fmt"
	"slices"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/internal/graph"
)

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

var axisNames = [...]string{"platform", "profile", "sanitizers", "Go race setting", "Rust features"}
var axisIdents = [...]string{"AxisPlatform", "AxisProfile", "AxisSanitizers", "AxisGoRace", "AxisFeatures"}

// Register registers a kind. Registering one name twice with the same axes is allowed; an
// invalid name or a conflicting second registration fails the evaluation.
func Register(k Kind) {
	axes := make([]int, len(k.Axes))
	for i, a := range k.Axes {
		axes[i] = int(a)
	}
	graph.Register(graph.Kind{Name: k.Name, Axes: axes, Host: k.Host})
}

// Lookup returns the kind registered under name in this binary: the core kinds of package bob
// ("bob.run", "bob.fetch", …) and every kind registered by a linked API package.
func Lookup(name string) (Kind, bool) {
	gk, ok := graph.Lookup(name)
	if !ok {
		return Kind{}, false
	}
	k := Kind{Name: gk.Name, Host: gk.Host}
	for _, a := range gk.Axes {
		k.Axes = append(k.Axes, Axis(a))
	}
	return k, true
}

// Config is the projected configuration a node sees.
type Config struct{ c graph.Config }

// declares reports whether the kind declared axis a; reading an undeclared axis is a definition
// error ("ext: kind <k> reads the profile but did not declare AxisProfile", R-FW-046) and the
// zero value is returned. The projection is structural: an undeclared axis never reaches the
// node's derivation.
func (c Config) declares(a Axis) bool {
	if slices.Contains(c.c.Axes, int(a)) {
		return true
	}
	if graph.Misuse != nil {
		graph.Misuse(fmt.Sprintf("ext: kind %s reads the %s but did not declare %s", c.c.Kind, axisNames[a], axisIdents[a]))
	}
	return false
}

// Platform returns the node's platform; reading it without AxisPlatform is a definition error.
func (c Config) Platform() bob.Platform {
	if !c.declares(AxisPlatform) {
		return ""
	}
	return bob.Platform(c.c.Platform)
}

// Profile returns the node's profile; reading it without AxisProfile is a definition error.
func (c Config) Profile() bob.Profile {
	if !c.declares(AxisProfile) {
		return ""
	}
	return bob.Profile(c.c.Profile)
}

// Sanitizers returns the node's sanitisers; reading them without AxisSanitizers is a definition
// error.
func (c Config) Sanitizers() []bob.Sanitizer {
	if !c.declares(AxisSanitizers) {
		return nil
	}
	var out []bob.Sanitizer
	for _, s := range c.c.Sanitizers {
		out = append(out, bob.Sanitizer(s))
	}
	return out
}

// Race reports the Go race setting; reading it without AxisGoRace is a definition error.
func (c Config) Race() bool {
	if !c.declares(AxisGoRace) {
		return false
	}
	return c.c.Race
}

// Features returns the Rust features of l; reading them without AxisFeatures is a definition
// error.
func (c Config) Features(l bob.Label) []string {
	if !c.declares(AxisFeatures) {
		return nil
	}
	return slices.Clone(c.c.Features[l.String()])
}

// New creates a node of kind for p. name is the label name; deps are the node's direct
// package dependencies (order is kept); spec is the kind's payload, serialised into the IR.
//
// The label's directory is the directory of the bob.go that made the call (R-FW-015) and its
// definedAt the line of that call (R-FW-016). For kinds named "nixpkgs.<x>" the label is
// "nixpkgs:<name>" and spec is the IR "nix" object. Errors (an unregistered kind, an invalid
// name, a call outside a bob.Define function) fail the evaluation and give a zero Node.
func New(p *bob.P, kind, name string, deps []bob.Package, srcs []bob.Sources, spec any) bob.Node {
	if graph.NewNode == nil {
		return bob.Node{}
	}
	n, _ := graph.NewNode(p, kind, name, deps, srcs, spec).(bob.Node)
	return n
}

// ConfigOf returns the projected configuration of a node.
func ConfigOf(n *bob.Node) Config {
	if graph.ConfigOf == nil {
		return Config{}
	}
	return Config{c: graph.ConfigOf(n)}
}
