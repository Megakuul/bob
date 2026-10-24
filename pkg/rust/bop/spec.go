package bop

// Job args of the Rust kinds (bspec.Job.Args). pkg/rust/gen writes the same JSON with its own
// types (generators may not import the builder layer, R-ARCH-003); the args are decoded
// strictly, so a mismatch fails the job as invalid instead of building something else.

// Toolchain is the programs of a job.
type Toolchain struct {
	Rustc string   `json:"rustc"` // the rustc program (<rustc>/bin/rustc)
	Path  []string `json:"path"`  // PATH: rustc, the C compiler (linker), binutils, native tools
}

// RustcArgs is the args of kind rustc: compile one crate (lib, proc-macro, bin, staticlib,
// cdylib, a build script or a test harness) into $out/lib or $out/bin.
type RustcArgs struct {
	Toolchain
	Src        string            `json:"src"`       // crate directory (store path)
	Remap      string            `json:"remap"`     // what Src becomes in diagnostics and debug info
	CrateName  string            `json:"crateName"` // code name ('-' → '_')
	CrateType  string            `json:"crateType"` // lib | proc-macro | bin | staticlib | cdylib
	Test       bool              `json:"test"`      // --test (a test harness binary)
	Edition    string            `json:"edition"`
	Root       string            `json:"root"`              // crate root, relative to Src
	Target     string            `json:"target"`            // target triple ("" = the host's, no --target)
	Sysroot    string            `json:"sysroot,omitempty"` // cross: the rust-std-<triple> sysroot
	Linker     string            `json:"linker,omitempty"`  // cross: -C linker= (the target's C compiler)
	Features   []string          `json:"features"`
	Metadata   string            `json:"metadata"` // -C metadata (and the file suffix of libs)
	OptLevel   string            `json:"optLevel"`
	Debug      bool              `json:"debug,omitempty"` // debug profile: -C debuginfo=2 -C debug-assertions=on, no strip
	CapLints   bool              `json:"capLints"`
	CheckCfg   bool              `json:"checkCfg"`   // --check-cfg for the features (local crates)
	FeatureSet []string          `json:"featureSet"` // every feature of the crate (check-cfg values)
	Externs    []Extern          `json:"externs"`
	DepDirs    []string          `json:"depDirs"`  // -L dependency= (directories of the transitive libraries)
	BuildRun   string            `json:"buildRun"` // output of the crate's build-script run; "" = none
	LinkFrom   []string          `json:"linkFrom"` // build-script runs of transitive dependencies (link search paths)
	LinkArgs   []string          `json:"linkArgs"` // -C link-arg= (native libraries, rpaths)
	Libs       []string          `json:"libs"`     // -l values (native libraries)
	Search     []string          `json:"search"`   // -L values (native libraries)
	Env        map[string]string `json:"env"`      // CARGO_PKG_* and the like
	Headers    string            `json:"headers"`  // staticlib/cdylib: include dir (relative to Src) copied to $out/include
	OutName    string            `json:"outName"`  // the file in $out/bin or $out/lib ("" = rustc's own name)
}

// Extern is one --extern: the dependency's code name and its library file.
type Extern struct {
	Name string `json:"name"`
	File string `json:"file"`
}

// BuildRunArgs is the args of kind rust-build-run: run a compiled build script with Cargo's
// environment and translate its directives into $out/directives.json; $out/out-dir is OUT_DIR,
// $out/output its stdout.
type BuildRunArgs struct {
	Toolchain
	Src      string            `json:"src"`    // crate directory (the script's working directory)
	Script   string            `json:"script"` // the compiled build script
	Env      map[string]string `json:"env"`
	DepLinks []DepLinks        `json:"depLinks"` // direct dependencies with `links`
}

// DepLinks names the build-script run of a direct dependency with `links`: its metadata
// becomes DEP_<LINKS>_<KEY>.
type DepLinks struct {
	Links string `json:"links"`
	Run   string `json:"run"`
}

// Directives is $out/directives.json of a build-script run (Cargo's build-script outputs).
type Directives struct {
	Cfgs        []string          `json:"cfgs"`
	CheckCfgs   []string          `json:"checkCfgs"`
	Env         map[string]string `json:"env"`
	LinkLibs    []string          `json:"linkLibs"`   // -l values
	LinkSearch  []string          `json:"linkSearch"` // -L values
	LinkArgs    []string          `json:"linkArgs"`   // -C link-arg for bins and cdylibs of the package
	Metadata    map[string]string `json:"metadata"`   // DEP_<LINKS>_<KEY> of dependents
	Warnings    []string          `json:"warnings"`
	Unsupported []string          `json:"unsupported"`
}
