// Package cc (mock) builds C and C++ code.
package cc

import "github.com/megakuul/bob/pkg/bob"

type Std string

const (
	C17   Std = "c17"
	CXX20 Std = "c++20"
)

// Linkable is anything a C/C++/Go/Rust package can link against.
type Linkable interface {
	bob.Package
	LinkInfo() LinkInfo
}

// LinkInfo says how to link a package (mock).
type LinkInfo struct{ Static bool }

// Library is a C/C++ library package.
type Library struct {
	*bob.Base
	Shared bool
}

func (l *Library) LinkInfo() LinkInfo { return LinkInfo{Static: !l.Shared} }

type LibraryOpts struct {
	Srcs    bob.Source
	Hdrs    string // include directory exported to dependents
	Std     Std
	Defines []string
	Deps    []Linkable
}

func deps(ls []Linkable) []bob.Package {
	out := make([]bob.Package, len(ls))
	for i, l := range ls {
		out[i] = l
	}
	return out
}

// SharedLibrary defines lib<name>.so.
func SharedLibrary(p *bob.P, name string, o LibraryOpts) *Library {
	return &Library{Base: bob.NewPackage(p, 1, "cc_library", name, deps(o.Deps), map[string]any{"shared": true, "std": o.Std, "srcs": o.Srcs.Patterns}), Shared: true}
}

// StaticLibrary defines lib<name>.a.
func StaticLibrary(p *bob.P, name string, o LibraryOpts) *Library {
	return &Library{Base: bob.NewPackage(p, 1, "cc_library", name, deps(o.Deps), map[string]any{"shared": false, "std": o.Std, "srcs": o.Srcs.Patterns})}
}

type prebuilt struct{ *bob.Base }

func (prebuilt) LinkInfo() LinkInfo { return LinkInfo{} }

// Prebuilt is used by the nixpkgs package.
func Prebuilt(p *bob.P, attr string) Linkable {
	return prebuilt{bob.Nixpkgs(p, attr)}
}
