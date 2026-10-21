# S9 — dynamitedb as bob's reconciliation store

## 1. Question and requirements

Can dynamitedb (HEAD `c0badfc867482196181a4cb8e66020b6a82a2f6e`, 2026-08-13, D46) hold bob's
desired/observed state: CAS job claims, leases, requeue, several orchestrators without a
coordinator, every UI list as a query, affordable polling, and no crash on panics or S3 errors?

Requirements: R-SRV-003/004/005, R-STORE-001…006, R-FLEET-001…004, R-SEC-001/003, R-UI-002/006,
R-API-002, R-AUTH-002/003/005, R-NFR-004/005/007/008. Decisions: D7, D8, D13, D38, D46.

**Short answer: yes, with a thin bob wrapper and five schema/usage rules (ADR 0011).** Zero
double claims, zero lost jobs, zero accepted stale results in all runs. dynamitedb has 13 bugs or
gaps (section 3.7). Three of them matter for bob, and the wrapper works around them locally. One
needs the owner's attention upstream: panics inside `Query` crash the whole process.

## 2. What was built

All in `spikes/s09-dynamitedb/` (module `bobspike/s09`, `replace` to `./vendor-src`):

| Path | Content |
|---|---|
| `vendor-src/` | dynamitedb clone, unmodified |
| `internal/store/store.go` | `store.Call` (recover → `ErrPanic`), typed wrappers, HTTP transport that counts requests by class and injects faults (500, 503, reset, lost response, stripped ETag, slow) |
| `internal/store/list.go` | `ListKeys`/`ListObjects`/`List`: raw `ListObjectsV2` + `dynamitedb.Get` per key in goroutines bob owns (replacement for `Query`) |
| `internal/model/model.go` | proposed key schema (fleet + control), time-sortable and reverse ids |
| `internal/fleet/fleet.go` | enqueue, claim (3 CAS modes), renew, complete, reconcile/requeue, leader lease |
| `cmd/s09/` | experiments: `probe panics faults oplat stress chaos poll lists` |
| `cmd/dbg/` | small backend checks (read-during-write, directory markers, file/dir clash) |
| `start-seaweed.sh`, `start-rustfs.sh`, `s3.json` | backends on ports 20900 / 20950 |
| `measure.sh` | reruns everything, writes `results/*.txt` |
| `results/` | raw outputs quoted below |

Backends: **SeaweedFS 4.36** (nixpkgs 26.05 pin) and **RustFS 1.0.3** (upstream musl release,
sha256 `28e973c6…1f06747`). MinIO was not needed.

## 3. Results

All numbers `[8c/15G]` unless marked. The host was heavily loaded by other spikes (load average
17–33), so latencies and throughput are **indicative only**. Request counts and correctness
results do not depend on load. Rerun with `measure.sh` on a quiet host.

### 3.1 Backend conditional-write support (`results/probe-*.txt`)

| Check | SeaweedFS 4.36 | RustFS 1.0.3 |
|---|---|---|
| PUT If-None-Match `*` (Create twice) | pass | pass |
| PUT / Update / Delete with a stale ETag rejected | pass | pass |
| Backend honours GET If-Match and DELETE If-Match | pass | pass |
| Raw PUT If-Match race, 20 writers × 100 rounds: exactly 1 winner | 100/100 | 100/100 |
| Raw PUT If-None-Match race, 20 × 100 | 100/100 | 100/100 |
| Raw DELETE If-Match race, 20 × 50: exactly 1 success | 50/50 | 50/50 |
| Read during overwrite, 10 s, 8 readers: torn or empty bodies | 0 / 13 051 reads | 0 / 2 368 reads |
| LIST after every object under a prefix is deleted | **returns `q/p/j/` as an object** | empty |
| GET of a "directory" key (`q/p/j/`) | **200, 0 bytes** | 404 |
| PUT `b/y` after `b/y/s/1` exists | **500** | ok |
| PUT If-Match on a missing key | 412 | 404 (AWS behaviour) |

