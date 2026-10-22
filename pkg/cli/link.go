package cli

// Packages linked into bob for their node kinds and ecosystems: ir.Parse accepts only kinds
// this binary can plan (R-FW-051).
import (
	_ "github.com/megakuul/bob/pkg/golang/gen"
	_ "github.com/megakuul/bob/pkg/nixpkgs"
)
