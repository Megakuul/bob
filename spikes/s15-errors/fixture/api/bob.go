//go:build ignore

// Illustrative bob.go (S15).
package api

import (
	"example.com/fixture/libfoo"
	"example.com/fixture/proto"
	"example.com/fixture/rcore"
	"github.com/megakuul/bob/pkg/golang"
)

var API = golang.Binary("api", golang.Opts{
	Main: "./cmd/api",
	CGo:  true,
	Deps: golang.Deps{libfoo.Foo, rcore.Rcore, proto.Greeting},
	Requires: golang.Requires{
		golang.Module("github.com/google/uuid", "v1.6.0"),
		golang.Module("golang.org/x/sync", "v0.24.0"),
		golang.Module("google.golang.org/protobuf", "v1.36.11"),
	},
})
