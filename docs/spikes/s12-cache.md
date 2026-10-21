# S12 — Nix binary cache and OCI registry on the artifacts bucket

Date: 2026-10-20. Host: see `docs/environment.md`. All numbers are indicative: the machine ran
other spikes at the same time (load average 17–35 during measurements).

## 1. Question and requirements

How does the orchestrator serve a Nix binary cache and an OCI pull registry from the artifacts
bucket? How do workers upload, how does the orchestrator verify, sign and promote, and how do
real Nix and OCI clients authenticate?

Requirements: R-REG-001..005 (cache, signing on orchestrators, OCI pull, releases, no
cache.nixos.org mirror), R-STORE-001/002/006/007, R-AUTH-005/007, R-SEC-001/002/004/005,
R-NFR-003/006/007/008, D9, D13, D19, D37, D38.

## 2. What was built

Code in `spikes/s12-cache/` (module `bobspike/s12`, Go 1.26.9, `CGO_ENABLED=0`):

| Path | What |
|---|---|
| `nixfmt/` | Nix base32, narinfo parse/format, fingerprint, ed25519 keys and signatures in Nix format (own code, no Nix library) |
| `nar/` | Streaming, validating NAR reader (no buffering of file contents), used for verification and `.ls` |
| `store/` | Artifacts bucket wrapper (aws-sdk-go-v2, same SDK as dynamitedb) and the key layout |
| `cmd/bobup` | Worker side: closure → filter upstream paths → `nix-store --dump` → sha256 → zstd → sha256 → S3 multipart into `staging/<build>/`; optional one-layer OCI image of a closure; `COMPLETE.json` last |
| `cmd/cachesrv` | Orchestrator side: `/nix/*` binary cache, `/v2/*` OCI pull API, `/oci/token`, `/api/whoami`, `/internal/promote` (verify, sign, publish) |
| `cmd/keygen`, `cmd/s3probe`, `cmd/tamper` | Key generation / token hashes; S3 policy and feature probe; "compromised worker" overwrite |
| `scripts/` | `s3-up.sh` (SeaweedFS 4.36 `weed mini`, ports 21200–21219, two identities), `srv-up.sh`, `nix-auth-matrix.sh`, `negcache.sh`, `rootless-nix.sh`, `atomicity.sh` |
| `fixture/closure.nix` | Test closure (spike tooling only): 4 locally built paths, 100.7 MiB NAR (40 MiB random, 40 MiB base64 text, 2,000 small files, one script) + a reference to upstream `hello`; optional 1 GiB path |
| `measure.sh` | Rerunnable throughput measurement, `./measure.sh 8c|2c [runs]` |

S3 backend: SeaweedFS 4.36 from the pinned nixpkgs (RustFS is not in the pin). Tools from the
pin: skopeo 1.22.2, crane 0.21.6, podman 5.8.8. Nix clients: host Nix 2.34.8 and the static
Nix 2.34.9 inside the S0b user namespace (`bobns`, as `bobtest`).

## 3. Results

### 3.1 Signing and format (all verified with real Nix)

| Check | Result |
|---|---|
| Go Nix-base32 vs `nix hash convert --to nix32` | identical (unit test `nixfmt/nixfmt_test.go`) |
| Go-generated key, Go-signed narinfo, `nix store verify --store http://…/nix --trusted-public-keys <go pub>` on 4 paths | exit 0 |
| Same with the cache.nixos.org key as the only trusted key | `path … is untrusted` |
| Go signature vs `nix store sign --key-file <same key>` on a `file://` copy | **byte-identical** `Sig:` line (ed25519 is deterministic) |
| Key from `nix key generate-secret` parsed by Go; Go-derived public key == `nix key convert-secret-to-public` | identical |
| `nix path-info --store <bob> --sigs`, `nix store ls --store <bob>` (uses `.ls`) | work |

Fingerprint signed: `1;<store path>;<NarHash sha256:nix32>;<NarSize>;<refs as full paths, sorted, comma-separated>`.

### 3.2 Nix client authentication (host Nix 2.34.8, fresh store and XDG cache per case)

