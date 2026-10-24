package gen

import (
	"fmt"
	"sort"
	"strings"
)

// Platform is a bob target platform. Derivations always run on the host (their Nix system is the
// host system); the target only changes compilers, flags and target libraries.
type Platform struct {
	Name   string // bob name: linux-amd64 | linux-arm64
	Nix    string // x86_64-linux | aarch64-linux
	GOARCH string // amd64 | arm64
	Rust   string // x86_64-unknown-linux-gnu | aarch64-unknown-linux-gnu
	Cross  string // nixpkgs pkgsCross attribute set used to reach this target from the other host
}

var Platforms = map[string]Platform{
	"linux-amd64": {"linux-amd64", "x86_64-linux", "amd64", "x86_64-unknown-linux-gnu", "gnu64"},
	"linux-arm64": {"linux-arm64", "aarch64-linux", "arm64", "aarch64-unknown-linux-gnu", "aarch64-multiplatform"},
}

// Variant is the full build configuration a user asks for. Each package kind only sees the part
// that matters to it (Project), and only that part enters its derivation.
type Variant struct {
	Profile  string              // release | debug
	Sanitize []string            // address, undefined (C/C++, cgo, Go -asan)
	GoRace   bool                // Go -race
	Features map[string][]string // Rust crate label -> extra features
	// TargetLibs says where nixpkgs libraries for a cross target come from:
	// "cross" = pkgsCross.<x>.<attr> (built by the host's cross toolchain, often not cached),
	// "native" = the target system's own nixpkgs package (cached by Hydra).
	TargetLibs string
}

// Proj is the projection of a Variant onto one package: what actually enters its derivation.
type Proj struct {
	Opt      string // "rel" | "dbg"
	Sanitize []string
	Race     bool
	Features []string
}

func (p Proj) Tag() string {
	var t []string
	if p.Opt == "dbg" {
		t = append(t, "dbg")
	}
	t = append(t, p.Sanitize...)
	if p.Race {
		t = append(t, "race")
	}
	for _, f := range p.Features {
		t = append(t, "f-"+f)
	}
	return strings.Join(t, "-")
}

// Cross reports whether target differs from the host.
func (g *Gen) Cross() bool { return g.Tgt.Name != g.Host.Name }

// suffix is appended to derivation names so a store path shows platform and variant.
func (g *Gen) suffix(pr Proj) string {
	s := ""
	if g.Cross() {
		s += "-" + g.Tgt.Name
	}
	if t := pr.Tag(); t != "" {
		s += "-" + t
	}
	return s
}

// ccProj: C/C++ objects see profile and sanitisers.
func (g *Gen) ccProj() Proj {
	return Proj{Opt: opt(g.V.Profile), Sanitize: g.V.Sanitize}
}

// rustProj: stable rustc has no sanitisers; profile and the crate's own features apply.
func (g *Gen) rustProj(label string) Proj {
	f := append([]string{}, g.V.Features[label]...)
	sort.Strings(f)
	return Proj{Opt: opt(g.V.Profile), Features: f}
}

// goProj: Go packages see profile (-N -l), address sanitiser (-asan) and -race.
func (g *Gen) goProj() Proj {
	var s []string
	for _, x := range g.V.Sanitize {
		if x == "address" {
			s = append(s, x)
		}
	}
	return Proj{Opt: opt(g.V.Profile), Sanitize: s, Race: g.V.GoRace}
}

func opt(profile string) string {
	if profile == "debug" {
		return "dbg"
	}
	return "rel"
}

// CCAttr is the nixpkgs attribute of the C toolchain that targets t from the host.
func (g *Gen) CCAttr() string {
	if g.Cross() {
		return "pkgsCross." + g.Tgt.Cross + ".stdenv.cc"
	}
	return "stdenv.cc"
}

// CCPrefix is the command prefix of that toolchain (x86_64-unknown-linux-gnu-gcc).
func (g *Gen) CCPrefix() string {
	if g.Cross() {
		return strings.Replace(g.Tgt.Rust, "-unknown-", "-unknown-", 1) + "-"
	}
	return ""
}

// LibAttr maps a nixpkgs library attribute to the attribute that provides it for the target.
func (g *Gen) LibAttr(a string) string {
	if !g.Cross() {
		return a
	}
	if g.V.TargetLibs == "native" {
		return "@" + g.Tgt.Nix + "/" + a
	}
	return "pkgsCross." + g.Tgt.Cross + "." + a
}

func (pr Proj) ccFlags() string {
	f := "-O2"
	if pr.Opt == "dbg" {
		f = "-O0 -g"
	}
	if len(pr.Sanitize) > 0 {
		f += fmt.Sprintf(" -fsanitize=%s -fno-omit-frame-pointer", strings.Join(pr.Sanitize, ","))
	}
	return f
}

func (pr Proj) sanLink() string {
	if len(pr.Sanitize) == 0 {
		return ""
	}
	return "-fsanitize=" + strings.Join(pr.Sanitize, ",")
}

// goLinkFlags: what the variant adds to the final `link`.
func (g *Gen) goLinkFlags(pr Proj) string {
	var f []string
	if pr.Race {
		f = append(f, "-race")
	}
	if len(pr.Sanitize) > 0 {
		f = append(f, "-asan")
	}
	return strings.Join(f, " ")
}
