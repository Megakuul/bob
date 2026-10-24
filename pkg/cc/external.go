package cc

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/cc/internal/ccspec"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
)

// BuildSystem selects how an External is built inside its derivation.
type BuildSystem int

// Build systems of an External.
const (
	CMake     BuildSystem = iota + 1 // cmake -G Ninja -DCMAKE_INSTALL_PREFIX=$out -DCMAKE_BUILD_TYPE=<profile>
	Autotools                        // ./configure --prefix=$out (autoreconf -fi if no configure)
	Meson                            // meson setup --prefix=$out --buildtype=<profile>; ninja
	Make                             // make PREFIX=$out install
)

// buildSystems maps a BuildSystem to its spec name and the nixpkgs tools it needs (besides the
// compiler, binutils, bash, coreutils and the other basic tools every External gets).
var buildSystems = map[BuildSystem]struct {
	name  string
	tools []nixext.Attr
}{
	CMake:     {ccspec.BuildCMake, []nixext.Attr{nixpkgs.Cmake, nixpkgs.Ninja}},
	Autotools: {ccspec.BuildAutotools, []nixext.Attr{nixpkgs.Gnumake, nixpkgs.Autoconf, nixpkgs.Automake, nixpkgs.Libtool, nixpkgs.M4, nixpkgs.Perl}},
	Meson:     {ccspec.BuildMeson, []nixext.Attr{nixpkgs.Meson, nixpkgs.Ninja}},
	Make:      {ccspec.BuildMake, []nixext.Attr{nixpkgs.Gnumake}},
}

// baseTools are the host tools of every External build: the shell its scripts run with and
// the programs build scripts expect on PATH.
var baseTools = []nixext.Attr{nixpkgs.Bash, nixpkgs.Coreutils, nixpkgs.Findutils, nixpkgs.Diffutils,
	nixpkgs.Gnused, nixpkgs.Gnugrep, nixpkgs.Gawk, nixpkgs.PkgConfig}

// ExternalOpts describes a third-party C/C++ project with its own build system.
type ExternalOpts struct {
	URL     string      // https archive (.tar.gz .tar.xz .tar.bz2 .tar.zst .zip); required
	Strip   int         // leading path components to strip; default 1
	Build   BuildSystem // required
	Args    []string    // extra configure args (-DFOO=ON, --disable-x, -Dfoo=false)
	Deps    []Linkable  // available to the build (CMAKE_PREFIX_PATH, PKG_CONFIG_PATH, CFLAGS/LDFLAGS)
	Tools   []bob.Tool  // extra host tools (nixpkgs.Perl.Tool(p))
	Libs    []string    // link names exported to consumers; default: from installed .pc files, else all lib*.so/lib*.a
	Include string      // include dir under $out; default "include"
}

// ExternalLib is a third-party library built by an External; it implements Linkable.
type ExternalLib struct{ bob.Node }

// LinkInfo is filled by bob's generator; the evaluator only records the library's identity.
func (e *ExternalLib) LinkInfo() bob.LinkInfo { return bob.LinkInfo{} }

// External fetches URL (pinned by narHash in bob.lock) and builds it with its own build
// system inside one derivation. To consumers it is an ordinary Linkable. Label //<dir>:<name>.
//
//	var Zstd = bob.Define(func(p *bob.P) *cc.ExternalLib {
//		return cc.External(p, "zstd", cc.ExternalOpts{
//			URL:   "https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz",
//			Build: cc.CMake, Args: []string{"-S", "build/cmake", "-DZSTD_BUILD_PROGRAMS=OFF"},
//		})
//	})
func External(p *bob.P, name string, o ExternalOpts) *ExternalLib {
	var problems []string
	strip := o.Strip
	if strip == 0 {
		strip = 1
	}
	if strip < 0 || strip > 64 {
		problems = append(problems, fmt.Sprintf("Strip %d is out of range 0..64", o.Strip))
		strip = 1
	}
	if err := checkURL(o.URL); err != nil {
		problems = append(problems, err.Error())
	}
	bs, ok := buildSystems[o.Build]
	if !ok {
		problems = append(problems, "Build is not set; choose cc.CMake, cc.Autotools, cc.Meson or cc.Make")
		bs = buildSystems[Make]
	}
	include := "include"
	if o.Include != "" {
		include = path.Clean(o.Include)
		if !localDir(include) {
			problems = append(problems, fmt.Sprintf("Include %q is not a directory below the install prefix", o.Include))
			include = "include"
		}
	}
	for _, l := range o.Libs {
		if l == "" || strings.ContainsAny(l, "/ \t\n\x00") || strings.HasPrefix(l, "-") {
			problems = append(problems, fmt.Sprintf("Libs entry %q is not a library name (\"zstd\" for -lzstd)", l))
		}
	}
	for _, a := range o.Args {
		if strings.ContainsRune(a, 0) {
			problems = append(problems, "Args contains a NUL byte")
		}
	}
	s := ccspec.External{Name: name, URL: o.URL, Strip: strip, Build: bs.name, Args: list(o.Args),
		Libs: list(o.Libs), Include: include, Tools: []int{}}
	deps := tools(p, &s.CC)
	for _, t := range append(append([]nixext.Attr{}, baseTools...), bs.tools...) {
		s.Tools = append(s.Tools, len(deps))
		deps = append(deps, t.Tool(p))
	}
	for _, t := range o.Tools {
		if t == nil {
			continue
		}
		s.Tools = append(s.Tools, len(deps))
		deps = append(deps, t)
	}
	s.Deps, deps = linkables(o.Deps, deps)
	e := &ExternalLib{Node: ext.New(p, ccspec.KindExternal, name, deps, nil, s)}
	report(&e.Node, problems)
	return e
}

// checkURL accepts an https (or, for local mirrors and tests, http) archive URL.
func checkURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("URL %q is not an https archive URL", raw)
	}
	return nil
}
