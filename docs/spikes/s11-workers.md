# S11: Worker registration and bucket isolation

## 1. Question and requirements

Can the owner's design (worker holds only fleet/analytics/staging S3 credentials, writes a
registration record with its public key into the fleet dynamitedb, an orchestrator reconciles it
and writes back a token encrypted to that key) be built safely on **RustFS and SeaweedFS**, and
can S3 credentials keep a worker away from the control bucket and the artifact write paths?
Requirements: R-FLEET-001/002/003/006, R-SEC-000/001/002/003/005, R-STORE-002/003/005/006,
R-SRV-004/007; D8, D13, D38, D46.

## 2. What was built

All under `spikes/s11-workers/` (Go module `bobspike/s11`, `CGO_ENABLED=0`).

| Path | What |
|---|---|
| `internal/reg/` | Registration protocol: keys, worker id, HPKE seal/open, records, reconciler, token, PoP, revoke, rotate |
| `cmd/s11/matrix.go` | 27 S3 operations × 6 bucket/prefix targets + 8 global/admin operations, run with worker and orchestrator credentials |
| `cmd/s11/bpol.go` | bucket policy on the control bucket vs identity Deny, anonymous access |
| `cmd/s11/regflow.go` | registration flow and 30 attack/rotation/revocation checks on real dynamitedb |
| `cmd/s11/sts.go` | STS AssumeRole with session policy, expiry, chaining, tampering |
| `policies/` | **shipped artefacts**: `worker.json`, `orchestrator.json`, `rustfs-worker-sts-parent.json`, `seaweedfs-security.toml` |
| `scripts/` | server start/setup (RustFS via `mc admin`, SeaweedFS static config + IAM config + mTLS), bypass probe, STS expiry |
| `measure.sh` | reruns everything on fresh servers (~8 min + 15 min STS expiry wait; `SKIP_EXPIRY=1` skips the wait). Ran clean end to end on 2026-10-21 except SeaweedFS 4.36, which needed a flag fix in `weed-up.sh` and was then run separately |

Software: **dynamitedb v0.6.1 = HEAD c0badfc** (real library, no stand-in). **RustFS 1.0.3**
(upstream release `rustfs-linux-aarch64-musl-v1.0.3.zip`, sha256 `28e973c6…`; not in nixpkgs at
the pin). **SeaweedFS 4.48** (upstream `linux_arm64.tar.gz`, sha256 `557e92c3…`) and **4.36**
(nixpkgs pin). Single-node servers on 127.0.0.1:21100–21123. Go 1.26.9 stdlib `crypto/hpke`.

## 3. Results

### 3.1 S3 policy matrix (real requests)

Worker credential with `policies/worker.json`; orchestrator with `orchestrator.json`. Each probe
starts from a fresh seed object; bucket-level changes are reset after every probe and checked with
the admin credential. Full tables: `spikes/s11-workers/tmp/results/matrix-*.md`.

| Server | Probes per credential | Worker results different from expected |
|---|---|---|
| RustFS 1.0.3 | 170 | **0** |
| SeaweedFS 4.48 (`-s3.config` identities + `policyNames`) | 170 | **0** |
| SeaweedFS 4.48 (same + `-s3.iam.config` for STS) | 170 | **0** |
| SeaweedFS 4.36 (nixpkgs) | 170 | **0** |

Worker outcome per target (same on both servers):

| Target | Allowed (verified OK) | Refused (verified 403) |
|---|---|---|
| `bob-control` | nothing | **all 27**: HeadBucket, ListObjectsV2, ListObjectVersions, ListMultipartUploads, GetObject, HeadObject, ranged GET, GetObjectAcl, GetObjectTagging, presigned GET, PutObject (new, overwrite, If-None-Match), presigned PUT, multipart upload, PutObjectTagging, PutObjectAcl, CopyObject into control, CopyObject out of control into fleet, UploadPartCopy out of control, DeleteObject, DeleteObjects, Get/PutBucketPolicy, PutBucketVersioning, PutBucketLifecycle, PutBucketAcl |
| `bob-fleet/worker/` (orchestrator-owned) | List, Get, Head, ranged/presigned GET, copy *out* | every write: Put (3 kinds), presigned PUT, multipart, CopyObject in, Delete(s), tagging, ACL |
| `bob-fleet/wreg/` | read + Put/multipart/CopyObject in | Delete(s), tagging, ACL, bucket ops |
| `bob-analytics` | read, write, delete, multipart, copy | tagging, ACL, versions, bucket ops |
| `bob-artifacts/nar/` (any non-staging path) | Get/Head/presigned GET | List, every write incl. CopyObject `staging/→nar/`, multipart, delete |
| `bob-artifacts/staging/` | Get, Put, multipart, CopyObject in | Delete(s), List, tagging, ACL, bucket ops |
| global | ListBuckets (names only) | CreateBucket, STS AssumeRole, RustFS admin API (`/rustfs/admin/v3/list-users`, `info-canned-policy`), SeaweedFS IAM API (`ListUsers`, `CreateAccessKey`, `PutUserPolicy` on self) |

