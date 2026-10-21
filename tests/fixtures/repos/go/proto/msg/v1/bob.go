//go:build bob

package v1

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/proto"
)

// Msg is the msg.v1 protobuf library.
var Msg = bob.Define(func(p *bob.P) *proto.Library { return proto.Library(p, proto.LibraryOpts{}) })
