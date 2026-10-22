package store

import "embed"

// nixFS holds the static Nix for this architecture, written by scripts/fetch-nix.sh
// (nixbin/amd64/nix.gz and nix.sha256; R-NIX-020).
//
//go:embed all:nixbin/amd64
var nixFS embed.FS

const nixDir = "nixbin/amd64"
