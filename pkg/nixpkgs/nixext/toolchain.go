package nixext

import "github.com/megakuul/bob/pkg/bob"

// ToolchainAttrs are the nixpkgs packages (host package set) of the C/C++ toolchain that runs
// on the package's host and compiles for its target (SPEC R-FW-033, ADR 0020 §2): the typed
// form of the attribute strings bob.Toolchain carried. Ecosystems take them as Tools, e.g.
// tc.CC.Tool(p), so every package of one kind and target depends on one shared toolchain
// derivation. bob.Toolchain keeps the triple, prefix and emulator command.
type ToolchainAttrs struct {
	// CC is the C/C++ compiler wrapper: gcc natively, pkgsCross.gnu64.stdenv.cc for
	// linux-amd64 on a linux-arm64 host.
	CC Attr
	// Bintools is the matching binutils (ar, strip, objcopy with bob.Toolchain.Prefix).
	Bintools Attr
	// CCLib is the unwrapped compiler whose "lib" output holds the target's libgcc_s, libstdc++
	// and sanitiser runtimes (R-FW-037); the zero Attr natively (tc.CCLib == nixext.Attr{}), where the wrapper's RUNPATH covers them.
	CCLib Attr
	// Emulator is the user-mode emulator (a host tool) that runs the target's test programs on
	// the host (R-FW-039), whose command is bob.Toolchain.EmulatorCmd; zero natively.
	Emulator Attr
}

// Toolchain returns the toolchain packages for p's (host, target) pair (the table of SPEC
// R-FW-033, the same pairs as p.Toolchain()).
//
//	tc := nixext.Toolchain(p)
//	cc := tc.CC.Tool(p)
func Toolchain(p *bob.P) ToolchainAttrs {
	return ToolchainFor(p.Host().Platform(), p.Platform())
}

// ToolchainFor is Toolchain for an explicit (host, target) pair; an unknown target gives the
// native toolchain of host.
func ToolchainFor(host, target bob.Platform) ToolchainAttrs {
	t := ToolchainAttrs{CC: New("gcc"), Bintools: New("binutils")}
	if !bob.ToolchainFor(host, target).Cross() {
		return t
	}
	set := "pkgsCross." + target.CrossSet() + ".stdenv.cc"
	t.CC, t.Bintools, t.CCLib = New(set), New(set+".bintools"), New(set+".cc")
	t.Emulator = New("qemu-user")
	return t
}
