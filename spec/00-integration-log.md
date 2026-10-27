# SPEC integration log (Phase 2.3, 2026-10-21)

Every change the integrator made to `spec/01…06`, the ADRs and the generated `SPEC.md`. Tie-breaker
for every conflict: owner decisions D1–D64, then the owning section (records, keys, state machines
and the worker proto: §05; error codes, exit codes and CLI: §02; other protos: §06; core API and
the `bob.lock` container: §01). Scratch and verification files: `/root/scratch-specint/`.

## 1. Duplicate requirement ids

| Conflict | Resolution | Files |
|---|---|---|
| R-NIX-014 / R-NIX-015 in §01 (Fetcher, Builder binary) and §02 (Error code stability, Credential pre-check) | §01 keeps both; §02's become **R-NIX-028** (error code stability) and **R-NIX-029** (credential pre-check); the reference in §02.6 step 1 updated. 028/029 were unused everywhere. | 02 |
| R-NIX-010 in §01 and §02 | One entry in §02 (§02.3); §01's evaluation part (only `resolve.nix` evaluates; extra leak patterns `while evaluating`, `/nix/store/…-source/….nix:`) merged into it; §01 keeps a one-paragraph pointer. | 01, 02 |
| R-INST-001…004 in §02 and §04 (R-INST-003 also in §06) | One entry each in §02 §02.8. Merged from §04: `closure` artifacts for the host, `list --json`, "nothing to install" error (now BOB-E-0009), `BOB_TOKEN`, netrc-only token passing, registry column / per-registry upgrade; merged from §06: server-side anonymous/private resolve checks. §04 §4.9 and §06 §6.8 keep pointers. | 02, 04, 06 |
| R-INST-005 (§04) contradicted §02's install files | §04 R-INST-005 rewritten as "profile semantics" on §02's layout: `$XDG_DATA_HOME/bob/install/`, last **5** generations rooted (was 10 in §04), shim `bob exec --installed` (was `--profile`), registries in `config.json` (was `registries.json`). §02's generation schema gained `platform`. | 02, 04 |
| R-FLEET-006 in §05 and §06 | One entry in §05 (§05.9.2), with §06's live-Forgejo checks merged; §06 §6.9.6 keeps the flow and a pointer. Revocation: event-driven at job end (token 401 ≤ 30 s), `forge-sweeper` every 60 s over `ftok/*`, plus §06's 5-min name-timestamp sweep for tokens without a record. | 05, 06 |

After the changes: 250 `#### R-…` headings, all unique across the six files (checked with
`grep -h '^#### R-' spec/0*.md | awk '{print $2}' | sort | uniq -d` → empty).

## 2. Error codes (§02 taxonomy)

- New codes in §02.3: usage 0006 (platform not supported by package), 0007 (conflicting
  variants), 0008 (unsupported host arch), 0009 (release has nothing to install); definition
  1015 (resolve), 1016 (generated-output conflict), 1017 (nixpkgs attr not evaluable), 1018
  (non-deterministic evaluation), 1019 (unreadable bob.lock), 1020 (invalid option), 1021
  (toolchain mismatch), 1022 (not a static binary), 1023 (cannot be pinned), 1024 (`bob.go`
  rules R1–R7), 1025 (source pattern errors); build 2013 (script denied), 2014 (phantom dep),
  2015 (prune removes needed file); fetch 3004 (unsafe archive entry); host 7007–7010
  (bob-server: unsupported backend, permissive policy, worker not root, no isolation); internal
  9006 (quirk without test), 9007 (embedded builder mismatch).
- New table "Cross-section code assignment" in §02.3 maps every named error of §01/§03/§04/§05:
  `E-DRIFT`→1011, `E-LOCK-STALE`→1012 (missing entry 1009), `E-RESOLVE`→1015,
  `E-FETCH-HASH`→3001, `E-GEN-CONFLICT`→1016, `E-SCRIPT-DENIED`→2013, `E-PHANTOM-DEP`→2014,
  `E-QUIRK-TEST`→9006, etc. It also maps §06's `BOB-API-NNNN` details to CLI codes.
- 7005's template now equals §01 R-NIX-022's text.
- Exit statuses corrected to §02.2 classes: §03 drift/lock-stale/gen-conflict/runtime-mismatch
  "exit 1" → exit 3; fetch hash mismatch → exit 4; §04 option errors (`MaxLayers`, non-static
  tarball) exit 2 → 3; §04 error table gained a code column.
- §01/§03 texts annotated with their codes; §06's §02-interface row now names 4001/4008/4009/4010.
- 1013 hint: bob adds ignore entries to `.git/info/exclude`, never to the user's `.gitignore`
  (§03.0.2 rule); R-CLI-009 acceptance adjusted.

## 3. Closure budget

