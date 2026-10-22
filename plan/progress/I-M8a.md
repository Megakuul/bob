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

## next
- full R-RS-002a corpus run (running), error-path checks.

## DECISIONS
- D-M8a-1: rust.Test's result type is rust.TestSet (SPEC names type and function Test).
- D-M8a-2: Toolchain is a bob.Tool (nixpkgs.Tool returns *ToolPackage; the tests pass one).
- D-M8a-3: features unify per root node (one Unify per crate/test node); shared crates get one
  derivation when their features agree (drv dedup by path).

## NEEDS
- (see final report)

## TEST PROBLEM
1. SPEC §03.3.1 and the fixtures (rust/corelib/bob.go, polyglot/crates/rcore/bob.go) use
   `*rust.Test` as type and `rust.Test(...)` as function; Go cannot declare both in one package.