SeaweedFS breaks dynamitedb: `Query` on a partition whose items were all deleted fails with
`EOF`, because it GETs the directory marker and CBOR-decodes 0 bytes. Job queue partitions empty
all the time, so this failed **57 732 claims** in a 200-job drain before I found it. RustFS has
none of these problems. **RustFS is the reference test backend** (R-STORE-006).

### 3.2 Exactly-once claim (`results/stress-*.txt`)

20 claimers, each with its own S3 client. Phase A: one job, all 20 claimers released at once,
repeated. Phase B: 300 jobs drained by 20 claimers. Every claimer completes its job.

| Backend, CAS mode | Same-job rounds: rounds with exactly 1 winner | Drain: double claims | Requests per job (claim + complete) | Drain rate |
|---|---|---|---|---|
| SeaweedFS, Get+Put(ETag), keys listing | 50/50 | 0 / 300 | 12.4 | 173/s |
| RustFS, Get+Put(ETag), keys listing | 50/50 | 0 / 300 | 12.0 | 32/s |
| RustFS, Update+CustomUpdate | 20/20 | 0 / 300 | 12.0 | 33/s |
| RustFS, Get+Update(ETag) | 20/20 | 0 / 300 | 12.8 | 23/s |
| RustFS, dynamitedb.Query hints, pick 5 | 20/20 | 0 / 300 | 48.5 | 7/s |
| RustFS, dynamitedb.Query hints, pick 50 | 20/20 | 0 / 300 | 179.7 | 11/s |
| RustFS, Get+Put, client `[2c/4G]` | 20/20 | 0 / 300 | 12.0 | 31/s |

Winner claim latency p50: 28 ms (SeaweedFS) / 90 ms (RustFS) for Get+Put, 228 ms for
Get+Update(ETag). With `dynamitedb.Query` hint listing, **1 198 claims failed outright**. The
cause is `Query` failing as a whole when a hint is deleted between its LIST and GET (bug B4).
The keys-only listing has neither problem and costs 1 request instead of 1+N.

### 3.3 Leases, requeue and several orchestrators (`results/chaos-*.txt`)

There are 300 jobs, 20 workers, a lease TTL of 3 s (renewed every TTL/4), 3 orchestrators that
reconcile every 1 s, and a clock-skew allowance of 0.5 s. In 20 % of jobs the worker either dies
(a new identity replaces it) or pauses for 2×TTL+1 s and then tries to complete. A pause stands
in for a GC stall or a network partition.

| Run | Requeues | Lost jobs | Accepted stale completions | Stale completions rejected | Overlapping executions | Reconciler CAS conflicts | Reconciler requests |
|---|---|---|---|---|---|---|---|
| RustFS, no leader | 71 | **0** | **0** | 29 | 20 | 140 | 3 × 131 req/s |
| RustFS, leader lease | 78 | **0** | **0** | 36 | 25 | 0 | 128 + 2 + 2 req/s |
| SeaweedFS, no leader | 65 | **0** | **0** | 28 | 17 | 134 | (slower run, 3 min) |
| SeaweedFS, leader lease | 86 | **0** | **0** | 48 | 28 | 2 | |

What goes wrong when several orchestrators run at once with no leader: nothing incorrect. Every
transition is a fenced CAS, so the only effects are (a) every orchestrator pays the full
reconcile cost, ×3 here, and (b) CAS conflicts between them, all of them harmless retries.
"Overlapping executions" are paused workers that keep running after their lease was requeued.
R-FLEET-003 allows this, and the epoch fence stops their result from being accepted. The leader
lease costs 1 GET + 1 PUT per standby orchestrator per interval.

The reconciler lists the live partition and GETs every live job on each pass (1 LIST + N GETs).
That is why it does about 128 req/s at 1 s intervals while 100+ jobs are live. This is the
largest steady cost in the design; see 3.5.

### 3.4 UI lists (`results/lists-*.txt`)

Page size 50. Each list is one prefix LIST plus one GET per item. Page 2 continues with
StartAfter set to the last key. There are no scans.

