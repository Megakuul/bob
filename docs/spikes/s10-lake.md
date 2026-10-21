# S10: build logs and analytics in lake

## 1. Question and requirements

Can lake (`github.com/megakuul/lake`, HEAD `1c410ce`, 2026-10-19, D46) store bob's build
logs and analytics: periodic small Parquet writes from workers, compaction later, queries for
the UI/CLI, retention?
Requirements: R-STORE-001/002/003/006/007, R-LOG-001/003 (R-LOG-002 is S17), R-UI-002 (build
log, analytics), R-NFR-004/005/007/008, D8, D20, D38, D46.

**Short answer: not with lake HEAD as it is.** Lake HEAD loses data silently once
several writers commit at the same time, hangs on normal queries over sorted tables,
duplicates rows when compactors race, and breaks readers after a compaction. With small
fixes (a local patch, P1–P7, about 130 added lines) every test kept every line exactly once
and the query latencies are fine for the UI. The single catalog object limits writes to
about 8-10 commits/s for the whole bucket. That is the main architectural limit.

## 2. What was built

`spikes/s10-lake/` (module `bobspike/s10`):

| Path | What |
|---|---|
| `vendor-src/lake` | lake HEAD, unchanged (MIT). |
| `vendor-src/lake-patched`, `lake-s10.patch` | HEAD + fixes P1–P7 (section 3.7). Local only, not upstreamed. |
| `go.mod` / `go.patched.mod` | same code against HEAD or the patch (`-tags lakepatched -modfile go.patched.mod`). |
| `logstore/` | schema (`LogLine`, `PackageRun`), counting S3 transport, generators, retention workaround. |
| `cmd/s10/` | experiments: `probe`, `repro-hang`, `repro-lost`, `cas`, `compact-race`, `ingest`, `bulk`, `qbench`, `retention`. |
| `start-seaweed.sh`, `start-rustfs.sh` | SeaweedFS 4.36 (nixpkgs pin, S3 :21000), RustFS 1.0.3 (upstream binary, :21050). |
| `measure.sh` | reruns everything; results to `tmp/out/`. |
| `tmp/*.log` | raw output of every run quoted below. |

Simulation (`ingest`): W workers, each with its own `lake.Bucket` and one `Ingestor`, 2 package
builds at a time, 2,000 lines per package over 10–40 s, a flusher that commits every F
seconds (or at 5,000 buffered lines) and keeps failed flushes in memory; 2 orchestrators
running `Compactor.Compact` every 15 s (30 s in the long run); 4 followers tailing a running
package with a fresh catalog per 2 s poll; 2 UI readers with a long-lived `Bucket` reading
finished packages. At the end every `(build, package, seq)` is checked for exactly-once.

## 3. Results

All numbers `[8c/15G]`, SeaweedFS unless noted, other spikes running (indicative).
`[2c/4G]` limits only the query client, not the S3 server.

### 3.1 lake's real API (from code; the README examples are empty)

- `lake.New(ctx, url, bucket, opts...)` / `lake.NewFromClient(ctx, *s3.Client, bucket)`; loads
  or creates the catalog object `lakedb.catalog` at the bucket root.
- Table = Go struct with a `lake.Table` marker field, tag `name:"…" sort:"col:asc,…"`. Columns
  that lake filters/groups/aggregates must be `lake.String`, `lake.Int`, `lake.Float`
  (a group `{data}`). Plain Go fields are stored but cannot be filtered.
- Write: `NewIngestor[T](b, WithAutoCommit, WithMaxBacklogSize)`, `Insert(ctx, rows...)`,
  `Commit(ctx)`. One commit = one Parquet object `table/<uuid>.parquet` (PUT If-None-Match)
  + one catalog rewrite (PUT If-Match ETag), up to 5 attempts.
- Compact: `NewCompactor[T](b, WithCompactionSize(32 MB default)).Compact(ctx)`: merges
  **all** shards smaller than the size into ≥ size shards, commits, deletes the inputs.
- Read: `Query[T]().Where(T{Col: lake.FilterString(lake.Eq(x))}).GroupBy(...).Aggregate(...).Limit(n).Scan(ctx, b)`.
  Filters: `Eq, In, Gt, Gte, Lt, Lte, Before, After`. Groupers: `Exact, Round, Floor, Fold,
  Prefix, Suffix, Date(DateDay…)`. Aggregators: `Sum, Count, Min, Max, Avg`.
  Shard pruning uses per-shard min/max ranges stored in the catalog.