| Mechanism | Result | Server saw |
|---|---|---|
| `netrc-file` with `password <token>` | ✓ substituted 9 paths | `Authorization: Basic`, sent **preemptively** on the first request |
| no credentials, private path | ✗ `no substituter that can build it` | 404 on narinfo (by design) |
| `access-tokens = 127.0.0.1:21220=<token>` | ✗ not applied to substituters | no `Authorization` header |
| token in URL userinfo `http://bob:<token>@host/nix` | ✓ works, but **leaks the token** into normal output (`copying path … from 'http://bob:alice-token-123@…'`, 17 times with `-v`) | Basic |
| wrong token, cold cache | ✗ Nix prints `warning: unable to download '…/nix-cache-info': HTTP error 401` **plus the response body**, then drops the substituter | 401 on `nix-cache-info` |
| wrong token, warm narinfo cache | ✗ **silent**: `don't know how to build these paths`, no warning; `nix-cache-info` is not re-fetched | 401 on narinfo |
| Bearer header | not possible: `nix help-stores` for HTTP stores has no header setting; strings in the static binary show only Basic/Digest from curl | — |
| TLS client certificate | `tls-certificate`/`tls-private-key` store settings exist | not tested |

Rootless static Nix 2.34.9 in the S0b namespace as `bobtest`, bob-written `nix.conf` + netrc
only: substituted the closure (bob paths from bob, `hello`+glibc from cache.nixos.org) in
1.8 s [8c/15G], signature present, binary runs.

### 3.3 Negative caching and promotion

`negcache.sh`: the client asks for a path before promotion (404), the orchestrator promotes,
the client asks again with the same XDG cache.

| `narinfo-cache-negative-ttl` | right after promotion | after TTL+1 s |
|---|---|---|
| default (3600) | ✗ still `no substituter` | (not waited) |
| 5 | ✗ | ✓ |
| 0 | ✓ | — |

A positive answer is cached too (`narinfo-cache-positive-ttl`, default 30 days), and
`nix-cache-info` is cached in `binary-cache-v7.sqlite`.

### 3.4 Large NARs, ranges, memory

| Test | Result |
|---|---|
| 1 GiB incompressible path: worker upload | 4.6–7.5 s, worker max RSS 132–134 MiB (4 parts × 16 MiB in flight + zstd) [8c/15G] |
| 1 GiB: promotion | 4.4–5.6 s: verify 2.3–3.0 s (stream: zstd decode + NAR parse + 2× sha256), **CopyObject 2.6–2.9 s** (SeaweedFS copies data; it is not a metadata copy) |
| 1 GiB: Nix substitution | 3.9 s; curl 1.07 s (~1 GB/s) |
| Orchestrator max RSS after promoting and serving 2 × 1 GiB | **19 MiB** (whole process); 32–33 MiB after `measure.sh` |
| Connection cut after 10,000,000 bytes (fault injection) | Nix: `retrying from offset 10000000 in 270 ms (attempt 1/5)`, sent `Range: bytes=10000000-`, got 206, path valid |
| Range on S3 | SeaweedFS honours ranges; beyond end → 416 `InvalidRange`, passed through |

### 3.5 Promotion protocol, failure cases (`scripts/atomicity.sh`)

| Case | Result |
|---|---|
| Worker dies after 2 of 4 NARs (no `COMPLETE.json`) | promote refused: `staging incomplete: staging/c1/COMPLETE.json missing`; nothing visible |
| Compromised worker replaces one staged NAR after upload | `NAR invalid: nar: string too long … at 8`; that path and its dependents stay 404; an independent sibling at the same topological level (rand) was already published — it is complete and correct |
| Orchestrator killed 2.5 s into a 1.1 GiB promotion | after kill: rand, text published; big, many, top 404 (never a dependent before its references); re-run: `promoted 3, alreadyThere 2` |
| Two orchestrators promote the same build at once | one `promoted 4`, the other `alreadyThere 4` (create-only narinfo write); `nix store verify` OK |
| Same input-addressed path rebuilt with different bytes | second promotion: `nonReproducible: [/nix/store/…-s12-big-c5]`, first publication kept |
| Copy with a wrong `x-amz-copy-source-if-match` | 412, no object (s3probe) |
| PUT `If-None-Match: *` on an existing key | 412, content unchanged (s3probe) |

