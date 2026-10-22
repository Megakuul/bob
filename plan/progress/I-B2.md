# I-B2 (Track B: M13.4 → M13.5 → M13.8 → M13.6) — internal/worker, internal/orchestrator, internal/fleet

## Done
- Read AGENTS, impl-brief, HARNESS, SPEC §05, I-B1 progress, tests/store/fleet, tests/chaos, S11/S14 spikes.
- internal/fleet (new): wire formats + crypto shared by worker and orchestrator (S11 §5.2/§5.3):
  worker id, RegBody + validation, HPKE (crypto/hpke MLKEM768-X25519) sealed tokens, bwt1 tokens,
  PoP, signed heartbeats, signed JobSpecs/JobResults, SealCheckout/OpenCheckout (for I-B3's
  GetCheckoutCredential), DecodeSpec/DecodeResult.
- M13.4 internal/worker/identity.go (Identity, Register, FetchSealed, Token, PoP, Heartbeat, forgery
  helpers) + internal/orchestrator/registrar.go (Registrar: Pass with ctl/registrar.cu cursor,
  ≤ MaxPerPass approvals, Authenticate with 10 s record cache and PoP replay cache, Rotate,
  ForceRotate, Revoke). Green: TestRFLEET002Registration, TokenLifecycle, ConcurrentRegistrars,
  RegistrationBacklog, ForgedResultsAndSpecs, AttachIsolation.
- M13.5 internal/orchestrator queue.go (Queue: Enqueue job-first/slot-first, Job, SlotTaken, Cancel,
  Finalise with forged-result detection + metric), reconcile.go (Reconciler), dispatch.go
  (Dispatcher), hub.go (Hub/Conn); internal/worker/loop.go (Worker). Green: R-FLEET-011 (3),
  R-FLEET-004 (2), R-FLEET-003 LeaseAndRequeue/ClaimGrace/Cancel/Chaos, R-FLEET-012 (5),
  R-FLEET-009.
- M13.8 internal/orchestrator/checkout.go (Checkouts: Get, JobEnded, Sweep; ftok/<sh2> records hold
  forge handle + expiry only). Green: both R-FLEET-006 tests.
- M13.6 planner.go (S14 §5.1 formula, G2 rule, ready-set waves). Green: TestRFLEET005PlannerBoundaries.

- Full tests/store/fleet run (host load ~19 on 8 cores): all library tests green except
  TestRFLEET003Reslot and TestRFLEET005Planner (TEST PROBLEM 1, 2) and TestRFLEET012WakeLatency
  under load (p99 1.1–2.0 s at load 15–19; 77 s run, green, at lower load). Process-level tests
  (R-FLEET-001 e2e, R-FLEET-008 ×3, R-FLEET-013) need cmd/bob-server wiring (I-B3, NEEDS).
  TestRFLEET003Chaos green 3 runs in a row (both modes). TestRFLEET010IdleRequestMix and
  TestRFLEET009 green.
