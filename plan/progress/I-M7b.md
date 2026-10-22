# I-M7b progress (M7.4 platforms and toolchains, M7.5 variants and projections, M7.6 budgets)

Owner of: pkg/bob platform files (platform.go, toolchain.go), pkg/engine/toolchain (new),
pkg/engine platform code (build planner: nixpkgs sets per system, host system for every drv),
pkg/cc/gen cross parts, pkg/golang cross parts (gen, bop toolchain args). Not pkg/rust, pkg/npm.

## done
- (checkpoint 0) read docs; baseline: native `bob lock` of the cc fixture 12m46s at load 30.

## next
- pkg/bob Toolchain + p.Toolchain(); pkg/engine/toolchain; engine nixpkgs per system;
  cc cross; Go cross + variants; pre-check; uncached note; qemu test runner.

## DECISIONS

## NEEDS

## TEST PROBLEM
