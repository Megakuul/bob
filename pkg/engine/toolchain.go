package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/diag"
	"github.com/megakuul/bob/pkg/engine/ir"
)

// Platforms and toolchains on the engine side (SPEC §04.1-§04.2, R-PLAT-004, R-FW-033): every
// derivation bob generates runs on the host system; the target platform only selects
// toolchains, flags and target libraries. The C/C++ toolchain table itself is bob.ToolchainFor
// (the evaluator needs it to pick the compiler packages); this file adds what generators and
// the build need besides: node platforms, Go and Rust settings, the source of nixpkgs packages,
// the emulator for foreign test binaries and the nixpkgs consistency pre-check.

// HostPlatform is the platform bob runs on ("" on an unsupported architecture).
func HostPlatform() bob.Platform {
	switch runtime.GOARCH {
	case "amd64":
		return bob.LinuxAMD64
	case "arm64":
		return bob.LinuxARM64
	}
	return ""
}

// HostSystem is the Nix system of every derivation bob generates on this machine.
func HostSystem() string { return HostPlatform().NixSystem() }

// NodePlatform returns the platform a node is built for: its projected platform, else the
// host's (nodes without a platform axis are host-independent).
func NodePlatform(n *ir.Node) (bob.Platform, error) {
	raw, ok := n.Proj["platform"]
	if !ok {
		return HostPlatform(), nil
	}
	var p string
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("%s: platform: %w", n.Label, err)
	}
	if bob.Platform(p).NixSystem() == "" {
		return "", fmt.Errorf("%s: unsupported platform %q", n.Label, p)
	}
	return bob.Platform(p), nil
}

// CToolchain is the C/C++ toolchain of a node, as the evaluator selected it (R-FW-033).
func CToolchain(n *ir.Node) (bob.Toolchain, error) {
	p, err := NodePlatform(n)
	if err != nil {
		return bob.Toolchain{}, err
	}
	return bob.ToolchainFor(HostPlatform(), p), nil
}

// GoTarget is the Go side of the toolchain table (R-FW-033): the host's go with GOOS/GOARCH of
// the target; cgo and external links use the target's C compiler.
type GoTarget struct {
	Platform     bob.Platform // the target
	GOOS, GOARCH string
	CC, CXX      string // C and C++ compiler commands ("gcc", "x86_64-unknown-linux-gnu-gcc")
	Cross        bool
}

// GoFor returns the Go settings that compile on host for target.
func GoFor(host, target bob.Platform) GoTarget {
	tc := bob.ToolchainFor(host, target)
	return GoTarget{Platform: tc.Target, GOOS: "linux", GOARCH: tc.Target.GOARCH(), CC: tc.Prefix + "gcc", CXX: tc.Prefix + "g++", Cross: tc.Cross()}
}

// Env is the environment the table names for a cross build (GOOS, GOARCH, CC, CXX); nil
// natively, where the defaults apply.
func (g GoTarget) Env() map[string]string {
	if !g.Cross {
		return nil
	}
	return map[string]string{"GOOS": g.GOOS, "GOARCH": g.GOARCH, "CC": g.CC, "CXX": g.CXX}
}

// RustTarget is the Rust side of the toolchain table (R-FW-033, R-FW-034): the host's rustc;
// for a cross target with --target Triple, a sysroot holding the target's standard library
// copied from the target system's own rustc (StdAttr in the package set of StdSystem), and the
// cross C compiler as linker.
type RustTarget struct {
	Triple    string // target triple, also natively ("aarch64-unknown-linux-gnu")
	Cross     bool
	StdAttr   string // nixpkgs attribute of the rustc whose lib/rustlib/<Triple> is the target std
	StdSystem string // Nix system of the package set StdAttr is taken from
	Linker    string // linker command ("x86_64-unknown-linux-gnu-cc"; "cc" natively)
	StdName   string // derivation name of the target std ("rust-std-x86_64-unknown-linux-gnu")
}

// RustFor returns the Rust settings that compile on host for target. Rust host units
// (proc-macros, build-script compiles) always use RustFor(host, host) (R-FW-035).
func RustFor(host, target bob.Platform) RustTarget {
	tc := bob.ToolchainFor(host, target)
	r := RustTarget{Triple: tc.Target.Triple(), Cross: tc.Cross(), Linker: tc.Prefix + "cc"}
	if r.Cross {
		r.StdAttr, r.StdSystem, r.StdName = "rustc.unwrapped", tc.Target.NixSystem(), "rust-std-"+r.Triple
	}
	return r
}

