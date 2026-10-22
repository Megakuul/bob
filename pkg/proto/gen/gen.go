// Package gen implements engine.Ecosystem "proto" (SPEC §03.6): protobuf libraries resolve to
// their imported .proto files (the code generators read them), checks (proto.Lint,
// proto.Breaking) are test units running buf.
package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/srcs"
	"github.com/megakuul/bob/pkg/proto/internal/protospec"
)

func init() { engine.Register(Ecosystem{}) }

// Ecosystem plans protobuf libraries and checks. It has no lock section and no native files.
type Ecosystem struct{}

// Name is the kind prefix "proto".
func (Ecosystem) Name() string { return "proto" }

// Lock has nothing to resolve.
func (Ecosystem) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) { return nil, nil }

// Native writes no files.
func (Ecosystem) Native(context.Context, engine.PlanRequest) ([]engine.GeneratedFile, error) {
	return nil, nil
}

// buildSrc is where checks lay out the .proto files at their repository paths (so buf names
// repository files in its messages); against is the Breaking baseline.
const (
	buildSrc = "/build/src"
	against  = "/build/against"
)

// testRunArgs is the job of kind "test-run" (pkg/builder/bspec.TestRunArgs).
type testRunArgs struct {
	Argv     []string          `json:"argv"`
	Path     []string          `json:"path,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Copy     []copyArgs        `json:"copy,omitempty"`
	TimeoutS int64             `json:"timeoutSeconds,omitempty"`
}

type copyArgs struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Plan resolves libraries to their source paths and plans one test-run job per check.
func (Ecosystem) Plan(ctx context.Context, req engine.PlanRequest) (res engine.PlanResult, err error) {
	defer func() { // a planner bug must not crash bob
		if v := recover(); v != nil {
			err = fmt.Errorf("proto planner: internal error: %v", v)
		}
	}()
	byID := map[string]*ir.Node{}
	if req.Graph != nil {
		for _, n := range req.Graph.Nodes {
			byID[n.ID] = n
		}
	}
	jobs := engine.NewJobs(req)
	res.Nodes = map[string]engine.Resolved{}
	var errs []error
	for _, n := range req.Nodes {
		switch n.Kind {
		case protospec.KindLibrary:
			src, ok := req.Sources[n.ID]
			if !ok {
				errs = append(errs, fmt.Errorf("%s: its .proto files are not imported", n.Label))
				continue
			}
			res.Nodes[n.ID] = engine.Resolved{Outputs: map[string]string{"out": src}}
		case protospec.KindCheck:
			rv, err := planCheck(ctx, req, jobs, byID, n)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			res.Nodes[n.ID] = rv
		default:
			errs = append(errs, fmt.Errorf("%s: unknown proto kind %s", n.Label, n.Kind))
		}
	}
	if len(errs) > 0 {
		return engine.PlanResult{}, errors.Join(errs...)
	}
	res.Drvs = jobs.Drvs
	return res, nil
}

// LibSpec is the spec of a proto.library node.
type LibSpec = protospec.Library

// Lib is a library node with its spec.
type Lib struct {
	Node *ir.Node
	Spec LibSpec
}

// Closure returns the library id and every library it imports, dependencies first, each once.
func Closure(byID map[string]*ir.Node, id string) ([]Lib, error) {
	seen := map[string]bool{}
	var out []Lib
	var walk func(id string) error
	walk = func(id string) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		n := byID[id]
		if n == nil || n.Kind != protospec.KindLibrary {
			return fmt.Errorf("%s is not a protobuf library", id)
		}
		var s protospec.Library
		if err := json.Unmarshal(n.Spec, &s); err != nil {
			return fmt.Errorf("%s: library spec: %w", n.Label, err)
		}
		for _, d := range n.Deps {
			if err := walk(d); err != nil {
				return err
			}
		}
		out = append(out, Lib{Node: n, Spec: s})
		return nil
	}
	return out, walk(id)
}

// BufConfig is the buf.yaml (v2, as JSON) of a library with import root root.
func BufConfig(root string) string {
	if root == "" {
		root = "."
	}
	b, _ := json.Marshal(map[string]any{"version": "v2", "modules": []any{map[string]any{"path": root}}})
	return string(b)
}

func planCheck(ctx context.Context, req engine.PlanRequest, jobs *engine.Jobs, byID map[string]*ir.Node, n *ir.Node) (engine.Resolved, error) {
	var s protospec.Check
	if err := json.Unmarshal(n.Spec, &s); err != nil {
		return engine.Resolved{}, fmt.Errorf("%s: check spec: %w", n.Label, err)
	}
	if len(n.Deps) != 2 {
		return engine.Resolved{}, fmt.Errorf("%s: a check has a library and buf", n.Label)
	}
	libs, err := Closure(byID, n.Deps[0])
	if err != nil {
		return engine.Resolved{}, fmt.Errorf("%s: %w", n.Label, err)
	}
	lib := libs[len(libs)-1]
	a := testRunArgs{Cwd: buildSrc}
	in := map[string][]string{}
	var srcPaths []string
	add := func(rv engine.Resolved, o string) string {
		if rv.Drv == "" {
			srcPaths = append(srcPaths, rv.Outputs[o])
		} else {
			in[rv.Drv] = append(in[rv.Drv], o)
		}
		return rv.Outputs[o]
	}
	for _, l := range libs {
		rv, ok := req.Inputs[l.Node.ID]
		if !ok {
			return engine.Resolved{}, fmt.Errorf("%s: library %s is not planned", n.Label, l.Node.Label)
		}
		a.Copy = append(a.Copy, copyArgs{From: add(rv, "out"), To: buildSrc})
	}
	buf, ok := req.Inputs[n.Deps[1]]
	if !ok || buf.Outputs["out"] == "" {
		return engine.Resolved{}, fmt.Errorf("%s: buf is not planned", n.Label)
	}
	a.Path = []string{add(buf, "out") + "/bin"}
	cfg := BufConfig(lib.Spec.Root)
	dir := lib.Spec.Dir
	if dir == "" {
		dir = "."
	}
	switch s.Mode {
	case protospec.Lint:
		a.Argv = []string{"buf", "lint", "--config", cfg, "--path", dir}
	case protospec.Breaking:
		base, err := baseline(ctx, req, n, libs, s.Against)
		if err != nil {
			return engine.Resolved{}, err
		}
		srcPaths = append(srcPaths, base)
		a.Copy = append(a.Copy, copyArgs{From: base, To: against})
		// buf applies --path to the against input in a way that targets nothing there (buf
		// 1.73): the imported libraries are excluded instead, so only lib's files are compared.
		a.Argv = []string{"buf", "breaking", "--config", cfg}
		for _, l := range libs[:len(libs)-1] {
			if l.Spec.Dir != lib.Spec.Dir {
				a.Argv = append(a.Argv, "--exclude-path", l.Spec.Dir)
			}
		}
		a.Argv = append(a.Argv, "--against", against, "--against-config", cfg)
	default:
		return engine.Resolved{}, fmt.Errorf("%s: unknown check mode %q", n.Label, s.Mode)
	}
	_, name, _ := strings.Cut(n.Label, ":")
	dp, out, err := jobs.Add(engine.Job{Name: drv.Name("proto", name, strings.ReplaceAll(lib.Spec.Dir, "/", "-")), Kind: "test-run",
		Label: n.Label, Args: a, Drvs: in, Srcs: srcPaths})
	if err != nil {
		return engine.Resolved{}, err
	}
	return engine.Resolved{Drv: dp, Outputs: map[string]string{"out": out}}, nil
}

// Bounds of the Breaking baseline read from git.
const (
	maxBaseFiles = 2000
	maxBaseBytes = 64 << 20
)

// baseline imports the .proto files of libs' directories at git revision rev of the
// repository as a source path (laid out at their repository paths).
func baseline(ctx context.Context, req engine.PlanRequest, n *ir.Node, libs []Lib, rev string) (string, error) {
	invalid := func(reason string) error {
		e := diag.New(1020, "label", n.Label, "option", "against revision", "reason", reason)
		e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
		return e
	}
	if req.Root == "" || req.Store == nil {
		return "", invalid("proto.Breaking needs the repository's working tree")
	}
	dirs := map[string]bool{}
	var args []string
	for _, l := range libs {
		d := l.Spec.Dir
		if d == "" {
			d = "."
		}
		dirs[l.Spec.Dir] = true
		args = append(args, d)
	}
	sort.Strings(args)
	git := func(stdout *bytes.Buffer, a ...string) error {
		c := exec.CommandContext(ctx, "git", append([]string{"-C", req.Root, "-c", "core.fsmonitor=false"}, a...)...)
		c.Stdout = stdout
		var stderr bytes.Buffer
		c.Stderr = &stderr
		if err := c.Run(); err != nil {
			return fmt.Errorf("git %s: %v: %s", a[0], err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
	var list bytes.Buffer
	if err := git(&list, append([]string{"ls-tree", "-r", "-z", "--name-only", "--end-of-options", rev, "--"}, args...)...); err != nil {
		return "", invalid(fmt.Sprintf("git revision %q cannot be read (%v)", rev, err))
	}
	tmp, err := os.MkdirTemp("", "bob-proto-base-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	var files []string
	total := 0
	for _, name := range strings.Split(list.String(), "\x00") {
		if name == "" || !strings.HasSuffix(name, ".proto") || !dirs[dirOf(name)] {
			continue
		}
		if c := path.Clean(name); c != name || strings.HasPrefix(c, "../") || path.IsAbs(c) {
			continue
		}
		if len(files) >= maxBaseFiles {
			return "", invalid(fmt.Sprintf("more than %d .proto files at %s", maxBaseFiles, rev))
		}
		var blob bytes.Buffer
		if err := git(&blob, "cat-file", "blob", rev+":"+name); err != nil {
			return "", invalid(fmt.Sprintf("%s at %s cannot be read (%v)", name, rev, err))
		}
		if total += blob.Len(); total > maxBaseBytes {
			return "", invalid(fmt.Sprintf("the .proto files at %s exceed %d bytes", rev, maxBaseBytes))
		}
		dst := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, blob.Bytes(), 0o644); err != nil {
			return "", err
		}
		files = append(files, name)
	}
	sort.Strings(files)
	paths, err := req.Store.ImportSources(ctx, []srcs.Source{{Name: "proto-baseline", Tree: srcs.Tree{Root: tmp, Files: files}}})
	if err != nil {
		return "", err
	}
	if len(paths) != 1 {
		return "", fmt.Errorf("%s: source import returned %d paths", n.Label, len(paths))
	}
	return paths[0], nil
}

func dirOf(name string) string {
	if d := path.Dir(name); d != "." {
		return d
	}
	return ""
}
