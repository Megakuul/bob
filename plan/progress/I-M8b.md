# I-M8b progress (Rust follow-ups: lock fixes, lock --update, cross, native defaults, --dry-run)

Owner of: pkg/rust/**, pkg/engine/lock, tools/regmirror crates parts; new file in pkg/cli.

## done
- (1) pkg/engine/lock: featureRe allows '?' (weak features), crate dependency targets capped at
  4096 bytes (checkTarget), per-crate ItemError; EncodeSection/File.Encode validation failures
  are BOB-E-1023 "<crates.io:name@ver> cannot be pinned: <reason>" (never 9005).
  Test: pkg/engine/lock TestRustSectionRealCrates.

## next
- (2) resolve prefer-locked; (3) cross; (4) native defaults; (5) --dry-run; (6) rust_1_94; (7) tests.
