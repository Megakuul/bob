# I-R2: reviewer report for M5-M18 (server security focus)

Role: reviewer (AGENTS.md §7), fresh context, read-only on code and tests. Date: 2026-10-24.
Scope given: pkg/{golang,cc,rust,npm,ocaml,proto,connect,graphql,openapi}, pkg/output/*,
pkg/release, pkg/cli, internal/{promote,registry,cache,release,build,orchestrator,worker,fleet,
fleetsrv,api,auth,rbac,forge,notify,ui}, cmd/bob-server, cmd/bob-builder, against SPEC.md and
spec/ chapters, with the emphasis on security.

Method: read the security-critical paths (promotion, cache and registry handlers, release
publication, API guard and rule kinds, job download tokens, checkout credentials, worker source
preparation, forge and notify HTTP clients, the worker-compromise runbook). Findings marked
**verified** were confirmed with throw-away tests in a private copy (`/var/tmp/I-R2/repo`, real
RustFS from the harness tool cache, deleted afterwards). Findings marked "by inspection" were
not executed. No repository file was written except this report and plan/progress/I-R2.md.

**Coverage limit:** ~150k lines are in scope. The ecosystem packages (pkg/golang, cc, rust, npm,
ocaml, proto, connect, graphql, openapi), pkg/output/*, pkg/cli, internal/ui and cmd/bob-builder
were **not** reviewed in depth. A second pass on them is needed (see "Not covered").

## Findings, ranked

### F1 (critical, security, verified). A compromised worker can publish any not-yet-served store path; a later release of another project then serves those bytes publicly

- **Where:** `internal/promote/promote.go` `ParseComplete` / `claims` / `promotePath` (paths are
  checked only for syntax, never tied to the job's derivations), `alreadyServed` (`:900-935`),
  `internal/release/publish.go` `closure` (`:398-440`) and `applyVisibility` (`:647-665`).
- **SPEC / docs:** §6.6.4, R-REG-002, R-REG-152, R-SEC-101; `docs/operations/worker-compromise.md`
  §1 and §5.5 say "the risk is limited to outputs of jobs the worker owned".
- **Failure scenario:** a worker host holding any job (any project A) stages a path whose name is
  the input-addressed output path of a package of project B (predictable: workers can read
  every source snapshot in `bob-artifacts`), with bytes the attacker picked. Promotion checks NAR
  structure, hash and references, and nothing else, so it publishes the path with visibility {A}.
  When B's honest worker later builds the same path, step 3a sees a different NarHash, records
  `non_reproducible` and keeps the first publication. B never gets visibility, so B's cache
  lookups of that path return 404 for good (a lasting denial of service against B). Then B
  publishes a release: `closure()` walks the served narinfos **without any visibility check**,
  includes the attacker's path, and `applyVisibility` marks it public. Everyone who installs B's
  release receives the attacker's bytes, signed by the orchestrator. Revoking the worker and
  rotating the S3 credential does not undo this, and the runbook audit (step 3 uses
  `package_run` in the worker-writable analytics bucket) can miss it.
  Throw-away test `TestIR2CrossProjectPoison`: j0 (project p2) published path X with "evil";
  j1 (p1) staged X with "good" → `pm=done nr=[X]`; p1's narinfo GET for X → 404; X's narinfo
  stays served for the release closure walk.
- **Fix (proposed):**
  1. Release closure: only walk paths whose visibility document contains the release's project
     (as `readManifest` already does for the manifest). Fail the release on a `non_reproducible`
     path in its closure instead of publishing the first-published bytes.
  2. Promotion: accept only paths that the build's evaluation names. Requiring every staged path
     to be an `Outputs` entry of the signed `eval.json` (or the closure of those outputs) narrows
     this to the evaluating worker. Removing the self-attestation of `evalbuild` jobs would need
     an owner decision (for example eval on one worker, build on another).
  3. Fix the runbook: the risk is not limited to the jobs a worker owned. Audit from the
     control-bucket promotion log (`store.PromoLog`, written by orchestrators) rather than
     `package_run`.

### F2 (high, security, by inspection). Checkout credentials and job download tokens are minted from a record the worker can write

- **Where:** `internal/orchestrator/checkout.go:160-173` (`held`), used by `Lease` (`:188`) and
  `Checkouts.Get`; `internal/fleetsrv/service.go:283-312`.
- **SPEC / docs:** R-FLEET-006, §05.9.1, ADR 0013 T10 (workers can overwrite job records);
  worker-compromise.md §1 ("a checkout credential is issued only for a job the worker holds").
- **Failure scenario:** `held` accepts any `job/<id>` whose `Owner`, `Epoch` and `State` match the
  caller. Workers have `PutObject` on `job/*`. It also ignores the `DecodeSpec` result
  (`spec, _ :=`) and never checks `SpecSig`. A compromised worker can either rewrite an existing
  job record of another project to name itself as owner, or write a new record with a made-up
  spec (`Project` = victim, `Repo` = any linked repository). `GetCheckoutCredential` then
  returns a read token for that repository and a download token for the victim project's private
  cache paths. The promoter (`promote.job`) does check `SpecSig`; this path does not.
- **Fix:** in `held`, check `DecodeSpec` ok and `fleet.VerifySpec(fleetKeys, Spec, SpecSig)`, and
  require the spec's `JobID` to equal the record id. Signing alone does not stop someone
  rewriting the owner of a real job, so ownership should be confirmed from state workers cannot
  write. Options: the orchestrator records the claim it accepted over Attach in the control
  bucket, or it checks the queue claim protocol. Then mint only for that. Add a regression test:
  a worker-forged owner gets `permission_denied`.

### F3 (medium, security, verified). A149's closure check can be bypassed through the unverified `skipped` list

- **Where:** `internal/promote/promote.go` `checkClaim` (`case !r.inJob[h] && !r.skipped[h]`),
  `promotePath` (claimed references in `skipped` are accepted without any check).
- **SPEC:** §6.6.4 step 3b (A149: "a hash outside it (e.g. a served path of another job) fails"),
  A150 (closure = paths ∪ skipped), R-REG-005.
- **Failure scenario:** `skipped` is whatever the worker writes; the orchestrator never checks
  that those paths are upstream (R-REG-005 forbids it any outbound client). If a worker adds the
  foreign hash to `skipped`, the A149 rule passes. Test `TestIR2KindsSkippedBypass`: an
  `oci.image` naming another project's served path, with that path listed in `skipped` →
  `pm=done`. The `kinds` claim itself is checked only against the store name and NAR layout,
  and the worker controls both because paths are not bound to derivations (F1). So A149's
  stated rationale ("a worker cannot smuggle foreign paths") does not hold.
- **Fix:** a `skipped` entry whose narinfo is served by bob is not upstream. Refuse it (or treat
  it as foreign) when bob serves it to another project. Correct the A149 rationale in
  plan/decisions.md. Binding paths to the evaluation (F1 fix 2) closes the rest.

### F4 (medium, security, verified). The strict reference scan stops looking at unknown hashes after 256

- **Where:** `internal/promote/verify.go:65,96` (`maxExtra = 256`; later `/nix/store/<hash>`
  occurrences are dropped silently).
- **SPEC:** §6.6.4 step 3b, R-REG-152 (4).
- **Failure scenario:** a NAR with 300 junk `/nix/store/<random>` strings followed by a reference
  to a served path, with a claim that lists no references → `pm=done` (test
  `TestIR2ExtraCapBypass`). The served narinfo then hides a real runtime reference, which is
  exactly what R-REG-152 (4) forbids.
- **Fix:** when the cap is reached, fail verification ("too many unknown store references")
  instead of ignoring the rest; or check candidates against served narinfos in batches with no
  silent cap. Add a test.

### F5 (medium, authz, by inspection). Release closure and public visibility are not limited to the project's own paths

- **Where:** `internal/release/publish.go` `create` (roots = manifest `artifacts[].storePath`),
  `closure`, `applyVisibility`; also used by `UpdateRelease` → public.
- **SPEC:** §6.8, D58 (visibility per object), R-REG-150.
- **Failure scenario:** the paths a release makes public are the transitive references of
  whatever narinfos it reaches, plus whatever store paths the manifest names. Neither is checked
  against the visibility document. A claimed reference, or a manifest entry that names another
  project's private path, makes that path public when the release is public. This is the same
  missing check as F1 fix 1.
- **Fix:** as F1 fix 1: refuse (`failf`) any closure path not visible to the release's project.

### F6 (low, docs drift). worker-compromise.md overstates containment

- Besides F1 and F2: §1 says checkout credentials are only for held jobs (F2); §5.5 says
  poisoning is limited to owned jobs (F1); §5.3 and §5.4 rely on `package_run` / `job_run` /
  `log_line`, which the compromised credential can rewrite (D38). Point the audit at the
  promotion log in the control bucket and at the orchestrator logs.

### F7 (low, secret handling, by inspection). The checkout token appears on the git command line

- **Where:** `internal/worker/jobio.go:213-215` (`git -c http.extraHeader=Authorization: Basic …`).
- Any local user or process on the worker host can read the token from `/proc/<pid>/cmdline`
  while the fetch runs. Errors are redacted, the command line is not.
- **Fix:** pass it through `GIT_CONFIG_COUNT/KEY/VALUE` environment variables (readable only by
  the same uid) or a one-shot credential helper on a file descriptor.

### F8 (low, SSRF posture, by inspection)

- Outgoing notify webhooks and forge base URLs need https and no userinfo
  (`internal/notify/service.go:46`, `internal/forge/check.go:24`), and notify follows no
  redirects. Neither blocks private or loopback addresses. Both are configured through
  admin/global permissions, so the risk is low. Consider a dialer that refuses link-local,
  loopback and private ranges unless an admin allows a host (in the spirit of
  R-FLEET-008's "no IMDS or loopback").

## Checked and found sound (no finding)

- API guard (`internal/api/interceptor.go`): every procedure must have a rule; GLOBAL and
  PROJECT rules are enforced centrally; download-scope tokens are refused outside
  `allow_download_scope` methods; cookie sessions need `Bob-Csrf` and no GET. PUBLIC,
  AUTHENTICATED and LIST_FILTERED handlers I read filter correctly: `CacheService.GetPath`,
  `AnalyticsService.GetOverview` / `RecentFailures`, `ReleaseService.Get/List/ResolveRelease`.
- Binary cache `/nix/` (`internal/cache/handler.go`): visibility from the per-object document;
  job download tokens are confined to `CacheProject`; invalid credential → 401, hidden → 404.
- OCI registry (`internal/registry`): HMAC tokens checked in constant time with key expiry;
  per-tag visibility (A151); digest access only through visibility documents; pull-only.
- Job download token (`internal/auth/jobtoken.go`, `identity.go`): `job:` owner → principal with
  no bindings and scope `download`; revoke only deletes job tokens. (Minting is affected by F2.)
- Promotion basics: spec and result signatures checked; COMPLETE.json bound by `CompSHA`;
  NAR structure, hash and size recomputed; create-only narinfo; ETag-guarded copies;
  `alreadyServed` adds visibility only after re-verifying identical bytes.
- Worker source snapshot unpack (`internal/worker/jobio.go` `unpackSnapshot`): the symlink check
  refused the chain input tested (`d/up -> ..`), so it is stricter than the M2-M4 F1 fetcher.
  Only one input was tried; the archive regression tests from the M2-M4 review should cover it too.

## Not covered (needs a second reviewer pass)

pkg ecosystems (golang, cc, rust, npm, ocaml, proto, connect, graphql, openapi), pkg/output/*
(appimage runtime, OCI layer generation), pkg/release, pkg/cli (install/profile, dev shell),
internal/ui, internal/rbac escalation rules, internal/auth OIDC, the forge webhook intake
signature paths (only the helpers were read), cmd/bob-builder, the pkg/builder inputs lock,
isolation (`internal/isolation`) and its R-FLEET-008 network rules.

## Proposed fixes, in order

1. F2: verify `SpecSig` in `Checkouts.held` and take ownership from state workers cannot write
   (small change; closes cross-project credential minting).
2. F1/F5: release closure limited to paths visible to the project; fail a release on a
   `non_reproducible` closure path.
3. F4: fail on the unknown-hash cap instead of ignoring the rest.
4. F3: treat a `skipped` path that bob serves to another project as foreign; correct the A149
   rationale.
5. F1 fix 2 (bind promoted paths to the signed evaluation): needs an owner decision on
   evalbuild self-attestation.
6. F6/F7/F8: runbook corrections; pass the git token through the environment; optional dialer
   policy.

## Owner-relevant items

1. **F1** is a supply-chain risk under the worker-compromise threat model that SPEC and the
   runbook say is contained. Closing it fully needs a design decision: promotion is currently
   based only on worker attestation of which paths a job produced.
2. **F2** contradicts R-FLEET-006 as the runbook describes it; it should be fixed before workers
   run untrusted jobs.
3. The review did not cover the ecosystem and output packages (see "Not covered").
