package bob

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"sync"
)

type repoDecl struct {
	pos  Position
	dir  string
	mod  string
	opts RepoOpts
}

type pipelineDecl struct {
	pos  Position
	name string
	opts PipelineOpts
}

// Repo declares repository-wide settings. It is allowed only in the root bob.go, at most once.
//
//	var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})
func Repo(o RepoOpts) struct{} {
	s := bobSite()
	reg.repos = append(reg.repos, repoDecl{pos: s.pos, dir: s.dir, mod: s.mod, opts: o})
	return struct{}{}
}

// RepoOpts are the repository-wide settings.
type RepoOpts struct {
	// Nixpkgs is a channel name ("nixos-26.05") or "<channel>@<40-hex rev>". bob.lock pins the
	// revision; `bob lock --update nixpkgs` moves it to the channel's current revision.
	Nixpkgs  string
	Variants []Named // see SPEC §1.9
}

// checkRepo validates the Repo declarations of the main module and returns the effective one
// and its named variants.
func checkRepo(r *registry) (RepoOpts, map[string]Named, []diagnostic) {
	var errs []diagnostic
	var own []repoDecl
	for _, d := range r.repos {
		if d.mod == "" {
			own = append(own, d)
		}
	}
	var opts RepoOpts
	for i, d := range own {
		switch {
		case d.dir != "":
			errs = append(errs, diagnostic{pos: d.pos, msg: fmt.Sprintf("%s: bob.Repo is allowed only in the root bob.go", d.pos)})
		case i > 0:
			errs = append(errs, diagnostic{pos: d.pos, msg: fmt.Sprintf("%s: bob.Repo is already declared at %s", d.pos, own[0].pos)})
		default:
			opts = d.opts
		}
	}
	named := map[string]Named{}
	for _, n := range opts.Variants {
		switch {
		case checkName(n.name) != nil:
			errs = append(errs, diagnostic{msg: fmt.Sprintf("bob.Repo: invalid variant name %q (lowercase letters, digits, '_', '-', '.')", n.name)})
		case named[n.name].name != "":
			errs = append(errs, diagnostic{msg: fmt.Sprintf("bob.Repo: variant %q is declared twice", n.name)})
		default:
			named[n.name] = n
		}
	}
	return opts, named, errs
}

// Pipeline registers a pipeline (R-FW-040). Triggers and steps are defined by SPEC §06.
func Pipeline(name string, o PipelineOpts) struct{} {
	s := bobSite()
	if s.mod == "" {
		reg.pipelines = append(reg.pipelines, pipelineDecl{pos: s.pos, name: name, opts: o})
	}
	return struct{}{}
}

// PipelineOpts is typed; the zero value is valid and means a manual-only pipeline. It maps 1:1
// onto proto `Pipeline` (SPEC §6.5.6): triggers (human form), concurrency, releases.
type PipelineOpts struct {
	Triggers    []Trigger   // none = manual runs only
	Concurrency Concurrency // unset = the per-ref default of SPEC §05.6.2
	Targets     []Pattern   // packages to build
	Tests       []Pattern   // packages to test
	Releases    []string    // release names to publish; non-empty = release pipeline (proto releases=true)
}

var pipelineName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`) })

// pipelineJSON is the IR form of a pipeline (SPEC §06 consumes it).
type pipelineJSON struct {
	Name        string   `json:"name"`
	Triggers    []string `json:"triggers"`
	Concurrency string   `json:"concurrency"` // "" = per-ref default
	Targets     []string `json:"targets"`
	Tests       []string `json:"tests"`
	Releases    []string `json:"releases"`
}

func checkPipelines(r *registry) ([]pipelineJSON, []diagnostic) {
	var out []pipelineJSON
	var errs []diagnostic
	seen := map[string]Position{}
	for _, d := range r.pipelines {
		bad := func(format string, args ...any) {
			errs = append(errs, diagnostic{pos: d.pos, msg: fmt.Sprintf("%s: pipeline %q: ", d.pos, d.name) + fmt.Sprintf(format, args...)})
		}
		if !pipelineName().MatchString(d.name) {
			bad("the name must match [a-z0-9][a-z0-9_.-]{0,63}")
			continue
		}
		if at, dup := seen[d.name]; dup {
			bad("already declared at %s", at)
			continue
		}
		seen[d.name] = d.pos
		pj := pipelineJSON{Name: d.name, Triggers: []string{}, Targets: []string{}, Tests: []string{}, Releases: nonNil(slices.Clone(d.opts.Releases))}
		for _, t := range d.opts.Triggers {
			if t.human == "" {
				bad("a trigger was not made by bob.OnPush, bob.OnPullRequest or bob.OnTag")
				continue
			}
			pj.Triggers = append(pj.Triggers, t.human)
		}
		switch d.opts.Concurrency {
		case 0:
		case Queue:
			pj.Concurrency = "queue"
		case Supersede:
			pj.Concurrency = "supersede"
		case Parallel:
			pj.Concurrency = "parallel"
		default:
			bad("unknown Concurrency %d", d.opts.Concurrency)
		}
		for _, list := range []struct {
			in  []Pattern
			out *[]string
		}{{d.opts.Targets, &pj.Targets}, {d.opts.Tests, &pj.Tests}} {
			for _, p := range list.in {
				if err := p.Valid(); err != nil {
					bad("%v", err)
					continue
				}
				*list.out = append(*list.out, string(p))
			}
		}
		out = append(out, pj)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}

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

var shellName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[a-z0-9-]{1,32}$`) })

// shellSpec is the IR payload of kind bob.devshell; deps are the Packages (first Packages
// entries, PATH order) followed by the Servers.
type shellSpec struct {
	Packages int               `json:"packages"`
	Servers  int               `json:"servers"`
	Env      map[string]string `json:"env"`
	Hooks    []string          `json:"hooks"`
}

// NewShell validates s and returns it as a graph node (kind "bob.devshell").
func NewShell(p *P, s DevShell) *Shell {
	name := s.Name
	if name == "" {
		name = "default"
	}
	var problems []string
	if !shellName().MatchString(name) {
		problems = append(problems, fmt.Sprintf("Name %q must match [a-z0-9-]{1,32}", s.Name))
		name = "default"
	}
	deps := make([]Package, 0, len(s.Packages)+len(s.Servers))
	for _, t := range s.Packages {
		deps = append(deps, t)
	}
	deps = append(deps, s.Servers...)
	spec := shellSpec{Packages: len(s.Packages), Servers: len(s.Servers), Env: s.Env, Hooks: nonNil(s.Hooks)}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	sh := &Shell{Node: newNode(p, "bob.devshell", name, deps, nil, spec)}
	reportInvalid(&sh.Node, "DevShell", problems)
	return sh
}

// Shell is the graph node of a DevShell.
type Shell struct{ Node }
