# I-B9 (Track B: M16.1 Nix binary cache → M16.2 promotion) — internal/cache, internal/promote, cmd/bob-server/parts_cache.go

## Done (M16.1 + M16.2)
- Read AGENTS, impl-brief, HARNESS, I-B1/I-B3/I-B4/I-B5/I-B8 progress, SPEC §6.6, ADR 0014, R-STORE-105,
  tests/cache, regenv.
- internal/cache: objects.go (artifacts bucket: Get with Range/If-Match, bounded Read, Head, conditional
  Put with lost-response read-back, PutFile, Copy with CopySourceIfMatch + If-None-Match/If-Match on the
  destination + metadata, Delete), narinfo.go (Nix32, NarInfo parse (strict, bounded) / Format in the §6.6.1
  field order, Deriver never written), sign.go (Sign = `nix store sign`, keys, Verify), nar.go (streaming
  NAR walker with the §6.6.4 caps, `.ls` listing v1 written while walking), vis.go (visibility documents
  nix/vis/<hash>.json: LRU ≤ 200,000, 30 s TTL, 2 s negative TTL, one GET per miss even under concurrency,
  AddProject / SetPublic CAS ≤ 5), handler.go (/nix/nix-cache-info, <hash>.narinfo, nar/<hash>.nar.zst,
  <hash>.ls; 404 without S3 for bad hashes; 401 + WWW-Authenticate; visibility from the document alone;
  Cache-Control public/private; streaming proxy: single Range forwarded, multi-range → 200, 416, 256 KiB
  buffer, max_parallel_downloads → 503 Retry-After 1, 60 s client idle; no Location, no presigned URL).
- internal/promote: keys.go (SigningKeyRecord skey/<name> with the seed, index skeys/all with the states,
  Ensure/Create/SetState/Active/PublicKeys), promote.go (Run = pass over jact; Promote(job) = event path;
  JobSpec/JobResult signatures checked; COMPLETE.json bounded + strict schema + CompSHA; claims; topological
  levels, 4 parallel; served → NarHash compare / non_reproducible / project added only after the staged NAR
  proves the bytes; verify (zstd → NAR structure → NarHash/NarSize/FileHash/FileSize → reference scan incl.
  served-but-unclaimed hashes after /nix/store/) → Copy (source ETag guard, create-only, bob-narhash meta)
  → .ls → visibility → promo log → signed narinfo create-only; `promoted N` / `alreadyThere N` log lines;
  verdicts done / blocked: staging incomplete / failed: artifact verification (security log line with the
  worker id); assets verified; staged NARs/claims (+ COMPLETE.json when no assets) deleted by known key),
  verify.go, retire.go (RetireKeys: re-sign sweep over nix/narinfo/ with If-Match, then RETIRED),
  settings.go (SettingsService ListSigningKeys / CreateSigningKey / RetireSigningKey).
- internal/store/sweep/narinfo.go (new file, the only LIST: `NarInfos.Each` over nix/narinfo/, UA
  bob-sweep/key-retire; nolist rule).
