# ADR 0012: Build logs and analytics in lake

Status: **Accepted (locked) — owner at Gate S, 2026-10-21**, 2026-10-21 (spike S10, 2026-10-20; amended by D55 and the owner's lake fix 045e301)
Evidence: `docs/spikes/s10-lake.md`, code `spikes/s10-lake/`.

## Context

R-LOG-001 and D20 want build logs written periodically as small Parquet files to lake and
compacted later. D38 lets workers write the analytics bucket directly. D8/D46: lake at its
current HEAD (`megakuul/lake@1c410ce`, 2026-10-19), changes upstream only for real bugs or
missing features.

S10 ran lake HEAD against SeaweedFS 4.36 and RustFS 1.0.3 with simulated workers,
compactors, followers and a 100M-line data set.

## Findings that drive the decision

1. lake HEAD **loses data silently** under normal multi-writer load: after 5 failed catalog
   CAS attempts `Ingestor.Commit` returns `nil` and the shard is never in the catalog
   (bug L2). With 10 workers flushing every 2 s, 37 % of lines were lost; 50 workers: 76 %.
2. A filtered query over two or more **sorted** shards whose key ranges overlap **hangs
   forever** (bug L1), and with a fix for the hang alone it returns wrong rows (L1b).
3. Two compactors on buckets that reload the catalog mid-run **duplicate all rows** (L3).
4. Readers fail with `NoSuchKey` for up to 60 s after a compaction (L4).
5. One catalog object per bucket serialises every commit: about **8-10 commits/s** in
   total on this host [8c/15G, SeaweedFS], whatever the number of writers.
6. With a local patch (P1–P7, ~130 added lines, `spikes/s10-lake/lake-s10.patch`) every run kept every line
   exactly once, and log queries on 100M lines are fast enough for the UI.

## Decision (proposed)

1. Logs and analytics go into lake as D20 says, at lake HEAD. **(Amended.)** L2 is fixed upstream
   (045e301). L1, L3 and L4 remain, and bob works around them: no sort tag; a single compactor
   under a lease, with its own Bucket object; readers retry once with a fresh Bucket on
   `NoSuchKey`. A commit error other than `ErrOptimisticLock` is ambiguous: the writer retries,
   and readers dedupe by `(build, package, attempt, seq)`.
2. Table layout: `log_line` (one row per line) and `package_run` (one row per package
   build), **no `sort` tag** (workaround for L1 until it is fixed), workers sort each flush.
   Only filter columns (`build_id`, `package`, `seq`) are lake types; the rest are plain
   Go fields with `dict`/`zstd` tags (2.8x smaller files).
3. **(Amended, D55.)** Workers flush in batches every **30 s or 10,000 lines / 2 MiB**, and at
   package end and job end. They keep failed flushes in memory until they commit.
   **Partitions use the URL-prefix trick** (verified with SigV4 on RustFS, SPEC V3/V4). Each
   partition is one lake "bucket" `<analytics>/<partition>/` with its own catalog:
   `log-<yyyymmdd>-<k>` (4 per day), `run-<yyyymm>` and `job-<yyyymm>`. The budget is
   ≤ 5 commits/s per partition. Live viewing is pulled from the worker's memory on demand
   (ADR 0019).
4. Compaction runs in the orchestrator reconcile loop, every 30 s, holding a dynamitedb
   lease so that only one orchestrator compacts at a time (defence against L3 even after
   the fix). Retention runs in the same loop.
5. **(Amended.)** Retention drops whole partitions (the catalog and every object under the
   prefix) with a cold hourly sweep. This sweep is the only LIST in bob-server (SPEC
   R-STORE-101). bob does not edit lake's catalog.

## Consequences

- Live logs from lake lag 2-3 s at 5 s flush (p50 1.9 s, p99 2.7 s in a 5 min run with the patch).
- Large fleets (> 25 workers) need S17 peering for live view, or a lake catalog that
  scales writes (owner decision).
- Whole-build log queries download whole shards (40 MB each); latency 0.3-0.4 s on 100M lines.

## Owner amendments

- **D55**: batch writes every 30 s; the live view is pulled from worker memory. No peering and
  no push.
- Owner lake fix **045e301** (L2), and the owner's note that errors other than lock errors are
  ambiguous (STATUS).
- Owner answer on partitions ("can you not just use a url in lake"): partitions use the URL
  prefix; lake needs no change.
- **D60/D63**: lake makes no LIST calls (verified, V4). Supported backends are those of
  ADR 0011.
