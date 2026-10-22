package main

// Ecosystems linked into bob-server: workers evaluate and build any repository inside
// `bob-server job-run`, and ir.Parse accepts only node kinds this binary can plan (R-FW-051).
// The list is the same as pkg/cli/link.go (the bob CLI); link_test.go checks that both agree.
// A shared package is not possible: pkg/ may not import internal/, and the CLI list lives in
// pkg/cli.
import (
	_ "github.com/megakuul/bob/pkg/cc/gen"
	_ "github.com/megakuul/bob/pkg/golang/gen"
	_ "github.com/megakuul/bob/pkg/nixpkgs"
	_ "github.com/megakuul/bob/pkg/npm/gen"
	_ "github.com/megakuul/bob/pkg/ocaml/gen"
	_ "github.com/megakuul/bob/pkg/output/appimage/gen"
	_ "github.com/megakuul/bob/pkg/output/oci/gen"
	_ "github.com/megakuul/bob/pkg/output/tarball/gen"
	_ "github.com/megakuul/bob/pkg/release/gen"
	_ "github.com/megakuul/bob/pkg/rust/gen"
)
