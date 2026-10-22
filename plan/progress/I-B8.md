# I-B8 (Track B: M14.3 controllers/reconciliation/HA → M14.4 retention) — internal/orchestrator/controllers_*.go, cmd/bob-server/parts_controllers.go

## Done
- Read AGENTS, impl-brief, HARNESS, I-B1..I-B5 progress, SPEC §05.6.5/§05.10.5/§05.11, tests/chaos,
  tests/server/fleet_test.go, tests/store/retention_test.go, tests/soak/intent_test.go.
- internal/orchestrator (new files only):
  - controllers.go: `Controller` (server.Component) = leader-only runner over ctl/<name>: own CAS
    lease state machine of §05.6.5 with the orchestrator clock (env.Now, so BOB_TEST_CLOCK_OFFSET
    ages leases too) and fleet.skew; TTL 2 × interval (D-IB8-1), renew after TTL/2, contenders
    re-check at min(pass period, expires + skew); release on shutdown; Persist mode keeps the last
    pass time + controller state in ctl/<name>.cu (hourly passes are not repeated by a new leader);
    Wake channel for event-driven controllers; pass panics converted to errors. `Leaders` (who
    holds which lease) and the controller name constants.
  - controllers_membership.go: `Membership` — heartbeat component on every orchestrator (orch/<id>
    every 10 s, join fleet/cfg.m at start and whenever missing, leave on graceful stop); leader
    pass `Prune` (lease ctl/membership, every 5 s) removes members with heartbeat > 30 s, or no
    record 30 s after joining; `List` for ListOrchestrators (members + record + leader_of).
  - controllers_keys.go: key controller (hourly): next key 7 d before notAfter (kids k000001…,
    seed first, then fleet/cfg.k; a crashed pass is finished by the next one under the same kid),
    expired keys retired 2 h after notAfter. Record format = fleetsrv.OrchKey.
  - controllers_repair.go: index-repair (10 min): jact entries > 5 min without job record removed;
    bpage entries > 5 min without build record tombstoned (state "deleted", needs a Projects hook);
    rmem entries without binding removed when seen dangling on two passes ≥ 5 min apart (state
    persisted); bindings missing from rmem added (walk uidx pages + saidx).
  - controllers_retention.go: retention (hourly, R-STORE-007): builds (terminal, older than
    retention.builds) walked from the oldest bpage page per project (cursor persisted): bnodes,
    build record, then tombstone; qs slots < head − 1000 per class of fleet/cfg.c; wrs slots <
    registrar cursor − 1000; hooks for lake partition drops (log 30 d, run/job 400 d) and cache GC
    (90 d, §06 reference rule). Releases never touched.
  - controllers_staging.go: staging-sweep controller (hourly; EnsureLifecycle once, then
    sweep.Staging.Sweep with the orchestrator clock); `StagedKeys`/`Staged.Delete` = R-STORE-105
    first bullet helper (COMPLETE.json → known staged keys, validated hostile input, COMPLETE.json
    deleted last) for the promotion controller.
  - controllers_fleet.go: RegistrarController (+ rotation every 60 s), ReconcilerController
    (UseLease off, runner holds the lease), ForgeSweeperController. I-B6 adopted them in
    fleetsrv.Components.
  - controllers_fsck.go: `CheckRecord` / `Fsck` (decode + invariants per record type) for
    `bob-server store fsck` (R-NFR-007); listing left to the caller (NEEDS).
- cmd/bob-server/parts_controllers.go + one line in parts.go: membership (heartbeat + leader),
  key, index-repair, retention, staging-sweep. Settings: BOB_FLEET_SKEW, BOB_FLEET_JOB_SHARDS,
  BOB_RETENTION_{LOGS,ANALYTICS,BUILDS,CACHE} ("30d" or Go duration).
- Tests (new): internal/orchestrator/controllers_test.go (RustFS): handover after a crashed holder
  within 3 × interval, handover on shutdown within one interval, one leader among 3 contenders,
  Persist state/last-pass hand-over, membership join/list/prune/leave/leader_of, index-repair
  (jact, bpage, rmem both directions, 5 min grace), retention (builds+bnodes+tombstones, qs, wrs,
  partition hook cutoffs, idempotent second pass), key rotation incl. crash recovery;
  controllers_staging_test.go: StagedKeys hostile cases, CheckRecord table. All green.
- Manual (devstack, 3 orchestrators, BOB_FLEET_SKEW=1s, host shared with other agents,
  2026-10-22): kill -9 of the holder of every lease → registrar taken over after 4.2 s (bound 6 s),
  membership 16.7 s (30 s), job-reconciler 16.9 s (30 s); killed member removed from fleet/cfg
  after 32 s (40 s).
- Checks: gofmt, GOWORK=off go vet, staticcheck, boblint (nopanic/noexit/nolist/safego/
  importboundary) clean on internal/orchestrator and cmd/bob-server (except cmd/bob-server
  auth.go U1000 authParts unused at one point while I-B6 edited parts.go — not mine).
  `tools/ci -only fmt,vet,lint,layering`: layering ok; fmt/vet/lint fail only on other units'
  files (internal/logstore, internal/orchestrator/logs_pull_test.go, pkg/golang gospec,
  internal/worker S1016, out/uicheck).