The orchestrator credential got OK on every bucket operation it needs (except PutObjectAcl on
SeaweedFS, not needed). ListBuckets cannot be restricted on either server without breaking
nothing bob uses; it only reveals bucket names.

### 3.2 Bucket policies vs identity policies

| Bucket policy on `bob-control` | RustFS: worker GET / PUT | SeaweedFS: worker GET / PUT | anonymous GET (R / S) | user with `s3:*` identity (R / S) |
|---|---|---|---|---|
| none | DENIED / DENIED | DENIED / DENIED | DENIED / DENIED | OK / OK |
| `Allow Principal:* s3:*` (mistake) | **OK / OK** | DENIED / DENIED | **OK / OK** | OK |
| `Allow {AWS:[wkr]} GetObject` | **OK** / DENIED | DENIED / DENIED | DENIED | OK |
| `Deny * unless aws:username ∈ {orch, root}` | DENIED / DENIED | DENIED / DENIED | DENIED | **DENIED / DENIED** |

- **RustFS: a bucket-policy Allow overrides an explicit Deny in the user's identity policy**
  (MinIO semantics). Source: `rustfs/src/storage/access.rs` (tag 1.0.3, ~L1377–1470): bucket-policy
  *deny* is checked first, then the IAM policy, and if IAM does not allow, the bucket-policy
  *allow* is used as fallback. SeaweedFS follows AWS (explicit Deny wins).
- **SeaweedFS 4.36 (nixpkgs pin) behaves like RustFS here**: with `Allow Principal:* s3:*` the
  worker got OK on GET and PUT in the control bucket despite its identity Deny; 4.36 also rejects
  `Principal:{"AWS":["wkr"]}` as `MalformedPolicy`. 4.48 follows AWS. So no server version can be
  trusted to let an identity Deny win.
- A **deny-all-except-orchestrators bucket policy works on all three servers** and also stops a
  mis-scoped worker credential (`wide` user with `s3:*`). bob should ship it as a second layer.

### 3.3 SeaweedFS ports outside S3 (the main finding)

Probed with no credentials. Full log: `tmp/results/bypass-weed*.md`.

| Configuration | filer HTTP GET/PUT/DELETE on `/buckets/bob-control/...` | volume upload | filer gRPC (`ListEntries`, `CreateEntry`, `DeleteEntry`, `AssignVolume`) |
|---|---|---|---|
| default (4.36 and 4.48, both verified) | **200 / 201 / 204** (read, write, delete control data) | 201 | **open** |
| + `jwt.signing`, `jwt.signing.read`, `jwt.filer_signing`, `jwt.filer_signing.read` | 401 | 401 | **still open**: listed control objects, deleted one, created `user/grpc-evil` (S3 read it back as `pwned`), got a volume write JWT |
| + gRPC mTLS (`[grpc] ca` + per-component certs) | 401 | 401 | refused (plaintext and TLS without client cert); positive control with client cert works; S3 still works |

So on SeaweedFS the S3 policies only hold if workers cannot reach master (9333/19333), volume
(8080/18080) and filer (8888/18888) ports, **and** `security.toml` sets the JWT keys and gRPC mTLS.
RustFS exposes one port (S3 + admin API + STS) in the tested setup; its console was disabled
(`--console-enable` off by default), FTPS/SFTP are off by default (not probed further).

### 3.4 Registration flow on dynamitedb (both servers, 31 checks each, 0 failed)

