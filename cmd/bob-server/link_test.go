package main

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/megakuul/bob/pkg/engine/ir"
)

// blankImports returns the sorted blank imports of a Go file.
func blankImports(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, im := range f.Imports {
		if im.Name == nil || im.Name.Name != "_" {
			continue
		}
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			t.Fatalf("%s: import %s: %v", path, im.Path.Value, err)
		}
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// TestLinkedEcosystemsMatchCLI: bob-server's workers must plan every kind the bob CLI can, so a
// repository that builds locally also builds remotely.
func TestLinkedEcosystemsMatchCLI(t *testing.T) {
	server := blankImports(t, "link.go")
	cli := blankImports(t, filepath.Join("..", "..", "pkg", "cli", "link.go"))
	if len(cli) == 0 {
		t.Fatal("pkg/cli/link.go has no blank imports")
	}
	if !slices.Equal(server, cli) {
		t.Fatalf("cmd/bob-server/link.go and pkg/cli/link.go link different ecosystems:\nserver: %v\ncli:    %v", server, cli)
	}
}

// TestLinkedKinds: the kinds remote builds failed on are known to this binary.
func TestLinkedKinds(t *testing.T) {
	for _, k := range []string{"rust.crate", "oci.image", "go.binary", "npm.lib", "cc.library", "opam.library", "tarball.tarball", "release.release"} {
		if !ir.KnownKind(k) {
			t.Errorf("kind %q is not known to bob-server", k)
		}
	}
}
