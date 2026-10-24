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

- M14.2: internal/worker/logs_job.go (Logs, JobLog: seq per package, SplitText, attempt ≥ 2 continues
  after LogLast or a lake LastSeqs read + LiveRestart, 20,000-line / 8 MiB ring, LineWriter, EndPackage,
  End → LogLast; NewProcessLogs = server.Component running the lake writer), logs_live.go (LiveStream:
  LiveSubscribe/Unsubscribe, chunk every 250 ms after the cursors, ≤ 5,000 lines / 4 MiB, 900 KiB/s with a
  3.6 MiB burst, finals after the lines, expiry). internal/orchestrator/logs_live.go (LiveLogs =
  logstore.Live: Attached/IsAttached, one subscription per (orchestrator, job) to the owner, renew 5 s,
  expiry 15 s, LiveUnsubscribe on requeue/last follower; Chunk validation: subscription of this worker for
  this job, record epoch, ≤ 5,000 lines, ≤ 4 MiB, lines ≤ 4 KiB UTF-8, 1 MiB/s + 4 MiB burst per worker;
  rejects log `invalid_argument: log line invalid: <package>/<seq>`), logs_service.go (BuildJobs,
  LogService GetLog/FollowLog/SearchLog), logs_maintenance.go (LakeMaintenance controller).
- cmd/bob-server/parts_logs.go (logParts: LogService + lake-maintenance) + one line in parts.go.
- tests/serverenv/workerclient (Register via fleet bucket + registrar approval, Attach over h2c/TLS with
  bwt1 + PoP, Stream.Send/SendChunk/In) + its test against a real Registrar and api.Server (green).
- tests/server/logs_forged_test.go TestRLOG102ForgedChunks (new): cross-job, current/stale/future epoch,
  5,001 lines, line > 4 KiB, forged restart, flood; follower unchanged = lake, hostile worker never
  subscribed, orchestrator logs the rejections, no panic. Fails like every tests/server log test today at
  srvtest.Start (`bob login` BOB-E-9008, CLI M16.5), then needs the build controller (BuildService).
- Green (in process): TestLiveLogsHostileChunks (all S17 cases adapted to pull), TestLiveLogsNotAttached,
  TestLivePullEndToEnd (real worker log + LiveLogs + Follow + RustFS lake: 10 followers, 200 lines/s,
  p50 101 ms / p99 253 ms line → follower [8c host, RustFS local, load from other agents], Attach cut
  5 s: stall ≤ 3 s via delayed + lake, final streams = lake), TestJobLogSeqAndRestart, TestLiveStream,
  TestGetLogBudget (p99 7.5 ms, 50 packages, 50 shards).
- Checks: gofmt clean; GOWORK=off vet, staticcheck, boblint (nopanic/noexit/nolist/safego/importboundary)
  clean on all touched packages. `tools/ci -only fmt,vet,lint,layering` fails only on other units' files
  (pkg/golang/internal/gospec/gospec.go fmt; internal/forge imports modules not in go.mod).

## Still red / not reachable yet
- tests/server TestRLOG001*, TestRLOG002LivePull, TestRLOG003Attribution, TestRLOG101Matrix*,
  TestRLOG102HostileOutput, TestRLOG102ForgedChunks: srvtest.Start fails at `bob login` (CLI M16.5);
  then they need BuildService + the build controller, the Attach glue below and the job runner's log
  stream (NEEDS).

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

- D-IB7-7 Chunk validation uses the owner/epoch of the job record as the feed read it (≤ 1 s old, every
  feed tick) instead of a GET per chunk: a chunk is accepted only on the subscription this orchestrator
  sent to that owner for that epoch. A requeue drops the subscription at the next tick (LiveUnsubscribe to
  the old owner), after which its chunks are refused. Structural (subscription ids are 96-bit random).
- D-IB7-8 Worker live buffer = one ring per job of the last 20,000 lines / 8 MiB (SPEC §6.5.17 caps); with
  the 10,000-line commit trigger it holds the uncommitted lines plus ≥ 10,000 committed ones in normal
  operation (SPEC §05.10.2). Beyond it a subscriber skips ahead and its orchestrator fills the gap from the
  lake.
