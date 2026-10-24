# GH-B progress
DONE R-CLI-001 (no findings), R-CLI-005 (no findings)
DONE R-CLI-006 (no findings; live fault-catalogue check lives in tests/errors, out of scope)
FINDING R-CLI-002 HIGH FIXED events_test.go checkStream: /nix/store check + leakcheck over whole stdout false-failed a correct impl (--print-paths host paths <state>/nix/store, --json-log raw lines); now strips paths[] and log line
FINDING R-CLI-002 MED FIXED output_test.go VerboseAddsLines compared Stdout only (vacuous if human output on stderr); now stdout+stderr and non-empty
FINDING R-CLI-002 MED OPEN events_test.go checkSchemaFile: returns silently when schema/events-v1.json absent; never validates against schema (hand-written validateEvent only) -> acceptance bullet 1 weak. schema/ dir does not exist. Owner decision
FINDING R-CLI-002 LOW OPEN VerboseAddsLines covers 3 runs not "every golden"
FINDING R-CLI-007 MED OPEN output_test.go TestRCLI007Colour: requires ESC[1;31m or ESC[31;1m for red bold; SPEC says only "red bold" (stricter; ESC[1m ESC[31m or 91 would fail)
FINDING R-CLI-007 LOW OPEN byte-exact S15 golden set is in tests/errors (outside scope); here only own golden
DONE R-CLI-002, R-CLI-007 (see findings)
FINDING R-CLI-008 MED OPEN logs_test.go:~70 local log: excerpt lines only required to appear (any order) among last 12 log lines, rewritten (…/) lines skipped; SPEC "last 10 lines match the excerpt" -> wrong impl printing excerpt lines scattered in last 12 passes
FINDING R-CLI-008 LOW OPEN logs_test.go -f case: never asserts the build was still running when -f started (sleep 5s only); requires -f output to start at line 1 (SPEC only: ends with 1000, no gaps/dups)
DONE R-CLI-008
DONE R-CLI-003 (no findings)
FINDING R-CLI-004 LOW OPEN completion_test.go: SPEC lists //libs/greet:greet but fixtures have only libs/foo (SPEC/fixture mismatch); test requires only //libs/foo:foo, and does not require the complete label set
DONE R-CLI-004
FINDING R-CLI-010 HIGH OPEN commands_test.go (--update block ~l.205): uses tests/fixtures/repos/go whose bob.lock is empty; after `bob lock` pins latest, `--update golang.org/x/sync` reports "no changes" and the loop over lines()[1:] is empty -> a no-op or update-everything impl passes; SPEC names golang.org/x/net and "changes only that module's entries (+transitive, listed)": never asserts changed names. Needs an intentionally outdated lock fixture (owner decision)
FINDING R-CLI-010 LOW FIXED commands_test.go: "exactly one + line" only counted zod lines; now counts all "  +" lines == 1
FINDING R-CLI-011 MED OPEN commands_test.go ~l.330: "--format json edge set equals the evaluator's graph" compared only with bob's own --json result (+1 hard-coded edge); no schema check; exit of --format json run unchecked
FINDING R-CLI-009 LOW OPEN TestRCLI009CheckLock never runs `check --fix` on a 1012 finding (statement: --fix runs bob lock; doc comment claims it); "check changes no file" asserted only for clean/1011, not 1012/1013
DONE R-CLI-009,010,011,012 (R-CLI-012 no findings)
FINDING R-INST-002 HIGH OPEN nonno_test nonix_test.go noNix script: asserts `! command -v nix` but harness host has real /usr/bin/nix (ELF) and PATH=/usr/bin:/bin -> once bob install is implemented the precondition still fails (exit 1 -> Fatalf "install without /nix"); test can never go green on this host. bind-mount /dev/null trick does not work (bash command -v still reports it). Owner decision: drop nix from the precondition or run with a PATH without /usr/bin
FINDING R-INST-001 MED FIXED lifecycle_test.go: post-uninstall `list --json` loop vacuous when no result event / list failing; now requires exit 0 + result event (see edit)
FINDING R-INST-001 MED OPEN KillPoints use wall-clock offsets 0..1500ms against a local fake server; upgrade likely finishes in <50 ms so most offsets kill nothing; no check that a kill hit mid-upgrade; non-atomic current flip would pass unless hit by luck
FINDING R-INST-003 LOW OPEN auth_test.go: netrc check comment says mode 0600, only content "bob_" checked; token-not-on-cmdline not tested; failed private install does not assert nothing was installed. Server-side half of acceptance (anonymous ResolveRelease/narinfo 200, NotFound for private) belongs to server suites (not in this scope)
DONE R-INST-001..004 (see findings)
FINDING R-INST-006 MED FIXED isolation_test.go: comparison was vacuous if hook / bob dev never activated; now fails if BOB_PROJECT unset after hook and BOB_DEV!=1 under bob dev
FINDING R-INST-005 MED OPEN profile_test.go GCRoots: only names of roots [3..7] checked; "every store path of each of last 5 generations has a GC root" not asserted (comment claims it); create-only temp+rename of generations/<n>.json not tested
DONE R-INST-005,006
DONE R-DEV-003,007,009 (no findings)
FINDING R-DEV-008 LOW OPEN shim_test.go ExecAndShims: namespace check runs 'go run ./ns' via the shim, not subprocesses started by gopls (SPEC bullet 2); ShimOverhead measures 'bob exec -- /bin/true' directly (no sh shim hop), no baseline subtraction
FINDING R-DEV-010 LOW OPEN lsp_test.go: hover/definition inside the opened store file asserted for gopls only (SPEC: 'same script shape' for clangd/rust-analyzer/typescript-language-server runs definition only)
DONE R-DEV-010
DONE R-DEV-011, R-DEV-001, R-DEV-006 (no findings)
FINDING R-DEV-005 MED FIXED login_test.go: request-log check used TrimPrefix(concat of all logs, earlier concat) which fails when several process logs grow, so the harness's own 403 PUT /nix/ attempt (taken before) would be reported as a write by bob build --remote; now per-file offsets (logSizes/logsSince)
FINDING R-DEV-005 LOW OPEN login_test.go: "internal promote endpoint" is approximated by WorkerControlService/GetCheckoutCredential (SPEC names no promote RPC); cache write regex depends on unknown request-log format
FINDING R-DEV-002 LOW OPEN vite_test.go: "server answers again" after the dependency change is satisfied by a server that never restarted/ went down; orphan check only covers descendants captured before SIGTERM (reparented daemons missed; SPEC says cgroup/process tree)
DONE R-DEV-002,004,005
FINDING R-OUT-* HIGH FIXED helpers_test.go world.bobEnv: leakcheck over the combined output of every `bob build --print-paths --json` flagged the host store paths <home>/nix/store/<hash> (printed on purpose) -> every OUT test using w.build would fail on a correct impl; now paths[] is blanked for the check
FINDING R-OUT-006 HIGH OPEN build_test.go:~72: first build of the bundles runs under `unshare --net` with a fresh BOB_HOME; only //cmd/api:api was built before, so nss-cacert/tzdata/glibc etc. for image/appimage/tarball cannot be substituted offline -> a correct impl fails (comment says "everything else is warm" but nothing warms it). Fix idea: build online first (warm), then assert non-FOD derivations + offline no-op rebuild
FINDING R-OUT-001 LOW (logged SPEC problem) uses package.finish paths instead of {"type":"result"} 
DONE R-OUT-001, R-OUT-006
FINDING R-OUT-010 MED FIXED oci_test.go: nix/ and nix/store/ 0555 uid0 dir check was skipped for layers 1..n-2 (SPEC: "each layer includes"); now every layer
FINDING R-OUT-010 LOW OPEN history[i].created loop vacuous when config has no history; gzip DefaultCompression, JSON field order, blob reuse by diff_id not asserted
FINDING R-OUT-009 HIGH(probable false red) OPEN oci_test.go:~132: asserts layer 0 holds "-glibc-" alone as "most popular path"; popularity(p) counts closure paths whose closure contains p, glibc's own deps (libidn2/libunistring in nixpkgs glibc closure) have strictly higher popularity -> a correct impl puts them in layer 0. Not in SPEC acceptance (only 14 layers / one blob differs / <=16% / MaxLayers 4). Compute expected order from nix path-info closure or drop
FINDING R-OUT-009 LOW OPEN v2 vs v1: config differing not asserted (manifest only)
FINDING R-OUT-011 LOW OPEN image layout "layer blobs are symlinks to layer outputs; config/manifests/index regular files" not asserted; layer refs check tolerates self-reference
FINDING R-OUT-013 LOW OPEN uses `bob build --remote --version` not the release pipeline; staging/COMPLETE.json images[] (worker half) not checked here
DONE R-OUT-002,009,010,011,012,013
FINDING R-OUT-015 MED OPEN appimage_test.go:~222 licence check: every runtime Go dep must start with golang.org/x/ (allowlist) -> stricter than SPEC "no LGPL/GPL runtime code (licence check on the module graph)"; a permissive third-party dep fails
FINDING R-OUT-015 LOW OPEN first-run extract <=0.5 s / cached <=0.1 s are in the SPEC acceptance text but not asserted (left to budget B-086, status proposal); squashfs -all-root/-no-xattrs/-all-time flags only partly checked (mkfs_time, bs, comp)
FINDING R-OUT-016 LOW OPEN BOB_APPIMAGE_MODE=ldso is never run where user namespaces are blocked (impl whose ldso mode secretly uses userns passes); "userns" debug check is a substring test; child exit-code propagation / `read launch.json:` message untested
FINDING R-OUT-007/019 note: SPEC §4.11 says §04 error text is on stderr; OUT-007 keeps r.Stderr (correct); R-OUT-019 warning stream unspecified -> checked on Combined (was Stderr only: FIXED, minor); DEV-001 BOB-E-1014 and DEV-011 BOB-E-0004 streams unspecified in SPEC -> Combined (FIXED, was Stderr only)
DONE R-OUT-007,008,015,016,019
DONE R-OUT-003,017,018,020 (slow; no findings), R-OUT-004 (no findings)
FINDING R-OUT-023 MED FIXED release_test.go: "keys sorted" was asserted as byte-equality with COMPACT canonical JSON (SPEC example is indented, SPEC asks only sorted keys); now token-order check (unsortedKeys)
FINDING R-OUT-023 LOW OPEN per-kind required fields live only in the schema description (validator subset ignores them; the test body checks them partly); invalid-version message compared with %q (escaped) while SPEC shows raw <v>
DONE R-OUT-022,023
FINDING R-PLAT-* HIGH FIXED platforms/helpers_test.go bobEnv: same leakcheck-vs---print-paths host path false positive as outputs; paths[] blanked
FINDING R-PLAT-004 LOW OPEN last assertion (binary must not run natively) fails on hosts with qemu binfmt registered; "extra-platforms unset" not asserted
FINDING R-PLAT-005 LOW OPEN cross outputs run under qemu only for polyglot and cc (SPEC: every fixture prints golden line); libs/staticlib outputs not ELF-checked
FINDING R-PLAT-007 LOW (SPEC problem logged A34) BOB_TEST_NIXPKGS_INDEX injection; rustc case asserts only "nixpkgs mismatch" + both versions regex
DONE R-PLAT-001,004,005,006,007; R-PLAT-002 manual; clients helper (no findings)
ALL DONE - report written to docs/review/gate-h-B.md
