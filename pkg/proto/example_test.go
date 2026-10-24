package proto_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/proto"
)

// A library that imports another one.
func ExampleNewLibrary() {
	common := bob.Define(func(p *bob.P) *proto.Library { return proto.NewLibrary(p, proto.LibraryOpts{}) })
	_ = bob.Define(func(p *bob.P) *proto.Library {
		return proto.NewLibrary(p, proto.LibraryOpts{Deps: []*proto.Library{common(p)}})
	})
}

// buf lint as a test unit (bob test //proto/greet/v1:lint).
func ExampleLint() {
	greet := bob.Define(func(p *bob.P) *proto.Library { return proto.NewLibrary(p, proto.LibraryOpts{}) })
	_ = bob.Define(func(p *bob.P) *proto.Check { return proto.Lint(p, greet(p)) })
}

// buf breaking against the library at origin/main.
func ExampleBreaking() {
	greet := bob.Define(func(p *bob.P) *proto.Library { return proto.NewLibrary(p, proto.LibraryOpts{}) })
	_ = bob.Define(func(p *bob.P) *proto.Check { return proto.Breaking(p, greet(p), "origin/main") })
}