- D-IB7-9 The worker sends at ≤ 900 KiB/s with a 3.6 MiB burst per Attach stream (90 % of the
  orchestrator's 1 MiB/s / 4 MiB), so honest chunks are not dropped by clock differences.
- D-IB7-10 A retried attempt whose last persisted seqs cannot be read (lake error) starts its packages at
  seq 1 with a Restart{from 1}: the restart rule then voids the previous attempt entirely, which is
  correct (only the follower's prefix is lost).
- D-IB7-11 Lines are made valid UTF-8 (U+FFFD) and split at ≤ 4 KiB rune boundaries on the worker (proto
  strings must be UTF-8; R-LOG-102 hostile output); an unterminated line is emitted at 64 KiB.
- D-IB7-12 BuildJobs uses plain GETs (build record, job records of non-terminal jobs) once per second per
  followed build; store.Cache was not used because it never forgets keys.

## NEEDS
- I-B6 (internal/fleetsrv Attach, cmd/bob-server/parts.go): wire the live path.
  Orchestrator: `live := logParts(env, &parts)` (the line exists; take its result) and give it to
  WorkerControl; in Attach after Hello:
  ```go
  liveOut := make(chan *workerv1.AttachResponse, 64)
  detach := live.Attached(wid, func(m *workerv1.AttachResponse) bool {
      select { case liveOut <- m: return true; default: return false } })
  defer detach()
  // receive loop: case *workerv1.AttachRequest_LiveChunk: _ = live.Chunk(wid, x.LiveChunk) // logs + counts rejects
  // send loop:    case m := <-liveOut: out = m
  ```
  and drop the Hub's "subscribe <job>" line (LiveLogs sends subscriptions itself; Hub.Subscribe is unused).
  The orchestrator's HTTP server must speak h2c (`srv.Protocols.SetUnencryptedHTTP2(true)`) for bidi
  Attach over http:// (devstack); tests/serverenv/workerclient uses h2c for http:// bases.
  Worker: `logs, _ := worker.NewProcessLogs(ctx, worker.LogConfig{Storage: env.Storage.Config, Wid: id,
  FlushInterval: setting(env, "BOB_LOG_FLUSH_INTERVAL", 30*time.Second), Now: env.Now, Log: env.Log})`, run
  `logs` as a component (it is a server.Component: the lake writer); per Attach stream
  `ls := logs.Stream(func(c *workerv1.LiveChunk) error { return send(&workerv1.AttachRequest{Msg:
  &workerv1.AttachRequest_LiveChunk{LiveChunk: c}}) })`, `safe.Go(func() error { return ls.Run(ctx) })`, and
  for each received message `if ls.Handle(m) { continue }`.
  Job runner (exec): `jl, _ := logs.Start(ctx, spec.BuildID, c.JobID, c.Epoch, int32(c.Attempt), prevLogLast)`;
  every output line of package p → `jl.Line(p, stream, text)` (or `jl.Writer(p, stream)`); package end →
  `jl.EndPackage(p, &logstore.PackageRun{...})`; before writing the result → `last, err := jl.End(ctx)` with
  a bounded ctx (e.g. 2 min: the R-LOG-101 lake-commit-failure run injects 503s for 3 s) and put `last` in
  JobResult.LogLast. The job container's stdout today is one JSON outcome; the lines of each package need a
  stream from `bob job-run` (e.g. line-delimited JSON events {pkg, stream, text}) — job runner design.
- Build controller (M16/M17): `logstore.Writer.AddJobRun(t, logstore.JobRun{...})` per finalised job
  (job-<yyyymm> partition, planner input); `BuildRecord.LogPartition = logstore.LogPartition(id, 4)`.
- I-B1/I-B8 (internal/store/sweep, retention): the lake sweep for Retention.DropPartitions (LIST is allowed
  only in internal/store/sweep): LIST `<analytics>` with Prefix "<class>-" and Delimiter "/", drop each
  partition whose `logstore.PartitionEnd(name)` ≤ before: DELETE `<p>/lakedb.catalog` first, then every
  object under `<p>/`; client with s3client.Options{Sweep: "lake-sweep"}.
- ADR/SPEC text: D-IB7-3 (cache bound), D-IB7-4 (compaction candidates) are clarifications of R-LOG-100 /
  §05.10.4; D-IB7-8 of §05.10.2.

## TEST PROBLEM
- None new. Known (I-B3): srvtest Env.Call posts to `<api_url>/<service>/<method>` without the `/api/`
  prefix, so GetLog/FollowLog calls of tests/server reach the 501 placeholder even once the CLI works.
