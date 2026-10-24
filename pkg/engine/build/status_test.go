package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/megakuul/bob/pkg/engine"
	"github.com/megakuul/bob/pkg/engine/ir"
	"github.com/megakuul/bob/pkg/engine/lock"
)

func init() { engine.RegisterCodegenKind("statustest.gen") }

// The inputs digest of bob status (A280): a codegen package's digest follows the sources and
// declarations of its closure but not their positions; another package's digest follows its own
// declaration and bob.lock, not its sources.
func TestInputsDigest(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proto/a.proto", "syntax = \"proto3\";\n")
	write("web/src/app.ts", "export {}\n")
	write("bob.lock", "{\"lockVersion\":1}\n")
	graph := func(line int, spec string) *ir.Graph {
		return &ir.Graph{IRVersion: ir.Version, Nodes: []*ir.Node{
			{ID: "p", Kind: "proto.library", Label: "//proto:proto", DefinedAt: ir.Pos{File: "proto/bob.go", Line: line},
				Srcs: []ir.Srcs{{Dir: "proto", Patterns: []string{"*.proto"}}}, Spec: json.RawMessage(`{}`)},
			{ID: "g", Kind: "statustest.gen", Label: "//svc:gen", Deps: []string{"p"}, Spec: json.RawMessage(spec)},
			{ID: "w", Kind: "npm.app", Label: "//web:web", Srcs: []ir.Srcs{{Dir: "web", Patterns: []string{"src/**"}}}, Spec: json.RawMessage(`{}`)},
		}}
	}
	lf := &lock.File{LockVersion: lock.Version}
	digest := func(g *ir.Graph, label string) string {
		t.Helper()
		d, err := newDigester(root, g, lf, statusSrcPath(root, nil)).of(label)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	gen, web := digest(graph(3, `{}`), "//svc:gen"), digest(graph(3, `{}`), "//web:web")
	if digest(graph(9, `{}`), "//svc:gen") != gen {
		t.Error("moving a declaration changed the digest")
	}
	if digest(graph(3, `{"x":1}`), "//svc:gen") == gen {
		t.Error("a changed codegen declaration kept the digest")
	}
	write("proto/a.proto", "syntax = \"proto3\";\nmessage A {}\n")
	if digest(graph(3, `{}`), "//svc:gen") == gen {
		t.Error("a changed .proto in the closure kept the digest")
	}
	write("web/src/app.ts", "export const x = 1\n")
	if digest(graph(3, `{}`), "//web:web") != web {
		t.Error("an edited source changed the digest of a non-codegen root (node_modules)")
	}
	write("bob.lock", "{\"lockVersion\":1,\"x\":1}\n")
	if digest(graph(3, `{}`), "//web:web") == web {
		t.Error("a changed bob.lock kept the digest of a non-codegen root")
	}
}
