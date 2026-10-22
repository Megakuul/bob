// Package npmspec holds the IR payloads ("spec") of the npm node kinds. pkg/npm writes them
// during evaluation, pkg/npm/gen reads them when planning and locking. Dependency references
// are indices into the node's deps.
package npmspec

import "github.com/megakuul/bob/pkg/bob/ext"

// Node kinds of the npm ecosystem; "npm" is also the bob.lock section key.
const (
	KindLib  = "npm.lib"  // a TypeScript library (npm.Library)
	KindApp  = "npm.app"  // a Node or Bun program (npm.App)
	KindSite = "npm.site" // a Vite site (npm.Vite)
	KindTest = "npm.test" // vitest or node --test over an importer (npm.Test)
)

// Dep is one declared dependency of an importer, or of a package (npm.Extend).
type Dep struct {
	Alias    string `json:"alias"`          // the name under node_modules
	Name     string `json:"name,omitempty"` // registry name when it differs (npm.Alias)
	Req      string `json:"req,omitempty"`  // node-semver range or dist-tag
	Dev      bool   `json:"dev,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Link     int    `json:"link"` // dep index of a local npm.lib, -1 for registry packages
}

// Extend is npm.Extend: dependencies a registry package uses without declaring them.
type Extend struct {
	Package string   `json:"package"`
	Deps    []Dep    `json:"deps"`
	Peers   []string `json:"peers"`
}

// Options are the package-level fixes of an importer (npm.Option).
type Options struct {
	AllowScripts []string `json:"allowScripts"` // sorted, unique
	Extend       []Extend `json:"extend"`       // in declaration order
	Hoisted      bool     `json:"hoisted,omitempty"`
}

// TSConfig is npm.TSConfig with the constructor's defaults applied.
type TSConfig struct {
	Target           string         `json:"target"`
	Module           string         `json:"module"`
	ModuleResolution string         `json:"moduleResolution"`
	Strict           bool           `json:"strict"`
	JSX              string         `json:"jsx,omitempty"`
	Lib              []string       `json:"lib"`
	Types            []string       `json:"types"`
	Extends          string         `json:"extends,omitempty"`
	Include          []string       `json:"include"`
	Exclude          []string       `json:"exclude"`
	Extra            map[string]any `json:"extra"`
}

// Tools are the dep indices of the nixpkgs packages an importer's derivations use; -1 when
// the importer does not need the tool.
type Tools struct {
	Node    int   `json:"node"`    // nodejs: build tools, .bin wrappers, scripts
	Bash    int   `json:"bash"`    // interpreter of .bin wrappers and lifecycle scripts
	Runtime int   `json:"runtime"` // apps: nodejs-slim or bun
	Esbuild int   `json:"esbuild"` // Node apps: the bundler
	Scripts []int `json:"scripts"` // allowed lifecycle scripts: python3, make, cc, sed, grep, coreutils, quirk tools
}

// Importer is the spec shared by npm.lib, npm.app and npm.site: one directory with a generated
// package.json, tsconfig.json and node_modules. The importer directory is the label's.
type Importer struct {
	Name     string   `json:"name"` // package name (libraries: linked under it)
	Deps     []Dep    `json:"deps"` // declaration order
	Peers    []Dep    `json:"peers"`
	Entry    string   `json:"entry,omitempty"`
	TSConfig TSConfig `json:"tsconfig"`
	Options  Options  `json:"options"`
	Tools    Tools    `json:"tools"`

	Runtime string `json:"runtime,omitempty"` // apps: "node" or "bun"
	Bundle  bool   `json:"bundle,omitempty"`  // apps: bundle everything (else ship node_modules)

	Config  string `json:"config,omitempty"`  // sites: the user's vite config file
	Kit     bool   `json:"kit,omitempty"`     // sites: SvelteKit (adapter-static)
	DevPort int    `json:"devPort,omitempty"` // sites: bob dev
	DevHost string `json:"devHost,omitempty"`
}

// Runtimes of an app.
const (
	RuntimeNode = "node"
	RuntimeBun  = "bun"
)

// Runners of a test.
const (
	RunnerVitest   = "vitest"
	RunnerNodeTest = "node-test"
)

// Test is the spec of an npm.test node: Of is the dep index of the importer whose sources and
// node_modules the test uses; the node's own sources are the test files.
type Test struct {
	Of       int           `json:"of"`
	Runner   string        `json:"runner"`
	Services []ext.Service `json:"services"`
	Node     int           `json:"node"` // dep index of nodejs
}
