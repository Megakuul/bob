package npm

import (
	"fmt"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
	"github.com/megakuul/bob/pkg/npm/internal/npmspec"
)

// Kinds of the npm ecosystem (SPEC §03.4). Every importer's node_modules depends on the target
// platform (os/cpu/libc filtering of optional packages).
// A library, app or site is the package's one npm importer (R-FW-071); tests are not units.
func init() {
	for _, k := range []string{npmspec.KindLib, npmspec.KindApp, npmspec.KindSite} {
		ext.Register(ext.Kind{Name: k, Axes: []ext.Axis{ext.AxisPlatform}, Unit: "npm"})
	}
	ext.Register(ext.Kind{Name: npmspec.KindTest, Axes: []ext.Axis{ext.AxisPlatform}})
}

// scriptTools are the host tools of allowed lifecycle scripts (node-gyp builds).
var scriptTools = []nixext.Attr{nixpkgs.Python3, nixpkgs.Gnumake, nixpkgs.Gcc, nixpkgs.Gnused,
	nixpkgs.Gnugrep, nixpkgs.Coreutils}

// Dependency is a registry package, an alias, a peer, or a local *Lib.
type Dependency interface{ npmDep() }

// DepOpt modifies a Dependency.
type DepOpt func(*depOpts)

type depOpts struct{ dev, optional bool }

// Pkg is a declared registry package (Dep, Alias, Indirect): the one place its name is written
// (R-FW-072). Dep and Alias handles are Dependencies; every option that refers to a package
// (AllowScripts, Extend, Peer) takes a handle. An Indirect handle is not a Dependency: it names
// a package of the resolved graph that is not a direct dependency.
type Pkg struct {
	alias, name, req string
	opts             depOpts
	indirect         string // the bob.go position of npm.Indirect; "" for Dep and Alias
}

func (*Pkg) npmDep() {}

// pkgName is the registry name of the package ("lodash" for Alias("lodash4", "lodash", …)).
func (d *Pkg) pkgName() string {
	if d.name != "" {
		return d.name
	}
	return d.alias
}

// peerDep is Peer: inside Extend, the package uses the dependent's copy of pkg.
type peerDep struct{ pkg *Pkg }

func (peerDep) npmDep() {}

// Dep declares a registry dependency; req is a node-semver range or dist-tag ("^19.1.0",
// "latest").
func Dep(name, req string, opts ...DepOpt) *Pkg {
	d := &Pkg{alias: name, req: req}
	for _, o := range opts {
		if o != nil {
			o(&d.opts)
		}
	}
	return d
}

// Alias declares "as" → npm:name@req ("lodash4" → npm:lodash@^4).
func Alias(as, name, req string) *Pkg { return &Pkg{alias: as, name: name, req: req} }

// Indirect declares a package of the resolved graph that is not a direct dependency, so that
// an option can refer to it (npm.AllowScripts(npm.Indirect("esbuild"))). bob lock fails with
// BOB-E-1015 at this position when the graph does not contain it.
func Indirect(name string) *Pkg {
	pos := callerPos()
	if pos == "" {
		pos = "npm.Indirect"
	}
	return &Pkg{alias: name, indirect: pos}
}

// Peer is only valid inside Extend: the package uses the dependent's copy of pkg.
//
//	fastify := npm.Dep("fastify", "^5")
//	npm.Extend(npm.Indirect("@fastify/cors"), npm.Peer(fastify))
func Peer(pkg *Pkg) Dependency { return peerDep{pkg: pkg} }

// Dev marks a build/tooling dependency; it never reaches a runtime closure.
func Dev() DepOpt { return func(o *depOpts) { o.dev = true } }

// Optional marks an optional dependency.
func Optional() DepOpt { return func(o *depOpts) { o.optional = true } }

// Option is a package-level fix declared by the user (not a quirk). Options apply to every
// instance of the named package in the importer's graph.
type Option interface{ npmOption() }

type option struct {
	allow    []string
	extend   *npmspec.Extend
	hoisted  bool
	problem  string
	indirect []npmspec.Indirect
}

func (option) npmOption() {}

// refer records the use of handle d by an option; a nil handle is a problem.
func (o *option) refer(d *Pkg, what string) (string, bool) {
	if d == nil {
		o.problem = what + ": a package handle is nil; declare it with npm.Dep, npm.Alias or npm.Indirect"
		return "", false
	}
	if d.indirect != "" {
		o.indirect = append(o.indirect, npmspec.Indirect{Name: d.pkgName(), Pos: d.indirect})
	}
	return d.pkgName(), true
}