| UI list (R-UI-002) | Key prefix | Page 1 requests |
|---|---|---|
| projects | `project/` | 1 LIST + 21 GET |
| builds of project, newest first | `build/<project>/n/` | 1 + 50 (page 2: 1 + 50) |
| running builds (overview) | `buildidx/running/b/` | 1 + 10 |
| recent failures (overview) | `buildidx/failed/b/` | 1 + 30 |
| releases of project | `release/<project>/v/` | 1 + 40 |
| jobs of a build (finished) | `jobs/<build>/j/` | 1 + 40 |
| live jobs per platform | `live/<platform>/j/` | 1 + 50 |
| queue depth per platform | `jobq/<platform>/j/` | **1 LIST, 0 GET** (keys only) |
| workers | `worker/` | 1 + 50 |
| users / roles | `user/`, `role/` | 1 + 50 / 1 + 10 |
| roles of a user / users of a role | `userrole/<u>/r/`, `roleuser/<r>/u/` | 1 + 10 / 1 + 30 |

Findings:
- Filters are client-side. `Query(State=failed)` over 300 builds with no match cost 1 LIST +
  **300 GET**. Every filter the UI offers needs its own partition (as `buildidx/failed`) or a
  bounded partition.
- Ordering is ascending only, so newest-first lists use reverse keys (`9999999999-n`, or a ULID
  with inverted time bits).
- Build-number allocation with a CAS counter on `project/<id>`: 100 numbers from 20 concurrent
  allocators, 0 duplicates, 955 conflict retries, 1.0 s. That is fine at forge-event rates.
- An early schema with `project/p01` (pk-only) and `project/p01/build/…` (pk+sk) under the same
  pk name broke on SeaweedFS (3.1, file/dir clash). It also costs an extra LIST on any backend,
  because common prefixes count against MaxKeys. Hence the rule "one pk name, one shape".

### 3.5 Polling cost and pickup latency (`results/poll-*.txt`)

100 workers run in-process. Each polls one platform queue with jitter (0.8–1.2 × interval) and
re-polls at once after finishing a job. One job is enqueued per second. AWS price equivalent:
LIST/PUT $0.005 per 1k, GET $0.0004 per 1k, 30-day month.

| Backend | Empty-queue interval | Requests/s | AWS-priced $/month | Enqueue→claim p50 / p99 |
|---|---|---|---|---|
| RustFS | 2 s | 59.6 | 669 | 91 ms / 249 ms |
| RustFS | 5 s | 27.2 | 276 | 139 ms / 289 ms |
| RustFS | 10 s | 14.7 | 140 | 167 ms / 509 ms |
| SeaweedFS | 5 s | 27.2 | 282 | 55 ms / 277 ms |

Pickup latency is low because 100 independent pollers together sample the queue every
interval/100. With 1 worker, the expected latency is interval/2.

Cost model per platform with W workers, poll interval I, R running jobs and O orchestrators
**[proposal for SPEC budgets]**:

- worker polling: W/I LIST/s (dominant on AWS: 100 workers at 5 s = 20 LIST/s ≈ $260/month);
- lease renewal: 2·R/(TTL/3) req/s (GET + PUT; 100 jobs, TTL 30 s → 20 req/s ≈ $108/month);
- reconcile: (1 + R)/interval per leader (100 jobs, every 10 s → 10 GET/s ≈ $10/month);
- per job: about 12 requests.

On self-hosted RustFS/SeaweedFS these costs are only CPU. On AWS-priced S3, worker polling and
renewals dominate. That is an owner decision (section 6).

### 3.6 Panics and S3 errors (`results/panics-*.txt`, `results/faults-*.txt`)

Panics found, with all calls wrapped by `store.Call`:

| Case | Result |
|---|---|
| update operator in a Get/Query (exact key) filter; filter operator in Update/Create | panic, **recovered** |
| non-struct `T`; nil model | panic, recovered |
| `Value()` on a filter/update operator (user code) | panic, recovered |
| backend/proxy omits the `ETag` header on GET (`*resp.ETag`, get.go:50) | nil-pointer panic, recovered |
| **prefix `Query` with an update operator in the filter** | **process exits (code 2), not recoverable** |
| **prefix `Query` when the backend omits `ETag`** | **process exits (code 2), not recoverable** |
| Update when the backend omits `ETag` | no panic, but **PUT without If-Match** (silent lost update) |