- No delete, no drop table, no retention, no catalog refresh call, no key prefix.

### 3.2 File size (probe, 2,000 lines, 47.7 B raw per line)

| Schema | File | B/line |
|---|---|---|
| all columns lake types (no compression possible) | 388.9 KB | 194 |
| filter columns lake types, others plain with `dict,zstd` | 136.6 KB | 68 |
| 100M-line bulk set, same schema | 6.69 GB for 4.80 GB raw | 66.9 |

`parquet:"line,zstd"` on a `lake.String` panics in parquet-go ("cannot add compression codec
to a non-leaf node"); lake writes everything `UNCOMPRESSED`. The three lake columns
(`build_id`, `package`, `seq`) are 54 of the 68 bytes per row.

### 3.3 Correctness (deterministic repros)

| Test | lake HEAD | patched |
|---|---|---|
| `repro-lost`: every catalog PUT of a writer gets 412 | `Commit()` returns **nil**, 0 rows visible, orphan object | `optimistic lock failure` error, rows stay in backlog |
| `repro-hang`: 2 sorted shards, overlapping keys, `Where(package=…)` | **hangs forever** (100 % CPU) | 6 correct rows |
| same, with the hang fixed only (sequential read) | — | **wrong rows** (`19 1 4 12 15 18`), fixed by P4b |
| `cas`: 16×50 If-Match increments | SeaweedFS 800/800, RustFS 80/80: both servers are atomic | |
| `compact-race` A: 2 orchestrators compact at once (10 rounds, RustFS) | one ok, one lock failure; 0 dup | one ok, one `ErrShardsGone`; 0 dup |
| `compact-race` B: B's Bucket reloads the catalog during its compaction | **10,000 of 10,000 rows duplicated** | B refused, 0 dup |
| `compact-race` C: reader with cached catalog after compaction | `NoSuchKey` for **60 s** | recovers at once |

### 3.4 Periodic small writes (60 s runs; HEAD = `tmp/ingest-*.log`, patched = `tmp/ingestP-*.log`)

| Workers × flush | commits/s wanted | HEAD lines lost | patched lost | patched commit p50 / p99 | patched failed commits (retried) | follow lag p50 / p99 |
|---|---|---|---|---|---|---|
| 4 × 2 s (30 s) | 2 | 0 % | — | — | — | — |
| 10 × 2 s | 5 | **36.8 %** | 0 | 407 / 1,148 ms | 0 | 2.4 / 2.7 s |
| 25 × 2 s | 12.5 | **66.3 %** | 0 | 811 / 10,919 ms | 16 | 2.8 / 5.6 s |
| 50 × 2 s | 25 | **76.3 %** | 0 | 2,065 / 16,531 ms | 126 | 2.7 / 6.4 s |
| 25 × 5 s | 5 | **61.2 %** | 0 | 559 / 2,401 ms | 0 | 1.7 / 2.3 s |
| 25 × 5 s, 300 s, compact 30 s | 5 | — | 0 of 1,166,766 | 520 / 2,006 ms | 0 | 1.9 / 2.7 s |

- Successful catalog writes top out at **9.5/s** (25 workers) and **7.9/s** (50 workers): one
  object, CAS, read-modify-write of the whole catalog. More writers make it slower, not faster.
- S3 requests per flush: uncontended 2 PUT (data + catalog). Every worker's catalog is stale
  after any other commit, so in practice each flush = 1 data PUT + ~9 catalog GET + ~9 catalog
  PUT (long run: 1,524 commits, 12,297 catalog GET, 12,272 rejected PUT).
- Write amplification in the long run (56 MB raw lines, 80 MB final objects):
  worker data PUT 86 MB; catalog traffic **985 MB up + 864 MB down** (catalog 87–146 KB,
  ~1.15 KB per shard, rewritten on every attempt); compactor rewrote 731 MB (the 32 MB
  "partial" output shard is merged again in every run). Total ≈ 33× the raw bytes, of which
  the catalog is 2/3.
- Catalog stays at 75–78 shards with compaction every 30 s; without compaction it grows by
  one entry per commit.
- Followers and UI readers downloaded **38 GB in 5 min**: every poll downloads every whole
  shard whose `build_id` range covers the build, including the recent ~32 MB compacted shard.
- HEAD compaction mostly fails under ingest (lock failure; no retry): 3 ok of 6 at 10 workers.
- HEAD UI readers with a long-lived `Bucket` saw `table 'log_line' does not exist` for the
  whole run: the catalog loaded at start is cached 60 s and not configurable.

### 3.5 Queries on 1,000 builds × 50 packages × 2,000 lines (100M lines, 6.69 GB, 177 shards ~40 MB, catalog 205 KB)

Bulk load: 2 min 17 s, 738k lines/s with 4 loaders. `tmp/qbench*.log`, 10 samples.

| Query | HEAD p50 / max | patched p50 | `[2c/4G]` HEAD p50 | data per query |
|---|---|---|---|---|
| catalog load (`NewFromClient`) | 10 / 12 ms | 7 ms | 6 ms | 0.2 MB |
| full log of one build (100k lines, sorted in Go) | 326 / 387 ms | 413 ms | 349 ms | 1 shard, 40 MB |
| log of one package (2k lines) | 111 / 128 ms | 225 ms | 117 ms | 40 MB |
| tail since seq 1900 (100 lines) | 111 / 143 ms | 189 ms | 115 ms | 40 MB |
| substring search in one build (Go-side) | 259 / 386 ms | 290 ms | 304 ms | 40 MB |
| avg duration per package per day, 30 days (1,500 groups) | 351 / 399 ms | 342 ms | 340 ms | 3 MB |
| same, all 90 days (4,500 groups) | 2.30 / 2.72 s | 2.31 s | 2.29 s | 8 MB |
| failed packages, last 7 days | 50 / 56 ms | 8 ms | 55 ms | 0.8 MB |

- Peak RSS 218–273 MB. Allocation per whole-build query 230 MB.
- 8 MB shards (120 builds, 1 build/shard): build 279 ms, package 51 ms, tail 48 ms, 6.7 MB per query.
- Grouping more than 1,000 groups fails by default (`maximum grouping size exceeded`);
  `WithMaxGroups(100000)` was used.
- The patched sequential `extract` is slower for selective queries (225 vs 111 ms) because
  it decodes every row; HEAD seeks. A correct fix should filter per shard before merging.

### 3.6 Retention

lake has no delete API. Workaround `logstore.DropShards`: read `lakedb.catalog`, drop shards
whose `build_id` max is older than the cutoff, PUT with If-Match, then delete the objects.
On the bulk set, keep 60 days: 55 shards / 2.21 GB dropped in **100 ms** (1 GET, 1 PUT, 55
DELETE); builds older than 60 days return 0 rows, newer ones all rows. Granularity is a
shard (a shard is dropped when its newest build is older than the cutoff). It depends on
lake's internal catalog format and checks `version == "v1"`.

### 3.7 Bugs and missing features in lake HEAD (for the owner)

| # | Severity | Where | What | Local workaround | Patch |
|---|---|---|---|---|---|
| L1 | critical | `engine.go extract` + parquet-go `merge.go` `mergedRowGroupRows.ReadRows` | Filtered `Scan` over ≥ 2 shards of a table with a `sort` tag and overlapping key ranges loops forever. `extract` seeks before every row with a 1-row buffer; parquet-go's skip loop consumes that buffer and then reads 0 rows forever. Still in parquet-go main (`v0.32.1-0.20260923…`). | no `sort` tag on any table | P4 |
| L1b | critical | `engine.go load/filter` | For sorted overlapping shards the filter bitset is built on `ColumnChunks()` (concatenated order) but rows are read in merge order: wrong rows. | no `sort` tag | P4b: concatenate, sort the result |
| L2 | critical | `bucket.go write` | After 5 lock failures `err` is overwritten by `loadCatalog`'s nil: `Commit` reports success, the shard is lost (orphan object). Also only 5 retries, no backoff. | none (bob cannot detect it) | P1 |
| L3 | high | `bucket.go commitCatalog`, `compactor.go` | Commit does not check that the shards to remove are still in the catalog; if the Bucket reloaded its catalog during compaction, the merged copy is added again → duplicates. | one compactor per Bucket object, nothing else uses it; plus a lease | P2 |
| L4 | high | `engine.go load` | Compaction deletes inputs at once; any reader with an older catalog (cached 60 s) gets `NoSuchKey`. | new `Bucket` per query | P6: reload once on `NoSuchKey` |
| L5 | high | `compactor.go` | No retry/rebase on lock failure: under steady ingest compaction often fails. | run often | P3 |
| L6 | medium | `compactor.go` | Loads all small shards of a table into memory in one run; no bound. | compact often | P7 `WithCompactionMaxInput` |
| L7 | medium | `bucket.go` | Catalog cache 60 s fixed; no exported refresh. Fresh data invisible to long-lived readers. | `NewFromClient` per poll (10 ms) | P5 `WithCatalogCacheDuration` |
| F1 | medium | `insert.go`, `compactor.go` | No compression/encoding option; lake columns can't take parquet tags. | plain fields with `dict,zstd` | — |
| F2 | medium | — | No delete / retention / drop-shards API. | catalog edit (3.6) | — |
| F3 | medium | `bucket.go` | One catalog object per bucket for all tables, rewritten whole on every commit: ~8-10 commits/s cap, catalog traffic 2/3 of all bytes. | flush budget (5.3) | — |
| F4 | low | `insert.go extractRanges` | Ranges for every column, also unfilterable ones (`line` min/max strings land in the catalog: up to 2 × line length per shard). | cap line length | — |
| F5 | low | `filter.go` | No `Contains`/prefix filter; `Filter` fields are unexported, so no custom predicate. | filter in Go after `Scan` | — |
| F6 | low | `insert.go commit` | Backlog over 100 entries drops the oldest shard silently; a failed `writer.Close` drops the buffered rows. | bob keeps its own buffer, sees errors | — |
| F7 | low | `compactor.go` | No minimum shard age: the shard of a running build is compacted into a 32 MB file and then downloaded on every follow poll. | — | — |
| F8 | low | query | Every query downloads whole shard objects (no range GETs, no cache). | smaller compaction size | — |

`lake-s10.patch` is a reference for the owner, not a proposal to vendor a fork (R-STORE-003).
lake's own integration tests pass with the patch.

## 4. Answer

- **Logs and analytics fit lake's model** (time-sortable build ids prune shards well; queries
  0.1–0.4 s on 100M lines; analytics 0.35 s for 30 days), **but lake HEAD must not be used
  for this as it is.** L1, L1b, L2, L3, L4 are real bugs (R-STORE-003 allows minimal
  upstream fixes). L2 alone loses most logs at 10+ workers.
