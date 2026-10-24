# I-B1 (Track B: M13.1 → M13.2 → M13.3) — internal/store, internal/safe, deploy/policies

## Done
- Read AGENTS, impl-brief, HARNESS, G17/G18, SPEC §05, ADR 0011, dynamitedb v0.6.1 source.
- RustFS 1.0.3 is in the offline tool cache (`~/.cache/bob-harness/tools/...`); copied into
  `/var/tmp/xdg-I-B1` (own XDG_CACHE_HOME). tests/serverenv/rustfs passes.
- M13.1: internal/store keys.go, bucket.go (request layer, lost-response rule), record.go
  (Call, Get, GetCached/Cache, Create, CAS, PutOwned, Delete), store.go (SeqLog, PagedIndex,
  ShardSet, Lease), class.go (ClassID, Satisfies), s3client/; internal/safe (Go, Run).
  Green: R-STORE-100 (static), 103, 104, 106 (4 primitives + imports), 005 (both), 102 seven
  checks, R-FLEET-004 class property test.
- M13.2: probe.go (Refusal, exit 78), storage.go (StorageConfigFromEnv, OpenStorage,
  CheckWorkerCredential). internal/store/storage_test.go covers positive path + refusal on
  RustFS with the shipped policies. deploy/policies already match SPEC (policy tests green).

- M13.3: records.go (fleet records of §05.4.1, JobRecord/JobSpec/JobResult, BuildRecord,
  BuildEntry, counters, bidx docs, promotion log, constructors Queue/RegLog/ActiveJobs/
  WorkerIndex/BuildPages/BranchBuilds/PromoLog, ControlKey for the encoded control keys,
  ReleaseKey, CheckoutShardKey), Lease.Release (§05.6.5 shutdown), sweep/staging.go
  (Staging.Sweep with UA bob-sweep/staging-sweep, EnsureLifecycle: RustFS 1.0.3 accepts the
  7-day staging/ rule). Internal tests: records_test.go (R-STORE-103 table per record type incl.
  lost response), sweep/staging_test.go.
- Checks: gofmt clean, GOWORK=off go vet, staticcheck and boblint (-nopanic -noexit -nolist
  -safego) clean on internal/store/... and internal/safe. `go run ./tools/ci -only
  fmt,vet,lint,layering`: vet + layering ok; fmt and lint fail only on other units' files
  (pkg/cli, pkg/ocaml/resolve, mid-edit).

## Still red (not this unit)
- R-STORE-102 binary part, R-SEC-102 (both tests): cmd/bob-server wiring (NEEDS).
- R-STORE-100/101/004/105/007/150, R-SEC-100 RPC test: need the running server (orchestrator,
  API, promotion; M14/M15/M16). R-STORE-004 has no row reachable without the §06 API.

## Next
- Done with M13.1–M13.3 store side; hand back.

## DECISIONS
- D-IB1-1 (store does not call the dynamitedb library; keeps its object format). Measured
  (scratch program against RustFS 1.0.3, 2026-10-22, dynamitedb v0.6.1):
  `dynamitedb.Create` of the protected tests' record shape (string `pk` field) fails with
  "partition key 'trec' is unset"; `dynamitedb.Put` of a record returned by `dynamitedb.Get`
  panics "incorrect DataField usage: cannot set value from filter or value operator"
  (update.go:114, data.go `dataField.set`), so `CAS(fn(cur))` cannot hand fn's result to Put;
  and dynamitedb models carry key names in struct tags, while SeqLog/PagedIndex/ShardSet/Lease
  take them at run time (SPEC §05.3). The store therefore issues the S3 requests itself with
  the AWS SDK, in dynamitedb's format (keys `<pk>/<v>[/<sk>/<v>]`, CBOR map with dynamitedb's
  encoder options + sorted keys, ETag optimistic locking, Create = If-None-Match, CAS = GET +
  PUT If-Match). Records are plain Go structs (string key fields tagged pk/sk, `etag:"true"`
  string, short CBOR names + omitempty) — exactly the shape of the protected tests' `rec`.
  ADR 0011 (locked) says "bob's store package wraps dynamitedb": the decision keeps every rule
  of ADR 0011 and R-STORE-103 except the library call itself. **Owner item** (change proposal
  for ADR 0011 decision 6 / R-STORE-103 "uses dynamitedb HEAD"; SPEC §05.4.3 shows JobRecord
  with DataField types). Misuse that dynamitedb would panic on (nil model, non-struct T,
  missing/non-string key field, missing ETag header) returns ErrPanic, as R-STORE-005 asks.
- D-IB1-2 SDK retries are off; the store resolves a write whose response was lost (no
  response or 5xx) by re-reading: stored bytes == our bytes → our write landed (success);
  precondition still holds → resend; else the other writer won (conflict). This implements
  R-STORE-103 rule 7 for any record, and HARNESS fault "lose the lost-response rule".
- D-IB1-3 GetCached decodes the body of the 200 itself (1 request, SPEC says 2 with
  dynamitedb.Get); the 304 path is 1 request as specified.