### 3.6 S3 credential boundary (SeaweedFS identities, `s3probe`)

Worker identity: `Write:artifacts/staging/*`, `Read:artifacts/staging/*`, `List:artifacts`.

| Worker action | Result |
|---|---|
| PUT/GET `staging/…` | allowed |
| PUT `nix/narinfo/…`, PUT `oci/blobs/…`, GET `nix/…`, DELETE `nix/…`, COPY staging→`nix/…` | **403** |
| LIST `nix/` | allowed (`List` is bucket-wide in this config: leaks store hashes only) |
| Lifecycle rule `staging/` expire 7 d (orchestrator) | accepted by the API; expiry itself **not verified** |

### 3.7 OCI registry

Images: `bobup -image` builds a deterministic one-layer gzip image of a closure (all store paths
under `/nix/store`, mtime 1, Entrypoint `<path>/bin/<first>`). Promoted as `demo/hello:v1`
(private, project demo, 100.6 MiB layer) and `public/hello:2.12.3` (public, 14.6 MiB).

| Client | Case | Result |
|---|---|---|
| crane 0.21.6 | anonymous, public repo | ✓ manifest |
| crane | anonymous, private repo | ✗ `UNAUTHORIZED: authentication required` |
| crane | service account `sa-ci` (docker config), private | ✓ manifest, `crane ls` → `v1` |
| crane | valid user of another project | ✗ `UNAUTHORIZED` (same as missing — no existence leak) |
| crane | wrong secret | ✗ token endpoint `invalid service account credentials` |
| skopeo 1.22.2 | `inspect` anonymous public; `copy --src-creds` private → `oci:` dir | ✓ / ✓ |
| skopeo | push with valid service-account creds | ✗ refused (`authentication required`; push scope is never granted) |
| podman 5.8.8 as root | pull + run public image | ✓ `Hello, world!` |
| podman **rootless** as `bobtest` | `login`, pull private + public, run both | ✓ `rootless-podman-public-ok`, `rootless-podman-private-ok` |

Flow seen by the server (skopeo): `GET /v2/` 401 → `GET /oci/token` (Basic) 200 → manifest,
config, layer with `Bearer`; layer 100.6 MiB in 0.40 s. Podman needed a `policy.json`; the
spike put it in a scratch `HOME`, never in `/etc` or `/root/.config`.

### 3.8 Throughput, 100.7 MiB closure (4 NARs, 95.8 MiB after zstd) — `measure.sh`

Server, client and worker on one host; SeaweedFS on the same disk. Upload time includes
`nix path-info` and the HEAD requests to cache.nixos.org (network). In [2c/4G] the worker,
the orchestrator and the Nix client each run in their own 2 vCPU / 4 GiB cgroup; SeaweedFS is
not limited.

| profile | run | upload s | upload MiB/s | promote s | nix substitute s | nix MiB/s | curl NARs s | curl MiB/s |
|---|---|---|---|---|---|---|---|---|
| [8c/15G] | 1 | 2.22 | 45 | 1.10 | 1.54 | 65 | 0.72 | 140 |
| [8c/15G] | 2 | 1.60 | 63 | 1.40 | 1.36 | 74 | 0.40 | 250 |
| [8c/15G] | 3 | 2.38 | 42 | 1.81 | 2.05 | 49 | 0.59 | 171 |
| [2c/4G] | 1 | 2.13 | 47 | 2.27 | 1.84 | 55 | 1.17 | 86 |
| [2c/4G] | 2 | 1.38 | 73 | 1.24 | 1.11 | 91 | 0.61 | 164 |
| [2c/4G] | 3 | 1.19 | 84 | 0.89 | 1.24 | 81 | 0.60 | 168 |

Worker max RSS 191–222 MiB with `-j 4` parallel NARs; orchestrator 33 MiB. [2c/4G] is not slower
than [8c/15G]: the bottleneck here is the shared disk/S3 and machine load, not client CPU. All
numbers in this table must be re-measured on a quiet machine (`./measure.sh 8c 5; ./measure.sh 2c 5`).

