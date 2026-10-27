---
title: Error codes
section: CLI
order: 2
summary: Every BOB-E error code with its class, exit status, message and hint, generated from the code table.
---
Every error bob prints has a stable code, `BOB-E-NNNN`. The first digit is the class, and
the class decides the exit status (see [Exit codes](01-commands.md#exit-codes)). Codes are never
reused or renumbered. In the messages, `{name}` is filled in with the details of the error.

> [!NOTE]
> This page is generated from bob's code table (`pkg/bob/diag`), so it always matches the
> binary built from the same source. Several hints are separated by " / "; bob prints the first
> one whose details it knows.

## Code table

| Code | Class | Exit | Message | Hint |
|---|---|---|---|---|
| BOB-E-0001 | usage | 2 | `unknown {kind} "{name}"` | `did you mean {suggest}? / run bob help` |
| BOB-E-0002 | usage | 2 | `not inside a bob repository (no bob.lock in {dir} or any parent)` | `run bob from your project, or use -C <dir>` |
| BOB-E-0003 | usage | 2 | `pattern {pattern} matches no package` | `run bob graph to list packages` |
| BOB-E-0004 | usage | 2 | `{flag} needs {what}` | — |
| BOB-E-0005 | usage | 2 | `{label} is not runnable` | `bob run needs a binary package` |
| BOB-E-0006 | usage | 2 | `{label} does not support {platform} (it allows: {platforms})` | `pick a supported platform, or build without --platform` |
| BOB-E-0007 | usage | 2 | `variants "{a}" and "{b}" conflict: {reason}` | `drop one of them` |
| BOB-E-0008 | usage | 2 | `unsupported host architecture "{arch}" (bob runs on linux-amd64 and linux-arm64)` | — |
| BOB-E-0009 | usage | 2 | `{ref} has nothing to install (it contains: {kinds}); download its artifacts from {url}` | — |
| BOB-E-0010 | usage | 2 | `unknown platform "{platform}" (known: {known})` | — |
| BOB-E-0011 | usage | 2 | `{label}: {ecosystem} packages cannot be cross-compiled yet (requested {platform} on a {host} host)` | `build for the host platform, or use an ecosystem that supports cross-compilation` |
| BOB-E-1001 | definition | 3 | `nixpkgs has no package "{attr}"` | `did you mean {suggest}?` |
| BOB-E-1001 | definition | 3 | `{pos}: nixpkgs at the pinned revision {rev} has no package "{attr}" (nixpkgs.{name})` | `bob's typed nixpkgs packages come from {surface}; move the pin with bob lock --update nixpkgs, or use another package` |
| BOB-E-1002 | definition | 3 | `nixpkgs package "{attr}" ({version}) is marked broken` | `pin a nixpkgs revision where it builds, or use another package` |
| BOB-E-1003 | definition | 3 | `nixpkgs package "{attr}" ({version}) is marked insecure: {cves}` | `use a fixed version; bob refuses known-vulnerable packages unless allowed per package` |
| BOB-E-1004 | definition | 3 | `nixpkgs package "{attr}" has an unfree licence` | `allow it explicitly for this package if your licence permits` |
| BOB-E-1005 | definition | 3 | `nixpkgs package "{attr}" is not available for {system}` | — |
| BOB-E-1006 | definition | 3 | `dependency cycle: {cycle}` | `a package cannot depend on itself; move the shared code into its own package` |
| BOB-E-1007 | definition | 3 | `{label} depends on unknown package {dep}` | — |
| BOB-E-1008 | definition | 3 | `{label} needs Go {want} but nixpkgs {rev} provides Go {have}` | `lower GoVersion or pin a newer nixpkgs` |
| BOB-E-1008 | definition | 3 | `{requirement} needs go {need}, but {module} uses go {go} (set Go in {file})` | `raise the module's Go, or require an older {requirement}` |
| BOB-E-1009 | definition | 3 | `{label} has no pinned hash in bob.lock` | `run bob lock` |
| BOB-E-1010 | definition | 3 | `{label} targets {system}, which this machine cannot build` | `build it remotely (bob build --remote) or for this machine's platform` |
| BOB-E-1011 | definition | 3 | `generated file {file} was edited by hand` | `move the change into bob.go; bob check --fix regenerates the file` |
| BOB-E-1012 | definition | 3 | `bob.lock is out of date: {what}` | `run bob lock` |
| BOB-E-1013 | definition | 3 | `{path} is not ignored by git` | `bob check --fix adds it to bob's block in .git/info/exclude` |
| BOB-E-1014 | definition | 3 | `no dev shell "{name}" (have: {names})` | — |
| BOB-E-1015 | definition | 3 | `cannot resolve {ecosystem} dependencies for {root}: {reason}` | `relax the conflicting requirement named above` |
| BOB-E-1016 | definition | 3 | `{path} conflicts with generated output of {label}: {reason}` | `move or delete the file, or run bob gen --force (keeps a backup)` |
| BOB-E-1017 | definition | 3 | `nixpkgs package "{attr}" cannot be evaluated on {system}` | `pin another nixpkgs revision or use another package` |
| BOB-E-1018 | definition | 3 | `evaluating bob.go is not deterministic: {label}` | `bob.go must not depend on time, randomness, environment or map order` |
| BOB-E-1019 | definition | 3 | `bob.lock cannot be read: {reason} (line {line})` | `resolve the merge conflict, or run bob lock --rebuild to resolve everything again from bob.go` |
| BOB-E-1020 | definition | 3 | `{label}: invalid {option}: {reason}` | `{hint}` |
| BOB-E-1021 | definition | 3 | `{label}: toolchain mismatch: {reason}` | `align the versions named above` |
| BOB-E-1022 | definition | 3 | `{label} is not a static binary (it needs {deps})` | `use tarball.Closure` |
| BOB-E-1023 | definition | 3 | `{package} cannot be pinned: {reason}` | — |
| BOB-E-1024 | definition | 3 | `{dir}/bob.go: the first line must be "//go:build bob" so that go build, go vet and go test ignore it` | — |
| BOB-E-1024 | definition | 3 | `{dir}/bob.go: package {have}, but the Go code in this directory is package {want}; bob.go must use package {want}` | — |
| BOB-E-1024 | definition | 3 | `{dir}/bob.go: imports the build definitions of {maindir}/, which is a main package and cannot be imported; define the shared package in a bob.go of a non-main directory` | — |
| BOB-E-1024 | definition | 3 | `{dir}/bob.go:{line}: {name} is also declared at {file}:{fileline}; bob.go shares its package with that code, rename one of them` | — |
| BOB-E-1024 | definition | 3 | `{dir}/bob.go imports {dir2}, whose Go code imports {dir} (cycle exists only for build definitions); move the definition` | — |
| BOB-E-1024 | definition | 3 | `{dir}/bob.go: imports {path}, which build definitions cannot use; bob.go may import the bob framework, the standard library and other bob.go packages` | — |
| BOB-E-1024 | definition | 3 | `{dir}/bob.go:{line}: assigns to {name}, a value of the bob framework; framework values are read-only, declare your own variable` | — |
| BOB-E-1025 | definition | 3 | `{pos}: source pattern "{pattern}" leaves the repository` | — |
| BOB-E-1025 | definition | 3 | `{pos}: bob.Glob("{pattern}") matches no files` | — |
| BOB-E-1025 | definition | 3 | `{file}: symlink points outside the repository` | — |
| BOB-E-1026 | definition | 3 | `nixpkgs mismatch: {what}` | `{hint}` |
| BOB-E-1028 | definition | 3 | `{pos}: {label}: "{pattern}" leaves the package directory {dir}/` | `each bob.go builds only the files below its own directory; give the other directory its own bob.go and depend on its package` |
| BOB-E-1028 | definition | 3 | `{pos}: {label}: "{pattern}" is inside {sub}/, which has its own bob.go` | `use the package defined in {sub}/bob.go through its handle` |
| BOB-E-1029 | definition | 3 | `{pos}: {label}: {dir}/bob.go already defines the {ecosystem} {kind} {other} at {pos2}; one library or binary per directory package` | `move {label} into its own directory with its own bob.go and depend on it through its handle` |
| BOB-E-2000 | build | 1 | `{label} failed to build` | — |
| BOB-E-2001 | build | 1 | `{label}: compile error in {file}` | — |
| BOB-E-2002 | build | 1 | `{label}: undefined symbol {symbol}` | `the symbol is not provided by any linked library; a dependency is probably missing from Deps` |
| BOB-E-2003 | build | 1 | `{label}: header {header} not found` | `add the package that provides {header} to Deps` |
| BOB-E-2004 | build | 1 | `{label}: library {lib} not found` | `add the package that provides lib{lib} to Deps, or remove the link flag` |
| BOB-E-2005 | build | 1 | `{label}: tests failed` | `run bob test {label} -v to see the full test output` |
| BOB-E-2006 | build | 1 | `{label}: dependency {module} needs Go {want} (toolchain is Go {have})` | `use an older version of {module} or pin a newer nixpkgs` |
| BOB-E-2007 | build | 1 | `{label} timed out after {seconds}s` | `raise the package's timeout if the step is legitimately slow` |
| BOB-E-2008 | build | 1 | `{label} produced no output for {seconds}s and was stopped` | `a step is probably waiting for input or hanging` |
| BOB-E-2009 | build | 1 | `{label} tried to use the network during the build` | `builds are offline; declare the download as a fetched dependency so bob.lock pins it` |
| BOB-E-2010 | build | 1 | `{label}: Rust crate {crate} not found` | `add the crate to the package's dependencies` |
| BOB-E-2011 | build | 1 | `{label} wrote more than {bytes} bytes of log output and was stopped` | `a step is printing in a loop; reduce its output or raise the log limit` |
| BOB-E-2012 | build | 1 | `dev server {name} exited with status {status}` | `see {log}` |
| BOB-E-2013 | build | 1 | `{label}: {package} needs its {script} script ({reason})` | `declare {package} (npm.Dep or npm.Indirect("{package}")) and add npm.AllowScripts(<its handle>) to {label}` |
| BOB-E-2014 | build | 1 | `{label}: {package} uses {dep} without declaring it` | `declare both (npm.Dep or npm.Indirect) and add npm.Extend(<handle of {package}>, npm.Peer(<handle of {dep}>))` |
| BOB-E-2015 | build | 1 | `{label}: prune pattern "{pattern}" removes {file}, which {user} needs` | `narrow the pattern` |
| BOB-E-2016 | build | 1 | `{label}: {reason}` | — |
| BOB-E-3001 | fetch | 4 | `{label}: download does not match bob.lock` | `if the upstream file legitimately changed, run bob lock; otherwise the source was tampered with` |
| BOB-E-3002 | fetch | 4 | `{label}: download failed: {reason}` | `check the URL and your network; bob retried {attempts} times` |
| BOB-E-3003 | fetch | 4 | `{label}: download not found (HTTP {status})` | `the file was removed or the URL in bob.go is wrong` |
| BOB-E-3004 | fetch | 4 | `{label}: archive {url} contains an unsafe entry {path} ({reason})` | `the archive is malformed or hostile; bob refused it` |
| BOB-E-4001 | cache | 4 | `cache {cache} rejected bob's credentials` | `run bob login` |
| BOB-E-4002 | cache | 4 | `cache {cache} lists {what} but cannot deliver it (HTTP {status})` | `the cache is inconsistent; report it to the cache admin` |
| BOB-E-4003 | cache | 4 | `cache {cache} is unreachable: {reason}` | `bob builds locally when a cache is down` |
| BOB-E-4004 | cache | 4 | `cache {cache} served {what} without a trusted signature` | `the cache's signing key is not trusted; check bob registry list` |
| BOB-E-4005 | cache | 4 | `cache {cache} served corrupted data for {what}` | `report this to the cache admin; bob refused the download` |
| BOB-E-4006 | cache | 4 | `{label} is not in any cache and local builds are disabled` | `build it remotely (bob build --remote) or allow local builds` |
| BOB-E-4007 | cache | 4 | `uploading {label} to the cache failed` | `the build itself succeeded; the worker retries the upload` |
| BOB-E-4008 | cache | 4 | `your login to {server} has expired or is invalid` | `run bob login {server}` |
| BOB-E-4009 | cache | 4 | `no bob server configured` | `run bob login <url>` |
| BOB-E-4009 | cache | 4 | `bob server {server} is unreachable: {reason}` | `check the URL and your network` |
| BOB-E-4010 | cache | 4 | `{server} denied {action} on project {project}` | `ask an admin for the permission {permission}` |
| BOB-E-4011 | cache | 4 | `release {ref} not found on {registry}` | `bob list --available {project} shows the versions` |
| BOB-E-4012 | cache | 4 | `remote build {id} failed on the server: {reason}` | `bob log --build {id}` |
| BOB-E-5001 | upstream | 5 | `nixpkgs package {attr} failed to build from source (needed by {users})` | `this is not your code: pin another revision or ask your bob server admin` |
| BOB-E-6001 | resource | 6 | `no space left on device while building {what}` | `free disk space (bob gc) or move bob's store to a larger disk` |
| BOB-E-6002 | resource | 6 | `{what} was killed: out of memory` | `reduce parallel jobs (bob build -j) or raise the memory limit` |
| BOB-E-6003 | resource | 6 | `{what} was killed by signal {signal}` | `run the build again; if it repeats, reduce parallel jobs` |
| BOB-E-7001 | store | 7 | `bob's store is locked by another process` | `another bob command is running` |
| BOB-E-7002 | store | 7 | `bob's store is not accessible: {reason}` | `bob's state directory belongs to another user or is read-only` |
| BOB-E-7003 | store | 7 | `bob's store is damaged: {what}` | `run bob doctor --repair` |
| BOB-E-7004 | store | 7 | `bob's cache database is busy` | `another bob process is using the same state directory` |
| BOB-E-7005 | store | 7 | `bob cannot create its private /nix: unprivileged user namespaces are blocked on this system` | `fix (run once as an administrator): {fix}` |
| BOB-E-7006 | store | 7 | `shim {shim} belongs to {root}, which has no bob environment` | `run bob env in {root}, or delete {shim}` |
| BOB-E-7007 | store | 7 | `storage backend {endpoint} is not supported: {reason}` | `bob-server needs an S3 backend with conditional writes` |
| BOB-E-7008 | store | 7 | `{bucket} bucket policy is too permissive: {reason}` | `apply the shipped policy` |
| BOB-E-7009 | store | 7 | `the worker must run as root; builds themselves never run as root` | `start bob-server worker as root` |
| BOB-E-7010 | store | 7 | `worker cannot isolate builds: {reason}` | `see the worker host requirements` |
| BOB-E-8001 | interrupt | 130 | `build cancelled` | — |
| BOB-E-9001 | internal | 70 | `bob's builder for {label} is broken: {reason}` | `this is a bob bug; please report it with bob log {label}` |
| BOB-E-9002 | internal | 70 | `{label} did not produce its output` | `this is a bob bug; please report it with bob log {label}` |
| BOB-E-9003 | internal | 70 | `{label} refers to {ref}, which it must not reference` | `this is a bob bug (missing fixup)` |
| BOB-E-9004 | internal | 70 | `bob generated an invalid build step for {label}: {reason}` | `this is a bob bug; please report it with bob build -v` |
| BOB-E-9005 | internal | 70 | `Nix stopped unexpectedly ({reason})` | `run with -v and report it` |
| BOB-E-9006 | internal | 70 | `quirks entry {entry} has no justification or passing test` | — |
| BOB-E-9007 | internal | 70 | `embedded builder does not match its recorded hash` | `this is a bob bug (stale builders.go table)` |
| BOB-E-9008 | internal | 70 | `{feature} is not implemented yet` | — |
| BOB-E-9998 | internal | 70 | `unrecognised Nix log format (Nix {nixVersion})` | `bob only supports the Nix it embeds` |
| BOB-E-9999 | internal | 70 | `unrecognised Nix error` | `this is a bob bug; the raw message is shown with -v` |

## Next steps

- [CLI reference](01-commands.md): every command and flag.
- [Troubleshooting](../06-server/13-troubleshooting.md): server-side problems.
- [Limitations and status](../07-reference/03-status.md): what is not finished yet.
