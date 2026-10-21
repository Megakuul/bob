package diag

// Variant is one form of a code's message. Most codes have one unnamed variant. A code with
// several variants (1024, 1025, 4009) selects one with the template argument "variant".
type Variant struct {
	Name    string   // "" for the only variant of a code
	Message string   // message template (first line); every {arg} in it is required
	Hints   []string // hint templates; the first whose {args} are all given is used
}

// Entry is one row of the code table (SPEC §02.3).
type Entry struct {
	Code     Code
	Variants []Variant
}

func one(msg string, hints ...string) []Variant { return []Variant{{Message: msg, Hints: hints}} }

const bugHint = "this is a bob bug; please report it with bob log {label}"

// table is the code table of SPEC §02.3, sorted by code. It is append-only (R-NIX-028):
// codes.lock records each code's class and argument names, and a test fails when either changes.
// Spec cross-references in hints ("§05.2.3", "D63") and SPEC notes in place of a hint are left out.
var table = []Entry{
	// 0xxx usage
	{1, one(`unknown {kind} "{name}"`, "did you mean {suggest}?", "run bob help")},
	{2, one(`not inside a bob repository (no bob.lock in {dir} or any parent)`, "run bob from your project, or use -C <dir>")},
	{3, one(`pattern {pattern} matches no package`, "run bob graph to list packages")},
	{4, one(`{flag} needs {what}`)},
	{5, one(`{label} is not runnable`, "bob run needs a binary package")},
	{6, one(`{label} does not support {platform} (it allows: {platforms})`, "pick a supported platform, or build without --platform")},
	{7, one(`variants "{a}" and "{b}" conflict: {reason}`, "drop one of them")},
	{8, one(`unsupported host architecture "{arch}" (bob runs on linux-amd64 and linux-arm64)`)},
	{9, one(`{ref} has nothing to install (it contains: {kinds}); download its artifacts from {url}`)},

	// 1xxx definition
	{1001, one(`nixpkgs has no package "{attr}"`, "did you mean {suggest}?")},
	{1002, one(`nixpkgs package "{attr}" ({version}) is marked broken`, "pin a nixpkgs revision where it builds, or use another package")},
	{1003, one(`nixpkgs package "{attr}" ({version}) is marked insecure: {cves}`, "use a fixed version; bob refuses known-vulnerable packages unless allowed per package")},
	{1004, one(`nixpkgs package "{attr}" has an unfree licence`, "allow it explicitly for this package if your licence permits")},
	{1005, one(`nixpkgs package "{attr}" is not available for {system}`)},
	{1006, one(`dependency cycle: {cycle}`, "a package cannot depend on itself; move the shared code into its own package")},
	{1007, one(`{label} depends on unknown package {dep}`)},
	{1008, one(`{label} needs Go {want} but nixpkgs {rev} provides Go {have}`, "lower GoVersion or pin a newer nixpkgs")},
	{1009, one(`{label} has no pinned hash in bob.lock`, "run bob lock")},
	{1010, one(`{label} targets {system}, which this machine cannot build`, "build it remotely (bob build --remote) or for this machine's platform")},
	{1011, one(`generated file {file} was edited by hand`, "move the change into bob.go; bob check --fix regenerates the file")},
	{1012, one(`bob.lock is out of date: {what}`, "run bob lock")},
	{1013, one(`{path} is not ignored by git`, "bob check --fix adds it to bob's block in .git/info/exclude")},
	{1014, one(`no dev shell "{name}" (have: {names})`)},
	{1015, one(`cannot resolve {ecosystem} dependencies for {root}: {reason}`, "relax the conflicting requirement named above")},
	{1016, one(`{path} conflicts with generated output of {label}: {reason}`, "move or delete the file, or run bob gen --force (keeps a backup)")},
	{1017, one(`nixpkgs package "{attr}" cannot be evaluated on {system}`, "pin another nixpkgs revision or use another package")},
	{1018, one(`evaluating bob.go is not deterministic: {label}`, "bob.go must not depend on time, randomness, environment or map order")},
	{1019, one(`bob.lock cannot be read: {reason} (line {line})`, "resolve the merge conflict, or run bob lock")},
	{1020, one(`{label}: invalid {option}: {reason}`)},
	{1021, one(`{label}: toolchain mismatch: {reason}`, "align the versions named above")},
	{1022, one(`{label} is not a static binary (it needs {deps})`, "use tarball.Closure")},
	{1023, one(`{package} cannot be pinned: {reason}`)},
	{1024, []Variant{ // R-FW-002 rules R1–R7 (R5 is not an error)
		{Name: "R1", Message: `{dir}/bob.go: the first line must be "//go:build bob" so that go build, go vet and go test ignore it`},
		{Name: "R2", Message: `{dir}/bob.go: package {have}, but the Go code in this directory is package {want}; bob.go must use package {want}`},
		{Name: "R3", Message: `{dir}/bob.go: imports the build definitions of {maindir}/, which is a main package and cannot be imported; define the shared package in a bob.go of a non-main directory`},
		{Name: "R4", Message: `{dir}/bob.go:{line}: {name} is also declared at {file}:{fileline}; bob.go shares its package with that code, rename one of them`},
		{Name: "R6", Message: `{dir}/bob.go imports {dir2}, whose Go code imports {dir} (cycle exists only for build definitions); move the definition`},
		{Name: "R7", Message: `{dir}/bob.go: imports {path}, which build definitions cannot use; bob.go may import the bob framework, the standard library and other bob.go packages`},
	}},
	{1025, []Variant{ // R-FW-021
		{Name: "outside", Message: `{pos}: source pattern "{pattern}" leaves the repository`},
		{Name: "noglob", Message: `{pos}: bob.Glob("{pattern}") matches no files`},
		{Name: "symlink", Message: `{file}: symlink points outside the repository`},
	}},

	// 2xxx build
	{2000, one(`{label} failed to build`)},
	{2001, one(`{label}: compile error in {file}`)},
	{2002, one(`{label}: undefined symbol {symbol}`, "the symbol is not provided by any linked library; a dependency is probably missing from Deps")},
	{2003, one(`{label}: header {header} not found`, "add the package that provides {header} to Deps")},
	{2004, one(`{label}: library {lib} not found`, "add the package that provides lib{lib} to Deps, or remove the link flag")},
	{2005, one(`{label}: tests failed`, "run bob test {label} -v to see the full test output")},
	{2006, one(`{label}: dependency {module} needs Go {want} (toolchain is Go {have})`, "use an older version of {module} or pin a newer nixpkgs")},
	{2007, one(`{label} timed out after {seconds}s`, "raise the package's timeout if the step is legitimately slow")},
	{2008, one(`{label} produced no output for {seconds}s and was stopped`, "a step is probably waiting for input or hanging")},
	{2009, one(`{label} tried to use the network during the build`, "builds are offline; declare the download as a fetched dependency so bob.lock pins it")},
	{2010, one(`{label}: Rust crate {crate} not found`, "add the crate to the package's dependencies")},
	{2011, one(`{label} wrote more than {bytes} bytes of log output and was stopped`, "a step is printing in a loop; reduce its output or raise the log limit")},
	{2012, one(`dev server {name} exited with status {status}`, "see {log}")},
	{2013, one(`{label}: {package} needs its {script} script ({reason})`, `add npm.AllowScripts("{package}") to {label}`)},
	{2014, one(`{label}: {package} uses {dep} without declaring it`, `add npm.Extend("{package}", npm.Peer("{dep}"))`)},
	{2015, one(`{label}: prune pattern "{pattern}" removes {file}, which {user} needs`, "narrow the pattern")},

	// 3xxx fetch
	{3001, one(`{label}: download does not match bob.lock`, "if the upstream file legitimately changed, run bob lock; otherwise the source was tampered with")},
	{3002, one(`{label}: download failed: {reason}`, "check the URL and your network; bob retried {attempts} times")},
	{3003, one(`{label}: download not found (HTTP {status})`, "the file was removed or the URL in bob.go is wrong")},
	{3004, one(`{label}: archive {url} contains an unsafe entry {path} ({reason})`, "the archive is malformed or hostile; bob refused it")},

	// 4xxx cache and server
	{4001, one(`cache {cache} rejected bob's credentials`, "run bob login")},
	{4002, one(`cache {cache} lists {what} but cannot deliver it (HTTP {status})`, "the cache is inconsistent; report it to the cache admin")},
	{4003, one(`cache {cache} is unreachable: {reason}`, "bob builds locally when a cache is down")},
	{4004, one(`cache {cache} served {what} without a trusted signature`, "the cache's signing key is not trusted; check bob registry list")},
	{4005, one(`cache {cache} served corrupted data for {what}`, "report this to the cache admin; bob refused the download")},
	{4006, one(`{label} is not in any cache and local builds are disabled`, "build it remotely (bob build --remote) or allow local builds")},
	{4007, one(`uploading {label} to the cache failed`, "the build itself succeeded; the worker retries the upload")},
	{4008, one(`your login to {server} has expired or is invalid`, "run bob login {server}")},
	{4009, []Variant{
		{Name: "none", Message: `no bob server configured`, Hints: []string{"run bob login <url>"}},
		{Name: "unreachable", Message: `bob server {server} is unreachable: {reason}`, Hints: []string{"check the URL and your network"}},
	}},
	{4010, one(`{server} denied {action} on project {project}`, "ask an admin for the permission {permission}")},
	{4011, one(`release {ref} not found on {registry}`, "bob list --available {project} shows the versions")},
	{4012, one(`remote build {id} failed on the server: {reason}`, "bob log --build {id}")},

	// 5xxx upstream
	{5001, one(`nixpkgs package {attr} failed to build from source (needed by {users})`, "this is not your code: pin another revision or ask your bob server admin")},

	// 6xxx resource
	{6001, one(`no space left on device while building {what}`, "free disk space (bob gc) or move bob's store to a larger disk")},
	{6002, one(`{what} was killed: out of memory`, "reduce parallel jobs (bob build -j) or raise the memory limit")},
	{6003, one(`{what} was killed by signal {signal}`, "run the build again; if it repeats, reduce parallel jobs")},

	// 7xxx store and host
	{7001, one(`bob's store is locked by another process`, "another bob command is running")},
	{7002, one(`bob's store is not accessible: {reason}`, "bob's state directory belongs to another user or is read-only")},
	{7003, one(`bob's store is damaged: {what}`, "run bob doctor --repair")},
	{7004, one(`bob's cache database is busy`, "another bob process is using the same state directory")},
	// 7005: the cause line goes in Error.Cause; {fix} is the exact fix of R-NIX-022.
	{7005, one(`bob cannot create its private /nix: unprivileged user namespaces are blocked on this system`, "fix (run once as an administrator): {fix}")},
	{7006, one(`shim {shim} belongs to {root}, which has no bob environment`, "run bob env in {root}, or delete {shim}")},
	{7007, one(`storage backend {endpoint} is not supported: {reason}`, "bob-server needs an S3 backend with conditional writes")},
	{7008, one(`{bucket} bucket policy is too permissive: {reason}`, "apply the shipped policy")},
	{7009, one(`the worker must run as root; builds themselves never run as root`, "start bob-server worker as root")},
	{7010, one(`worker cannot isolate builds: {reason}`, "see the worker host requirements")},

	// 8xxx interrupt
	{8001, one(`build cancelled`)},

	// 9xxx internal
	{9001, one(`bob's builder for {label} is broken: {reason}`, bugHint)},
	{9002, one(`{label} did not produce its output`, bugHint)},
	{9003, one(`{label} refers to {ref}, which it must not reference`, "this is a bob bug (missing fixup)")},
	{9004, one(`bob generated an invalid build step for {label}: {reason}`, "this is a bob bug; please report it with bob build -v")},
	{9005, one(`Nix stopped unexpectedly ({reason})`, "run with -v and report it")},
	{9006, one(`quirks entry {entry} has no justification or passing test`)},
	{9007, one(`embedded builder does not match its recorded hash`, "this is a bob bug (stale builders.go table)")},
	{9008, one(`{feature} is not implemented yet`)}, // agent decision A26
	{9998, one(`unrecognised Nix log format (Nix {nixVersion})`, "bob only supports the Nix it embeds")},
	{9999, one(`unrecognised Nix error`, "this is a bob bug; the raw message is shown with -v")},
}