Compression on a real closure (skopeo, 64 paths, 188.6 MiB NAR), single thread [8c/15G, loaded]:

| zstd level | ratio | MiB/s |
|---|---|---|
| 1 | 3.12 | 222 |
| 3 | 3.55 | 126 |
| 9 | 4.12 | 35 |
| 19 | 4.78 | 2 |

## 4. Answer

- **Layout:** workers write only `staging/<build>/…`; orchestrators verify every staged NAR by
  streaming it once (zstd → NAR parser → hashes), copy it with an ETag guard to the served
  prefix, write `.ls`, the visibility marker, and the **signed narinfo last, create-only**,
  in topological order. Every step is idempotent; promotion is re-run until done. This held
  under worker death, tampering, orchestrator kill, concurrent orchestrators and
  non-reproducible rebuilds (§3.5).
- **Signing in Go** is byte-identical to Nix and verified with `nix store verify`.
- **Nix auth = netrc Basic (password = bob token).** `access-tokens` does not apply, Bearer
  is impossible, URL userinfo leaks the token. Nix hides 401s once its cache is warm, so the
  bob CLI checks credentials with `GET /api/whoami` (same token) before every Nix invocation
  that uses the server and translates a 401 into its own error.
- **Negative cache:** bob passes `narinfo-cache-negative-ttl = 0` to the Nix invocation that
  fetches the result of a remote build or a release, and uses a short global default (proposal:
  60 s) otherwise.
- **Streaming works:** the orchestrator stays at ~20–35 MiB RSS for 1 GiB NARs; Nix resumes
  cut downloads with `Range`, which the server passes through to S3.
- **OCI:** the distribution token flow with HMAC-signed short-lived tokens works with crane,
  skopeo, podman (also rootless). Anonymous tokens cover public repositories. Push is never granted.
- **R-REG-005:** the worker leaves out every closure path that cache.nixos.org already has
  (HEAD narinfo); clients list cache.nixos.org as a second substituter.

## 5. SPEC input

### 5.1 Artifacts bucket key layout [proposal]

```
staging/<build-id>/nar/<storehash>.nar.zst        worker; zstd NAR
staging/<build-id>/narinfo/<storehash>.narinfo    worker; unsigned claim (same text format)
staging/<build-id>/oci/blobs/sha256/<hex>         worker; layers, config, manifest
staging/<build-id>/COMPLETE.json                  worker; written LAST
nix/nar/<storehash>.nar.zst                       orchestrator; immutable
nix/ls/<storehash>.ls                             orchestrator; JSON listing v1
nix/vis/<storehash>/<scope>                       orchestrator; empty; scope = public | p.<project-id>
nix/narinfo/<storehash>.narinfo                   orchestrator; signed; written LAST, If-None-Match:*
oci/blobs/sha256/<hex>                            orchestrator; content-addressed, shared
oci/repos/<repo>/_blobs/<hex>                     orchestrator; empty; repo membership of a blob
oci/repos/<repo>/_vis/<scope>                     orchestrator; empty
oci/repos/<repo>/_tags/<tag>                      orchestrator; body "sha256:<hex>"; written LAST
```

- `<storehash>` = 32-char Nix base32 hash, validated by `^[0-9a-df-np-sv-z]{32}$`; `<build-id>`
  `^[a-z0-9][a-z0-9-]{0,62}$`; repo names by the distribution-spec regex, ≤ 255 bytes;
  tags `^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`. Keys are built only from validated values.
- NARs are keyed by store hash (one NAR per store path, first publication wins), so auth on
  `nar/<storehash>.nar.zst` uses the same visibility check as the narinfo.
- `COMPLETE.json`: `{"build": "...", "paths": ["/nix/store/…"], "images": [{"repo","tag","manifest","mediaType","blobs":[…]}], "skipped": [upstream paths]}`.
- S3 policies (R-SEC-002): worker `Put/Get` on `staging/*` only (+ abort multipart);
  orchestrator full access. Lifecycle: `staging/` expires after 7 days, incomplete multipart
  uploads after 1 day. **SPEC must restrict `List` to `staging/` where the backend allows it.**