## Still red (not reachable yet)
- TestRSRV004OrchestratorKill, TestRNFR007CrashSafety, TestRSRV102Membership: srvtest.Start
  fails at `bob login` (BOB-E-9008, CLI M16.5; I-B3 TEST PROBLEM: t.Fatalf instead of red);
  then they need builds (build controller), FleetService.ListOrchestrators (NEEDS I-B6) and
  `bob-server store fsck` (NEEDS).
- TestRSTORE007Retention, TestRSTORE105StagingCleanup: need `bob build --remote` (M16.5), the
  build controller/projects (Projects hook), promotion (M16.2, StagedKeys helper ready), lake
  partition drop (logstore owner), cache GC (§06).
- TestRSRV101IntentFirst (slow soak): needs builds, projects, role services end-to-end.

## DECISIONS
- D-IB8-1 Controller lease TTL = 2 × interval, not SPEC §05.6.5's 3 × interval: R-SRV-004 asks
  that a dead holder's lease changes owner within 3 × interval, and a takeover is only allowed
  after expires + skew, so TTL 3 × interval can never meet it. 2 × interval + skew meets it when
  skew < interval (the chaos env sets fleet.skew 1 s; with the 5 s default the 2 s registrar
  cannot meet it under any TTL ≥ its interval). Correctness never depends on the lease (every
  write is a CAS). SPEC §05.6.5 text should follow (owner may overturn).
- D-IB8-2 The runner implements the lease itself (CAS on store.ControllerLease) instead of
  store.Lease: it needs the orchestrator clock (test clock offset: the retention tests age a fresh
  orchestrator by 31/91/366 d and expect the hourly controllers to run at once) and fleet.skew
  (store.LeaseSkew is a 5 s constant). Same record and fields; cursor kept.
- D-IB8-3 Lease owner = orchestrator id (BOB_SERVER_NAME): a restarted process with the same id
  renews its unexpired leases at once. leader_of = leases with owner = member id and expires > now.
- D-IB8-4 Membership write order: fleet/cfg.m entry (index) before orch/<id> (record); removal
  deletes orch/<id> first. Prune keeps a member without record for 30 s after `since`.
  The leader prunes every 5 s (lease interval stays 10 s) to keep the 40 s removal bound.
- D-IB8-5 Key controller kids are "k" + 6 digits derived from the published keys, so a pass that
  crashed between okey/<kid> and fleet/cfg.k adopts its own record next time (no orphan seed).
  fleetsrv's start-up key keeps its hash kid.
- D-IB8-6 Build page entries removed by index-repair/retention become tombstones
  {n, state "deleted"} (positions never move); readers must skip them.
- D-IB8-7 rmem entries carry no time: index-repair removes an entry only when it was dangling on
  two passes at least 5 min apart (first-seen times persisted in ctl/index-repair.cu).
- D-IB8-8 Retention deletes build bnodes, then build/<id>, then tombstones the entry; per-project
  cursor = first bpage page that still has live entries. Queue/registration slot deletes are
  bounded to 10,000 per log per pass.
- D-IB8-9 Staged objects after promotion: StagedKeys derives nar/narinfo keys from COMPLETE.json
  paths, oci/blobs from manifest+blobs digests, assets only on request; COMPLETE.json last and
  kept when any other delete failed.

## NEEDS
- I-B6 (internal/fleetsrv): FleetService.ListOrchestrators from `Membership.List` (parts.go
  should keep the `*controllerSet` controllerParts returns: `cs := controllerParts(env, &parts)`
  and pass `cs.Membership`); map OrchestratorInfo → fleetv1.Orchestrator (id, endpoint, version,
  started_at, last_heartbeat, leader_of).
- I-B6 (fleetsrv keysOf): pick the signing key as the oldest valid key with more than 3.5 d left
  (not the smallest kid): rotated kids "k000001…" sort before hash kids, so "smallest kid" would
  switch to a just-published key before workers (30 s config refresh) know it.
- cmd/bob-server main.go owner + I-B1 (internal/store/sweep): `bob-server store fsck` = for the
  control and fleet buckets, LIST all keys in internal/store/sweep (UA bob-sweep/fsck) and call
  `orchestrator.Fsck(ctx, client, bucket, role, keys)`; print problems, exit 1 if any.
- Build controller / projects owner: a `Projects func(ctx) ([]string, error)` (pidx walk) for
  IndexRepair.Projects and Retention.Projects in parts_controllers.go; bpage readers skip
  state "deleted".
- logstore owner: Retention.DropPartitions(ctx, class "log"|"run"|"job", before) over the lake
  sweep (R-STORE-101). §06 cache owner: Retention.CacheGC(ctx, before).
- Promotion controller (M16.2): after `pm` → done call
  `(&orchestrator.Staged{S3, Bucket}).Delete(ctx, spec.Staging, completeJSON, false)`.
- pidx repair (index-repair) and shard rehash after a gen change: when the project index and
  shard-count changes exist.

## TEST PROBLEM
- (none new) srvtest.Start → `bob login` stub is t.Fatalf, not red (already I-B3's item).
