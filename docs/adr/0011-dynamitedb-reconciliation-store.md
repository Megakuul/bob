# ADR 0011 — dynamitedb as the reconciliation store

Status: **Accepted (locked) — owner at Gate S, 2026-10-21**, 2026-10-21; decisions 3, 6 and 7 rewritten by owner decisions (see
"Owner amendments"). Evidence: `docs/spikes/s09-dynamitedb.md`, SPEC §05 verifications V1–V2.

## Context

R-SRV-004 and R-FLEET-003 need compare-and-swap state transitions, job leases, requeue of dead
workers' jobs and list views without full-bucket scans (R-STORE-004), on S3 only (R-STORE-001),
with dynamitedb as it is today (R-STORE-003, D46: HEAD `c0badfc`, 2026-08-13).

## Decision

1. **dynamitedb is fit as the reconciliation store**, with the rules below. Measured: 0 double
   claims (20 claimers, 50 same-job rounds plus 300-job drains, both backends), 0 lost jobs and 0
   accepted stale completions under worker death/pause chaos with 3 orchestrators, with and
   without a leader lease.
2. **One CAS primitive: "decide on a Get, write with that Get's ETag"** (`Get` then `Put` with
   `ETagField`, i.e. one `PUT If-Match`). `Update`+`CustomUpdate` is used only for owner-only
   renewals, because dynamitedb's `Update` always writes, even when the closure refuses.
3. **(Rewritten, D60.)** Job truth lives in `job/<jobID>` while it is not finalised; terminal
   states are absorbing and are summarised into the build record, then the job record is deleted
   with its ETag. Dispatch uses a **sequence log per class**: dense slots `qs/<class>/s/<seq20>`
   created with If-None-Match, plus tail/head hint records `qt/<class>`, `qh/<class>`. An idle
   worker polls with one GET of the next slot number (404 = nothing new). The job record is written
   before its slot.
4. **Leases are fenced by an epoch** that increments on every claim. All worker writes check
   owner + epoch. **(Amended.)** Liveness of a claimed job comes from the owner's heartbeat
   `whb/<wid>` (one owner-only PUT per worker per 15 s), not from per-job renewals. Duplicate execution after a pause is allowed (R-FLEET-003); a stale result is
   never accepted.
5. **No coordinator. Each controller takes an optional leader lease `ctl/<controller>`** (CAS,
   TTL 3× interval). Correctness does not depend on it (measured: no-leader runs are also
   correct); it cuts reconcile cost by the number of orchestrators and removes CAS conflicts
   between them.
6. **(Rewritten, D60.) No LIST on the control and fleet buckets and no `dynamitedb.Query`.**
   bob's store package wraps dynamitedb (`recover` on every call) and offers only GET-based
   primitives: `Get`, `GetCached` (conditional GET, 304 verified on RustFS), `Create`, `CAS`
   (Get + Put If-Match), `PutOwned`, `Delete`, `SeqLog`, `PagedIndex` (lists from page documents
   numbered by counters), `ShardSet` (membership documents for reconcilers) and `Lease`. Index
   entries are written intent-first and repaired by a controller. Exact API and key schema:
   SPEC §05.3–§05.5.
7. **Reference S3 backend for tests: RustFS** (upstream 1.0.3 binary). **Only AWS S3 and fully
   AWS-compatible servers with conditional writes are supported** (D63); SeaweedFS and Garage
   are not. bob-server runs a conformance probe at start and refuses unsupported backends.
8. **Schema rule:** a pk name is used either by pk-only types or by pk+sk types, never both.
9. **Every key segment from outside input** is hashed or encoded to `[A-Za-z0-9@._-]{1,100}`;
   empty segments are rejected by bob (dynamitedb accepts `Key("")`).

## Consequences

- (Amended.) Each list page costs 1–3 GETs of page/index documents; filters run client-side on
  loaded pages or use dedicated index documents (running, failed, per branch).
- (Amended.) Idle polling costs 1 GET per class per 30 s per worker (D54 slow polling plus
  wake-up); 100 idle workers ≈ 15 req/s, of which LIST 0 (was 27 req/s with LIST).
- dynamitedb bugs found are listed in the write-up for the owner; none blocks bob with the
  wrapper and rules above.

## Alternatives considered

- `Update`+`CustomUpdate` as the general CAS: 2 requests too, but refused transitions still
  rewrite the object (ETag churn, conflicts for the lease owner).
- `Get` + `Update(ETag)`: 3 requests, slower (claim p50 228 ms vs 90 ms on RustFS under load).
- Lease per worker heartbeat instead of per job: fewer renewals, but needs cross-object
  reasoning for claim-then-crash; rejected for simplicity.

## Owner amendments

- **D60** (GET-only): decisions 3 and 6 rewritten; the LIST-based key schema of S9 §5.2/§5.3 is
  replaced by SPEC §05.4.
- **D54** (slow polling + wake-up): dispatch via sequence log + `Wake{class, slot}` over the
  worker's Attach stream; claims stay CAS.
- **D63** (AWS-compatible only): decision 7.
- **D64** (no dynamitedb changes): bob uses dynamitedb HEAD unchanged; S9's B1–B13 are robustness
  notes; B1/B3/B4 are moot because bob never calls `Query`.
- Agent amendment (SPEC §05.6.1): heartbeat-based job liveness (decision 4) to remove per-job
  renewal PUTs.
