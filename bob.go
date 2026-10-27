//go:build bob

// Package bobrepo holds bob's own build definitions (R-ARCH-005): bob builds bob, bob-builder,
// bob-server and the web UI, and `bob pipeline run ci` builds them and runs the hermetic tests.
// The repository root has no other Go files, so any package name is accepted (A131).
package bobrepo

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/output/appimage"
)

var _ = bob.Repo(bob.RepoOpts{Nixpkgs: bob.NixOS2605})

// ci is bob's CI pipeline (A153): the three binaries and the web UI, the Go tests that run
// inside the build sandbox (hermetic below) and svelte-check. The checks that need the
// network, git, a live S3 backend or files outside a test's package (lint, layering, licences,
// buf, goldens, acceptance suites, budgets, protected files and the remaining unit tests) stay
// in `go run ./tools/ci`, which is also the bootstrap fallback.
var _ = bob.Pipeline("ci", bob.PipelineOpts{
	Targets: []bob.Pattern{"//cmd/bob:bob", "//cmd/bob-builder:bob-builder", "//cmd/bob-server:bob-server", "//web:web"},
	Tests:   append(units(hermetic), "//web:svelte-check"),
})

// hermetic are the Go packages whose tests pass inside the build sandbox: they read nothing
// outside their own sources and need no Go toolchain, Nix, network or S3 backend at run time.
var hermetic = []string{
	"cmd/bob-builder/genbuilders",
	"internal/api", "internal/auth", "internal/build/jobspec", "internal/forge/github",
	"internal/forge/gitlab", "internal/http", "internal/isolation", "internal/safe", "internal/server",
	"internal/ui",
	"pkg/bob/ext", "pkg/cc/bop", "pkg/cc/gen", "pkg/cli/devenv", "pkg/cli/remote", "pkg/connect/gen",
	"pkg/engine", "pkg/engine/drv", "pkg/engine/eval/evalbox", "pkg/engine/fetch", "pkg/engine/ir",
	"pkg/engine/lock", "pkg/engine/materialise", "pkg/engine/nar", "pkg/golang", "pkg/golang/bop",
	"pkg/golang/resolve", "pkg/graphql", "pkg/graphql/gen", "pkg/nixpkgs", "pkg/npm/bop", "pkg/npm/gen",
	"pkg/ocaml/bop", "pkg/ocaml/gen", "pkg/ocaml/resolve", "pkg/openapi", "pkg/openapi/gen",
	"pkg/output/appimage", "pkg/output/appimage/bop", "pkg/output/oci/bop", "pkg/output/oci/gen",
	"pkg/output/tarball/gen", "pkg/proto", "pkg/proto/gen", "pkg/release/bop", "pkg/rust",
	"pkg/rust/bop", "pkg/rust/gen", "pkg/services/postgres",
}

// units returns the labels of the test units of dirs (//<dir>:go_test).
func units(dirs []string) []bob.Pattern {
	out := make([]bob.Pattern, 0, len(dirs)+1)
	for _, d := range dirs {
		out = append(out, bob.Pattern("//"+d+":go_test"))
	}
	return out
}

// Module is github.com/megakuul/bob (label //:module); go.mod and go.sum are generated from it.
var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path: "github.com/megakuul/bob",
		Go:   "1.26",
		Require: []golang.Requirement{
			golang.Require("codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3", "v3.0.0"),
			golang.Require("connectrpc.com/connect", "v1.21.1"),
			golang.Require("github.com/aws/aws-sdk-go-v2", "v1.42.0"),
			golang.Require("github.com/aws/aws-sdk-go-v2/config", "v1.32.25"),
			golang.Require("github.com/aws/aws-sdk-go-v2/credentials", "v1.19.24"),
			golang.Require("github.com/aws/aws-sdk-go-v2/service/s3", "v1.103.3"),
			golang.Require("github.com/aws/smithy-go", "v1.27.2"),
			golang.Require("github.com/chromedp/cdproto", "v0.157.8"),
			golang.Require("github.com/chromedp/chromedp", "v0.21.0"),
			golang.Require("github.com/coreos/go-oidc/v3", "v3.17.0"),
			golang.Require("github.com/fxamacker/cbor/v2", "v2.9.2"),
			golang.Require("github.com/google/go-github/v88", "v88.0.0"),
			golang.Require("github.com/klauspost/compress", "v1.20.3"),
			golang.Require("github.com/megakuul/dynamitedb", "v0.6.1"),
			golang.Require("github.com/megakuul/lake", "v0.5.3"),
			golang.Require("github.com/santhosh-tekuri/jsonschema/v5", "v5.3.1"),
			golang.Require("github.com/ulikunitz/xz", "v0.5.17"),
			golang.Require("gitlab.com/gitlab-org/api/client-go", "v1.46.0"),
			golang.Require("golang.org/x/crypto", "v0.58.0"),
			golang.Require("golang.org/x/oauth2", "v0.36.0"),
			golang.Require("google.golang.org/protobuf", "v1.36.12"),
		},
	})
})

// Bob is the bob command (label //cmd/bob:bob), bound in cmd/bob/bob.go (one binary per
// directory package, R-FW-071). It embeds bob-builder and the static Nix of
// pkg/engine/store/{builderbin,nixbin}: go generate ./pkg/engine/store and scripts/fetch-nix.sh
// write them, scripts/bootstrap.sh runs both. bob-builder (//cmd/bob-builder:bob-builder) and
// bob-server (//cmd/bob-server:bob-server) are defined in their own directories' bob.go.
var Bob = bob.NewSlot[*golang.Binary]()

// AppImage is the bob AppImage of the GitHub release (label //:bob, file bob-<arch>.AppImage,
// .github/workflows/release.yml): //cmd/bob:bob with its embedded static Nix and builder, for
// the host platform (the release runs once per architecture). bob is a static binary, so the
// bundle needs no glibc, locales, iconv or CA certificates of its own (bob embeds its fallback
// roots, A242).
var AppImage = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "bob", appimage.BundleOpts{
		Program:   Bob.Get(p),
		NoCACerts: true,
		NoIconv:   true,
	})
})

// Unit declares the test units of the Go packages of the repository root's region
// (//<dir>:go_test for pkg/, internal/, cmd/ and tests/; the directories with a bob.go of their
// own declare theirs); the ci pipeline runs the hermetic ones.
var Unit = bob.Define(func(p *bob.P) *golang.TestSet {
	return Module(p).Tests(golang.TestOpts{Recursive: true, Name: "unit", CGo: golang.CGoOff})
})
