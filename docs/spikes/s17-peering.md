# S17: live logs across orchestrators (optional peering)

## 1. Question and requirement ids

How does a client connected to orchestrator A follow the live log of a build whose worker
streams to orchestrator B, without breaking "no coordinator" and without making any
orchestrator-to-orchestrator connection required for correctness?

Requirements: R-SRV-003, R-SRV-004, R-SRV-005 (orchestrators need not talk; peering only as
an optional optimisation), R-LOG-001, R-LOG-002 (live view uses the direct path when
available, lake is fallback and persistent store), R-UI-002 (build detail: live log),
R-SEC-004 (hostile input). Decisions D7, D12, D13, D20, D38.

Inputs used: S10 (live view via lake p50 1.9 s / p99 2.7 s at 5 s flush; per-follower lake
polling downloaded 38 GB in 5 min; lake cap ~8–10 commits/s per bucket), S9 (`orch/<id>`
record, leases, polling costs), S11 (worker tokens, prefix policy: workers cannot write
`orch/`, `okey/` only in the control bucket).

## 2. What was built

`spikes/s17-peering/` (module `bobspike/s17`, one binary `s17`, ~1,300 lines):

| File | Content |
|---|---|
| `cmd/s17/orch.go` | Orchestrator process: `orch/<id>` heartbeat + member list in a dynamitedb fleet bucket (calls wrapped with `recover`, copied S9 wrapper in `store/`), connectrpc server over TLS/h2 with `PushLines` (worker), `FollowLog` (client, server stream), `PeerLogService.Follow` (peer, server stream); per-build live buffer; lake reader. |
| `cmd/s17/buf.go` | Live buffer (append-only, capped, dedup by package/attempt/seq) and the **merger** (section 5.4). |
| `cmd/s17/worker.go` | Simulated worker: produces lines, pushes every 200 ms, flushes to the lake stand-in every 5 s, crash and retry (attempt 2). |
| `cmd/s17/bench.go`, `sec.go` | Scenarios: `kill`, `killclient`, `retry`, `fanout`, `sec`. Each starts 3 orchestrator processes (A, B, C on 21701–21703). |
| `measure.sh`, `start-rustfs.sh` | Rerun everything (RustFS 1.0.3 on 21750, binary from S9). |
| `results/*.txt` | Raw output of the runs below. |

Four modes, same code:

- **peer**: worker pushes to the build's *home* orchestrator (rendezvous hash of build id over
  live `orch/` records; next one on failure). Other orchestrators with followers subscribe to
  the first K=2 non-self orchestrators of the same order (`PeerLogService.Follow`, mTLS-like:
  TLS with the SPKI pin from `orch/`, plus a signed peer token).
- **pushall** ("worker fan-out"): worker pushes to *every* live orchestrator. No
  orchestrator-to-orchestrator connection at all.
- **lakeonly**: no live path; each orchestrator polls the lake every 3 s, shared by its followers.
- **lakeprivate** (baseline): every follower polls the lake on its own.

Simplifications (all marked again in section 6):
- The **lake is a stand-in**: one JSONL object per package per flush under
  `lake/<build>/<pkg>/<att>-<first>-<last>.jsonl` in RustFS, plus `_end.json`. Real lake
  (S10) has shards, a catalog and its own latency; byte numbers for the lake path are
  therefore far *lower* here than with real lake.
- Worker tokens are Ed25519-signed claims with a list of builds standing in for "worker holds
  a job lease of this build" (S9 `live/…` lease check); no PoP header (S11 has it).
- Peer and worker tokens are signed with one key read from the control bucket (`okey/k1`),
  with domain separation, as S11 proposes for `okey/<kid>`.
- JSON over connectrpc with a custom codec instead of generated protobuf (no codegen in a
  throwaway spike); byte counts are JSON payload sizes without TLS/h2 framing.

## 3. Results

All numbers `[8c/15G]`, localhost, RustFS 1.0.3, other spikes running (indicative; rerun
`measure.sh` on a quiet host for latency and CPU). Latency = client receive time − worker
read time of the line (same clock).

### 3.1 Kill tests (`results/kill.txt`): 1 build, 3 packages, 100 lines/s, 30 s, kill at 10 s

Build ids are chosen so the home is B; the follower is on A (fails over to C).

