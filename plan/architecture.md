# bob — Architecture (Stage 2, Phase 2.4)

Status: draft for Gate S, 2026-10-21. Derived from SPEC.md (§01–§06) and ADRs 0001–0019.
SPEC wins on any conflict; this file explains how the parts fit together and does not add
requirements. Package names marked *(proposed)* are not in SPEC and get fixed in task M0.1.

## 1. Components

| Component | Where | Binary / artefact | Role |
|---|---|---|---|
| Framework (public) | `pkg/` | linked into `bob` and into users' evaluators | typed Go API, engine, Nix backend, ecosystems, outputs |
| User CLI | `cmd/bob`, `pkg/cli` | `bob` | build/test/run/dev/env/exec/lock/check/graph/pipeline/login/install (§02) |
| Builder | `cmd/bob-builder`, `pkg/builder`, `pkg/<eco>/bop` | `bob-builder` (static, lives in the Nix store, embedded in `bob`) | the only program that runs inside bob derivations (§1.12) |
| Server | `cmd/bob-server`, `internal/` | `bob-server` (`orchestrator`, `worker`, `admin` modes) | HA build/cache server (§05, §06) |
| API contracts | `proto/bob/<area>/v1/*.proto` | generated Go in `internal/gen/bob/…`, TS in `web/src/lib/gen` | connectrpc + buf (§6.5) |
| Web UI | `web/` (SvelteKit, static adapter) | `web/build`, embedded via `embed.FS` in `internal/ui` | UI over the public API only (R-API-004) |
| Harness | `tools/ci`, `tests/` | `go run ./tools/ci`, later `bob pipeline run ci` | HARNESS.md |
| Bootstrap | `scripts/bootstrap.sh`, `scripts/fetch-nix.sh` | first `bob` | R-ARCH-005 |

`bob` contains no server code; `bob-server` reuses `pkg/engine` (evaluation, derivations,
store) for its worker mode, never the other way round (R-ARCH-002, R-ARCH-004, D36).

## 2. `pkg/` layers (R-ARCH-003)

```
               glue:  cmd/bob, pkg/cli  ───────────────────────────────┐ (may import all of pkg/)
                                                                        │
  ┌──────────── evaluator side (linked into the generated evaluator) ──┼───────────────┐
  │ L3 outputs     pkg/oci  pkg/appimage  pkg/tarball  pkg/release      │               │
  │ L2 ecosystems  pkg/cc pkg/golang pkg/rust pkg/npm pkg/ocaml         │               │
  │                pkg/proto pkg/connect pkg/graphql pkg/openapi        │               │
  │                pkg/services/...                                     │               │
  │ L1 sources     pkg/nixpkgs                                          │               │
  │ L0 core        pkg/bob  pkg/bob/ext  pkg/bob/diag                   │               │
  └─────────────────────────────────────────────────────────────────────┼───────────────┘
  ┌──────────── engine side (never in the evaluator) ───────────────────┘
  │ E3 orchestration  pkg/engine/eval  pkg/engine/build  pkg/engine/translate
  │ E2 generators     pkg/engine (registry, engine.Ecosystem)  pkg/<eco>/gen
  │ E1 services       pkg/engine/store  fetch  lock  nixidx  srcs
  │ E0 primitives     pkg/engine/drv  pkg/engine/nar  pkg/engine/ir
  └──── builder: pkg/builder, pkg/builder/bspec, pkg/<eco>/bop  (imports E0 + bspec only)
```

Rules (checked by `TestLayering` in `go run ./tools/ci`):
- downward imports only; L2 packages may import each other only acyclically;
- L0–L3 never import `pkg/engine/...` or `pkg/builder/...` (keeps the evaluator small; the
  IR → drv step is trusted code, S13);
- third-party modules only where the R-ARCH-003 table lists them;
- `pkg/`, `cmd/bob`, `cmd/bob-builder` never reach `internal/`, `proto/` server code or
  `cmd/bob-server` (R-ARCH-002).