§01 R-NIX-030 said ≤ 74 MiB, §04 R-OUT-025 ≤ 85 MiB for the native `api` closure after fixup.
Chosen: **≤ 74 MiB, ≤ 12 paths** in both. Basis: S8 §3.1 measured 75.9 MiB without fixup; the
package fixup removes 2.5 MiB (strip, 7.7 → 5.2 MiB) and `itoa-src` (0.05 MiB) → 73.4 MiB.
§04's cross row aligned to §01's unit (≤ 112 MiB; 113.8 MB = 108.5 MiB). §04's interface text
now describes §01's fixup (reference prevention + BOB-E-9003, no `remove-references-to`).
Files: 01, 04.

## 4. `discard-references` / `unsafeDiscardReferences`

Verified at integration (`/root/scratch-specint/dr/`, §04's `t.nix`/`b.sh`, scratch stores
`/root/scratch-specint/store-*`, `experimental-features = nix-command` only):

| Nix | `unsafeDiscardReferences.out = true` | control | `discard-references` in experimental-features |
|---|---|---|---|
| 2.34.8 (host) | references `[]` | references = the input | `warning: unknown experimental feature 'discard-references'` |
| 2.34.9 (`spikes/rootless/bin/nix-2.34.9`) | references `[]` | references = the input | same warning |

So the feature needs no flag. §01 (R-NIX-024) already said so and now records the check on both
versions; §04 (interfaces, R-OUT-011, verification list) and ADR 0010 decision 6 corrected.

## 5. Fixed-output wording

§03.0.4 and R-FW-063 rewritten: sources are content-addressed store paths imported by bob
(`engine/fetch.Fetcher`, `nix store add` / `nix-store --import`) at the path a FOD would have
(path identity, R-NIX-008); no derivation with `outputHash` is generated. Derivation-layout table
rows (cc external, Go module, crate, npm tarball, opam source and repository snapshot) say
"bob-imported … (FOD-identical)". §03's "§01 provides" list and §04's step table updated. No
`fetch-crate` job kind existed in §03 any more (checked). Files: 03, 04.

## 6. Worker connectivity (E-06-1 closed)

- Adopted §05's outbound `bob.worker.v1.WorkerControlService/Attach` (Wake D54, live-log pull
  D55, CancelJob, Drain). Workers expose no port and serve no RPC.
- §06: removed `bob/workeragent/v1/agent.proto` (`WorkerAgentService`, `Wake`, `ReadLiveLog`),
  the `bot1` orchestrator→worker token, the `ORCHESTRATOR` rule kind (`KIND_ORCHESTRATOR` →
  `reserved 7`), and its own `bob/worker/v1/worker.proto` (`WorkerService/GetJobCredentials`).
  §6.5.17 now points to §05.9.1; RBAC tables, §6.0, §6.5.8 intro, §6.9.6 flow and E-06-1 updated.
- §05.9.1 proto: imports `bob/authz/v1/authz.proto`; both RPCs carry
  `rule = {kind: KIND_WORKER}`; `GetCheckoutCredentialResponse` adopts §06's design
  (`sealed_checkout` HPKE-sealed CBOR, `source_key`, `download_token`, `expires_unix`).
- Verification: §06's 15 proto files + §05's `worker.proto` extracted by
  `/root/scratch-specint/extract.py` → `buf build` OK, `buf lint` (STANDARD) OK, buf 1.73.0
  (`/nix/store/…-buf-1.73.0`); 98 RPCs, 98 `rule` options (was 99: −3 removed, +2 §05).
- ADR 0013 (amendment), ADR 0018 (RPC name) updated.

## 7. Names, records, state machines, interfaces

- **Build states** (§06 `BuildState`) = §05.6.2: PENDING, QUEUED, RUNNING, SUCCEEDED, FAILED,
  CANCELLED, SUPERSEDED, SKIPPED; new `BuildPhase` (EVALUATING, BUILDING, PROMOTING) = §05 `ph`,
  `Build.phase = 19`. CANCELLING/EVALUATING/PROMOTING states removed. §05 gained the `skipped`
  transition (no pipeline matched).
- **Job** (`JobState`) = §05.6.1: QUEUED, CLAIMED, RUNNING, SUCCEEDED, FAILED, CANCELLED
  (UPLOADING/PROMOTING removed; promotion progress is `JobRecord.pm` / `Job.promotion`);
  `JOB_KIND_EVAL` added. §05 `JobRecord` gained `Promotion` (`pm`) and `NonReproducible` (`nr`).
- **Worker**: STALE comment uses `fleet.gone_after` (5 min). **Release** (`ReleaseState`) =
  §05.6.4: PENDING, PUBLISHING, PUBLISHED, FAILED, DELETED (DELETING removed; deleted releases
  stay as tombstones).