| Check | Result (both servers) |
|---|---|
| worker reads `bob-control/okey/k1` (orchestrator signing key) | DENIED |
| token visible before reconcile | no (`ErrNotFound`) |
| register → reconcile → open sealed token → token + PoP accepted | pass |
| PoP replayed / PoP for another path | rejected (`replayed`, `signature`) |
| second `Create` of the same `wreg/` | `ErrAlreadyExists` |
| attacker (other worker credential holder) opens victim's sealed token | `message authentication failed` |
| replay victim's registration bytes under another id | rejected (id ≠ hash of keys) |
| victim's Ed25519 key + attacker's KEM key | rejected (signature) |
| 25 h old registration | rejected (window) |
| attacker overwrites `wreg/<victim>` (Put allowed) | allowed by S3, **harmless**: keys were copied into `worker/<wid>` at approval; victim still opens its token |
| attacker deletes `wreg/<victim>` / writes `worker/<self>` | DENIED / DENIED (prefix policy) |
| stolen token + attacker's PoP | rejected (`signature`) |
| token signed with another key | rejected |
| sealed token moved to another worker, or opened at another serial | `message authentication failed` (info binds wid/gen/serial) |
| rotation at half-life, only with fresh signed heartbeat | serial 1 → 2; worker without heartbeat not rotated |
| previous serial accepted during overlap; two forced rotations kill an unexpired leaked token | pass (`superseded serial 3 (current 5)`) |
| dead worker | token expires, no reissue |
| revoke → existing valid token on next request | `worker revoked`; never reissued |
| **revoked host re-registers with new keys** | **approved** (inherent: credential = proof, R-FLEET-001) |
| 3 orchestrators × 20 registrations concurrently | each approved exactly once (Create = If-None-Match), all 20 tokens usable |
| 100 registrations in one pass | all approved |

Timings `[8c/15G]`, shared host with other spikes, indicative only (two runs each):

| | RustFS 1.0.3 | SeaweedFS 4.48 |
|---|---|---|
| Register (one Create) | 6–39 ms | 15–208 ms (first call incl. connect) |
| Reconcile pass with 1 registration | 16–33 ms | 20–122 ms |
| Fetch + open sealed token | 3–9 ms | 2–3 ms |
| Revoke (Get + CAS Update) | 11 ms | 15–17 ms |
| 3 orchestrators, 20 regs | 0.8–1.1 s wall | 0.4–1.4 s wall |
| Approving 100 new regs (one orchestrator, sequential) | 10–39 ms/reg | 19–21 ms/reg |
| Idle pass over 120 records (Query + Get each) | 0.37–0.78 s | 0.35–0.53 s |

The idle pass grows linearly with the fleet (one ListObjects + one GET per registration and
worker record). At 1,000 workers expect several seconds per pass; this must be measured by S9/S14.

### 3.5 Crypto cost (`tmp/s11 bench`, 2,000 iterations, 600-byte plaintext)

| Suite | KEM pub | sealed size | keygen | seal | open | Ed25519 verify | profile |
|---|---|---|---|---|---|---|---|
| X25519 (0x0020) | 32 B | 648 B | 0.41 ms | 0.47 ms | 0.29 ms | 0.26 ms | [8c/15G] |
| MLKEM768-X25519 (0x647a) | 1216 B | 1736 B | 0.54 ms | 0.66 ms | 0.38 ms | 0.23 ms | [8c/15G] |
| X25519 | | | 0.29 ms | 0.42 ms | 0.19 ms | 0.25 ms | [2c/4G] |
| MLKEM768-X25519 | | | 0.56 ms | 0.58 ms | 0.36 ms | 0.25 ms | [2c/4G] |

Crypto is negligible next to one S3 round trip (≥ 2 ms). Single-threaded, so the low-power
profile barely differs.

### 3.6 Short-lived S3 credentials (STS AssumeRole)

