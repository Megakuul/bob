package bob

// Repo declares repository-wide settings. It is allowed only in the root bob.go, at most once.
//
//	var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})
func Repo(o RepoOpts) struct{} { return struct{}{} }

// RepoOpts are the repository-wide settings.
type RepoOpts struct {
	// Nixpkgs is a channel name ("nixos-26.05") or "<channel>@<40-hex rev>". bob.lock pins the
	// revision; `bob lock --update nixpkgs` moves it to the channel's current revision.
	Nixpkgs  string
	Variants []Named // see SPEC §1.9
}

// Pipeline registers a pipeline (R-FW-040). Triggers and steps are defined by SPEC §06.
func Pipeline(name string, o PipelineOpts) struct{} { return struct{}{} }

// PipelineOpts configures a pipeline; its fields are SPEC §06's.
type PipelineOpts struct{}

// Import requires another bob repository as a Go module (R-FW-022); its bob.go packages are
// then imported as Go packages and its labels are "<module>//dir:name".
//
//	var _ = bob.Import("example.com/other", "v1.2.0")
func Import(module, version string) struct{} { return struct{}{} }

// DevShell declares one development environment for bob dev, bob env, bob exec and the
// shell hook (SPEC §02.7.0).
type DevShell struct {
	Name     string            // "default" when empty; [a-z0-9-]{1,32}
	Packages []Tool            // top-level tools/toolchains; their bin/ go on PATH in this order
	Env      map[string]string // extra variables
	Hooks    []string          // shell commands run by bob dev before the shell starts
	Servers  []Package         // dev-server packages started by bob dev
}

// NewShell validates s and returns it as a graph node (kind "bob.devshell").
func NewShell(p *P, s DevShell) *Shell { return nil }

// Shell is the graph node of a DevShell.
type Shell struct{ Node }