| Mode | Killed | Gaps | Dups | Equal to lake | Reconnects | Max stall | Latency p50 / p99 |
|---|---|---|---|---|---|---|---|
| peer | B (home, worker's target) | 0 | 0 | yes (2,985 lines) | 0 | 1.74 s | 103 ms / 1,430 ms |
| peer | A (follower's own) | 0 | 0 | yes (2,988) | 1 | 0.52 s | 102 ms / 225 ms |
| pushall | B | 0 | 0 | yes (2,988) | 0 | 0.25 s | 101 ms / 194 ms |
| pushall | A | 0 | 0 | yes (2,987) | 1 | 0.51 s | 102 ms / 208 ms |
| lakeonly | B | 0 | 0 | yes (2,991) | 0 | 6.05 s | 3,655 ms / 7,006 ms |
| lakeonly | A | 0 | 0 | yes (2,986) | 1 | 6.06 s | 3,694 ms / 7,118 ms |

"Equal to lake" compares every (package, seq, attempt, text) the follower holds at the end
with the persisted log after the validity rule (5.4). In peer mode the 1.7 s stall is the
worker noticing B is gone and re-sending its unflushed window to C; A was already subscribed
to C (K=2), so no lake read was needed.

### 3.2 Worker crash and retry (`results/retry.txt`)

Worker crashes 12 s into the job (2 s after its last flush), ~195 unflushed lines are lost;
attempt 2 starts 2 s later, continues `seq` after the last persisted line and sends
`Restart{pkg, attempt 2, from}`.

| Mode | Lost lines | Restart markers at follower | Gaps / dups | Final equal to lake |
|---|---|---|---|---|
| peer | 198 | 3 (one per package) | 0 / 0 | yes (1,995) |
| pushall | 197 | 3 | 0 / 0 | yes (1,993) |
| lakeonly | 194 | 0 (lost lines were never shown) | 0 / 0 | yes (1,990) |

Live followers had already shown the lost lines; the restart marker retracts them and the
follower ends identical to the lake. This is the only case where a live view shows lines that
are later withdrawn; the UI must render the marker ("worker lost, attempt 2 continues at
line N").

### 3.3 Fan-out (`results/fanout.txt`): 20 builds × 200 lines/s × 5 packages, 60 s; build 0 has 50 followers (17/17/16 on A/B/C), builds 1–19 one follower each

| Mode | Gaps / dups / not done | Latency build 0 p50 / p99 / max | All builds p99 | worker→orch | orch→orch | orch→clients | S3 by orchestrators | Orch CPU (3 procs) |
|---|---|---|---|---|---|---|---|---|
| peer | 0 / 0 / 0 | 109 / 398 / 580 ms | 382 ms | 35.6 MB | 30.2 MB | 122.5 MB | 15.1 req/s, GET 0.2 MB | 16.9 s (28 % of a core) |
| pushall | 0 / 0 / 0 | 113 / 223 / 446 ms | 236 ms | 107.7 MB | 0 | 123.5 MB | 15.1 req/s, GET 0.2 MB | 21.3 s (36 %) |
| lakeonly | 0 / 0 / 0 | 4,100 / 7,489 / 7,955 ms | 7,495 ms | 0 | 0 | 119.8 MB | 45.2 req/s, GET 41.6 MB | 8.1 s (13 %) |
| lakeprivate | 0 / 0 / 0 | 4,045 / 7,474 / 8,078 ms | 7,467 ms | 0 | 0 | 124.1 MB | 109.4 req/s, GET 134.3 MB | 15.4 s (26 %) |

- 15.1 req/s of S3 in the live modes is the spike's 1 s heartbeat + member list of 3
  orchestrators (1 PUT + 1 LIST + 3 GET each). At the proposed 10 s interval: 1.5 req/s.
- Worker S3 writes are the same in every mode (~1,660 PUT, 35 MB): the persisted log does not
  depend on the live path.
- Lake baseline from S10 with **real lake**: per-follower polling downloaded **38 GB in 5 min**
  (40 MB shard re-reads). The stand-in's small objects make the lake modes look much cheaper
  than they are; with real lake, `lakeonly` needs the S10 shard cache and `lakeprivate` is
  not viable.
- Fan-out of 50 followers costs the live path nothing extra between servers: each
  orchestrator holds one buffer per build and one upstream (peer) or receives the push
  (pushall); followers read from local memory.

`[2c/4G]`: see 3.5.

### 3.4 Security (`results/sec.txt`): 14 cases, 0 failed

| Case | Result |
|---|---|
| valid peer token A→B | accepted |
| worker token on the peer endpoint | `unauthenticated: peer token invalid: wrong token type` |
| peer token for audience A replayed to B | `audience mismatch` |
| expired peer token | `expired` |
| issuer not in live `orch/` records | `issuer is not a live orchestrator` |
| token signed with a different key | `bad signature` |
| peer claims signed under the worker domain | `bad signature` |
| connecting to B's address with A's pin (redirected record) | TLS fails: `peer certificate does not match orch record pin` |
| worker push to its own build | accepted |
| worker push to another build | `worker holds no job of this build` |
| peer token used as worker token | `wrong token type` |
| line > 4 KiB, package name with ESC | `invalid_argument` |
| 6 MB request | `resource_exhausted: message size … larger than configured max 4194304` |
| flood, 3 s of ~1 MB pushes | 7 accepted, 138 rejected (`resource_exhausted`; 1 MiB/s, 4 MiB burst) |

Not tested here: that a worker credential cannot write `orch/` (S11 verified the prefix
policy on RustFS and SeaweedFS; `orch/` is not in the worker's allowed prefixes).

### 3.5 Low-power profile

`[2c/4G]` (`results/fanout-2c4g.txt`): the whole bench (3 orchestrators, 20 workers, 69
followers) inside `systemd-run --scope -p AllowedCPUs=0-1 -p MemoryMax=4G`; RustFS outside
the limit. Same fan-out load as 3.3.

| Mode | Gaps / dups | Build 0 p50 / p99 / max | orch→orch | Orch CPU |
|---|---|---|---|---|
| peer | 0 / 0 | 123 / 297 / 440 ms | 23.5 MB | 14.1 s (23 %) |
| pushall | 0 / 0 | 133 / 289 / 395 ms | 0 | 18.8 s (31 %) |

The p99 is lower than in the `[8c/15G]` run because other spikes loaded the full host at that
time; both profiles are far inside the 1 s budget.

## 4. Answer

1. **The live path must never be needed for correctness, and it is not.** The persisted log
   (lake) plus `(package, attempt, seq)` with the restart rule is the truth. Every live
   source is only a faster copy. All 12 failure runs ended identical to the lake with zero
   gaps and zero duplicates, including killing the worker's target, killing the client's
   orchestrator, and a worker crash with retry.
2. **Recommended: worker fan-out (`pushall`) with no orchestrator-to-orchestrator connection.**
   Workers already call orchestrator RPCs (S11). Pushing each batch to every live
   orchestrator (from `orch/` records) gives the best tail latency (p99 223 ms vs 398 ms),
   the shortest stall on failure (0.25 s vs 1.7 s), and needs no peer protocol, peer auth,
   subscription manager or peer-reachability between orchestrators. It keeps R-SRV-005 and
   D7 literally. Cost: worker log egress × N orchestrators (here 3×: 108 MB vs 36 MB per
   minute for 4,000 lines/s fleet-wide) and +26 % orchestrator CPU. For a typical fleet
   (2–5 orchestrators, logs averaging well under 200 lines/s per build) this is a few hundred
   KB/s per orchestrator.
3. **Peering (`peer`) works and is the right choice only for many orchestrators** (where
   N× worker egress gets large): rendezvous home per build, peers subscribe through
   `PeerLogService.Follow` with TLS pinned to the `orch/` record and a short-lived signed
   peer token. No per-build records are needed; discovery is the existing `orch/<id>` record.
4. **Lake-only is the degraded mode**, automatically used when no live line arrives for 6 s
   or a follower sees a gap for 1 s: p50 ~4 s, p99 ~7.5 s with 5 s flush and 3 s poll. Lake
   reads are shared per orchestrator and per build, never per follower.
5. The merge runs **on the orchestrator**, so the UI/CLI client only appends lines, applies
   restart markers and reconnects with its cursor (`map package → (seq, attempt)`). Client
   reconnect to another orchestrator was gap-free.
6. A compromised worker can, through this path, only (a) write lines for builds it holds a
   job lease on (already true for the lake, D38), (b) spend its rate budget, and (c) send
   control characters that the UI must render inert. It cannot reach the peer endpoint,
   cannot mint peer tokens (key only in the control bucket), and cannot redirect peers or
   workers to a fake orchestrator because `orch/` is orchestrator-only and TLS is pinned to
   the record.

## 5. SPEC input

### 5.1 Proto draft [proposal]

```proto
// proto/bob/worker/v1/livelog.proto
syntax = "proto3";
package bob.worker.v1;
import "bob/log/v1/log.proto";

// Worker -> orchestrator. fanout mode: sent to every live orchestrator; peer mode: to the
// build's rendezvous home (next one on failure). Auth: worker token + PoP (S11).
service LiveLogService {
  rpc PushLines(PushLinesRequest) returns (PushLinesResponse);
}
message PushLinesRequest {
  string build_id = 1;
  string job_id = 2;                         // must be leased to the caller (live/<platform>/j/<job>, owner + epoch)
  int64 epoch = 3;                           // job lease epoch (S9 fence)
  repeated bob.log.v1.LogLine lines = 4;     // ≤ 5,000 per request, text ≤ 4 KiB, request ≤ 4 MiB
  repeated bob.log.v1.Restart restarts = 5;  // sent with every request of attempt ≥ 2
  repeated bob.log.v1.Final end = 6;         // once, after the last lake commit succeeded
}
message PushLinesResponse { int32 accepted = 1; }

// proto/bob/log/v1/log.proto (adds to S10's LogService)
service LogService {
  // Server stream: persisted + live lines after the cursor, merged gap-free and duplicate-free.
  // Ends with done=true after the end marker; the client reconnects with its cursor on error.
  rpc FollowLog(FollowLogRequest) returns (stream FollowLogResponse);
}
message Cursor  { int64 seq = 1; int32 attempt = 2; }
message Restart { string package = 1; int32 attempt = 2; int64 from_seq = 3; }
message Final   { string package = 1; int64 last_seq = 2; int32 attempt = 3; }
message FollowLogRequest  { string build_id = 1; string package = 2; /* optional filter */ map<string, Cursor> cursor = 3; }
message FollowLogResponse {
  repeated LogLine lines = 1;      // in seq order per package
  repeated Restart restarts = 2;   // client drops its lines of this package with seq >= from_seq
  repeated Final end = 3;
  bool done = 4;
  bool delayed = 5;                // true while this orchestrator has no live source (lake only)
}

// proto/bob/peer/v1/peer.proto — only when live_log.mode = peer
package bob.peer.v1;
service PeerLogService {
  // Lines this orchestrator received directly from workers (never re-forwarded peer lines),
  // after the cursor; first message is empty (connection established); ends after the end marker.
  rpc Follow(PeerFollowRequest) returns (stream PeerFollowResponse);
}
message PeerFollowRequest  { string build_id = 1; map<string, bob.log.v1.Cursor> cursor = 2; }
message PeerFollowResponse { repeated bob.log.v1.LogLine lines = 1; repeated bob.log.v1.Restart restarts = 2; repeated bob.log.v1.Final end = 3; }
```

`PeerLogService` is not a public API (R-API-004 unaffected); it is not in the RBAC endpoint
mapping because it accepts only peer tokens. The RBAC CI test (R-AUTH-006) must list it as
an explicit exception.

### 5.2 Discovery record [proposal] (extends S9 `orch/<orchID>`, fleet bucket, CBOR)

| Field | CBOR | Meaning |
|---|---|---|
| endpoint | `u` | `https://host:port` reachable by workers (and peers in peer mode) |
| cert pin | `fp` | hex SHA-256 of the TLS leaf SubjectPublicKeyInfo; workers and peers accept only this key |
| live ingest | `ll` | accepts `PushLines` |
| peering | `pe` | serves `bob.peer.v1` |
| peer proto | `pv` | `1` |
| heartbeat | `h` | written every 10 s by the owner; member if `h > now − 30 s` |
| started | `st` | process start (restart detection) |

Writer: only that orchestrator (workers have no write on `orch/`, S11 policy). Readers:
orchestrators and workers, one LIST + N GET per 10 s each. No per-build records.

Home of a build (peer mode): `order = sort live members by fnv64a(build_id + "|" + orch_id)
descending`; home = first reachable. Peers subscribe to the first K non-self entries
(K = 2, so a home failure does not need a new subscription).

### 5.3 Worker push protocol [proposal]

1. Lines go to an in-memory log (never blocked by the network) as in S10 5.2.
2. Every 200 ms: for each target (fanout: all live members; peer: the home), send the lines
   after that target's acknowledged index. A new or recovered target starts at the first
   line **not yet committed to the lake**, so a target change never needs the lake.
3. On error: target is skipped for 5 s and its index is forgotten.
4. After the final lake commit and `_end`/`package_run` write: push `end` to all targets.
5. Retry of a job (attempt n ≥ 2): seq continues after the last persisted seq per package;
   every push carries `Restart{pkg, n, from}`.
6. Live push failures never fail the job and never delay the lake flush.

### 5.4 Merge algorithm [proposal] (orchestrator side, one merger per follower)

Valid line of package p at seq s: the line with attempt `max{a : from(p,a) ≤ s}`,
`from(p,1) = 1`. Valid lines of a finished package are exactly seq 1..last, once each.

```
state: cur[p] = (seq, att) last delivered (from the client cursor), pending[p][seq] = line,
       from[p][att] = known restarts
on restart r: from[r.p][r.a] = min(existing, r.from)
              if cur[r.p].att < r.a and cur[r.p].seq >= r.from:
                  emit Restart r; cur[r.p] = (r.from - 1, r.a)
              drop pending lines that are no longer valid
on line l:   drop if l.att < attFor(l.p, l.seq) or (l.seq <= cur.seq and l.att <= cur.att)
             keep the higher attempt per seq in pending
drain:       while pending[p] has cur[p].seq + 1: emit it, advance cur[p]
done:        end marker known and cur[p].seq == last[p] for all p
lake read:   when a package has a gap for > 1 s, the end marker is known but not reached
             for > 1 s, the reader's buffer offset was evicted, or no live line came for 6 s
```

Sources feed one per-build buffer per orchestrator (worker push, peer stream, lake reads),
deduplicated by (package, attempt, seq). Restarts from the lake are inferred from the
smallest first seq of attempt n. When lake becomes a real lake table, the same rule works on
`log_line(build_id, package, attempt, seq)` (S10 5.1).

### 5.5 Failure modes [proposal]

| Failure | Effect | Recovery | Measured |
|---|---|---|---|
| Worker's push target dies | fanout: none (other targets); peer: stall until worker fails over | worker resends its unflushed window to next target | stall 0.25 s / 1.74 s, 0 gaps |
| Follower's orchestrator dies | stream breaks | client reconnects to any orchestrator with its cursor | 0.5 s, 0 gaps |
| All live paths down / peering off | `delayed = true` | lake read every 3 s | p99 7.5 s, 0 gaps |
| Worker crash | unflushed lines lost (R-LOG-001) | retry with Restart; client retracts lines ≥ from | 0 gaps, final = lake |
| Orchestrator restart | empty buffer | followers fill from lake; peer/push resume | not separately measured (same path as kill) |
| Lake commit lag/failure | live view unaffected; fallback later | worker retries flush (S10) | — |
| Peer token or pin invalid | subscription refused | peer treated as down; lake fallback | sec tests |
| Buffer eviction (long build, slow follower) | follower offset lost | lake read | code path, not measured |

### 5.6 Configuration [proposal]

| Key (env `BOB_…`) | Default | Meaning |
|---|---|---|
| `live_log.mode` | `fanout` | `fanout` \| `peer` \| `off` (lake only) |
| `live_log.push_interval` | `200ms` | worker batch interval |
| `live_log.buffer_lines` | `50000` per build | orchestrator buffer cap (with followers) |
| `live_log.idle_ring_lines` | `2000` per build | cap for builds without followers (fanout) |
| `live_log.worker_rate` | `1MiB/s`, burst `4MiB` | per-worker ingest limit |
| `live_log.stale_after` | `6s` | no live line → read lake |
| `live_log.gap_wait` | `1s` | gap → read lake |
| `live_log.lake_poll` | `3s` | lake read interval while degraded |
| `live_log.peer_k` | `2` | peers to subscribe per build (peer mode) |
| `fleet.orch_heartbeat` | `10s` (member for 30 s) | `orch/` record |

### 5.7 Budgets [proposal]

| Item | Budget | Measured [8c/15G] |
|---|---|---|
| Line → screen, live path | p99 ≤ 1 s | fanout 223 ms, peer 398 ms (50 followers, 20 builds) |
| Line → screen, degraded | p99 ≤ 10 s | 7.5 s (5 s flush, 3 s poll, stand-in lake) |
| Stall on orchestrator loss | ≤ 3 s | 0.25 s (fanout), 1.74 s (peer) |
| Gaps / duplicates after any failure | 0 | 0 in 12 failure runs + 4 fan-out runs |
| Server-to-server bytes per follower | 0 (one stream per build and orchestrator) | 50 followers → 1 upstream per orchestrator |
| Lake GET by live followers | ≤ 1 read of each object per orchestrator per build | 0.2 MB in 60 s (live modes) |
| Orchestrator CPU per 1,000 ingested lines/s | ≤ 10 % of a core | fanout ~9 %, peer ~7 % (3 procs, 4,000 lines/s, 800k lines out) |

### 5.8 Error texts [proposal]

- `unauthenticated: worker token invalid: worker holds no job of this build`
- `invalid_argument: log line invalid: <package>/<seq>`
- `resource_exhausted: live log rate limit; lines stay in the persisted log`
- `unauthenticated: peer token invalid: <reason>` (peer endpoint; logged, not shown to users)
- `unimplemented: live log ingest disabled` (mode off; worker stops pushing for 60 s)
- UI marker: `attempt <n> continues at line <from>; lines after it from attempt <n-1> were not saved`

## 6. Decisions, open items

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S17-1 | Merge on the orchestrator, not in the UI. | One implementation, the TS client stays trivial (R-API-004 still holds: it is a public RPC). |
| S17-2 | Cursor = map package → (seq, attempt); restarts are explicit markers. | Seq alone cannot express a retried attempt that reuses seq numbers (S10 5.2 step 5). |
| S17-3 | Worker resends from the last lake commit on target change. | Avoids lake reads in the common failover case (measured: none needed). |
| S17-4 | No per-build location records in dynamitedb. | Rendezvous over `orch/` gives the same answer with zero extra writes (S9 cost budget). |
| S17-5 | TLS pin in `orch/` instead of a cluster CA. | No new secret or CA to operate; `orch/` is orchestrator-only (S11). |
| S17-6 | Peer and worker tokens signed with the S11 orchestrator key, domain-separated. | No new key material; tested that one cannot be used as the other. |
| S17-7 | Lake reads shared per orchestrator and build, objects cached by key. | S10's 38 GB/5 min came from per-follower reads. |

### Owner decision needed

**O-S17-1: How live logs reach an orchestrator that a client is connected to.**

- **a) Worker fan-out** (recommended): workers push to every live orchestrator; no
  orchestrator-to-orchestrator connection exists. Keeps R-SRV-005 and D7 literally. Measured
  best latency and failover. Cost: worker log egress × number of orchestrators.
- **b) Peering as D12 describes**: workers push to one home orchestrator; others subscribe to
  it over `bob.peer.v1` (TLS pin + peer token). Lowest worker egress, adds a peer protocol,
  peer auth and orchestrator-to-orchestrator reachability; stall on home loss 1.7 s.
- **c) Both, configurable** (`live_log.mode = fanout | peer | off`), default fanout; peer for
  installations with many orchestrators. More code and tests (two live paths).

Recommendation: **a**, and keep b documented (this write-up) for later if fleets with many
orchestrators appear. Either way `off` (lake only) stays and is the fallback.

### Open risks

- Real lake latency and read cost on the degraded path are not measured here (stand-in);
  S10's numbers apply, and the S10 shard cache is required.
- Workers must reach every orchestrator endpoint directly (fanout) or the home (peer); a
  deployment with workers behind a single load balancer address cannot use fanout as is.
- In fanout, orchestrators without followers still buffer every build. The idle ring cap
  (2,000 lines) is a proposal; the spike used one 50,000-line cap for all buffers. Memory at
  100+ builds is not measured.
- Lines shown live may be withdrawn after a worker crash; the UI must make this visible.
- Log content can contain secrets: TLS on every hop is required; it was used in the spike.

### Still to verify

- Quiet-host rerun of latency/CPU (`measure.sh`), and 5+ orchestrators.
- Job-lease check on `PushLines` against real `live/` records (spike used token claims).
- Orchestrator restart (not kill) with followers attached; buffer eviction path under load.
- HTTP/2 through a reverse proxy for `FollowLog` server streams (browser via connect-web).
