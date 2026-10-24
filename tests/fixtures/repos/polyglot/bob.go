//go:build bob

package polyglot

import (
	greetingv1 "example.com/polyglot/proto/greeting/v1"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{
	Nixpkgs: bob.NixOS2605,
	Variants: []bob.Named{
		bob.NamedVariant("asan", bob.WithSanitize(bob.ASan, bob.UBSan), bob.WithProfile(bob.Debug)),
	},
})

// The release pipeline runs on a tag push matching v* (concurrency Queue) and publishes
// //deploy:polyglot.
var _ = bob.Pipeline("release", bob.PipelineOpts{
	Triggers:    []bob.Trigger{bob.OnTag("v*")},
	Concurrency: bob.Queue,
	Releases:    []bob.Pattern{"//deploy:polyglot"},
})

// Module is the Go module example.com/polyglot (label //:module).
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path: "example.com/polyglot",
		Go:   "1.26",
		Require: []golang.Requirement{
			golang.Require("github.com/google/uuid", "v1.6.0"),
			golang.Require("golang.org/x/sync", "v0.20.0"),
		},
		Generated: []golang.Generated{connect.Go(greetingv1.Greeting(p), connect.GoOpts{})},
	})
})

// API is the cgo binary //cmd/api:api. cmd/api is a main package, whose bob.go cannot be
// imported (R-FW-002 R3): cmd/api/bob.go binds its constructor to this typed slot and
// deploy/bob.go gets the binary from it.
var API = bob.NewSlot[*golang.Binary]()

// Tests runs the Go tests of the module's packages below the root that have no bob.go of their
// own (units //<pkg dir>:go_test); cmd/api declares its own.
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests(golang.TestOpts{Recursive: true})
})
