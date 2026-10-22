package npm

import (
	"fmt"
	"path"
	"reflect"
	"regexp"
	"slices"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/npm/internal/npmspec"
)

// Kinds of the npm ecosystem (SPEC §03.4). Every importer's node_modules depends on the target
// platform (os/cpu/libc filtering of optional packages).
func init() {
	for _, k := range []string{npmspec.KindLib, npmspec.KindApp, npmspec.KindSite, npmspec.KindTest} {
		ext.Register(ext.Kind{Name: k, Axes: []ext.Axis{ext.AxisPlatform}})
	}
}

// Dependency is a registry package, an alias, a peer, or a local *Lib.
type Dependency interface{ npmDep() }

// DepOpt modifies a Dependency.
type DepOpt func(*depOpts)

type depOpts struct{ dev, optional bool }

// registryDep is Dep, Alias and Peer.
type registryDep struct {
	alias, name, req string
	opts             depOpts
	peer             bool
}

func (registryDep) npmDep() {}

// Dep is a registry dependency; req is a node-semver range or dist-tag ("^19.1.0", "latest").
func Dep(name, req string, opts ...DepOpt) Dependency {
	d := registryDep{alias: name, req: req}
	for _, o := range opts {
		if o != nil {
			o(&d.opts)
		}
	}
	return d
}

// Alias declares "as" → npm:name@req ("lodash4" → npm:lodash@^4).
func Alias(as, name, req string) Dependency { return registryDep{alias: as, name: name, req: req} }

// Peer is only valid inside Extend: the package uses the dependent's copy of name.
func Peer(name string) Dependency { return registryDep{alias: name, peer: true} }

// Dev marks a build/tooling dependency; it never reaches a runtime closure.
func Dev() DepOpt { return func(o *depOpts) { o.dev = true } }

// Optional marks an optional dependency.
func Optional() DepOpt { return func(o *depOpts) { o.optional = true } }

// Option is a package-level fix declared by the user (not a quirk). Options apply to every
// instance of the named package in the importer's graph.
type Option interface{ npmOption() }

type option struct {
	allow   []string
	extend  *npmspec.Extend
	hoisted bool
	problem string
}

func (option) npmOption() {}

// AllowScripts is the lifecycle-script allowlist (R-NPM-004): exact package names whose
// preinstall/install/postinstall scripts bob runs, sandboxed and offline.
func AllowScripts(pkgs ...string) Option { return option{allow: slices.Clone(pkgs)} }

// Extend adds phantom dependencies to a package:
//
//	npm.Extend("@fastify/cors", npm.Peer("fastify"))
func Extend(pkg string, deps ...Dependency) Option {
	x := &npmspec.Extend{Package: pkg, Deps: []npmspec.Dep{}, Peers: []string{}}
	o := option{extend: x}
	for _, d := range deps {
		switch d := d.(type) {
		case registryDep:
			if d.peer {
				x.Peers = append(x.Peers, d.alias)
				continue
			}
			x.Deps = append(x.Deps, specDep(d, -1))
		default:
			o.problem = fmt.Sprintf("npm.Extend(%q): only registry packages and npm.Peer can be added to a package", pkg)
		}
	}
	return o
}

// Hoisted selects the per-importer hoisted layout fallback (D30): one npm-style node_modules
// derivation instead of one derivation per package.
func Hoisted() Option { return option{hoisted: true} }

// Runtime selects the runtime of an App.
type Runtime int

// Runtimes.
const (
	Node Runtime = iota
	Bun
)

// TSConfig generates tsconfig.json (R-NPM-008). Zero value = defaults per constructor.
type TSConfig struct {
	Target           string         // default "ES2022"
	Module           string         // default "ESNext" (Vite) / "NodeNext" (Library, App)
	ModuleResolution string         // default "Bundler" (Vite) / "NodeNext"
	Strict           *bool          // default true
	JSX              string         // "", "react-jsx", "preserve"
	Lib, Types       []string       //
	Extends          string         // e.g. "./.svelte-kit/tsconfig.json" (set automatically for Kit)
	Include, Exclude []string       // default ["src"], []
	Extra            map[string]any // other compilerOptions
}

