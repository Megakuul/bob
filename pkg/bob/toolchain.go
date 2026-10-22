package bob

import "strings"

// Toolchain describes the C/C++ compilers an ecosystem uses for one (host, target) pair (SPEC
// R-FW-033). Builders in pkg/<ecosystem> receive it from p.Toolchain; users never construct one.
//
// The compiler is named by its nixpkgs attribute of the host package set, so that ecosystems
// take it as nixpkgs.Tool(p, tc.CC): every package of one kind and target then depends on one
// shared toolchain derivation. (SPEC §04 shows the field as a derivation reference; the
// evaluator does not compute derivations, the attribute names the same derivation.)
type Toolchain struct {
	Host, Target Platform
	Triple       string // "x86_64-unknown-linux-gnu"; empty for native
	Prefix       string // "x86_64-unknown-linux-gnu-" for cross, "" for native
	// CC is the nixpkgs attribute (host set) of the C/C++ compiler wrapper: "gcc" natively,
	// "pkgsCross.gnu64.stdenv.cc" for linux-amd64 on a linux-arm64 host.
	CC string
	// Bintools is the attribute of the matching binutils (ar, strip, objcopy with Prefix).
	Bintools string
	// CCLib is the attribute of the unwrapped compiler whose "lib" output holds the target's
	// libgcc_s, libstdc++ and sanitiser runtimes (R-FW-037); "" natively, where the wrapper's
	// RUNPATH already covers them.
	CCLib string
	// Emulator is the attribute of the user-mode emulator (a host tool) that runs the target's
	// test programs on the host (R-FW-039), and EmulatorCmd its command in bin/; "" natively.
	Emulator, EmulatorCmd string
}

// Cross reports whether the toolchain compiles for another platform than it runs on.
func (t Toolchain) Cross() bool { return t.Host != t.Target }

// Toolchain returns the C/C++ toolchain for the package's target. Example:
//
//	tc := p.Toolchain()          // inside a bob.Define body
//	_ = tc.Prefix + "gcc"
func (p *P) Toolchain() Toolchain { return ToolchainFor(p.Host().Platform(), p.Platform()) }

// ToolchainFor returns the C/C++ toolchain that runs on host and compiles for target (the
// table of SPEC R-FW-033). An unknown platform gives the native toolchain of host.
func ToolchainFor(host, target Platform) Toolchain {
	t := Toolchain{Host: host, Target: target, CC: "gcc", Bintools: "binutils"}
	if host == target || target.CrossSet() == "" {
		t.Target = host
		return t
	}
	set := "pkgsCross." + target.CrossSet() + ".stdenv.cc"
	t.Triple = target.Triple()
	t.Prefix = t.Triple + "-"
	t.CC, t.Bintools, t.CCLib = set, set+".bintools", set+".cc"
	t.Emulator, t.EmulatorCmd = "qemu-user", "qemu-"+target.cpu()
	return t
}

// NixSystem is the Nix system of the platform ("aarch64-linux"); "" for an unknown platform.
func (p Platform) NixSystem() string {
	arch := p.cpu()
	if arch == "" {
		return ""
	}
	return arch + "-linux"
}

// GOARCH is the Go architecture of the platform ("arm64").
func (p Platform) GOARCH() string {
	if !strings.HasPrefix(string(p), "linux-") || p.cpu() == "" {
		return ""
	}
	return strings.TrimPrefix(string(p), "linux-")
}

// Triple is the GNU target triple of the platform ("aarch64-unknown-linux-gnu"), also Rust's.
func (p Platform) Triple() string {
	if arch := p.cpu(); arch != "" {
		return arch + "-unknown-linux-gnu"
	}
	return ""
}

// CrossSet is the nixpkgs pkgsCross set that targets the platform from the other architecture
// ("gnu64", "aarch64-multiplatform").
func (p Platform) CrossSet() string {
	switch p {
	case LinuxAMD64:
		return "gnu64"
	case LinuxARM64:
		return "aarch64-multiplatform"
	}
	return ""
}

// cpu is the CPU name of the platform in Nix systems and triples.
func (p Platform) cpu() string {
	switch p {
	case LinuxAMD64:
		return "x86_64"
	case LinuxARM64:
		return "aarch64"
	}
	return ""
}