**Package names** (aligned in Stage 2): L2 codegen packages are `pkg/{proto,connect,graphql,openapi}`;
L3 outputs are `pkg/output/{oci,appimage,tarball}` and `pkg/release`. The R-ARCH-003 table in
SPEC §1.1 is the source of truth for `TestLayering`.

## 3. `internal/` (server)

| Package | Owns | SPEC |
|---|---|---|
| `internal/store` | the only dynamitedb access: `Get/GetCached/Create/CAS/PutOwned/Delete`, `SeqLog`, `PagedIndex`, `ShardSet`, `Lease`; `store.Objects`, plain S3 on the artifacts bucket (staging, nix/, oci/, rel/, src/, §05.2.4) | §05.3, R-STORE-106 |
| `internal/store/sweep` | the only LIST (cold sweeps of analytics/artifacts) | R-STORE-101 |
| `internal/logstore` | lake tables `log_line`, `package_run`; writer, compactor, `Reader`, `Follow` | §05.10 |
| `internal/cache` | Nix binary cache endpoints, visibility, download proxy (no outbound HTTP client) | §6.6 |
| `internal/forge` | `forge.Adapter` + GitHub / GitLab / Forgejo | §6.9 |
| `internal/gen/bob/<area>/v1` | buf-generated Go | §6.5 |
| `internal/ui` | embedded `web/build` | §6.10 |
| `internal/http` *(proposed)* | listener, endpoint map, middleware (authn, authz rule per RPC) | §6.2 |
| `internal/orchestrator` *(proposed)* | controllers (membership, registrar, build, job-reconciler, promotion, release, lake-maintenance, retention, forge-sweeper, index-repair, key) | §05.11 |
| `internal/worker` *(proposed)* | root worker loop, registration, claim, job container, Attach client, log buffer | §05.7–§05.10 |
| `internal/auth`, `internal/rbac` *(proposed)* | principals, tokens, OAuth/OIDC, permission check per RPC × project | §6.3, §6.4 |
| `internal/api` *(proposed)* | connect service implementations | §6.5 |
| `internal/registry`, `internal/promote`, `internal/release` *(proposed)* | OCI pull registry, staging → served promotion and signing, release state | §6.6–§6.8 |

Server dependency rules: every S3 access goes through `internal/store` (records and
`store.Objects`) or `internal/logstore`; no LIST outside `sweep`; workers never hold control-bucket
credentials (policy-enforced, R-SEC-002); `internal/` may import `pkg/engine/...`.

## 4. Data flow: local build (`bob build //...`)

```
bob build
  │ ns.Enter: userns+mountns, ~/.local/share/bob/nix bound at /nix         (R-NIX-021, ADR 0002)
  ▼
scan bob.go files ─► generate evaluator main (blank-imports every bob.go)  (R-FW-002/043, ADR 0003)
  │ eval key hit? ─yes─► cached IR
  ▼ no
evaluator in evalbox (cgroup, namespaces, Landlock, seccomp) ──fd 3──► Graph IR v1 JSON
  │                                                                  (R-FW-045/047, ADR 0015)
  ▼
IR validation ─► bob.lock check (drift = BOB-E-1011/1012) ─► nixidx pre-checks (R-NIX-011)
  ▼
srcs: import repo sources (nix-store --import / nix store add)                  (R-NIX-009)
  ▼
engine.Ecosystem.Plan per ecosystem (pkg/<eco>/gen) ─► drv.Derivation (structured attrs)
  │                                     bob-builder job (`bobSpec`) as builder  (R-NIX-006/016)
  ▼
fetch.Ensure: bob fetches all PlanResult.Fetches (≤ 8 parallel), adds to store (R-NIX-014/008)
  ▼
import drvs ─► nix build --no-link --print-out-paths --log-format internal-json -L --keep-going
  │                 (substituters: cache.nixos.org [+ bob server cache])        (R-NIX-007)
  ▼
translate: internal-json + Context (drv → label, definedAt) ─► diag.Error BOB-E-NNNN
  │ success = exit 0 AND every requested output valid                (R-NIX-010/018, ADR 0017)
  ▼
outputs in /nix/store (+ materialised codegen / generated native files) ─► human or --json events
```