### 5.2 Narinfo and signing rules [proposal]

- Served narinfo fields in this order: `StorePath, URL, Compression, FileHash, FileSize,
  NarHash, NarSize, References, Deriver, Sig, CA`. `URL: nar/<storehash>.nar.zst`,
  `Compression: zstd`, hashes `sha256:<nix32>`, references as base names, sorted.
- Signature: ed25519 over `1;<path>;<NarHash>;<NarSize>;<refs full paths sorted, ","-joined>`,
  `Sig: <keyname>:<base64(sig)>`. Key files in Nix format (`name:base64(64 bytes)` /
  `name:base64(32 bytes)`); default key name `<server-name>-1`; rotation = new name, both public
  keys published during overlap (`/api/whoami` returns the list).
- The signing key lives in the control bucket (orchestrators only), never in fleet/artifacts.
- The orchestrator signs only after its own verification: NarHash, NarSize, FileHash, FileSize
  recomputed; NAR structure valid (magic, sorted unique names, no `/`, `.`, `..`, NUL, zero
  padding, nothing after the root node); `StorePath` hash equals the key; references are valid
  store paths. **References are worker claims** (see risks).
- Caps: NAR ≤ 64 GiB, ≤ 100,000 paths per build, ≤ 5,000,000 NAR entries, names ≤ 255 bytes,
  symlink targets ≤ 4,095 bytes, depth ≤ 256, small objects ≤ 1–4 MiB.

### 5.3 Promotion protocol [proposal]

Triggered by the reconciler when a build is `succeeded` and not `promoted` (state in the fleet
or control dynamitedb table; CAS on the transition):

1. Read `COMPLETE.json`. Missing → state `promotion-blocked: staging incomplete` (worker died;
   the job is requeued, R-FLEET-003).
2. Read and validate all claims. Order paths topologically (references inside the set first).
3. Per level, in parallel (4): for each path
   a. narinfo exists → compare NarHash; differ → record `nonReproducible` (R-NFR-006), keep
      the published one; add visibility; done.
   b. GET staged NAR, remember ETag, stream-verify (§5.2).
   c. `CopyObject` to `nix/nar/…` with `x-amz-copy-source-if-match: <ETag>`.
   d. PUT `.ls`; PUT visibility markers.
   e. Sign; PUT narinfo with `If-None-Match: *`; 412 = another orchestrator won = success.
4. Images: verify each blob digest by streaming, copy (ETag guard) unless present, write repo
   blob markers, visibility, then the tag last.
5. Mark build `promoted` (CAS). Staging is deleted by lifecycle, not by promotion (a re-run
   still needs it).

Invariants: a visible narinfo always has its NAR and all in-set references visible; a crash at
any point leaves at most orphan NARs/markers without narinfo, which GC removes after 24 h.
Partial publication of a failed build is possible for complete sub-closures (accepted: each such
path is correct and signed).

### 5.4 HTTP endpoints [proposal]

| Method | Path | Auth | Answer |
|---|---|---|---|
| GET | `/nix/nix-cache-info` | none or valid token; **invalid token → 401** | `StoreDir: /nix/store`, `WantMassQuery: 1`, `Priority: 30` |
| GET/HEAD | `/nix/<hash>.narinfo` | visibility | 200 / 404 (not visible = not there) / 401 invalid token |
| GET/HEAD | `/nix/nar/<hash>.nar.zst` | visibility | 200 / 206 (single `Range`) / 404 / 416 |
| GET | `/nix/<hash>.ls` | visibility | JSON listing |
| GET | `/api/whoami` | token | 200 `{name, projects, nix: {substituter, publicKeys}}` / 401 JSON |
| GET | `/v2/` | Bearer | 401 + `WWW-Authenticate: Bearer realm="<base>/oci/token",service="bob"` / 200 |
| GET/HEAD | `/v2/<repo>/manifests/<tag or digest>` | Bearer scope pull | manifest, `Docker-Content-Digest` |
| GET/HEAD | `/v2/<repo>/blobs/<digest>` | Bearer scope pull | blob, ranges |
| GET | `/v2/<repo>/tags/list` | Bearer scope pull | `{name, tags}` |
| GET | `/v2/<repo>/referrers/<digest>` | Bearer scope pull | empty OCI index (v1) |
| any other method on `/v2/` | — | 405 `UNSUPPORTED` "this registry is pull-only" |
| GET | `/oci/token?service=bob&scope=repository:<repo>:pull` | Basic (service account or user token) or none | `{token, access_token, expires_in: 900, issued_at}`; scopes the caller may not read are left out |

