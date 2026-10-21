// Package ir is the backend-neutral package graph used by spike S0a.
// Both backends (A′ and B) consume exactly this; nothing here knows about Nix syntax.
package ir

import (
	"encoding/json"
	"fmt"
	"os"
)

type Kind string

const (
	KindFetch     Kind = "fetch"      // fixed-output download (crate, Go module zip, source tarball)
	KindCCLibrary Kind = "cc_library" // C/C++ library
	KindRustCrate Kind = "rust_crate" // Rust crate compiled with rustc directly (no cargo)
	KindProtoGo   Kind = "proto_go"   // protoc + protoc-gen-go
	KindGoBinary  Kind = "go_binary"  // Go binary (optionally cgo)
	// KindScript is S15-only: a package whose build step is a shell script. It stands in for
	// bob-builder steps, test runners and fault injection (timeouts, sandbox, resources).
	KindScript Kind = "script"
)

type Graph struct {
	Version  int        `json:"version"`
	System   string     `json:"system"` // e.g. aarch64-linux
	Nixpkgs  Pin        `json:"nixpkgs"`
	Root     string     `json:"root"`     // absolute path of the source root that Source.Path is relative to
	Packages []*Package `json:"packages"` // topologically sorted: deps before dependents
}

type Pin struct {
	Rev string `json:"rev"` // github:NixOS/nixpkgs/<rev>
}

type Package struct {
	Label     string   `json:"label"` // //dir:name
	Kind      Kind     `json:"kind"`
	DefinedAt string   `json:"definedAt"` // file:line of the definition in Go
	Src       *Source  `json:"src,omitempty"`
	Fetch     *Fetch   `json:"fetch,omitempty"`
	Deps      []string `json:"deps,omitempty"`    // labels of bob packages (must appear earlier)
	Nixpkgs   []string `json:"nixpkgs,omitempty"` // nixpkgs attribute names, e.g. "openssl"

	CC     *CCAttrs     `json:"cc,omitempty"`
	Rust   *RustAttrs   `json:"rust,omitempty"`
	Go     *GoAttrs     `json:"go,omitempty"`
	Proto  *ProtoAttrs  `json:"proto,omitempty"`
	Script *ScriptAttrs `json:"script,omitempty"`
}

// ScriptAttrs (S15 only). Deps' outputs are visible as $DEP_0, $DEP_1, … in Deps order.
type ScriptAttrs struct {
	Shell          string            `json:"shell"` // "bash" (nixpkgs bash + coreutils) | "sh" (sandbox /bin/sh, no inputs)
	Body           string            `json:"body"`
	Env            map[string]string `json:"env,omitempty"`
	NoChroot       bool              `json:"noChroot,omitempty"`
	DisallowedRefs []string          `json:"disallowedRefs,omitempty"` // labels
	System         string            `json:"system,omitempty"`         // override (unsupported-system fault)
	Builder        string            `json:"builder,omitempty"`        // override (broken-builder fault)
}

// Source is either a local directory (Path, relative to Graph.Root) or the output of a fetch package.
type Source struct {
	Path      string `json:"path,omitempty"`
	FromLabel string `json:"fromLabel,omitempty"` // a KindFetch package; its output is the (unpacked) source
	Subdir    string `json:"subdir,omitempty"`    // within the unpacked fetch
}

type Fetch struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Hash   string `json:"hash"`   // SRI sha256 of the downloaded file (flat)
	Unpack string `json:"unpack"` // "" (keep file), "tar.gz", "zip"
}

type CCAttrs struct {
	Lang   string   `json:"lang"` // "c" | "c++"
	Std    string   `json:"std"`  // e.g. c++20, c17
	Srcs   []string `json:"srcs"` // relative to Src
	Hdrs   string   `json:"hdrs"` // include dir relative to Src, exported to dependents
	Shared bool     `json:"shared"`
	Lib    string   `json:"lib"` // library name: libfoo.so / libfoo.a
}

type RustAttrs struct {
	CrateName string            `json:"crateName"`
	Version   string            `json:"version"`
	Edition   string            `json:"edition"`
	CrateType string            `json:"crateType"` // "lib" | "staticlib"
	Root      string            `json:"root"`      // crate root file relative to Src, e.g. src/lib.rs
	Externs   map[string]string `json:"externs,omitempty"`
	Hdrs      string            `json:"hdrs,omitempty"` // C headers exported (staticlib)
	Features  []string          `json:"features,omitempty"`
}

type GoAttrs struct {
	ModulePath string      `json:"modulePath"`
	GoVersion  string      `json:"goVersion"`
	Main       string      `json:"main"` // package path relative to module, e.g. ./cmd/api
	CGo        bool        `json:"cgo"`
	Requires   []GoRequire `json:"requires"`
	Generated  []GoGen     `json:"generated,omitempty"`
	Binary     string      `json:"binary"`
	Test       bool        `json:"test,omitempty"` // S15: run go test ./... before building
}

type GoRequire struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	Label   string `json:"label"` // KindFetch of the module zip
}

type GoGen struct {
	Label  string `json:"label"`  // a KindProtoGo package
	Subdir string `json:"subdir"` // where it is placed inside the module, e.g. gen/greetingv1
}

type ProtoAttrs struct {
	Files     []string `json:"files"` // relative to Src
	GoPackage string   `json:"goPackage"`
}

// Validate checks referential integrity and ordering. Both backends may assume a valid graph.
func (g *Graph) Validate() error {
	seen := map[string]*Package{}
	for _, p := range g.Packages {
		if _, dup := seen[p.Label]; dup {
			return fmt.Errorf("%s: duplicate label %s", p.DefinedAt, p.Label)
		}
		for _, d := range p.Deps {
			if _, ok := seen[d]; !ok {
				return fmt.Errorf("%s: %s depends on %s which is unknown or defined later (cycle?)", p.DefinedAt, p.Label, d)
			}
		}
		if p.Src != nil && p.Src.FromLabel != "" {
			if f, ok := seen[p.Src.FromLabel]; !ok || f.Kind != KindFetch {
				return fmt.Errorf("%s: %s source %s is not a known fetch package", p.DefinedAt, p.Label, p.Src.FromLabel)
			}
		}
		seen[p.Label] = p
	}
	return nil
}

func (g *Graph) Get(label string) *Package {
	for _, p := range g.Packages {
		if p.Label == label {
			return p
		}
	}
	return nil
}

func Load(path string) (*Graph, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g Graph
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &g, g.Validate()
}

func (g *Graph) Write(path string) error {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
