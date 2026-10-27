package bob

import "strings"

// Toolchain describes the C/C++ compilers an ecosystem uses for one (host, target) pair (SPEC
// R-FW-033). Builders in pkg/<ecosystem> receive it from p.Toolchain; users never construct one.
//
// The compiler packages themselves are typed nixpkgs handles that bob's ecosystem packages get
// from nixext.Toolchain(p) (CC, Bintools, CCLib, Emulator; ADR 0020): Toolchain carries no
// attribute strings.
type Toolchain struct {
	Host, Target Platform
	Triple       string // "x86_64-unknown-linux-gnu"; empty for native
	Prefix       string // "x86_64-unknown-linux-gnu-" for cross, "" for native
	// EmulatorCmd is the command of the user-mode emulator in its bin/ that runs the target's
	// test programs on the host (R-FW-039); "" natively.
	EmulatorCmd string
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
	t := Toolchain{Host: host, Target: target}
	if host == target || target.CrossSet() == "" {
		t.Target = host
		return t
	}
	t.Triple = target.Triple()
	t.Prefix = t.Triple + "-"
	t.EmulatorCmd = "qemu-" + target.cpu()
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