Headers: narinfo `Cache-Control: public, max-age=300`; NAR and blobs
`Cache-Control: public, max-age=31536000, immutable` (private objects: `private` — to be fixed
in SPEC; the spike sends `public`).

OCI tokens: `bob-oci-v1.<base64url(json{sub, exp, acc{repo:[pull]}, prj})>.<base64url(HMAC-SHA256)>`,
TTL 15 min, HMAC key shared by all orchestrators through the control bucket. Private-but-
not-allowed and missing repos give the same 401 (`insufficient_scope`).

### 5.5 Client configuration written by bob [proposal]

```
substituters = https://<server>/nix https://cache.nixos.org
trusted-public-keys = <server-key(s)> cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
netrc-file = <bob state>/etc/netrc          # machine <server> login bob password <token>, mode 0600
narinfo-cache-negative-ttl = 60
```

- Before any Nix call that may hit the server: `GET /api/whoami` with the same token (cached
  for the process). 401 → bob error `BOB-AUTH-001: your bob login has expired or is invalid. Run 'bob login'.`
  Unreachable → `BOB-CACHE-001: the bob server <url> is not reachable; continuing without it` (Nix still works from cache.nixos.org, D37).
- After `bob build --remote` / for `bob install`: the fetching Nix call gets
  `--option narinfo-cache-negative-ttl 0`.
- Anonymous (public releases): no netrc entry for the server.
- Workers substitute from the server the same way with their worker token.

### 5.6 Error behaviour [proposal]

| Situation | Server | What the user sees (bob) |
|---|---|---|
| invalid/expired token | 401 + `WWW-Authenticate: Basic realm="bob"` and body `unauthorized: token invalid or expired (run 'bob login')` | BOB-AUTH-001 (from whoami) |
| path not visible / not there | 404 | nothing; Nix falls through to cache.nixos.org or builds |
| S3 unavailable | 503 `storage unavailable` | Nix retries (5 attempts), then bob: BOB-CACHE-002 server storage unavailable |
| connection cut mid-NAR | — | Nix resumes with `Range` (verified) |
| staging incomplete | promotion state `blocked: staging incomplete` | build page: "upload from worker did not finish; retrying" |
| NAR or blob verification failed | promotion `failed: artifact verification` + worker id | build fails; event logged as a security signal |

### 5.7 Budgets [proposal, re-measure on a quiet machine]

| Budget | Value |
|---|---|
| Orchestrator memory per NAR stream (serve or verify) | ≤ 1 MiB + zstd decoder window; process ≤ 64 MiB at 10 parallel 1 GiB transfers (measured 19–33 MiB for 1–2) |
| Worker upload memory | ≤ 256 MiB at `-j 4` × 4 parts × 16 MiB (measured 222 MiB); SPEC default part 8 MiB × 2 → ≤ 128 MiB |
| 100 MiB closure, upload (worker → staging) | ≤ 3 s [8c/15G], ≤ 3 s [2c/4G] (measured 1.2–2.4 s) |
| 100 MiB closure, promotion | ≤ 3 s (measured 0.9–2.3 s) |
| 100 MiB closure, Nix substitution from bob, same LAN | ≤ 3 s (measured 1.1–2.1 s) |
| narinfo latency (orchestrator, S3 local) | p50 ≤ 5 ms (measured 2–5 ms, includes a visibility LIST/HEAD) |
| zstd level for NARs | 3 (3.55× at 126 MiB/s single thread on a real closure) |

