package store

import "embed"

// builderFS holds bob-builder for this architecture, written by go generate (builderbin/amd64/
// bob-builder.gz; R-NIX-015).
//
//go:embed all:builderbin/amd64
var builderFS embed.FS

const builderDir = "builderbin/amd64"
