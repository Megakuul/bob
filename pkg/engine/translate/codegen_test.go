package translate

import (
	"strings"
	"testing"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// R-GEN-001a: a failed code generator reads `✗ <label> code generation failed (defined at
// <bob.go:line>)` with the generator's own messages (bob-builder's lines left out); R-GEN-006:
// a runtime mismatch reads as SPEC §03.6.4 writes it.
func TestCodegenBlocks(t *testing.T) {
	const drv = "/nix/store/00000000000000000000000000000000-gen-svc-gen-proto-greet-v1.drv"
	tr := New(&Context{Packages: map[string]Pkg{drv: {Label: "//svc:gen-proto-greet-v1", DefinedAt: "svc/bob.go:22:4",
		Kind: "connect.go", Codegen: true}}})
	tr.logs[drv] = &buildLog{}
	for _, l := range []string{"proto/greet/v1/greet.proto:9:1:unexpected `}` after definition",
		"bob-builder: step 4 exec buf failed: exit status 100"} {
		tr.logs[drv].add(l)
	}
	tr.cannotBuild(drv, "builder failed with exit code 1.", "raw")
	tr.cannotBuild(drv, "builder failed with exit code 1.", "raw") // Nix repeats it for dependents
	if len(tr.errs) != 1 {
		t.Fatalf("%d errors, want 1: %v", len(tr.errs), tr.errs)
	}
	var b strings.Builder
	Block(&b, tr.errs[0], "", Style{})
	want := "✗ //svc:gen-proto-greet-v1 code generation failed (defined at svc/bob.go:22)\n" +
		"  package defined at svc/bob.go:22:4\n" +
		"   | proto/greet/v1/greet.proto:9:1:unexpected `}` after definition\n" +
		"  full log: bob log //svc:gen-proto-greet-v1\n"
	if b.String() != want {
		t.Errorf("block\n%s\nwant\n%s", b.String(), want)
	}

	rt := diag.New(1021, "label", "//web:gen-proto-greet-v1", "runtime", "@bufbuild/protobuf",
		"reason", "generator protoc-gen-es 2.11.0 (bob's nixpkgs pin) does not match its runtime: the generated code needs @bufbuild/protobuf 2.11.0, but web/bob.go requires 2.10.0")
	b.Reset()
	Block(&b, rt, rt.Hint(), Style{})
	if !strings.HasPrefix(b.String(), "✗ //web:gen-proto-greet-v1: generator protoc-gen-es 2.11.0 (bob's nixpkgs pin) does not match its runtime") {
		t.Errorf("runtime block\n%s", b.String())
	}
}