- cmd/bob-server/parts_cache.go + one line in parts.go (after authParts): /nix/ handler as "GET /nix/"
  (replaces authParts' "GET /nix/nix-cache-info" stand-in), SettingsService key RPCs, first key at start,
  promotion controller (orchestrator.Controller ctl/promotion, 10 s, woken by RetireSigningKey) whose pass
  = Keys.Ensure + Promoter.Run + RetireKeys.
- Tests (all green): internal/cache (RustFS conditional ops, NAR walker accept/reject table, listing,
  routes/ranges, narinfo round trip, visibility: 1,000 concurrent lookups of 10 docs = 10 GETs);
  internal/promote on RustFS: promote 3-path job → real `nix copy` from the handler + `nix store verify`
  with bob's key alone, staging deleted, 401/404/private/public, ranges; two promoters on one job →
  `promoted 3` + `alreadyThere 3`; tampered NAR → failed, path + dependent 404, log with worker id; claim
  omitting a reference → failed; non-reproducible keeps the first publication, other project with the same
  bytes gains visibility; key rotation + retirement; missing COMPLETE.json → blocked.
- Protected tests green: TestRREG002SignatureEqualsNixStoreSign, TestRREG005CacheHasNoOutboundHTTPClient,
  TestRREG002WorkerCannotReadSigningKeys (e2e on devstack). Manual devstack check: nix-cache-info 200
  anonymous / 401 bad token / 200 SA download token; ListSigningKeys lists bob-1; promotion lease taken.
- gofmt, GOWORK=off vet, staticcheck, boblint clean on my packages; `tools/ci -only fmt,vet,lint,layering`
  fails only on other units' files (internal/build, internal/notify, pkg/golang, pkg/engine/build).

## Still red (not this unit)
- TestRREG154DownloadTokenScope: ProjectService/CreateProject is unimplemented (501).
- TestRREG001, 150, 151, 152 (all), 153, 005 skipped-paths, 002 tamper: need ForgeService, builds, the
  worker staging (COMPLETE.json etc.), JobService (promotion / non_reproducible fields), releases (M16.4:
  ReleaseService, UpdateRelease → SetPublic) and fixtures nondet/bigclosure.

## DECISIONS
- D-IB9-1 Key states live in the index `skeys/all` (one CAS document), seeds in `skey/<name>` (create-only):
  a rotation (new ACTIVE, old VERIFY_ONLY) is one atomic write. Seed record first, then index; an orphan seed
  of a crashed create is adopted by the next create of the same name. SPEC §05.4.5 puts the state in the
  record; text should follow.
- D-IB9-2 Seeds stored unsealed in the control bucket (orchestrator-only, R-SEC-102), like D-IB4-6: no
  orchestrator data key exists yet.
- D-IB9-3 Key names `bob-<n>` (server_name default "bob"; Keys.Prefix): BOB_SERVER_NAME is the per-process
  orchestrator id (D-IB3-5) and would give each orchestrator a different first key.
- D-IB9-4 ListSigningKeys (and PublicKeys) leave RETIRED keys out: clients use the list as trust source.
  Retiring the ACTIVE key is FailedPrecondition; only VERIFY_ONLY keys can be retired.
- D-IB9-5 Per-job promotion lock `plock/<jobID>` (store.Lease, control bucket, TTL 60 s, renewed, deleted
  after use). Correctness never depends on it (every write is conditional); it makes concurrent promotion
  of one job deterministic: the second orchestrator waits, re-reads the job and, when it is done, confirms
  the served paths (`alreadyThere N`) instead of redoing the work. Without it two orchestrators split the
  paths and R-REG-152 (1)'s "one promoted N, the other alreadyThere N" is not met. New key prefix: SPEC
  §05.4.2 needs a `plock/<jobID>` row.
- D-IB9-6 A served path's visibility gains a project only when that job's staged NAR proves the same bytes
  (NarHash recomputed): otherwise a worker could name the hash of another project's private path and gain
  visibility. Different bytes → non_reproducible, project not added.
- D-IB9-7 NAR copy guard: create-only with `bob-narhash` metadata. A 412 with our hash in the metadata is
  our own earlier copy; a different hash with a served narinfo is non-reproducible; without narinfo it is a
  concurrent promotion (retry) or, after 1 h, a crash orphan that is replaced with If-Match. This closes the
  race "two builds of one path with different bytes copy over each other's NAR before the narinfo".
- D-IB9-8 Claimed references must be in the job, in `skipped`, or served; otherwise the job waits (pm stays
  "") for up to 1 h after the job finished (a sibling job's promotion may be running), then fails
  verification. NAR hashes after "/nix/store/" that are not in the job's closure are checked against the
  served narinfos (≤ 256 per NAR).
- D-IB9-9 Logs: one `promoted N` and one `alreadyThere N` line per job and pass, each only when N > 0.
- D-IB9-10 Staging cleanup deletes NARs and claims of every path in COMPLETE.json, OCI blobs only when
  images were promoted (Images hook), and COMPLETE.json last only when the job has no assets and no
  unpromoted images (the release controller still needs it).
- D-IB9-11 Visibility negative entries are cached 2 s (not 30 s) so a path promoted on another orchestrator
  is served soon after; positive entries 30 s (SPEC).
- D-IB9-12 POST etc. on /nix/ still answer the 501 of the pending "/nix/" prefix of internal/http (the mux
  pattern is "GET /nix/", which also serves HEAD).
- D-IB9-13 Known window: a promotion that read the ACTIVE key just before a rotation and writes its narinfo
  after the retirement sweep passed it leaves a narinfo signed only by the retired key (clients then build
  locally; no data loss). Retire a key some minutes after creating the new one.

## NEEDS
- I-B8 (controllers): the promotion controller (ctl/promotion) is wired in cmd/bob-server/parts_cache.go;
  do not add a second one. If you prefer it in parts_controllers.go, call `promote.Promoter.Run` and
  `RetireKeys(ctx, sweep.NewNarInfos(...))` from your Pass.
- Build controller / job finalisation (orchestrator.Queue.Finalise): do not finalise a `succeeded` job
  while `JobRecord.pm == ""` (promotion pending); copy pm and nr into the build summary (build.v1 Job
  `promotion`, `non_reproducible`); requeue on "blocked: staging incomplete" (§6.6.4 step 1). Optionally call
  `Promoter.Promote(ctx, jobID)` as soon as a job result lands (the "event" of "event + 10 s").
- Worker (staging, M14.x): layout `<staging>nar/<hash>.nar.zst` (zstd), `<staging>narinfo/<hash>.narinfo`
  claim (StorePath, NarHash, NarSize, References; FileHash/FileSize optional and checked when present),
  COMPLETE.json last with exactly the §6.6.4 fields (unknown fields are rejected), JobResult.Complete =
  `staging/<build>/<job>/<attempt>/COMPLETE.json`, JobResult.CompSHA = hex SHA-256 of COMPLETE.json.
- I-B4 (internal/auth): WhoAmI.nix.public_keys / GetServerInfo.nix.public_keys from
  `promote.Keys.PublicKeys(ctx)` (parts_cache.go can pass a func if auth.Config gets a hook); drop the
  `GET /nix/nix-cache-info` stand-in in parts_auth.go and `bhttp.NixCacheInfo` (parts_cache.go deletes the
  route, so they are dead).
- Registry (M16.3): `promote.Config.Images` hook (step 4), `cache.NewVisStore(obj, cache.OCIVisPrefix, …)`,
  `cache.Objects` for blobs.
- Releases (M16.4): `VisStore.SetPublic` for closure paths; delete COMPLETE.json/assets after copying.
- Settings milestone: SettingsService GetSettings/UpdateSettings: embed `promote.SettingsService`; pass
  server_name as `promote.Keys.Prefix` and max_parallel_downloads (now BOB_MAX_PARALLEL_DOWNLOADS).
- SPEC text: `plock/<jobID>` row (D-IB9-5); key state in `skeys/all` (D-IB9-1).

## TEST PROBLEM
- tests/cache/big_slow_test.go assertNoDanglingReferences requires a narinfo for every reference, but
  references to `skipped` paths (served by cache.nixos.org, R-REG-005) have no bob narinfo by design. It
  holds only for a fixture whose closure has no nixpkgs references (bigclosure, owner: fixtures).
