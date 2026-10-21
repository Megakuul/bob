// Package nixpkgs (mock) exposes packages from the pinned nixpkgs as typed values.
package nixpkgs

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
)

// Lib is a C/C++ library from nixpkgs, e.g. nixpkgs.Lib(p, "openssl").
func Lib(p *bob.P, attr string) cc.Linkable { return cc.Prebuilt(p, attr) }