## 6. Decisions, risks, open points

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S12-1 | Nix auth via netrc Basic, password = bob token, user `bob` | only working mechanism without leaking (§3.2), principle 2 |
| S12-2 | `GET /api/whoami` before Nix calls | Nix hides 401 with a warm cache (§3.2), principle 2 |
| S12-3 | Invalid token → 401 everywhere; not visible → 404 | Nix drops a broken substituter; no existence leak |
| S12-4 | NAR key by store hash, first publication wins, report non-reproducible | immutability, simple auth (principles 1, 8) |
| S12-5 | Verify-then-`CopyObject` with ETag guard instead of tee-upload | one code path, guard verified on SeaweedFS; copy cost measured (§3.4) |
| S12-6 | Visibility markers as empty objects in the artifacts bucket | per-request auth with one HEAD/LIST, no dynamitedb on the hot path; S3-only (principle 6) |
| S12-7 | Signed narinfo last, `If-None-Match: *`; tag last for OCI | atomic publish without a coordinator (R-SRV-004) |
| S12-8 | SeaweedFS as the spike S3 | in the pin; RustFS is not |
| S12-9 | aws-sdk-go-v2 + klauspost/compress (Apache-2.0 / BSD-3) | same S3 SDK as dynamitedb; no cgo |
| S12-10 | OCI tokens HMAC-signed, own format, 15 min | no JWT dependency; clients treat tokens as opaque |
| S12-11 | Skip closure paths that cache.nixos.org has, checked by the worker | R-REG-005 / D37 |

### Owner decision needed

**O-S12-1: How are private cache paths scoped?**
- a) Per-path visibility markers (`public`, `p.<project>`), one substituter URL for everything
  (built and measured here). A user with access to project A sees A's paths, also paths shared
  with other projects.
- b) One substituter URL per project (`/nix/p/<project>`), bob writes several substituters.
  Simpler auth, but more Nix requests per missing path (one per substituter).
- c) No per-project scoping for the cache: any authenticated user may read every bob-built
  path; only releases are public. Simplest; weaker isolation inside the org.

Recommendation: **a)** — matches RBAC per project (D14′) with one URL and measured 2–5 ms narinfo.

**O-S12-2: Where do NAR and blob downloads go?**
- a) Always through the orchestrator (built here; ~1 GB/s per stream measured locally).
- b) Orchestrator answers with a 302 to a presigned S3 URL (offloads bandwidth; needs the S3
  endpoint reachable by every client; Nix follows redirects — not verified).
- c) a) by default, b) as an option.

Recommendation: **c)**, with b) to be verified before it is offered.

### Open risks

1. **References are worker claims.** A compromised worker can sign-launder a path with wrong
   references (missing references → broken closures at the client). Mitigation proposal: the
   orchestrator scans the NAR for the 32-char hashes of all closure paths it knows and refuses if
   a found hash is missing from the claim (cheap during the existing stream). Not built.
2. **Worker-built ≠ trustworthy:** signing vouches for what a worker built; R-SEC-000 isolation is the real boundary.
3. SeaweedFS `CopyObject` copies the bytes (2.6–2.9 s per GiB). Behaviour on RustFS / Ceph RGW not measured.
4. SeaweedFS `List:artifacts` is bucket-wide for the worker; prefix-scoped List not tested.
5. Lifecycle expiry of `staging/` accepted by the API, effect not verified.
6. Partial publication of a failed build (complete sub-closures) is visible.
7. Timings taken under load average 17–35; re-run `measure.sh`.

### Still to verify

- TLS (`https`), TLS client certificates as an alternative to netrc.
- Presigned redirects with Nix and with OCI clients (O-S12-2 b).
- docker CLI (not available here); Docker Hub-style `docker login` flow is the same as podman's.
- RustFS (other spikes use RustFS 1.0.3 from GitHub releases): run `s3probe` and `measure.sh` against it.
- GC of orphans and of unreferenced NARs (R-STORE-007) — design only.
- HA: two orchestrators with the shared HMAC secret were used for promotion only, not for token
  validation across instances.
