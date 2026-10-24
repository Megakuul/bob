# Gate H test review, unit GH-D

Scope: R-STORE-*, R-FLEET-*, R-SEC-*, R-LOG-*, R-SRV-*, R-NFR-*, R-API-*, R-AUTH-*, R-REG-*, R-PIPE-*, R-UI-*
(tests/store, policy, server, chaos, soak, rbac, auth, cache, oci, forge, ui). The reviewer did not write the tests.
Each finding gives the id, file and function (line numbers are approximate), and the wrong implementation that would still pass
(or the false red, or the missed bullet). F-numbers are stable.

## Edits made (test-side, minimal)
1. tests/ui/browser_test.go (newTab, storage boot script, ~line 245): clear `localStorage["bob.theme"]` only on the tab's first
   document (sessionStorage guard). Before: the script ran on every new document, so the Reload in TestThemeDefault erased the
   persisted Dark choice and no correct implementation could pass R-UI-004 (F52). `go vet ./tests/ui/...` is clean.

No other file was edited. A second edit (tests/store/keys_test.go, Sh2 handling, F9) was refused by the permission layer as
test loosening; it stays a finding for the owner. Nothing was committed.

## HIGH

- F52 (FIXED) R-UI-004, tests/ui/browser_test.go newTab: see Edits. A correct implementation could not pass.
- F23 R-LOG-002, tests/server/log_live_test.go TestRLOG002LivePull (stall check, ~l.165/178): `recovered` is the time from the cut to
  the FIRST later message that has lines or `Delayed`; an in-flight chunk or the next 250 ms chunk satisfies it. A server that
  stalls the follower 10 s passes whenever any message arrives within 3 s of `blockedAt`. It must measure the maximum
  inter-message gap after the cut.
- F24 R-LOG-102, tests/server/log_hostile_test.go TestRLOG102HostileOutput: SPEC names oversize chunk, flood, cross-job chunk and
  stale epoch (S17's 14 security cases). The test only pushes hostile build OUTPUT through a legitimate worker. No forged
  LiveChunk (cross-job, stale epoch, oversize, more than 1 MiB/s) and no check of the log text
  `invalid_argument: log line invalid: <package>/<seq>`. An orchestrator that accepts any chunk from any caller passes
  (declared gap: needs an Attach client).
- F10 (SPEC problem) R-FLEET-005, tests/store/fleet/planner_test.go TestRFLEET005Planner (~l.34): the test demands go200 at W=100
  splits, but the stated rule (S14 5.1 line 232, R-FLEET-005 statement) is `T1-Tsplit >= max(60 s, 0.3*T1)`; go200's gain is
  30.0-16.4 = 13.6 s, so a planner implementing the stated rule FAILS. There is also no boundary test of the 60 s / 30 %
  thresholds, and the 4-worker real-run bullet is absent. Owner must reconcile SPEC.

## MED

- F1 R-STORE-004, tests/store/stack_test.go TestRSTORE004ReadPlan: rows of the 05.5.2 table that are not tested: Builds of a
  branch, Build by number, Release by version, Users of a role, Auth of an API call (cache miss). SPEC: "for each row".
- F2 R-STORE-105, tests/store/retention_test.go TestRSTORE105StagingCleanup (~l.97): it waits for ALL staging except the planted
  dead-build to vanish, including the staging of the killed worker's failed attempt. SPEC: failed attempts expire by the 7 d
  lifecycle or sweep; only successful attempts must be 0 -> a correct implementation can fail. Scale also reduced (acme build,
  1 of 3 workers killed instead of 300 jobs with 20 % killed).
- F3 R-STORE-150, tests/store/retention_test.go TestRSTORE150: "deletes the unreleased build's paths" is asserted as
  `gone(before) > 0`; an implementation that deletes a single path passes.
- F11 R-FLEET-003, tests/store/fleet/lease_test.go chaos (~l.437): requeue bound = ttl+interval+skew+3 s; SPEC is
  lease_ttl + reconcile interval + skew. The test measures the claim by another worker and adds 3 s (60 % over a 5 s base).
- F12 R-FLEET-002, tests/store/fleet/identity_test.go TestRFLEET002AttachIsolation: worker A never attempts Subscribe or
  CancelJob on B's job (only hub-side delivery and A's JobEvent/LiveChunk). An Attach that lets A subscribe or cancel through its
  own conn passes. S11's "31 checks" are ported only partly (about 19 behaviours; policy denials live in tests/policy).