The two crashes happen inside `errgroup` goroutines that `Query` starts (get.go:107). x/sync
v0.20.0 errgroup deliberately does not propagate panics. **R-STORE-005 cannot be met with
`dynamitedb.Query`.** bob's `store.List` avoids it.

S3 errors (same on both backends):

| Fault | dynamitedb result |
|---|---|
| 2 × HTTP 500, then success | ok (SDK standard retryer, 3 attempts) |
| persistent 500 / 503 / connection reset / endpoint down | opaque error after about 3.5–4.5 s; no sentinel |
| context deadline 500 ms on a slow backend | error after 502 ms, but `errors.Is(err, context.DeadlineExceeded)` = **false** |
| Create, response lost after the server wrote | `ErrAlreadyExists`, although our write succeeded |
| Update, response lost after the server wrote | `ErrConcurrencyConflict`, although our write succeeded |
| Claim, PUT response lost | fleet re-reads after a conflict and finds itself owner with its epoch: **claimed** (ambiguousWins = 1) |

Rule **[proposal]**: on `ErrConcurrencyConflict` or `ErrAlreadyExists`, re-read and check
whether the stored state is the one we tried to write (owner + epoch, or a request id) before
treating it as a loss.

### 3.7 dynamitedb bugs and missing features (for the owner; nothing modified)

| # | Where | Problem | Evidence | Severity for bob | Workaround in spike |
|---|---|---|---|---|---|
| B1 | get.go:107 `scanGroup.Go` | Panics in `Query` worker goroutines (misuse, or nil ETag) crash the process; caller `recover` cannot catch them | panics child exit 2 | high | `store.List` |
| B2 | get.go:50, :127; update.go:68 | `*resp.ETag` dereferenced without a nil check; in Update a nil ETag makes the PUT unconditional | panics test | high (silent lost update) | none needed on RustFS/SeaweedFS; check in wrapper |
| B3 | get.go:91–97 | `WithLimit` stops GETs but the paginator keeps LISTing every remaining page; no `MaxKeys` | limit 5 on 2 500 items: 3 LIST | medium | `ListKeys` with MaxKeys |
| B4 | get.go:107–113 | Object deleted between LIST and GET (`NoSuchKey`) fails the whole Query | 10/20 queries failed under churn; 1 198 failed claims | high | skip 404 in `store.List` |
| B5 | get.go:71 | Query on an exact key with a filter mismatch returns `[nil], ErrFilterMismatch` (prefix query returns `[]`, nil) | probe | low | — |
| B6 | data_filter.go:321 | `LessOrEqThan`: `case reflect.Int \| reflect.Int64` is a bitwise OR (= Int64), so plain `int` fields never match | `N=5 LessOrEqThan(5)` → mismatch | medium | use `LessThan(n+1)` |
| B7 | data_filter.go:267/285/303 | `GreaterThan`/`GreaterOrEqThan`/`LessThan` handle `Int` only, so `time.Duration` (Int64) never matches although the constraint allows it | probe | low | — |
| B8 | create.go:26, put.go:26 | Static (non-DataField) fields are not copied by `applyUpdate`, so they are always stored as zero; README says they are allowed | probe `static=""` | medium (silent data loss) | use DataFields only |
| B9 | update.go:19 | Doc says "Update will modify v to represent the final state"; it does not, and no new ETag is returned by Update/Put/Create | probe | medium (an extra GET per follow-up CAS) | re-Get |
| B10 | option.go `WithExpires` | Sets the HTTP `Expires` header; objects are not deleted (S3 lifecycle cannot act on it) | object readable after expiry, both backends | medium (misleading) | bob GC controller |
| B11 | all ops: `errors.New(err.Error())` (9 places) | Wrapping is lost: `context.Canceled`/`DeadlineExceeded` not detectable, nor 5xx vs 4xx | probe/faults | medium | none; check `ctx.Err()` after a failed call |
| B12 | dynamitedb.go:76 | Sanitizer allows empty segments (`{0,100}`), so `Get(Key(""))` reads the "directory" on SeaweedFS (EOF) | probe | low | bob rejects empty keys |
| B13 | put.go | `Put` with ETag on a missing key returns a raw 404 error on RustFS/AWS instead of a sentinel | probe | low | treat as not found |

