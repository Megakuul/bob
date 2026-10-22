# I-B7 (Track B: M14.1 → M14.2) — internal/logstore, internal/orchestrator/logs_*.go, internal/worker/logs_*.go, tests/serverenv/workerclient, cmd/bob-server/parts_logs.go

## Done
- Read AGENTS, impl-brief, HARNESS, I-B1/I-B2 progress, SPEC §05.9–05.10, ADR 0012/0019, S10/S17,
  lake v0.5.3 (= 045e301) source, tests/server log tests, srvtest.

- go.mod: github.com/megakuul/lake v0.5.3 (= 045e301) via `go get` (bumps aws-sdk-go-v2 1.41.7 → 1.42.0,
  s3 1.101.0 → 1.103.3, smithy-go 1.27.2; adds parquet-go & co.); ATTRIBUTION.md row (all MIT/BSD/Apache).
- internal/logstore: tables.go (LogLine/PackageRun/JobRun, limits, SplitText), partition.go (BuildTime,
  LogPartition/RunPartition/JobPartition/PartitionEnd, PrefixClient = URL-prefix trick, guard = recover
  around lake), cache.go (shard cache as s3.HTTPClient), reader.go (Reader: Package/Build/Page/Search/Raw/
  LastSeqs, NoSuchKey retry with fresh Bucket), writer.go (Writer: batches, interval/size/flush triggers,
  budget interval, backoff, memory bound, Persisted), compact.go (Maintenance.Pass, own Bucket per
  partition), merge.go (settle = dedupe + restart rule, merger = S17 §5.4), feed.go (Sources, Feed,
  Follow/FollowPackage). Tests green: unit (partitions, split, settle, merger), RustFS: TestWriteRead,
  TestCompactionWorkarounds (R-LOG-100 L3+L4: 2 concurrent passes + 3 readers: 0 dup, 0 failed reads),
  TestWriterBudget (50 writers × 200 lines/s, 40 s, flush 5 s: 0 lost, 5.5 commits/s over 4 partitions),
  TestFollowMerge.

## Next
- worker logs_*.go (JobLog, LiveStream), orchestrator logs_*.go (LiveLogs: chunk validation, subscriptions,
  LogService), workerclient, forged tests, parts_logs.go.

## DECISIONS
- D-IB7-1 lake v0.5.3 is the tagged version of the SPEC's 045e301 (go mod download reports Origin.Hash
  045e30128cb5…).
- D-IB7-2 bob keeps its own batches; every commit uses a fresh lake.Ingestor (lake's backlog drops
  entries silently, S10 F6, and loses the error type: `bucket writer: %v`). Any commit error keeps the
  batch for a retry with backoff; non-lock errors also reload the catalog.
- D-IB7-3 Shard cache bounded by bytes (256 MiB, ≤ 64 MiB per shard, ≤ 256 shards) instead of "last 4
  shards": uncompacted shards of running builds are small (one per commit), 4 would cache nothing useful.
- D-IB7-4 Compaction candidates: log partitions of today and the 2 previous days, run/job of this and the
  previous month, whose time range began ≥ 10 min ago (R-LOG-100 "older than 10 min"), and whose catalog
  (HEAD, never creates a partition) changed within 48 h ("written within the last 2 days"). Target shard
  size 8 MB (S10 §3.5: faster package reads and follow-from-lake).
- D-IB7-5 Writer memory bound: 256 MiB of uncommitted rows per writer; beyond it the oldest uncommitted
  lines (never those of a running commit) are dropped, logged and counted. Unbounded memory would crash
  the worker (principle 1: never crash) while the store refuses commits.
- D-IB7-6 Follow ends ("done") when the build and all its jobs are terminal and a lake read started after
  that point is merged with no gap left; the end markers are the last delivered (seq, attempt) per package.
  Lake = truth; LiveFinal is not needed for correctness.

## NEEDS

## TEST PROBLEM
