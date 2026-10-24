# I-P1 progress (cross-cutting items from finished units)

Touched: pkg/engine/build/native.go, pkg/engine/engine.go (comment), pkg/engine/drv/aterm.go,
pkg/bob/eval.go, pkg/engine/ir/evalout.go, pkg/engine/translate/render.go,
pkg/engine/fetch/{fetch.go,client.go,loopback.go,loopback_bobtest.go}, spec/03-ecosystems.md +
SPEC.md (§03.1.1), tests/fixtures/repos/{cc/app,cc/libs/mathx,cpp-lib,rust/cmain,
polyglot/libs/foo}/bob.go (test-corrections item 18 / A108), pkg/engine/eval/framework.zip
(regenerated). pkg/builder unchanged (no BuilderVersion bump).

## done
1. native.go: Gen/Check pass the repository root to every ecosystem's Native
   (`nativeFilesAt(ctx, root, …)`, PlanRequest.Root); cc writes compile_commands.json.
   Test: pkg/engine/build/native_root_test.go.
2. drv.Parse moves `__json` into Derivation.Structured when printing Structured gives back the
   same bytes (canonical JSON, integers only); otherwise `__json` stays in Env, so Parse→ATerm
   stays the identity for any derivation (nixpkgs ones with floats included). The I-M7a job
   golden candidates (out/golden-candidates/tests/goldens/eco/cc/jobs) are json.Marshal of
   Structured["bobSpec"] (sorted keys, integers): consistent. Test: drv/structured_parse_test.go.
3. pkg/bob misuse (ext.Errorf): a message starting "<file>.go:<line>: " carries that position,
   a following "//dir:name: " the label; ir.evalDiag strips the position (raw or mapped file).
   Result: `libs/mathx/bob.go:12: invalid build definition: //libs/mathx:mathx: flag …`.
   Tests: pkg/bob/misuse_pos_test.go, pkg/engine/ir/evalout_pos_test.go.
4. Human output (D-P1-1): BOB-E-1020 and BOB-E-0006 blocks start with `✗ <message>` instead of
   `error[CODE]: <message>`; a code-less definition error (option "build definition", with a
   position) shows its reason there (`-->` gives the position). Every other code keeps the
   `error[CODE]:` head, so all S15 goldens stay byte-identical (no golden has 1020/0006;
   translate golden replays pass). BOB-E-0006 from the evaluator keeps its dependency chain as
   the excerpt (`//apps/app:app → //libs/restricted:foo`, R-PLAT-006). Tests:
   translate/definition_line_test.go, ir/evalout_chain_test.go.
5. BOB-E-3001 from the fetcher: excerpt `url: / bob.lock: / downloaded:` (layout of fault
   16-wrong-hash), cause "the downloaded file has a different hash than bob.lock pins".
   Test: fetch/mismatch_test.go.
6. -tags bobtest: unpinned plain http to loopback (127.0.0.0/8, ::1, localhost) is allowed for
   requested URLs; redirects keep the strict rule; release builds refuse (BOB-E-3002).
   Tests: fetch/loopback_release_test.go (!bobtest), fetch/loopback_bobtest_test.go (bobtest).
7. §03.1.1: NewBinary / NewTest (types Binary / Test) in spec/03-ecosystems.md and SPEC.md
   (identical); five fixtures renamed. Test: pkg/cc/constructors_test.go (fixture and SPEC
   calls name declared pkg/cc functions; both SPEC copies of §03.1.1 equal).

## DECISIONS
- D-P1-1: the `✗ <message>` head is limited to 1020 and 0006 (the codes R-CC-002a and
  R-PLAT-006 write that way and that no S15 golden pins); SPEC's other `✗ …` texts with codes
  in the goldens (1001–1010, 2xxx …) keep `error[CODE]:`.
- D-P1-2: the loopback allowance applies to requested URLs only, not redirect targets.

## TEST PROBLEM (needs owner OK, protected)
1. tests/platforms/platform_test.go:205 (`restrictedAppGo`, R-PLAT-006) calls `cc.Binary(p, …)`:
   must be `cc.NewBinary` like the five fixtures (A108); the overlay does not compile otherwise.
2. Under -tags bobtest, the R-NIX-014 checks that plain http to a loopback httptest server is
   refused (tests/nix/fetch_test.go TestRNIX014HTTPAndSizeCap; pkg/engine/fetch TestFileFlow)
   fail by design of A108. tests/nix must run without bobtest, while tests/eco/cc needs a bob
   built with bobtest (the harness builds with the caller's GOFLAGS). tools/ci today adds
   -tags=bobtest only for the errors stage; the eco stage needs it too (coordinator).

## verification
go test green: pkg/cli, pkg/engine/{translate (incl. S15 golden replays), ir, drv, fetch},
pkg/engine/fetch with -tags bobtest except TestFileFlow (TEST PROBLEM 2), pkg/bob/...,
pkg/cc/..., pkg (layering), pkg/engine/eval TestFrameworkZip; pkg/engine/build native/lock
tests green. pkg/engine/build TestRunDrvCheckGen failed before reaching Check/Gen: "evaluating
bob.go took longer than 120s" (cold evaluator build, host load ~40 on 8 cores); not re-run.
tests/nix TestRNIX006 (Parse identity on goldens) not run: compile did not finish in 10 min
under that load. gofmt/vet clean for the touched packages.
