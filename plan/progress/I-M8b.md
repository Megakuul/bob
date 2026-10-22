# I-M8b progress (Rust follow-ups: lock fixes, lock --update, cross, native defaults, --dry-run)

Owner of: pkg/rust/**, pkg/engine/lock, tools/regmirror crates parts; new file in pkg/cli.

## done
- (1) pkg/engine/lock: featureRe allows '?' (weak features), crate dependency targets capped at
  4096 bytes (checkTarget), per-crate ItemError; EncodeSection/File.Encode validation failures
  are BOB-E-1023 "<crates.io:name@ver> cannot be pinned: <reason>" (never 9005).
  Test: pkg/engine/lock TestRustSectionRealCrates. `bob lock` on the rust fixture: ok.
- (2) resolve.Options.Locked (locked versions first, yanked locked allowed); gen keptPins
  (no --update: keep all; --update / --update rust: none; --update <crate>: all but it).
  Tests: resolve TestLockedPreferred, gen TestKeptPins.
- (3) cross: rsspec.Crate.Cross (cross cc, binutils, target rustc.unwrapped), rsspec.Test
  Emulator; host units use host tools/triple only (R-FW-035); target units --target, --sysroot
  rust-std-<triple>, -C linker=<prefix>cc; kind rust-std in pkg/rust/bop; BuilderVersion 8.
  Verified: fixture `bob build --platform linux-amd64 //...` on arm64, demo/usec/cmain run
  under qemu-x86_64; `bob test` @linux-amd64 units pass with runner qemu-x86_64; rust-std output
  = manifest files + manifest (56 files). Tests: gen TestPlanCross, TestPlanCrossMismatch,
  bop TestWriteStd.
- (6) SPEC R-RS-004a decided (rust_1_97.packages.stable.rustc); tests/eco/rust 004a updated.
- (7) TestAPISurface allows the SPEC-listed shared results; R-RS-001a runs native outputs via
  bob run.

- (5) `bob build --dry-run`: pkg/cli/cmd_dryrun.go (+ flag and dispatch in cli.go), engine/build
  Options.DryRun / Result.Planned (small hook in build.go, pipeline.go). Manual: warm home, fresh
  home (online, no crate-* in store) and the same home under `unshare -n`: identical 89 paths.
  Test: pkg/cli TestDryRunFlag; acceptance TestRRS005 (correction 28).
- (4) native-crate defaults table (pkg/rust/gen/defaults.go, plan time via PlanRequest.Nixpkgs).
  Manual: crate using openssl 0.10 without rust.Uses builds and runs natively (bob run:
  "ossl sha256(x)[0]=2d OpenSSL 3.6.5") and cross (qemu-x86_64, same line).
  Tests: TestNativeDefaultsAdmission, TestNativeDefaultsPlanned.
- builders.go regenerated (BuilderVersion 8), framework.zip regenerated.
- Records: plan/decisions.md M8b-1..8, plan/test-corrections.md 25-28, SPEC R-RS-004a and §02
  --dry-run (spec/ + SPEC.md).

## NEEDS
1. schema/events-v1.json: no event for a plan-only run's derivations (--dry-run --json prints
   only run.start/run.finish).
2. tests/eco/rust noNetwork as host root maps uid 0→0 only; evalbox refuses to evaluate as root
   without 65534 (A94). Works when the eval cache hits (same repo dir, same evaluator Go).
3. Files outside ownership touched: pkg/engine/build/{build.go,pipeline.go} (DryRun hook),
   pkg/cli/cli.go (dry-run flag, runBuildCmd), pkg/builder/bspec (BuilderVersion 8),
   pkg/engine/store/builders.go, pkg/engine/eval/framework.zip, protected tests per
   test-corrections 25-28, SPEC.md + spec/02, spec/03.
4. ci fmt/vet/lint failures are other units' (pkg/engine/build/dev.go fmt, out/uicheck,
   internal/store/draft_test.go).
