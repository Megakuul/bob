# I-M4c progress (M4.5 tests and loopback services, per-test-unit result cache; O-02-9)

Owner of (new code only): pkg/builder/testrun.go (+ bspec/testrun.go, testdata/helper),
pkg/engine/build/testrun (new package), pkg/services/postgres, pkg/bob/servicespec.go (new
file), pkg/bob/ext/services.go (new file). Small edits in finished units' files: pkg/builder/
builder.go (Core registers "test-run"), pkg/builder/run.go (Exec uses the shared baseEnv/envList),
pkg/engine/store/builders.go regenerated (`go generate ./pkg/engine/store`, builder bytes changed).

## done (M4.5, engine/builder level)
- bob-builder kind `test-run` (bspec.TestRunArgs/Service/Probe/TestResult/TestCase): services
  started in list order on 127.0.0.(i+2) (ADDR for the service and its probe, `$ADDR` expanded in
  argv and Env, `<NAME>_ADDR` + Env to the test), TCP or command probe until the timeout,
  early-exit detection, teardown SIGTERM to the process group, 5 s, SIGKILL; never-ready /
  exited service → last 20 log lines + `bob::` diag `<label>: service <name> did not become ready
  within 30s` + final line; test with HOME=$TMPDIR, output tee'd to $out/test.log and the build
  log; `$out/test-result.json` v1; format "go" parses `-test.v` cases (failing case output ≤ 64
  KiB); a failing test writes `bob:: {"type":"test-result",…}` to the log and fails (exit 1).
- pkg/engine/build/testrun (E3): Units (test-run drvs by bobSpec kind, label = job label),
  Cached (one batched validity query), Rerun (nonce attribute `bobRerun`, new output paths),
  ReadResult (bounded, strict), FailedResult (from the build log), Result.DurationMs/Failed,
  Status pass/fail/cached/skipped.
- pkg/bob: *ServiceSpec implements Service; bob.SpecOf; Probe.Port/Argv.
  pkg/bob/ext: ext.Services(services, deps) → IR form + deps with the tools appended; definition
  errors for nil/no start/no or two probes/bad name/duplicate <NAME>_ADDR; default timeout 30 s.
- pkg/services/postgres: Service(p, Options{Version, Databases}) (bash start script: initdb
  --no-sync, postgres on $ADDR:5432 with fsync off, createdb, ready mark; SIGTERM → fast
  shutdown), env PGHOST/PGPORT/PGUSER/DATABASE_URL; ExampleService.
- Tests (green): pkg/builder TestTestRun{Services,NeverReady,ServiceExits,FailingTest,Invalid},
  TestAddrVar; pkg/bob/ext TestServices; pkg/engine/build/testrun TestUnitsCachedRerun,
  TestResultDecoding, TestTestRunInNix (real embedded Nix: service on 127.0.0.2 in the sandbox,
  cached after the run, `--check` fails, Rerun runs again), TestPostgresServiceInNix (the real
  postgres spec, closure copied from the host store: two units in one parallel realise, each
  ~0.7 s incl. initdb+start, whole realise 0.83–0.87 s [8c/15G shared host], 0 postgres left).
  Store TestEmbeddedBuilderMatchesTable etc. green after regeneration.

## still red (not in my packages)
- tests/core TestRFW025PostgresServices, TestRFW025NeverReadyService, TestRFW064*,
  TestRFW024*: red only with "bob test/build is not implemented yet" (BOB-E-9008); they need
  `bob test` (M4.6 CLI + build.Run), the Go ecosystem's Tests (M5) and the nixpkgs build path (M4.3).
- tests/core TestAPISurface: red for other units' missing examples only (postgres has
  ExampleService).
- `go run ./tools/ci -only fmt,vet,lint,layering` fails only on other units' work in progress
  (evalbox.go gofmt, eval framework.zip embed, translate → diag layering); fmt/vet/staticcheck/
  boblint on my packages are clean. Seen once under parallel load: pkg/engine/store
  TestConcurrentStores "nix-store --import failed" (passes alone; not my code).

## files
- pkg/builder/bspec/testrun.go, pkg/builder/testrun.go, pkg/builder/testrun_test.go,
  pkg/builder/testdata/helper/main.go; pkg/builder/builder.go (1 line), pkg/builder/run.go (Exec env)
