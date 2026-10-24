package build

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/eval"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
)

// rootEco is a test ecosystem (kinds rooteco.*) whose Native writes rooted.txt only when it
// is given the repository root, like cc's compile_commands.json.
type rootEco struct{}

func init() { engine.Register(rootEco{}) }

func (rootEco) Name() string { return "rooteco" }
func (rootEco) Lock(context.Context, engine.LockRequest) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (rootEco) Plan(context.Context, engine.PlanRequest) (engine.PlanResult, error) {
	return engine.PlanResult{}, nil
}
func (rootEco) Native(_ context.Context, req engine.PlanRequest) ([]engine.GeneratedFile, error) {
	if req.Root == "" {
		return nil, nil
	}
	return []engine.GeneratedFile{{Path: "rooted.txt", Content: []byte(req.Root + "\n"), Owner: "//:r"}}, nil
}

// I-M7a NEEDS 1: Gen and Check pass the repository root to Native.
func TestNativeGetsRoot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	g := &ir.Graph{IRVersion: ir.Version, Nodes: []*ir.Node{{ID: "r", Kind: "rooteco.thing", Label: "//:r"}}}
	lf := &lock.File{LockVersion: lock.Version}

	files, err := nativeFilesAt(ctx, root, g, lf, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || string(files[0].Content) != root+"\n" {
		t.Fatalf("nativeFilesAt with a root: %+v", files)
	}
	if _, err := generate(ctx, root, g, lf, nil, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st := readNativeState(root); st.Files["rooted.txt"].Owner != "//:r" {
		t.Fatalf("generate did not write rooted.txt: %+v", st)
	}
}

// I-M5b NEEDS 2: the root go.sum carries the framework's go.sum lines next to the root go.mod.
func TestRootGoSumHasFramework(t *testing.T) {
	g := &ir.Graph{IRVersion: ir.Version, Module: "example.com/app", BobVersion: eval.BobVersion()}
	files, err := withRootGoMod(nil, g, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want, err := eval.FrameworkSum()
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
		if f.Path == "go.sum" && string(f.Content) != want {
			t.Errorf("go.sum:\n%s\nwant:\n%s", f.Content, want)
		}
	}
	if len(paths) != 2 || paths[0] != "go.mod" || paths[1] != "go.sum" {
		t.Errorf("root files %v, want go.mod and go.sum", paths)
	}
}
