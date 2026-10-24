// Package mlspec holds the IR payloads ("spec") of the OCaml node kinds. pkg/ocaml writes them
// during evaluation, pkg/ocaml/gen reads them when planning. Dependency references are indices
// into the node's deps.
package mlspec

// Node kinds of the OCaml ecosystem; "opam" is also the bob.lock section key.
const (
	KindLibrary    = "opam.library"    // a local OCaml library (ocaml.Library)
	KindExecutable = "opam.executable" // a native executable (ocaml.Executable)
	KindTest       = "opam.test"       // a test executable and its run (ocaml.Test)
)

// Target types.
const (
	TypeLibrary    = "library"
	TypeExecutable = "executable"
	TypeTest       = "test"
)

// Tool attributes of the OCaml toolchain (nixpkgs, host set). Every OCaml node depends on all
// of them; Target.Tools maps each attribute to its dep index.
const (
	AttrOCaml     = "ocamlPackages.ocaml"
	AttrFindlib   = "ocamlPackages.findlib"
	AttrDune      = "ocamlPackages.dune_3"
	AttrInstaller = "opam-installer"
	AttrCC        = "gcc"
	AttrBinutils  = "binutils"
	AttrPatchelf  = "patchelf"
)

// ToolAttrs are the toolchain attributes in PATH order: the OCaml toolchain, the C toolchain
// and the programs opam build commands expect (S5 §5.2).
var ToolAttrs = []string{
	AttrOCaml, AttrFindlib, AttrDune, AttrInstaller, AttrCC, AttrBinutils, AttrPatchelf,
	"coreutils", "bash", "gnutar", "gzip", "bzip2", "xz", "gnumake", "gnused", "gnugrep", "gawk",
	"findutils", "diffutils", "which", "gnupatch", "pkg-config", "m4",
}

// ConfEntry maps an opam conf-* package to nixpkgs libraries (SPEC §03.5.5). Test names the
// corpus graph (R-ML-002) that builds with the entry; Why says what the package checks.
type ConfEntry struct {
	Opam  string
	Attrs []string
	Why   string
	Test  string
}

// ConfTable is bob's shipped conf-* table: exactly the entries the 20-graph corpus needs.
var ConfTable = []ConfEntry{
	{Opam: "conf-gmp", Attrs: []string{"gmp"}, Why: "GNU MP headers and library (zarith)", Test: "cohttp"},
	{Opam: "conf-gmp-powm-sec", Attrs: []string{"gmp"}, Why: "GNU MP with mpz_powm_sec (mirage-crypto-pk)", Test: "dream"},
	{Opam: "conf-libev", Attrs: []string{"libev"}, Why: "libev for lwt's Lwt_engine.libev", Test: "dream"},
	{Opam: "conf-libssl", Attrs: []string{"openssl"}, Why: "OpenSSL headers and library (ssl)", Test: "dream"},
	{Opam: "conf-pkg-config", Attrs: []string{"pkg-config"}, Why: "the pkg-config program", Test: "cohttp"},
}

// Opam is one declared opam dependency: the package, its version formula and the findlib
// library the code uses ("" = the library named like the package).
type Opam struct {
	Name string `json:"name"`
	Req  string `json:"req"`
	Lib  string `json:"lib"`
}

// Conf is one conf-* mapping a node carries: the shipped table's and ocaml.Conf's.
type Conf struct {
	Opam string `json:"opam"`
	Libs []int  `json:"libs"` // dep indices of the nixpkgs (or other Linkable) packages
}

// Target is the spec of opam.library, opam.executable and opam.test nodes. The node's sources
// are its Srcs and Stubs in one source path: .ml/.mli files are modules, .c files C stubs.
// Deps of the node: the tools (Tools), the local libraries (Local), the Native libraries and
// the conf libraries.
type Target struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`    // Type*
	Main    string         `json:"main"`    // executables and tests: the main module ("main")
	Opam    []Opam         `json:"opam"`    // declared opam deps
	Local   []int          `json:"local"`   // dep indices of local libraries
	Native  []int          `json:"native"`  // dep indices of bob.Linkable libraries
	Wrapped bool           `json:"wrapped"` // libraries: dune-style module wrapping
	Flags   []string       `json:"flags"`   // extra ocamlopt flags
	Dune    bool           `json:"dune"`    // build with a generated dune file
	Conf    []Conf         `json:"conf"`
	Tools   map[string]int `json:"tools"` // ToolAttrs → dep index
}
