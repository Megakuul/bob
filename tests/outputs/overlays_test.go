package outputs

// Overlay packages written into a copy of the polyglot fixture. They declare the bundle variants
// the fixture's deploy/bob.go does not (SPEC examples of §04); each is a separate bob.go
// package so the fixture itself stays unchanged.
//
// SPEC gap: §04 gives no constructor for a nixpkgs program as a bob.Executable (the "curl
// bundle" of R-OUT-003/018 needs one) and none for a script executable (R-OUT-019). The overlays
// use nixpkgs.Exe(p, attr, mainProgram) and bob.Exe(step, mainProgram); both are listed under
// "SPEC problems" in plan/progress/T3b.md. Overlays that use them are only the slow distro
// matrix, R-OUT-019 and R-OUT-020; every other test uses real fixture programs.

// ocitestGo: images with extra roots. Tools has coreutils and curl (id, date, HTTPS GET), Four
// has MaxLayers 4 (R-OUT-009), Pruned removes libssl (R-OUT-007).
const ocitestGo = `//go:build bob

package ocitest

import (
	"example.com/polyglot"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/output/oci"
)

var Tools = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "tools", oci.ImageOpts{
		Program: polyglot.API(p),
		Extra:   []bob.Package{nixpkgs.Pkg(p, "coreutils"), nixpkgs.Pkg(p, "curl")},
	})
})

var Four = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "four", oci.ImageOpts{Program: polyglot.API(p), MaxLayers: 4})
})

var Pruned = bob.Define(func(p *bob.P) *oci.Image {
	return oci.NewImage(p, "pruned", oci.ImageOpts{
		Program: polyglot.API(p),
		Prune:   []string{"/nix/store/*-openssl-*/lib/libssl.so*"},
	})
})
`

// apptestGo: AppImage variants.
const apptestGo = `//go:build bob

package apptest

import (
	"example.com/polyglot"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/output/appimage"
)

// Tools is the bundle of the distro matrix: the api plus curl, date and iconv as entries.
var Tools = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "tools", appimage.BundleOpts{
		Program: polyglot.API(p),
		Entries: map[string]bob.Executable{
			"curl":  nixpkgs.Exe(p, "curl", "bin/curl"),
			"date":  nixpkgs.Exe(p, "coreutils", "bin/date"),
			"iconv": nixpkgs.Exe(p, "glibc", "bin/iconv"),
		},
		Locales: []string{"C.UTF-8", "en_US.UTF-8"},
	})
})

// Unpruned keeps everything of the closure.
var Unpruned = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "unpruned", appimage.BundleOpts{Program: polyglot.API(p), Prune: []string{}})
})

// BadPrune removes libssl, which the api needs (R-OUT-007).
var BadPrune = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "bundle", appimage.BundleOpts{
		Program: polyglot.API(p),
		Prune:   []string{"/nix/store/*-openssl-*/lib/libssl.so*"},
	})
})

var script = bob.Define(func(p *bob.P) bob.Executable {
	step := bob.Run(p, "deploy-script", bob.RunOpts{
		Tools: []bob.Tool{nixpkgs.Tool(p, "coreutils")},
		Cmd:   []string{"sh", "-c", "mkdir -p $out/bin && printf '#!/bin/sh\necho deploy\n' > $out/bin/deploy && chmod +x $out/bin/deploy"},
	})
	return bob.Exe(step, "bin/deploy")
})

// Script has a shell-script entry in AutoMode (R-OUT-019 warns); ScriptUserns says so explicitly.
var Script = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "tools", appimage.BundleOpts{
		Program: polyglot.API(p),
		Entries: map[string]bob.Executable{"deploy": script(p)},
	})
})

var ScriptUserns = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "tools-userns", appimage.BundleOpts{
		Program: polyglot.API(p),
		Entries: map[string]bob.Executable{"deploy": script(p)},
		Mode:    appimage.UsernsOnly,
	})
})

// Graphics runs vulkaninfo with the host's GPU libraries appended (R-OUT-020).
var Graphics = bob.Define(func(p *bob.P) *appimage.Bundle {
	return appimage.NewBundle(p, "graphics", appimage.BundleOpts{
		Program:      nixpkgs.Exe(p, "vulkan-tools", "bin/vulkaninfo"),
		HostGraphics: true,
	})
})
`

// tartestGo asks for a static tarball of the cgo api, which needs glibc and openssl (R-OUT-004).
const tartestGo = `//go:build bob

package tartest

import (
	"example.com/polyglot"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/output/tarball"
)

var NotStatic = bob.Define(func(p *bob.P) *tarball.Tarball {
	return tarball.New(p, "api-static", tarball.TarballOpts{Program: polyglot.API(p), Kind: tarball.Static})
})
`

// stamptarGo declares tarballs of the pure-Go stamp binary of the go fixture. It is placed in
// the go fixture at tools/tar/bob.go.
const stamptarGo = `//go:build bob

package tar

import (
	"example.com/tools"
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/output/tarball"
)

var Static = bob.Define(func(p *bob.P) *tarball.Tarball {
	return tarball.New(p, "stamp", tarball.TarballOpts{Program: tools.Stamp(p), Kind: tarball.Static})
})
`

// modeOf renders the overlay map for a world.
func ociOverlay() map[string]string { return map[string]string{"ocitest/bob.go": ocitestGo} }