// Generated is a codegen output materialised into an importer (§03.6).
type Generated interface{ npmGenerated() }

// LibOpts configures Library. There is deliberately no Runtime field (R-NPM-009).
type LibOpts struct {
	Name      string      // package name ("@demo/ui"); required
	Srcs      bob.Sources // default bob.Glob("src/**")
	Entry     string      // default "src/index.ts"
	Deps      []Dependency
	Peers     []Dependency // provided by the consumer
	TSConfig  TSConfig
	Generated []Generated
	Options   []Option
}

// Lib is a TypeScript library importer.
type Lib struct{ bob.Node }

func (*Lib) npmImporter() {}
func (*Lib) npmDep()      {}

// labelName matches bob's package names.
var labelName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)

// Library compiles with tsc into dist/ (+ .d.ts) and is linked into consumers' node_modules
// under Name (R-NPM-006). Label //<dir>:<last element of Name>.
//
//	var UI = bob.Define(func(p *bob.P) *npm.Lib {
//		return npm.Library(p, npm.LibOpts{Name: "@demo/ui", Peers: []npm.Dependency{npm.Dep("react", "^19")}})
//	})
func Library(p *bob.P, o LibOpts) *Lib {
	b := newImporter(p, o.Deps, o.Options, o.Generated)
	b.spec.Name = o.Name
	b.spec.Entry = defaultStr(o.Entry, "src/index.ts")
	for _, d := range o.Peers {
		if r, ok := d.(registryDep); ok && !r.peer {
			b.spec.Peers = append(b.spec.Peers, specDep(r, -1))
		} else {
			b.problems = append(b.problems, "npm.LibOpts.Peers: only registry packages (npm.Dep) can be peers")
		}
	}
	b.spec.TSConfig = tsconfig(o.TSConfig, "NodeNext", "NodeNext")
	label := path.Base(o.Name)
	if o.Name == "" {
		b.problems = append(b.problems, "npm.LibOpts.Name is required (the package name, e.g. \"@demo/ui\")")
		label = "lib"
	} else if !labelName.MatchString(label) {
		label = "lib"
	}
	srcs := o.Srcs
	if isZero(srcs) {
		srcs = bob.Glob("src/**")
	}
	l := &Lib{}
	l.Node = b.node(p, npmspec.KindLib, label, srcs)
	return l
}

// AppOpts configures App.
type AppOpts struct {
	Name      string      // default: directory name
	Entry     string      // "src/server.ts"; required
	Srcs      bob.Sources // default: the importer directory (without node_modules, dist, generated files)
	Deps      []Dependency
	Runtime   Runtime // Node (default) | Bun (R-NPM-009)
	Bundle    *bool   // default true: esbuild bundle; false: ship node_modules
	TSConfig  TSConfig
	Generated []Generated
	Options   []Option
}

// App is a Node/Bun program: bin/<Name> calling the runtime by store path.
type App struct{ bob.Node }

func (*App) npmImporter() {}

// MainProgram is the program's path in the package output (bob run).
func (a *App) MainProgram() string { return "bin/" + a.Label().Name }

// NewApp declares a Node/Bun program (SPEC §03.4.1 calls it App; Go cannot have a function and
// a type of one name, decision A35). Label //<dir>:<Name>.
//
//	var Server = bob.Define(func(p *bob.P) *npm.App {
//		return npm.NewApp(p, npm.AppOpts{Entry: "src/server.ts", Deps: []npm.Dependency{npm.Dep("fastify", "^5")}})
//	})
func NewApp(p *bob.P, o AppOpts) *App {
	b := newImporter(p, o.Deps, o.Options, o.Generated)
	b.spec.Entry = o.Entry
	if o.Entry == "" {
		b.problems = append(b.problems, "npm.AppOpts.Entry is required (e.g. \"src/server.ts\")")
	}
	b.spec.Bundle = o.Bundle == nil || *o.Bundle
	b.spec.TSConfig = tsconfig(o.TSConfig, "NodeNext", "NodeNext")
	switch o.Runtime {
	case Node:
		b.spec.Runtime = npmspec.RuntimeNode
		b.spec.Tools.Runtime = b.tool(nixpkgs.Pkg(p, "nodejs-slim"))
		b.spec.Tools.Esbuild = b.tool(nixpkgs.Tool(p, "esbuild"))
	case Bun:
		b.spec.Runtime = npmspec.RuntimeBun
		b.spec.Tools.Runtime = b.tool(nixpkgs.Pkg(p, "bun"))
	default:
		b.problems = append(b.problems, fmt.Sprintf("npm.AppOpts.Runtime %d is neither npm.Node nor npm.Bun", o.Runtime))
	}
	name := b.defaultName(o.Name, "app")
	b.spec.Name = name
	a := &App{}
	a.Node = b.node(p, npmspec.KindApp, name, b.srcs(o.Srcs))
	return a
}

