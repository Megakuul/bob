//go:build bob

package polyglot

import (
	"example.com/polyglot/crates/rcore"
	"example.com/polyglot/libs/foo"
	greetingv1 "example.com/polyglot/proto/greeting/v1"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/connect"
	"github.com/megakuul/bob/pkg/golang"
)

var _ = bob.Repo(bob.RepoOpts{
	Nixpkgs: "nixos-26.05",
	Variants: []bob.Named{
		bob.NamedVariant("asan", bob.WithSanitize(bob.ASan, bob.UBSan), bob.WithProfile(bob.Debug)),
	},
})

// The release pipeline runs on a tag push matching v* (concurrency Queue) and publishes
// //deploy:polyglot.
var _ = bob.Pipeline("release", bob.PipelineOpts{
	Triggers:    []bob.Trigger{bob.OnTag("v*")},
	Concurrency: bob.Queue,
	Releases:    []string{"polyglot"},
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

// FFI gives the cgo code of cmd/api its C++ and Rust libraries.
var FFI = bob.Define(func(p *bob.P) *golang.Package {
	return Module(p).Cgo("./cmd/api", golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})
})

// API is the cgo binary (label //cmd/api:api). It is declared here, not in cmd/api/bob.go,
// because deploy/bob.go imports it and cmd/api is a main package (rule R3). FFI is declared
// first: a Go binary picks up the module's cgo packages declared so far, and a build of only
// //deploy:polyglot never evaluates //:FFI otherwise.
var API = bob.Define(func(p *bob.P) *golang.Binary {
	FFI(p)
	return Module(p).Binary("./cmd/api", golang.BinaryOpts{})
})

// Tests runs the Go tests of the module (units //<pkg dir>:go_test).
var Tests = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests("./...", golang.TestOpts{})
})
