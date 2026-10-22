package gen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/builder/bspec"
	"github.com/megakuul/bob/pkg/connect/internal/connspec"
	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/drv"
	"github.com/megakuul/bob/pkg/engine/fetch"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/srcs"
)

const (
	builder   = "/nix/store/0000000000000000000000000000000b-bob-builder"
	commonSrc = "/nix/store/cccccccccccccccccccccccccccccccc-source"
	greetSrc  = "/nix/store/gggggggggggggggggggggggggggggggg-source"
	casPath   = "/nix/store/zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-gen-svc-gen-proto-greet-v1"
)

// fakeStore keeps store paths below root; RealiseNow "builds" by writing one generated file.
type fakeStore struct {
	root     string
	fail     bool
	imported []srcs.Source
}

func (s *fakeStore) HostPath(p string) string { return filepath.Join(s.root, filepath.Base(p)) }
func (s *fakeStore) ImportSources(_ context.Context, trees []srcs.Source) ([]string, error) {
	s.imported = append(s.imported, trees...)
	if err := os.MkdirAll(s.HostPath(casPath), 0o755); err != nil {
		return nil, err
	}
	out := make([]string, len(trees))
	for i := range trees {
		out[i] = casPath
	}
	return out, nil
}
func (s *fakeStore) Ensure(context.Context, fetch.Request) (string, error) { return "", nil }
func (s *fakeStore) RealiseNow(_ context.Context, drvs map[string]*drv.Derivation) (map[string]map[string]string, error) {
	if s.fail {
		return nil, errors.New("building while planning failed")
	}
	res := map[string]map[string]string{}
	for p, d := range drvs {
		for _, o := range d.Outputs {
			host := s.HostPath(o.Path)
			if err := os.MkdirAll(filepath.Join(host, "greetv1connect"), 0o755); err != nil {
				return nil, err
			}
			_ = os.WriteFile(filepath.Join(host, "greet.pb.go"), []byte("package greetv1\n"), 0o644)
			_ = os.WriteFile(filepath.Join(host, "greetv1connect", "greet.connect.go"), []byte("package greetv1connect\n"), 0o644)
			res[p] = map[string]string{o.Name: host}
		}
	}
	return res, nil
}

