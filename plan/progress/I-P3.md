# I-P3 progress (start-up cost, nested-repo scan, test corrections 33/34)

## Done
1. Lazy regexps: every package-level `regexp.MustCompile` in pkg/ (144, 35 files) is now
   `sync.OnceValue(func() *regexp.Regexp { … })`, call sites `re().X(…)` (A139). init()s left:
   kind registrations, the namespace/evalbox child hooks (argv check only).
2. eval scan stops at a directory with its own bob.lock (pkg/engine/eval/scan.go, test
   TestScanStopsAtNestedRepo); golang/gen.BobImports likewise; SPEC R-FW-002 scan text in
   spec/01-core.md and SPEC.md (A138).
3. Test corrections 33/34 applied in tests/devenv/shim_test.go (A137); SPEC R-DEV-008 acceptance
   text in spec/02-cli-dev.md and SPEC.md.
4. tools/devbench (new): hook/exec/shim/dev/LSP timing with load averages.

## Files
pkg/** (regexp vars, 35 files), pkg/engine/eval/scan.go, scan_nested_test.go,
pkg/golang/gen/root.go, tools/devbench/main.go, tests/devenv/shim_test.go, spec/01-core.md,
spec/02-cli-dev.md, SPEC.md, plan/test-corrections.md, plan/decisions.md.

## Measurements
(see below, filled in at the end)
