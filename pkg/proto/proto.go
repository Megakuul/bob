package proto

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/proto/internal/protospec"
)

// A library is the same for every configuration (its .proto files); checks run on the host.
func init() {
	ext.Register(ext.Kind{Name: protospec.KindLibrary, Unit: "proto"})
	ext.Register(ext.Kind{Name: protospec.KindCheck, Axes: []ext.Axis{ext.AxisPlatform}, Host: true})
}

// LibraryOpts configures a protobuf library: the *.proto files of the calling bob.go's directory.
// The import root is derived (SPEC §03.6.1): the nearest ancestor directory named "proto"
// (the library's own directory included), else the repository root.
type LibraryOpts struct {
	Deps []*Library // imported libraries
}

// Library is one proto package (one directory, label //<dir>:proto). .proto files need no
// go_package: the code generators decide the import paths (connect.Go sets M options).
type Library struct {
	bob.Node
	root, dir string
	deps      []*Library
}

// ImportRoot is the library's import root, repo-relative ("" for the repository root): .proto
// imports are relative to it.
func (l *Library) ImportRoot() string { return l.root }

// Dir is the library's directory relative to its import root ("greet/v1").
func (l *Library) Dir() string {
	if l.root == "" {
		return l.dir
	}
	return strings.TrimPrefix(l.dir, l.root+"/")
}

// Imports are the libraries this one imports (LibraryOpts.Deps).
func (l *Library) Imports() []*Library { return l.deps }

// Closure is the library and every library it imports, each once, dependencies first.
func (l *Library) Closure() []*Library {
	seen := map[*Library]bool{}
	var out []*Library
	var walk func(x *Library)
	walk = func(x *Library) {
		if x == nil || seen[x] {
			return
		}
		seen[x] = true
		for _, d := range x.deps {
			walk(d)
		}
		out = append(out, x)
	}
	walk(l)
	return out
}

var revision = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9._/@^~{}-]{1,200}$`) })

// NewLibrary declares the protobuf library of the calling bob.go's directory (its *.proto files).
//
//	var Greet = bob.Define(func(p *bob.P) *proto.Library {
//		return proto.NewLibrary(p, proto.LibraryOpts{Deps: []*proto.Library{commonv1.Common(p)}})
//	})
func NewLibrary(p *bob.P, o LibraryOpts) *Library {
	dir, _ := ext.CallerDir()
	root := defaultRoot(dir)
	l := &Library{root: root, dir: dir}
	var problems []string
	var deps []bob.Package
	for i, d := range o.Deps {
		if d == nil {
			problems = append(problems, fmt.Sprintf("proto.LibraryOpts.Deps[%d] is nil", i))
			continue
		}
		if d.root != root {
			problems = append(problems, fmt.Sprintf("proto.LibraryOpts.Deps: %s has import root %q, this library %q; libraries that import each other share one root", d.Label(), d.root, root))
		}
		l.deps = append(l.deps, d)
		deps = append(deps, d)
	}
	l.Node = ext.New(p, protospec.KindLibrary, "proto", deps, []bob.Sources{bob.Glob("*.proto")}, protospec.Library{Root: root, Dir: dir})
	for _, pr := range problems {
		ext.Errorf("%s: %s", l.DefinedAt(), pr)
	}
	return l
}

// defaultRoot is the nearest ancestor of dir (itself included) named "proto", else "".
func defaultRoot(dir string) string {
	parts := strings.Split(dir, "/")
	for i := len(parts); i > 0; i-- {
		if parts[i-1] == "proto" {
			return strings.Join(parts[:i], "/")
		}
	}
	return ""
}

// Check is a buf lint or buf breaking check of a library: a test unit, run by bob test and in
// pipelines (bob build leaves it out).
type Check struct{ bob.Node }

// Lint and Breaking are check packages (buf lint / buf breaking) usable in pipelines.
// Lint is labelled //<dir>:lint.
//
//	var GreetLint = bob.Define(func(p *bob.P) *proto.Check { return proto.Lint(p, Greet(p)) })
func Lint(p *bob.P, lib *Library) *Check {
	return check(p, lib, protospec.Check{Mode: protospec.Lint}, "lint")
}

// Breaking compares the library with itself at a git revision of the same repository (buf
// breaking); labelled //<dir>:breaking.
//
//	var GreetCompat = bob.Define(func(p *bob.P) *proto.Check { return proto.Breaking(p, Greet(p), "origin/main") })
func Breaking(p *bob.P, lib *Library, againstRev string) *Check {
	if !revision().MatchString(againstRev) || strings.HasPrefix(againstRev, "-") || strings.Contains(againstRev, "..") {
		ext.Errorf("proto.Breaking: %q is not a git revision", againstRev)
		return &Check{}
	}
	return check(p, lib, protospec.Check{Mode: protospec.Breaking, Against: againstRev}, "breaking")
}

func check(p *bob.P, lib *Library, spec protospec.Check, name string) *Check {
	if lib == nil {
		ext.Errorf("proto.%s: the library is nil", strings.ToUpper(name[:1])+name[1:])
		return &Check{}
	}
	return &Check{Node: ext.New(p, protospec.KindCheck, name, []bob.Package{lib, nixpkgs.Buf.Tool(p)}, nil, spec)}
}
