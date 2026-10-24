// Package jobspec is the contract between the build controller and the workers: the target
// selection a JobSpec carries, the pipeline matcher (SPEC §6.9.4 pipeline.Match) and the
// evaluation report a job stages. It imports nothing of the orchestrator, so workers use it.
package jobspec

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// Pipeline is a pipeline as the graph IR carries it (pkg/bob pipelineJSON, SPEC §1.4.4):
// triggers in human form ("push <glob>", "pull_request", "tag <glob>").
type Pipeline struct {
	Name        string   `json:"name"`
	Triggers    []string `json:"triggers"`
	Concurrency string   `json:"concurrency"` // "" | queue | supersede | parallel
	Targets     []string `json:"targets"`
	Tests       []string `json:"tests"`
	Releases    []string `json:"releases"`
}

// Event kinds of a Selection.
const (
	EventPush        = "push"
	EventTag         = "tag"
	EventPullRequest = "pull_request"
)

// Selection says what a build runs; it travels in JobSpec.Targets as selector entries next to
// plain labels (SPEC §05.4.3 has no other field for it). Labels start with "//" or a module
// path, never with "@", so the selectors cannot be mistaken for labels:
//
//	@event:push:<branch>   @event:tag:<name>   @event:pull_request   (forge triggers)
//	@pipeline:<name>       (run these pipelines regardless of their triggers)
//	@test                  (Labels are tests)
//	@variant:<name>        @upload:<sha256 hex>
//	@version:<v>           (release version: the tag without "v", or bob build --remote --version)
type Selection struct {
	Event     string   // push | tag | pull_request; "" = no forge event
	Ref       string   // branch name (push) or tag name (tag)
	Pipelines []string // explicit pipelines
	Labels    []string // plain targets (no pipeline runs)
	Test      bool
	Variants  []string
	Upload    string // sha256 of the uploaded source snapshot ("" = git source)
	Version   string // release version the build passes to its releases (R-OUT-022); "" = none
}

// MaxTargets bounds JobSpec.Targets (SPEC §05.4.3).
const MaxTargets = 64

// Encode returns the JobSpec.Targets form of s.
func (s Selection) Encode() ([]string, error) {
	var out []string
	if s.Event != "" {
		switch s.Event {
		case EventPush, EventTag:
			out = append(out, "@event:"+s.Event+":"+s.Ref)
		case EventPullRequest:
			out = append(out, "@event:"+s.Event)
		default:
			return nil, fmt.Errorf("selection: unknown event %q", s.Event)
		}
	}
	for _, p := range s.Pipelines {
		out = append(out, "@pipeline:"+p)
	}
	if s.Test {
		out = append(out, "@test")
	}
	for _, v := range s.Variants {
		out = append(out, "@variant:"+v)
	}
	if s.Upload != "" {
		out = append(out, "@upload:"+s.Upload)
	}
	if s.Version != "" {
		if !ValidVersion(s.Version) {
			return nil, fmt.Errorf("selection: invalid release version %q", s.Version)
		}
		out = append(out, "@version:"+s.Version)
	}
	for _, l := range s.Labels {
		if strings.HasPrefix(l, "@") || l == "" {
			return nil, fmt.Errorf("selection: %q is not a label", l)
		}
		out = append(out, l)
	}
	if len(out) > MaxTargets {
		return nil, fmt.Errorf("selection: %d targets, the limit is %d", len(out), MaxTargets)
	}
	return out, nil
}

var (
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// versionRE is the release version syntax of R-OUT-023 (pkg/engine/build.CheckVersion).
	versionRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
)

// ValidVersion reports whether v is a release version bob builds with (R-OUT-023).
func ValidVersion(v string) bool { return versionRE.MatchString(v) }

// TagVersion is the release version of a git tag: the tag without a leading "v" ("v1.4.2" →
// "1.4.2", SPEC §6.8), or "" when that is not a valid release version.
func TagVersion(tag string) string {
	v := strings.TrimPrefix(tag, "v")
	if !ValidVersion(v) {
		return ""
	}
	return v
}

