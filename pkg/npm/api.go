package npm

import "github.com/megakuul/bob/pkg/bob"

// Dependency is a registry package, an alias, a peer, or a local *Lib.
type Dependency interface{ npmDep() }

type dep struct{}

func (dep) npmDep() {}

// DepOpt modifies a Dependency.
type DepOpt func(*depOpts)

type depOpts struct{ dev, optional bool }

// Dep is a registry dependency; req is a node-semver range or dist-tag.
func Dep(name, req string, opts ...DepOpt) Dependency { return dep{} }

// Alias declares "as" → npm:name@req.
func Alias(as, name, req string) Dependency { return dep{} }

// Peer is only valid inside Extend.
func Peer(name string) Dependency { return dep{} }

// Dev marks a build/tooling dependency.
func Dev() DepOpt { return func(o *depOpts) { o.dev = true } }

// Optional marks an optional dependency.
func Optional() DepOpt { return func(o *depOpts) { o.optional = true } }

// Option is a package-level fix declared by the user (not a quirk).
type Option interface{ npmOption() }

type option struct{}

func (option) npmOption() {}

// AllowScripts is the lifecycle-script allowlist (R-NPM-004).
func AllowScripts(pkgs ...string) Option { return option{} }

// Extend adds phantom dependencies to a package.
func Extend(pkg string, deps ...Dependency) Option { return option{} }

// Hoisted selects the hoisted layout fallback (D30).
func Hoisted() Option { return option{} }

// Runtime selects the runtime of an App.
type Runtime int

// Runtimes.
const (
	Node Runtime = iota
	Bun
)

// TSConfig generates tsconfig.json (R-NPM-008).
type TSConfig struct {
	Target, Module, ModuleResolution string
	Strict                           *bool
	JSX                              string
	Lib, Types                       []string
	Extends                          string
	Include, Exclude                 []string
	Extra                            map[string]any
}

// Generated is a codegen output materialised into an importer (§03.6).
type Generated interface{ npmGenerated() }

// LibOpts configures Library. There is deliberately no Runtime field (R-NPM-009).
type LibOpts struct {
	Name      string
	Srcs      bob.Sources
	Entry     string
	Deps      []Dependency
	Peers     []Dependency
	TSConfig  TSConfig
	Generated []Generated
	Options   []Option
}

// Lib is a TypeScript library importer.
type Lib struct{}

func (*Lib) npmImporter() {}

// Library compiles with tsc into dist/.
func Library(p *bob.P, o LibOpts) *Lib { return &Lib{} }

// AppOpts configures App.
type AppOpts struct {
	Name      string
	Entry     string
	Srcs      bob.Sources
	Deps      []Dependency
	Runtime   Runtime
	Bundle    *bool
	TSConfig  TSConfig
	Generated []Generated
	Options   []Option
}

// App is a Node/Bun program.
type App struct{}

func (*App) npmImporter() {}

// NewApp is the stub constructor; the public name App is the type, as in SPEC the function and
// type share it, which Go forbids, so the SPEC signature is spelled AppFunc here until the
// implementation settles the naming (reported to the coordinator).
func NewApp(p *bob.P, o AppOpts) *App { return &App{} }

// DevServer configures `bob dev`.
type DevServer struct {
	Port int
	Host string
}

// ViteOpts configures Vite. There is deliberately no Runtime field (R-NPM-009).
type ViteOpts struct {
	Name      string
	Srcs      bob.Sources
	Deps      []Dependency
	Config    string
	Kit       bool
	Dev       DevServer
	TSConfig  TSConfig
	Generated []Generated
	Options   []Option
}

// Site is a Vite importer.
type Site struct{}

func (*Site) npmImporter() {}

// Vite runs `vite build` hermetically.
func Vite(p *bob.P, o ViteOpts) *Site { return &Site{} }

// Runner selects the test runner.
type Runner int

// Runners.
const (
	Vitest Runner = iota
	NodeTest
)

// TestOpts configures Test.
type TestOpts struct {
	Of       interface{ npmImporter() }
	Runner   Runner
	Files    bob.Sources
	Services []bob.Service
}

// Test runs vitest or node --test.
type Test struct{}

// NewTest is the stub constructor of Test (see NewApp for the naming note).
func NewTest(p *bob.P, o TestOpts) *Test { return &Test{} }
