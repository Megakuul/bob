package bop

// Job args of the Go kinds (bspec.Job.Args). pkg/golang/gen writes the same JSON with its own
// types (generators may not import the builder layer, R-ARCH-003); the args are decoded
// strictly, so a mismatch fails the job as invalid instead of building something else.
// TestGenJobsDecode checks gen's jobs against these types.

// Toolchain is the Go toolchain and target of a job.
type Toolchain struct {
	GoRoot string   `json:"goroot"` // <go>/share/go
	GOOS   string   `json:"goos"`
	GOARCH string   `json:"goarch"`
	Path   []string `json:"path"` // PATH of the job: the C toolchain's bin directories
	CC     string   `json:"cc"`   // C compiler for cgo and external linking; "" = cgo off
	// Env is the cross environment of SPEC R-FW-033 (GOOS, GOARCH, CC, CXX); GOOS and GOARCH
	// must equal the fields above, CC and CXX name the target's compilers. Empty natively.
	Env map[string]string `json:"env,omitempty"`
	// GCFlags are the variant's compile flags (-N -l for debug, -race, -asan), given to every
	// compile and, for go-std, to go list.
	GCFlags []string `json:"gcflags,omitempty"`
	// LDFlags are link flags: -race, -asan, and -extld=<target C compiler> when cross.
	LDFlags []string `json:"ldflags,omitempty"`
	// CFlags are added to the C compiles of cgo and to an external link (-O0 -g, -fsanitize=…).
	CFlags []string `json:"cflags,omitempty"`
}

// StdArgs is the args of kind go-std: the standard library compiled once per toolchain,
// target and cgo setting. Output: $out/pkg/<importpath>.a and $out/importcfg.
type StdArgs struct {
	Toolchain
	Out string `json:"out"`
}

// ToolsArgs is the args of kind go-tools: the Go tools nixpkgs' Go does not ship prebuilt
// (cmd/pack). Output: $out/bin/pack.
type ToolsArgs struct {
	Toolchain
	Out string `json:"out"`
}

// BuildArgs is the args of kinds go-compile, go-cgo, go-link and go-testmain: compile Packages
// in order (dependencies first), then link if Link is set.
type BuildArgs struct {
	Toolchain
	Std      string    `json:"std"`  // importcfg of the go-std output
	Pack     string    `json:"pack"` // the pack program (cgo, assembly, .syso); "" if unused
	Packages []Package `json:"packages"`
	Link     *Link     `json:"link,omitempty"`
}

// Package is one package compile.
type Package struct {
	ImportPath string `json:"importPath"`
	Name       string `json:"name"` // package name (cgo -dynpackage)
	// Flags are the planner's compile flags: "-p <path>" ("main" for commands) and -lang.
	Flags []string `json:"flags"`
	Dir   string   `json:"dir"` // source directory ("" = the work directory)
	// Write holds generated sources (the test main): file name → content, written to the
	// package's work directory; GoFiles names them relative to it when Dir is "".
	Write     map[string]string `json:"write,omitempty"`
	GoFiles   []string          `json:"goFiles"` // relative to Dir; cgo files are in CgoFiles
	CgoFiles  []string          `json:"cgoFiles,omitempty"`
	CFiles    []string          `json:"cFiles,omitempty"`
	SFiles    []string          `json:"sFiles,omitempty"`
	SysoFiles []string          `json:"sysoFiles,omitempty"`
	CFlags    []string          `json:"cflags,omitempty"`  // cgo CFLAGS: #cgo directives, then the deps' -I
	LDFlags   []string          `json:"ldflags,omitempty"` // cgo LDFLAGS: #cgo directives, then the deps' -L/-l
	Imports   map[string]string `json:"imports"`           // import path → archive or export data
	Embed     *Embed            `json:"embed,omitempty"`
	Trim      string            `json:"trim"` // what Dir becomes in recorded paths ("<module>@<v>/<rel>")
	Out       string            `json:"out"`  // the archive
	// Export, when set, receives the export data alone (-linkobj split, R-GO-005); Out then
	// holds the object code for the link.
	Export string `json:"export,omitempty"`
	// Header, when set, receives cgo's export header (C libraries, -buildmode=c-archive).
	Header string `json:"header,omitempty"`
}

// Embed is the -embedcfg of a package: patterns → files, files relative to Dir.
type Embed struct {
	Patterns map[string][]string `json:"patterns"`
	Files    []string            `json:"files"`
}

// Link links a program or C library.
type Link struct {
	Main     string            `json:"main"`     // archive of the main package
	Packages map[string]string `json:"packages"` // every non-std package of the closure → archive
	Out      string            `json:"out"`
	Mode     string            `json:"mode"` // exe | c-archive | c-shared
	X        []string          `json:"x"`    // -X importpath.name=value, sorted
	ModInfo  string            `json:"modinfo"`
	External bool              `json:"external"` // link with the C compiler (cgo in the closure)
	// Trim links as cmd/go -trimpath does: GOROOT is cleared for the linker, so the binary does
	// not embed runtime.defaultGOROOT (a reference to the toolchain, SPEC R-NIX-019).
	Trim bool `json:"trim,omitempty"`
	// Strip drops the symbol table and DWARF (-s -w): release binaries (R-NIX-019 strip step).
	Strip bool `json:"strip,omitempty"`
}
