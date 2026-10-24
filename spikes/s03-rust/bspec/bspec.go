// Package bspec is the contract between bob's derivation generator and bob-builder (the static
// Go builder of every Rust derivation). The generator puts one Spec as JSON into the
// derivation environment (`bobSpec`); bob-builder executes it. No shell is involved.
package bspec

// Source is either an unpacked local source (Dir, a store path) or a .crate archive.
type Source struct {
	Dir    string `json:"dir,omitempty"`
	Crate  string `json:"crate,omitempty"`  // store path of the .crate file
	Prefix string `json:"prefix,omitempty"` // top directory inside the .crate (name-version)
}

// Rustc compiles one crate (lib/rlib, proc-macro, bin, staticlib, cdylib, or a build script).
type Rustc struct {
	Src       Source            `json:"src"`
	CrateName string            `json:"crateName"`
	CrateType string            `json:"crateType"` // lib | proc-macro | bin | staticlib | cdylib
	Edition   string            `json:"edition"`
	Root      string            `json:"root"` // relative to the source
	Features  []string          `json:"features,omitempty"`
	Metadata  string            `json:"metadata"`
	OptLevel  string            `json:"optLevel"`
	CapLints  bool              `json:"capLints,omitempty"`
	Externs   []Extern          `json:"externs,omitempty"`
	DepDirs   []string          `json:"depDirs,omitempty"`    // -L dependency= (transitive rlib dirs)
	BuildRun  string            `json:"buildRun,omitempty"`   // output of this crate's build-script run
	LinkFrom  []string          `json:"linkFrom,omitempty"`   // build-script outputs of transitive deps (link search)
	NativeLib []NativeLib       `json:"nativeLibs,omitempty"` // bob C/C++ libraries to link
	Env       map[string]string `json:"env,omitempty"`        // CARGO_PKG_* etc.
	CheckCfg  []string          `json:"checkCfg,omitempty"`
	Headers   string            `json:"headers,omitempty"` // staticlib/cdylib: include dir to export (relative)
	ExtraArgs []string          `json:"extraArgs,omitempty"`
}

type Extern struct {
	Name string `json:"name"`
	Path string `json:"path"` // store path of the dependency's output; file found by Lib
	Lib  string `json:"lib"`  // file name, e.g. libserde-0123abcd.rlib
}

type NativeLib struct {
	Dir  string `json:"dir"`  // store path containing lib/ and include/
	Name string `json:"name"` // -l name
	Kind string `json:"kind"` // static | dylib
}

// BuildRun executes a compiled build script the way cargo does.
type BuildRun struct {
	Src      Source            `json:"src"`
	Script   string            `json:"script"` // store path of the build script's output dir
	Env      map[string]string `json:"env"`
	DepLinks []DepLinks        `json:"depLinks,omitempty"` // direct deps with `links`: DEP_<LINKS>_<KEY>
}

type DepLinks struct {
	Links string `json:"links"`
	Out   string `json:"out"` // build-run output of that dependency
}

// Directives is what bob-builder writes to $out/directives.json after a build-script run.
type Directives struct {
	Cfgs        []string          `json:"cfgs,omitempty"`
	CheckCfgs   []string          `json:"checkCfgs,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	LinkLibs    []string          `json:"linkLibs,omitempty"`   // values for -l
	LinkSearch  []string          `json:"linkSearch,omitempty"` // values for -L
	LinkArgs    []string          `json:"linkArgs,omitempty"`   // -C link-arg for bins/cdylibs of this package
	Metadata    map[string]string `json:"metadata,omitempty"`   // for DEP_<LINKS>_<KEY>
	Warnings    []string          `json:"warnings,omitempty"`
	Unsupported []string          `json:"unsupported,omitempty"`
}

// CC compiles a small C library or program (stand-in for bob's cc builder).
type CC struct {
	Src      string      `json:"src"`
	Srcs     []string    `json:"srcs"`
	Hdrs     string      `json:"hdrs,omitempty"`
	Name     string      `json:"name"`
	Kind     string      `json:"kind"` // static | shared | bin
	Deps     []NativeLib `json:"deps,omitempty"`
	LinkArgs []string    `json:"linkArgs,omitempty"`
}

// Go builds a cgo binary from a local module without external modules.
type Go struct {
	Src     string      `json:"src"`
	Name    string      `json:"name"`
	Main    string      `json:"main"`
	Deps    []NativeLib `json:"deps,omitempty"`
	LdExtra []string    `json:"ldExtra,omitempty"`
}

// FetchCrate downloads a .crate, checks its sha256 and unpacks it (top directory stripped) into
// $out. It runs as a recursive fixed-output derivation, so Nix verifies the unpacked tree too.
type FetchCrate struct {
	URL      string `json:"url"`
	Checksum string `json:"checksum"` // SRI sha256 of the .crate file
	CACert   string `json:"caCert"`
}