## 5. Data flow: remote build (`bob build --remote`, pipelines)

```
bob (client)                 orchestrator(s)                    worker (root)          job container
──────────────               ───────────────                    ─────────────          ─────────────
PUT /upload/source ───────►  src/<proj>/<sha>.tar.zst
BuildService.CreateBuild ─►  BuildRecord (C) + bpage/bidx
                             build controller (holds ctl/build):
                               enqueue: JobRecord job/<id> (C),
                               slot qs/<class>/s/<seq> (If-None-Match)
                               wake-up over Attach ───────────►  read qh/qs (GET), claim
                                                                 job/<id> (CAS, epoch, lease)
                                                                 heartbeat whb/<wid> ─────►  evalbox eval → IR
                                                                                              → drvs → nix build
                                                                                              (sandboxed, non-root uid)
                                                                 upload staging/<b>/<j>/<a>/  ◄─ outputs
                                                                 COMPLETE.json last; job → succeeded (CAS)
                             promotion controller: verify NARs/blobs,
                               copy to nix/, oci/, visibility docs,
                               signed narinfo last (create-only)
                             build → terminal; forge status (SetStatus)
WatchBuild / FollowLog ◄──── logstore (lake) + live pull: LiveSubscribe ─► LiveChunk/250 ms
nix substitutes /nix/* ◄──── download proxy (netrc token, visibility check, no presigned URLs)
```

Scheduling: one closure job per build by default; split into ready-set batch jobs (≤ 64 drvs)
only when the planner predicts a gain (D53, ADR 0016). Jobs exchange outputs only through
promoted paths.

## 6. Process model

```
            users / bob CLI / forges / nix / podman
                         │ HTTPS :8443 (connect API, /nix, /v2, /oauth, /upload, /hooks, UI)
      ┌──────────────────┼──────────────────┐
  orchestrator A    orchestrator B    orchestrator C     (identical, stateless; admin :9090)
      │  every controller runs everywhere, works only while holding ctl/<name> lease (CAS)
      │  no orchestrator ↔ orchestrator traffic (D7, D55)
      ▲ Attach (outbound from worker, pinned SPKI, worker token bwt1 + Bob-PoP)
      │
  bob-server worker (uid 0) ── slots ──► job container per job (cgroup leaf, own mount/pid/ipc/uts
      │                                   /cgroup ns, /nix from /var/lib/bob/worker/nix,
      │                                   no worker credentials) ─► evalbox (uid map ≠ 0) + Nix
      │                                   sandbox builds as auto-allocated non-root uids
      └── S3 creds from the deployment (env/IMDSv2); tokens derived from S3 access (D57)
                         │
   S3 (RustFS reference; conditional writes required, D63)
   bob-control (dynamitedb, orch only) · bob-fleet (dynamitedb, workers limited)
   bob-analytics (lake, both) · bob-artifacts (plain S3, workers write staging/ only)
```

The worker opens one `WorkerControlService/Attach` stream to each orchestrator listed in
`fleet/cfg`. Orchestrators use it for wake-ups, live-log subscriptions and drain; correctness
never depends on it (truth = buckets and lake, ADR 0019).

## 7. Storage layout summary

