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
	// Unit names the ecosystem ("cc", "rust", "go", …) when a node of this kind is a build unit:
	// a package (directory with its own bob.go) defines at most one unit per ecosystem
	// (R-FW-071, BOB-E-1029). Tests, recipes, steps and outputs leave it empty.
	Unit string
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
	graph.Register(graph.Kind{Name: k.Name, Axes: axes, Host: k.Host, Unit: k.Unit})
}

// Lookup returns the kind registered under name in this binary: the core kinds of package bob
// ("bob.run", "bob.fetch", …) and every kind registered by a linked API package.
func Lookup(name string) (Kind, bool) {
	gk, ok := graph.Lookup(name)
	if !ok {
		return Kind{}, false
	}
	k := Kind{Name: gk.Name, Host: gk.Host, Unit: gk.Unit}
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

// Site is a bob.go call position captured by Here, for NewAt.
type Site struct{ s any }

// Here returns the bob.go position of the call that is running now (the first stack frame in
// a bob.go file). Codegen recipes call it in their constructor (connect.Go, graphql.Gqlgen) so
// the package the consumer creates from them later is defined at the recipe's line (R-FW-016).
func Here() Site {
	if graph.Site == nil {
		return Site{}
	}
	return Site{s: graph.Site()}
}

// NewAt is New with the definedAt position of at (from Here) instead of the line of the call
// that creates the node; a zero Site, or one of another module, keeps New's position.
func NewAt(p *bob.P, at Site, kind, name string, deps []bob.Package, srcs []bob.Sources, spec any) bob.Node {
	if graph.NewNodeAt == nil {
		return bob.Node{}
	}
	n, _ := graph.NewNodeAt(p, at.s, kind, name, deps, srcs, spec).(bob.Node)
	return n
}

// NewIn is New for a package whose label directory is dir (relative to the repository or, in a
// dependency repo, to its module root; "" is the root) instead of the directory of the calling
// bob.go: packages an ecosystem places below the declaring definition, such as a Go binary
// //<module dir>/<dir>:<name> declared in the module's bob.go (SPEC §03.2.1). An invalid dir
// is a definition error and gives a zero Node.
func NewIn(p *bob.P, kind, dir, name string, deps []bob.Package, srcs []bob.Sources, spec any) bob.Node {
	if graph.NewNodeIn == nil {
		return bob.Node{}
	}
	n, _ := graph.NewNodeIn(p, kind, dir, name, deps, srcs, spec).(bob.Node)
	return n
}

// Restrict limits the platforms n may be built for, from a kind's `Platforms []bob.Platform`
// option (SPEC R-FW-030, R-PLAT-006); an empty list allows every platform. Requesting n,
// directly or as a dependency, for another platform fails the evaluation with BOB-E-0006
// "<label> does not support <platform> (it allows: <platforms>)". It returns n.
//
//	return &Library{Node: ext.Restrict(ext.New(p, "cc.library", name, deps, srcs, spec), opts.Platforms)}
func Restrict(n bob.Node, platforms []bob.Platform) bob.Node {
	if graph.Restrict != nil && len(platforms) > 0 {
		graph.Restrict(n, platforms)
	}
	return n
}

// ConfigOf returns the projected configuration of a node.
func ConfigOf(n *bob.Node) Config {
	if graph.ConfigOf == nil {
		return Config{}
	}
	return Config{c: graph.ConfigOf(n)}
}

// Errorf records a definition error of the running constructor, for example an invalid option
// of an ecosystem constructor. Evaluation goes on; it fails at the end with every error. Start
// the message with the position it concerns ("api/bob.go:12: …", from Node.DefinedAt).
func Errorf(format string, a ...any) {
	if graph.Misuse != nil {
		graph.Misuse(fmt.Sprintf(format, a...))
	}
}

// CallerDir returns the label directory of the bob.go that is calling the constructor ("" for
// the repository root): the directory New gives a node created now. Constructors use it for
// defaults such as "the label name is the directory name". ok is false outside a bob.go.
func CallerDir() (dir string, ok bool) {
	if graph.CallerDir == nil {
		return "", false
	}
	return graph.CallerDir()
}

// Elsewhere evaluates the constructor that is running now (the innermost bob.Define function)
// once more for platform pl, with p's variant, and returns the configuration it ran in. The
// run is "nested": its configuration is never a target, so the packages it creates become part
// of the graph only through a node of the outer run that depends on them. Output constructors
// use it for their programs on other platforms (oci.ImageOpts.Platforms,
// release.ReleaseOpts.Platforms): called in the outer run, the constructor runs again for each
// other platform; in the nested run (Nested reports true) it records the packages it got with
// Record instead of creating its node, and the outer run reads them with Recorded under the
// same key. For p's own platform Elsewhere returns p without running anything. An unknown
// platform is a definition error and gives nil.
func Elsewhere(p *bob.P, pl bob.Platform) *bob.P {
	if graph.Elsewhere == nil {
		return nil
	}
	q, _ := graph.Elsewhere(p, string(pl)).(*bob.P)
	return q
}

// Nested reports whether p is a configuration made by Elsewhere.
func Nested(p *bob.P) bool { return graph.Nested != nil && graph.Nested(p) }

// Record stores v under key in configuration p (see Elsewhere). Keys are chosen by the kind,
// e.g. "oci.image:" + label.
func Record(p *bob.P, key string, v any) {
	if graph.Record != nil {
		graph.Record(p, key, v)
	}
}

// Recorded returns the value stored under key in configuration p.
func Recorded(p *bob.P, key string) (any, bool) {
	if graph.Recorded == nil {
		return nil, false
	}
	return graph.Recorded(p, key)
}