// DevServer configures `bob dev`.
type DevServer struct {
	Port int    // default 5173
	Host string // default "localhost"
}

// ViteOpts configures Vite. There is deliberately no Runtime field (R-NPM-009).
type ViteOpts struct {
	Name      string      // default: directory name
	Srcs      bob.Sources // default: the importer directory (without node_modules, dist, build, generated files)
	Deps      []Dependency
	Config    string // default "vite.config.ts", else the vite.config.* found (user-owned, D33)
	Kit       bool   // SvelteKit with adapter-static
	Dev       DevServer
	TSConfig  TSConfig
	Generated []Generated
	Options   []Option
}

// Site is a Vite importer; its output is the static site directory.
type Site struct{ bob.Node }

func (*Site) npmImporter() {}

// Vite runs `vite build` hermetically; `bob dev` runs its dev server (R-NPM-007, R-NPM-010).
//
//	var Web = bob.Define(func(p *bob.P) *npm.Site {
//		return npm.Vite(p, npm.ViteOpts{Deps: []npm.Dependency{
//			npm.Dep("svelte", "^5"), npm.Dep("vite", "^7.1.0", npm.Dev()),
//			npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()), ui.UI(p)}})
//	})
func Vite(p *bob.P, o ViteOpts) *Site {
	b := newImporter(p, o.Deps, o.Options, o.Generated)
	b.spec.Config = o.Config
	b.spec.Kit = o.Kit
	b.spec.DevPort, b.spec.DevHost = o.Dev.Port, o.Dev.Host
	if b.spec.DevPort == 0 {
		b.spec.DevPort = 5173
	}
	if b.spec.DevHost == "" {
		b.spec.DevHost = "localhost"
	}
	if o.Dev.Port < 0 || o.Dev.Port > 65535 {
		b.problems = append(b.problems, fmt.Sprintf("npm.DevServer.Port %d is not a port", o.Dev.Port))
	}
	inherit := false
	if o.Kit && o.TSConfig.Extends == "" {
		// svelte-kit sync writes the include list (src, ambient types); overriding it loses them.
		o.TSConfig.Extends = "./.svelte-kit/tsconfig.json"
		inherit = o.TSConfig.Include == nil && o.TSConfig.Exclude == nil
	}
	b.spec.TSConfig = tsconfig(o.TSConfig, "ESNext", "Bundler")
	b.spec.TSConfig.Inherit = inherit
	name := b.defaultName(o.Name, "site")
	b.spec.Name = name
	s := &Site{}
	s.Node = b.node(p, npmspec.KindSite, name, b.srcs(o.Srcs))
	return s
}

// Runner selects the test runner.
type Runner int

// Runners. SvelteCheck and ESLint are checks run as tests (SPEC R-NPM-007a, Gate H G24):
// `svelte-check --fail-on-warnings` (after `svelte-kit sync` for SvelteKit) and `eslint .`.
const (
	Vitest Runner = iota
	NodeTest
	SvelteCheck
	ESLint
)

// TestOpts configures Test. Of names the importer whose deps and sources are used.
type TestOpts struct {
	Of       interface{ npmImporter() } // *Lib, *App, *Site
	Runner   Runner
	Files    bob.Sources // default bob.Glob("src/**/*.test.ts")
	Services []bob.Service
}

