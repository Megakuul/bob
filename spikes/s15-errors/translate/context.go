package translate

// Pkg is what bob knows about one derivation it generated (from the IR).
type Pkg struct {
	Label     string `json:"label"`
	DefinedAt string `json:"definedAt"`
	Kind      string `json:"kind"`
	Role      string `json:"role,omitempty"`   // "" = the package itself, "unpack" = helper drv
	SrcDir    string `json:"srcDir,omitempty"` // repo-relative source dir (maps relative compiler paths)
	URL       string `json:"url,omitempty"`    // fetch packages
	LockHash  string `json:"lockHash,omitempty"`
}

// Context is everything the translator needs besides the Nix stream. bob has all of it in
// memory when it starts Nix; captures store it in meta.json so replays are self-contained.
type Context struct {
	Packages     map[string]Pkg      `json:"packages"`  // drv path -> bob package
	Outputs      map[string]string   `json:"outputs"`   // output path -> drv path (bob drvs)
	Sources      map[string]string   `json:"sources"`   // imported source store path -> repo-relative dir
	Nixpkgs      map[string]string   `json:"nixpkgs"`   // drv path -> nixpkgs attribute (direct inputs of bob drvs)
	Users        map[string][]string `json:"users"`     // nixpkgs drv path -> labels of bob packages that use it
	Requested    []string            `json:"requested"` // labels requested on the command line
	Limits       Limits              `json:"limits"`
	Substituters []Substituter       `json:"substituters,omitempty"`
	Chains       map[string][]string `json:"chains,omitempty"` // failing non-bob drv -> path from a direct nixpkgs input
}

// Substituter is a cache bob configured (bob knows which are its own and their auth state).
type Substituter struct {
	URL  string `json:"url"`
	Name string `json:"name"` // e.g. "acme cache", "cache.nixos.org"
	Auth bool   `json:"auth"` // bob supplies credentials for it
}

// Outcome is how the Nix process ended.
type Outcome struct {
	ExitCode   int      `json:"exitCode"`
	Signal     string   `json:"signal,omitempty"`    // signal that killed Nix, if any
	Interrupt  bool     `json:"interrupt,omitempty"` // bob forwarded SIGINT (user pressed Ctrl-C)
	DurationMs int64    `json:"durationMs"`
	NixVersion string   `json:"nixVersion"`
	Valid      []string `json:"valid,omitempty"` // bob drvs whose outputs exist after the run (bob checks the store)
}

// Limits bob configured for this run (so it can tell a timeout from a silence timeout).
type Limits struct {
	TimeoutS    int   `json:"timeoutS,omitempty"`
	MaxSilentS  int   `json:"maxSilentS,omitempty"`
	MaxLogBytes int64 `json:"maxLogBytes,omitempty"`
	LocalBuilds bool  `json:"localBuilds"` // false = cache-only (max-jobs 0)
}
