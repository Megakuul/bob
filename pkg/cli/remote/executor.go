package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/engine"
)

// Methods of bob.build.v1.BuildService the CLI calls.
const (
	CreateBuildMethod = "bob.build.v1.BuildService/CreateBuild"
	WatchBuildMethod  = "bob.build.v1.BuildService/WatchBuild"
	CancelBuildMethod = "bob.build.v1.BuildService/CancelBuild"
)

// Event names of Executor.Build. Data is the JSON of the struct of the same name.
const (
	EvSubmitted = "remote.submitted" // Submitted: the build exists on the server
	EvState     = "remote.state"     // State: the build changed state
	EvPackage   = "remote.package"   // Package: a package changed state
	EvDone      = "remote.done"      // Done: always the last event
)

// Submitted is the data of EvSubmitted.
type Submitted struct {
	Server  string `json:"server"`
	BuildID string `json:"buildId"`
	URL     string `json:"url"`
}

// State is the data of EvState; State is the §05 build state name (pending, queued, running,
// succeeded, failed, cancelled, superseded, skipped).
type State struct {
	BuildID string `json:"buildId"`
	State   string `json:"state"`
}

// Package is the data of EvPackage; State is the PackageState suffix in lower case (pending,
// queued, building, cached, succeeded, failed, skipped, cancelled).
type Package struct {
	Label    string        `json:"label"`
	Platform string        `json:"platform,omitempty"`
	State    string        `json:"state"`
	Outputs  []string      `json:"outputs,omitempty"`
	Error    *ErrorSummary `json:"error,omitempty"`
}

// Done is the data of EvDone.
type Done struct {
	BuildID string         `json:"buildId"`
	State   string         `json:"state"` // final build state; "" when the watch ended early
	Errors  []ErrorSummary `json:"errors,omitempty"`
	// Downloaded are the output paths made valid locally (SUCCEEDED builds with Fetch).
	Downloaded []string `json:"downloaded,omitempty"`
	Detached   bool     `json:"detached,omitempty"` // the context ended first (Ctrl-C)
	// Failure describes why the watch or the download failed (State is then "" or the
	// download did not happen).
	Failure *Failure `json:"failure,omitempty"`
}

// Failure is a failed step after the build was created.
type Failure struct {
	Step        string `json:"step"` // watch | download
	Code        string `json:"code,omitempty"`
	Message     string `json:"message"`
	Unreachable bool   `json:"unreachable,omitempty"`
}

// ErrorSummary is bob.build.v1.ErrorSummary.
type ErrorSummary struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Package *struct {
		Label string `json:"label"`
	} `json:"package,omitempty"`
}

// Label is the package label of the error, or "".
func (e ErrorSummary) Label() string {
	if e.Package == nil {
		return ""
	}
	return e.Package.Label
}

// Build is the part of bob.build.v1.Build the CLI reads.
type Build struct {
	ID        string         `json:"id"`
	ProjectID string         `json:"projectId"`
	Number    Int64          `json:"number"`
	State     string         `json:"state"`
	Errors    []ErrorSummary `json:"errors"`
	WebURL    string         `json:"webUrl"`
}

type packageNode struct {
	Package struct {
		Label    string `json:"label"`
		Platform string `json:"platform"`
	} `json:"package"`
	State   string        `json:"state"`
	Outputs []string      `json:"outputs"`
	Error   *ErrorSummary `json:"error"`
}

type watchResponse struct {
	Build        Build         `json:"build"`
	ChangedNodes []int32       `json:"changedNodes"`
	Nodes        []packageNode `json:"nodes"`
}

// StateName turns "BUILD_STATE_RUNNING" / "PACKAGE_STATE_BUILDING" into "running" / "building".
func StateName(s string) string {
	for _, p := range []string{"BUILD_STATE_", "PACKAGE_STATE_"} {
		s = strings.TrimPrefix(s, p)
	}
	return strings.ToLower(s)
}

// Terminal reports whether a build state (StateName form) is final.
func Terminal(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled", "superseded", "skipped":
		return true
	}
	return false
}

// Executor runs builds on a bob server (engine.RemoteExecutor, SPEC §02.6): it uploads the
// source snapshot, creates the build, follows it and, for a successful build, makes the
// outputs of the requested packages valid locally through Fetch.
type Executor struct {
	Server  *Server
	Project string
	// Fetch substitutes store paths from the server into the local store (negative narinfo
	// TTL 0). nil: no download (--no-download).
	Fetch func(ctx context.Context, paths []string) error
	// Reconnect is how long a broken watch stream is retried (default 2 min): orchestrators
	// may restart while a build runs.
	Reconnect time.Duration
}

var _ engine.RemoteExecutor = (*Executor)(nil)

