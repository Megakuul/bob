package translate

// Code is a stable bob error code (BOB-E-NNNN). Codes are never reused or renumbered.
type Code string

// Class groups codes; it decides the exit code.
type Class string

const (
	ClassDefinition Class = "definition" // bob.go / bob.lock is wrong; caught before building
	ClassBuild      Class = "build"      // the user's code failed to build or test
	ClassFetch      Class = "fetch"      // a pinned download failed or does not match bob.lock
	ClassCache      Class = "cache"      // a binary cache (substituter) failed
	ClassResource   Class = "resource"   // the machine ran out of something, or a limit was hit
	ClassStore      Class = "store"      // bob's local store is damaged, busy or inaccessible
	ClassUpstream   Class = "upstream"   // a nixpkgs package failed to build from source
	ClassInterrupt  Class = "interrupt"  // the user cancelled
	ClassInternal   Class = "internal"   // a bob bug: Nix rejected what bob generated, or output is unknown
)

// Exit codes [proposal]. When several errors occur, the highest-priority class wins.
var ExitCodes = map[Class]int{
	ClassBuild:      1,
	ClassDefinition: 3,
	ClassFetch:      4,
	ClassCache:      4,
	ClassUpstream:   5,
	ClassResource:   6,
	ClassStore:      7,
	ClassInternal:   70,
	ClassInterrupt:  130,
}

// Priority for the exit code when several classes occur (first wins).
var classPriority = []Class{ClassInterrupt, ClassInternal, ClassStore, ClassResource, ClassCache, ClassFetch,
	ClassUpstream, ClassDefinition, ClassBuild}

// Spec describes one code. Title is the message template's first line.
type Spec struct {
	Code  Code
	Class Class
	Title string // template; {label}, {attr}, {path}, … are filled from Error.Args
	Hint  string // default hint template ("" = none)
}

