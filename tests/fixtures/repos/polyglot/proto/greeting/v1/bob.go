//go:build bob

package v1

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/proto"
)

// Greeting is the greeting.v1 protobuf library (proto package greeting.v1).
var Greeting = bob.Define(func(p *bob.P) *proto.Library {
	return proto.NewLibrary(p, proto.LibraryOpts{})
})

// Lint runs buf lint on the library.
var Lint = bob.Define(func(p *bob.P) *proto.Check { return proto.Lint(p, Greeting(p)) })