- **Write throughput is the architectural limit:** one catalog CAS object per bucket,
  ~8-10 commits/s in total here. Logs every 2 s from many workers do not fit; bob needs a
  bucket-wide commit budget, and live view should come from S17 peering, with lake as the
  persistent store and fallback (as R-LOG-002 already says).
- **Compaction** runs in the orchestrator reconcile loop under a dynamitedb lease (one
  compactor at a time). With P2/P3 concurrent compactors are safe anyway (measured), the lease
  only saves wasted work.
- Retention works today only through a catalog edit; a lake API is the clean fix.

## 5. SPEC input

### 5.1 Buckets and tables [proposal]

Analytics bucket (lake), tables:

```go
// log_line: one row per output line. No sort tag until lake L1/L1b are fixed.
type LogLine struct {
    lake.Table `name:"log_line"`
    BuildID lake.String `parquet:"build_id"` // 26-char time-sortable id (48-bit ms + 80 random bits, Crockford base32)
    Package lake.String `parquet:"package"`  // bob label, e.g. //svc/api:server
    Seq     lake.Int    `parquet:"seq"`      // per (build, package), from 1, gap-free, continues across attempts
    Drv     string      `parquet:"drv,dict,zstd"`
    Attempt int64       `parquet:"attempt,zstd"`
    Stream  int64       `parquet:"stream,zstd"`   // 1 stdout, 2 stderr, 3 bob
    TS      int64       `parquet:"ts,delta,zstd"` // unix ns, read time on the worker
    Line    string      `parquet:"line,zstd"`     // UTF-8, max 4 KiB, longer lines split with continuation marker
}

// package_run: one row per finished package build (analytics, R-UI-002).
type PackageRun struct {
    lake.Table `name:"package_run"`
    BuildID, Project, Package, Drv, Worker, Status lake.String // status: ok|failed|cached|cancelled
    Start, End lake.Int // unix ns
    DurationMs, Lines lake.Int
}
```

