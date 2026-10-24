package bop

// Job args of the C/C++ kinds (bspec.Job.Args). pkg/cc/gen writes the same JSON with its own
// types (generators may not import the builder layer, R-ARCH-003); the args are decoded
// strictly, so a mismatch fails the job as invalid instead of building something else.
// TestGenJobsDecode in pkg/cc/gen checks gen's jobs against these types.

// CompileLinkArgs is the args of kind cc-compile-link: compile every object (in parallel),
// then archive or link them, and copy the public headers.
type CompileLinkArgs struct {
	Src  string   `json:"src"`  // working directory of every command: the source tree
	Path []string `json:"path"` // PATH: the compiler's and binutils' bin directories
	// PkgConfig are libraries described by pkg-config files; an argv element "@cflags:<i>" or
	// "@libs:<i>" expands to the compile or link flags of entry i.
	PkgConfig []PkgConfig `json:"pkgConfig"`
	Headers   []Copy      `json:"headers"` // public headers copied into the output
	Compile   []Command   `json:"compile"` // one per object
	Link      Command     `json:"link"`    // the archive or link step
}

// Command is one program run; Out is the file it creates (its directory is created first).
type Command struct {
	Argv []string `json:"argv"`
	Out  string   `json:"out"`
}

// Copy copies the tree From to To.
type Copy struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// PkgConfig is one library found through pkg-config files.
type PkgConfig struct {
	// Roots are the library's store outputs. Its .pc files are in <root>/lib/pkgconfig and
	// <root>/share/pkgconfig, and in those of the packages listed in
	// <root>/nix-support/propagated-build-inputs.
	Roots   []string `json:"roots"`
	Modules []string `json:"modules"` // the modules to use; empty = every .pc file of the roots
	// Include and LibDir are used when the roots have no .pc file: -I for each Include,
	// -L LibDir and -l for every lib*.so / lib*.a in it.
	Include []string `json:"include"`
	LibDir  string   `json:"libDir"`
}

// ExternalArgs is the args of kind cc-external: copy the source to a writable directory and
// build and install it with its own build system into Out.
type ExternalArgs struct {
	Src     string            `json:"src"`     // the unpacked, pinned source
	Build   string            `json:"build"`   // cmake | autotools | meson | make
	Args    []string          `json:"args"`    // extra configure arguments
	Profile string            `json:"profile"` // release | debug
	Path    []string          `json:"path"`    // PATH: compiler, binutils, the build system's tools
	Shell   string            `json:"shell"`   // bash, for make and configure scripts
	Env     map[string]string `json:"env"`     // CC, CXX, CFLAGS, LDFLAGS, PKG_CONFIG_PATH, CMAKE_PREFIX_PATH
	Out     string            `json:"out"`     // install prefix
}