Missing features that would simplify bob (no upstream request is needed to proceed):
- **F1** conditional Update: a filter plus update in one call that aborts without writing on
  mismatch. Today a refused `CustomUpdate` still PUTs.
- **F2** keys-only listing with a page cursor (`QueryKeys`, `Next`). It would remove the raw-S3
  dependency in `store.List`.
- **F3** return the new ETag (and object) from Create/Put/Update.
- **F4** bounded GET concurrency in Query (today one goroutine per listed key, up to 1 000).
- **F5** skip keys ending in `/` in Query (SeaweedFS directory markers).

## 4. Answer

dynamitedb works as bob's reconciliation store for both the control and fleet buckets, under
these conditions (ADR 0011):
1. Every state change is "Get, then Put with that ETag". CustomUpdate is only for owner-only
   renewals.
2. Live job truth is per platform. Terminal states are absorbing and moved to history. Queue
   hints are written before and deleted after. Every claim has an epoch fence.
3. bob wraps every call with `recover`, and lists with raw `ListObjectsV2` plus `dynamitedb.Get`,
   never with `dynamitedb.Query`.
4. RustFS is the reference backend. SeaweedFS is not supported until B12/F5 are handled, or
   SeaweedFS changes its directory behaviour.
5. Orchestrators use an optional leader lease per controller, for cost only.

## 5. SPEC input [proposal]

### 5.1 Key rules
- Key = `<pk>/<pkval>` or `<pk>/<pkval>/<sk>/<skval>`. Each value matches `^[A-Za-z0-9@._-]{1,100}$`
  (bob rejects empty values).
- A pk name is used either by pk-only types or by pk+sk types, never both.
- Ids: 26-char Crockford ULID (`NewID`); newest first via `NewRevID` (inverted 48-bit ms time) or
  `RevNum(n)` = `%010d` of `9999999999-n`.
- External strings in keys (OIDC subject, usernames with `+`, image names with `/`): use
  `hex(sha256(s))[:32]`, or lowercase base32 (no padding) when the value must be reversible
  (≤ 62 bytes).
- Field CBOR names are short (1–3 chars), and every field is `DataField` with `omitempty`. No
  static fields (B8).

### 5.2 Fleet bucket (orchestrators + workers; no personal data, R-SEC-003)

| Type | Key | Fields | Written by |
|---|---|---|---|
| WorkerReg | `workerreg/<workerID>` | pubkey, platform, caps, labels, created; `token` encrypted to pubkey | worker creates (If-None-Match); orchestrator writes token |
| Worker | `worker/<workerID>` | platform, caps, labels, state (pending/active/draining/gone), heartbeat | orchestrator (state); worker (heartbeat) |
| Orchestrator | `orch/<orchID>` | endpoint URL, heartbeat | each orchestrator |
| Controller lease | `ctl/<controller>` | lease {owner, epoch, expires} | orchestrators |
| LiveJob | `live/<platform>/j/<jobID>` | lease {state, owner, epoch, expires, attempts, changed}, buildID, projectID, needs, created | orchestrator creates; workers claim/renew/complete; reconciler requeues/finalises |
| QueueHint | `jobq/<platform>/j/<jobID>` | buildID, needs, since (the body changes on every rewrite, so the ETag changes) | orchestrator/reconciler; claimers delete If-Match the listed ETag |
| JobRecord | `jobs/<buildID>/j/<jobID>` | final lease, platform, result summary, log pointer (lake) | whoever finalises (idempotent Put) |

If a capability split per platform is needed, use the partition value
`<platform>.<capsHash>`. Workers discover non-empty classes with one LIST on `jobq/` with
Delimiter `/`.