- F13 R-FLEET-008 / R-SEC-000, tests/store/fleet/e2e_test.go TestRFLEET008BuildsAreIsolated: (a) the "S13 hostile suite (15
  evaluator cases)" bullet is not in this test; (b) the "hostile package ran" check is `!Contains(name) && !Contains("hostile")`,
  satisfied by any output mentioning "hostile"; (c) the cgroup "no processes remain" loop is vacuous when the job cgroup is
  gone, and nothing proves a job-<id> cgroup ever existed (memory.max, pids.max, namespaces untested).
- F14 R-FLEET-013, tests/store/fleet/e2e_test.go TestRFLEET013: "at the same time" is not asserted (two sequential jobs pass);
  "built once" is a package_run count over all projects.
- F22 R-LOG-100, tests/soak/lake_test.go TestRLOG100LakeWorkarounds: never asserts that compaction ran, nor the rules (older than
  10 min, written within 2 d, retry on NoSuchKey). A server with a lease holder and no compaction at all passes.
- F28 R-SRV-100, tests/server/fleet_test.go TestRSRV100: the "checkout job fails with an infrastructure error after 3 attempts"
  bullet is absent (needs forge in the stack); the test does not assert that the Attach block took effect (`AttachStreams()==0`),
  so a non-blocking proxy makes it trivially pass.
- F30 R-SRV-004 / R-NFR-007, tests/chaos/chaos_test.go: jobsHaveLeases runs after settle, when no job is claimed or running, so
  "no job runs without a lease" reduces to "a succeeded job has an owner and claimed_at"; double runs are invisible.
  TestRNFR007CrashSafety never asserts "0 stale results accepted"; R-SRV-101 is folded in as random 1 % S3 faults, not writer
  kills between write pairs.
- F31 R-NFR-005, tests/soak/scale_test.go: 1 of 4 limits tested (5,000 projects, 1,000 workers, 1,024 jobs per build missing);
  SPEC wants limit and limit+1 for each (declared infeasible black-box).
- F32 R-SRV-101, tests/soak/intent_test.go TestRSRV101IntentFirst: "every index matches its records after index-repair" is checked
  only through reader APIs that already skip missing records, so a no-op index-repair passes. Dangling entries are never
  inspected raw (bpage/rmem/pidx pages) and removal after 5 min is unasserted. No reverse check (record -> index).
