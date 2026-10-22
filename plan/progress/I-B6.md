# I-B6 (Track B: fleet wiring into bob-server + M13.7 worker isolation) — cmd/bob-server (parts.go, parts_fleet.go, store_fsck.go, main.go), internal/fleetsrv (new), internal/isolation (new), internal/worker (process.go, runner.go, attach.go; Outcome.LogLast in loop.go), internal/http (h2c), internal/store/sweep/fsck.go

## Done
- internal/isolation (M13.7): `Check()` = cgroup v2 at /sys/fs/cgroup (statfs magic) with memory/pids/cpu,
  creates bob-worker.slice and enables the controllers; Landlock ABI ≥ 4 (evalbox.LandlockABI); the
  uid/gid range of Nix auto-allocate-uids (872415232 + 128×65536) mapped in the worker's userns.
  Failure = *Refusal → BOB-E-7010 "✗ worker cannot isolate builds: <reason>", exit 7.
  `Run(ctx, Spec)` = job container without an OCI runtime: leaf bob-worker.slice/job-<id> with memory.max,
  pids.max, cpu.max (started into it with CLONE_INTO_CGROUP); new mount/pid/ipc/uts/cgroup ns; tmpfs root
  + pivot_root; store bound rw at /nix; private /tmp; small /dev; generated /etc; fresh /proc; empty
  environment except the job's; cgroup.kill + recursive rmdir afterwards (0 processes left).
  `Child` = the re-exec side (main dispatches `bob-isolation:container`). `NixConf` = SPEC root-mode
  nix.conf. `Evaluate` = evalbox.Run with Strict: true (inside the container; evalbox delegates from the
  job leaf). Tests (root, this host): TestContainer (pid 1, own pid ns, no worker env/files/environ,
  store write lands, cgroup ns root 0::/, hostname, private /tmp, leaf removed, 0 procs),
  TestEvalboxInContainer (strict evalbox works inside, cgroup=true, evaluator not uid 0),
  TestContainerKilledOnCancel, refusal/idmap/nix.conf unit tests.