### 5.3 Control bucket (orchestrators only)

| Type | Key |
|---|---|
| Project (+ `nextBuild` counter) | `project/<projectID>` |
| Build | `build/<projectID>/n/<RevNum(number)>` (state, branch, commit, trigger, created) |
| Build index | `buildidx/running/b/<RevID>`, `buildidx/failed/b/<RevID>` (pruned to N or T), `buildidx/branch.<project>.<branchHash>/b/<RevNum>` if branch filter is needed |
| Release | `release/<projectID>/v/<sortableVersion>` (`v%04d.%04d.%04d[-pre]`) |
| Release index (registry page) | `releaseidx/all/r/<RevID>` |
| OCI image / tag | `image/<projectID>/i/<nameHash>`, `imagetag/<projectID.nameHash>/t/<tag>` |
| User | `user/<userID>` |
| Username → user (unique via Create) | `username/<nameOrHash>` |
| OIDC subject → user | `oidcsub/<hash(issuer\|sub)>` |
| Role (permissions inline, bounded) | `role/<roleID>`; `admin` is created at bootstrap and protected |
| Role bindings | `userrole/<userID>/r/<roleID>` and `roleuser/<roleID>/u/<userID>` (both written; reconciler repairs one-sided pairs) |
| OIDC mapping | `oidcmap/<mappingID>` |
| Service account | `sa/<saID>`; tokens `token/<hex sha256(token)>` (exact Get on auth) and `usertoken/<owner>/t/<tokenID>` for listing |
| Forge integration | `forge/<forgeID>` |
| Settings | `setting/<name>` |

### 5.4 State machines

Job (`live/…lease.state`):
```
queued --claim(CAS state=queued; epoch+1, owner=W, expires=now+TTL)--> claimed
claimed --start(owner,epoch)--> running
claimed|running --renew(owner,epoch; expires=now+TTL)--> same
claimed|running --complete(owner,epoch)--> succeeded|failed            (absorbing)
queued|claimed|running --cancel(orchestrator, CAS)--> cancelled        (absorbing; worker sees it on next renew)
claimed|running --requeue(reconciler, CAS on listed ETag; expires+skew < now)--> queued (owner="")
terminal --finalise(any: Put jobs/…, Delete live If-Match)--> (gone from live)
```
Order of writes: requeue = Put hint, CAS live, Put hint again. Enqueue = Put hint, Create live.
Claim = CAS live, Delete hint If-Match listed ETag.

Build (`build/…state`, derived by the build controller from its jobs):
`pending → evaluating → queued → running → succeeded | failed | cancelled`, plus `superseded`
(concurrency rule, R-PIPE-005). Every transition is a CAS on the build record. Retry creates a
new build number; it never reopens a terminal build.

Worker lease (`worker/<id>`): `pending → active` (orchestrator, after verifying the
registration), heartbeat every 10 s, `active → gone` when `heartbeat + 3×10 s < now`.
`draining` is set by an admin. A gone worker's jobs are requeued by their own job leases,
not via the worker record.

Controller leader (`ctl/<name>`): Create if absent; otherwise take over when `expires < now`
(epoch+1) or renew if owner == me. TTL = 3 × the controller interval.

### 5.5 Conditional-write rules
1. Every write of shared state uses the ETag of the read it decided on (`Put` with
   `ETagField`), or `Create` for new keys.
2. `Update`+`CustomUpdate` only where the writer is the sole legitimate writer (lease renewal).
3. On `ErrConcurrencyConflict`/`ErrAlreadyExists`: re-read, and treat it as success if the
   stored state carries our owner + epoch (or request id).
4. Never delete a hint or index entry without the ETag from the LIST or Get it was decided on.
5. Terminal states are absorbing; history copies are idempotent Puts.
6. All clock comparisons use a skew allowance (0.5 s in tests; 5 s **[proposal]** in production).

