# ADR 0013: Worker registration, token sealing and S3 bucket isolation

Status: **Accepted (Gate S pending)**, 2026-10-21 (spike S11, 2026-10-20; decision 8 struck by D57, decisions 3 and 6 amended). Evidence: `docs/spikes/s11-workers.md`,
code `spikes/s11-workers/`.

## Context

R-FLEET-001/002, R-SEC-000..003, R-STORE-002/006, D13, D38. The owner fixed the design: a worker
only has S3 credentials for the fleet bucket (and the analytics bucket, D38, and artifact
staging); it generates a key pair, writes a registration record into the fleet dynamitedb,
an orchestrator reconciles it and writes back a token encrypted to the worker's key. The bucket
split is emergency defence; worker isolation (R-SEC-000) is the primary boundary.

## Decision

1. **Keys.** Each worker process creates, in memory only, an Ed25519 signing key and an HPKE
   KEM key. **Worker id = `"w"` + base32(SHA-256("bob-worker-id-v1" ‖ sigpub ‖ suite ‖ kempub))[:26]**.
   New process = new keys = new worker id.
2. **Encryption: HPKE (RFC 9180) from the Go standard library `crypto/hpke` (Go 1.26)**,
   suite **MLKEM768-X25519 (0x647a) / HKDF-SHA256 / ChaCha20-Poly1305**, base mode,
   `info = "bob-wseal-v1" ‖ wid ‖ gen ‖ serial`. DHKEM(X25519) (0x0020) stays supported by
   the same code (suite id is in the record). No third-party crypto dependency.
3. **Records** (dynamitedb, fleet bucket): `wreg/<wid>` (worker-written, self-signed, created with
   If-None-Match), `worker/<wid>` (orchestrator-written: state, generation, serial, copies of the
   public keys, sealed token), `whb/<wid>` (signed heartbeat). The orchestrator signing key is in
   the control bucket.
4. **Token** = `bwt1.<cbor claims>.<Ed25519 sig>` signed by an orchestrator key, bound to the worker
   key (`cnf` = SHA-256 of the worker's Ed25519 key). Every RPC carries a **proof of possession**:
   Ed25519 over method, authority, path, timestamp, body hash and token hash. A token without
   the worker's private key is useless.
5. **Lifetime** 1 h [proposal], reissued by the reconciler at half-life only while the signed
   heartbeat is fresh; tokens of the current and previous serial are accepted. Revocation sets
   `state=revoked`, bumps `gen`, clears the sealed token; orchestrators check the worker record
   (cache ≤ 10 s [proposal]) on every request.
6. **S3 isolation is enforced by identity policies**, scoped by prefix inside the fleet bucket.
   **(Amended to the SPEC §05.4 schema.)** Workers may write only `wreg/ wrs/ wrt/ whb/ job/ qs/
   qh/`, and never `worker/ fleet/ orch/ ctl/ qt/ jact/ widx/`; analytics read/write; artifacts read + `staging/` write; control: nothing (plus an
   explicit Deny). bob also ships a **control-bucket bucket policy that denies every principal
   except the orchestrator users**, because on RustFS 1.0.3 and SeaweedFS 4.36 a bucket-policy Allow
   overrides an identity-policy Deny.
7. **SeaweedFS deployments must set the four JWT keys and gRPC mTLS in `security.toml`** and keep
   master/volume/filer ports unreachable from workers; otherwise the filer bypasses all S3
   policies (verified: unauthenticated read, write, delete in the control bucket).
8. **(Struck, D57.)** bob never mints S3 credentials: no STS, no AssumeRole. Workers always get
   their S3 credentials from the deployment (e.g. IMDSv2). Every further token is obtained
   through S3 first (the sealed token in `worker/<wid>`), never the other way round.
   OD-S11-1 = a: to cut off a compromised host, revoke its worker id and rotate the shared
   credential (runbook).

## Consequences

- The fleet credential stays the proof of being a worker: a revoked worker *host* that still
  holds the credential can register again with new keys. Revocation protects against stolen
  tokens and keys, not against a compromised host; for that, rotate the shared credential
  (or use the STS option b in the write-up).
- Workers can still overwrite each other's leases and job status (shared credential); anything
  security-relevant a worker reports must be signed with its key or go through the API.
- Operators must apply server-specific setup (documented in the write-up); bob-server should
  self-check at start (control bucket policy present, worker credential refused on control).

## Alternatives considered

- age (filippo.io/age, X25519): good, but an extra dependency and no gain over stdlib HPKE.
- NaCl box (x/crypto): not standardised, no context binding (info).
- Opaque random token with a hash in the control bucket: needs a control-bucket read per
  request; the signed token plus worker-record check gives the same revocation.
- Bucket policies instead of identity policies: RustFS evaluates bucket-policy Allow after an
  identity Deny (MinIO semantics), so identity policies plus a deny-all-others bucket policy
  are the safe combination.

## Owner amendments

- **D57**: decision 8 struck; OD-S11-1 resolved as option a; there is no STS code path (SPEC
  R-SEC-100).
- **D63**: SeaweedFS is unsupported. Decision 7 (SeaweedFS hardening) applies only to operators
  who run it anyway. bob documents RustFS and AWS S3.
- **D60**: the registration inbox is a sequence log `wrs/reg/s/<seq20>` read by GET; the registrar
  never lists `wreg/` (SPEC §05.6.3). Job specs are signed with the orchestrator key, so a worker
  cannot alter another job's spec (T10).
- **SPEC integration (D54, D55, D57)**: workers serve no RPC and need no inbound port. Each worker
  opens `bob.worker.v1.WorkerControlService/Attach` outbound to every orchestrator (wake-ups,
  live-log subscriptions, cancel, drain) and calls `GetCheckoutCredential` for the job it holds
  (SPEC §05.9.1). The orchestrator-to-worker token `bot1` and the `WorkerAgentService` drafted in
  SPEC §06 were dropped.
