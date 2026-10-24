//go:build bob

package appimage_test

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/output/appimage"
)

func ExampleNewBundle() {
	_ = bob.Define(func(p *bob.P) *appimage.Bundle {
		return appimage.NewBundle(p, "tools", appimage.BundleOpts{
			Program: nixpkgs.Curl.Exe(p),
			Entries: map[string]bob.Executable{"date": nixpkgs.Coreutils.Exe(p, nixpkgs.MainProgram("bin/date"))},
			Locales: []string{"C.UTF-8", "en_US.UTF-8"},
		})
	})
}
