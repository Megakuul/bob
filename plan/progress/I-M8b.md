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

## next
- (5) --dry-run, (4) native defaults, regenerate builders.go + framework.zip, records.
