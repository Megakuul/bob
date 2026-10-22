# I-M8a progress (M8.1 integration, M8.2, M8.3: Rust ecosystem)

Owner of: pkg/rust/** (resolve internals only for fixes), tools/regmirror; one-line edits in
pkg/cli/link.go (blank import pkg/rust/gen) and cmd/bob-builder/main.go (rustbop.Kinds()).

## done (checkpoint 1)
- pkg/rust API (evaluator side): Crate/Features/NoDefaultFeatures/Rename, Uses, LibOpts/BinOpts,
  Library/ProcMacro/StaticLib/CDylib/Binary, (*Lib).With, TestOpts/Test (type TestSet, see
  TEST PROBLEM 1), CfgTable. Kinds rust.crate, rust.test, rust.tests (pkg/rust/internal/rsspec).
- cfg tables moved to pkg/rust/internal/cfgtab (pkg/rust may not import resolve: the
  evaluator's framework zip excludes resolve/); resolve.CfgTable uses it.
- pkg/rust/gen: Lock (resolver + per-crate download for meta + fetch.Resolve for narHash,
  unchanged inputs → no request), Stale, Native (Cargo.toml workspace resolver "3" + per crate,
  Cargo.lock from bob.lock, .cargo/config.toml, .bob/vendor stand-ins), Plan (per-crate units,
  build-script compile/run, proc-macros, staticlib/cdylib LinkInfo, test units + test-run).
- pkg/rust/bop: kinds rustc, rust-build-run.
- tools/regmirror: crates route (config.json, sparse index files, .crate downloads) and
  -record-crates (manual online capture into the manifest/blob dirs).
- Private test copy: /var/tmp/i-m8a/{sync.sh,repo,regmirror,registry,blobs} (HEAD + my files;
  the shared tree does not build: other units' WIP in pkg/golang/gen, pkg/cc/gen).
- TestRRS002aResolutionEqualsCargo/itoa-hex green in the private copy (bob lock + bob gen
  Cargo.lock byte-identical to cargo generate-lockfile through the mirror).

- Build path verified on /var/tmp/i-m8a/scratch (the rust fixture without cfoo/usec/cmain, root
  package renamed, Test type renamed, see TEST PROBLEM): bob lock (53 crates), bob build //...
  (demo, corelib, mymacro, rcore, rdyn), demo under env -i prints the S3 line
  (json, regex, zstd 650->29, local derive, corelib=42 fast build.rs target=aarch64-…);
  bob test //... passes both units; a second store (fresh BOB_HOME) gives the same output paths,
  bit-identical (diff -r). cargo metadata --locked --offline (pinned cargo 1.95.0) exits 0 on
  the generated Cargo files + .bob/vendor stand-ins.
- Unit tests: pkg/rust/gen (plan of lib+build script+proc-macro+bin+test, strict decode of every
  job into bop/bspec types, deterministic, mixed toolchains → 1020 with the SPEC text, Cargo files,
  rust-project.json), pkg/rust/bop (directives, rustc argv, invalid jobs), tools/regmirror
  (crates routes, record mode).
- PrepareDev (pkg/rust/gen/dev.go; engine.DevPreparer is another unit's WIP, not in HEAD):
  rustc, cargo, rust-analyzer, rustPlatform.rustLibSrc packages, rust-project.json.

- Full rust fixture (with TEST PROBLEM 1+2 applied in a copy, /var/tmp/i-m8a/fx; pkg/cc now in
  HEAD): bob lock (53 crates, R-RS-005 facts present: serde features, serde_derive deps,
  zstd-sys features + links "zstd", tokio deps + features, every meta.edition), bob build //...
  builds all 8 packages; demo and usec (Rust bin + bob C static lib: cfoo_mul(6,7)=42) run under
  env -i; cmain (C + Rust cdylib + staticlib) prints rdyn_add(40,2)=42 rcore_num_hex(7)=37 with
  LD_LIBRARY_PATH (see TEST PROBLEM 7); bob test //...: corelib_test and corelib_test_num pass.
  Its Cargo.lock is byte-identical to cargo generate-lockfile (59 packages).
- TestRRS002aCfgTablesEqualRustc: PASS (shared tree; warm harness cache, TEST PROBLEM 5).
- R-RS-002a corpus with a warm BOB_HOME (/var/tmp/i-m8a/corpus.sh, same layout and cargo call as
  the test, pinned cargo 1.95.0 through the regmirror): see "corpus" below.
- Stale: unchanged inputs → fresh; otherwise the graph's crates are resolved against the locked
  crates only (a build of part of the repository is not reported stale).
- Variant features (AxisFeatures, Proj "features") are added to a root's Enable list.

## next
- nothing in this chain beyond NEEDS / TEST PROBLEM.

## DECISIONS
- D-M8a-1: rust.Test's result type is rust.TestSet (SPEC names type and function Test).
- D-M8a-2: Toolchain is a bob.Tool (nixpkgs.Tool returns *ToolPackage; the tests pass one).
- D-M8a-3: features unify per root node (one Unify per crate/test node); shared crates get one
  derivation when their features agree (drv dedup by path).

## NEEDS
1. pkg/engine/lock (owner: engine/lock unit; patch verified in the private copy,
   /var/tmp/i-m8a/patches/lock.diff): bob.lock validation rejects real crates:
   a. featureRe has no '?': weak dependency features ("memchr?/std" of aho-corasick, "quote?/proc-macro"
      of syn) fail every graph with regex or syn: `featureRe = ^[A-Za-z0-9_][A-Za-z0-9_+./:?-]{0,127}$`.
   b. dependency targets go through checkText (maxName 512); rustix has an 851-byte cfg():
      a separate cap (4096) for CrateDep.Target.
   Without (a) and (b) `bob lock` fails for the rust fixture and 12 of 14 corpus workspaces
   (shown as BOB-E-9005 "Nix stopped unexpectedly (lock: section rust: …)" — that code is wrong
   too: a lock-section validation error of an ecosystem's own output is an internal error).
2. pkg/cli (I-M4f): `bob build --dry-run` (plan only, ruling G24) for R-RS-005.
3. Dev environment (M6 owner): rust-analyzer needs the proc-macro dylibs and build-script
   OUT_DIRs in rust-project.json, i.e. planned and realised bob derivations; DevRequest has no
   Builder/Resolver and PrepareDev may not build. Proposal: DevPrep.Build []engine.Resolved
   (derivations the CLI realises with the packages) plus Builder/Resolver in DevRequest. Until
   then rust-project.json resolves imports but not derive/attribute macros or include!(OUT_DIR).
4. Native-crate defaults table (§03.3.6, openssl-sys → nixpkgs openssl): a registry crate's
   build script can only get a nixpkgs package that is a node of the graph, and the evaluator
   does not know the locked crates. Needs either evaluator access to bob.lock or a
   plan-time nixpkgs lookup that the engine plans (PlanStore). rust.Uses works.
5. Cross (M7.4): the Rust planner returns BOB-E-9008 for a foreign platform; host units,
   --target, and the rust-std-<triple> sysroot (R-FW-034) come with the platform work.
6. framework.zip must be regenerated (go generate ./pkg/engine/eval) when pkg/rust is
   committed (pkg/rust changed: API, internal/cfgtab, internal/rsspec).
7. resolve (lock --update): the resolver has no "prefer locked versions" mode, so a re-lock
   after a declaration change moves every pin to the newest matching version (Cargo keeps
   unrelated pins). Needs resolve.Options.Locked (I-R-crates' package).

## TEST PROBLEM
1. SPEC §03.3.1 and the fixtures (rust/corelib/bob.go, polyglot/crates/rcore/bob.go) use
   `*rust.Test` as type and `rust.Test(...)` as function; Go cannot declare both in one package.
   Implemented: func Test returning *rust.TestSet. Fix: the two fixtures write `*rust.TestSet`.
2. tests/fixtures/repos/rust/bob.go is `package rust` but go.mod says module
   example.com/rustfix and the root has no other Go code: the evaluator's R2 rule wants
   `package rustfix` (BOB-E-1024). Every test on the rust fixture stops there.
3. tests/core TestAPISurface: "constructors [Library ProcMacro] all return *Lib" and
   "[StaticLib CDylib] all return *CLib" — the SPEC API (§03.3.1) and the fixtures (mymacro:
   `*rust.Lib` from ProcMacro; rdyn/rcore: `*rust.CLib`) require exactly that.
4. TestRRS004a*: the attribute "rust_1_94" is not in the pin (nixos-26.05 has rust_1_95,
   rust_1_97, rust_1_98, which are package sets, not tools; a rustc tool would be e.g.
   "rust_1_97.packages.stable.rustc").
5. serverenv.NixTool("rustc") returns the first printed output of `nix build`, the -man output,
   on a cold harness cache (TestRRS002aCfgTablesEqualRustc then fails with "fork/exec
   …-rustc-wrapper-1.95.0-man/bin/rustc"); with the cache warm it returns the out link and
   the test passes.
6. On this loaded host (load 25–30 on 8 CPUs) a fresh BOB_HOME's first `bob lock` (nixpkgs
   tarball import) exceeds the harness's 5-minute cap per bob run; the corpus subtests then
   exit -1. Measured with a warm home instead (corpus.sh, results below).
7. TestRRS001a runs outputs straight from $BOB_HOME/nix/store under env -i. Their ELF
   interpreter and RUNPATH name /nix/store/... paths (they must: that is where they live inside
   bob's namespace). demo and usec only need glibc/gcc-lib, which this host's own /nix/store
   happens to hold; cmain also needs librdyn.so from the Rust cdylib, which exists only in bob's
   store, so it fails with "librdyn.so: cannot open shared object file" outside the namespace
   (it runs with LD_LIBRARY_PATH=<home store>/…-rdyn/lib). The test needs to run the outputs
   inside bob's namespace (or a host /nix bind), as it does for the foreign platform via qemu -L.
