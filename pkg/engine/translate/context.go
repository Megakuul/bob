package translate

// Context is everything the translator needs besides the Nix stream (ADR 0017 Decision 2).
// bob has all of it in memory when it starts Nix; captures store it in meta.json so that a
// replay is self-contained. All paths in it are bob's own facts, never parsed from Nix.
type Context struct {
	Packages     map[string]Pkg      `json:"packages"`  // drv path → bob package
	Outputs      map[string]string   `json:"outputs"`   // output path → drv path (bob drvs)
	Sources      map[string]string   `json:"sources"`   // imported source store path → repo-relative dir
	Nixpkgs      map[string]string   `json:"nixpkgs"`   // drv path → nixpkgs attribute (direct inputs of bob drvs)
	Users        map[string][]string `json:"users"`     // nixpkgs drv path → labels of the bob packages that use it
	Requested    []string            `json:"requested"` // labels requested on the command line
	Limits       Limits              `json:"limits"`
	Substituters []Substituter       `json:"substituters,omitempty"`
	// Chains maps a failing non-bob drv to the path from a direct nixpkgs input down to it
	// (S15 §5.7: the build pipeline computes it lazily for failing drvs).
	Chains map[string][]string `json:"chains,omitempty"`
	// ChainOf computes a missing Chains entry when a nixpkgs drv fails (the pipeline walks the
	// input derivations only then); the result is recorded in Chains. Optional.
	ChainOf func(drv string) []string `json:"-"`
	// PreBuilt are bob drvs built while planning (codegen early cutoff, §03.6.3): Nix finds
	// them valid, but this run built them, so their packages are reported "built".
	PreBuilt []string `json:"preBuilt,omitempty"`
	// Exports are the packages that export an include directory (engine.RegisterIncludeDir),
	// by label; Deps are the direct dependencies (labels) of every package that has an
	// include-exporting dependency or exports one itself. A missing header that lies in an
	// exported directory of another package gets a hint (includeHint).
	Exports map[string]Export `json:"exports,omitempty"`
	// Root is the repository (empty in a replay): a missing header that exists below it, in a
	// directory of another package, is explained even when that package is not in the graph.
	Root string              `json:"-"`
	Deps map[string][]string `json:"deps,omitempty"`
}

// Export is a package's exported include directory.
type Export struct {
	Dir     string `json:"dir"`     // repo-relative package directory ("" = repository root)
	Include string `json:"include"` // repo-relative exported include directory
}

// Pkg is what bob knows about one derivation it generated.
type Pkg struct {
	Label     string `json:"label"`
	DefinedAt string `json:"definedAt"` // "dir/bob.go:line:col"
	Kind      string `json:"kind"`
	Role      string `json:"role,omitempty"`   // "" = the package itself; helper drvs: "unpack" (a download's unpack job), "helper" (any other step)
	SrcDir    string `json:"srcDir,omitempty"` // repo-relative source dir (maps relative compiler paths)
	URL       string `json:"url,omitempty"`    // fetch packages
	LockHash  string `json:"lockHash,omitempty"`
	// Codegen marks a code generator's derivation (engine.RegisterCodegenKind): its failure
	// reads `✗ <label> code generation failed (defined at …)` with the generator's messages.
	Codegen bool `json:"codegen,omitempty"`
}

// Substituter is a cache bob configured.
type Substituter struct {
	URL  string `json:"url"`
	Name string `json:"name"` // e.g. "acme cache", "cache.nixos.org"
	Auth bool   `json:"auth"` // bob supplies credentials for it
}

// Limits are the limits bob gave Nix for this run. Nix prints the same text for the build
// timeout and the silence timeout, so bob tells them apart by the value it set.
type Limits struct {
	TimeoutS    int   `json:"timeoutS,omitempty"`
	MaxSilentS  int   `json:"maxSilentS,omitempty"`
	MaxLogBytes int64 `json:"maxLogBytes,omitempty"`
	LocalBuilds bool  `json:"localBuilds"` // false = cache only (max-jobs 0)
}

// Outcome is how the Nix process ended, plus bob's structural check of the outputs
// (R-NIX-018: success is exit 0 and every requested output valid, never a message).
type Outcome struct {
	ExitCode   int      `json:"exitCode"`
	Signal     string   `json:"signal,omitempty"`    // signal that killed Nix ("killed", "terminated", …)
	Interrupt  bool     `json:"interrupt,omitempty"` // bob forwarded SIGINT (the user pressed Ctrl-C)
	DurationMs int64    `json:"durationMs"`
	NixVersion string   `json:"nixVersion"`
	Valid      []string `json:"valid,omitempty"` // bob drvs whose outputs are valid after the run
}