| Check | RustFS 1.0.3 | SeaweedFS 4.48 |
|---|---|---|
| How | `AssumeRole` signed with a dedicated parent user `wsts` (worker policy without the sts Deny); no RoleArn needed | `AssumeRole` signed by `orch`, `RoleArn=arn:aws:iam::000000000000:role/bob-worker-session` from `-s3.iam.config` (trust policy names `user/orch`) |
| Mint 900 s creds with per-worker session policy | OK, 6–26 ms | OK, 8–66 ms |
| Session policy narrows (`staging/wtest1/*` OK, `staging/wother/*`, `wreg/wother` DENIED) | pass | pass |
| Session policy tries to widen to control | DENIED (intersection) | DENIED |
| Tampered / missing session token | refused (`InvalidRequest`) | refused (`InvalidAccessKeyId`) |
| Temp creds call AssumeRole (chaining) / worker credential calls AssumeRole | DENIED / DENIED | DENIED / DENIED |
| DurationSeconds=60 / 86400 | clamped to 900 s / 12 h | rejected (400 InvalidParameterValue) |
| Expiry: same creds 14 min before / 10 s after `Expiration` | OK / DENIED (`ExpiredToken`) | OK / DENIED (`InvalidAccessKeyId`) |
| Revoke one session early | **no per-session revoke tested**; disabling the parent user blocks all its sessions at once (verified; re-enable restores them); resetting the parent's secret with `mc admin user add` did **not** revoke sessions (verified) | **not possible**: stateless JWT, source `weed/iam/sts/sts_service.go` L920–930 says sessions are valid until expiry (source only, not runtime-tested) |

Server setup needed for STS: SeaweedFS requires `-s3.iam.config` with `sts.signingKey`, and
**`"policy": {"defaultEffect": "Deny"}`** (the wiki says the zero-config default is Allow); with it
the identity matrix still passes (0 unexpected). Without the IAM config the STS endpoint answers
503.

### 3.7 What a stolen token gives

| Attacker holds | Gains |
|---|---|
| token only (log leak, network capture) | nothing: every RPC needs a PoP signed by the worker key (verified) |
| sealed token record from the fleet bucket | nothing: HPKE ciphertext for the victim's key (verified) |
| token + worker private key (worker host compromised) | that worker's identity until revoke or expiry: claim jobs, receive their per-job forge checkout credentials (R-FLEET-006), upload to staging, write logs |
| the shared fleet credential (any worker host compromised) | read the whole fleet bucket (job queue, public keys, ciphertexts; no personal data per R-SEC-003), write `wreg/ whb/ lease/ jstat/` (steal leases, fake job status, flood registrations), analytics read/write/delete, artifacts read + staging write. **Not**: control bucket, `worker/` records, non-staging artifacts, bucket config, IAM/admin APIs (verified). It can register itself again after revocation. |

## 4. Answer

Yes. The owner's design works unchanged on RustFS 1.0.3 and SeaweedFS 4.36/4.48 with the real
dynamitedb library: registration, sealing, approval by racing orchestrators, rotation and
revocation all passed, and a worker credential failed on every control-bucket and artifact write
operation tested. Three things are required that the design did not say:

1. **Prefix-scoped write permissions inside the fleet bucket** (workers must not write `worker/`),
   so that orchestrator records cannot be forged even though all workers share one credential.
2. **A control-bucket deny bucket policy**, because RustFS (and SeaweedFS 4.36) let a bucket-policy
   Allow beat an identity Deny.
3. **On SeaweedFS: JWT keys + gRPC mTLS and network isolation** of master/volume/filer ports.
   Without them a worker reads, writes and deletes control data with no credentials at all.

Revocation of a *worker key* works within one request. Revocation of a *compromised host* does not,
because the shared credential lets it register again; only rotating that credential (or the STS
option below) closes this.

## 5. SPEC input

### 5.1 Registration state machine [proposal]

```
worker:  START ─gen keys─► REGISTERING ─Create wreg/<wid>─► WAITING ─worker/<wid> appears─► ACTIVE
                                            │ ErrAlreadyExists (restart, same keys): continue
         ACTIVE ─poll worker/<wid> every 60 s; new serial─► ACTIVE (swap token)
         ACTIVE ─state=revoked─► STOPPED (exit; a new process gets new keys)
         ACTIVE ─token expired, no new serial─► WAITING (keep beating; orchestrator may reissue)
orchestrator (per wid, every reconcile pass; all writes conditional):
  wreg only        ─validate ok─► Create worker/<wid> {active, gen 0, serial 1, sealed}   (If-None-Match; loser: ErrAlreadyExists)
                   ─invalid────► ignore (GC deletes wreg after 24 h)
  active           ─exp-now < TTL/2 and signed heartbeat age < 2 min─► Update {serial+1, sealed}  (If-Match ETag)
  active           ─admin Revoke─► Update {revoked, gen+1, sealed=∅}                              (If-Match ETag)
  revoked | no heartbeat 24 h ─► GC: delete wreg/, whb/, worker/ (worker/ kept 7 days if revoked, for audit)
```