| Place | Contents |
|---|---|
| `~/.local/share/bob/` (`$XDG_DATA_HOME/bob`) | rootless Nix store (`nix/`), bob-owned `nix.conf`, netrc, evaluator cache, install profiles |
| `<repo>/bob.lock` | the only lock (nixpkgs pin + per-ecosystem sections), committed |
| `<repo>/.bob/` | `env.json`, `bin/` shims, `gen/manifest.json`, editor state; gitignored |
| generated in-tree files | `go.mod`, `Cargo.toml`, `package.json`, `tsconfig.json`, codegen dirs: read-only, bob-owned |
| `bob-control` | `proj/`, `build/`, `bpage/`, `bidx/`, `rel/`, `user/`, `role/`, `tok/`, `sa/`, `forge/`, `ftok/`, `set/`, `okey/`, `promo/` |
| `bob-fleet` | `fleet/cfg`, `orch/`, `ctl/`, `wreg/`, `wrs/`, `worker/`, `whb/`, `widx/`, `qt/ qh/ qs/`, `job/`, `jact/` |
| `bob-analytics` | lake tables `log_line`, `package_run` (partitioned per day × shard) |
| `bob-artifacts` | `staging/`, `nix/{nar,ls,narinfo,vis}/`, `oci/`, `rel/`, `src/` |
| worker host | `/var/lib/bob/worker/nix` (persistent root-owned store) |

All hot paths are GET or conditional PUT on known keys (D60, R-STORE-100).

## 8. Where each ADR lands

| ADR | Decision | Lands in |
|---|---|---|
| 0001 Generation strategy B (locked) | Go computes derivations directly | `pkg/engine/drv`, `pkg/<eco>/gen`, `pkg/engine/build` |
| 0002 Rootless embedded static Nix (locked) | userns + mount ns, bob-owned config, `bob dev`/shims | `pkg/engine/store`, `pkg/cli` (dev, env, exec), `scripts/fetch-nix.sh` |
| 0003 `bob.go` next to code | `//go:build bob`, generated evaluator | `pkg/engine/eval`, `pkg/bob` |
| 0004 Go builds | hybrid granularity, compile without cmd/go | `pkg/golang`, `pkg/golang/gen`, `pkg/golang/bop` |
| 0005 Rust | own resolver, per-crate drvs, builder runs build scripts | `pkg/rust`, `pkg/rust/gen`, `pkg/rust/bop` |
| 0006 npm | own resolver, per-instance drvs, scripts denied | `pkg/npm`, `pkg/npm/gen`, `pkg/npm/bop` |
| 0007 OCaml/opam | own opam resolver, per-opam-package drvs | `pkg/ocaml`, `pkg/ocaml/gen` |
| 0008 Codegen + materialisation | one drv per owned dir, manifest | `pkg/proto`, `pkg/connect`, `pkg/graphql`, `pkg/openapi`, `pkg/cli` (gen, status) |
| 0009 Platforms and variants | host builds, target compiles; projections | `pkg/bob` (model), `pkg/nixpkgs`, every `pkg/<eco>/gen` |
| 0010 OCI + AppImage | Go-written OCI, type-2 AppImage + launcher | `pkg/oci`, `pkg/appimage`, `pkg/tarball`, `pkg/release` |
| 0011 dynamitedb reconciliation store | Get+ETag-Put CAS, seq logs, leases | `internal/store` |
| 0012 Logs/analytics in lake | batched Parquet, workarounds L1/L3/L4 | `internal/logstore` |
| 0013 Worker registration, bucket isolation | HPKE-sealed tokens, policies | `internal/worker`, `internal/orchestrator` (registrar), shipped policies |
| 0014 Cache, registry, promotion | staging → verified promotion, Go signer | `internal/cache`, `internal/promote`, `internal/registry` |
| 0015 Worker isolation | evalbox; root worker + job container (D56) | `pkg/engine/eval` (evalbox), `internal/worker` |
| 0016 Scheduling unit | closure job, ready-set batches | `internal/orchestrator` (build controller) |
| 0017 Error translation | internal-json → `diag` | `pkg/engine/translate`, `pkg/bob/diag` |
| 0018 Forge adapters | one adapter interface, official SDKs | `internal/forge` |
| 0019 Live logs without coordinator | pull on demand over Attach | `internal/worker`, `internal/logstore`, `LogService` |
