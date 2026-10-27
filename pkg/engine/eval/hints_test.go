package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNixpkgsHint(t *testing.T) {
	fw := &framework{h1: "test-hints", files: map[string][]byte{"pkg/nixpkgs/attrs_gen.go": []byte(
		"var (\n\t// x\n\tOpenssl = nixext.New(\"openssl\")\n\tOpensc = nixext.New(\"opensc\")\n\tNixUpdate = nixext.New(\"nix-update\")\n)\n")}}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bob.go"), []byte("package x\n\tDeps: []cc.Linkable{nixpkgs.Lib(p, \"openssl\", nixpkgs.Modules(\"libcrypto\"))},\n\tnixpkgs.Tool(p, \"nix-update\")\n\tnixpkgs.Pkg(p, \"nope\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		msg  string
		line int
		want string
	}{
		{"undefined: nixpkgs.Opensll", 1, "did you mean nixpkgs.Openssl?"},
		{"undefined: nixpkgs.Lib", 2, "nixpkgs packages are typed: write nixpkgs.Openssl.Lib(p)"},
		{"undefined: nixpkgs.Tool", 3, "nixpkgs packages are typed: write nixpkgs.NixUpdate.Tool(p)"},
		{"undefined: nixpkgs.Pkg", 4, `nixpkgs packages are typed: write nixpkgs.<Name>.Pkg(p); nixpkgs "nope" has no generated handle (see the exclusions in go doc nixpkgs)`},
		{"undefined: nixpkgs.Open", 1, "did you mean nixpkgs.Opensc?"},
		{"undefined: foo.Bar", 1, ""},
	} {
		if got := nixpkgsHint(c.msg, root, "bob.go", c.line, fw); got != c.want {
			t.Errorf("%s: %q, want %q", c.msg, got, c.want)
		}
	}
}
