package bop

import "github.com/megakuul/bob/pkg/builder/bspec"

// Args is the Args schema of kind "appimage" (SPEC R-OUT-015). Every path is a store path the
// derivation has as an input; the closure of the roots comes from the structured attribute
// exportReferencesGraph.closure.
type Args struct {
	Name         string                       `json:"name"`       // bundle name: <name>-<arch>.AppImage, <name>.desktop
	Arch         string                       `json:"arch"`       // x86_64 | aarch64
	Runtime      string                       `json:"runtime"`    // the bob AppImage runtime binary for Arch
	Mksquashfs   string                       `json:"mksquashfs"` // nixpkgs squashfsTools bin/mksquashfs (host)
	Entry        string                       `json:"entry"`      // main program: <out>/<MainProgram>
	Entries      map[string]string            `json:"entries"`    // name → <out>/<MainProgram>
	Closure      string                       `json:"closure"`    // attribute holding the exportReferencesGraph
	CACert       string                       `json:"cacert"`     // nss-cacert output ("" with NoCACerts)
	Env          map[string]string            `json:"env"`        // user Env, overrides the defaults
	NoIconv      bool                         `json:"noIconv"`
	Locales      []string                     `json:"locales"`   // besides C.UTF-8 (built into an archive)
	Localedef    string                       `json:"localedef"` // glibc bin/localedef (host) when Locales is set
	I18n         string                       `json:"i18n"`      // glibc share/i18n (locale sources) when Locales is set
	HostGraphics bool                         `json:"hostGraphics"`
	Mode         string                       `json:"mode"`  // auto | userns
	Prune        []string                     `json:"prune"` // globs over /nix/store/<hash>-<name>/<path>
	Desktop      Desktop                      `json:"desktop"`
	Licences     map[string]bspec.LicenceInfo `json:"licences"` // store path → licence data (nixpkgs meta)
}

// Desktop is the .desktop entry and the icon.
type Desktop struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Terminal   bool     `json:"terminal"`
	Icon       string   `json:"icon"` // file (SVG or PNG) in a source store path; "" = placeholder
}