### 5.6 Intervals and budgets [proposal]
| Item | Value |
|---|---|
| Job lease TTL / renew | 30 s / every 10 s |
| Worker heartbeat | 10 s; gone after 30 s |
| Empty-queue worker poll | 5 s ± 20 % jitter; immediate re-poll after a job |
| Reconcile (requeue/finalise) | every 10 s, leader only; leader lease TTL 30 s |
| Hint candidate set | first 50 keys, random pick |
| List page | 50 items = 1 LIST + 50 GET |
| Budget: job pickup latency (R-NFR-004) | p99 ≤ 1 s with ≥ 10 idle workers polling at 5 s; ≤ interval + 0.5 s for one worker |
| Budget: requests per job | ≤ 15 |
| Budget: steady S3 rate, 100 workers, 100 running jobs | ≤ 50 req/s (measured parts: 27 poll + 20 renew + 1–10 reconcile) |

### 5.7 Error texts [proposal]
- `store: dynamitedb panicked: <value>` (wrapped `ErrPanic`, logged with stack, request fails with 500)
- `fleet: no claimable job`
- `fleet: lease lost (job requeued or taken by another worker)`
- `store: key segment invalid: must match [A-Za-z0-9@._-]{1,100}`

## 6. Decisions, risks, open items

### Agent decisions
| # | Decision | Reason |
|---|---|---|
| S9-1 | RustFS 1.0.3 is the reference test backend; SeaweedFS not supported for dynamitedb | Measured SeaweedFS directory-marker and file/dir bugs break Query/Create (principle 1) |
| S9-2 | bob lists with raw `ListObjectsV2` + `dynamitedb.Get` instead of `Query` | `Query` crashes the process on panics (R-STORE-005) and fails on concurrent deletes |
| S9-3 | CAS = Get + Put(ETag) | 2 requests, never writes on refusal; fastest measured |
| S9-4 | Builds, build indexes and releases live in the control bucket; jobs only carry ids | R-SEC-003: commit authors/names must not be in fleet |
| S9-5 | Job leases per job (not per worker heartbeat) | Simple, no cross-object reasoning |
| S9-6 | Leader lease per controller, optional for correctness | Cuts cost ×O, no conflicts; principle 6 (no coordinator) kept |

### Owner decision needed
**O-S9-1 Job dispatch path** (affects AWS-priced S3 cost; architecture):
- (a) Workers poll the fleet bucket directly (measured: 100 workers at 5 s = 27 req/s,
  ≈ $276/month on AWS list prices; $0 extra on self-hosted S3). Simplest; no orchestrator is
  needed to pick up work.
- (b) Workers long-poll an orchestrator RPC. The orchestrator lists `jobq/` once per platform
  per second and wakes workers, and workers still claim by CAS in the fleet bucket. Cost drops
  to about O×P LIST/s; adds an RPC.
- (c) (a) by default, (b) as an optimisation used when an orchestrator is reachable.
- **Recommendation: (c)**. Correctness stays in the bucket; cost is configurable.

**O-S9-2 Upstream fixes in dynamitedb** (owner-owned; R-STORE-003 allows them):
- (a) Fix B1, B2, B4 (crash and lost-update class) now, as small separate PRs.
- (b) Also B3, B6–B11 and features F1–F3.
- (c) No upstream changes; bob keeps the local workarounds.
- **Recommendation: (a)**, and later (b) for F2/F3. bob does not block on either.

### Open risks
- `store.List` depends on dynamitedb's key layout. The README documents it, but it is not a typed
  API, so a test must pin it.
- Clock skew between workers and orchestrators is not tested beyond the 0.5 s allowance.
- Reconcile cost grows with live jobs (1 GET each per pass); above about 1 000 live jobs per
  platform, shard by `live/<platform>.<shard>`.
- Ceph RGW and AWS S3 were not tested (no access); AWS conditional semantics were inferred
  from the RustFS 404 behaviour only.
- Latency and throughput numbers were taken on a loaded host.

### Still to verify
- Quiet-host rerun of `measure.sh` (the `[2c/4G]` run only limited the client; the S3 server was not limited).
- Lease renewal under S3 latency spikes (p99 PUT 128 ms on RustFS, under load): TTL margin.
- RustFS multi-node (erasure set) conditional-write races; only single-node was tested.