- **Retry/rerun**: §05 "retry = new build with retry-of"; §06 RetryBuild now creates a new build
  (`TRIGGER_KIND_RETRY`, `Trigger.retry_of`).
- **Concurrency**: §06's per-pipeline `RefHeadRecord` replaced by §05's per-ref rule on `bbr`;
  §05 gained tag default `queue` and "release builds are never superseded".
- **Records**: new §05.4.5 defines the control-bucket records §06 used but §05 lacked
  (`RepoLinkRecord`, `SessionRecord`, `OAuthCodeRecord`, `DeviceCodeRecord`, OIDC state,
  `OidcProviderRecord`, `AdminIndex`, `ForgeDeliveryRecord` + ring, `SigningKeyRecord`,
  `OciHmacKey`, `NotifyWebhookRecord`) and maps §06 aliases (`OrchestratorRecord`=`OrchRecord`,
  `ForgeIntegrationRecord`=`ForgeRecord`, `SettingsRecord`=`SettingRecord`,
  `CheckoutRecord`=entry of `ftok/<sh2>`, `ReleaseIndex`=`relv`+`rpage`+`ridx`, role bindings =
  `UserRecord.ro`). `UserRecord.ro` now holds bindings {role, project, source}; `BuildRecord`
  gained `src`, `tg`, `va`, `pi`, `rp`. Release uniqueness key includes the release name
  (`relv/<projectID>/<name>/v/<version>`); release id = `<projectID>.<num10>`.
- **Artifacts prefixes**: staging is `staging/<buildID>/<jobID>/<attempt>/` everywhere (§04, §05
  JobSpec comment, §06 promotion/releases, ADR 0014); `src/` snapshots and the `oci/vis/` documents
  added to §05.2.4; release assets `rel/<projectID>/<num10>/`.
- **§02 ↔ §06 API names**: `/api/whoami` → `AuthService/WhoAmI`; `/api/cache-info` →
  `AuthService/GetServerInfo`; `BuildService.{Submit,Watch,Cancel}` →
  `{CreateBuild,WatchBuild,CancelBuild}`; `LogService.{Read,Follow}` → `{GetLog,FollowLog}`;
  `ReleaseService.Resolve` → `ResolveRelease`; `SourceService.Upload` → `PUT /upload/source/…`;
  `AuthService.Refresh/Revoke` → `/oauth/token` (refresh grant) / `/oauth/revoke`; loopback path
  `/cb` → `/callback`; device page `/login/device` → `/device`. ADR 0017 wording updated. §04's
  `ListReleaseVersions` → `ListReleases`/`ResolveRelease`.
- **§01 ↔ §03**: `bob.Ecosystem` → `engine.Ecosystem` (`pkg/engine`); `pkg/lock` →
  `pkg/engine/lock`; `nixpkgs.Lib(..., ...LibOpt)` → `...nixpkgs.Option`; `nixpkgs.Package` used
  as `*nixpkgs.Package` (§01 struct). §01's lock container now lists the `cc` section (for
  `cc.External`); `sources` is for `bob.Fetch` only.
- **Added small definitions**: `bob.Fetch`/`bob.FetchOpts` (§01 §1.4.1; referenced by the lock
  container but undefined); `bob.DevShell`/`bob.NewShell`/`bob.Shell` (§02.7.0; §01 and §02 each
  expected the other to define it). Both marked [proposal].
- **Repo root**: §01 said "directory of the root `go.mod`", §02 "nearest ancestor with
  `bob.lock`" → §02's rule, written into §01 §1.2 (the generated root `go.mod` sits there);
  bootstrapping a new repo listed as open O-02-11.
- **Missing interface** listed as [open]: per-ecosystem dev preparation (`Prepare`) → O-02-10.

## 8. Visibility markers (D58, D60)

One design: one JSON document per served object, `nix/vis/<storehash>.json` and
`oci/vis/sha256/<hex>.json`, `{"v":1,"projects":[…],"public":[release ids]}`, created with
`If-None-Match: *`, updated with `If-Match`; a check is one GET (cached 30 s). §05's CBOR
recommendation and its [open] item removed; §05.2.4 and §05 interfaces point to §06 §6.6.2.

## 9. Other contradictions fixed

