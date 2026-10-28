package translate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A header named by its repository path lies outside the sandbox of a package: the hint says
// which package exports it, what to write instead and whether the Deps entry is missing.
func TestIncludeHint(t *testing.T) {
	const drv = "/nix/store/00000000000000000000000000000000-cc-hello.drv"
	ctx := func(deps map[string][]string) *Context {
		return &Context{
			Packages: map[string]Pkg{drv: {Label: "//:hello", DefinedAt: "bob.go:16:9", Kind: "cc.binary"}},
			Exports:  map[string]Export{"//lib/asio:asio": {Dir: "lib/asio", Include: "lib/asio/includes"}},
			Deps:     deps,
		}
	}
	for _, c := range []struct {
		name, header string
		deps         map[string][]string
		want         string
	}{
		{"in Deps", "lib/asio/includes/asio.hpp", map[string][]string{"//:hello": {"//lib/asio:asio"}},
			`write #include "asio.hpp": //lib/asio:asio exports lib/asio/includes/`},
		{"not in Deps", "lib/asio/includes/net/tcp.hpp", nil,
			`write #include "net/tcp.hpp": //lib/asio:asio exports lib/asio/includes/; add //lib/asio:asio to Deps first`},
		{"not exported", "lib/asio/src/internal.hpp", nil,
			`lib/asio/src/internal.hpp belongs to //lib/asio:asio, which exports only lib/asio/includes/ to its consumers`},
		{"unrelated", "boost/asio.hpp", nil, ""},
	} {
		tr := New(ctx(c.deps))
		tr.logs[drv] = &buildLog{}
		tr.logs[drv].add("main.cpp:1:10: fatal error: " + c.header + ": No such file or directory")
		tr.cannotBuild(drv, "builder failed with exit code 1.", "raw")
		if len(tr.errs) != 1 {
			t.Fatalf("%s: %d errors, want 1", c.name, len(tr.errs))
		}
		got := tr.hint(tr.errs[0])
		if c.want == "" {
			if strings.Contains(got, "exports") {
				t.Errorf("%s: unexpected include hint %q", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: hint %q, want it to contain %q", c.name, got, c.want)
		}
	}
}

// A package nothing in the run uses is not in the graph: the repository tells the hint that the
// header belongs to a package of its own.
func TestIncludeHintUnknownPackage(t *testing.T) {
	const drv = "/nix/store/00000000000000000000000000000000-cc-hello.drv"
	root := t.TempDir()
	for _, f := range []string{"lib/asio/bob.go", "lib/asio/includes/asio.hpp", "bob.go", "main.cpp"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for header, want := range map[string]string{
		"lib/asio/includes/asio.hpp": "is a file of the package in lib/asio (lib/asio/bob.go)",
		"lib/asio/includes/gone.hpp": "", // not in the repository
		"main.cpp":                   "", // a file of the package itself
	} {
		tr := New(&Context{Root: root, Packages: map[string]Pkg{drv: {Label: "//:hello", Kind: "cc.binary"}}})
		tr.logs[drv] = &buildLog{}
		tr.logs[drv].add("main.cpp:1:10: fatal error: " + header + ": No such file or directory")
		tr.cannotBuild(drv, "builder failed with exit code 1.", "raw")
		got := tr.hint(tr.errs[0])
		if want == "" && strings.Contains(got, "package in") || want != "" && !strings.Contains(got, want) {
			t.Errorf("%s: hint %q, want %q", header, got, want)
		}
	}
}