// ParseSelection reads JobSpec.Targets (hostile only in the sense of a forged spec, which the
// worker refuses by signature before; still validated).
func ParseSelection(targets []string) (Selection, error) {
	var s Selection
	if len(targets) > MaxTargets {
		return s, fmt.Errorf("selection: %d targets, the limit is %d", len(targets), MaxTargets)
	}
	for _, t := range targets {
		switch {
		case t == "@test":
			s.Test = true
		case strings.HasPrefix(t, "@event:"):
			kind, ref, _ := strings.Cut(strings.TrimPrefix(t, "@event:"), ":")
			if s.Event != "" {
				return s, errors.New("selection: two events")
			}
			switch kind {
			case EventPush, EventTag:
				if ref == "" {
					return s, fmt.Errorf("selection: %s event without a ref", kind)
				}
			case EventPullRequest:
			default:
				return s, fmt.Errorf("selection: unknown event %q", kind)
			}
			s.Event, s.Ref = kind, ref
		case strings.HasPrefix(t, "@pipeline:"):
			name := strings.TrimPrefix(t, "@pipeline:")
			if !pipelineName.MatchString(name) {
				return s, fmt.Errorf("selection: pipeline name %q", name)
			}
			s.Pipelines = append(s.Pipelines, name)
		case strings.HasPrefix(t, "@variant:"):
			s.Variants = append(s.Variants, strings.TrimPrefix(t, "@variant:"))
		case strings.HasPrefix(t, "@upload:"):
			h := strings.TrimPrefix(t, "@upload:")
			if !sha256Hex.MatchString(h) {
				return s, fmt.Errorf("selection: upload %q is not a sha256", h)
			}
			s.Upload = h
		case strings.HasPrefix(t, "@version:"):
			v := strings.TrimPrefix(t, "@version:")
			if !ValidVersion(v) || s.Version != "" {
				return s, fmt.Errorf("selection: release version %q", v)
			}
			s.Version = v
		case strings.HasPrefix(t, "@"), t == "":
			return s, fmt.Errorf("selection: unknown selector %q", t)
		default:
			s.Labels = append(s.Labels, t)
		}
	}
	return s, nil
}

// RunsPipelines reports whether the selection is pipeline-based (forge event or explicit
// pipelines) rather than plain targets.
func (s Selection) RunsPipelines() bool { return s.Event != "" || len(s.Pipelines) > 0 }

var pipelineName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// ErrNoPipeline is returned by Match for an explicit pipeline the repository does not define.
var ErrNoPipeline = errors.New("pipeline not defined")

// Match is the pipeline matcher of SPEC §6.9.4 (`pipeline.Match`): the names, sorted, of the
// pipelines s runs. Explicit pipelines run regardless of their triggers; a forge event runs
// every pipeline with a matching trigger; plain targets run none. The worker runs it after
// evaluation and the orchestrator runs it again on the reported definitions.
func Match(ps []Pipeline, s Selection) ([]string, error) {
	var out []string
	if len(s.Pipelines) > 0 {
		for _, name := range s.Pipelines {
			if !slices.ContainsFunc(ps, func(p Pipeline) bool { return p.Name == name }) {
				return nil, fmt.Errorf("%w: %s", ErrNoPipeline, name)
			}
			out = append(out, name)
		}
	} else if s.Event != "" {
		for _, p := range ps {
			if slices.ContainsFunc(p.Triggers, func(t string) bool { return triggerMatches(t, s) }) {
				out = append(out, p.Name)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// triggerMatches matches one trigger in human form against the event of s.
func triggerMatches(trigger string, s Selection) bool {
	kind, glob, _ := strings.Cut(trigger, " ")
	switch kind {
	case "push":
		return s.Event == EventPush && Glob(glob, s.Ref)
	case "tag":
		return s.Event == EventTag && Glob(glob, s.Ref)
	case "pull_request":
		return s.Event == EventPullRequest
	}
	return false
}

// Glob matches name against pattern: "*" matches any run of characters (including "/"),
// "?" any one character, everything else itself.
func Glob(pattern, name string) bool {
	p, n := []rune(pattern), []rune(name)
	pi, ni, star, mark := 0, 0, -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == n[ni]) && p[pi] != '*':
			pi++
			ni++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ni
			pi++
		case star >= 0:
			pi, mark = star+1, mark+1
			ni = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// Targets returns the union of the targets and of the tests of the named pipelines, in
// pipeline order, without duplicates.
func Targets(ps []Pipeline, names []string) (targets, tests []string) {
	for _, p := range ps {
		if !slices.Contains(names, p.Name) {
			continue
		}
		for _, t := range p.Targets {
			if !slices.Contains(targets, t) {
				targets = append(targets, t)
			}
		}
		for _, t := range p.Tests {
			if !slices.Contains(tests, t) {
				tests = append(tests, t)
			}
		}
	}
	return targets, tests
}

// UploadKey is the artifacts key of a source snapshot (SPEC §6.9.7).
func UploadKey(project, sha string) string { return "src/" + project + "/" + sha + ".tar.zst" }

// HostPlatform is the Nix system of this host.
func HostPlatform() string {
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64-linux"
	case "amd64":
		return "x86_64-linux"
	}
	return runtime.GOARCH + "-linux"
}

// PipelineName is the syntax of pipeline names (pkg/bob).
func PipelineName(s string) bool { return pipelineName.MatchString(s) }

// IsSHA256 reports a lower-case hex SHA-256.
func IsSHA256(s string) bool { return sha256Hex.MatchString(s) }
