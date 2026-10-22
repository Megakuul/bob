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

// PipelineOpts is typed; the zero value is valid and means a manual-only pipeline. It maps 1:1
// onto proto `Pipeline` (SPEC §6.5.6): triggers (human form), concurrency, releases.
type PipelineOpts struct {
	Triggers    []Trigger   // none = manual runs only
	Concurrency Concurrency // unset = the per-ref default of SPEC §05.6.2
	Targets     []Pattern   // packages to build
	Tests       []Pattern   // packages to test
	Releases    []string    // release names to publish; non-empty = release pipeline (proto releases=true)
}

// Pattern is a label pattern ("//...", "//dir/...", "//dir:all", a label, "//dir"); a string type
// so that fixtures can write []bob.Pattern{"//..."}.
type Pattern string

// Trigger says when a pipeline starts.
type Trigger struct{ human string }

// OnPush triggers on a push to a branch matching branchGlob (human form "push <glob>").
func OnPush(branchGlob string) Trigger { return Trigger{human: "push " + branchGlob} }

// OnPullRequest triggers on pull requests (human form "pull_request").
func OnPullRequest() Trigger { return Trigger{human: "pull_request"} }

// OnTag triggers on a tag matching glob (human form "tag <glob>").
func OnTag(glob string) Trigger { return Trigger{human: "tag " + glob} }

// String returns the human form stored in the proto Pipeline.
func (t Trigger) String() string { return t.human }

// Concurrency says what happens to a run of the same pipeline and ref while one is active.
type Concurrency int

// The zero value of Concurrency is the per-ref default of SPEC §05.6.2.
const (
	Queue Concurrency = iota + 1
	Supersede
	Parallel
)

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