// Build implements engine.RemoteExecutor. Errors before the build exists on the server are
// returned directly; afterwards everything arrives as events, EvDone last.
func (x *Executor) Build(ctx context.Context, req engine.RemoteRequest) (<-chan engine.Event, error) {
	snap, err := PackSource(ctx, req.Root)
	if err != nil {
		return nil, err
	}
	defer snap.Remove()
	if err := x.Server.Upload(ctx, x.Project, snap); err != nil {
		return nil, err
	}
	platforms := make([]string, 0, len(req.Platforms))
	for _, p := range req.Platforms {
		platforms = append(platforms, ServerPlatform(p))
	}
	reason := "bob build --remote"
	if req.Test {
		reason = "bob test --remote"
	}
	create := map[string]any{
		"projectId": x.Project,
		"source":    map[string]string{"uploadSha256": snap.SHA256},
		"targets":   req.Patterns,
		"platforms": platforms,
		"variants":  req.Variants,
		"test":      req.Test,
		"reason":    reason,
	}
	var resp struct {
		Build Build `json:"build"`
	}
	if err := x.Server.Call(ctx, CreateBuildMethod, create, &resp); err != nil {
		return nil, err
	}
	b := resp.Build
	if b.ID == "" {
		return nil, &Error{Method: CreateBuildMethod, Code: "internal", Message: "the server returned no build id"}
	}
	ch := make(chan engine.Event, 64)
	go func() {
		defer close(ch)
		defer func() {
			if p := recover(); p != nil {
				send(ch, EvDone, Done{BuildID: b.ID, Failure: &Failure{Step: "watch", Message: fmt.Sprint("internal error: ", p)}})
			}
		}()
		web := b.WebURL
		if web == "" {
			web = x.Server.URL + "/p/" + x.Project + "/builds/" + b.ID
		}
		send(ch, EvSubmitted, Submitted{Server: x.Server.Host(), BuildID: b.ID, URL: web})
		send(ch, EvDone, x.follow(ctx, ch, b, req))
	}()
	return ch, nil
}

func send(ch chan<- engine.Event, name string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	ch <- engine.Event{Name: name, Data: b}
}

// follow watches the build until it is terminal and downloads its outputs.
func (x *Executor) follow(ctx context.Context, ch chan<- engine.Event, b Build, req engine.RemoteRequest) Done {
	done := Done{BuildID: b.ID}
	state := ""
	nodes := map[int]packageNode{}
	window := x.Reconnect
	if window <= 0 {
		window = 2 * time.Minute
	}
	var final Build
	lastOK := time.Now()
	for {
		err := x.Server.Stream(ctx, WatchBuildMethod, map[string]string{"id": b.ID}, func(m json.RawMessage) error {
			var w watchResponse
			if err := json.Unmarshal(m, &w); err != nil {
				return &Error{Method: WatchBuildMethod, Code: "internal", Message: "invalid message: " + err.Error()}
			}
			lastOK = time.Now()
			for i, idx := range w.ChangedNodes {
				if i >= len(w.Nodes) {
					break
				}
				n := w.Nodes[i]
				if old, ok := nodes[int(idx)]; ok && old.State == n.State {
					continue
				}
				nodes[int(idx)] = n
				send(ch, EvPackage, Package{Label: n.Package.Label, Platform: n.Package.Platform,
					State: StateName(n.State), Outputs: n.Outputs, Error: n.Error})
			}
			if s := StateName(w.Build.State); s != "" && s != state {
				state = s
				send(ch, EvState, State{BuildID: b.ID, State: s})
			}
			final = w.Build
			return nil
		})
		if Terminal(state) {
			break
		}
		if ctx.Err() != nil {
			done.Detached = true
			return done
		}
		e, ok := AsError(err)
		retry := err == nil || (ok && e.Unreachable())
		if !retry || time.Since(lastOK) > window {
			f := &Failure{Step: "watch", Message: "the server stopped reporting the build"}
			if err != nil {
				f.Message = err.Error()
			}
			if ok {
				f.Code, f.Unreachable = e.Code, e.Unreachable()
			}
			done.Failure = f
			return done
		}
		select {
		case <-ctx.Done():
			done.Detached = true
			return done
		case <-time.After(2 * time.Second):
		}
	}
	done.State, done.Errors = state, final.Errors
	if state != "succeeded" || x.Fetch == nil {
		return done
	}
	paths := outputsOf(nodes, req.Patterns)
	if len(paths) == 0 {
		return done
	}
	if err := x.Fetch(ctx, paths); err != nil {
		if ctx.Err() != nil {
			done.Detached = true
			return done
		}
		done.Failure = &Failure{Step: "download", Message: err.Error()}
		return done
	}
	done.Downloaded = paths
	return done
}

// outputsOf returns the outputs of the successful packages the patterns name; when no package
// matches a pattern (a form this client does not understand), every package's outputs.
func outputsOf(nodes map[int]packageNode, patterns []string) []string {
	var picked, all []string
	for _, n := range nodes {
		st := StateName(n.State)
		if st != "succeeded" && st != "cached" {
			continue
		}
		all = append(all, n.Outputs...)
		if MatchLabel(patterns, n.Package.Label) {
			picked = append(picked, n.Outputs...)
		}
	}
	if len(picked) == 0 {
		picked = all
	}
	slices.Sort(picked)
	return slices.Compact(picked)
}

// MatchLabel reports whether label ("//dir:name") is named by one of the patterns: "//...",
// "//dir/...", "//dir:name", "//dir" (= //dir:<last element>), with an optional
// "@platform+variant" suffix.
func MatchLabel(patterns []string, label string) bool {
	if len(patterns) == 0 {
		return true
	}
	dir, _, _ := strings.Cut(label, ":")
	for _, p := range patterns {
		p, _, _ = strings.Cut(p, "@")
		switch {
		case p == "//...":
			return true
		case strings.HasSuffix(p, "/..."):
			pre := strings.TrimSuffix(p, "/...")
			if dir == pre || strings.HasPrefix(dir, pre+"/") {
				return true
			}
		case strings.Contains(p, ":"):
			if p == label {
				return true
			}
		default:
			last := p[strings.LastIndex(p, "/")+1:]
			if p+":"+last == label {
				return true
			}
		}
	}
	return false
}

// ServerPlatform turns a bob platform ("linux-amd64", "native") into the server's form.
func ServerPlatform(p bob.Platform) string {
	switch s := string(p); s {
	case "", "native":
		return "linux/" + runtime.GOARCH
	default:
		return strings.Replace(s, "-", "/", 1)
	}
}
