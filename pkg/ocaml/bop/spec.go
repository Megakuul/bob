package bop

// Job args of the OCaml kinds (bspec.Job.Args). pkg/ocaml/gen writes the same JSON with its own
// types (generators may not import the builder layer, R-ARCH-003); the args are decoded
// strictly, so a mismatch fails the job as invalid instead of building something else.
// TestGenJobsDecode in pkg/ocaml/gen checks gen's jobs against these types.

// JobsMarker in an opam command argument stands for the job's parallelism (opam's %{jobs}%):
// bob-builder replaces it with NIX_BUILD_CORES, so the derivation does not depend on the
// machine.
const JobsMarker = "@@BOB_JOBS@@"

// OpamBuildArgs is the args of kind opam-build: unpack the package's source, run its opam
// build: and install: commands (rendered by bob) in it, then opam-installer on <Name>.install.
type OpamBuildArgs struct {
	Name      string            `json:"name"`      // opam package name
	Src       string            `json:"src"`       // source archive; "" = the package has none
	Path      []string          `json:"path"`      // PATH: tool bin dirs, then the deps' bin dirs
	Env       map[string]string `json:"env"`       // OCAMLPATH, OCAMLFIND_DESTDIR, … (added to the base environment)
	Commands  [][]string        `json:"commands"`  // build then install commands, run in the source dir
	Installer string            `json:"installer"` // opam-installer binary
	Out       string            `json:"out"`       // the output (opam's prefix)
}

// CompileArgs is the args of kind ml-compile: one local OCaml library. Direct mode compiles
// with ocamlfind ocamlopt (ocamldep order, dune-compatible wrapping), dune mode writes a dune
// file and runs dune build -p.
type CompileArgs struct {
	Name     string            `json:"name"`     // library name (findlib name, module namespace)
	Src      string            `json:"src"`      // source store path (repository-relative files)
	Dir      string            `json:"dir"`      // the library's directory below Src ("" = root)
	Modules  []string          `json:"modules"`  // .ml/.mli files relative to Dir
	Stubs    []string          `json:"stubs"`    // .c files relative to Dir
	Wrapped  bool              `json:"wrapped"`  // dune-style Name__Module wrapping
	Packages []string          `json:"packages"` // findlib packages the modules use (also META requires)
	Flags    []string          `json:"flags"`    // extra ocamlopt flags
	CFlags   []string          `json:"cflags"`   // C stub compile flags (-I of the Native libraries)
	CLibs    []string          `json:"clibs"`    // C link arguments recorded in the archive (-L…, -l…, archives)
	Path     []string          `json:"path"`
	Env      map[string]string `json:"env"`
	Dune     bool              `json:"dune"`
	Out      string            `json:"out"`
}

// LinkArgs is the args of kind ml-link: one native executable bin/<Name>, then the build-only
// store references (Scrub) are removed from it.
type LinkArgs struct {
	Name     string            `json:"name"` // program name
	Main     string            `json:"main"` // main module (linked last)
	Src      string            `json:"src"`
	Dir      string            `json:"dir"`
	Modules  []string          `json:"modules"`
	Packages []string          `json:"packages"`
	Flags    []string          `json:"flags"`
	CLibs    []string          `json:"clibs"` // C link arguments of the Native libraries
	Path     []string          `json:"path"`
	Env      map[string]string `json:"env"`
	Dune     bool              `json:"dune"`
	Out      string            `json:"out"`
	// Scrub are regular expressions over store path names ("ocaml-5.4.1"): every reference to
	// a matching store path in the program is replaced by a dead path (SPEC §03.5.3).
	Scrub []string `json:"scrub"`
}