- D-IB1-4 CAS budget: 8 attempts for records (SPEC); 64 for the primitives' shared documents
  (PagedIndex pages, ShardSet shards), where many writers meet on one document by design
  (200 concurrent ShardSet.Add over 4 shards exhausted 8 attempts in the R-STORE-106 test).
- D-IB1-5 Lease: a lapsed lease (expires + skew < now) is taken over with epoch+1 even by its
  last owner (another orchestrator may have acted meanwhile; the R-STORE-106 test expects the
  epoch to rise after expiry). LeaseSkew = 5 s constant (fleet.skew default).
- D-IB1-6 R-STORE-102 probe and workers: the worker policy (R-SEC-002) allows no write under
  `_bob/probe/` on bob-fleet or bob-artifacts, so a worker cannot probe those buckets. A 403
  on the first probe write skips that bucket; OpenStorage requires at least one probed bucket
  (all share the endpoint; a worker probes bob-analytics). With wider credentials every bucket
  is probed.
- D-IB1-7 R-SEC-102 order: HeadObject control (worker cred) → GetBucketPolicy control (deny
  statement with s3:*, bucket and bucket/*, a Condition) → PutObject fleet/worker/_probe
  (worker cred); stop at the first failure, so a wide credential never writes fleet state
  before the refusal. A writable fleet/worker/ refuses with "fleet bucket prefix worker/ is
  writable with the worker credential" (7008) and the probe object is removed.
- D-IB1-8 PagedIndex.Page of a page never written is ErrNotFound; a written page always has
  P entries (missing = zero values).
- D-IB1-10 Composite control keys: SPEC rows `relv/<projectID>/<enc(name)>/v/<enc(version)>`,
  `repo/<forgeID>/<enc(id)>`, `fdel/<forgeID>/<enc(id)>` have a segment count R-STORE-104
  forbids, and `bbr/<projectID.enc(branch)>` can exceed 100 chars (26 + 1 + 97). store.Scoped
  joins "<id>.<enc(s)>" and falls back to "<id>.h<40 hex of SHA-256(s)>" when longer than 100;
  relv = relv/<Scoped(project,name)>/v/<enc(version)>, repo/fdel = <pk>/<Scoped(forge,id)>.
  SPEC §05.4.2/§05.4.5 text should follow (NEEDS).
- D-IB1-11 Records are plain structs with key fields `cbor:"-"`; times use omitzero; nested
  field CBOR names not fixed by the SPEC are chosen short (records.go).
- D-IB1-9 Config: storage.worker_probe_credential = BOB_STORAGE_WORKER_PROBE_CREDENTIAL
  "<access>:<secret>" (as the protected test decided); bucket names
  BOB_STORAGE_BUCKETS_{CONTROL,FLEET,ANALYTICS,ARTIFACTS}.

## NEEDS
- cmd/bob-server (owner of cmd/): at start of `orchestrator` and `worker` modes, before
  anything else:
  ```go
  cfg, err := store.StorageConfigFromEnv(os.Getenv)            // error → exit 78 too
  st, err := store.OpenStorage(ctx, cfg, mode)                 // "orchestrator" | "worker"
  var r *store.Refusal
  if errors.As(err, &r) { fmt.Fprintf(os.Stderr, "✗ %s\nhint: %s\n", r.Msg, r.Hint); os.Exit(r.ExitCode()) }
  ```
  This turns TestRSTORE102RefusesBackendThatIgnoresIfNoneMatch (binary part),
  TestRSEC102OrchestratorRefusesReadableControlBucket and TestRSEC102PositivePathAndRefusal
  (refusal part) green; the store side is tested in internal/store/storage_test.go.
- go.mod: `github.com/aws/aws-sdk-go-v2{,/config,/credentials,/service/s3}`,
  `github.com/fxamacker/cbor/v2` are now direct imports of internal/store (still marked
  `// indirect`; `go mod tidy` will move them). No new module; go.sum unchanged.
  `github.com/megakuul/dynamitedb` is no longer needed by shipped code (D-IB1-1).
- ADR 0011 / SPEC §05.3–05.4 / R-STORE-103 text: see D-IB1-1 (owner item); SPEC key rows: D-IB1-10.
- promote (owner of internal/promote or internal/artifacts): R-STORE-105 first bullet (delete the
  staged objects named in COMPLETE.json after promotion) is theirs; the hourly controller that
  calls sweep.Staging.Sweep (and EnsureLifecycle at start) belongs to the orchestrator controllers.
- Environment note: during one parallel run the host had ~29k TCP TIME_WAIT sockets (other
  agents' tests); tests/store and tests/policy then failed with "listen tcp 127.0.0.1:0: bind:
  address already in use". Rerun alone: green; tests/store peaks at ~700 TIME_WAIT.

## TEST PROBLEM
- (none blocking) tests/store/doc.go says "when dynamitedb is added to go.mod, rec_test.go is
  the one file that changes" (to DataField types). With D-IB1-1 the plain record shape stays
  the right one; if the owner instead keeps the dynamitedb library, rec_test.go cannot work
  with it (see D-IB1-1 evidence) and the CAS API would need update models.
