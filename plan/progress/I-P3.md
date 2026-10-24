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

5. framework.zip regenerated (`go generate ./pkg/engine/eval`): the L0–L3 sources changed.

## Measurements (2026-10-22, this host: 8 CPUs aarch64, 15G, heavily loaded; release build,
## CGO_ENABLED=0, project = tests/devenv/testdata/repos/goonly copy, own HOME, root user)
tools/devbench, new binary vs the same tree before the change (bob-old), interleaved:
| path | new (load 13-15) | old (same time) | budget [8c/15G] |
|---|---|---|---|
| hook fast path `bob env --hook=bash`, n=100 | p95 5.4-5.6 ms, median 4.2-4.3, min 3.5 | p95 13.9-23.0, median 11.0-11.8, min 8.9 | B-079 p95 <= 10 |
| `bob exec --project . -- /bin/true`, n=100 | p95 12.7 ms, median 10.0-10.1, min 7.9 | p95 27.7-29.1, median 21.5-22.4, min 17.5 | B-080/B-018 p95 <= 15 |
| `bob dev -- true` no-op, n=5 | median 13.0-13.8 ms | 28.2-30.5 ms | B-014 <= 0.5 s |
| shim `.bob/bin/go version` (incl. go), n=20 | median 25 ms (load 16) | 15.6 | B-083 (tool time included) |
| warm definition `.bob/bin/gopls` + proxy, n=20 | mean 0.63 ms, host-path URIs | 0.63 | B-081 mean <= 2 |
Same benchmark at load 20-34: hook p95 27.8 ms (old 69.6), exec p95 53.9 (old 63.8).
GODEBUG=inittrace=1 `bob env --hook=bash`: init 1.1-1.2 ms, 153 KB, 1.3k allocs (old 6.3-8.4 ms,
3.1 MB, 33k allocs); largest remaining: pkg/golang/gen 0.1 ms. `bob version` min 3.3 ms (old 8.4).
`go list -deps ./cmd/bob`: 271 packages; one executable means all are linked and initialised, so
lazy init is the lever (the fast path itself is in pkg/cli/devenv, 13 bob deps).
Nested repo check: parent with nested sub/ (own bob.lock, bob-headed go.mod): old binary fails
with BOB-E-1024 on sub/cmd/hello/bob.go, new binary evaluates the parent.

## Notes / NEEDS
- devenv stamps contain bobVersion() but not the framework hash: a rebuilt dev bob with changed
  framework sources (same "v0.0.0-dev") keeps the hook/`bob dev` fast path on an old env.json.
  Release builds differ in version, so only developers of bob see it (pkg/cli/devenv owner).
- tools/ci -only fmt,vet,lint,layering: fmt, layering ok; vet fails only on out/uicheck (R-ARCH-001,
  other unit's scratch), lint only on internal/store/draft_test.go SA1019 (other unit).
- go test ./pkg/...: all ok except pkg/engine/srcs TestImportManyTiming (load: cold import 84 s
  vs 5 s budget; package untouched); TestFrameworkZip fixed by regeneration.
