//go:build bob

package v1

import (
	commonv1 "example.com/codegen/proto/common/v1"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/proto"
)

// Greet is the greet.v1 protobuf library; it imports common.v1.
var Greet = bob.Define(func(p *bob.P) *proto.Library {
	return proto.Library(p, proto.LibraryOpts{Deps: []*proto.Library{commonv1.Common(p)}})
})

// Lint runs buf lint on greet.v1.
var Lint = bob.Define(func(p *bob.P) *proto.Check { return proto.Lint(p, Greet(p)) })