- No hive partitioning (lake has none). Pruning comes from catalog ranges on `build_id`
  (time-sortable ids ⇒ compacted shards cover narrow id ranges).
- After L1 is fixed upstream: add `sort:"build_id:asc,package:asc,seq:asc"` to `log_line`.
- Line cap 4 KiB (catalog range bloat F4, hostile input R-SEC-004).

### 5.2 Write protocol (worker) [proposal]

1. The build's stdout/stderr reader appends lines to an in-memory buffer (never blocked by S3).
2. A flusher sorts the buffer by `(build_id, package, seq)`, calls `Insert` + `Commit`:
   every **5 s**, or at **5,000 lines / 1 MiB**, and at package end.
3. On a commit error the batch stays in lake's backlog and is retried with the next flush;
   if the backlog holds > 60 s of data the worker logs a warning and (after the fix for L2)
   reports `logs_degraded` on the job status.
4. Package end: flush, then insert one `package_run` row (same flusher, same commit).
5. Crash: lines since the last successful commit are lost (R-LOG-001 "up to the last flush").
   A retried job continues `seq` after the last seq found in lake for `(build, package)`,
   `attempt` + 1.
6. Bucket-wide budget: **≤ 5 commits/s** total. Flush interval =
   `max(5 s, active_workers / 5 s)`; active worker count from the fleet bucket.

