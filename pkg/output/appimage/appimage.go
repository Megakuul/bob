package appimage

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
)

// Bundle is a self-contained AppImage for one platform.
type Bundle struct{ bob.Node }

// BundleOpts configures a bundle.
type BundleOpts struct {
	Program      bob.Executable            // main entry
	Entries      map[string]bob.Executable // extra entries, chosen by BOB_ENTRY or argv[0]
	Env          map[string]string         // "${STORE}" expands to the bundled store at run time
	NoCACerts    bool                      // default false: nss-cacert, SSL_CERT_FILE set
	Locales      []string                  // default {"C.UTF-8"}; others add a locale-archive
	NoIconv      bool                      // true prunes glibc lib/gconv (iconv only builtin charsets)
	HostGraphics bool                      // append host GPU library dirs (R-OUT-020)
	Desktop      *Desktop                  // nil → Terminal=true entry, built-in placeholder icon
	Platform     bob.Platform              // default host
	Prune        []string                  // default DefaultPrune; nil-vs-empty: []string{} = no pruning
	Mode         Mode                      // AutoMode (default), UsernsOnly
}

// Desktop describes the .desktop entry.
type Desktop struct {
	Name       string      // Name= in the .desktop file
	Icon       bob.Sources // SVG or PNG, 256x256 (SPEC names the type bob.Source)
	Categories []string    //
	Terminal   bool        //
}

// Mode restricts the launcher. UsernsOnly is required for programs that exec other
// store paths or are shebang scripts (R-OUT-019).
type Mode int

const (
	AutoMode Mode = iota
	UsernsOnly
)

// DefaultPrune is the prune set used when Prune is nil.
var DefaultPrune = []string{
	"/nix/store/*-glibc-*/share/i18n/**",
	"/nix/store/*-glibc-*/share/locale/**",
	"/nix/store/*-gcc-*-lib/lib/lib?san.so*",
	"/nix/store/*-gcc-*-lib/lib/libhwasan.so*",
}

// NewBundle declares an AppImage.
func NewBundle(p *bob.P, name string, opts BundleOpts) *Bundle {
	deps := []bob.Package{opts.Program}
	for _, e := range opts.Entries {
		deps = append(deps, e)
	}
	return &Bundle{Node: ext.New(p, "appimage.bundle", name, deps, nil, opts.Mode)}
}