// Catalogue is the error taxonomy [proposal]. Keep sorted by code.
var Catalogue = []Spec{
	// 1xxx definition (Go pre-checks, before Nix runs; R-NIX-011)
	{"BOB-E-1001", ClassDefinition, `nixpkgs has no package "{attr}"`, "did you mean {suggest}?"},
	{"BOB-E-1002", ClassDefinition, `nixpkgs package "{attr}" ({version}) is marked broken`, "pin a nixpkgs revision where it builds, or use another package"},
	{"BOB-E-1003", ClassDefinition, `nixpkgs package "{attr}" ({version}) is marked insecure: {cves}`, "use a fixed version; bob refuses known-vulnerable packages unless allowed per package"},
	{"BOB-E-1004", ClassDefinition, `nixpkgs package "{attr}" has an unfree licence`, "allow it explicitly for this package if your licence permits"},
	{"BOB-E-1005", ClassDefinition, `nixpkgs package "{attr}" is not available for {system}`, ""},
	{"BOB-E-1006", ClassDefinition, "dependency cycle: {cycle}", "a package cannot depend on itself; move the shared code into its own package"},
	{"BOB-E-1007", ClassDefinition, "{label} depends on unknown package {dep}", ""},
	{"BOB-E-1008", ClassDefinition, "{label} needs Go {want} but nixpkgs {rev} provides Go {have}", "lower GoVersion or pin a newer nixpkgs"},
	{"BOB-E-1009", ClassDefinition, "{label} has no pinned hash in bob.lock", "run bob lock"},
	{"BOB-E-1010", ClassDefinition, "{label} targets {system}, which this machine cannot build", "build it remotely (bob build --remote) or for this machine's platform"},

	// 2xxx build (the user's code; R-NIX-012)
	{"BOB-E-2000", ClassBuild, "{label} failed to build", ""},
	{"BOB-E-2001", ClassBuild, "{label}: compile error in {file}", ""},
	{"BOB-E-2002", ClassBuild, "{label}: undefined symbol {symbol}", "the symbol is not provided by any linked library; a dependency is probably missing from Deps"},
	{"BOB-E-2003", ClassBuild, "{label}: header {header} not found", "add the package that provides {header} to Deps"},
	{"BOB-E-2004", ClassBuild, "{label}: library {lib} not found", "add the package that provides lib{lib} to Deps, or remove the link flag"},
	{"BOB-E-2005", ClassBuild, "{label}: tests failed", "run bob test {label} -v to see the full test output"},
	{"BOB-E-2006", ClassBuild, "{label}: dependency {module} needs Go {want} (toolchain is Go {have})", "use an older version of {module} or pin a newer nixpkgs"},
	{"BOB-E-2007", ClassBuild, "{label} timed out after {seconds}s", "raise the package's timeout if the step is legitimately slow"},
	{"BOB-E-2008", ClassBuild, "{label} produced no output for {seconds}s and was stopped", "a step is probably waiting for input or hanging"},
	{"BOB-E-2009", ClassBuild, "{label} tried to use the network during the build", "builds are offline; declare the download as a fetched dependency so bob.lock pins it"},
	{"BOB-E-2010", ClassBuild, "{label}: Rust crate {crate} not found", "add the crate to the package's dependencies"},
	{"BOB-E-2011", ClassBuild, "{label} wrote more than {bytes} bytes of log output and was stopped", "a step is printing in a loop; reduce its output or raise the log limit"},

	// 3xxx fetch
	{"BOB-E-3001", ClassFetch, "{label}: download does not match bob.lock", "if the upstream file legitimately changed, run bob lock; otherwise the source was tampered with"},
	{"BOB-E-3002", ClassFetch, "{label}: download failed: {reason}", "check the URL and your network; bob retried {attempts} times"},
	{"BOB-E-3003", ClassFetch, "{label}: download not found (HTTP {status})", "the file was removed or the URL in bob.go is wrong"},

	// 4xxx cache (substituters)
	{"BOB-E-4001", ClassCache, "cache {cache} rejected bob's credentials", "run bob login"},
	{"BOB-E-4002", ClassCache, "cache {cache} lists {what} but cannot deliver it (HTTP {status})", "the cache is inconsistent; bob built locally instead or report it to the cache admin"},
	{"BOB-E-4003", ClassCache, "cache {cache} is unreachable: {reason}", "bob builds locally when a cache is down; check the network or the cache URL"},
	{"BOB-E-4004", ClassCache, "cache {cache} served {what} without a trusted signature", "the cache's signing key is not trusted; run bob login again or check the server key"},
	{"BOB-E-4005", ClassCache, "cache {cache} served corrupted data for {what}", "report this to the cache admin; bob refused the download"},
	{"BOB-E-4006", ClassCache, "{label} is not in any cache and local builds are disabled", "build it remotely (bob build --remote) or allow local builds"},
	{"BOB-E-4007", ClassCache, "uploading {label} to the cache failed", "the build itself succeeded; the worker retries the upload"},

	// 5xxx upstream (nixpkgs packages built from source)
	{"BOB-E-5001", ClassUpstream, "nixpkgs package {attr} failed to build from source (needed by {users})", "this is not your code: the package is not in any cache for this nixpkgs revision; pin another revision or ask your bob server admin"},

	// 6xxx resources / limits of this machine
	{"BOB-E-6001", ClassResource, "no space left on device while building {what}", "free disk space (bob gc) or move bob's store to a larger disk"},
	{"BOB-E-6002", ClassResource, "{what} was killed: out of memory", "reduce parallel jobs (bob build -j) or raise the memory limit"},
	{"BOB-E-6003", ClassResource, "{what} was killed by signal {signal}", ""},

	// 7xxx store (bob's local Nix store)
	{"BOB-E-7001", ClassStore, "bob's store is locked by another process", "another bob command is running; wait for it or stop it"},
	{"BOB-E-7002", ClassStore, "bob's store is not accessible: {reason}", "bob's state directory belongs to another user or is read-only"},
	{"BOB-E-7003", ClassStore, "bob's store is damaged: {what}", "run bob doctor --repair"},
	{"BOB-E-7004", ClassStore, "bob's cache database is busy", "another bob process is using the same state directory"},

	// 8xxx interrupt
	{"BOB-E-8001", ClassInterrupt, "build cancelled", ""},

	// 9xxx internal (bob bugs; every occurrence needs a regression test, R-NIX-010)
	{"BOB-E-9001", ClassInternal, "bob's builder for {label} is broken: {reason}", "this is a bob bug; please report it with bob log {label}"},
	{"BOB-E-9002", ClassInternal, "{label} did not produce its output", "this is a bob bug; please report it with bob log {label}"},
	{"BOB-E-9003", ClassInternal, "{label} refers to {ref}, which it must not reference", "this is a bob bug (missing fixup); please report it"},
	{"BOB-E-9004", ClassInternal, "bob generated an invalid build step for {label}: {reason}", "this is a bob bug; please report it with bob build -v"},
	{"BOB-E-9005", ClassInternal, "Nix stopped unexpectedly ({reason})", "run with -v to see Nix's raw output and report it"},
	{"BOB-E-9998", ClassInternal, "unrecognised Nix log format (Nix {nixVersion})", "bob only supports the Nix it embeds; this is a bob bug"},
	{"BOB-E-9999", ClassInternal, "unrecognised Nix error", "this is a bob bug; the raw message is shown with -v"},
}

var specByCode = func() map[Code]Spec {
	m := map[Code]Spec{}
	for _, s := range Catalogue {
		m[s.Code] = s
	}
	return m
}()

// SpecOf returns the spec of a code.
func SpecOf(c Code) Spec { return specByCode[c] }