// Test runs vitest or node --test over an importer.
type Test struct{ bob.Node }

// NewTest declares a test of an importer (SPEC §03.4.1 calls it Test; see NewApp). Label
// //<dir>:test (//<dir>:svelte-check, //<dir>:eslint for those runners). The checks use the
// importer's sources; Files adds to them (default for Vitest and NodeTest: src/**/*.test.ts).
//
//	var WebTest = bob.Define(func(p *bob.P) *npm.Test { return npm.NewTest(p, npm.TestOpts{Of: Web(p)}) })
func NewTest(p *bob.P, o TestOpts) *Test {
	var problems []string
	var of bob.Package
	switch v := o.Of.(type) {
	case *Lib:
		if v != nil {
			of = v
		}
	case *App:
		if v != nil {
			of = v
		}
	case *Site:
		if v != nil {
			of = v
		}
	}
	if of == nil {
		ext.Errorf("npm.TestOpts.Of must be an npm.Library, npm.App or npm.Vite package")
		return &Test{}
	}
	spec := npmspec.Test{Of: 0, Runner: npmspec.RunnerVitest}
	switch o.Runner {
	case Vitest:
	case NodeTest:
		spec.Runner = npmspec.RunnerNodeTest
	case SvelteCheck:
		spec.Runner = npmspec.RunnerSvelteCheck
	case ESLint:
		spec.Runner = npmspec.RunnerESLint
	default:
		problems = append(problems, fmt.Sprintf("npm.TestOpts.Runner %d is not npm.Vitest, npm.NodeTest, npm.SvelteCheck or npm.ESLint", o.Runner))
	}
	deps := []bob.Package{of}
	spec.Node = len(deps)
	deps = append(deps, nixpkgs.Tool(p, "nodejs"))
	spec.Services, deps = ext.Services(o.Services, deps)
	var srcs []bob.Sources
	switch {
	case !isZero(o.Files):
		srcs = []bob.Sources{o.Files}
	case o.Runner == Vitest || o.Runner == NodeTest:
		srcs = []bob.Sources{bob.Glob("src/**/*.test.ts")}
	}
	name := "test"
	switch o.Runner {
	case SvelteCheck:
		name = "svelte-check"
	case ESLint:
		name = "eslint"
	}
	t := &Test{Node: ext.New(p, npmspec.KindTest, name, deps, srcs, spec)}
	for _, pr := range problems {
		ext.Errorf("%s: %s: %s", t.DefinedAt(), t.Label(), pr)
	}
	return t
}

// importer collects one importer's spec and deps.
type importer struct {
	spec     npmspec.Importer
	deps     []bob.Package
	problems []string
}

func newImporter(p *bob.P, deps []Dependency, opts []Option, gen []Generated) *importer {
	b := &importer{spec: npmspec.Importer{Deps: []npmspec.Dep{}, Peers: []npmspec.Dep{},
		Options: npmspec.Options{AllowScripts: []string{}, Extend: []npmspec.Extend{}},
		Tools:   npmspec.Tools{Node: -1, Bash: -1, Runtime: -1, Esbuild: -1, Scripts: []int{}}}}
	for _, d := range deps {
		switch d := d.(type) {
		case registryDep:
			if d.peer {
				b.problems = append(b.problems, fmt.Sprintf("npm.Peer(%q) is only valid inside npm.Extend", d.alias))
				continue
			}
			b.spec.Deps = append(b.spec.Deps, specDep(d, -1))
		case *Lib:
			if d == nil {
				b.problems = append(b.problems, "a dependency is a nil *npm.Lib")
				continue
			}
			b.spec.Deps = append(b.spec.Deps, npmspec.Dep{Link: b.tool(d)})
		default:
			b.problems = append(b.problems, fmt.Sprintf("unsupported npm dependency %T", d))
		}
	}
	allow := []string{}
	for _, o := range opts {
		v, ok := o.(option)
		if !ok {
			b.problems = append(b.problems, fmt.Sprintf("unsupported npm option %T", o))
			continue
		}
		if v.problem != "" {
			b.problems = append(b.problems, v.problem)
		}
		allow = append(allow, v.allow...)
		if v.extend != nil {
			b.spec.Options.Extend = append(b.spec.Options.Extend, *v.extend)
		}
		b.spec.Options.Hoisted = b.spec.Options.Hoisted || v.hoisted
	}
	slices.Sort(allow)
	b.spec.Options.AllowScripts = slices.Compact(allow)
	if len(gen) > 0 {
		b.problems = append(b.problems, "npm Generated outputs are not supported yet (codegen, §03.6)")
	}
	b.spec.Tools.Node = b.tool(nixpkgs.Tool(p, "nodejs"))
	b.spec.Tools.Bash = b.tool(nixpkgs.Tool(p, "bash"))
	if len(b.spec.Options.AllowScripts) > 0 {
		attrs := []string{"python3", "gnumake", "gcc", "gnused", "gnugrep", "coreutils"}
		for _, q := range quirks {
			if slices.Contains(b.spec.Options.AllowScripts, q.Package) {
				attrs = append(attrs, q.Tools...)
			}
		}
		for _, a := range attrs {
			b.spec.Tools.Scripts = append(b.spec.Tools.Scripts, b.tool(nixpkgs.Tool(p, a)))
		}
	}
	return b
}