### 5.2 Record formats (fleet bucket, dynamitedb, CBOR)

| Key | Writer | Fields |
|---|---|---|
| `wreg/<wid>` | worker, `Create` | `b`: canonical CBOR `RegBody{1:v=1, 2:wid, 3:sigpub(32), 4:suite, 5:kempub, 6:platform, 7:caps, 8:labels, 9:created unix, 10:nonce(16)}`; `s`: Ed25519(`"bob-wreg-v1"‖0‖LV(b)`) |
| `worker/<wid>` | orchestrator | `st` active/revoked, `g` gen, `n` serial, `sp` sigpub, `su` suite, `kp` kempub, `pl` platform, `ap` approved, `te` token exp, `ct` HPKE ciphertext of `TokenEnvelope{1:token, 2:exp, 3:S3Grant?}` |
| `whb/<wid>` | worker, `Put` | `at` unix, `s` Ed25519(`"bob-whb-v1"‖wid‖at`) |
| control `okey/<kid>` | orchestrator | Ed25519 seed; old keys kept ≥ token TTL |

Worker id: `"w" + base32-lower-nopad(SHA-256("bob-worker-id-v1" ‖ LV(sigpub) ‖ u16(suite) ‖ LV(kempub)))[:26]`
(passes dynamitedb's key sanitizer `^[A-Za-z0-9@._-]{0,100}$`). LV = 4-byte big-endian length + bytes.

Validation of `wreg/` (hostile input): body ≤ 4 KiB; canonical CBOR (re-encode equals input);
v=1; 32-byte sigpub; 16-byte nonce; known suite; signature valid; wid = hash of keys = object key;
created within [now−24 h, now+5 min].

### 5.3 Crypto

- HPKE `crypto/hpke`: KEM `MLKEM768X25519` (0x647a), KDF HKDF-SHA256, AEAD ChaCha20-Poly1305;
  `info = "bob-wseal-v1"‖0‖LV(wid)‖LV(gen)‖LV(serial)`. Accept 0x0020 (X25519) too.
  Justification: stdlib only (no dependency, principle 6/8), RFC standard, context binding via
  `info`, post-quantum hybrid costs < 0.2 ms and 1.1 KB more (measured).
- Token: `bwt1.` + b64url(canonical CBOR `Claims{1:v,2:aud="bob-worker-api",3:kid,4:wid,5:gen,6:serial,7:iat,8:exp,9:cnf=SHA-256(sigpub)}`) + `.` + b64url(Ed25519(`"bob-wtok-v1"‖0‖LV(claims)`)). Max 2 KiB.
- PoP header [proposal] `Bob-PoP: <unix ts>.<b64url sig>`, sig = Ed25519(`"bob-pop-v1"‖0‖LV(method)‖LV(authority)‖LV(path)‖LV(ts)‖LV(sha256(body))‖LV(sha256(token))`); |skew| ≤ 60 s; per-orchestrator replay cache of signatures for 120 s. Authority in the message stops cross-orchestrator replay.
- Accept a token iff: signature (kid known), not expired, aud, worker record `active`, `gen` equal, `serial ≥ record.serial−1`, `cnf` matches record sigpub, PoP valid.
- Budgets [proposal]: token TTL 1 h, rotate at 30 min left, heartbeat every 30 s, stale after 2 min, worker record cache 10 s → revocation effective ≤ 10 s.
- Error texts [proposal]: `worker registration rejected: <reason>` (orchestrator log only), RPC `unauthenticated: worker token invalid`, `unauthenticated: worker revoked`, `unauthenticated: proof of possession invalid`.

### 5.4 Shipped S3 policies (identical JSON on RustFS and SeaweedFS)

`policies/worker.json` (verified on both servers):

```json
{"Version":"2012-10-17","Statement":[
 {"Sid":"DenyControlAlways","Effect":"Deny","Action":["s3:*"],"Resource":["arn:aws:s3:::bob-control","arn:aws:s3:::bob-control/*"]},
 {"Sid":"DenySts","Effect":"Deny","Action":["sts:*"],"Resource":["arn:aws:s3:::*"]},
 {"Sid":"FleetList","Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::bob-fleet"]},
 {"Sid":"FleetRead","Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bob-fleet/*"]},
 {"Sid":"FleetWriteWorkerPrefixes","Effect":"Allow","Action":["s3:PutObject"],"Resource":["arn:aws:s3:::bob-fleet/wreg/*","arn:aws:s3:::bob-fleet/whb/*","arn:aws:s3:::bob-fleet/lease/*","arn:aws:s3:::bob-fleet/jstat/*"]},
 {"Sid":"FleetDeleteWorkerPrefixes","Effect":"Allow","Action":["s3:DeleteObject"],"Resource":["arn:aws:s3:::bob-fleet/whb/*","arn:aws:s3:::bob-fleet/lease/*"]},
 {"Sid":"AnalyticsList","Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::bob-analytics"]},
 {"Sid":"AnalyticsRW","Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::bob-analytics/*"]},
 {"Sid":"ArtifactsRead","Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::bob-artifacts/*"]},
 {"Sid":"ArtifactsStagingWrite","Effect":"Allow","Action":["s3:PutObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],"Resource":["arn:aws:s3:::bob-artifacts/staging/*"]}]}
```

(RustFS rejects `iam:*` and `admin:*` actions and a `"*"`
resource in sts statements, so the statement is `sts:*` on `arn:aws:s3:::*`.)
`orchestrator.json`: `s3:*` on the four buckets and their objects. Control-bucket bucket policy
(second layer; verified on both):

```json
{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":["s3:*"],
 "Resource":["arn:aws:s3:::bob-control","arn:aws:s3:::bob-control/*"],
 "Condition":{"StringNotEquals":{"aws:username":["<orchestrator user>","<admin user>"]}}}]}
```

Lake (`bob-analytics`) may need more actions (e.g. delete for compaction is granted); S10 must
confirm the exact set.

Per server:

| | RustFS 1.0.3 | SeaweedFS 4.36 / 4.48 |
|---|---|---|
| Users | `mc admin user add`, `mc admin policy create/attach` (MinIO admin API on the S3 port, `/rustfs/admin/v3`); stored in the cluster | static `-s3.config` JSON: `identities[{name, credentials[{accessKey,secretKey}], policyNames:["bob-worker"]}]`, `policies[{name, content:"<policy JSON as string>"}]` (SIGHUP reload); alternatives: `weed shell s3.user.*`, embedded IAM API, Admin UI (not tested) |
| Policy language | AWS IAM JSON; rejects `iam:*`, `admin:*`; prefix resources work | AWS IAM JSON via `policyNames`; legacy `Action:bucket/prefix` strings exist (not used) |
| Bucket policies | supported; **Allow overrides identity Deny** | 4.48: AWS semantics; 4.36: **Allow overrides identity Deny** too; `Principal:*` Allow gave anonymous GET but not PUT on both |
| STS | `AssumeRole` (MinIO style: caller's own policy ∩ session policy), 900 s–12 h clamped | `AssumeRole` with roles/trust policies in `-s3.iam.config`; needs `signingKey` and `defaultEffect: Deny`; 900 s–12 h, out of range rejected |
| Extra hardening | console off; FTPS/SFTP off (defaults, not probed) | `security.toml`: 4 JWT keys + gRPC mTLS; firewall master/volume/filer ports from workers |
| Not supported / not verified | per-session STS revoke (admin `revoke-tokens` exists in source, untested); secret reset via `mc admin user add` did not end sessions | session revoke (stateless JWT); `mc pipe` (streaming PUT) failed with Content-MD5 mismatch under the JWT config, `aws s3api put-object` worked |

bob-server self-check at start [proposal]: with the configured worker credential (optional
setting), `HeadObject bob-control/<probe>` must be 403, and `GetBucketPolicy bob-control` must
contain the deny statement; refuse to start otherwise (R-SEC-002 test).

### 5.5 Threats and mitigations

| # | Threat | Mitigation | Evidence |
|---|---|---|---|
| T1 | worker reads/changes control data via S3 | identity policy + control deny bucket policy | matrix, bpol |
| T2 | SeaweedFS filer/volume/gRPC bypass | JWT keys, gRPC mTLS, network isolation | 3.3 |
| T3 | bucket-policy misconfiguration opens control (RustFS) | deny bucket policy; self-check; orchestrator never writes bucket policies | 3.2 |
| T4 | worker forges `worker/<wid>` (self-approval) | prefix policy; token is orchestrator-signed anyway | regflow F, I |
| T5 | replay or tampering of a registration | wid = hash(keys), self-signature, time window, keys copied at approval | regflow B, C, D, G |
| T6 | stolen token | PoP with worker key; 1 h TTL; forced rotation | regflow H, forced rotation |
| T7 | stolen sealed record | HPKE to the worker key; info binds wid/gen/serial | regflow A, J |
| T8 | registration flood | approve rate limit per pass [proposal: 50], cap pending regs [proposal: 1,000], GC after 24 h | flood cost measured |
| T9 | compromised worker host | R-SEC-000; revoke key; rotate shared credential; STS option | LIMIT check |
| T10 | lease / job-status tampering by another worker | job results accepted only via authenticated RPC or signed by the worker key; S9/S14 | design note |
| T11 | analytics tampering/deletion by a worker | accepted by D38; worker-written rows carry wid; deny DeleteObject if lake allows (S10) | |
| T12 | orchestrator signing key theft | key only in control bucket; kid rotation | regflow (worker DENIED on okey) |

## 6. Decisions and open items

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S11-1 | HPKE from Go stdlib, MLKEM768-X25519 suite | no dependency, standard, measured cost negligible (principles 6, 8, 9) |
| S11-2 | Ed25519 signing key separate from the KEM key; both hashed into the worker id | PoP needs signatures; hashing binds both keys |
| S11-3 | worker keys live in memory only; a restart is a new worker | nothing to steal from disk; old ids expire via GC |
| S11-4 | signed stateless token + per-request worker-record check | revocation within the cache window without a control-bucket read |
| S11-5 | rotation by the reconciler (fleet bucket), not by an RPC | one mechanism, works without orchestrator connectivity |
| S11-6 | prefix-scoped worker writes in the fleet bucket (`wreg/ whb/ lease/ jstat/`) | structural boundary (principle 5) |
| S11-7 | ship the control deny bucket policy | RustFS and SeaweedFS 4.36: bucket Allow beats identity Deny |
| S11-8 | SeaweedFS JWT + mTLS + port isolation is a documented deployment requirement | verified bypass otherwise |
| S11-9 | used real dynamitedb, no stand-in | it worked; Create/Update give the needed CAS |

### Owner decision needed

**OD-S11-1: How should a compromised worker host be cut off?**
- **a)** Keep the design as is: one long-lived shared worker credential. Revocation stops the key;
  to cut off a host, the operator rotates the shared credential on all workers (runbook).
- **b)** Keep registration exactly as designed, but use a narrow long-lived *bootstrap*
  credential (write `wreg/` only, read `worker/`), and deliver per-worker **STS credentials
  (15–60 min, session policy per wid)** inside the sealed token. A revoked worker stops getting
  S3 access within the STS lifetime; leases/staging can be scoped to its own wid. Verified on both
  servers (3.6). Costs: SeaweedFS needs `-s3.iam.config`; RustFS needs one extra parent user held
  by orchestrators; a re-registering host still gets new credentials unless (c) is on.
