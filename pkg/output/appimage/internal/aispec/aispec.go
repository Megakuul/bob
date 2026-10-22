// Package aispec holds the IR payload ("spec") of the appimage.bundle node kind. pkg/output/appimage
// writes it during evaluation, pkg/output/appimage/gen reads it when planning. Dependency
// references are indices into the node's deps.
package aispec

// KindBundle is the node kind of appimage.NewBundle; "appimage" is the ecosystem name.
const KindBundle = "appimage.bundle"

// Bundle is the spec of one AppImage.
type Bundle struct {
	Name         string            `json:"name"`
	Entries      []Entry           `json:"entries"` // [0] is the main program (named like the bundle)
	Env          map[string]string `json:"env"`
	CACert       int               `json:"cacert"`  // dep index of nss-cacert, -1 with NoCACerts
	Locales      []string          `json:"locales"` // other than C.UTF-8, sorted
	Glibc        int               `json:"glibc"`   // dep index of the host glibc (localedef), -1 without Locales
	NoIconv      bool              `json:"noIconv"`
	HostGraphics bool              `json:"hostGraphics"`
	Desktop      Desktop           `json:"desktop"`
	Platform     string            `json:"platform"` // the bundle's platform (runtime and programs)
	Prune        []string          `json:"prune"`    // DefaultPrune already applied for nil
	Mode         string            `json:"mode"`     // auto | userns
	Squashfs     int               `json:"squashfs"` // dep index of nixpkgs squashfsTools (host)
}

// Entry is one program of the bundle.
type Entry struct {
	Name string `json:"name"`
	Dep  int    `json:"dep"`
	Main string `json:"main"` // MainProgram, relative to the dep's out
}

// Desktop is the .desktop entry; Icon reports whether the node's sources hold the icon.
type Desktop struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Terminal   bool     `json:"terminal"`
	Icon       bool     `json:"icon"`
}