// tool appends a dependency and returns its index.
func (b *importer) tool(pk bob.Package) int {
	b.deps = append(b.deps, pk)
	return len(b.deps) - 1
}

// defaultName is the label name: the given one or the directory's name.
func (b *importer) defaultName(name, fallback string) string {
	if name != "" {
		if !labelName.MatchString(name) {
			b.problems = append(b.problems, fmt.Sprintf("name %q is not a valid package name (lower case letters, digits, '_', '-', '.')", name))
			return fallback
		}
		return name
	}
	dir, _ := ext.CallerDir()
	if base := path.Base(dir); dir != "" && labelName.MatchString(base) {
		return base
	}
	return fallback
}

// srcs is the default source set of apps and sites: the importer directory without what
// bob, the package manager or the build put there.
func (b *importer) srcs(s bob.Sources) bob.Sources {
	if !isZero(s) {
		return s
	}
	return bob.Glob("**", "!node_modules", "!dist", "!build", "!.svelte-kit", "!.vite",
		"!package.json", "!tsconfig.json", "!package-lock.json")
}

func (b *importer) node(p *bob.P, kind, name string, srcs bob.Sources) bob.Node {
	n := ext.New(p, kind, name, b.deps, []bob.Sources{srcs}, b.spec)
	for _, pr := range b.problems {
		ext.Errorf("%s: %s: %s", n.DefinedAt(), n.Label(), pr)
	}
	return n
}

func specDep(d registryDep, link int) npmspec.Dep {
	s := npmspec.Dep{Alias: d.alias, Req: d.req, Dev: d.opts.dev, Optional: d.opts.optional, Link: link}
	if d.name != "" && d.name != d.alias {
		s.Name = d.name
	}
	return s
}

// tsconfig applies the constructor's defaults (SPEC §03.4.1).
func tsconfig(t TSConfig, module, resolution string) npmspec.TSConfig {
	out := npmspec.TSConfig{Target: defaultStr(t.Target, "ES2022"), Module: defaultStr(t.Module, module),
		ModuleResolution: defaultStr(t.ModuleResolution, resolution), Strict: t.Strict == nil || *t.Strict,
		JSX: t.JSX, Lib: nonNil(t.Lib), Types: nonNil(t.Types), Extends: t.Extends,
		Include: nonNil(t.Include), Exclude: nonNil(t.Exclude), Extra: map[string]any{}}
	if t.Include == nil {
		out.Include = []string{"src"}
	}
	for k, v := range t.Extra {
		out.Extra[k] = v
	}
	return out
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return slices.Clone(s)
}

// isZero reports whether s selects nothing (the zero bob.Sources: the default applies).
func isZero(s bob.Sources) bool { return reflect.ValueOf(s).IsZero() }
