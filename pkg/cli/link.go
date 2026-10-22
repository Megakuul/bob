package cli

// Packages linked into bob for their node kinds and ecosystems: ir.Parse accepts only kinds
// this binary can plan (R-FW-051).
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