- CI: `go run ./tools/ci -only fmt,vet,lint,layering`: fmt, lint, layering ok; vet fails only on
  `out/uicheck` (another unit's package outside the R-ARCH-001 roots). boblint on my packages clean.

## Next
- Hand back. M13.7 (worker isolation) not in this unit's chain.

## DECISIONS
- D-IB2-1 internal/fleet holds every signed format; LV = 4-byte big-endian length (S11 §5.2).
  PoP header = "<unix ts>.<b64url nonce>.<b64url sig>", sig over ("bob-pop-v1", method, path,
  ts, nonce, sha256(token)); the Go API (Token.PoP) has no authority/body, so they are not in
  the message (S11 proposed them; replay cache is per orchestrator, 120 s).
- D-IB2-2 Heartbeat signature = Ed25519("bob-whb-v1"‖0‖LV(wid)‖LV(at unix ns)‖{LV(job)‖LV(epoch)}…).
  The reconciler verifies it only when it knows the worker's key (worker/<wid> exists).
- D-IB2-3 Queue.Enqueue stores a canonical JobSpec built from Job.Spec plus the Job's fields
  (job, build, class, repo, prefer, created). A Spec that is not a CBOR JobSpec v1 (the
  in-process tests pass opaque bytes) is replaced by one built from the Job's fields: the repo
  must live in the signed spec for GetCheckoutCredential, and JobRecord has no other field.
- D-IB2-4 Wake-up path claims straight from the job id in the Wake (CAS job, CAS slot tk, start,
  result = 8 requests); the worker does not GET the slot first (the herd test caps one wake at 8).
  A slot tk may go "stale" → claimer id (a peer saw the job claimed before the claimer wrote tk).
- D-IB2-5 "Poll again at once after finishing a job" applies to jobs claimed by a poll (the queue
  had a backlog); a worker fed by wake-ups does not poll after each job, otherwise every job pays
  a walk over slots its woken peers just took (R-FLEET-009 budget). Pending wake-ups that arrived
  while all slots were busy are processed when a slot frees. The first poll comes one jittered
  interval after start (the selection test's worker with a 1 h poll must claim only on wake).
- D-IB2-6 Poll = GET slot[cursor] (404 = idle, 1 request). With a backlog: GET qh when the cursor
  slot is closed (jump to head), GET qt + one random slot in (cursor, tail) (spread), then walk
  from the cursor (≤ 1000 slots), then AdvanceHead to the walked cursor. Wake-ups never move
  the cursor, so the head only passes slots verified closed.
- D-IB2-7 Reconciler: heartbeat cache per owner ("alive" from the cache is final, "expired" is
  confirmed by a fresh GET whb); jobs read with GetCached in parallel (16); jobs seen terminal are
  not re-read for 60 s; known held jobs are checked at the start and the end of each pass (the
  chaos bound lease_ttl + interval + skew leaves no room for the pass duration); the
  ctl/job-reconciler lease (TTL 30 s) is renewed only after a third of its TTL. Finalising
  orphaned terminal jobs needs Reconciler.Queue (optional field, FinaliseAfter 10 min).
- D-IB2-8 Worker results: outcome infra → state failed with JobResult.Status "infra" (the build
  controller decides on retries, §05.6.2). The owner may overwrite a terminal result that its own
  key did not sign (forged by another fleet writer); Finalise of a forged result puts the job
  back to running and counts bob_fleet_forged_results_total.
- D-IB2-9 Checkout tokens: CheckoutTTL 1 h; ftok/<sh2(job), 16 shards> = {job → [{ep, forge,
  handle, ex, fx}]}; minted before the record (a crash in between is the forge-side name sweep's,
  §6.9.6); a record write failure revokes at once. Clone URLs must be https without userinfo.
- D-IB2-11 Backlogs shorter than 8 slots are walked in order (no spread pick): the spread exists
  against herds on deep backlogs; on a short one it would run newer jobs before older ones.
- D-IB2-12 The claimer marks the slot tk after the run started (the job record already says
  claimed; tk is a poller hint), so the wake → Exec path is claim + start (4 requests). A start
  that finds a terminal result in its own name that it did not sign (forged before the start
  write) replaces it with running.
- D-IB2-10 Planner: T1 = list schedule by bottom level + 0.14 s × drvs; Tsplit as SPEC; split
  with 1e-9 tolerance; batches = dependency waves, k = ceil(|wave| / I) ≤ 64 and ≤ 60 est. s;
  QueuedJobs = min(first-wave batches, 2 × I).

## NEEDS
- tests/store/fleet/lib_test.go (owner): `l.q` needs `Now: l.clk.Now` (see TEST PROBLEM 1).
- I-B1 / internal/store: a sentinel for "record body does not decode" (the registrar matches
  the text "decode record" to reject a garbage wreg/ instead of failing the pass).
- I-B3 (cmd/bob-server, internal/api): wire Registrar (Pass every 2 s, Rotate every 60 s),
  Reconciler (10 s), Dispatcher over Hub.Workers(), Checkouts (Get for GetCheckoutCredential,
  sealed with fleet.SealCheckout to the worker KEM key from worker/<wid>; Sweep every 60 s);
  worker mode: worker.NewIdentity → Register → poll FetchSealed every 1 s (5 min) → OpenToken;
  worker.Config{SigKey: id.SigningKey(), Classes from fleet/cfg.c, Keys from fleet/cfg.k}.
- docs/operations/worker-compromise.md (R-SEC-101 runbook, M13.4): owner of docs/; the commands
  are `bob-server worker revoke <wid>` (→ Registrar.Revoke) and rotating the shared worker S3
  credential in the deployment.

## TEST PROBLEM
1. TestRFLEET003Reslot: the lab's Queue has no clock (`lib_test.go` newLab builds
   `orchestrator.Queue{Fleet, Control, Class, Shards, Key, Kid, WorkerKey}`), so Enqueue stamps
   `qa` with the wall clock while the reconciler runs on the injected clock (2026-10-22 12:00 UTC).
   With qa = wall time (e.g. 15:41 UTC) the 59 s / 61 s comparison can never hold. Queue now has
   an optional `Now` field; the fix is one line in newLab: `l.q = &orchestrator.Queue{…, Now: l.clk.Now}`.
2. TestRFLEET005Planner: the "within 25 % of the S14 model" rows cannot hold with the SPEC formula
   that TestRFLEET005PlannerBoundaries pins (T1 = makespan + 0.14 s × drvs in full, also for 2
   slots): go200 T1 = 10.9 + 0.14 × 201 = 39.0 s vs 30.0 (30 % off); wide2000 T1 = 49.9 + 280.1 =
   329.9 s vs 223.6, and 0.14 × 2001 alone (280 s) exceeds the 25 % band (279.5 s); wide2000
   Tsplit = (100 + 0.6 × 2001) / 200 = 6.5 s vs 16.6. The S14 numbers come from the event
   simulation (claim-rate ceiling, fetch/upload costs, 0.14 s inside each slot), not from the
   planner formula. All Split decisions (fixture no, go200/rust129/wide2000 yes, 1 worker no)
   are correct; only the closeness rows fail.
