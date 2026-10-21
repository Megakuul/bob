# ADR 0014 — Nix cache and OCI registry on the artifacts bucket

- **Status:** Accepted (Gate S pending)
- **Date:** 2026-10-20
- **Requirements:** R-REG-001..005, R-STORE-002/006/007, R-AUTH-005, R-SEC-001/002/004/005, R-NFR-003/006/007
- **Evidence:** `docs/spikes/s12-cache.md`, code in `spikes/s12-cache/`

## Context

Orchestrators serve the Nix binary cache and an OCI pull registry from the artifacts bucket.
Workers may write only `staging/`. Signing stays on orchestrators. Nix hides 401 errors.

## Decision

1. **Staging → promotion.** Workers upload zstd NARs, unsigned narinfo claims and OCI blobs to
   `staging/<build>/`, then `COMPLETE.json` last. An orchestrator stream-verifies every object
   (NAR structure, NarHash/FileHash/sizes, blob digests), copies it to the served prefix with an
   ETag guard, writes listing and visibility markers, and writes the **signed narinfo last and
   create-only** (`If-None-Match: *`), in topological order. OCI tags are written last. All
   steps are idempotent; concurrent or crashed promotions are safe (verified).
2. **Signing** is done in Go (ed25519, Nix fingerprint and key format); output is byte-identical
   to `nix store sign`.
3. **Nix authentication** uses a netrc entry (Basic, password = bob token). `access-tokens`,
   Bearer headers and URL userinfo are not used (do not work or leak). The bob CLI checks the
   token with `GET /api/whoami` before Nix uses the server; invalid tokens get 401 everywhere,
   paths a caller may not see get 404.
4. **Negative cache:** bob sets `narinfo-cache-negative-ttl = 60` globally and `0` for the Nix
   call that fetches a just-promoted remote build or a release.
5. **OCI:** distribution-spec pull API with the token flow (`/oci/token`, HMAC-signed 15-minute
   tokens, anonymous tokens for public repositories, push never granted).
6. **R-REG-005:** workers stage only closure paths that cache.nixos.org does not have.
7. **Access scope:** per-object visibility (owner decision D58 = O-S12-1 a). See Owner amendments.

## Consequences

- One substituter URL per server; per-request auth costs one S3 HEAD or LIST (2–5 ms measured).
- Promotion reads every byte once and copies it once (on SeaweedFS the copy is a data copy:
  ~2.7 s/GiB).
- Orchestrator memory stays flat for large NARs (19–33 MiB process RSS measured).
- Staging is cleaned by an S3 lifecycle rule; orphans (NAR without narinfo) by GC.
- Open: reference claims are trusted from workers (mitigation: hash scan during verification);
  presigned redirects (O-S12-2) unverified.

## Rejected

- Serving narinfo that points into `staging/` (no copy): a worker could delete or replace
  published data.
- Workers writing final keys: violates R-STORE-002 / D13.
- Token in the substituter URL: printed by Nix in normal output.
- Mirroring cache.nixos.org: R-REG-005 / D37.

## Owner amendments

- **D58** (O-S12-1 a): one cache URL, visibility per path. Implemented as **one visibility
  document per object** (`nix/vis/<storehash>.json`, `oci/vis/sha256/<hex>.json`:
  `{"v":1,"projects":[…],"public":[<release ids>]}`, CAS-written) instead of one empty marker
  object per scope, so each request costs one GET (cached 30 s) and no LIST (spirit of D60).
  OCI per-repo `_blobs`/`_vis` markers and per-tag objects are replaced by the visibility
  documents and one CAS-written `oci/repos/<repo>/index.json`.
- **D61** (O-S12-2 a): all NAR, blob and release-asset downloads stream through the
  orchestrator. Presigned URLs and redirects to the storage backend are rejected (the bucket's
  own auth system must not be exposed; buckets stay interchangeable). The "presigned redirects
  unverified" open point is closed.
- **D63**: RustFS is the reference backend; SeaweedFS (used in the spike) is unsupported. The
  CopyObject cost and lifecycle expiry must be re-measured on RustFS in the harness.
- **D60**: no change to this ADR's hot path beyond the visibility documents (artifacts bucket is
  plain S3; LIST is used only by GC and key-retirement sweeps).
- **ADR 0016 / D53**: promotion runs **per job**; staging prefix is `staging/<buildID>/<jobID>/<attempt>/` (SPEC §05.2.4).
- SPEC changes beyond the owner decisions: the CLI checks the token with the
  `bob.auth.v1.AuthService/WhoAmI` RPC instead of `GET /api/whoami`; Nix uses a **download-scoped
  token** (`bob_dt_`) in netrc; the reference-scan mitigation for worker-claimed references is
  mandatory; `Deriver` is omitted from served narinfo; private objects are served with
  `Cache-Control: private`. Exact protocol: SPEC §06.6–§06.8.
