package appimage

import (
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/bob/ext"
	"github.com/megakuul/bob/pkg/nixpkgs"
	"github.com/megakuul/bob/pkg/nixpkgs/nixext"
	"github.com/megakuul/bob/pkg/output/appimage/internal/aispec"
)

// The bundle kind reads no variant axis itself: its programs carry their own configuration.
func init() { ext.Register(ext.Kind{Name: aispec.KindBundle}) }

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

// roots are what a nested evaluation (ext.Elsewhere) records for the outer one: the bundle's
// programs on the bundle's platform.
type roots struct {
	program bob.Executable
	entries map[string]bob.Executable
}

var (
	entryName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9_+][A-Za-z0-9._+-]*$`) })
	envName   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`) })
	locale    = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^[A-Za-z]{2,3}(_[A-Z]{2})?\.[A-Za-z0-9-]+(@[a-z]+)?$`)
	})
)

// NewBundle declares an AppImage. Example:
//
//	var Bundle = bob.Define(func(p *bob.P) *appimage.Bundle {
//		return appimage.NewBundle(p, "api", appimage.BundleOpts{
//			Program:  api.Binary(p),
//			Platform: bob.LinuxAMD64,
//		})
//	})
func NewBundle(p *bob.P, name string, opts BundleOpts) *Bundle {
	dir, _ := ext.CallerDir()
	key := aispec.KindBundle + ":" + dir + ":" + name
	if ext.Nested(p) {
		// The outer run of this constructor asked for the programs on this platform.
		ext.Record(p, key, roots{program: opts.Program, entries: opts.Entries})
		return &Bundle{}
	}
	var problems []string
	plat := opts.Platform
	if plat == "" {
		plat = p.Platform()
	}
	r := roots{program: opts.Program, entries: opts.Entries}
	if plat != p.Platform() {
		q := ext.Elsewhere(p, plat)
		if q == nil {
			return &Bundle{} // unknown platform: Elsewhere recorded the definition error
		}
		v, _ := ext.Recorded(q, key)
		r, _ = v.(roots)
	}
	spec := aispec.Bundle{Name: name, Env: maps.Clone(opts.Env), CACert: -1, Glibc: -1, NoIconv: opts.NoIconv,
		HostGraphics: opts.HostGraphics, Platform: string(plat), Mode: "auto", Locales: []string{}}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	var deps []bob.Package
	add := func(pk bob.Package) int {
		deps = append(deps, pk)
		return len(deps) - 1
	}
	addEntry := func(n string, e bob.Executable) {
		if e == nil || reflect.ValueOf(e).Kind() == reflect.Pointer && reflect.ValueOf(e).IsNil() {
			problems = append(problems, fmt.Sprintf("entry %q has no program", n))
			return
		}
		main := e.MainProgram()
		if main == "" || strings.HasPrefix(main, "/") || slices.Contains(strings.Split(main, "/"), "..") {
			problems = append(problems, fmt.Sprintf("entry %q: %s has no main program inside its output", n, e.Label()))
			return
		}
		spec.Entries = append(spec.Entries, aispec.Entry{Name: n, Dep: add(e), Main: main})
	}
	if r.program == nil {
		problems = append(problems, "Program is required")
	} else {
		addEntry(name, r.program)
	}
	for _, n := range slices.Sorted(maps.Keys(r.entries)) {
		if !entryName().MatchString(n) {
			problems = append(problems, fmt.Sprintf("entry name %q must consist of letters, digits and ._+-", n))
			continue
		}
		addEntry(n, r.entries[n])
	}
	for k := range spec.Env {
		if !envName().MatchString(k) {
			problems = append(problems, fmt.Sprintf("Env name %q is not a valid environment variable name", k))
		}
	}
	for _, l := range opts.Locales {
		switch {
		case l == "C.UTF-8":
		case !locale().MatchString(l):
			problems = append(problems, fmt.Sprintf("locale %q is not <language>_<TERRITORY>.<charset>", l))
		default:
			spec.Locales = append(spec.Locales, l)
		}
	}
	slices.Sort(spec.Locales)
	spec.Locales = slices.Compact(spec.Locales)
	switch {
	case opts.Prune == nil:
		spec.Prune = slices.Clone(DefaultPrune)
	default:
		spec.Prune = slices.Clone(opts.Prune)
	}
	for _, g := range spec.Prune {
		if !strings.HasPrefix(g, "/nix/store/") {
			problems = append(problems, fmt.Sprintf("prune pattern %q must start with /nix/store/", g))
		}
	}
	switch opts.Mode {
	case AutoMode:
	case UsernsOnly:
		spec.Mode = "userns"
	default:
		problems = append(problems, fmt.Sprintf("unknown Mode %d", opts.Mode))
	}
	var srcs []bob.Sources
	if d := opts.Desktop; d != nil {
		spec.Desktop = aispec.Desktop{Name: d.Name, Categories: slices.Clone(d.Categories), Terminal: d.Terminal}
		if !reflect.ValueOf(d.Icon).IsZero() {
			spec.Desktop.Icon = true
			srcs = []bob.Sources{d.Icon}
		}
	} else {
		spec.Desktop.Terminal = true
	}
	if spec.Desktop.Categories == nil {
		spec.Desktop.Categories = []string{}
	}
	// Build tools and data come from the host set: mksquashfs and localedef run at build time,
	// CA certificates are the same for every platform.
	spec.Squashfs = add(nixpkgs.SquashfsTools.Tool(p.Host()))
	if !opts.NoCACerts {
		spec.CACert = add(nixpkgs.Cacert.Pkg(p.Host()))
	}
	if len(spec.Locales) > 0 {
		spec.Glibc = add(nixpkgs.Glibc.Tool(p.Host(), nixext.Outputs(nixext.OutBin, nixext.Out)))
	}
	b := &Bundle{Node: ext.New(p, aispec.KindBundle, name, deps, srcs, spec)}
	for _, pr := range problems {
		ext.Errorf("%s: %s: invalid BundleOpts: %s", b.DefinedAt(), b.Label(), pr)
	}
	return b
}