- F33 R-AUTH-006, tests/rbac/matrix_test.go TestRBACMatrix (~l.150-165): for every id-resolving PROJECT method
  (BuildService/*, JobService/*, LogService/*, Release*) the expectation is forced to NotFound for every principal except
  admin/role@*, because no seeded resources are used although authenv Seed exists; per-principal authorization of these
  methods is never distinguished. `Allow` also accepts NotFound/InvalidArgument, so an authorized principal wrongly answered
  NotFound passes in direct rows (the R-AUTH-002 table avoids this with its PackageDurations probe). Worker row is always Refused.
- F37 R-REG-152, tests/cache/promote_test.go TestRREG152NonReproduciblePathKeepsFirstPublication: `first` is read AFTER both builds
  are terminal, so a server that lets the later promotion overwrite the narinfo looks "unchanged". The test never establishes
  which publication was first.
- F38 R-REG-152, tests/cache/promote_test.go TestRREG152TwoOrchestratorsPromoteOneJob: nothing forces two orchestrators to promote
  the SAME job concurrently (no DelayStaging alignment); it scans logs afterwards for `promoted N` and `alreadyThere N` with
  equal N. A correct implementation where only one orchestrator touches the job yields no alreadyThere -> false red; the log text
  format is assumed.
- F43 R-REG-003, tests/oci/oci_test.go TestRREG003PullRegistry: SPEC lists crane, skopeo, podman (rootful and rootless) and
  `skopeo copy` push refused; the test uses rootless podman only (podman push for the push bullet).
- F47 R-PIPE-003, tests/forge/pipelines_live_test.go: the forged-evaluation bullet (`evaluation result rejected: pipeline match
  mismatch`) is not written (needs a worker Attach client).
- F48 R-PIPE-005 (3), tests/forge/pipelines_live_test.go: retry asserts only `Cached >= previous Built+Cached && Built == 0`; it
  never shows the failing package was re-run, so a retry that re-runs nothing passes.
- F50 R-FLEET-006 live bullets, tests/forge/forgejo_live_test.go TestForgejoLive/checkout+sweeper and checkout_test.go: exercised only
  at adapter level (IssueCheckout/RevokeCheckout/SweepCheckouts called directly). The product path "job ends -> orchestrator
  revokes -> token 401 within 30 s" and "orchestrator killed between mint and revoke -> forge-sweeper" never runs against live
  Forgejo; after revoke only "fetch fails" (any error) is asserted, not 401, not the 30 s window.
- F53 R-UI-007, tests/ui/ui_test.go TestFirstLoadBudget: initial JS/CSS is summed from the asset references inside 200.html only
  (shell), not from what the overview actually loads (no resource timing); a tiny shell that lazy-loads a huge overview chunk
  passes. FCP bound is fixed 1.0 s ([8c/15G]); the 2.0 s [2c/4G] row is not selected by host class.
- F54 R-UI-006, tests/ui/ui_test.go checkDOM: link targets are opened for at most 5 distinct hrefs per page (SPEC: every cell with
  class id); the rendered id is compared after TrimRight("…"), so a truncated id matches by prefix only; "reduces rows" accepts
  after <= before.

## LOW

- F4 R-STORE-007, retention_test.go: no assertion of bpage tombstones, qs/wrs slot deletion, or the 400 d analytics drop (declared).
- F5 R-STORE-100, stack_test.go TestRSTORE100NoListOnControlAndFleet: SPEC scope is the whole e2e suite (300-job drain, chaos,
  retention); the test covers registration + list pages (other suites end with AssertNoList).
- F6 R-STORE-101, stack_test.go: 20 s run only; "at most 2 sweep runs per controller" lives only in the slow soak test.
- F7 R-STORE-103, records_test.go TestRSTORE103RecordRules: one synthetic record type, not a table per record type; the
  If-Match of CAS is not asserted (the 8-contender race could pass an unconditional Put by luck); the stored-CBOR short-name check
  inspects the test's own struct tags.
- F8 R-STORE-102, backend_test.go (~l.100): store.Open's probe is verified only by ">= 2 PUTs under _bob/"; checks 3 to 7 are not
  shown to run; "when bob-server starts all checks pass" is not run through the binary on RustFS.
- F9 R-STORE-104, keys_test.go (~l.143): `Sh2` of an invalid id ('/', 1 KiB, junk) must return a valid shard; only "" may error.
  SPEC says ids are ULIDs; an implementation returning ErrKeySegment for invalid ids is compliant but fails (edit refused).
- F15 R-FLEET-009, budget_test.go: `l.view` polling (300 GETs per Wait iteration) is counted by the proxy in the mean, which
  inflates requests/job: false red possible.
- F16 R-FLEET-012, dispatch_test.go: polling-only uses poll=2 s (bound 3.4 s), not 30 s; SecondWakeAfter is set by the test (300/500
  ms, not the 2 s default); the +-20 % poll jitter is not asserted.
- F17 R-FLEET-006, checkout_test.go: the name-timestamp sweep of tokens without a record is not asserted here; the sweeper is
  checked only at +61 s.
- F18 R-FLEET-001, e2e_test.go: the first Wait result is ignored; "holds a valid token" is not asserted.
- F19 R-STORE-002, matrix_test.go (~l.131): DeleteObjects accepts HTTP 200 + per-key AccessDenied where SPEC says every denied
  cell is 403 (S3 semantics; documented, benign).
- F20 R-SEC-100, sec_test.go: source scan only; no assertion that tools/ci/lint rejects sts/iam imports; the RPC test checks only
  this stack's own known secrets.
- F21 R-SEC-102, sec_test.go: checks 2 and 3 (403 on fleet worker/_probe PUT, bucket-policy deny statement) are never singled out
  (SPEC acceptance only names the missing-policy case).
- F25 R-LOG-101, log_merge_test.go (~l.99): `Percentile(empty)=0`, so if no Delayed line ever arrives the degraded-latency check
  passes; "delayed = true while degraded" is not asserted.
- F26 R-LOG-003, log_attrib_test.go: lower-bound checks (`len(ls) < per`, `len(all) < pkgs*per`) let own-package duplicates at the
  tail pass; p99 500 ms is measured on 20-line data, not the 100M-line set (declared SPEC problem).
- F27 R-LOG-001, log_commit_test.go TestRLOG001KillLosesOnlyUncommitted: "loses only lines after the last commit" is not bounded.
- F29 R-SRV-102, fleet_test.go: the wrong-key bullet passes if workers never reconnect (no positive control after the redirect is
  removed).
- F34 R-AUTH-006, rules_test.go (~l.71-79): the UpdateProject/UpdateRelease project_field proto defect is reported with the marker
  `red: not implemented yet:`; it is a proto problem (declared) and could be misclassified as "not implemented".
- F35 R-AUTH-006, rules_test.go: "the interceptor refuses methods without a rule" has no server-side test (descriptor check +
  fixture only).
- F36 R-API-151, auth/api_test.go TestRAPI151ListsAreGetOnly: only List*/GetOverview/RecentFailures are called; the whole-suite LIST
  count is carried by zz_logs_test.go TestRAPI151WholeSuite (works only if the whole package runs).
- F39 R-REG-002, promote_test.go TestRREG002TamperedStagingIsRejected: dependents of the tampered path are computed in one pass
  over the narinfo keys (not a fixpoint).
- F40 R-REG-005, promote_test.go: asserts skipped is non-empty and 404, never that `hello` (the SPEC example) is among them.
- F41 R-REG-150, visibility_test.go: only GET on nix/vis/ is counted (HEAD not); needs >= 10 served paths in the release fixture.
- F42 R-REG-153, rotation_test.go: "narinfo rewrite uses If-Match" is not asserted (declared: the proxy does not record headers).
- F44 (SPEC inconsistency) R-FLEET-006 denial text `worker holds no lease on job <id>` vs 6.9.6 `job not leased by this worker`.
- F45 R-PIPE-007, suites_test.go: checks test-name prefixes and build tags only, not that tools/ci defines the CI jobs
  forge-fixtures, forge-github-replay, forge-forgejo-live, forge-gitlab-live.
- F46 R-PIPE-008, intake_live_test.go: "no git process started" is a post-hoc /proc scan (a short-lived process is missed).
- F49 R-PIPE-004, pipelines_live_test.go: CLI substitution with negative TTL 0 is checked by a direct narinfo GET (admin token).
- F51 (SPEC problem) R-FLEET-006 says the other repo answers 404; S16/HARNESS measured 403 and the test asserts 403.
- F55 R-UI-002, ui_test.go/build_test.go: runs against the harness fixture server (per SPEC "seeded fixture server"); the live-line
  budget is measured once with +250 ms slack, not as p99.

## SPEC problems surfaced (owner decides; tests not edited)
- R-FLEET-005 go200 split vs the split rule (F10).
- R-FLEET-006 404 vs 403 and denial text (F44, F51).
- Bullets that cannot be written without harness features (Attach client, forge in the stack, raw graph submit, 100M-line data
  set, 1,000 workers/5,000 projects): R-LOG-102, R-LOG-003, R-SRV-100, R-NFR-005, R-FLEET-005, R-PIPE-003.
- tests/rbac proto defect (UpdateProject/UpdateRelease project_field) (F34).

## IDs reviewed with no finding above LOW
R-STORE-005, R-STORE-106, R-STORE-002, R-STORE-104 (F9 only), R-STORE-102 (F8 only), R-SEC-002, R-SEC-100, R-SEC-102,
R-AUTH-001, R-AUTH-002, R-AUTH-003, R-AUTH-004, R-AUTH-005, R-AUTH-007, R-AUTH-150, R-AUTH-151, R-AUTH-152, R-API-001, R-API-002,
R-API-003, R-API-004, R-API-150, R-API-151, R-REG-001, R-REG-005, R-REG-151, R-REG-153, R-REG-154, R-PIPE-001, R-PIPE-002,
R-PIPE-004, R-PIPE-006, R-PIPE-007, R-PIPE-008, R-PIPE-009, R-FLEET-001, R-FLEET-004, R-FLEET-009, R-FLEET-010, R-FLEET-011,
R-FLEET-012, R-UI-001, R-UI-004, R-UI-005, R-LOG-001, R-NFR-007 (F30 MED listed under R-SRV-004).
Manual / out of scope: R-SEC-101, R-UI-003 (manual); R-NFR-150 (budget file only; tests/budgets is not in this unit).
The independent reference models (policy matrix, RBAC evaluator, key encodings, the 6.5.19 summary table) were checked against the
SPEC text and are not derived from the code under test.

Checkpoint: plan/progress/GH-D.md.