### 5.3 Compaction and retention (orchestrator reconcile loop) [proposal]

- Controller `lake-maintenance`, every **30 s**, only the holder of dynamitedb lease
  `fleet/lease/lake-maintenance` (TTL 90 s) works.
- Step 1: `Compactor[LogLine]` with `WithCompactionSize(32 MB)` (8 MB if follow-from-lake is
  frequent, see 3.5) and, once available, `WithCompactionMaxInput(256 MB)`. Then
  `Compactor[PackageRun]`.
- Step 2, once per hour: retention. Drop `log_line` shards whose newest `build_id` is older
  than `retention.logs` (default **30 days**), `package_run` older than
  `retention.analytics` (default **400 days**). Releases are not affected (R-STORE-007).
- Errors: `ErrOptimisticLock` / `ErrShardsGone` → retry next tick, metric
  `bob_lake_compaction_conflicts_total`. Any other error → event on the fleet page.
- Orphan sweep (daily): list `log_line/` objects older than 24 h that are not in the catalog,
  delete them (they come from failed commits and crashed compactions).

### 5.4 Query API (connectrpc, orchestrator) [proposal]

```proto
service LogService {
  // Full log of a build or of one package; ordered by package, seq.
  rpc GetLog(GetLogRequest) returns (GetLogResponse);
  // Follow: returns lines after the cursor; client polls every 3 s when no peering (S17).
  rpc TailLog(TailLogRequest) returns (TailLogResponse);
  // Substring search inside one build (case-sensitive, max 1,000 hits).
  rpc SearchLog(SearchLogRequest) returns (SearchLogResponse);
}
message GetLogRequest  { string build_id = 1; string package = 2; /* optional */ int64 after_seq = 3; int32 limit = 4; /* default 10000, max 100000 */ }
message TailLogRequest { string build_id = 1; map<string,int64> cursor = 2; /* package -> last seq; empty = from start */ }
message LogLine        { string package = 1; int64 seq = 2; int32 stream = 3; int64 ts_unix_nano = 4; string text = 5; int32 attempt = 6; }
message TailLogResponse { repeated LogLine lines = 1; map<string,int64> cursor = 2; bool build_finished = 3; }
service AnalyticsService {
  rpc PackageDurations(PackageDurationsRequest) returns (PackageDurationsResponse); // GroupBy package, Date(day); Avg/Count
  rpc RecentFailures(RecentFailuresRequest) returns (RecentFailuresResponse);
}
```

- Server-side cache: the orchestrator keeps the last 4 downloaded shards (≤ 256 MB) in memory
  keyed by object key (immutable) to avoid the 40 MB re-download per poll (3.4: 38 GB in 5 min).
- Error texts: `log not found for build <id>` (NotFound), `log store busy, try again`
  (Unavailable, on lock conflicts/timeouts), `search term must be 3..200 bytes`.