- pkg/engine/store/builders.go (generated)
- pkg/engine/build/testrun/{testrun.go,testrun_test.go}
- pkg/bob/servicespec.go, pkg/bob/ext/{services.go,services_test.go}
- pkg/services/postgres/{postgres.go,tools.go (nixpkgs.Tool: postgresql_<v>, bash, coreutils),example_test.go}

## DECISIONS
- D-M4c-1 (O-02-9): `bob test --rerun` uses a nonce, not `nix build --rebuild`. Measured with the
  embedded Nix 2.34.9 (TestTestRunInNix): `nix-store --realise --check` of a passing test unit
  fails with "may not be deterministic: output … differs" (exit 104) because test-result.json and
  test.log carry timings. testrun.Rerun adds structured attribute `bobRerun=<nonce>` to the
  test-run derivation (a leaf: nothing else changes); the original cached result stays valid.
- D-M4c-2: a test unit is any derivation of builder kind "test-run"; its label is the job label.
  Cached ⇔ output "out" valid (after bob's substitution step). No separate result database.
- D-M4c-3: service address/env contract lives in the builder: instance i → 127.0.0.(i+2),
  `ADDR` for the service and its probe, `$ADDR` expanded in argv and in exported Env values,
  `<NAME>_ADDR` (upper-case, other chars → `_`). Each service gets its own HOME=TMPDIR
  (<build>/tmp/services/<i>-<name>) and log file. Test env precedence: base < services < job Env.
- D-M4c-4: TestResult.durationSeconds covers service start + test run (what the unit costs; the
  R-FW-025 "≤ 3 s" bound is read from it), not the teardown.
- D-M4c-5: `*bob.ServiceSpec` itself implements `bob.Service` (still sealed: only pkg/bob types),
  so service packages return `&bob.ServiceSpec{…}`; `bob.SpecOf` + `Probe.Port/Argv` let
  ecosystems read it; `ext.Services` is the one IR encoding (tools as dep indices).
- D-M4c-6: kind test-run has no fixup (NoFixup); outputs are JSON and text.
- D-M4c-7: BuilderVersion stays 1 (no release yet; the store path moved with the bytes). If the
  "builder changed without a bump" CI check lands, bump to 2 then.
- D-M4c-8: the probe of a postgres service is a command (`[ -e $HOME/ready ]`) written by the
  start script after createdb, so the test never starts before its databases exist.

## NEEDS
- Commit order: pkg/services/postgres/tools.go imports pkg/nixpkgs (nixpkgs.Tool), which is
  I-M3c's uncommitted work (pkg/nixpkgs/nixpkgs.go); commit postgres with or after it.
- M4.6 (build.Run / pkg/cli `bob test`): use testrun.Units on the planned drvs; Cached → status
  `cached` (read ReadResult for durationMs/cases) and do not build them; `--rerun` → testrun.Rerun
  with a fresh nonce (e.g. time + random) for the selected units, build those; a built unit →
  ReadResult (pass) or FailedResult from its log (fail, failed[] = Result.Failed()); test.result
  event fields label/status/durationMs/failed[]. Units that never ran (service failure) → fail
  without result. Count `built` from derivations actually built.
- M5 ecosystems (pkg/golang etc.): Tests opts.Services → `svcs, deps := ext.Services(o.Services,
  deps)` into the kind spec; the generator turns each ext.Service into bspec.Service (Tools →
  `<out>/bin` PATH, ReadyPort/ReadyCmd → Probe, TimeoutMs) in the unit's test-run job, format "go"
  with `-test.v` for Go.
- M4.2 translator: a failed test-run derivation whose log has a `bob::` diag must show the diag
  msg verbatim (multi-line: `<label>: service <name> did not become ready within 30s` + the last
  20 log lines; tests/core TestRFW025NeverReadyService) and exit 1; test failures → BOB-E-2005.
  A code for "service not ready" is not in §02.3 (2005 is "tests failed"); SPEC/diag may add one.
- Layering (pkg/layering_test.go owner): E2 generators need bspec (same NEED as I-M4a).

## TEST PROBLEM
- none found in tests/core for R-FW-025/R-FW-064 so far (could not compile tests/core, see above).
