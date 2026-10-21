# ADR 0019: Live build logs without a coordinator

Status: **Accepted (Gate S pending)**, 2026-10-21. Decision rewritten by D55 (O-S17-1 superseded: neither fan-out push nor peering).
Evidence: `docs/spikes/s17-peering.md`, code `spikes/s17-peering/`.

## Context

R-LOG-002 and D12 want live log viewing over an optional direct path, with lake as the
persistent store and fallback. R-SRV-005 / D7: orchestrators need not talk to each other;
orchestrator-to-orchestrator connections are allowed only as an optional optimisation. S10
showed the live view from lake alone costs p99 seconds and, polled per follower, 38 GB in
5 min; lake allows only ~8–10 commits/s per bucket, so it cannot be made faster by flushing
more often.

S17 ran 3 orchestrator processes, simulated workers and up to 69 followers on localhost with
dynamitedb `orch/` records on RustFS 1.0.3 and a lake stand-in, in four modes: worker
fan-out, peering, lake-only shared, lake-only per follower.

## Decision (rewritten after D55)

1. **Correctness never depends on a live path.** The truth is the persisted log, keyed by
   `(build, package, attempt, seq)` with the restart rule (the valid attempt for seq s is
   `max{a : from(a) ≤ s}`). Live data is a copy. The orchestrator merges all sources per
   follower into a stream without gaps or duplicates (S17 §5.4 algorithm, unchanged).
2. **Live path: pull on demand (D55).** Workers write to lake in batches (30 s, ADR 0012) and
   keep an in-memory log. Only while a user follows a build does the follower's orchestrator send
   `LiveSubscribe{job, cursors, expires}` to the job's worker. It uses the worker's own outbound
   `WorkerControlService.Attach` stream (worker token + PoP, ADR 0013). The worker answers with
   `LiveChunk` every 250 ms until the subscription expires (15 s, renewed every 5 s) or is
   cancelled. There is one subscription per (orchestrator, job), however many followers watch.
   Without followers, workers send no log data.
3. **No peering and no continuous push.** `bob.peer.v1` and `LiveLogService.PushLines` are
   dropped. Orchestrators never talk to each other, and never connect to workers. Workers attach
   to every member listed in `fleet/cfg`.
4. **Degraded mode is automatic:** an orchestrator reads the lake for a build when a follower has
   a gap for 1 s, when no live chunk arrived for 6 s, or when the job's worker is not attached to
   it. Lake reads are shared per orchestrator and build and cached by immutable object key,
   never made per follower.
5. **Discovery** uses only `fleet/cfg` (members, endpoint, SPKI pin), read by GET. There are no
   per-build location records; the job record names the owner.
6. Worker input on this path is hostile input. The orchestrator checks the job's owner and epoch,
   and applies the S17 limits (≤ 4 KiB lines, ≤ 5,000 lines and ≤ 4 MiB per chunk, 1 MiB/s per
   worker).

Exact protocol: SPEC §05.9.1 and §05.10.3.

## Evidence from S17 (`[8c/15G]`, indicative, other spikes running; push modes, kept for the merge algorithm and failover numbers)

| | fan-out | peering | lake only |
|---|---|---|---|
| 12 failure runs (kill worker target, kill client's orchestrator, worker crash + retry) | 0 gaps, 0 dups, = lake | 0 gaps, 0 dups, = lake | 0 gaps, 0 dups, = lake |
| Stall when the worker's target dies | 0.25 s | 1.74 s | 6 s |
| 20 builds × 200 lines/s, 50 followers on one build: p50 / p99 | 113 / 223 ms | 109 / 398 ms | 4.1 / 7.5 s |
| worker→orch / orch→orch bytes per minute | 108 / 0 MB | 36 / 30 MB | 0 / 0 |
| Orchestrator CPU (3 processes) | 36 % of a core | 28 % | 13 % (+ S3 reads) |

Security: 14 cases passed (worker token refused on the peer endpoint, audience replay,
expiry, foreign key, non-member issuer, redirected address vs pin, cross-build push,
oversize, flood).

## Options the owner was given (O-S17-1, superseded by D55)

- **a) Worker fan-out only** (recommended): keeps R-SRV-005/D7 literally, best latency and
  failover; costs worker egress × number of orchestrators.
- **b) Peering only** (D12 as written): lowest worker egress; adds a peer protocol, peer
  auth and orchestrator reachability; slower failover.
- **c) Both, configurable** (`live_log.mode = fanout | peer | off`): most flexible, two live
  paths to test and maintain.

## Consequences

- A live view costs worker egress only while someone follows a build; it goes to one
  orchestrator per follower orchestrator, not to all of them.
- Workers must reach every orchestrator endpoint directly (outbound only). Orchestrators need no
  route to workers.
- The first live lines appear after subscribe (≤ 1 round trip plus 250 ms). History before the
  subscription comes from the worker's buffer (unflushed lines plus the last 10,000 committed
  lines per package) or from the lake.
- A worker crash can withdraw lines already shown live; the UI shows a restart marker.
- The pull latency must be re-measured in Stage 3 (S17 measured push at 200 ms).

## Owner amendments

- **D55**: decisions 2 and 3 rewritten (pull on demand; no worker→orchestrator streaming, no
  orchestrator↔orchestrator peering). This supersedes D12 and R-LOG-002's optional direct
  orchestrator connection.
- **D60**: discovery through `fleet/cfg` by GET; the S17 `orch/` LIST is gone.
