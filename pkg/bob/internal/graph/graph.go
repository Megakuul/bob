// Package graph holds what pkg/bob and pkg/bob/ext share without importing each other: the
// registry of node kinds and the hooks through which ext creates and inspects nodes. Package bob
// implements the hooks in its init; ext imports bob, so bob cannot import ext.
package graph

import (
	"fmt"
	"regexp"
	"slices"
	"sync"
)

// Axis values mirror ext.Axis.
const (
	AxisPlatform = iota
	AxisProfile
	AxisSanitizers
	AxisGoRace
	AxisFeatures
	NumAxes
)

// Kind is one registered node kind.
type Kind struct {
	Name string
	Axes []int
	Host bool
}

var kindName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9]+)*$`) })

// The registry is written by init functions (ext.Register) and read during evaluation and by
// the engine; the mutex makes concurrent init-time registration from tests safe.
var (
	mu       sync.Mutex
	kinds    = map[string]Kind{}
	problems []string
)

// Register adds k. An invalid name, an unknown axis or a second, different registration of one
// name is recorded as a problem (reported by the evaluator) instead of panicking in an init.
func Register(k Kind) {
	mu.Lock()
	defer mu.Unlock()
	if !kindName().MatchString(k.Name) {
		problems = append(problems, fmt.Sprintf("ext.Register: kind name %q must match [a-z0-9]+(.[a-z0-9]+)*", k.Name))
		return
	}
	axes := slices.Clone(k.Axes)
	slices.Sort(axes)
	axes = slices.Compact(axes)
	for _, a := range axes {
		if a < 0 || a >= NumAxes {
			problems = append(problems, fmt.Sprintf("ext.Register: kind %s declares unknown axis %d", k.Name, a))
			return
		}
	}
	k.Axes = axes
	if old, ok := kinds[k.Name]; ok {
		if old.Host != k.Host || !slices.Equal(old.Axes, k.Axes) {
			problems = append(problems, fmt.Sprintf("ext.Register: kind %s is registered twice with different axes", k.Name))
		}
		return
	}
	kinds[k.Name] = k
}

// Lookup returns the kind registered under name.
func Lookup(name string) (Kind, bool) {
	mu.Lock()
	defer mu.Unlock()
	k, ok := kinds[name]
	return k, ok
}

// Problems returns the registration problems recorded so far.
func Problems() []string {
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(problems)
}

// Config is the full configuration of a node as package bob sees it; ext projects it.
type Config struct {
	Kind       string
	Axes       []int
	Platform   string
	Profile    string
	Sanitizers []string
	Race       bool
	Features   map[string][]string // label string → features
}

// Hooks implemented by package bob. They are set once, in bob's init, before any ext call can
// run (ext imports bob, so bob is initialised first).
var (
	// NewNode creates a node: p is a *bob.P, deps a []bob.Package, srcs a []bob.Sources; the
	// result is a bob.Node.
	NewNode func(p any, kind, name string, deps, srcs, spec any) any
	// NewNodeIn is NewNode with the label directory dir (repository- or module-relative)
	// instead of the calling bob.go's directory.
	NewNodeIn func(p any, kind, dir, name string, deps, srcs, spec any) any
	// NewNodeAt is NewNode with the definedAt position taken from site (a value Site
	// returned) instead of the call that creates the node.
	NewNodeAt func(p any, site any, kind, name string, deps, srcs, spec any) any
	// Site returns the bob.go position of the running call (an opaque value for NewNodeAt).
	Site func() any
	// ConfigOf returns the configuration of n, a *bob.Node.
	ConfigOf func(n any) Config
	// Misuse records a definition error caused by a constructor (for example reading an axis
	// its kind did not declare). Evaluation goes on; the error fails it at the end.
	Misuse func(msg string)
	// Restrict limits the platforms n (a bob.Node) may be built for; platforms is a
	// []bob.Platform (empty = every platform).
	Restrict func(n any, platforms any)
	// CallerDir returns the label directory a node created now would get (the calling
	// bob.go's directory); ok is false outside a bob.go of this build.
	CallerDir func() (dir string, ok bool)
	// Elsewhere evaluates the running definition again for platform (same variant) in a
	// nested configuration and returns that configuration (a *bob.P).
	Elsewhere func(p any, platform string) any
	// Nested reports whether p (a *bob.P) is a configuration made by Elsewhere.
	Nested func(p any) bool
	// Record stores v under key in the configuration p; Recorded reads it back.
	Record   func(p any, key string, v any)
	Recorded func(p any, key string) (any, bool)
)