### 5.5 Budgets [proposal] (measured values in brackets, `[8c/15G]`, SeaweedFS local)

| Item | Budget | Measured |
|---|---|---|
| Live log via lake (no peering), line → visible | p99 ≤ 8 s | p50 1.9 s, p99 2.7 s at 5 s flush |
| Worker flush commit | p99 ≤ 5 s | p99 2.0 s at 5 commits/s |
| GetLog full build (100k lines) | p99 ≤ 1.5 s | p50 0.33 s, max 0.39 s |
| GetLog one package / TailLog | p99 ≤ 500 ms | 111 / 143 ms |
| SearchLog one build | p99 ≤ 1.5 s | 259 / 386 ms |
| Analytics 30 days | p99 ≤ 1 s | 351 / 399 ms |
| Bucket commits | ≤ 5/s | cap ~8-10/s |
| Lost lines | 0 except since last flush on crash | 0 (patched) |

### 5.6 Volume estimates

- 1 line ≈ 48 B raw ≈ **67 B stored** (catalog excluded). 100k lines per build ⇒ 6.7 MB.
- 1,000 builds × 50 packages × 2,000 lines = 100M lines = **6.7 GB**, 177 shards, catalog 205 KB.
- `package_run`: 50k rows in 11 shards (size not measured separately).
- Catalog ≈ 1.15 KB per shard; 10,000 shards (≈ 380 GB of logs at 40 MB) ⇒ 11.5 MB catalog,
  rewritten on every commit: retention must keep log shards below ~2,000 (≈ 2.3 MB catalog).

## 6. Decisions, open points

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S10-1 | SeaweedFS primary, RustFS for the race repros and CAS check. | Both are on the R-STORE-006 list; S09 uses the same. |
| S10-2 | Tables without `sort` tag; workers sort flush batches. | L1 hangs otherwise (principle 1). |
| S10-3 | Plain Go fields with `dict,zstd` for non-filter columns. | 2.8× smaller, works with lake HEAD. |
| S10-4 | Reference patch kept local, measured, not pushed. | Brief: no publishing; owner decides upstream fixes. |
| S10-5 | Bulk data loaded at compacted shard size, not through compaction. | HEAD compactor loads all small shards into memory (L6). |
| S10-6 | `seq` per (build, package) instead of per build. | Packages of one build run on different workers; no coordination needed. |

### Owner decisions needed

1. **lake fixes.** (a) Owner fixes L1, L1b, L2, L3, L4 (and ideally L5–L7) upstream before
   Stage 3 uses lake; `lake-s10.patch` shows one way. (b) bob uses lake HEAD with the
   workarounds only (no sort tag, single compactor Bucket, new Bucket per query) and accepts
   L2 data loss under contention. (c) Logs not in lake. **Recommend (a)**: L2 cannot be
   worked around from outside.
2. **Write scaling (single catalog, ~8-10 commits/s).** (a) Accept the cap: commit budget
   5/s, flush interval grows with fleet size, live view via S17 peering. (b) Owner adds a
   scalable catalog to lake (e.g. one catalog per table, or an append-only manifest log
   compacted by the compactor). (c) Workers send logs to orchestrators, which batch commits
   (changes D38 for logs). **Recommend (a) for v1**, (b) as a later lake feature.
3. **Retention.** (a) Owner adds `DropShards`/`Delete` with a filter to lake. (b) bob edits
   the catalog object directly (works today, depends on lake internals). **Recommend (a)**,
   with (b) as interim if (a) is not ready.

### Open risks

- Catalog traffic dominates bytes (2/3); on paid S3 that is request and egress cost.
- Follow-from-lake downloads whole shards; without peering or a shard cache it does not scale.
- Patched `extract` decodes every row (2× slower selective queries); the upstream fix should
  filter per shard.
- Upstream parquet-go bug in `mergedRowGroupRows.ReadRows` stays even if lake avoids it.

### Not verified

- Behaviour on real AWS S3 / Ceph RGW (only local SeaweedFS and RustFS).
- Compaction amplification over hours (only 5 min measured: ~9× data rewrite).
- Timings on a quiet machine (`measure.sh`); `[2c/4G]` only limits the query client.
- Real build logs (generator lines average 48 B; real compiler output is often longer).