- **c)** (b) plus an optional "approval required" mode where an admin approves new worker ids
  (breaks zero-setup when enabled; off by default).
- **Recommendation: b.** It keeps zero-setup and the owner's flow, makes revocation real for S3
  access, and limits what one stolen credential can touch. (c) can come later.

### Open risks

- SeaweedFS is safe only with correct `security.toml` and network layout; a single missed port
  undoes the bucket split. bob cannot check this from the S3 API.
- RustFS bucket-policy semantics differ from AWS; any operator-added bucket policy can open the
  control bucket unless bob's deny policy is in place.
- The shared fleet credential lets a compromised worker steal leases and with them forge checkout
  credentials for any project's jobs (R-FLEET-006 limits scope per job, not per worker).
- Reconcile passes scan all registrations; cost at 1,000+ workers not measured.
- RustFS 1.0.3 is not in the nixpkgs pin; bob's harness would download the release binary.

### Still to verify

- RustFS `revoke-tokens` admin API, SeaweedFS
  session behaviour after a role/policy change.
- SeaweedFS legacy `Action:bucket/prefix` identity format and the Admin UI path (not tested).
- RustFS FTPS/SFTP/console when enabled (out of scope; keep disabled).
- Exact lake action set (S10) and lease/job-status prefixes (S9/S14).
- Quiet-machine timings: rerun `spikes/s11-workers/measure.sh`; all timings above depend on it.
