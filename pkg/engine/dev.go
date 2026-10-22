package engine

import "context"

// DevPreparer is implemented by an Ecosystem that contributes to developer environments
// (SPEC §02.7, R-DEV-001, R-DEV-002; §02 open item 10): its toolchain and tools on PATH, its
// variables, the working-tree files tooling needs, in-tree tools and dev servers. bob dev,
// bob env, the shell hook and editor settings call it for every registered ecosystem that owns
// nodes of the repository's graph. PrepareDev must be deterministic and run no processes;
// it may look up nixpkgs attributes through req.Nixpkgs.
type DevPreparer interface {
	PrepareDev(ctx context.Context, req DevRequest) (DevPrep, error)
}

// DevRequest asks an ecosystem for its share of one dev shell.
type DevRequest struct {
	// PlanRequest carries the whole repository's graph, this ecosystem's nodes, its lock
	// section, the nixpkgs index and the resolved nixpkgs nodes (Inputs: node id → outputs,
	// e.g. a module's toolchain). Sources and Resolver are not set.
	PlanRequest
	Shell   string   // the dev shell's name
	Servers []string // ids of this ecosystem's nodes the shell lists in DevShell.Servers
}

// DevPrep is an ecosystem's share of a dev shell.
type DevPrep struct {
	// Packages are top-level packages whose bin/ goes on PATH inside, in this order (after
	// the dev shell's own Packages). Every executable in their bin/ gets a shim (§02.7.2).
	Packages []DevPackage
	// Env are variables of the dev shell. They are exported outside the namespace too when
	// their value references no store path (§02.7.2 outside rules); the dev shell's own Env
	// wins over them.
	Env map[string]string
	// InsideEnv are variables for inside the namespace only (GOROOT, GOTOOLCHAIN).
	InsideEnv map[string]string
	// ToolEnv are extra variables for single tools (tool name → variables), e.g. gopls's
	// GOFLAGS=-tags=bob (S1-7).
	ToolEnv map[string]map[string]string
	// Tools are in-tree tools (tool name → repo-relative path), e.g. node_modules/.bin/tsc.
	Tools map[string]string
	// Files are working-tree files tooling needs beyond the Native files (materialised
	// codegen, a dev-server wrapper config). bob writes them like Native files.
	Files []GeneratedFile
	// Servers are the dev servers of the requested Servers nodes.
	Servers []DevServerSpec
}

// DevPackage is one top-level package of a dev shell.
type DevPackage struct {
	Name    string // "go", "gopls": the summary line of bob dev
	Version string // "1.26.8"; "" when unknown
	Path    string // store path whose bin/ goes on PATH
	Drv     string // the derivation that realises Path ("" when Path needs no realisation)
}

// DevServerSpec is one dev server bob dev starts (§02.7.7 step 5, the §03 contract).
type DevServerSpec struct {
	Name  string            // unique per shell, [a-z0-9-]: the log is .bob/dev/<name>.log
	Label string            // the package it serves
	Cmd   []string          // argv; a name without "/" is looked up on the dev shell's PATH
	Cwd   string            // repo-relative working directory
	Env   map[string]string // extra variables
	// Ready is how bob sees that the server is up: "http" = a TCP connect to Port succeeds,
	// "log" = Pattern (a Go regexp) matches a line of its output.
	Ready   string
	Port    int
	Host    string // for the readiness line; "" = localhost
	Pattern string
}