// NixpkgsSource returns where the nixpkgs package of node n comes from: the Nix system of the
// package set and the attribute in it (R-FW-032, R-FW-036). Tools come from the host set; a
// Lib or Pkg for a cross target from the target system's own set, or with Cross() from the
// host's pkgsCross set; natively all three are the host set's attribute.
func NixpkgsSource(n *ir.Node) (system, attr string, err error) {
	if n.Nix == nil {
		return "", "", fmt.Errorf("%s is not a nixpkgs package", n.Label)
	}
	host := HostPlatform()
	p, err := NodePlatform(n)
	if err != nil {
		return "", "", err
	}
	if n.Nix.Set == "host" || p == host {
		return host.NixSystem(), n.Nix.Attr, nil
	}
	if n.Nix.Set == "cross" {
		return host.NixSystem(), "pkgsCross." + p.CrossSet() + "." + n.Nix.Attr, nil
	}
	return p.NixSystem(), n.Nix.Attr, nil
}

// NativeOnly lists the ecosystems that cannot cross-compile in v1 (R-PLAT-005), by kind prefix,
// with the name used in messages.
var nativeOnly = map[string]string{"ocaml": "OCaml"}

// CrossRefused returns the error for a node of a native-only ecosystem requested for another
// platform than the host's, or nil.
func CrossRefused(n *ir.Node) error {
	eco, _, _ := strings.Cut(n.Kind, ".")
	name, ok := nativeOnly[eco]
	if !ok {
		return nil
	}
	p, err := NodePlatform(n)
	if err != nil || p == HostPlatform() {
		return nil
	}
	// SPEC R-PLAT-005 text: "<label>: OCaml packages cannot be cross-compiled yet (requested
	// <p> on a <host> host)"; §02.3 has no code for it yet, 1010 is the nearest (I-M7b NEEDS).
	e := diag.New(1010, "label", n.Label, "system", p.NixSystem())
	e.Cause = fmt.Sprintf("%s packages cannot be cross-compiled yet (requested %s on a %s host)", name, p, HostPlatform())
	e.DefinedAt = diag.Pos{File: n.DefinedAt.File, Line: n.DefinedAt.Line}
	return e
}

// Versions are the facts the nixpkgs consistency pre-check compares (R-PLAT-007).
type Versions struct {
	Glibc string `json:"glibc"`
	Rustc string `json:"rustc"`
}

// TestIndexEnv names a JSON file {"native": Versions, "cross": Versions} that replaces the
// versions looked up in the nixpkgs index (tests/platforms R-PLAT-007): "native" are the target
// system's own packages, "cross" the cross toolchain's glibc and the host's rustc.
const TestIndexEnv = "BOB_TEST_NIXPKGS_INDEX"

// TestVersions reads the file named by TestIndexEnv; ok is false when it is not set.
func TestVersions() (native, cross Versions, ok bool, err error) {
	f := os.Getenv(TestIndexEnv)
	if f == "" {
		return native, cross, false, nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return native, cross, false, err
	}
	if len(b) > 1<<16 {
		return native, cross, false, fmt.Errorf("%s: %s is larger than 64 KiB", TestIndexEnv, f)
	}
	var doc struct{ Native, Cross Versions }
	if err := json.Unmarshal(b, &doc); err != nil {
		return native, cross, false, fmt.Errorf("%s: %w", TestIndexEnv, err)
	}
	return doc.Native, doc.Cross, true, nil
}

// CheckVersions is the pre-check of R-PLAT-007 for one cross target: with target libraries from
// the target system's own set (libs), its glibc must equal the cross toolchain's; with Rust code
// (rust), the target system's rustc must equal the host's (its standard library is the target
// std). An empty version is unknown and not compared.
func CheckVersions(target bob.Platform, native, cross Versions, libs, rust bool) error {
	var errs []error
	if libs && native.Glibc != "" && cross.Glibc != "" && native.Glibc != cross.Glibc {
		errs = append(errs, mismatch(fmt.Sprintf("target libraries for %s use glibc %s but the cross toolchain uses glibc %s",
			target, native.Glibc, cross.Glibc), "set Libs: cc.CrossLibs on the packages that link them, or move the nixpkgs pin"))
	}
	if rust && native.Rustc != "" && cross.Rustc != "" && native.Rustc != cross.Rustc {
		errs = append(errs, mismatch(fmt.Sprintf("the Rust standard library for %s comes from rustc %s but the host compiler is rustc %s",
			target, native.Rustc, cross.Rustc), "the nixpkgs pin must provide the same rustc for both systems"))
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	}
	return errorList(errs)
}

// mismatch is the R-PLAT-007 error. SPEC: "✗ nixpkgs mismatch: <what>" with the hint; §02.3
// has no code for it, so it uses 1021 (toolchain mismatch) with label "nixpkgs" until one is
// assigned (I-M7b NEEDS). The hint is kept in Cause.
func mismatch(what, hint string) *diag.Error {
	e := diag.New(1021, "label", "nixpkgs", "reason", what)
	e.Cause = hint
	return e
}

// errorList joins errors so that callers still see each diagnostic (Unwrap []error).
type errorList []error

func (l errorList) Error() string {
	s := make([]string, len(l))
	for i, e := range l {
		s[i] = e.Error()
	}
	return strings.Join(s, "\n")
}

func (l errorList) Unwrap() []error { return l }