// AllowScripts is the lifecycle-script allowlist (R-NPM-004): the packages whose
// preinstall/install/postinstall scripts bob runs, sandboxed and offline. The packages are
// handles of npm.Dep, npm.Alias or npm.Indirect.
//
//	esbuild := npm.Dep("esbuild", "^0.25.0")
//	npm.AllowScripts(esbuild)
func AllowScripts(pkgs ...*Pkg) Option {
	o := option{allow: []string{}}
	for _, d := range pkgs {
		if n, ok := o.refer(d, "npm.AllowScripts"); ok {
			o.allow = append(o.allow, n)
		}
	}
	return o
}

// Extend adds phantom dependencies to a package (a handle of npm.Dep or npm.Indirect):
//
//	npm.Extend(npm.Indirect("@fastify/cors"), npm.Peer(fastify))
func Extend(pkg *Pkg, deps ...Dependency) Option {
	o := option{}
	name, ok := o.refer(pkg, "npm.Extend")
	if !ok {
		return o
	}
	x := &npmspec.Extend{Package: name, Deps: []npmspec.Dep{}, Peers: []string{}}
	o.extend = x
	for _, d := range deps {
		switch d := d.(type) {
		case peerDep:
			if n, ok := o.refer(d.pkg, "npm.Peer"); ok {
				x.Peers = append(x.Peers, n)
			}
		case *Pkg:
			if d == nil || d.indirect != "" {
				o.problem = fmt.Sprintf("npm.Extend(%q): only packages declared with npm.Dep or npm.Alias and npm.Peer can be added to a package", name)
				continue
			}
			x.Deps = append(x.Deps, specDep(d, -1))
		default:
			o.problem = fmt.Sprintf("npm.Extend(%q): only registry packages and npm.Peer can be added to a package", name)
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

// ESTarget is tsconfig's compilerOptions.target.
type ESTarget string

// ECMAScript targets.
const (
	ES2020 ESTarget = "ES2020"
	ES2022 ESTarget = "ES2022"
	ES2023 ESTarget = "ES2023"
	ESNext ESTarget = "ESNext"
)

// ModuleKind is tsconfig's compilerOptions.module.
type ModuleKind string

// Module kinds (ModuleESNext: Go cannot give the ESTarget and the ModuleKind one name).
const (
	ModuleESNext   ModuleKind = "ESNext"
	ModuleNodeNext ModuleKind = "NodeNext"
	ModulePreserve ModuleKind = "Preserve"
)

// Resolution is tsconfig's compilerOptions.moduleResolution.
type Resolution string

// Module resolution strategies.
const (
	Bundler            Resolution = "Bundler"
	NodeNextResolution Resolution = "NodeNext"
)

// JSX is tsconfig's compilerOptions.jsx.
type JSX string

// JSX modes.
const (
	JSXNone  JSX = ""
	ReactJSX JSX = "react-jsx"
	Preserve JSX = "preserve"
)

// TSConfig generates tsconfig.json (R-NPM-008). Zero value = defaults per constructor.
type TSConfig struct {
	Target           ESTarget       // default ES2022
	Module           ModuleKind     // default ModuleESNext (Vite) / ModuleNodeNext (Library, App)
	ModuleResolution Resolution     // default Bundler (Vite) / NodeNextResolution
	Strict           *bool          // default true
	JSX              JSX            // JSXNone, ReactJSX, Preserve
	Lib, Types       []string       //
	Extends          string         // e.g. "./.svelte-kit/tsconfig.json" (set automatically for Kit)
	Include, Exclude []string       // default ["src"], []
	Extra            map[string]any // other compilerOptions
}

// Generated is a codegen recipe for an importer (connect.TS, graphql.TS, openapi.TS): the
// importer realises it as the package //<importer dir>:gen-<dir without a leading "gen/",
// '/'→'-'> whose output is the content of Dir. Codegen packages fill it; users only pass it on.
type Generated struct {
	Kind    string        // node kind of the generator, registered by the codegen package
	Dir     string        // owned directory relative to the importer ("gen/proto/greet/v1")
	Deps    []bob.Package // the generator's inputs (proto libraries, schemas)
	Tools   []nixext.Attr // the generator's programs (added as Tool deps after Deps)
	Spec    any           // the generator's IR payload
	Runtime []GenRuntime  // npm packages the generated code imports (§03.6.4)
	At      ext.Site      // where the recipe was made (ext.Here): the package's definedAt
	// Also are recipes the importer realises together with this one (the code of the proto
	// libraries a library imports); a recipe for a directory already realised is left out.
	Also []Generated
}

// GenRuntime is an npm package generated code imports, at the version of the generator
// program Generator in bob's nixpkgs pin: bob lock adds it to the importer at exactly that
// version (@bufbuild/protobuf = protoc-gen-es). An explicit npm.Dep of Name whose range
// excludes that version fails bob lock with BOB-E-1021 (R-GEN-006).
type GenRuntime struct {
	Name      string      // the npm package
	Generator nixext.Attr // the generator program
}

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
var labelName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`) })

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
		if r, ok := d.(*Pkg); ok && r != nil && r.indirect == "" {
			b.spec.Peers = append(b.spec.Peers, specDep(r, -1))
		} else {
			b.problems = append(b.problems, "npm.LibOpts.Peers: only registry packages (npm.Dep) can be peers")
		}
	}
	b.spec.TSConfig = tsconfig(o.TSConfig, ModuleNodeNext, NodeNextResolution)
	label := path.Base(o.Name)
	if o.Name == "" {
		b.problems = append(b.problems, "npm.LibOpts.Name is required (the package name, e.g. \"@demo/ui\")")
		label = "lib"
	} else if !labelName().MatchString(label) {
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
	b.spec.TSConfig = tsconfig(o.TSConfig, ModuleNodeNext, NodeNextResolution)
	switch o.Runtime {
	case Node:
		b.spec.Runtime = npmspec.RuntimeNode
		b.spec.Tools.Runtime = b.tool(nixpkgs.NodejsSlim.Pkg(p))
		b.spec.Tools.Esbuild = b.tool(nixpkgs.Esbuild.Tool(p))
	case Bun:
		b.spec.Runtime = npmspec.RuntimeBun
		b.spec.Tools.Runtime = b.tool(nixpkgs.Bun.Pkg(p))
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
	b.spec.TSConfig = tsconfig(o.TSConfig, ModuleESNext, Bundler)
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
	deps = append(deps, nixpkgs.Nodejs.Tool(p))
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
		case peerDep:
			name := "nil"
			if d.pkg != nil {
				name = d.pkg.pkgName()
			}
			b.problems = append(b.problems, fmt.Sprintf("npm.Peer(%s) is only valid inside npm.Extend", name))
		case *Pkg:
			if d == nil {
				b.problems = append(b.problems, "a dependency is a nil *npm.Pkg")
				continue
			}
			if d.indirect != "" {
				b.problems = append(b.problems, fmt.Sprintf("npm.Indirect(%q) declared at %s is not a dependency; declare it with npm.Dep(name, req)", d.alias, d.indirect))
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
		b.spec.Options.Indirect = append(b.spec.Options.Indirect, v.indirect...)
		if v.extend != nil {
			b.spec.Options.Extend = append(b.spec.Options.Extend, *v.extend)
		}
		b.spec.Options.Hoisted = b.spec.Options.Hoisted || v.hoisted
	}
	slices.Sort(allow)
	b.spec.Options.AllowScripts = slices.Compact(allow)
	slices.SortFunc(b.spec.Options.Indirect, func(a, c npmspec.Indirect) int { return strings.Compare(a.Name+" "+a.Pos, c.Name+" "+c.Pos) })
	b.spec.Options.Indirect = slices.Compact(b.spec.Options.Indirect)
	b.generated(p, gen)
	b.spec.Tools.Node = b.tool(nixpkgs.Nodejs.Tool(p))
	b.spec.Tools.Bash = b.tool(nixpkgs.Bash.Tool(p))
	if len(b.spec.Options.AllowScripts) > 0 {
		attrs := slices.Clone(scriptTools)
		for _, q := range quirks {
			if slices.Contains(b.spec.Options.AllowScripts, q.Package) {
				attrs = append(attrs, q.Tools...)
			}
		}
		for _, a := range attrs {
			b.spec.Tools.Scripts = append(b.spec.Tools.Scripts, b.tool(a.Tool(p)))
		}
	}
	return b
}

// generated realises the importer's codegen recipes as packages in its directory (and their
// Also recipes), adds them as dependencies and the runtimes they need as dependencies that bob
// lock pins at the generators' versions.
func (b *importer) generated(p *bob.P, gen []Generated) {
	b.spec.Generated = []npmspec.Generated{}
	owned := map[string]bool{}
	declared := map[string]bool{}
	for _, d := range b.spec.Deps {
		declared[d.Alias] = true
	}
	var realise func(g Generated, explicit bool)
	realise = func(g Generated, explicit bool) {
		dir := path.Clean(g.Dir)
		if g.Kind == "" || g.Dir == "" || path.IsAbs(dir) || dir == "." || dir == ".." || strings.HasPrefix(dir, "../") || dir == "node_modules" || strings.HasPrefix(dir, "node_modules/") {
			b.problems = append(b.problems, fmt.Sprintf("generated output %q needs a generator kind and a directory inside the importer", g.Dir))
			return
		}
		if owned[dir] {
			if explicit {
				b.problems = append(b.problems, fmt.Sprintf("two generated outputs generate into %s/; give one of them another output directory", dir))
			}
			return
		}
		owned[dir] = true
		deps := slices.Clone(g.Deps)
		for _, t := range g.Tools {
			deps = append(deps, t.Tool(p))
		}
		name := "gen-" + strings.ReplaceAll(strings.TrimPrefix(dir, "gen/"), "/", "-")
		n := &genOutput{Node: ext.NewAt(p, g.At, g.Kind, name, deps, nil, g.Spec)}
		sg := npmspec.Generated{Dir: dir, Dep: b.tool(n), Runtime: []npmspec.Runtime{}}
		for _, r := range g.Runtime {
			sg.Runtime = append(sg.Runtime, npmspec.Runtime{Name: r.Name, Generator: r.Generator.Name()})
			if !declared[r.Name] {
				declared[r.Name] = true
				b.spec.Deps = append(b.spec.Deps, npmspec.Dep{Alias: r.Name, Req: npmspec.GeneratorPrefix + r.Generator.Name(), Link: -1})
			}
		}
		b.spec.Generated = append(b.spec.Generated, sg)
		for _, a := range g.Also {
			realise(a, false)
		}
	}
	for _, g := range gen {
		realise(g, true)
	}
	var nested []string
	for a := range owned {
		for c := range owned {
			if a != c && strings.HasPrefix(c, a+"/") {
				nested = append(nested, fmt.Sprintf("generated output %s/ is inside %s/; generated directories may not be nested", c, a))
			}
		}
	}
	slices.Sort(nested)
	b.problems = append(b.problems, nested...)
}

// genOutput is a realised codegen recipe.
type genOutput struct{ bob.Node }

// tool appends a dependency and returns its index.
func (b *importer) tool(pk bob.Package) int {
	b.deps = append(b.deps, pk)
	return len(b.deps) - 1
}

// defaultName is the label name: the given one or the directory's name.
func (b *importer) defaultName(name, fallback string) string {
	if name != "" {
		if !labelName().MatchString(name) {
			b.problems = append(b.problems, fmt.Sprintf("name %q is not a valid package name (lower case letters, digits, '_', '-', '.')", name))
			return fallback
		}
		return name
	}
	dir, _ := ext.CallerDir()
	if base := path.Base(dir); dir != "" && labelName().MatchString(base) {
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

func specDep(d *Pkg, link int) npmspec.Dep {
	s := npmspec.Dep{Alias: d.alias, Req: d.req, Dev: d.opts.dev, Optional: d.opts.optional, Link: link}
	if d.name != "" && d.name != d.alias {
		s.Name = d.name
	}
	return s
}

// tsconfig applies the constructor's defaults (SPEC §03.4.1).
func tsconfig(t TSConfig, module ModuleKind, resolution Resolution) npmspec.TSConfig {
	out := npmspec.TSConfig{Target: defaultStr(string(t.Target), string(ES2022)), Module: defaultStr(string(t.Module), string(module)),
		ModuleResolution: defaultStr(string(t.ModuleResolution), string(resolution)), Strict: t.Strict == nil || *t.Strict,
		JSX: string(t.JSX), Lib: nonNil(t.Lib), Types: nonNil(t.Types), Extends: t.Extends,
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
