//go:build !nixsystem

package store

import "embed"

// nixFS holds the static Nix for this architecture, written by scripts/fetch-nix.sh
// (nixbin/arm64/nix.gz and nix.sha256; R-NIX-020).
//
//go:embed all:nixbin/arm64
var nixFS embed.FS

const nixDir = "nixbin/arm64"
