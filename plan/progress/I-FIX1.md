# I-FIX1 progress

Scope: F1, F2, F4, F8, F9, F10 (security/hostile input) + F19/F19b (docs/review/m2-m4-reviewer.md).

## done
- F1: component-wise symlink resolution against the unpack root (`linkInside`, ≤ 40 hops, fails
  at the first step outside the root, absolute targets, loops). Fetcher: checked after the
  validation pass (links may go through later entries), nothing extracted on refusal.
  Builder: checked after the walk (job fails, Nix discards output). srcs: same walk on the real
  filesystem (Lstat/Readlink), BOB-E-1025 symlink. Tests: fetch/symlink_test.go,
  builder/unpack_symlink_test.go, srcs/symlink_test.go.
- F4: fetch client CheckRedirect: https only, http only when pinned (context flag), ≤ 10
  redirects, refusal not retried → 3002 naming the redirect URL. Test: fetch/redirect_test.go.
- F2: eval/hostile.go: module path grammar (dotted lower-case first element, safe elements),
  version grammar (semver incl. pseudo/+incompatible), go-line grammar, main module path;
  checked in resolveDeps before any path is formed; `Request.Untrusted` (worker) refuses
  replace targets outside the repo (lexically and after EvalSymlinks); generate refuses any
  name not clean below .bob/eval before writing; `.bob`/.bob/eval symlink refused
  (checkGenDir). Errors are BOB-E-1020 definition errors. Test: eval/hostile_test.go.
- F8: readCapped (O_NOFOLLOW, regular only): bob.go/go.mod 1 MiB, go.sum 16 MiB; ≤ 10,000
  bob.go files per repo, depth ≤ 128 (scan and dependency repos). Test: TestScanCaps.
- F9: addReplaceKeys (eval/replacekey.go) adds go.mod + bob.go of each directory replace
  target to the key/index (`../replace/<module>/<path>`). SPEC R-FW-043.1 amended in
  spec/01-core.md and SPEC.md; A90 in plan/decisions.md. Test: TestEvalKeyCoversReplacedRepos.
- F10: Step.File accepts only a declared output (clean, exact); otherwise misuse error at
  evaluation. Test: pkg/bob/stepfile_test.go. framework.zip regenerated.
- F19/F19b (A94): store.Enter as host root also maps 65534; evalbox drops to nobody when uid 0
  with 65534 mapped, refuses when uid 0 = parent root without 65534. Tests:
  evalbox/nobody_test.go. tests/sandbox green; tests/nix R-NIX-019 CLI tests now get past the
  sandbox (no 9005); they fail only on missing L2 APIs (cc.Library, rust.CLib ...).
- go generate ./pkg/engine/store and ./pkg/engine/eval run (re-run after I-FIX2 edits);
  R-NIX-015 green.

## DECISIONS
- D-FIX1-1: replace targets outside the repo stay allowed locally (sibling checkouts; existing
  TestDependencyBobRepo uses one) and are refused only with Request.Untrusted (workers must set
  it, M13). Inside the repo always allowed.
- D-FIX1-2: caps 1 MiB bob.go/go.mod, 16 MiB go.sum, 10,000 bob.go, depth 128 → BOB-E-1020.
- D-FIX1-3: Step.File(p) requires p == path.Clean(one declared output); sub-paths of an output
  directory are not accepted (SPEC: "one declared output").

## NEEDS
- M13 worker: set eval.Request.Untrusted = true.
- pkg/engine/build (planFile) could also reject bob.file paths not in the step's outputs
  (defence in depth for hostile IR); it already confines with Clean("/"+p).

## TEST PROBLEM
- none. Observed (not mine): pkg/engine/store TestConcurrentOpenImport fails with and without
  my ns.go change (verified via overlay) — likely I-FIX2's streaming import in progress;
  srcs TestImportManyTiming over budget at load ~19.