| Topic | Before | After | Files |
|---|---|---|---|
| Substituter order | §06: bob server before cache.nixos.org | cache.nixos.org first (as §01 nix.conf and §02 R-DEV-004) | 06 |
| netrc / nix.conf paths | §02 `<state>/etc/netrc`, §06 `<bob state>/etc/netrc` | §01's `<home>/etc/nix/{nix.conf,netrc}`; `<state>` defined as §01 `<home>` | 02, 06 |
| Live-log latency | §06: 500 ms pull poll, p99 ≤ 1.5 s, lake fallback ≤ 8 s | §05: 250 ms chunks over Attach, p99 ≤ 1 s, fallback ≤ flush_interval + 10 s; O-06-3 closed | 06 |
| Retention defaults | §06 settings: cache 30 d, builds 90 d | §05 config: cache 90 d, builds 365 d | 06 |
| Server listener config | only in §06 | rows added to §05.11.1 config table | 05 |
| CLI commands/flags | §03 used `bob gen [--check\|--force]`, `bob status`, `build --lock`, §04 `--require-all-tests`, §02 used `lock --offline` – none in §02's table | added to §02 command table; `check --fix` = `gen --force` semantics | 02, 03 |
| Dev budgets | §02 R-DEV-006: eval no-op "≤ 50 ms", Go one-file ≤ 5 s | §01 ≤ 0.1/0.15 s; §03 ≤ 4 s | 02 |
| C/C++ no-op [2c/4G] | §03 ≤ 0.6 s vs §01 ≤ 0.8 s (same measurement) | ≤ 0.8 s | 03 |
| Rust lock layout | §03 "one crate per line" vs §01 canonical 2-space JSON | §01's canonical form; open item closed | 03 |
| `logstore.Follow` reference | §05.10.5 | §05.10.4 | 05 |
| §01 escalation notes | open coordinator notes | marked resolved | 01 |

## 10. SPEC.md

`/root/bob/SPEC.md` = hand-written preamble (precedence, reading guide, regeneration command),
generated table of contents, Appendix A (38 open items), Appendix B (97 budget rows, generated
once by `/root/scratch-specint/budgets.py` and then maintained by hand), then §01–§06 concatenated
(the "Source of SPEC.md" note line of each file is stripped). Each `spec/0N-*.md` starts with that
note. Regenerate with the Python block in the SPEC.md preamble (idempotent; checked twice).

## 11. Gate H rulings (2026-10-22)

Text changes from `plan/gate-h-rulings.md` (decisions A42–A64): G1 `project_field` dotted path (§6.4.2,
§6.5, `UpdateProject` `project.id`, `UpdateRelease` `release.id`); G2 R-FLEET-005 split rule
`max(10 s, 0.3 × T1)` (ADR 0016 amendment, S14 text); G3 R-FLEET-006 403 and denial text (§6.9.6);
G4 R-NIX-012 conditional acceptance; G6 `PipelineOpts`, `Trigger`, `Concurrency` (§01.4); G7 exit 78
for start-up refusals (§02); G8 R-FW-016 `:13`; G9 `Parse` vs `ParseLabel` and length bounds;
G10 BOB-E-1019 hint; G11 R-FW-030 host/target `system`; G12 R-FW-047 node cap; G13 R-FW-045 memory;
G14 derivation name precedence; G15 Rust features API (§1.9); G16 R-NIX-020/021 observation points;
G17 R-STORE-104 `sh2`; G18 R-NFR-005; G19 R-STORE-004; G20 R-LOG-102 and R-PIPE-003 follow-up notes;
G24 FIX-C clarifications; G21 `docs/operations/worker-compromise.md`; G23 HARNESS §10 row 27.
SPEC.md regenerated (idempotent).

## 2026-10-24 — ADR 0020 typed surface and per-package modules (Q1, owner D71/D72)

- §01: R-FW-010 amended; new §1.4.5 with R-FW-070 (package locality), R-FW-071 (one unit per
  package, `bob.Slot`), R-FW-072 (typed public surface); §1.4.1 `bob.Include`/`IncludeDir`;
  §1.4.4 `bob.Channel`, `RepoOpts.NixpkgsRev`, `PipelineOpts.Releases []Pattern`; R-FW-002 R7
  (nixext) and R8 (framework values read-only); §1.7 rewritten as the generated typed surface
  (R-NIX-013 amended, R-NIX-031, R-NIX-032); R-NIX-005 default pin = `lock.DefaultRev`.
- §02: codes 1001 `pin`, 1024 `R8`, 1028, 1029 (1027 reserved); 2013/2014 hints with handles.
- §03: §03.0.7 common rule; cc R-CC-006, `Hdrs bob.Include`, all path flags refused; Go methods
  without directory arguments, `TestOpts.Recursive`; Rust `CrateDep`/`Indirect`/`Uses`; npm
  `Pkg`/`Indirect`/typed `AllowScripts`/`Extend`/`Peer`, tsconfig enums; OCaml `Indirect`/`Conf`;
  codegen tools as `nixpkgs.Attr`, proto `Root` removed; `ExternalOpts.Tools []bob.Tool`.
- §04: R-FW-032 typed twice (handle + role method); `bob.Toolchain` without attribute strings
  (`nixext.Toolchain`); release example through a slot.
- Appendix B: B-098…B-103.