- internal/fleetsrv: ParamsFromEnv (BOB_FLEET_*, BOB_LOG_*), EnsureKeys (start-up key: okey/<kid> then
  fleet/cfg.k; publishes fleet/cfg.pr), keysOf (signs with the oldest valid key with > 3.5 d left, else
  the longest-lived; I-B8 ruling), Fleet (Registrar, Reconciler, Hub, Checkouts, Enqueue via Dispatcher
  over Hub.Workers(), AddClass), Components = orchestrator.RegistrarController (2 s, rotation 60 s),
  ReconcilerController (10 s), ForgeSweeperController (60 s) (I-B8's lease runner), FleetService
  (ListWorkers over widx only, GetWorker, Drain/Undrain/Revoke, ListOrchestrators from
  Membership.List), WorkerControl (GetCheckoutCredential sealed with fleet.SealCheckout; Attach: Hello →
  Hub.Attach + LiveLogs.Attached, Welcome, wake/cancel/drain lines, LiveChunk → LiveLogs.Chunk, SlotState,
  ping). Test TestFleet (RustFS): concurrent orchestrators share one key, registration → active in
  ListWorkers, drain filter, no checkout without lease.
- internal/worker: Process (worker mode, Critical component): register → wait approval (1 s poll, new
  keys after 5 min) → fleet/cfg keys/classes/params → Worker loop (restarted when idle and classes change),
  NewProcessLogs (lake writer) run in background, token refresh (30 s), one Attach stream per
  BOB_ORCHESTRATOR_ADDRS (or fleet/cfg.m) over h2c with token + PoP, Wake → Worker.Wake, LiveStream per
  stream, SlotState every 5 s. exec = job container running `/bin/bob job-run`; stdout is NDJSON events
  (lines, package ends, outcome) fed into JobLog; End() → Outcome.LogLast → JobResult.LogLast (2-line change
  in loop.go). Runner interface (Run(ctx, Job, Output) Outcome) + NotBuilt stub = BOB-E-9008 until
  M16/M17; EvalRequest sets eval.Request.Untrusted (coordinator, A98). Tests: TestExecRunsJobInContainer
  (root), TestJobEventsRoundTrip, TestRunJobNotBuilt, TestEvalRequestIsUntrusted.
- cmd/bob-server: parts.go small (rbac, auth, controllers → cs, logs → live, fleetParts(cs, live));
  parts_fleet.go; main.go dispatches the container child and hidden `job-run`, `store fsck`
  (store_fsck.go: LIST via internal/store/sweep Keys with UA bob-sweep/fsck, orchestrator.Fsck, exit 1 on
  problems); orchestratorParts may fail (key I/O).
- internal/http: server speaks HTTP/1.1 + HTTP/2 + h2c (bidi Attach over http://).
- Manual on devstack: 2 workers → attach proxy `/streams` {"orchestrator-0":2}; ListOrchestrators lists
  the member with its leases; `store fsck` → "29 objects checked, 0 problems", exit 0.
- Measured (this host, devstack, 1 orchestrator, load from other agents): 20 sequential extra workers
  start → ACTIVE in ListWorkers: 1.97–2.19 s, p99 2.19 s (budget 5 s), via /api/ path.

## Tests
- Green: TestRFLEET008WorkerRefusesNonRoot, TestRFLEET008WorkerRefusesWithoutCgroupV2,
  TestRSTORE102RefusesBackendThatIgnoresIfNoneMatch (worker half now passes), all library fleet tests as
  before (full tests/store/fleet run: same 5 non-green as before this unit).
- Red (harness.Red): TestRFLEET008BuildsAreIsolated (`bob test --remote` 9008), TestRFLEET013 (`bob build
  --remote` 9008): need remote builds + the job runner (M16/M17).
- Failing, TEST PROBLEM: TestRFLEET001ZeroSetupRegistration (see below).
- tests/chaos and tests/server: srvtest stops at `bob login` (exit 70, plan/test-corrections.md 12c) and
  posts without /api/ (12b); build submission (M16/M17) needed beyond that.

## DECISIONS
- D-IB6-1 New package internal/fleetsrv for the orchestrator fleet side (keys, controllers wiring,
  FleetService, WorkerControl) instead of putting services into internal/orchestrator (I-B2) or
  internal/server (whose doc keeps it free of controllers).
- D-IB6-2 The job container is a re-exec of bob-server dispatched in main (`bob-isolation:container`),
  so the setup can exit via main's os.Exit (noexit lint; no new syscall.Exit hook file). On success it
  execve's the job program as pid 1 of the container.
- D-IB6-3 Host check for "Nix auto-allocate-uids support" = the embedded Nix pins 2.34.9 (supports it)
  plus the runtime condition that can fail: the auto-allocated uid range must be mapped in the worker's
  user namespace. cgroup check also prepares bob-worker.slice (refuses if controllers cannot be enabled).
- D-IB6-4 Persistent store at <BOB_STATE_DIR or /var/lib/bob>/worker/nix (the test expects
  BOB_STATE_DIR/worker/nix); root-mode nix.conf at <state>/worker/etc/nix.conf, bound read-only.
- D-IB6-5 Slot limits: memory.max = MemTotal / slots, cpu.max = max(1, nproc / slots) CPUs,
  pids.max = 16384.
- D-IB6-6 The worker process loop is Critical: only revocation ends it (the process exits; a new process
  registers new keys); transient S3 errors are retried inside. Classes: the Worker has a fixed class set,
  so the loop restarts the Worker when fleet/cfg.c changed and all slots are idle.
- D-IB6-7 Runner stub outcome is status "failed" (not "infra": a not-implemented runner is not transient).
- D-IB6-8 ListWorkers reads widx only (SPEC §05.5 "Fleet: workers = GC widx/*"); an entry whose
  heartbeat (as summarised in widx) is older than fleet.gone_after shows as stale.
- D-IB6-9 fleet/cfg.pr is published once by the first orchestrator (from its env); workers merge
  fleet/cfg.pr over their env/defaults.
- D-IB6-10 Registrar, reconciler and forge-sweeper run under I-B8's orchestrator.Controller leases; the
  hourly key rotation is I-B8's Keys controller; fleetsrv.EnsureKeys covers start-up only.

## NEEDS
- Test owner (TEST PROBLEM 1): tests/store/env/api.go RPC posts to `<api_url>/<method>`, missing the
  `/api/` prefix of SPEC §6.2 (same bug as srvtest, test-corrections 12b) → every call answers 501 and
  TestRFLEET001ZeroSetupRegistration can never see an active worker.
- pkg/engine/eval (owner of eval): an `eval.Request.Strict` (→ evalbox.Spec.Strict) pass-through, and an
  evalbox option to map the evaluator to `fleet.eval_uid_base + slot` (default 2,000,000,000 + slot)
  instead of nobody (SPEC R-FLEET-008 "Evaluation"); Job.Slot carries the slot number for it.
- Build integration (M16/M17): a real Runner (eval via isolation.Evaluate + worker.EvalRequest, drv
  generation, Nix builds inside the container with the root-mode nix.conf, staging upload — the upload
  needs worker credentials, so it must happen outside the container, by the worker, from the store);
  build controller calls Fleet.AddClass + Fleet.Enqueue; checkout via GetCheckoutCredential; forge
  minter (Config.Forge) from M17.1. Nix use-cgroups inside the job leaf is unverified until real builds.
- internal/worker loop.go (I-B2): a drain state (Drain over Attach / worker record draining is not acted
  on) and a cancel hook (CancelJob over Attach; today only the 30 s job watch sees the cancel flag).
- Long-running orchestrators keep the signing key picked at start; after the key controller rotates, a
  restart picks the new one. A reload (Registrar/Queue key swap) belongs with the key controller.
- Worker TLS pinning of fleet/cfg.m SPKI (R-SRV-102 wrong-key case) is not implemented: https uses the
  system roots; devstack uses http.
- widx heartbeat summaries ("registrar … widx summaries", §05.11) are not written by anyone yet.

## TEST PROBLEM
1. tests/store/fleet/e2e_test.go TestRFLEET001ZeroSetupRegistration uses tests/store/env.RPC, which builds
   `x.APIURL+"/"+method` without `/api/`; the server's §6.2 mount answers 501 there
   (curl `<api>/bob.fleet.v1.FleetService/ListWorkers` → 501; `<api>/api/...` → workers listed). The first
   env.Wait ignores its timeout, so the failure shows as "worker 0 did not become active within 30 s".
   Behaviour measured with the /api/ path: p99 2.19 s over 20 workers, 0 LIST (proxy not checked by hand).