func writeProto(t *testing.T, root, src, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.Base(src), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("syntax = \"proto3\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func graph(t *testing.T, kind string, spec connspec.Gen) (engine.PlanRequest, *fakeStore) {
	t.Helper()
	st := &fakeStore{root: t.TempDir()}
	writeProto(t, st.root, commonSrc, "proto/common/v1/common.proto")
	writeProto(t, st.root, greetSrc, "proto/greet/v1/greet.proto")
	lib := func(id, dir string, deps ...string) *ir.Node {
		raw, _ := json.Marshal(map[string]string{"root": "proto", "dir": dir})
		return &ir.Node{ID: id, Kind: "proto.library", Label: "//" + dir + ":proto", Spec: raw, Deps: deps}
	}
	raw, _ := json.Marshal(spec)
	n := &ir.Node{ID: "g", Kind: kind, Label: "//svc:gen-proto-greet-v1", Spec: raw, Deps: []string{"greet", "buf", "plugin"}}
	g := &ir.Graph{Nodes: []*ir.Node{lib("common", "proto/common/v1"), lib("greet", "proto/greet/v1", "common"), n}}
	tool := func(name string) engine.Resolved {
		return engine.Resolved{Drv: "/nix/store/dddddddddddddddddddddddddddddd" + name[:2] + "-" + name + ".drv",
			Outputs: map[string]string{"out": "/nix/store/oooooooooooooooooooooooooooooo" + name[:2] + "-" + name}}
	}
	return engine.PlanRequest{Graph: g, Nodes: []*ir.Node{n}, Builder: builder, Store: st,
		Inputs: map[string]engine.Resolved{
			"common": {Outputs: map[string]string{"out": commonSrc}},
			"greet":  {Outputs: map[string]string{"out": greetSrc}},
			"buf":    tool("buf"), "plugin": tool("protoc-gen-go"),
		},
		Resolver: resolver{}}, st
}

type resolver struct{}

func (resolver) Modulo(p string) ([32]byte, error) { return [32]byte{1}, nil }

// runArgs decodes the job of d strictly into the builder's own "run" schema.
func runArgs(t *testing.T, d *drv.Derivation) bspec.RunArgs {
	t.Helper()
	spec, _ := d.Structured["bobSpec"].(map[string]any)
	if spec["kind"] != "run" {
		t.Fatalf("job kind %v", spec["kind"])
	}
	b, _ := json.Marshal(spec["args"])
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var a bspec.RunArgs
	if err := dec.Decode(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

func execOf(t *testing.T, a bspec.RunArgs) bspec.ExecArgs {
	t.Helper()
	for _, s := range a.Steps {
		if s.Op == "exec" {
			dec := json.NewDecoder(bytes.NewReader(s.Args))
			dec.DisallowUnknownFields()
			var e bspec.ExecArgs
			if err := dec.Decode(&e); err != nil {
				t.Fatal(err)
			}
			return e
		}
	}
	t.Fatal("no exec step")
	return bspec.ExecArgs{}
}

// R-GEN-001a, R-GEN-005: connect.Go is one buf generate job with an M option per .proto file of
// the closure (bob decides the import paths); it is built while planning and consumers get the
// content-addressed copy (Cutoff), which is also what the working tree receives.
func TestPlanConnectGo(t *testing.T) {
	spec := connspec.Gen{Dir: "gen/proto/greet/v1", Module: "example.com/svc",
		Imports: map[string]string{"proto/greet/v1": "gen/proto/greet/v1", "proto/common/v1": "gen/proto/common/v1"}}
	req, st := graph(t, connspec.KindGo, spec)
	res, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drvs) != 1 {
		t.Fatalf("%d derivations, want 1", len(res.Drvs))
	}
	e := execOf(t, runArgs(t, res.Drvs[0]))
	argv := strings.Join(e.Argv, " ")
	for _, want := range []string{"buf generate", "--path proto/greet/v1",
		"Mcommon/v1/common.proto=example.com/svc/gen/proto/common/v1;commonv1",
		"Mgreet/v1/greet.proto=example.com/svc/gen/proto/greet/v1;greetv1",
		`"local":"protoc-gen-connect-go"`, `"paths=source_relative"`} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q:\n%s", want, argv)
		}
	}
	if e.Cwd != buildSrc || len(e.Path) != 2 {
		t.Errorf("cwd %q, path %v", e.Cwd, e.Path)
	}
	rv := res.Nodes["g"]
	if rv.Cutoff["out"] != casPath || rv.Drv == "" {
		t.Errorf("resolved %+v, want the cutoff copy %s", rv, casPath)
	}
	if len(res.Materialise) != 1 || res.Materialise[0].Dir != "svc/gen/proto/greet/v1" || res.Materialise[0].Output != casPath {
		t.Errorf("materialise %+v", res.Materialise)
	}
	if len(st.imported) != 1 || !slices.Equal(st.imported[0].Tree.Files, []string{"greet.pb.go", "greetv1connect/greet.connect.go"}) ||
		st.imported[0].Name != "gen-svc-gen-proto-greet-v1" {
		t.Errorf("imported %+v", st.imported)
	}
	if host, sp, ok := engine.CodegenOutput(st, rv); !ok || sp != casPath || host == "" {
		t.Errorf("CodegenOutput = %q %q %v", host, sp, ok)
	}
}

// A generator that fails while planning leaves the node on its derivation: the build stage
// reports the failure, consumers that read the files plan blocked (no CodegenOutput).
func TestPlanConnectFailure(t *testing.T) {
	req, st := graph(t, connspec.KindGo, connspec.Gen{Dir: "gen/proto/greet/v1", Module: "example.com/svc"})
	st.fail = true
	res, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	rv := res.Nodes["g"]
	if rv.Cutoff != nil || rv.Drv == "" || rv.Outputs["out"] == "" {
		t.Errorf("resolved %+v", rv)
	}
	if _, _, ok := engine.CodegenOutput(st, rv); ok {
		t.Errorf("a failed generator has an output to read")
	}
}

func TestPlanConnectTS(t *testing.T) {
	req, _ := graph(t, connspec.KindTS, connspec.Gen{Dir: "gen/proto/greet/v1"})
	res, err := Ecosystem{}.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(execOf(t, runArgs(t, res.Drvs[0])).Argv, " ")
	if !strings.Contains(argv, `"local":"protoc-gen-es"`) || !strings.Contains(argv, `"target=ts","import_extension=js"`) {
		t.Errorf("argv %s", argv)
	}
}

// A Go recipe without the consuming module's path cannot set import paths.
func TestPlanConnectGoNeedsModule(t *testing.T) {
	req, _ := graph(t, connspec.KindGo, connspec.Gen{Dir: "gen/proto/greet/v1"})
	if _, err := (Ecosystem{}).Plan(context.Background(), req); err == nil || !strings.Contains(err.Error(), "no Go module") {
		t.Errorf("err = %v", err)
	}
}

func TestGoPackage(t *testing.T) {
	for in, want := range map[string]string{"greet/v1": "greetv1", "a/b/greet/v1": "greetv1", "msg": "msg", "1x": "pb1x", "my-api/v2": "myapiv2"} {
		if got := goPackage(in); got != want {
			t.Errorf("goPackage(%q) = %q, want %q", in, got, want)
		}
	}
}
