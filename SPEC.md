# bob — SPEC (Stage 2, Phase 2.3)

Status: integrated draft, 2026-10-21, awaiting Gate S. Owner: Linus Moser.

## Preamble

**What this is.** The single specification that Stage 3 builds bob from: the typed Go framework
that makes Nix an invisible build engine (`pkg/`, the `bob` binary) and the separate HA
build/cache server (`internal/`, `cmd/`, the `bob-server` binary). Every requirement entry
(`#### R-<AREA>-<NNN>`) has a Statement, an Acceptance criterion that the harness checks (unless
marked `(manual)`) and a Source. Budgets marked **[proposal]** derive from the quiet-machine
measurements named next to them; `[8c/15G]` and `[2c/4G]` are the two machine profiles.

**How to read it.** Sections §01–§06 follow below in order; each starts with an *Interfaces* list
(what it provides and what it uses from other sections) and ends with its open points and
escalations. Appendix A collects every `[open]` item, Appendix B every budget. Cross-references
use the section numbers (`§05.4.3`, `§6.6.2`). Error codes are always `BOB-E-NNNN` from §02.3;
server API error details are `BOB-API-NNNN` (§06 §6.1).

| § | Scope |
|---|---|
| §01 | `pkg/` core: `bob.go` convention, core API, graph IR, `bob.lock` container, platforms/variants model, derivation layer, fetching, `bob-builder`, fixup, embedded rootless Nix, nixpkgs index |
| §02 | CLI, exit codes, error taxonomy, JSON events, developer environment (hook, shims, LSP proxy, `bob dev`), login, `bob install` |
| §03 | Ecosystems: C/C++, Go, Rust, npm/TS, OCaml, code generators and materialisation |
| §04 | Cross-compilation, toolchains, variants, OCI images, AppImages, tarballs, releases, install profiles |
| §05 | Server data: buckets, dynamitedb keys (GET-only), state machines, fleet, worker protocol, isolation, logs, HA |
| §06 | Server API: `proto/`, RBAC, identity, Nix cache and OCI registry, download proxy, promotion, releases, forges, UI |

**Precedence.** When two sources disagree: owner decisions (REQUIREMENTS.md §24, D1–D64) >
this SPEC > ADRs (`docs/adr/`, status `Accepted (locked)`) > spike write-ups
(`docs/spikes/`). Inside the SPEC, the owning section of a topic wins (records, keys and state
machines: §05; error codes and CLI: §02; proto texts: §06, except `bob/worker/v1`: §05). The
integration changes are listed in `spec/00-integration-log.md`.

**Regeneration.** The section texts are generated from `spec/01-core.md` … `spec/06-server-api.md`
by concatenation; edit those files, never the generated part of this file. The preamble and the
appendices above the `SECTIONS` marker are maintained by hand. Regenerate with:

```sh
python3 - <<'PY'
import re
p = '/root/bob/SPEC.md'; s = open(p).read()
T, A, S = '<!-- ' + 'TOC -->', '<!-- ' + 'APPENDICES -->', '<!-- ' + 'SECTIONS -->'
head, rest = s.split(T, 1); app = rest.split(A, 1)[1].split(S, 1)[0]
files = ['01-core', '02-cli-dev', '03-ecosystems', '04-outputs-platforms', '05-server-data', '06-server-api']
secs = '\n'.join(''.join(l for l in open(f'/root/bob/spec/{f}.md').read().splitlines(True)
                         if not l.startswith('> Source of SPEC.md')) for f in files)
seen, toc, fence = {}, [], False
for l in (app + '\n' + secs).splitlines():
    if l.startswith('```'): fence = not fence; continue
    m = None if fence else re.match(r'^(#{1,2}) (.+)$', l)
    if not m: continue
    a = re.sub(r'[^\w\- ]', '', m.group(2).lower()).replace(' ', '-')
    n = seen.get(a, 0); seen[a] = n + 1; a = a if n == 0 else f'{a}-{n}'
    toc.append(('' if m.group(1) == '#' else '  ') + f'- [{m.group(2)}](#{a})')
open(p, 'w').write(head + T + '\n\n## Table of contents\n\n' + '\n'.join(toc) + '\n\n' + A + app + S + '\n\n' + secs)
PY
```

<!-- TOC -->

## Table of contents

- [Appendix A — Open items](#appendix-a--open-items)
- [Appendix B — Budgets](#appendix-b--budgets)
- [§01 — Framework core (`pkg/`)](#01--framework-core-pkg)
  - [Interfaces](#interfaces)
  - [1.1 Repository layout and layering](#11-repository-layout-and-layering)
  - [1.2 `bob.go` convention (ADR 0003)](#12-bobgo-convention-adr-0003)
  - [1.3 Labels](#13-labels)
  - [1.4 Core API (`pkg/bob`)](#14-core-api-pkgbob)
  - [1.5 Ecosystem extension API (`pkg/bob/ext`)](#15-ecosystem-extension-api-pkgbobext)
  - [1.6 Graph IR v1](#16-graph-ir-v1)
  - [1.7 nixpkgs as package source (`pkg/nixpkgs`)](#17-nixpkgs-as-package-source-pkgnixpkgs)
  - [1.8 `bob.lock`](#18-boblock)
  - [1.9 Platforms and variants (ADR 0009, model part)](#19-platforms-and-variants-adr-0009-model-part)
  - [1.10 Derivation layer (backend B, ADR 0001)](#110-derivation-layer-backend-b-adr-0001)
  - [1.11 Sources and fetching (bob fetches; no user FODs)](#111-sources-and-fetching-bob-fetches-no-user-fods)
  - [1.12 `bob-builder` contract](#112-bob-builder-contract)
  - [1.13 Fixup and closure budgets](#113-fixup-and-closure-budgets)
  - [1.14 Embedded rootless Nix (ADR 0002)](#114-embedded-rootless-nix-adr-0002)
  - [1.15 nixpkgs lookup and index](#115-nixpkgs-lookup-and-index)
  - [1.16 Build pipeline and generic interfaces](#116-build-pipeline-and-generic-interfaces)
  - [1.17 Budgets (R-NFR-001/002, core part) [proposal]](#117-budgets-r-nfr-001002-core-part-proposal)
  - [1.18 ADR status changes (this section)](#118-adr-status-changes-this-section)
  - [Open points](#open-points)
  - [Escalations](#escalations)
- [§02 — CLI, errors and developer experience](#02--cli-errors-and-developer-experience)
  - [Interfaces](#interfaces-1)
  - [02.1 Command line grammar](#021-command-line-grammar)
  - [02.2 Exit codes](#022-exit-codes)
  - [02.3 Error taxonomy](#023-error-taxonomy)
  - [02.4 `--json` event stream, schema v1](#024---json-event-stream-schema-v1)
  - [02.5 Human output](#025-human-output)
  - [02.6 Build, test, run, log, remote](#026-build-test-run-log-remote)
  - [02.7 Developer environment](#027-developer-environment)
  - [02.8 Login, registries and `bob install`](#028-login-registries-and-bob-install)
  - [02.9 Completion](#029-completion)
  - [02.10 `check`, `lock`, `graph`, `pipeline run`](#0210-check-lock-graph-pipeline-run)
  - [[open] items](#open-items)
  - [Escalations](#escalations-1)
- [§03 — Ecosystems: C/C++, Go, Rust, npm/TS, OCaml, code generators](#03--ecosystems-cc-go-rust-npmts-ocaml-code-generators)
  - [Interfaces](#interfaces-2)
  - [§03.0 Rules common to all ecosystems](#030-rules-common-to-all-ecosystems)
  - [§03.1 C / C++ (`pkg/cc`)](#031-c--c-pkgcc)
  - [§03.2 Go (`pkg/golang`) — ADR 0004](#032-go-pkggolang--adr-0004)
  - [§03.3 Rust (`pkg/rust`) — ADR 0005](#033-rust-pkgrust--adr-0005)
  - [§03.4 TypeScript / npm (`pkg/npm`) — ADR 0006](#034-typescript--npm-pkgnpm--adr-0006)
  - [§03.5 OCaml (`pkg/ocaml`) — ADR 0007](#035-ocaml-pkgocaml--adr-0007)
  - [§03.6 Code generators and materialisation — ADR 0008](#036-code-generators-and-materialisation--adr-0008)
  - [§03.7 bob.lock entry schemas (summary for §01)](#037-boblock-entry-schemas-summary-for-01)
  - [§03.8 Ecosystem budgets **[proposal]**](#038-ecosystem-budgets-proposal)
  - [§03.9 Open points](#039-open-points)
  - [Escalations](#escalations-2)
- [§04 Outputs and platforms](#04-outputs-and-platforms)
  - [Interfaces](#interfaces-3)
  - [4.1 Platforms](#41-platforms)
  - [4.2 Toolchains and dependencies](#42-toolchains-and-dependencies)
  - [4.3 Variants](#43-variants)
  - [4.4 Outputs in general](#44-outputs-in-general)
  - [4.5 OCI images](#45-oci-images)
  - [4.6 AppImages](#46-appimages)
  - [4.7 Tarballs](#47-tarballs)
  - [4.8 Releases as built objects](#48-releases-as-built-objects)
  - [4.9 Installing releases (consumer side)](#49-installing-releases-consumer-side)
  - [4.10 Output budgets **[proposal]**](#410-output-budgets-proposal)
  - [4.11 Error messages (this section)](#411-error-messages-this-section)
  - [Open points](#open-points-1)
  - [Escalations](#escalations-3)
  - [Verifications run while drafting](#verifications-run-while-drafting)
- [§05 Server data: buckets, keys, state machines, fleet, logs](#05-server-data-buckets-keys-state-machines-fleet-logs)
  - [Interfaces](#interfaces-4)
  - [05.1 Rules for all S3 access](#051-rules-for-all-s3-access)
  - [05.2 Buckets and policies](#052-buckets-and-policies)
  - [05.3 `internal/store` primitives](#053-internalstore-primitives)
  - [05.4 Key schemas](#054-key-schemas)
  - [05.5 Hot paths](#055-hot-paths)
  - [05.6 State machines](#056-state-machines)
  - [05.7 Scheduling (D53) and dispatch (D54)](#057-scheduling-d53-and-dispatch-d54)
  - [05.8 Worker isolation (D56)](#058-worker-isolation-d56)
  - [05.9 Worker protocol and authentication (D57)](#059-worker-protocol-and-authentication-d57)
  - [05.10 Logs (D55)](#0510-logs-d55)
  - [05.11 Orchestrator HA without a coordinator](#0511-orchestrator-ha-without-a-coordinator)
  - [05.12 Budgets [proposal]](#0512-budgets-proposal)
  - [05.13 Verifications run for this section](#0513-verifications-run-for-this-section)
  - [05.14 Open items](#0514-open-items)
  - [Escalations](#escalations-4)
- [§06 — Server API, auth, cache/registry, releases, pipelines, UI](#06--server-api-auth-cacheregistry-releases-pipelines-ui)
  - [6.0 Interfaces](#60-interfaces)
  - [6.1 Conventions](#61-conventions)
  - [6.2 HTTP endpoint map (orchestrator)](#62-http-endpoint-map-orchestrator)
  - [6.3 Identity, tokens and login](#63-identity-tokens-and-login)
  - [6.4 RBAC](#64-rbac)
  - [6.5 `proto/` (complete draft)](#65-proto-complete-draft)
  - [6.6 Nix binary cache, visibility, download proxy, promotion](#66-nix-binary-cache-visibility-download-proxy-promotion)
  - [6.7 OCI registry (pull only)](#67-oci-registry-pull-only)
  - [6.8 Releases](#68-releases)
  - [6.9 Pipelines and forges](#69-pipelines-and-forges)
  - [6.10 Web UI](#610-web-ui)
  - [6.11 Budgets [proposal]](#611-budgets-proposal)
  - [6.12 Open points](#612-open-points)
  - [6.13 Escalations](#613-escalations)

<!-- APPENDICES -->

# Appendix A — Open items

Every `[open]` point of §01–§06 after integration. Each carries the drafting recommendation, which
Stage 3 follows unless the owner decides otherwise. None is an architecture conflict.

| Id | Section | Item | Recommendation |
|---|---|---|---|
| O-01-1 | §01 Open points | Egress restriction for developer fetches to private addresses | none on developer machines; workers use §05's policy |
| O-01-2 | §01 Open points, §03.9 | `git` sources for `cc.External` (no git fetcher in v1) | forge archive URLs of a commit (`/archive/<sha>.tar.gz`) as `Archive` sources |
| O-01-3 | §01 Open points | Source import via `nix-store --import` vs `nix store add` per path | `--import` with the path-equality oracle test (R-NIX-009) and `bob doctor` check |
| O-01-4 | §01 Open points | Determinism check cost on workers | single evaluation on workers; `bob check --determinism` only in pipeline check stages |
| O-01-5 | §01 Open points | `abi <abi/4.0>` line of the AppArmor profile unverified | verify on Ubuntu 24.04 in Stage 3 (ADR 0002 risk 1) |
| O-02-1 | §02 [open] items | Remote source transfer default | snapshot upload (§06 §6.9.7) rather than "commit must be pushed" |
| O-02-2 | §02 [open] items, §02.7.4 rule 9 | tsserver framing; VS Code built-in TypeScript outside `bob dev` | v1 documents `bob dev -- code` |
| O-02-3 | §02 [open] items | `bob dev -- code` joining a running VS Code | per-project `--user-data-dir` under `.bob/vscode`; manual check |
| O-02-4 | §02 [open] items | JetBrains under `bob dev` | manual Stage 3 check |
| O-02-5 | §02 [open] items | Extra LSP tools in `DevShell` | add `Tool(...).LSP()` only if a user needs it |
| O-02-6 | §02 [open] items | Trust model of the shell hook | per path (not per content hash) |
| O-02-7 | §02 [open] items | Registry key pinning | trust on first use; `--key` to pre-seed |
| O-02-8 | §02 [open] items | Working-tree symlinks into `/nix/store` dangle outside the namespace | keep `/nix/store` targets in v1; editors browse through the LSP proxy |
| O-02-9 | §02 [open] items | `bob test --rerun` via Nix `--rebuild` | switch to a nonce input if `--rebuild` proves noisy |
| O-02-10 | §02 [open] items (integration) | No single per-ecosystem dev-preparation interface | optional `engine.DevPreparer` (`PrepareDev`) implemented by npm and codegen, specified in Phase 3.1 |
| O-02-11 | §02 [open] items (integration) | Root of a new repository before the first `bob lock` | `bob lock` alone uses the nearest `.git` ancestor and creates `bob.lock` there |
| O-03-1 | §03.9 | Ignore mechanism for generated files | bob-managed block in `.git/info/exclude` |
| O-03-2 | §03.9 | R-CC-004 per-object C/C++ derivations | per-library in v1; measure per-object on a 200-file C++ library |
| O-03-3 | §03.9 | R-GO-005 export-data cutoff default | off until the Stage 3 measurement |
| O-03-4 | §03.9 | Go test main | bob's `_testmain.go` template; fallback `go test -c` |
| O-03-5 | §03.9 | Second toolchain versions (R-RS-004a, R-GO-004a) | named extra nixpkgs pins in `bob.go`; toolchains only from pins (needs §01 support for several pins) |
| O-03-6 | §03.9 | SvelteKit (`svelte-kit sync` in build and dev) | unverified; verify in Stage 3 |
| O-03-7 | §03.9 | OpenAPI TypeScript tool lock | build the openapi-typescript tool lock in Stage 3 |
| O-03-8 | §03.9 | `proto.Breaking` against a git revision | read the revision with `git archive` into a store path |
| O-04-1 | §04 Open points | Version stamping into binaries | `golang.BinaryOpts.Stamp` on the release link step only; not in v1 unless pipelines need it |
| O-04-2 | §04 Open points | AppImage runtime delivery | embed prebuilt static runtimes, add with `nix store add` |
| O-04-3 | §04 Open points | squashfs reader in the runtime | own minimal reader + `klauspost/compress/zstd` |
| O-04-4 | §04 Open points | Fleet-wide popularity for layer order | no; closure-local popularity in v1 |
| O-04-5 | §04 Open points | OCaml cross-compilation | native only in v1 |
| O-05-1 | §05.14, R-STORE-101 | Remaining cold-path LIST (lake partition drop, staging sweep) | keep the sweep until lake has a delete API and S3 lifecycle is verified on RustFS |
| O-05-2 | §05.14, R-LOG-100 | lake L1/L3/L4 upstream fixes | bob works around them; owner may upstream the reference patch |
| O-05-3 | §05.14 | `fleet.heartbeat` 15 s vs 60 s | 15 s, documented |
| O-05-4 | §05.14 | Worker container hardening beyond namespaces (seccomp against nested userns) | v1 without; Stage 3 security review |
| O-05-5 | §05.14 | S9/S10/S11/S17 numbers have no quiet-machine logs | re-measure in Stage 3 before Gate H |
| O-06-1 | §6.12, §6.6.2 | Cap of 10,000 release ids per visibility document | keep; switch to a CAS counter if hit |
| O-06-2 | §6.12 | Source snapshot cap 512 MiB | keep as a setting; measure on real monorepos |
| O-06-4 | §6.12 | GitHub not exercised live | replay tests + manual live check before the first release |
| O-06-5 | §6.12 | Visibility LRU TTL 30 s | accept and document |

Closed at integration: §05 visibility-marker format (one JSON document per object, §06 §6.6.2),
O-06-3 (live-log pull interval), §03 Rust lock layout, E-06-1 (worker connectivity).

# Appendix B — Budgets

Every **[proposal]** budget of §01–§06 (97 rows), with its profile and source measurement. The
harness budgets file is generated from the requirement entries, not from this table; this table is
the review view. "busy" marks numbers from a loaded host, which Stage 3 re-measures before Gate H.

| Section | Requirement / measurement | Budget | Profile | Source measurement |
|---|---|---|---|---|
| §01 §1.17 (R-NFR-001/002) | Evaluation, 1,000 `bob.go`, no change (eval key hit) | ≤ 0.1 s / ≤ 0.15 s | [8c/15G] / [2c/4G] | 0.47 s with `go build` no-op (S1 quiet) |
| §01 §1.17 (R-NFR-001/002) | Evaluation, 1,000 `bob.go`, one body change | ≤ 1.5 s / ≤ 3 s | [8c/15G] / [2c/4G] | 0.93 s (S1 quiet) |
| §01 §1.17 (R-NFR-001/002) | Evaluation, 1,000 wide, cold (empty GOCACHE) | ≤ 15 s / ≤ 30 s | [8c/15G] / [2c/4G] | 10.8 s / 23.9 s (S1, quiet run) |
| §01 §1.17 (R-NFR-001/002) | graph → drvs, N=1,000 new / unchanged | ≤ 3.5 s / ≤ 1.2 s / ≤ 3.5 s / ≤ 1.3 s | [8c/15G] / [2c/4G] | 3.03 / 1.11 s; 2.96 / 1.17 s (b-quiet) |
| §01 §1.17 (R-NFR-001/002) | graph → drvs, N=10,000 new / unchanged | ≤ 30 s / ≤ 9 s / ≤ 30 s / ≤ 10 s | [8c/15G] / [2c/4G] | 24.9 / 7.7 s; 26.2 / 8.7 s |
| §01 §1.17 (R-NFR-001/002) | peak RSS, N=10,000 | ≤ 300 MB / ≤ 300 MB | [8c/15G] / [2c/4G] | 175 MB / 217 MB |
| §01 §1.17 (R-NFR-001/002) | `examples/polyglot` no-op `bob build` (all stages) | ≤ 0.6 s / ≤ 0.8 s | [8c/15G] / [2c/4G] | 0.33 s / 0.45 s (backend only) + eval no-op 0.09 s |
| §01 §1.17 (R-NFR-001/002) | `examples/polyglot` cold (toolchains present) | ≤ 13 s / ≤ 25 s | [8c/15G] / [2c/4G] | 11.8 s / 22.0 s (B, bash builders) |
| §01 §1.17 (R-NFR-001/002) | namespace entry | ≤ 10 ms / ≤ 10 ms | [8c/15G] / [2c/4G] | 5–7 ms |
| §01 | R-NIX-030 native arm64 `//cmd/api:api` closure (NAR) | **≤ 74 MiB**, 12 paths max [8c/15G]; §04 R-OUT-025 uses the same value | [8c/15G] | 75.9 MiB without fixup (S8 §3.1, includes itoa-src, unstripped 7.7 MiB binary) → 73.4 MiB after fixup (strip −2.5 MiB, itoa-src dropped; S8 §3.1 numbers); native with cross-checked gcc ref: 366 MB (S7) |
| §01 | R-NIX-030 `api` binary, release, stripped | ≤ 5.5 MiB | [8c/15G] | 5.2 MiB (S8) |
| §01 | R-NIX-030 cross `//cmd/api:api@linux-amd64`, target libs native (two glibc) | ≤ 112 MiB | [8c/15G] | 113.8 MB (S7) |
| §01 | R-NIX-030 cross, target libs `Cross()` | ≤ 74 MiB | [8c/15G] | 76.1 MB (S7) |
| §02 | R-DEV-006 `bob dev -- true`, nothing changed (eval no-op + materialise no-op + namespace) | ≤ 1.0 s [2c/4G], ≤ 0.5 s [8c/15G] | [2c/4G] unless marked | §01 eval no-op ≤ 0.1 s [8c/15G] / ≤ 0.15 s [2c/4G] (§1.17); S4 materialise no-op 0.42 s [2c/4G]; namespace 5–7 ms |
| §02 | R-DEV-006 Vite ready inside `bob dev` | ≤ 1 s | [2c/4G] unless marked | S4 257–309 ms [8c] |
| §02 | R-DEV-006 no-op `bob build` (Go fixture) | ≤ 0.5 s [8c/15G] | [2c/4G] unless marked | S2 quiet: 0.26 s hybrid |
| §02 | R-DEV-006 one-file incremental build (Go fixture `main.go`) | ≤ 4 s [8c/15G] (= §03.8) | [2c/4G] unless marked | S2 quiet: 2.87 s hybrid |
| §02 | R-DEV-006 shim start (`/bin/true`) | ≤ 15 ms [8c/15G] | [2c/4G] unless marked | S1 6.8–7.8 ms |
| §02 | R-DEV-006 hook fast path | ≤ 20 ms p95 | [2c/4G] unless marked | R-DEV-007 (not measured yet) |
| §02 | R-DEV-006 first `bob build` of the Go fixture on a fresh machine with a warm server cache | ≤ 2 s + closure size / 100 MiB/s + cold eval | [2c/4G] unless marked | S12 quiet: 100.7 MiB substitute 0.61–0.67 s [8c]; S0b extraction 294 ms |
| §03 §03.8 | Go no-op build, fixture | ≤ 0.5 s / ≤ 0.5 s | [8c/15G] / [2c/4G] | hybrid 0.26 / 0.27 s |
| §03 §03.8 | Go one-file change in cgo main package (fixture) | ≤ 4 s / ≤ 4 s | [8c/15G] / [2c/4G] | 2.87 / 2.89 s |
| §03 §03.8 | Go library change with dependents (fixture `internal/msg`) | ≤ 4.5 s / ≤ 4.5 s | [8c/15G] / [2c/4G] | 3.28 / 2.99 s |
| §03 §03.8 | Go bottom-layer change, 200-pkg workspace (36 dependents) | ≤ 4.5 s / ≤ 4.5 s | [8c/15G] / [2c/4G] | 3.03 / 3.12 s |
| §03 §03.8 | Go graph → derivations, 1,000 packages, new / again | ≤ 5 / ≤ 2 s / ≤ 5 / ≤ 2 s | [8c/15G] / [2c/4G] | 3.53/1.33, 3.43/1.19 s |
| §03 §03.8 | Go cold fixture incl. std build | ≤ 40 s / ≤ 75 s | [8c/15G] / [2c/4G] | 25.0 / 48.7 s |
| §03 §03.8 | Go cold fixture, std cached | ≤ 10 s / ≤ 11 s | [8c/15G] / [2c/4G] | 6.11 / 7.15 s |
| §03 §03.8 | Go peak RSS of bob during a build | ≤ 300 MB / ≤ 300 MB | [8c/15G] / [2c/4G] | ≤ 160 MB (S2, busy) |
| §03 §03.8 | Rust no-op, 49-crate demo | ≤ 1 s / ≤ 1 s | [8c/15G] / [2c/4G] | 0.56 / 0.47 s |
| §03 §03.8 | Rust leaf crate change | ≤ 4.5 s / ≤ 7 s | [8c/15G] / [2c/4G] | 2.92 / 4.60 s |
| §03 §03.8 | Rust local lib change (with dependents) | ≤ 5.5 s / ≤ 8 s | [8c/15G] / [2c/4G] | 3.80 / 5.36 s |
| §03 §03.8 | Rust cold demo vs `cargo build --release` | ≤ 1.3× / ≤ 1.3× | [8c/15G] / [2c/4G] | 48.3/40.7 = 1.19×, 130.9/119.3 = 1.10× |
| §03 §03.8 | Rust per-derivation cost (trivial crate chain) | ≤ 0.25 s / ≤ 0.3 s | [8c/15G] / [2c/4G] | 0.146 s (chain30 [8c]) |
| §03 §03.8 | Rust plan + derivations, demo (no build) | ≤ 1 s / ≤ 2 s | [8c/15G] / [2c/4G] | 0.49 s [8c] |
| §03 §03.8 | Rust `bob lock`, 392 crates, warm index | ≤ 2 s / ≤ 4 s | [8c/15G] / [2c/4G] | 0.85 s offline [8c]; 1.9 s live (S3, busy) |
| §03 §03.8 | npm `bob lock`, ~200 packages, warm cache (revalidation) | ≤ 4 s / ≤ 5 s | [8c/15G] / [2c/4G] | 2.90 s ts-lib [8c]; 3.4 s [2c] (S4, busy) |
| §03 §03.8 | npm `bob lock`, inputs unchanged | ≤ 0.5 s / ≤ 1 s | [8c/15G] / [2c/4G] | offline 0.11–0.42 s [8c] |
| §03 §03.8 | npm materialise node_modules, no-op | ≤ 0.75 s / ≤ 0.75 s | [8c/15G] / [2c/4G] | 0.58 s [8c]; 0.42 s [2c] (busy) |
| §03 §03.8 | npm materialise after adding one dependency | ≤ 1.5 s / ≤ 1.5 s | [8c/15G] / [2c/4G] | 0.87 s [8c] |
| §03 §03.8 | npm cold node_modules, ~66 packages, tarballs fetched | ≤ 6 s / ≤ 8 s | [8c/15G] / [2c/4G] | 4.79 s [8c] |
| §03 §03.8 | npm Vite site, one source file changed (`vite build`) | ≤ 7 s / ≤ 12 s **[unmeasured 2c]** | [8c/15G] / [2c/4G] | 5.79 s [8c] |
| §03 §03.8 | npm save → HMR update (server side), p95 | ≤ 50 ms / ≤ 50 ms | [8c/15G] / [2c/4G] | median 2.4–3.3 ms, max 29 ms (S4, busy) |
| §03 §03.8 | npm tsserver first diagnostics through a shim | ≤ 4 s / ≤ 6 s | [8c/15G] / [2c/4G] | 2.1–2.6 s [8c] (S4, busy) |
| §03 §03.8 | OCaml cold demo (11 opam + C + lib + exe) | ≤ 25 s / ≤ 40 s | [8c/15G] / [2c/4G] | 15.9 / 25.5 s |
| §03 §03.8 | OCaml no-op | ≤ 0.5 s / ≤ 0.5 s | [8c/15G] / [2c/4G] | 0.33 / 0.29 s |
| §03 §03.8 | OCaml executable source change | ≤ 2.5 s / ≤ 2.5 s | [8c/15G] / [2c/4G] | 1.46 / 1.36 s |
| §03 §03.8 | OCaml library change (lib + exe) | ≤ 3 s / ≤ 3 s | [8c/15G] / [2c/4G] | 1.86 / 1.77 s |
| §03 §03.8 | OCaml resolve, ≤ 110-package graph | ≤ 0.5 s / ≤ 1 s | [8c/15G] / [2c/4G] | 0.15 s (S5, busy) |
| §03 §03.8 | codegen `bob gen` no-op | ≤ 0.3 s / ≤ 0.3 s | [8c/15G] / [2c/4G] | 0.16 / 0.16 s |
| §03 §03.8 | codegen `bob dev` proto edit → Go+TS files in tree | ≤ 2.5 s / ≤ 2.5 s | [8c/15G] / [2c/4G] | 1.39 / 1.35 s |
| §03 §03.8 | codegen whitespace-only proto change, full build (cutoff) | ≤ 2.5 s / ≤ 2.5 s | [8c/15G] / [2c/4G] | 1.43 / 1.36 s |
| §03 §03.8 | codegen `bob status` | ≤ 0.3 s / ≤ 0.3 s | [8c/15G] / [2c/4G] | 0.13 / 0.13 s |
| §03 §03.8 | C/C++ no-op build, fixture | ≤ 0.6 s / ≤ 0.8 s | [8c/15G] / [2c/4G] | backend B fixture 0.33 / 0.45 s (`b-quiet.md`) + eval no-op; = §01 §1.17 polyglot row |
| §03 §03.8 | C/C++ synthetic 100-package cold build | ≤ 15 s / ≤ 15 s | [8c/15G] / [2c/4G] | 10.0 / 9.4 s (`b-quiet.md`) |
| §05 §05.12 | R-NFR-100 Job pickup with wake-up | enqueue → claim p99 ≤ 1 s | [8c/15G] | V2: p50 106 ms, p99 182 ms (burst, 20 claimers) |
| §05 §05.12 | R-NFR-101 Job pickup polling only | p99 ≤ 1.2 × poll_interval + 1 s | [8c/15G] | S9 §3.5 model |
| §05 §05.12 | R-NFR-102 | Requests per job ≤ 26 (no LIST) | — | protocol count §05.5.1 |
| §05 §05.12 | R-NFR-103 | Idle worker ≤ 0.15 req/s | — | §05.5.1 |
| §05 §05.12 | R-NFR-104 | Dead worker's job queued again ≤ lease_ttl + 10 s + skew (75 s) | [8c/15G] | S9 chaos (3 s TTL scaled) |
| §05 §05.12 | R-NFR-105 | Live log line → screen p99 ≤ 1 s; degraded p99 ≤ flush_interval + 10 s | [8c/15G] | S17 fan-out 223 ms; S10 lake 2.7 s at 5 s flush |
| §05 §05.12 | R-NFR-106 | Registration → active p99 ≤ 5 s | [8c/15G] | registrar 2 s poll; S11 crypto < 1 ms |
| §05 §05.12 | R-NFR-107 | Revocation effective ≤ 10 s | — | S11 record cache |
| §05 §05.12 | R-NFR-108 | Per-job worker overhead (excluding work) ≤ 1 s; evalbox setup ≤ 25 ms | [8c/15G], [2c/4G] | S14 quiet: drv-closure fetch 0.24 s + upload 0.29 s; S13 quiet: 18.7 ms [8c], 15.0 ms [2c] |
| §05 §05.12 | R-NFR-109 | Total steady S3 load (workers + orchestrators) at 100 workers / 100 running jobs ≤ 30 req/s, 0 LIST | — | §05.5.1: idle 15 + reconcile 7 + jobs |
| §06 §6.11 (R-NFR-150) | 100 MiB closure: worker upload to staging | ≤ 1.5 s [8c/15G]; ≤ 3 s [2c/4G] | as stated | quiet 0.64–0.76 s [8c/15G]; loaded 1.19–2.13 s [2c/4G] (S12 §3.8) |
| §06 §6.11 (R-NFR-150) | 100 MiB closure: promotion | ≤ 1.5 s [8c/15G]; ≤ 3 s [2c/4G] | as stated | quiet 0.49–0.54 s; loaded 0.89–2.27 s |
| §06 §6.11 (R-NFR-150) | 100 MiB closure: Nix substitution from bob, same LAN | ≤ 1.5 s [8c/15G]; ≤ 3 s [2c/4G] | as stated | quiet 0.61–0.67 s; loaded 1.11–1.84 s |
| §06 §6.11 (R-NFR-150) | narinfo latency (warm visibility cache) | p50 ≤ 5 ms, p99 ≤ 20 ms | as stated | 2–5 ms incl. a visibility HEAD (S12 §5.7) |
| §06 §6.11 (R-NFR-150) | Orchestrator RSS at 10 × 1 GiB parallel downloads | ≤ 64 MiB | as stated | 19–33 MiB for 1–2 |
| §06 §6.11 (R-NFR-150) | API unary p99 (Get*, warm caches, RustFS local) | ≤ 50 ms; List* ≤ 150 ms | as stated | S12 narinfo 2–5 ms + one dynamitedb GET per index slot (§05) |
| §06 §6.11 (R-NFR-150) | Webhook → BuildRecord created | p99 ≤ 500 ms after receipt | as stated | delivery latency is the forge's (Forgejo 2–3 s, S16 §3.2) |
| §06 §6.11 (R-NFR-150) | Checkout mint (Forgejo / GitLab) | ≤ 500 ms p99 | as stated | 153 / 145 ms median (S16 §3.2, loaded) |
| §06 §6.11 (R-NFR-150) | Live log line → UI (D55 pull over Attach, 250 ms chunks) | = §05 R-NFR-105: p99 ≤ 1 s; lake fallback p99 ≤ `log.flush_interval` + 10 s (40 s at the 30 s default) | as stated | S17 fan-out p99 223 ms; S10 lake p99 2.7 s at 5 s flush |
| §06 §6.11 (R-NFR-150) | Token revocation effective | ≤ 10 s (API), ≤ 30 s visibility | as stated | cache TTLs §6.3.1/§6.6.2 |
| §01 | R-FW-045 evaluator sandbox, local defaults | wall clock 120 s, IR 64 MiB, stderr 1 MiB, pids 512, memory 2 GiB | local | S13 §5.2 |
| §01 | R-NIX-009 import 10,000 small source dirs | ≤ 5 s | [8c/15G] | gen-b §4: 3–10 s with nix-store --add (loaded) |
| §01 | R-FW-020 N=100 chain of trivial bob.Run steps, cold | ≤ 12 s | both | B: 10.0 / 9.4 s (bash builders) |
| §01 | R-NIX-027 nixpkgs index, polyglot fixture | cold ≤ 2 s, warm ≤ 50 ms | [8c/15G] | gen-b: 1.55 s / 0.02 s |
| §02 | R-DEV-007 hook fast path p95 | ≤ 10 ms / ≤ 20 ms | [8c/15G] / [2c/4G] | not measured; S1 shim 6.8–7.8 ms |
| §02 | R-DEV-008 shim start `/bin/true` p95 | ≤ 15 ms / ≤ 20 ms | [8c/15G] / [2c/4G] | S1 6.8–7.8 ms |
| §02 | R-DEV-010 warm definition through shim + LSP proxy (mean) | ≤ 2 ms / ≤ 3 ms | [8c/15G] / [2c/4G] | 0.55–0.72 ms / 0.56 ms |
| §02 | R-DEV-002 HMR update p95 (server side); dependency add → server answers | ≤ 50 ms; ≤ 10 s | [2c/4G] | median 3.3 ms, max 29 ms; relock 2.1–2.5 s + materialise 0.7–0.8 s + restart 0.3 s |
| §02 | R-INST-002 installed shim start p95 | ≤ 15 ms | [8c/15G] | S1 shim 6.8–7.8 ms |
| §02 | R-CLI-004 label completion p95 | ≤ 50 ms | [2c/4G] | not measured |
| §04 | R-PLAT-008 cross toolchain substitution / cold builds | ≤ 700 MiB, ≤ 15 s; api native ≤ 35/75 s, @linux-amd64 ≤ 35/75 s, +asan ≤ 35/85 s, +debug ≤ 30/60 s; variant add ≤ 60 s [2c/4G] | [8c/15G] / [2c/4G] | s07-cross.log quiet × 1.3 |
| §04 | R-OUT-024 OCI cold / incremental; AppImage; AppImage start | OCI ≤ 3/4 s, ≤ 1/1 s; AppImage ≤ 5/18 s, 100 MiB ≤ 30 s [2c]; start ≤ 0.5 s first, ≤ 0.1 s cached | [8c/15G] / [2c/4G] | s08-bundles.log quiet × 1.5: 1.91/2.3 s, 0.40/0.57 s, 3.00/11.6 s |
| §04 | R-OUT-025 sizes | AppImage ≤ 12 MB (≤ 23 MB unpruned); OCI arm64 ≤ 26 MB, v1→v2 ≤ 16 %; native api closure ≤ 74 MiB; cross ≤ 112 MiB; runtime ≤ 3 MiB | — | S8 §3.1/§3.2, S7 §3.2 |
| §05 | R-FLEET-009 requests per job (wake-ups on) | mean ≤ 26, 0 LIST | [8c/15G] | protocol count; V2 |
| §05 | R-FLEET-010 idle fleet cost, 100 workers | ≤ 15 req/s, PUT ≤ 7/s, LIST 0 | [8c/15G] | S9 §3.5 model |
| §05 | R-FLEET-001 registration → active p99 | ≤ 5 s | [8c/15G] | registrar poll 2 s; S11 |
| §05 | R-FLEET-005 split batch build of 73-drv layer graph | ≤ 15 s | [8c/15G] | S14 quiet: closure 20.6 s, per-drv 9.3 s |
| §05 | R-FLEET-012 enqueue → claim p99 with wake-ups | ≤ 1 s | [8c/15G] | V2: p50 106 ms, p99 182 ms |
| §05 | R-LOG-002 live line → follower p99 | ≤ 1 s | [8c/15G] | S17 p99 223 ms |
| §05 | R-LOG-101 degraded latency p99 | ≤ flush_interval + 10 s | — | S10/S17 |
| §05 | R-LOG-003 GetLog per package p99, 100M lines | ≤ 500 ms | — | S10: 111 ms |
| §06 | R-REG-151 orchestrator RSS, 10 × 1 GiB downloads | ≤ 64 MiB | — | S12 §3.4: 19–33 MiB |
| §06 | R-UI-007 first load | JS ≤ 250 KiB gz, CSS ≤ 30 KiB gz, FCP ≤ 1.0 / 2.0 s | [8c/15G] / [2c/4G] | not measured |

<!-- SECTIONS -->

# §01 — Framework core (`pkg/`)


Status: SPEC draft (Phase 2.3), 2026-10-21. Covers R-ARCH-001…005, R-NIX-001…025, R-FW-001…031
(core part), R-NFR-001/002 (core budgets). ADRs: 0001, 0002 (locked), 0003, 0009 (model part).
Budgets are **[proposal]** and derive from the quiet-machine logs (`docs/spikes/measure/b-quiet.md`,
`docs/spikes/measure/quiet/*.log`) unless marked otherwise.

## Interfaces

Provided by §01 (names used by other sections):

- `bob.P`, `bob.Define`, `bob.Package`, `bob.Label`, `bob.Position`, `bob.Sources`, `bob.Glob`,
  `bob.Files`, `bob.Dir`, `bob.Output`, `bob.Tree`, `bob.Tool`, `bob.Executable` (`MainProgram()`),
  `bob.Linkable` + `bob.LinkInfo`, `bob.Run`, `bob.Service`, `bob.Pipeline` (registration only),
  `bob.Repo`, `bob.Platform`, `bob.Variant`, `bob.NamedVariant` (§1.4–§1.9). Host platform:
  `p.Host().Platform()`.
- `ext` package (`pkg/bob/ext`): `ext.New`, `ext.Kind`, `ext.Config`, `ext.Axis`
  — the only way ecosystem packages (§03, §04) create graph nodes (§1.5).
- Graph IR v1 JSON (§1.6); the `engine.Ecosystem` registration interface (§1.6.1; it lives in
  `pkg/engine` because it must not be linked into the evaluator).
- `bob.lock` container format, `lock.File` (`pkg/engine/lock`), §1.8; §03 owns
  the ecosystem sections and entry types.
- `engine/drv`: `Derivation` (structured attributes), `Name`, `MakeStorePath`, `FixedOutputPath`,
  `NAR` (§1.10).
- `engine/fetch.Fetcher` (§1.11), `engine/store.Store` (embedded rootless Nix, §1.14),
  `engine/nixidx.Index` (§1.15), `engine/build.Run` (whole pipeline, §1.16).
- `bob-builder` job contract `bspec.Job` v1 and core ops (§1.12); fixup contract (§1.13).
- `engine.Substituter` and `engine.RemoteExecutor` interfaces (R-FW-009, used by §02 `--remote`).

Expected from other sections:

- §02: error codes and templates (`BOB-E-NNNN`, S15) for every error text in this section,
  exit codes, `bob dev`/`bob env`/shims/LSP proxy, the `bob.DevShell`/`bob.NewShell` API
  (§02.7.0, part of `pkg/bob`), the CLI config file
  that lists extra substituters and trusted keys (`store.Config.Substituters`).
- §03: ecosystem API packages and generators, their `bob.lock` sections, bob-builder ecosystem
  ops, generated native files and drift detection, materialisation manifest (paths excluded from
  source hashing), `cc.Linkable = bob.Linkable` alias.
- §04: cross toolchain selection, per-kind projection tables, OCI/AppImage outputs, size budgets
  of bundles.
- §05: worker Nix profile (root worker, build users, per-job container, D56) built on
  `store.Config`; evaluator sandbox limits on workers; fetch egress policy on workers.
- §06: pipeline trigger semantics on the server, using `bob.Pipeline` registration.

---

## 1.1 Repository layout and layering

#### R-ARCH-001 Monorepo layout
- **Statement:** One Go module `github.com/megakuul/bob` with top-level `pkg/`, `cmd/`,
  `internal/`, `proto/`, `web/`. `pkg/` has exactly the packages in the table of R-ARCH-003
  (new packages are added to that table in the same change).
- **Acceptance:** Given the repo, when `go list ./...` runs, then every package path starts with
  one of `pkg/`, `cmd/`, `internal/`, `proto/` (generated), and `go.mod`'s module line is
  `module github.com/megakuul/bob`.
- **Source:** R-ARCH-001.

#### R-ARCH-002 Framework never imports server code
- **Statement:** No package under `pkg/` and no `cmd/bob` package depends (transitively) on
  `internal/…`, generated `proto/…` server packages, or `cmd/bob-server`.
- **Acceptance:** `TestLayering` (in `pkg/layering_test.go`) runs
  `go list -deps -f '{{.ImportPath}}' ./pkg/... ./cmd/bob ./cmd/bob-builder` and fails if any
  result matches `^github.com/megakuul/bob/(internal|proto|cmd/bob-server)(/|$)`. CI runs it.
- **Source:** R-ARCH-002.

#### R-ARCH-003 Layers
- **Statement:** `pkg/` is layered; a package may import only packages of its own or a lower
  layer, plus the standard library and the third-party modules listed per layer.

| Layer | Packages | May import (besides std) | Runs inside the evaluator? |
|---|---|---|---|
| L0 core API | `pkg/bob`, `pkg/bob/ext` | — | yes |
| L1 sources | `pkg/nixpkgs` | L0 | yes |
| L2 ecosystem APIs | `pkg/cc`, `pkg/golang`, `pkg/rust`, `pkg/npm`, `pkg/ocaml`, codegen `pkg/proto`, `pkg/connect`, `pkg/graphql`, `pkg/openapi` (§03), `pkg/services/...` | L0, L1, other L2 APIs (acyclic) | yes |
| L3 output APIs | `pkg/output/oci`, `pkg/output/appimage`, `pkg/output/tarball`, `pkg/release` (§04) | L0–L2 | yes |
| E0 engine primitives | `pkg/engine/drv`, `pkg/engine/nar`, `pkg/engine/ir` | L0 | **no** |
| E1 engine services | `pkg/engine/store`, `pkg/engine/fetch`, `pkg/engine/lock`, `pkg/engine/nixidx`, `pkg/engine/srcs` | E0, L0; `golang.org/x/sys`, `github.com/klauspost/compress`, `github.com/ulikunitz/xz` | no |
| E2 generators | `pkg/<eco>/gen` (one per L2/L3 package, implements `engine.Ecosystem`), `pkg/engine` (registry, types) | E0, E1, L0–L3 types | no |
| E3 orchestration | `pkg/engine/eval`, `pkg/engine/build`, `pkg/engine/translate` | E0–E2, L0 | no |
| builder | `pkg/builder`, `pkg/builder/bspec`, `pkg/<eco>/bop` | E0, `bspec` | no (runs in Nix builds) |
| glue | `cmd/bob`, `pkg/cli` (§02) | everything in `pkg/` | no |

- **Acceptance:** `TestLayering` encodes the table and fails on any import of a higher layer, any
  L0–L3 package importing `pkg/engine/...` or `pkg/builder/...` (this keeps the evaluator small
  and free of engine code), any non-listed third-party module, and any L2 import cycle.
- **Source:** R-ARCH-003, ADR 0001 Consequences, S13 §5.4 (IR → drv is trusted code).

#### R-ARCH-004 Two binaries
- **Statement:** `cmd/bob` builds `bob`; `cmd/bob-server` builds `bob-server`. A third binary,
  `cmd/bob-builder`, is not installed by anyone: it is embedded in `bob` and lives in the Nix
  store (§1.12).
- **Acceptance:** `go list -deps ./cmd/bob` contains no `internal/` package (R-ARCH-002 test);
  `bob --help` lists no server command.
- **Source:** R-ARCH-004, D36.

#### R-ARCH-005 Self-hosting and bootstrap
- **Statement:** bob's own `bob.go` files build `bob`, `bob-builder` and `bob-server`. Bootstrap:
  `go build ./cmd/bob-builder` then `BOB_BOOTSTRAP_BUILDER=<path> go build -tags bootstrap ./cmd/bob`
  produces a first `bob` that embeds that builder and a static Nix fetched by
  `scripts/fetch-nix.sh` (pinned URL + sha256 of nixpkgs `nixStatic` at the pin). That `bob` then
  runs `bob build //cmd/bob:bob`.
- **Acceptance:** Given a clean checkout and a machine with Go ≥ the go.mod version, when
  `scripts/bootstrap.sh` runs, then it ends with a `bob` built by bob whose embedded builder
  store path equals the one in `pkg/engine/store/builders.go` (bit-reproducible builder).
- **Source:** R-ARCH-005, D17, ADR 0001 Consequences.

---

## 1.2 `bob.go` convention (ADR 0003)

#### R-FW-001 Typed Go, gopls-visible
- **Statement:** Build definitions are Go files that compile with the standard toolchain under
  `-tags=bob` and are type-checked by gopls through the `.bob/bin/gopls` shim (which sets
  `GOFLAGS=-tags=bob`) and editor settings `build.buildFlags=["-tags=bob"]` (§02).
- **Acceptance:** The S1 LSP script (`bobtest/lsp/acme.steps`: diagnostics, completion `foo.` →
  `Lib`, definition `foo.Lib` → `libs/foo/bob.go:12`, type error on wrong handle type) passes
  against the `examples/acme` repo with gopls from the pin, both directly and through the shim
  as a non-root user in bind and pivot mode.
- **Source:** R-FW-001, ADR 0003, S1 §3.2–3.3, D59.

#### R-FW-002 File convention and rules
- **Statement:** Definitions live in files named exactly `bob.go`, at most one per directory, next
  to the code they describe. Rules R1–R7 below are checked before compiling; all violations are
  reported together, sorted by path; any violation stops evaluation (exit class *definition*).

| Rule | Check | Error text (first line) |
|---|---|---|
| R1 | line 1 is exactly `//go:build bob` | `<dir>/bob.go: the first line must be "//go:build bob" so that go build, go vet and go test ignore it` |
| R2 | package clause = package of the directory's non-test `.go` files; without Go files: dir name lowercased, chars outside `[a-z0-9_]` removed, prefixed `x` if empty or starting with a digit | `<dir>/bob.go: package <x>, but the Go code in this directory is package <y>; bob.go must use package <y>` |
| R3 | no `bob.go` imports a `bob.go` package whose directory is `package main` | `<dir>/bob.go: imports the build definitions of <maindir>/, which is a main package and cannot be imported; define the shared package in a bob.go of a non-main directory` |
| R4 | no top-level identifier of `bob.go` is declared in another file of the package | `<dir>/bob.go:<line>: <Name> is also declared at <file>:<line>; bob.go shares its package with that code, rename one of them` |
| R5 | dir has `.c .cc .cpp .cxx .m .f .F .f90 .swig .swigcxx` files next to `bob.go` | not an error: bob materialises `<dir>/zz_bob_cgo.go` (gitignored): `//go:build bob && cgo` / `package <x>` / `import "C"` |
| R6 | no import cycle that exists only under the tag (bob.go import graph ∪ real Go import graph) | `<dir>/bob.go imports <dir2>, whose Go code imports <dir> (cycle exists only for build definitions); move the definition` |
| R7 | `bob.go` imports no `github.com/megakuul/bob/pkg/engine/...`, `.../pkg/builder/...`, `.../pkg/cli/...`, and no package outside the std library, the framework L0–L3 packages, and `bob.go` packages (own repo or bob-repo Go modules) | `<dir>/bob.go: imports <path>, which build definitions cannot use; bob.go may import the bob framework, the standard library and other bob.go packages` |

- Repo root = the nearest ancestor directory that contains `bob.lock` (§02 `FindRoot`, error
  0002); the bob-generated root `go.mod` lives in the same directory.
- Scan: from the repo root, skipping directories whose name
  starts with `.` or `_`, `node_modules`, `testdata`, and directories containing their own
  `go.mod`. No other file in the repo or the framework is named `bob.go` (framework: `core.go`).
- **Acceptance:** Fixture `bobtest/fixtures/rules/` has one sub-repo per rule; for each,
  `bob build //...` exits 3 and stderr contains exactly the rule's text with paths filled in; R5
  produces the stub and gopls (CGO_ENABLED=1) reports no diagnostics.
- **Source:** R-FW-002, D22, ADR 0003 Decision 6, S1 §5.6, A14.

#### R-FW-043 Evaluator generation
- **Statement:** bob builds one evaluator binary per repo state as specified in S1 §5.3, with
  these exact rules:
  1. Eval key = sha256 over: sorted (path, content) of every `bob.go`, `go.mod`, `go.sum`; the
     `go.mod` and every `bob.go` (scan rules of R-FW-002) of each directory `replace` target of
     the root `go.mod` (a dependency bob repo checked out locally; outside the repo only when
     the evaluation is not untrusted) [A90]; bob
     version; Go version; `GOARCH` of the host. Key equal to `.bob/eval/key` → reuse
     `.bob/eval/bob-eval`. A size+mtime index (`.bob/eval/index.json`) skips re-reading
     unchanged files; content is re-hashed when size or mtime differ.
  2. Overlay `.bob/eval/overlay.json` hides every other compilable file in a `bob.go` directory;
     `package main` `bob.go` files are replaced by a copy with the clause renamed to
     `bobmain<n>` on the same line; the virtual main `<root>/_bobeval/main.go` blank-imports all
     `bob.go` packages sorted by directory and calls `bob.Main()`.
  3. Dependency bob repos: their `go.mod` + `bob.go` files are copied to
     `.bob/eval/mods/<escaped module path>@<version>/` and swapped in through
     `.bob/eval/go.mod` (`replace` lines).
  4. The framework itself: `require github.com/megakuul/bob <bob's own version>` is written by
     bob into the generated root `go.mod`, and bob embeds the framework L0–L3 module zip of its
     own version, extracted into its module cache with the matching `go.sum` line. A dependency
     bob repo requiring a newer framework than the running bob → error
     `<module> needs bob <v> or newer; this is bob <v'>` + hint `update bob`.
  5. Compile: `GOWORK=off CGO_ENABLED=0 GOFLAGS= GOPROXY=off GOTOOLCHAIN=local go build -tags=bob
     -trimpath -modfile=<…> -overlay=<…> -o .bob/eval/bob-eval ./_bobeval` with the pinned Go,
     inside the evaluator sandbox (R-FW-045). Compiler messages naming
     `.bob/eval/rename/<n>.go` are rewritten to the original `bob.go` path.
  6. Run: `bob-eval` writes IR JSON (§1.6) to fd 3, diagnostics to stderr.
- **Acceptance:** (a) Two copies of `examples/acme` at different paths give byte-identical IR.
  (b) A real Go file in a `bob.go` directory that does not compile (`var X = generatedByBob`)
  does not affect evaluation. (c) A type error in `cmd/api/bob.go` (main package) is reported as
  `cmd/api/bob.go:<line>:<col>: …`. (d) Unchanged repo: second evaluation does not run
  `go build` (asserted via `--json` event `eval.cached=true`).
- **Source:** R-FW-001/002/008, ADR 0003 Decision 4, S1 §5.3.

#### R-FW-044 Evaluator ↔ bob version lock
- **Statement:** The IR carries `bobVersion` and `irVersion`; bob rejects IR whose `bobVersion`
  differs from its own (internal error; cannot happen unless rule R-FW-043.4 is broken).
- **Acceptance:** Unit test feeds IR with another `bobVersion` → error class internal, exit 70.
- **Source:** R-FW-008, BOOTSTRAP §5.4 (hostile input).

#### R-FW-045 Evaluator sandbox (local and worker)
- **Statement:** The evaluator (compile and run) always runs in the S13 evalbox (user, mount,
  net, pid, ipc, uts, cgroup namespaces; read-only binds; nested userns uid 1000; no_new_privs;
  Landlock; seccomp) with the S13 §5.2 contract. Locally, cgroup limits apply only when a
  delegated cgroup is available; wall-clock timeout and output caps always apply. Worker limits
  are §05's.
- **Local defaults [proposal]:** wall clock 120 s, IR output 64 MiB, stderr kept 1 MiB, pids 512,
  memory 2 GiB (enforced always, by RLIMIT_AS or a polling watchdog, and by the cgroup when delegated).
- **Acceptance:** the 15 S13 hostile cases (`bobtest/evalbox/`) each fail with their S13 error
  text locally without a delegated cgroup (except the three cgroup-only cases, which are skipped
  with a note), and all 15 on a machine with delegation.
- **Source:** R-SEC-000/004, S13 §5.1–5.3, S13-4.

#### R-FW-008 Determinism
- **Statement:** The evaluator has no access to time, environment, network, or files other than
  the read-only source tree; the framework API offers no such access; IR is canonicalised (§1.6).
  `bob check --determinism` evaluates twice in separate sandboxes and compares IR bytes.
- **Acceptance:** (a) `bob check --determinism` on every example repo passes. (b) A fixture whose
  `bob.go` builds a deps slice by ranging over a map with ≥ 8 keys makes `bob check
  --determinism` fail within 5 runs with BOB-E-1018 `✗ evaluating bob.go is not deterministic: <label>
  differs between two runs (field <json path>)` + hint `do not range over maps when building
  lists; sort the keys`.
- **Source:** R-FW-008.

---

## 1.3 Labels

#### R-FW-015 Label grammar
- **Statement:** Labels use this grammar (ABNF-like):

```
label      = local / foreign / nixlabel
local      = "//" [dir] ":" name
foreign    = modpath "//" [dir] ":" name
nixlabel   = "nixpkgs:" attr
dir        = seg *("/" seg)          ; seg = 1*( ALPHA / DIGIT / "_" / "-" / "." ), not "." or ".."
name       = (lower / DIGIT) *127(lower / DIGIT / "_" / "-" / ".")
attr       = 1*( ALPHA / DIGIT / "_" / "-" / "'" / "." )
configured = label "@" [platform] *("+" token)    ; §1.9
pattern    = "//..." / "//" dir "/..." / "//" [dir] ":all" / label / "//" dir   ; "//a/b" = "//a/b:b"
```

- **Bounds (hostile input):** the whole string is ≤ 1024 bytes and one `seg` ≤ 255 bytes; longer
  is invalid (a 1000-byte `dir` is valid, a 5000-byte one is not).
- `<dir>` is the directory of the `bob.go` that made the outermost constructor call (first stack
  frame outside `github.com/megakuul/bob/`); `<name>` is the constructor's name argument.
- CLI relative forms (`:name`, `./...`, `.`) resolve against the current directory (§02).
- Duplicate label → `<pos>: package <label> is already defined at <pos2>`.
- **Acceptance:** Table test `pkg/bob/label_test.go`: ≥ 30 valid and invalid strings incl.
  `//:x`, `//a/b`, `example.com/o//libs/bar:bar`, `nixpkgs:python3Packages.numpy`,
  `//api:api@linux-amd64+debug+asan`, `//api:API` (invalid), `//a/../b:x` (invalid),
  `//a:b+c` (invalid). `Parse` accepts all the forms of the table (`//a/b`, `//api:api@linux-amd64+debug+asan` are valid
  for `Parse`; `ParseLabel` accepts labels only). Round trip `Parse(s).String()==s` for canonical
  inputs. A 1000-byte dir is valid, a 5000-byte dir is invalid.
- **Source:** R-FW-015, ADR 0003 Decision 5, ADR 0009 Decision 5, S1 §5.4.

#### R-FW-016 Source positions
- **Statement:** Every graph node carries `definedAt` = module-relative file and line of the
  outermost constructor call in a `bob.go` (with `-trimpath`: `<module>[@<version>]/<dir>/bob.go`);
  bob displays own-repo positions repo-relative (`cmd/api/bob.go:15`).
- **Acceptance:** For each node in `examples/acme`'s IR, `definedAt` points at the line with the
  constructor call (golden file). `definedAt` is the line of the call to the ecosystem
  constructor (the first stack frame outside `github.com/megakuul/bob/`); in `libs/foo/bob.go`
  that is line 13 (line 12 is the `var Lib = bob.Define(` line used by R-FW-001). A failing C++
  build of `//libs/foo:foo` prints `(defined at libs/foo/bob.go:13)`.
- **Source:** R-FW-016, R-NIX-012.

---

## 1.4 Core API (`pkg/bob`)

Everything below is the complete public surface of L0. Doc comments are normative.

```go
// Package bob is the core of bob's build definitions. It is imported by bob.go files.
package bob

// P is the configuration a constructor builds for: one platform and one variant.
// bob creates one P per requested configuration; constructors must not keep it.
type P struct{ /* unexported */ }

// Platform returns the platform packages built with p run on.
func (p *P) Platform() Platform
// Variant returns the variant of p (profile, sanitisers, features, named variants).
func (p *P) Variant() Variant
// Host returns the configuration for build-time tools of p: the host platform, release
// profile, no sanitisers, no features. Host().Host() == Host().
func (p *P) Host() *P

// Define turns a constructor into a memoised, registered constructor. It must be called in a
// package-level variable declaration of a bob.go file. The returned function calls fn at most
// once per P and returns the same value on every later call with that P (R-FW-011).
// Every value reachable from fn's result that implements Package becomes part of the graph.
func Define[T any](fn func(p *P) T) func(p *P) T

// Main is called by the generated evaluator. bob.go files never call it.
func Main()

// Package is a node of the build graph. It is implemented only by types that embed Node,
// and a Node is created only by ext.New; the unexported method seals the interface.
type Package interface {
	Label() Label
	DefinedAt() Position
	// Outputs lists the package's store outputs ("out" first, then e.g. "dev", "lib").
	Outputs() []Output
	bobNode() *Node
}

// Node is embedded by every ecosystem package type. Its fields are unexported; ext.New
// creates it (through an internal hook in pkg/bob/internal/graph, which avoids an import cycle).
type Node struct{ /* unexported */ }
func (n *Node) Label() Label
func (n *Node) DefinedAt() Position
func (n *Node) Outputs() []Output

// Output names one store output of a package, e.g. the "dev" output of a library.
type Output struct {
	Package Package
	Name    string // "out", "dev", "lib", "debug"
}

// Tree is a package whose "out" output is a directory tree that other steps can read.
type Tree interface{ Package; tree() }

// Tool is a package that provides executables in bin/ for build steps and dev shells.
type Tool interface{ Package; tool() }

// Executable is a package with one main program, e.g. a Go or Rust binary.
// MainProgram is a path relative to the "out" output, e.g. "bin/api".
type Executable interface{ Tool; MainProgram() string }

// Linkable is a native (C ABI) library. cc.Linkable is an alias of it (§03).
type Linkable interface{ Package; LinkInfo() LinkInfo }

// LinkInfo is what a Linkable gives its consumers (cgo, Rust Native, OCaml Native, cc Deps).
// Paths are store paths; they are filled by bob's generators, not by the evaluator
// (the evaluator only records the Linkable's identity and Modules/Libs choices).
type LinkInfo struct {
	IncludeDirs []string   // -I
	LinkDirs    []string   // -L
	Libs        []string   // -l names, in link order
	Archives    []string   // static archives, absolute paths
	LinkArgs    []string   // extra linker arguments
	RuntimeDirs []string   // RUNPATH entries
	PkgConfig   []string   // pkg-config dirs (PKG_CONFIG_PATH)
	Public      []Linkable // transitive public set, needed at the final link
}

// Label identifies a package, e.g. //libs/foo:foo (see SPEC §1.3).
type Label struct{ Module, Dir, Name string; Nix string }
// ParseLabel accepts labels only (local, foreign, nixlabel).
func ParseLabel(s string) (Label, error)
func (l Label) String() string

// Parse accepts the union of label, pattern and configured forms (R-FW-015) and returns a typed
// value: a Label, a Pattern or a Configured label. Use ParseLabel when only a label is valid.
func Parse(s string) (Ref, error)

// Ref is the typed result of Parse: a Label, a Pattern or a Configured value; String() returns
// the canonical form.
type Ref interface{ String() string }

// Pattern is a label pattern ("//...", "//dir/...", "//dir:all", a label, "//dir"); a string type
// so that fixtures can write []bob.Pattern{"//..."}.
type Pattern string

// Position is a file and line in a bob.go file, module-relative.
type Position struct{ File string; Line int }
func (p Position) String() string // "libs/foo/bob.go:12"
```

#### R-FW-010 Values, not strings
- **Statement:** Packages are passed as typed values; the only strings naming packages are the
  constructor's own name argument and nixpkgs attribute names (R-NIX-013).
- **Acceptance:** `bobtest/typecheck/`: passing `golang.Binary` where `bob.Linkable` is expected
  fails `go vet -tags=bob` with `missing method LinkInfo`; the example programs (R-FW-013)
  contain no label string literals except constructor names.
- **Source:** R-FW-010.

#### R-FW-011 Memoised constructors
- **Statement:** `Define` memoises per (definition, `*P`). Calling a defined constructor outside
  evaluation (e.g. in `init`) panics with `bob: <pos>: <Var> was called before evaluation; call
  it only inside another bob.Define function`. Calling `Define` inside a constructor →
  definition error `<pos>: bob.Define must be used in a package-level var declaration`.
  Recursive calls during their own construction → definition error 1006 (cycle) naming the chain.
- **Example:**

```go
//go:build bob

package foo

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/cc"
	"github.com/megakuul/bob/pkg/nixpkgs"
)

// Lib is libfoo.so (label //libs/foo:foo).
var Lib = bob.Define(func(p *bob.P) *cc.Library {
	return cc.SharedLibrary(p, "foo", cc.LibraryOpts{
		Srcs: bob.Glob("src/*.cpp"),
		Hdrs: bob.Dir("include"),
		Deps: []bob.Linkable{nixpkgs.Lib(p, "openssl")},
	})
})
```

- **Acceptance:** Unit tests: `Lib(p) == Lib(p)` (same pointer); two different `P` give two
  values; the three error cases print their texts.
- **Source:** R-FW-011, D23, S1 §5.5.

#### R-FW-012 / R-FW-014 API style and one way per task
- **Statement:** Constructors take `(p *bob.P, name string, opts <Kind>Opts)`; options are plain
  structs with zero-value defaults; builder chaining is used only for modifiers that refine an
  existing value (`rust.Crate("openssl-sys", "0.9").Uses(…)`). Each ecosystem offers exactly one
  constructor per kind. Every exported constructor has a doc comment and a runnable example
  (`Example<Func>` test in `_test.go` under `-tags=bob`).
- **Acceptance:** `TestAPISurface` lists exported functions of L0–L3 and fails if a constructor
  lacks an `Example` test or if two exported functions return the same kind type from the same
  package (manual review of the list at Gate S).
- **Source:** R-FW-012, R-FW-014, D24, R-FW-013.

#### R-FW-013 Example programs
- **Statement:** `examples/` holds six repos: `cpp-lib`, `go-cgo-service`, `rust-crate`,
  `svelte-frontend`, `polyglot` (= S0a fixture: C++ + openssl, Rust staticlib, protobuf, cgo Go),
  `cross-repo` (consumer + a served bob module). They are the API acceptance tests.
- **Acceptance:** CI: for each, `bob build //...` and `bob test //...` exit 0 on both profiles;
  `polyglot`'s `//cmd/api:api` prints `digest=81b637d8… numhex=3432 proto_bytes=116`.
  Readability review (manual) at Gate S.
- **Source:** R-FW-013.

### 1.4.1 Sources

```go
// Sources is a set of files from the repo, relative to the directory of the calling bob.go.
type Sources struct{ /* unexported */ }

// Glob selects files by patterns: "*" and "?" do not cross "/", "**" matches any depth,
// "[...]" classes as in path.Match. A pattern starting with "!" excludes.
//   bob.Glob("src/**/*.cpp", "!src/legacy/**")
func Glob(patterns ...string) Sources
// Files selects exact files (no patterns).  bob.Files("foo_test.cpp")
func Files(paths ...string) Sources
// Dir selects a whole directory subtree.  bob.Dir("include")
func Dir(path string) Sources
// Union combines sources.
func (s Sources) Union(o ...Sources) Sources

// FetchOpts declares one external file or archive [proposal, added at integration].
type FetchOpts struct {
	URL     string   // https (http only with a pinned hash)
	Mirrors []string // tried after URL, in order
	Archive bool     // unpack (formats of R-NIX-014); false = flat file
	Strip   int      // leading path components to drop (Archive)
	Name    string   // store name; default from the URL's base name via drv.Name
}

// Fetch declares an external input pinned in bob.lock "sources" (§1.8) and fetched by bob's
// Fetcher (§1.11), never by a Nix FOD. Its "out" is the file or the unpacked tree.
//
//	var Fonts = bob.Define(func(p *bob.P) bob.Tree {
//		return bob.Fetch(p, bob.FetchOpts{URL: "https://example.org/fonts-1.2.tar.gz", Archive: true, Strip: 1})
//	})
func Fetch(p *P, o FetchOpts) Tree
```

#### R-FW-021 Source hashing
- **Statement:** A `Sources` value resolves (outside the evaluator, by trusted bob code) to the sorted set
  of regular files and symlinks matching it, always excluding: `.git/`, `.bob/`, every `bob.go`
  and `zz_bob_cgo.go`, files ignored by any `.gitignore` on the path (gitignore semantics
  implemented in Go, no git binary), `.git/info/exclude` patterns, and paths in the
  materialisation manifest (§03). Each package's source set becomes one content-addressed store
  path named `<store-name>-src` whose tree keeps repo-relative paths (§1.11).
- Errors: pattern escaping the module root (`..`, absolute) → `<pos>: source pattern "<p>" leaves
  the repository`; a pattern that matches nothing → `<pos>: bob.Glob("<p>") matches no files`;
  a symlink pointing outside the repo → `<file>: symlink points outside the repository`.
- **Acceptance:** (a) Adding a gitignored file or editing `bob.go` changes no source store path.
  (b) Changing one `.cpp` file changes exactly the source path of the package(s) whose `Sources`
  contains it. (c) File modes other than the executable bit and mtimes do not change the path.
- **Source:** R-FW-021, S6 (materialisation), gen-b §4.

### 1.4.2 Hermetic custom step (`bob.Run`)

```go
// RunOpts declares a hermetic step. Everything the command may read is declared here.
type RunOpts struct {
	Tools   []Tool            // on PATH, in order (nixpkgs.Tool or bob-built tools)
	Srcs    Sources           // copied writable to $BOB_SRC (cwd)
	Inputs  map[string]Tree   // read-only at $BOB_IN_<NAME> (NAME = key upper-cased, [A-Z0-9_])
	Cmd     []string          // argv; no shell. ${VAR} expands from the step's environment only
	Env     map[string]string // extra environment
	Outputs []string          // paths relative to $BOB_OUT that must exist afterwards
	Timeout time.Duration     // 0 = 30 min
}

// Run creates a sandboxed, cached build step. Its output tree is the Step (a Tree).
//   gen := bob.Run(p, "version-file", bob.RunOpts{
//       Tools:   []bob.Tool{nixpkgs.Tool(p, "coreutils")},
//       Cmd:     []string{"sh", "-c", "…"}, // only if a shell is declared as a Tool
//       Outputs: []string{"version.txt"},
//   })
func Run(p *P, name string, o RunOpts) *Step

// Step is the result of Run; it implements Tree. File returns one declared output as a Tree.
type Step struct{ /* unexported */ }
func (s *Step) File(path string) Tree
```

#### R-FW-023 `bob.Run`
- **Statement:** `Run` becomes one derivation built by bob-builder op `exec`. Inside: `PATH` = the
  declared tools' `bin/` only; `HOME=/homeless-shelter`; `TMPDIR=/build/tmp`;
  `BOB_SRC=/build/src` (cwd), `BOB_OUT=$out`, `SOURCE_DATE_EPOCH=1`, `TZ=UTC`, `LC_ALL=C`; no
  network. A missing declared output → build error `<label>: step finished but did not create
  <path>`; undeclared files in `$out` are kept. There is no API that accepts Nix code.
- **Acceptance:** (a) A step running `curl` fails with the network error template (2009). (b) A
  step reading `/home` sees nothing. (c) The same step on two machines gives the same output path
  and NarHash. (d) Grep of `pkg/` public API for parameters named or typed `Nix…Expr` finds none.
- **Source:** R-FW-023, D27.

### 1.4.3 Tests and services

#### R-FW-024 Tests are packages
- **Statement:** Every ecosystem's `Test` constructor returns a `bob.Test` (`Package`). Running a
  test = building its derivation; the builder exits non-zero on failure (error 2005), so only
  passing results are cached. A passing test's output holds `test-result.json` (§03 `TestResult`
  v1) and `test.log`.
  `bob test --rerun` forces a rebuild (`nix build --rebuild`) of selected tests.
- **Acceptance:** Second `bob test //...` with no change runs no builder (cache hit, ≤ the
  no-op budget); a failing test reruns on every invocation.
- **Source:** R-FW-024.

#### R-FW-025 Loopback services
- **Statement:** API as S13 §5.5 with `*bob.P` (not `*bob.Project`):

```go
type Service interface{ serviceSpec() *ServiceSpec } // sealed
type ServiceSpec struct {
	Name    string            // "postgres"; used in errors and logs
	Start   []string          // argv inside the test sandbox
	Tools   []Tool
	Ready   Probe             // TCPReady(port) | CmdReady(argv...)
	Env     map[string]string // exported to the test
	Timeout time.Duration     // readiness timeout, default 30 s
}
func TCPReady(port int) Probe
func CmdReady(argv ...string) Probe
type ServiceOptions struct{ Args []string; Env map[string]string; Ready Probe; Timeout time.Duration }
// ServiceFrom runs a binary built in this repo as a service of a test.
func ServiceFrom(bin Tool, o ServiceOptions) Service
```

  Instance *i* (list order, from 0) binds `127.0.0.(i+2)` and exports `<NAME>_ADDR`; startup in
  list order, the test starts after every `Ready` passes; teardown SIGTERM, 5 s, then pid
  namespace end. `pkg/services/postgres` is the v1 reference service.
- **Acceptance:** S13 tests: Postgres test passes ≤ 3 s [8c/15G] (measured 1.3–2.0 s); two such
  tests in parallel both pass; 0 service processes remain; a never-ready service fails with
  `<label>: service <name> did not become ready within 30s` + last 20 log lines.
- **Source:** R-FW-025, S13 §3.4/§5.5.

### 1.4.4 Repository settings and pipelines (registration only)

```go
// Repo declares repository-wide settings. It is allowed only in the root bob.go, at most once.
//   var _ = bob.Repo(bob.RepoOpts{Nixpkgs: "nixos-26.05"})
func Repo(o RepoOpts) struct{}
type RepoOpts struct {
	// Nixpkgs is a channel name ("nixos-26.05") or "<channel>@<40-hex rev>". bob.lock pins the
	// revision; `bob lock --update nixpkgs` moves it to the channel's current revision.
	Nixpkgs string
	Variants []Named // see §1.9
}

// Pipeline registers a pipeline (R-FW-040). Trigger semantics on the server are SPEC §06.
func Pipeline(name string, o PipelineOpts) struct{}

// PipelineOpts is typed; the zero value is valid and means a manual-only pipeline. It maps
// 1:1 onto proto `Pipeline` (§6.5.6): triggers (human form), concurrency, releases.
type PipelineOpts struct {
	Triggers    []Trigger   // none = manual runs only
	Concurrency Concurrency // unset = the per-ref default of §05.6.2
	Targets     []Pattern   // packages to build
	Tests       []Pattern   // packages to test
	Releases    []string    // release names to publish; non-empty = release pipeline (proto releases=true)
}

// Trigger says when a pipeline starts.
type Trigger struct{ /* unexported */ }
func OnPush(branchGlob string) Trigger // human form "push <glob>"
func OnPullRequest() Trigger           // "pull_request"
func OnTag(glob string) Trigger        // "tag <glob>"

// Concurrency says what happens to a run of the same pipeline and ref while one is active.
type Concurrency int
const ( Queue Concurrency = iota + 1; Supersede; Parallel ) // zero value = per-ref default
```

- Repo errors: `Repo` outside the root `bob.go` or twice → definition error; missing →
  default `Nixpkgs` = the channel bob was released with (recorded in bob's build info).

---

## 1.5 Ecosystem extension API (`pkg/bob/ext`)

#### R-FW-046 Node creation contract
- **Statement:** Ecosystem API packages create graph nodes only through `ext.New`; the node's
  payload is a JSON-serialisable struct registered per kind. A kind declares the variant axes it
  reads; `ext.Config` exposes only those (R-FW-031 projection is structural).

```go
package ext

// Kind describes one node kind, e.g. "cc.library". Registered in an init() of the API package.
type Kind struct {
	Name   string  // "<ecosystem>.<kind>", [a-z0-9.]+
	Axes   []Axis  // variant axes this kind's derivation depends on
	Host   bool    // always built for p.Host() (codegen tools, proc-macros)
}
type Axis int
const ( AxisPlatform Axis = iota; AxisProfile; AxisSanitizers; AxisGoRace; AxisFeatures )
func Register(k Kind)

// Config is the projected configuration a node sees.
type Config struct{ /* unexported */ }
func (c Config) Platform() bob.Platform   // panics if AxisPlatform not declared
func (c Config) Profile() bob.Profile     // panics if AxisProfile not declared
func (c Config) Sanitizers() []bob.Sanitizer
func (c Config) Race() bool
func (c Config) Features(l bob.Label) []string

// New creates a node of kind for p. name is the label name; deps are the node's direct
// package dependencies (order is kept); spec is the kind's payload, serialised into the IR.
func New(p *bob.P, kind, name string, deps []bob.Package, srcs []bob.Sources, spec any) bob.Node
// ConfigOf returns the projected configuration of a node.
func ConfigOf(n *bob.Node) Config
```

- **Acceptance:** A unit test registers a kind with `Axes={AxisPlatform}`; its constructor
  calling `ext.ConfigOf(n).Profile()` panics with `ext: kind <k> reads the profile but did not declare
  AxisProfile`; two P differing only in profile produce IR nodes with equal `config` projections
  and equal derivation paths.
- The per-kind axis table is §04 R-FW-038; this entry makes it structural.
- **Source:** R-FW-031, R-FW-038 (§04), ADR 0009 Decision 4, R-LANG §7.7.

---

## 1.6 Graph IR v1

#### R-FW-047 IR format
- **Statement:** The evaluator writes one JSON document (UTF-8, canonical: keys sorted, no
  insignificant whitespace, nodes sorted by `id`) to fd 3. Schema:

| Field | Type | Meaning |
|---|---|---|
| `irVersion` | int | `1` |
| `bobVersion` | string | must equal the running bob's version |
| `module` | string | root Go module path |
| `repo` | object | `{"nixpkgs": "<RepoOpts.Nixpkgs>", "variants": [...]}` |
| `configs` | array | `{"id", "platform", "profile", "sanitizers":[], "race":bool, "features":{label:[...]}, "named":[...]}` |
| `nodes[]` | array | one per (package, projected config) |
| `nodes[].id` | string | canonical configured label (§1.9), unique |
| `nodes[].kind` | string | registered kind name |
| `nodes[].label`, `.definedAt` | string, `{file,line}` | |
| `nodes[].proj` | object | only the declared axes' values |
| `nodes[].deps` | array of ids | order kept |
| `nodes[].srcs` | array | `{"dir": "<bob.go dir>", "patterns": [...]}` (resolved by bob, not by the evaluator) |
| `nodes[].spec` | object | kind payload (§03/§04 schemas) |
| `nodes[].nix` | object | for `nixpkgs:` nodes: `{"attr","set":"host|target|cross","outputs":[...],"allow":{"insecure":[...],"unfree":bool}}` |
| `roots` | array of ids | packages requested by the CLI patterns |
| `pipelines` | array | §06 |

- bob validates before use: size ≤ 64 MiB, ≤ 1,000,000 nodes, every dep id exists, no cycles
  (error 1006 with the chain and each `definedAt`), unknown kinds rejected, every string ≤ 64 KiB. The node cap shares the IR-invalid error of the
  field validation: a rejection with any structured IR-invalid error satisfies the cap test.
- **Acceptance:** JSON-schema file `pkg/engine/ir/ir.schema.json`; fuzz test of the IR parser
  (no panic, bounded memory); golden IR for `examples/polyglot`.
- **Source:** R-FW-008, R-SEC-004, gen-b §1, S13 §5.2.

### 1.6.1 Ecosystem registration (engine side)

#### R-FW-051 `engine.Ecosystem`
- **Statement:** Each ecosystem registers one implementation from its generator package
  (`pkg/<eco>/gen`, init) with `engine.Register(e Ecosystem)`; the interface is §03.0.1's, with
  these request/result types (in `pkg/engine`, never linked into the evaluator):

```go
type Ecosystem interface {
	Name() string                                                    // bob.lock section key
	Lock(ctx context.Context, req LockRequest) (json.RawMessage, error)
	Plan(ctx context.Context, req PlanRequest) (PlanResult, error)
	Native(ctx context.Context, req PlanRequest) ([]GeneratedFile, error)
}
type LockRequest struct {
	Graph   *ir.Graph         // validated IR (all configurations)
	Prev    json.RawMessage   // previous section, nil if none
	Update  []string          // names to move (bob lock --update), empty = keep pins
	Fetch   fetch.Fetcher
	Nixpkgs *nixidx.Index
}
type PlanRequest struct {
	Graph   *ir.Graph
	Nodes   []*ir.Node          // this ecosystem's nodes (by kind prefix "<name>.")
	Lock    json.RawMessage     // this ecosystem's section
	Inputs  map[string]Resolved // node id → resolved deps: drv paths/outputs, store paths, LinkInfo
	Sources map[string]string   // node id → imported source store path (R-NIX-009)
	Nixpkgs *nixidx.Index
	Builder string              // bob-builder store path for the host system
}
type PlanResult struct {
	Drvs    []*drv.Derivation        // new derivations, any order (bob sorts topologically)
	Nodes   map[string]Resolved      // node id → its outputs and, for Linkables, filled LinkInfo
	Fetches []fetch.Request          // external inputs; their paths are computed from the lock
	                                 // hashes (drv.FixedOutputPath), the fetcher makes them valid before nix build
}
type Resolved struct {
	Drv      string            // drv path ("" for plain store paths)
	Outputs  map[string]string // output name → store path
	LinkInfo *bob.LinkInfo
	MainProgram string
}
```

  bob calls `Plan` per ecosystem in dependency order of the ecosystems' nodes (a node's deps of
  other ecosystems are resolved first); `Plan` must be deterministic and run no processes.
- **Acceptance:** a test ecosystem `bobtest/eco/toy` registered from its own package builds a
  two-node graph with no change to `pkg/engine` (self-contained ecosystems, §7.7).
- **Source:** §03.0.1, R-ARCH-003, REQUIREMENTS §7.7.

---

## 1.7 nixpkgs as package source (`pkg/nixpkgs`)

```go
package nixpkgs

// Tool is a nixpkgs package used at build time (always from the host package set).
//   nixpkgs.Tool(p, "protobuf")
func Tool(p *bob.P, attr string, opts ...Option) *Package
// Lib is a nixpkgs library linked into target code (target package set; see Cross).
//   nixpkgs.Lib(p, "openssl", nixpkgs.Modules("libcrypto"))
func Lib(p *bob.P, attr string, opts ...Option) *Package
// Pkg is any other nixpkgs package needed at run time (data, services, runtime tools).
func Pkg(p *bob.P, attr string, opts ...Option) *Package

type Option func(*options)
func Outputs(names ...string) Option        // default: the package's default outputs
func Modules(names ...string) Option        // pkg-config modules a Lib links; default: all modules of its dev output
func Cross() Option                         // take a target library from pkgsCross (A19); default: the target system's own set
func AllowInsecure(ids ...string) Option    // accept these knownVulnerabilities entries
func AllowUnfree() Option

// Package implements bob.Tool, bob.Linkable and bob.Tree (nixpkgs.Package of §03).
type Package struct{ bob.Node }
```

#### R-NIX-013 Only `Nixpkgs` leaks
- **Statement:** The only Nix concept in the public API is nixpkgs attribute names (and the
  channel name in `RepoOpts`). No public identifier contains `Derivation`, `Drv`, `StorePath`,
  `Flake` or `Expr`; users never see a `.nix` file.
- **Acceptance:** `TestAPISurface` greps exported identifiers of L0–L3 for those words → none;
  `find examples -name '*.nix'` → none, also after `bob dev`.
- **Source:** R-NIX-013, D6.

Typing and set selection (`Tool` = host set, `Lib` = target set, `Cross()`) are specified by
§04 R-FW-032/R-FW-036; §01 implements them in `resolve.nix` calls (`system` argument) and the IR
`nix.set` field. The index also answers "is this output cached?" for §04's note
(`Index.Cached(ctx, outPath) (bool, error)`: one `HEAD <substituter>/<hash>.narinfo` per configured
substituter, cached per run).

---

## 1.8 `bob.lock`

#### R-FW-004 Container format
- **Statement:** `bob.lock` at the repo root is canonical JSON: UTF-8, 2-space indent, object keys
  sorted bytewise, arrays in the order each schema defines, no floats, hashes in SRI form
  (`sha256-<base64>`, `sha512-<base64>`), `\n` line ends, one final newline. bob writes it
  atomically (temp file + rename) and only when the content changes. Top level:

| Key | Owner | Content |
|---|---|---|
| `lockVersion` | §01 | `1` |
| `nixpkgs` | §01 | `{"channel","rev","url","narHash"}` |
| `sources` | §01 | generic external sources (`bob.Fetch`): map id → entry |
| `go`, `rust`, `npm`, `opam`, `cc` | §03 | ecosystem sections (S2 §5.3, S3 §5.3, S4 §5.2, S5; `cc` holds `cc.External` entries, §03.7.6) |
| `nixpkgsFacts` | §01 | `{"<system>": {"<attr>": {"version"}}}` informational, for review diffs |

  `sources` entry: `{"url": "...", "kind": "file|archive", "hash": "<SRI of the downloaded
  file>", "narHash": "<SRI of the unpacked tree, archive only>", "name": "<store name>",
  "strip": <int components>}`.

- Example:

```json
{
  "lockVersion": 1,
  "nixpkgs": {
    "channel": "nixos-26.05",
    "narHash": "sha256-…",
    "rev": "cd4ea8d57de16f4810bc5f14207bac45fbf19f67",
    "url": "https://github.com/NixOS/nixpkgs/archive/cd4ea8d57de16f4810bc5f14207bac45fbf19f67.tar.gz"
  },
  "sources": {
    "//third_party/zlib:zlib": {
      "hash": "sha256-…",
      "kind": "archive",
      "name": "zlib-1.3.1-src",
      "narHash": "sha256-…",
      "strip": 1,
      "url": "https://zlib.net/zlib-1.3.1.tar.gz"
    }
  },
  "go": { "modules": [] }
}
```

- Every ecosystem section carries `"inputs": "<sha256 hex>"` = hash of the canonical JSON of that
  ecosystem's declared requirements (§03 R-FW-062); `nixpkgs` and `sources` carry their own
  `inputs`. `lock.File` (Go type in `pkg/engine/lock`):

```go
// File is bob.lock in memory. Sections are raw JSON owned by their ecosystem.
type File struct {
	LockVersion int
	Nixpkgs     NixpkgsPin
	Sources     map[string]Source
	Sections    map[string]json.RawMessage // "go", "rust", "npm", "opam", "cc"
}
func Read(path string) (*File, error)   // errors per R-FW-048
func (f *File) Write(path string) error // canonical, atomic, only if bytes change
```

- **Acceptance:** (a) `bob lock` twice → identical bytes. (b) Reformatting `bob.lock` by hand
  (other indent) → next `bob build` rewrites it canonically and prints `note: bob.lock was
  reformatted`. (c) JSON schema `pkg/engine/lock/lock.schema.json` validates every example lock.
- **Source:** R-FW-004, D2, D58 (crate data allowed), S2/S3/S4 lock proposals.

#### R-FW-005 Locking on demand
- **Statement:** Follows §03 R-FW-062: a section whose `inputs` differs from the current
  declarations is stale; `bob build`/`bob test` fail with the stale-lock error ("run bob lock")
  unless `--lock` is given; `bob lock`, `bob dev` and `--lock` re-resolve only the stale
  sections. Existing pins move only with `bob lock --update [<ecosystem>|nixpkgs|<name>]`; entries
  no declaration uses are removed by `bob lock`. Workers and `bob install` never re-resolve.
- **Acceptance:** adding `golang.Require("github.com/google/uuid","v1.6.0")` then `bob lock`
  changes only the `go` section (other sections byte-identical); `bob build` before `bob lock`
  exits with the stale-lock error and makes no network request.
- **Source:** R-FW-005, D26, R-FW-003, §03 R-FW-062.

#### R-FW-048 Unparseable lock
- **Statement:** A `bob.lock` that is not valid JSON (e.g. merge conflict markers) →
  BOB-E-1019 `✗ bob.lock cannot be read: <reason> (line <n>)` + hint `resolve the merge conflict, or run
  bob lock --rebuild to resolve everything again from bob.go`. `--rebuild` ignores the old file
  except `nixpkgs.rev`.
- **Acceptance:** fixture with `<<<<<<<` markers → exit 3 with that text; `bob lock --rebuild`
  succeeds and keeps the nixpkgs rev.
- **Source:** R-FW-004.

#### R-NIX-005 nixpkgs pin
- **Statement:** `nixpkgs.rev` comes from `https://channels.nixos.org/<channel>/git-revision`
  (verified for `nixos-26.05`, returns `cd4ea8d5…`), or from the `@rev` in `RepoOpts.Nixpkgs`.
  The source is fetched by bob's fetcher from `url` (GitHub archive tarball), unpacked with
  `strip=1`, and verified against `narHash`. A changed `rev` is the only way nixpkgs changes.
- **Acceptance:** fresh repo with `Nixpkgs: "nixos-26.05@cd4ea8d5…"`: `bob lock` writes the pinned
  entry; a tampered `narHash` → 3001 for `nixpkgs`.
- **Source:** R-NIX-002, A3, gen-b §4 (first fetch 16.5 s).

---

## 1.9 Platforms and variants (ADR 0009, model part)

```go
type Platform string
const (
	LinuxAMD64 Platform = "linux-amd64" // Nix system x86_64-linux
	LinuxARM64 Platform = "linux-arm64" // Nix system aarch64-linux
)
type Profile string
const ( Release Profile = "release"; Debug Profile = "debug" )
type Sanitizer string
const ( ASan Sanitizer = "asan"; UBSan Sanitizer = "ubsan" )

// Variant is the configuration of one build request. The zero value means release, no extras.
type Variant struct {
	Profile  Profile
	Sanitize []Sanitizer
	Race     bool                 // Go -race only
	Features map[Label][]string   // Rust features added on top of the crate's declared ones
	Named    []string             // named variants that produced this variant
}

// Named is a reusable variant (type Named; constructor NamedVariant, A66) referred to by name from the CLI and pipelines.
//   bob.NamedVariant("asan", bob.WithSanitize(bob.ASan, bob.UBSan), bob.WithProfile(bob.Debug))
func NamedVariant(name string, mods ...VariantMod) Named
type VariantMod func(*Variant)
func WithProfile(p Profile) VariantMod
func WithSanitize(s ...Sanitizer) VariantMod
func WithRace() VariantMod
func WithFeatures(crate Package, features ...string) VariantMod
```

Named variants may also be written as `func(*bob.Variant)`; such a function sets
`Variant.Features[bob.Label{Dir, Name}]` (a feature list per crate label) (R-FW-031, R-FW-038).

#### R-FW-030 Platform-generic packages, host builds
- **Statement:** Every package is defined once and built for any `Platform`. Derivations of
  host-side tools (bob-builder, generators) have the **host** Nix system; target libraries and
  outputs have the **target** `system` (consistent with R-FW-032); the target also changes
  toolchain and flags (§04 owns the toolchain table). Kinds may restrict platforms through their options
  (`Platforms []bob.Platform`); requesting another → `<label> does not support <platform>`.
- **Acceptance:** S7 matrix: `examples/polyglot` cross-builds aarch64→x86_64 and the binary runs
  under qemu-user with the expected output; every drv of a host-side tool has `system = "aarch64-linux"`, every drv of a target library or
  output has the target `system`.
- **Source:** R-FW-030, D3, ADR 0009 Decisions 1–2.

#### R-FW-050 Configured labels
- **Statement:** A configured label is `label "@" [platform] *("+" token)`, token ∈ {`release`,
  `debug`, `asan`, `ubsan`, `race`, named variant}. Canonical form: platform always printed;
  tokens ordered profile (omitted if `release`), `asan`, `ubsan`, `race`, named variants
  alphabetically. Unknown platform → `unknown platform "<x>" (known: linux-amd64,
  linux-arm64)`; unknown token → `unknown variant "<x>" (known: …)`. Variant semantics,
  projection and foreign-architecture tests are §04 (R-FW-031, R-FW-038, R-FW-039).
- **Acceptance:** `ParseConfigured` table test: `//api:api@+asan+debug` → canonical
  `//api:api@linux-arm64+debug+asan` on an arm64 host; `@linux-x86` → the unknown-platform text.
- **Source:** ADR 0009 Decision 5, S7 §5.2.

---

## 1.10 Derivation layer (backend B, ADR 0001)

#### R-NIX-001 Strategy B
- **Statement:** bob computes every derivation of its own packages in Go and imports the `.drv`
  files with `nix-store --import`; the only Nix expression bob evaluates is `resolve.nix`
  (§1.15). nixpkgs derivations are real `inputDrvs`.
- **Acceptance:** `grep -r --include='*.nix' -l . pkg/` lists only `pkg/engine/nixidx/resolve.nix`;
  `bob build -v` shows no `nix eval`/`nix-instantiate` except the index's.
- **Source:** R-NIX-001, D49, ADR 0001.

#### R-NIX-006 Exact derivation computation
- **Statement:** `pkg/engine/drv` implements, for Nix 2.34.9:
  - **ATerm:** `Derive([<outputs>],[<inputDrvs>],[<inputSrcs>],"<system>","<builder>",[<args>],[<env>])`;
    outputs `("<name>","<path>","<algo>","<hash>")` (input-addressed: algo and hash empty;
    fixed: `r:sha256`/`sha256` and lowercase hex); inputDrvs `("<drv>",["<out>",…])`. Strings
    escape `"`→`\"`, `\`→`\\`, LF→`\n`, CR→`\r`, TAB→`\t`. Sorted: outputs by name, inputDrvs by
    path, their output names, inputSrcs, env by key; args in order.
  - **makeStorePath(type, h, name)** = `/nix/store/` + nixbase32(xor-fold of
    sha256(`<type>:sha256:<hex(h)>:/nix/store:<name>`) to 20 bytes) + `-` + name.
  - **Output paths (input-addressed):** compute `hashModulo` with every output path and every
    env var named like an output set to `""`; output *o* → `makeStorePath("output:"+o, hashModulo,
    name + (o=="out" ? "" : "-"+o))`; then fill paths and env.
  - **hashDerivationModulo:** fixed-output: sha256(`fixed:out:<algo>:<hex>:<outPath>`);
    otherwise sha256 of the ATerm with each inputDrv path replaced by hex(hashModulo(input)).
    nixpkgs drvs are parsed (`drv.Parse`) recursively; their modulo hashes are cached per
    nixpkgs revision in the index (§1.15).
  - **drv path** = makeStorePath(`text` + concat(`:`+ref for sorted inputSrcs ∪ inputDrvs),
    sha256(ATerm), name + `.drv`).
  - **Fixed paths:** flat → makeStorePath(`output:out`, sha256(`fixed:out:<algo>:<hex>:`), name);
    recursive sha256 → makeStorePath(`source`, narHash, name).
  - Store names: `drv.Name(parts ...string) string` joins parts with `-`; inside a part `/` → `-`,
    `@` → `-`, any char outside `[A-Za-z0-9+._?=-]` → `-`; no leading `.`; > 200 bytes → cut to
    191 bytes + `-<hash8>` (first 8 nixbase32 chars of sha256 of the full name). bob's package
    names: `drv.Name(dir, name, ["host"], [platform if cross], [projection tag])` (§04 R-FW-038
    tag rules), e.g. `libs-foo-foo-linux-amd64-address`. This is the fallback for packages without
    an ecosystem name rule; the per-ecosystem names of §03 win for ecosystem packages (a local
    crate's version in names is `0.0.0`).
- **Acceptance:** (a) golden ATerm tests; (b) recomputing nixpkgs `hello` at the pin from its 187
  parsed input drvs gives the recorded output path; (c) cross-check suite: every drv of
  `examples/*` submitted with `nix derivation add` (Nix computes paths) yields the same drv and
  output paths; (d) `nix store add --mode flat --hash-algo sha512` and `--mode nar` give the
  same paths as `FixedOutputPath` (verified in Phase 2.3 against Nix 2.34.8 for both modes);
  (e) property test: changing any env value changes the output path.
- **Source:** ADR 0001, gen-b §1 (four-way cross-check).

#### R-NIX-007 Import and caching of drvs
- **Statement:** New drvs are imported in one `nix-store --import` stream (export format v1,
  topologically ordered, no signatures); already-valid drvs are skipped after one batched
  `nix-store --check-validity --print-invalid`. A drv cache `<home>/cache/drv/` maps
  sha256(canonical IR node + resolved input drv paths + generator version + bob-builder path) →
  drv path, so unchanged nodes are not recomputed.
- **Acceptance:** export-stream golden test against `nix-store --export` of the same drv; second
  `bob build` of `examples/polyglot` imports 0 drvs (`--json` event `drv.imported=0`).
- **Source:** gen-b §1/§4.

#### R-NIX-003 Standard store paths and caches
- **Statement:** All outputs are ordinary input-addressed store paths; sources and fetched files
  are ordinary content-addressed paths. They substitute from and upload to any standard binary
  cache.
- **Acceptance:** sign → `nix copy --to file://` → copy into a fresh store → `nix store verify
  --sigs` → run `//cmd/api:api` from there (gen-b round trip) in CI.
- **Source:** R-NIX-003, gen-b §1.

---

## 1.11 Sources and fetching (bob fetches; no user FODs)

#### R-NIX-008 No user-influenced fixed-output derivations
- **Statement:** bob never generates a fixed-output derivation for anything declared in `bob.go`
  or `bob.lock`. Repo sources and external downloads become content-addressed store paths that
  bob creates itself and references as `inputSrcs`; they equal the paths a FOD with the same
  name and hash would have (R-NIX-006 d), so "fixed-output input" in R-GO-002, R-NPM-002 and
  R-CC-005 is satisfied by path identity. Only nixpkgs' own FODs (trusted, from the pin) may run.
- **Acceptance:** for every drv generated from `examples/*`, no drv whose label is not
  `nixpkgs:` has `outputHash`; `bob build` of a package with an unreachable source URL succeeds
  when the path is already valid.
- **Source:** S13 §3.3/§5.4, S13-5, STATUS findings.

#### R-NIX-009 Repo source import
- **Statement:** For each package source set, bob serialises the filtered tree (R-FW-021) as a NAR
  in Go, computes `makeStorePath("source", sha256(NAR), name)`, checks validity in one batch, and
  imports only missing paths through one `nix-store --import` stream.
- **Acceptance:** for 50 random fixture trees, the path equals `nix store add --mode nar --name
  <name>` of the same filtered tree; importing 10,000 small source dirs takes ≤ 5 s [8c/15G]
  [proposal; measured 3–10 s with `nix-store --add` under load, gen-b §4].
- **Source:** gen-b §4, S13 §5.4.

#### R-NIX-014 Fetcher
- **Statement:** `engine/fetch.Fetcher` gets every external input (nixpkgs source, Go modules,
  crates, npm tarballs, opam archives, `sources` entries):

```go
type Request struct {
	URLs     []string // mirrors in order
	Kind     Kind     // File (flat) or Archive (unpacked tree)
	Hash     string   // SRI of the downloaded bytes (required for File; checked if set for Archive)
	NarHash  string   // SRI of the unpacked tree (required for Archive)
	Name     string   // store name
	Strip    int      // leading path components to drop (Archive)
	MaxBytes int64    // 0 = 2 GiB
}
type Fetcher interface {
	// Ensure makes the store path valid and returns it: already valid → substituters
	// (nix-store --realise of the path) → origin URLs. It never runs a Nix FOD.
	Ensure(ctx context.Context, r Request) (storePath string, err error)
	// Resolve downloads without a known NarHash (bob lock) and returns the hashes to record.
	Resolve(ctx context.Context, r Request) (hash, narHash string, err error)
}
```

  Rules: `https` (and `http` only with a pinned hash); 3 attempts with 1 s / 4 s backoff;
  connect timeout 10 s, idle timeout 60 s; size cap; `Hash` verified before unpacking; archives
  (`.tar`, `.tar.gz/.tgz`, `.tar.bz2`, `.tar.xz`, `.tar.zst`, `.zip`, `.crate`) are unpacked in Go
  with: paths cleaned and confined to the root (`..`, absolute, and paths through symlinks
  rejected), hard links turned into copies, device/fifo entries rejected, symlinks kept only if
  they resolve inside the tree, modes normalised to 0644/0755, ≤ 1,000,000 entries and ≤ 8 GiB
  unpacked; the tree's NAR hash must equal `NarHash`; import through `nix-store --import`
  (as R-NIX-009). Egress policy (private address ranges) is a `Policy` set by the caller:
  developers default to no restriction; workers use §05's policy.
- Errors: mismatch 3001 (prints URL, lock hash, got hash); download failure 3002; not found
  3003; unsafe archive → 3004 `<label>: archive <url> contains an unsafe entry <path> (<reason>)`.
- **Acceptance:** tests with a local HTTP server: tampered byte → 3001; 404 → 3003; tar with
  `../x`, absolute path, symlink escape, device node, 10⁶+1 entries → each refused; correct
  crate archive → path equals the S3 recursive FOD path for the same `narHash`.
- **Source:** S13 §5.4, R-CC-005, D44, R-SEC-004, S15 3001–3003.

---

## 1.12 `bob-builder` contract

#### R-NIX-015 Builder binary
- **Statement:** `bob-builder` is a static Go binary (`CGO_ENABLED=0 -trimpath -buildvcs=false
  -ldflags='-s -w -buildid='`), built from `cmd/bob-builder` (packages `pkg/builder`, `bspec`,
  `pkg/<eco>/bop`). It has its own version `bspec.BuilderVersion` (integer, starting at 1),
  bumped only when its behaviour changes. Its store path is the `source` path of a tree
  `bin/bob-builder`, named `bob-builder-<ver>`. `bob` embeds the host-arch binary and a table
  `builders.go` of `{system → store path}` for both systems; at first use bob imports the
  embedded binary and refuses to run if the resulting path differs from the table
  (BOB-E-9007 `embedded builder does not match its recorded hash`).
- **Acceptance:** building `cmd/bob-builder` twice (two machines of one arch, or two checkouts)
  gives identical bytes; a CI check fails when a builder source change does not bump
  `BuilderVersion`… and when the table is stale (`go generate ./pkg/engine/store` diff).
- **Source:** ADR 0001 Consequences, ADR 0005 Consequences (builder is an input of every drv).

#### R-NIX-016 Derivation shape and job format
- **Statement:** Every non-nixpkgs derivation uses **structured attributes**
  (`__structuredAttrs = true`; ATerm env = `__json` + one env var per output): `builder =
  <bob-builder>/bin/bob-builder`, `args = ["<kind>"]` (subcommand = job kind), and the JSON
  attributes `bobSpec` (the `bspec.Job`), `outputs`, `outputChecks.<out>.disallowedReferences`
  (fixup contract, §1.13) and, where a kind needs it (§04 OCI layers), `unsafeDiscardReferences.<out>
  = true`. The builder reads only `$NIX_ATTRS_JSON_FILE` and the variables Nix sets; there is no
  env-size limit on the spec. `__json` is canonical JSON (keys sorted, no whitespace).

```go
package bspec
const BuilderVersion = 1
// Job is one derivation's work. Kind selects the subcommand; Args is the kind's own schema.
type Job struct {
	V       int             `json:"v"`       // 1
	Kind    string          `json:"kind"`    // must equal argv[1]
	Label   string          `json:"label"`   // for log lines
	Outputs []string        `json:"outputs"` // "out", optional "dev", "lib", "debug"
	Args    json.RawMessage `json:"args"`
	Fixup   Fixup           `json:"fixup"`
}
type Fixup struct {
	Strip      bool     `json:"strip"`      // false for profile debug
	DebugOut   bool     `json:"debugOut"`   // move debug info to the "debug" output
	ShrinkRPath bool    `json:"shrinkRpath"`
	Forbidden  []string `json:"forbidden"`  // store paths that must not be referenced (also in outputChecks)
	Tools      Tools    `json:"tools"`      // store paths of strip, objcopy, patchelf
}
```

  Core kinds (in `pkg/builder`): `run` (bob.Run: `{steps:[{op,args}]}` with ops `mkdir`, `copy`,
  `symlink`, `write`, `exec {argv,env,cwd,path[]}`, `check-outputs`), `unpack {archive,to,strip}`,
  `copy-tree`, `test-run` (shared with §03). Ecosystem kinds are §03's table (§03.0.4) and §04's
  (`oci-layer`, `oci-image`, `appimage`, …); each registers through `pkg/<eco>/bop`. Unknown kind →
  exit 2. Every path a job writes is under an output or `/build`; others → exit 2.
- Environment for `exec`: exactly `PATH` (declared), `HOME=/homeless-shelter`,
  `TMPDIR=/build/tmp`, `NIX_BUILD_CORES`, `SOURCE_DATE_EPOCH=1`, `TZ=UTC`, `LC_ALL=C`, the outputs,
  plus the step's `env`.
- **Acceptance:** (a) ATerm golden for a structured-attrs drv equals Nix's: `nix derivation show`
  of a Nix-evaluated `derivation { __structuredAttrs = true; … }` and bob's drv with the same
  attributes give the same drv path (verified shape on Nix 2.34.8: env holds the output vars,
  attributes go to `structuredAttrs`); (b) unit tests per kind/op; (c) a fuzzed `Job` never
  escapes `/build`/outputs; (d) a 1 MiB job builds.
- **Source:** gen-b §2/§7, S3 §5.2, S15 §5.7, §04 R-OUT-011.

#### R-NIX-017 Builder logging and exit codes
- **Statement:** The builder writes plain log lines; structured diagnostics as
  `bob:: {"type":"diag","severity","file","line","col","msg"}` lines; phase changes as
  `@nix {"action":"setPhase","phase":"<op>"}`. It never relies on `start`/`result`/`stop`
  messages and never uses `/bin/sh` (the embedded sandbox shell has no applets). Exit codes:
  0 ok; 1 a step failed (user/build error; the last line names the step and argv[0]); 2 invalid
  job (bob bug → 9004); 3 fixup violation (→ 9003); 4 a declared output missing (→ 9002 or the
  `bob.Run` text).
- **Acceptance:** S15 fault rows 28 (builder missing → 9001) and the 2xxx rows pass with the
  real builder; a step that prints `@nix {"action":"msg","level":0,…}` does not change the
  result of a successful build (warning only).
- **Source:** S15 §4/§5.7, STATUS findings.

#### R-NIX-018 Success is structural
- **Statement:** A build succeeded iff `nix build` exits 0 **and** `nix-store --check-validity
  --print-invalid` reports every requested output valid. Log content never decides success.
- **Acceptance:** S15 full-disk fault (partial output file) is reported as failure 6001 even if the
  builder printed success; injected fake error messages in a successful build → exit 0 with a
  warning.
- **Source:** S15 §4, STATUS findings.

---

## 1.13 Fixup and closure budgets

#### R-NIX-019 Fixup contract
- **Statement:** After the last step the builder runs fixup on every output unless the kind
  disables it:
  1. **Prevent** (done by generators, §03): `-ffile-prefix-map=/build=.`, Rust
     `--remap-path-prefix=<src>=<label dir>`, Go `-trimpath`, so sources are not referenced.
  2. **Strip** ELF files under `bin/ libexec/ lib/` with nixpkgs binutils `strip` (an input of
     the drv): executables `--strip-all`, shared and static libs `--strip-debug`. Profile
     `debug`: no stripping; with output `debug` declared, debug info moves there
     (`objcopy --only-keep-debug` + `--add-gnu-debuglink`).
  3. **RUNPATH:** `patchelf --shrink-rpath` and drop entries outside `/nix/store` (e.g. `/build`).
  4. **Reference check:** scan outputs for store hashes (Nix's algorithm); a reference to a path
     in the kind's forbidden set → exit 3 → 9003 `<label> refers to <ref>, which it must not
     reference`. Forbidden for runtime outputs of all kinds: compilers and wrappers (gcc
     unwrapped, cc-wrapper, binutils, rustc, go, ocaml), any bob `-src` path, crate/module
     source paths, bob-builder. The same set goes into `outputChecks.<out>.disallowedReferences`,
     so Nix enforces it too (§02 maps Nix's message to 9003).
- **Acceptance:** `examples/polyglot` native `//cmd/api:api`: closure contains no `gcc-15.3.0`
  (only `gcc-15.3.0-lib`), no `-src` path, binary has no `.debug_info`; a fixture C program
  embedding `__FILE__` of a source path builds without a reference to it.
- **Source:** gen-b §7, S7 risks (366 MB closure via gcc), S8 §3.1, S5 (693 → 55 MB).

#### R-NIX-030 Closure budgets [proposal]
| Package (examples/polyglot) | Measured | Budget |
|---|---|---|
| native arm64 `//cmd/api:api` closure (NAR) | 75.9 MiB without fixup (S8 §3.1, includes itoa-src, unstripped 7.7 MiB binary) → 73.4 MiB after fixup (strip −2.5 MiB, itoa-src dropped; S8 §3.1 numbers); native with cross-checked gcc ref: 366 MB (S7) | **≤ 74 MiB**, 12 paths max [8c/15G]; §04 R-OUT-025 uses the same value |
| `api` binary, release, stripped | 5.2 MiB (S8) | ≤ 5.5 MiB |
| cross `//cmd/api:api@linux-amd64`, target libs native (two glibc) | 113.8 MB (S7) | ≤ 112 MiB |
| cross, target libs `Cross()` | 76.1 MB (S7) | ≤ 74 MiB |

- **Acceptance:** `bobtest/closure_test.go` measures `nix path-info -S` for each row on both
  architectures' native builds and fails above budget.
- **Source:** S7 §3.2, S8 §3.1. Bundle-level pruning (glibc locales, gconv) is §04.

---

## 1.14 Embedded rootless Nix (ADR 0002)

#### R-NIX-020 Embedded static Nix
- **Statement:** `bob` embeds nixpkgs `nixStatic` 2.34.9 (pin) for its own architecture, stripped,
  gzip-9 (13.8 MB), and extracts it on first use to `<home>/bin/nix-<first 12 hex of sha256>/bin/nix`
  (temp dir + atomic rename; observable as a temporary entry that is renamed to the final
  `nix-<12 hex>` name) with symlinks `nix-store`, `nix-build`, `nix-instantiate`,
  `nix-env`, `nix-hash`, `nix-daemon` → `nix`. Later runs only stat. The host's `nix` is never
  executed. `ATTRIBUTION.md` lists Nix (LGPL-2.1-or-later), its static dependencies and busybox
  (GPL-2.0) with source locations (D52).
- **Acceptance:** under `env -i PATH=/usr/bin:/bin` as a user without Nix, `bob build` of
  `examples/cpp-lib` works; first extraction ≤ 500 ms [8c/15G] (measured 294 ms); `strace -f -e
  execve` shows no exec of a `nix` outside `<home>/bin`.
- **Source:** R-NIX-020/023, D50, D52, ADR 0002 Decisions 2–3.

#### R-NIX-021 Namespace mechanism
- **Statement:** As ADR 0002 Decision 1: re-exec of `/proc/self/exe` with
  `CLONE_NEWUSER|CLONE_NEWNS`, uid/gid map `0 <uid> 1`, `setgroups deny`, `/` made `rprivate`;
  host has `/nix` → bind `<home>/nix` on `/nix`; otherwise tmpfs root mirroring top-level
  entries + `nix/`, `pivot_root`, old root detached. Every Nix process and every store binary
  bob runs (builds, `bob exec`, `bob dev`, shims) runs inside. One namespace entry per bob
  command; Nix subprocesses inherit it.
- **Acceptance:** S0b probes F1, F3 (network blocked, `/home` hidden, loopback present) pass in
  bind and pivot mode as non-root; 4 parallel `bob build` on one state finish without lock
  errors; the `namespace` stage event carries `durationMs`; namespace entry ≤ 10 ms [8c/15G] and [2c/4G] (measured 5–7 ms).
- **Source:** R-NIX-021, D11, ADR 0002 Decision 1, rootless-results.

#### R-NIX-022 Refusal with the exact fix
- **Statement:** Detection order: sysctl pre-check (hint only) → clone error class (ENOSPC, EPERM,
  EACCES) → first mount failing inside the namespace (child exit 125). Output (BOB-E-7005, exit 7):

```
✗ bob cannot create its private /nix: unprivileged user namespaces are blocked on this system
  cause: <one of the lines below>
  fix (run once as an administrator):
    <fix text>
```

| Cause (detected) | Fix text |
|---|---|
| AppArmor restricts user namespaces (Ubuntu ≥ 24.04; mount EPERM, or sysctl `kernel.apparmor_restrict_unprivileged_userns=1` + clone/mount failure) | the AppArmor profile from rootless-results.md with bob's real path (`readlink /proc/self/exe`), then `sudo apparmor_parser -r /etc/apparmor.d/bob`; alternative: the `60-bob-userns.conf` sysctl line |
| `user.max_user_namespaces = 0` (ENOSPC; RHEL STIG) | `echo 'user.max_user_namespaces = 15000' \| sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system` |
| `kernel.unprivileged_userns_clone = 0` (Debian, linux-hardened) | `echo 'kernel.unprivileged_userns_clone = 1' \| sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system` |
| NixOS with `security.allowUserNamespaces = false` (`/etc/NIXOS` exists) | `set security.allowUserNamespaces = true; in configuration.nix and run nixos-rebuild switch` |
| other EPERM/EACCES (container seccomp, SELinux) | `bob runs only where unprivileged user namespaces are allowed; inside containers, allow them (e.g. podman --security-opt seccomp=unconfined, or docker --security-opt seccomp=unconfined)` |

  There is no fallback mode.
- **Acceptance:** simulated cases (`max_user_namespaces=0` in a throwaway namespace; ENOSPC)
  print the matching fix (S0b-verified); the AppArmor case is verified on a real Ubuntu 24.04
  host (manual, Gate S open risk).
- **Source:** R-NIX-022, D11, ADR 0002 Decision 4.

#### R-NIX-024 bob owns its Nix configuration
- **Statement:** State home `<home>` = `$BOB_HOME`, else `${XDG_DATA_HOME:-$HOME/.local/share}/bob`:

```
<home>/nix/                     mounted at /nix (store + var/nix/db)
<home>/bin/nix-<h>/bin/         extracted Nix (R-NIX-020)
<home>/etc/nix/nix.conf         generated, mode 0644
<home>/etc/nix/netrc            generated, mode 0600 (cache tokens)
<home>/etc/nix/user.conf        empty (NIX_USER_CONF_FILES)
<home>/xdg/{config,cache,data,state}/   XDG_* for every Nix process
<home>/cache/{go-build,drv,nixpkgs,eval}/
<home>/lock                     flock for state creation and nix.conf rewrites
```

  Nix processes get exactly: `NIX_CONF_DIR=<home>/etc/nix`, `NIX_USER_CONF_FILES=<home>/etc/nix/user.conf`,
  the four `XDG_*` above, `HOME`, `PATH=<home>/bin/nix-<h>/bin`, `TMPDIR`, `TERM` (if set);
  `NIX_PATH` and every other `NIX_*` variable are removed. Generated `nix.conf` (dev profile):

```
# Generated by bob. DO NOT EDIT.
build-users-group =
sandbox = true
sandbox-fallback = false
require-drop-supplementary-groups = false
experimental-features = nix-command
substituters = https://cache.nixos.org <extra substituters from bob config, in order>
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY= <extra keys>
netrc-file = <home>/etc/nix/netrc
narinfo-cache-negative-ttl = 60
fallback = true
connect-timeout = 5
max-jobs = auto
cores = 0
eval-cache = false
allow-import-from-derivation = false
auto-optimise-store = false
warn-dirty = false
```

  (Defaults checked on Nix 2.34.8: `sandbox-fallback=true`, `eval-cache=true`,
  `allow-import-from-derivation=true`, `narinfo-cache-negative-ttl=3600`,
  `require-drop-supplementary-groups=true` — so each line above changes behaviour.)
  `discard-references` is **not** listed: Nix 2.34.8 and 2.34.9 warn `unknown experimental
  feature 'discard-references'` (the feature is stable; `unsafeDiscardReferences` needs no flag —
  verified at integration on both versions with `experimental-features = nix-command`: the
  output's references are empty with it and present without it). §04's request is met by the
  derivation layer alone. Remote-build
  and release fetches pass `--option narinfo-cache-negative-ttl 0` (S12). bob validates cache
  credentials itself (§02 R-NIX-029, `AuthService/WhoAmI` → 4008/4010) before a build because Nix reports a 401 only as a level-1
  warning. §05 derives the worker profile from the same `store.Config`.
- **Acceptance:** `bob doctor --print-nix-env` shows exactly the variables above; a user
  `~/.config/nix/nix.conf` with `substituters = http://bogus` and `NIX_PATH` set have no effect;
  a system Nix's store, db and narinfo cache are unchanged after a bob build (R-NIX-025).
- **Source:** R-NIX-024/025, ADR 0002 Decision 1 + Consequences, S12, S15, STATUS findings.

#### R-NIX-025 Coexistence with a system Nix
- **Statement:** On a host with `/nix`, bob's namespace hides it (bind mount over `/nix`); bob
  never reads `/etc/nix`, the system daemon socket, or `~/.config/nix`.
- **Acceptance:** with a system Nix daemon running, `bob build` works and `nix-store -q
  --references` on the host store shows no bob paths.
- **Source:** R-NIX-025.

#### R-NIX-026 Single-user store, concurrency
- **Statement:** bob uses Nix in single-user (local store) mode, no daemon. Concurrent bob
  commands share the store through Nix's own locks; bob's own caches use `<home>/lock` for
  writes and atomic renames. SQLite busy errors are retried 3× with 200 ms backoff before 7004.
- **Acceptance:** 4 concurrent `bob build //...` on `examples/polyglot` all succeed; 8 concurrent
  `bob lock` produce one consistent `bob.lock`.
- **Source:** gen-b §9, rootless-results (4 parallel processes), S15 7004.

---

## 1.15 nixpkgs lookup and index

#### R-NIX-011 Pre-checks in Go
- **Statement:** Before any derivation is built, bob checks with index data: attribute exists
  (1001, with up to 3 suggestions by edit distance over the top-level names list, ≤ 2 edits or
  same prefix), broken (1002), insecure without `AllowInsecure` (1003), unfree without
  `AllowUnfree` (1004), unavailable on the system (1005), dependency cycle (1006, Go topo sort),
  unknown dependency (1007), toolchain version constraints (1008, §03 provides the rules), missing
  lock hash (1009), unbuildable system (1010).
- **Acceptance:** gen-b fault catalogue rows (unknown `opensll` → suggestion includes `openssl`;
  `aranym` broken; `bzip2_1_1` insecure; cycle; Go version) each print their template with
  `definedAt` and exit 3 without starting `nix build`.
- **Source:** R-NIX-011, gen-b §3, S15 §5.2.

#### R-NIX-027 `resolve.nix` and the index
- **Statement:** `pkg/engine/nixidx/resolve.nix` (embedded, ≤ 60 lines, no build logic) takes
  `{src, system, attrs, names, allowUnfree, permittedInsecure}` and returns, per attribute, data
  only: `missing`, `broken`, `insecure`, `knownVulnerabilities`, `unfree`, `available`, `version`,
  `drvPath`, `outputs`, `evalRefused` (gen-b version, extended with the two allow lists). It is
  run as `nix eval --json --file resolve.nix --arg … ` inside the namespace. Index cache
  `<home>/cache/nixpkgs/<rev>-<system>.json` holds results per attribute, the names list (first
  use), and modulo hashes of parsed nixpkgs drvs; it is keyed by (rev, system, index version) and
  re-queried only for new attributes or when a cached `drvPath` is no longer valid. A full
  `nix-env -qa` index is never built (56 s, 6.4 GB).
- If the batch evaluation itself fails (an `abort` that `tryEval` cannot catch), bob retries per
  attribute to isolate the failing one and reports `nixpkgs package "<attr>" cannot be
  evaluated on <system>` (BOB-E-1017, definition class) with the raw text under `-v`.
- **Acceptance:** cold index for the polyglot fixture ≤ 2 s, warm ≤ 50 ms [8c/15G] (measured 1.55 s
  / 0.02 s); a fixture attribute that aborts evaluation produces the per-attribute error, not
  9999.
- **Source:** gen-b §1/§4, ADR 0001 Consequences, R-NIX-010.

**R-NIX-010** (no raw Nix evaluation error) is specified once, in §02 (§02.3, error taxonomy):
`resolve.nix` is the only Nix evaluation bob performs, and any unclassified error from it is a
9999 bug with a regression test.

#### R-NIX-002 nixpkgs stays usable
- **Statement:** Any attribute that evaluates to a derivation at the pin can be used through
  `nixpkgs.Tool/Lib/Pkg`; uncached ones are built by Nix from the nixpkgs recipe (upstream
  failures → 5001).
- **Acceptance:** `examples/polyglot` built against nixos-25.11 (`b6018f87`) after lowering the
  Go version: no builder code changes; only real version constraints fail, with 1008/2006.
- **Source:** R-NIX-002, gen-b §5.

---

## 1.16 Build pipeline and generic interfaces

#### R-FW-049 `engine/build.Run` order
- **Statement:** One `bob build` runs: enter namespace → scan + rules (R-FW-002) → evaluator
  (cached) → IR validation → lock check/update → index pre-checks → source import → `Plan` per
  ecosystem → fetch (`Ensure` all `PlanResult.Fetches`, in parallel ≤ 8) → drv import → `nix build --no-link
  --print-out-paths --log-format internal-json -L --keep-going <drv>^<outputs>…` → validity
  check → translation (§02). Each stage emits a `--json` event (§02 schema) with its duration.
- **Acceptance:** `--json` event sequence for `examples/polyglot` cold build matches the stage list.
- **Source:** gen-b §1, S15 §5.1.

#### R-FW-009 Works without a server
```go
package engine
// Substituter is a binary cache bob passes to Nix (URL, public key, optional netrc credential).
type Substituter struct{ URL, PublicKey, NetrcMachine, NetrcPassword string }
// RemoteExecutor runs a build elsewhere and makes the outputs available locally (bob build --remote).
type RemoteExecutor interface {
	Build(ctx context.Context, req RemoteRequest) (<-chan Event, error)
}
```
- **Statement:** The framework has no other server concept; with no `Substituter` beyond
  cache.nixos.org and no `RemoteExecutor`, every command works.
- **Acceptance:** all example builds pass with network access to cache.nixos.org and the
  upstream registries only.
- **Source:** R-FW-009, D37.

#### R-FW-020 Fine-grained packages (core rule)
- **Statement:** The core imposes no per-derivation overhead beyond one bob-builder start
  (static binary, no shell setup); granularity per ecosystem is §03's.
- **Acceptance:** synthetic N=100 chain of trivial `bob.Run` steps builds cold in ≤ 12 s at both
  profiles [proposal; B measured 10.0 s / 9.4 s with bash builders].
- **Source:** R-FW-020, ADR 0001 (0.1 s/drv).

#### R-FW-022 Cross-repo use
- **Statement:** Another bob repo is a Go module required in `go.mod` through bob.go
  (`bob.Import("example.com/other", "v1.2.0")`, recorded in the generated `go.mod`); its
  `bob.go` packages are imported as Go packages; labels are `example.com/other//dir:name`. Its
  `bob.lock` is not used: the consumer's `bob lock` resolves all declarations of the graph.
- **Acceptance:** `examples/cross-repo` (module served from a `file://` GOPROXY) builds the
  dependency's package by typed handle; gopls completion on `bar.` offers `Lib`.
- **Source:** R-FW-022, S1 §3.5.

---

## 1.17 Budgets (R-NFR-001/002, core part) [proposal]

| Measurement | Measured (quiet) | Budget [8c/15G] | Budget [2c/4G] |
|---|---|---|---|
| Evaluation, 1,000 `bob.go`, no change (eval key hit) | 0.47 s with `go build` no-op (S1 quiet) | ≤ 0.1 s | ≤ 0.15 s |
| Evaluation, 1,000 `bob.go`, one body change | 0.93 s (S1 quiet) | ≤ 1.5 s | ≤ 3 s |
| Evaluation, 1,000 wide, cold (empty GOCACHE) | 10.8 s / 23.9 s (S1, quiet run) | ≤ 15 s | ≤ 30 s |
| graph → drvs, N=1,000 new / unchanged | 3.03 / 1.11 s; 2.96 / 1.17 s (b-quiet) | ≤ 3.5 s / ≤ 1.2 s | ≤ 3.5 s / ≤ 1.3 s |
| graph → drvs, N=10,000 new / unchanged | 24.9 / 7.7 s; 26.2 / 8.7 s | ≤ 30 s / ≤ 9 s | ≤ 30 s / ≤ 10 s |
| peak RSS, N=10,000 | 175 MB / 217 MB | ≤ 300 MB | ≤ 300 MB |
| `examples/polyglot` no-op `bob build` (all stages) | 0.33 s / 0.45 s (backend only) + eval no-op 0.09 s | ≤ 0.6 s | ≤ 0.8 s |
| `examples/polyglot` cold (toolchains present) | 11.8 s / 22.0 s (B, bash builders) | ≤ 13 s | ≤ 25 s |
| namespace entry | 5–7 ms | ≤ 10 ms | ≤ 10 ms |

The "unchanged" graph→drvs budgets are expected to drop sharply with the drv cache (R-NIX-007);
Stage 3 records the new numbers and may tighten them. Incremental budgets per ecosystem are §03.

---

## 1.18 ADR status changes (this section)

- ADR 0003 → **Accepted (locked at Gate S)**; owner amendments: D59 (cd + editor must work → gopls
  shim and settings carry the tag), A14 (next-to-code accepted), rules extended to R6/R7.
- ADR 0009 → **Accepted (locked at Gate S)** for the model part (§04 updates the cross part);
  amendments A18 (foreign-arch tests), A19 (target libraries default).

## Open points

1. **[open]** Corporate registries on private addresses for developer fetches: recommended no
   egress restriction on developer machines (they already reach their network); workers use §05.
2. **[open]** `git` sources for `cc.External`: no git fetcher in v1; recommended: use forge
   archive URLs of a commit (`/archive/<sha>.tar.gz`) as `Archive` sources.
3. **[open]** Import of sources and fetched trees via `nix-store --import` (fast, one process)
   versus `nix store add` per path (slower, Nix computes the CA): recommended `--import` with the
   path-equality oracle test (R-NIX-009) and `bob doctor` verification.
4. **[open]** Determinism check cost on workers: recommended a single evaluation on workers and
   `bob check --determinism` in pipelines' check stage only.
5. **[open]** `abi <abi/4.0>` line of the AppArmor profile is unverified (ADR 0002 risk 1).

## Escalations

None. Coordinator notes from drafting, resolved at integration (spec/00-integration-log.md):
R-GO-002/R-NPM-002/R-CC-005 wording now says path identity (R-NIX-008); §03's `fetch-crate`
uses the Fetcher; the `api` closure budget is ≤ 74 MiB in both §01 and §04; §04 now describes
the fixup as prevention + BOB-E-9003; §03's error names map to §02 codes (§02.3).

# §02 — CLI, errors and developer experience


Scope: the `bob` command line (commands, flags, output, exit codes), the error taxonomy and its
message templates, `bob dev`, `bob env`, the shell hook, `.bob/bin` shims, the LSP
path-rewriting proxy, editor settings, `bob install`, and `--remote`.
ADR: 0017 (Accepted, locked). Builds on ADR 0002 Decision 5 (locked), D51, D59.

## Interfaces

Provided by §02:
- `diag.Error`, `diag.Code`, `diag.Class`, and the code table (§02.3). Every section raises
  errors through them.
- The `--json` event schema v1 (§02.4). §06 reuses `package.*`/`error` for remote build events.
- `.bob/env.json` (§02.7.2), the shim templates (§02.7.3) and `bob exec [--lsp]` (§02.7.4).
- The editor setting templates (§02.7.6).
- The install profile layout and `bob install` (§02.8).

Expected from other sections:
- **§01:** `bob.FindRoot(dir) (root string, err error)` (the repo root = the nearest ancestor
  that contains `bob.lock`); the evaluator with its no-op key (`EvalKey`); `DevShell` (name,
  toolchain packages, extra packages, env vars, hooks, dev servers); labels and label patterns;
  `definedAt` positions; the translator `Context` inputs (drv → package map); the namespace entry
  (`ns.Enter`), the state dir layout (`$XDG_DATA_HOME/bob`, default `~/.local/share/bob`), the
  bob-owned `nix.conf`/netrc writer; the R-NIX-022 fix text; and the pre-checks that raise
  1xxx codes.
- **§03:** per-ecosystem working-tree preparation (`Prepare(ctx, shell) error`: native files,
  materialised codegen, `node_modules`); the list of extra in-tree tools (e.g.
  `node_modules/.bin/*`); the dev server contract (Vite wrapper config, A20); the toolchain
  diagnostic patterns for 2xxx codes.
- **§04:** `--platform` / `--variant` value sets; release artifact kinds; the entry binaries of a
  closure release.
- **§05:** `BuildRecord` / `JobRecord` state names for `bob build --remote` progress; the live
  log pull (D55).
- **§06:** OAuth endpoints `/oauth/authorize`, `/oauth/token` (code + PKCE, refresh),
  `/oauth/device/code`, `/oauth/revoke` (§6.3.2); RPCs `AuthService/{WhoAmI, GetServerInfo}`,
  `BuildService/{CreateBuild, WatchBuild, CancelBuild}`, `LogService/{GetLog, FollowLog}`,
  `ReleaseService/{ResolveRelease, GetRelease}`; the source upload `PUT /upload/source/sha256/<hex>`
  (§6.9.7); the `/nix/*` cache through the download proxy (D61), the per-path visibility
  documents (D58, §6.6.2). (Names aligned at integration.)

---

## 02.1 Command line grammar

```
bob [global flags] <command> [subcommand] [flags] [args] [-- passthrough args]
```

- Flags may appear anywhere before `--`. Everything after `--` goes unchanged to the program
  (`run`, `test`, `exec`, `dev`).
- Long flags are `--name`, `--name=value` or `--name value`. The only short flags are `-v`,
  `-j`, `-C`, `-f` and `-q`.
- Unknown command or flag → `BOB-E-0001`, exit 2. A close match is suggested
  (Levenshtein ≤ 2).
- Implementation: the standard library only, with bob's own small parser **[proposal]**.

### Global flags

| Flag | Meaning |
|---|---|
| `-C <dir>` | run as if started in `<dir>` |
| `-v` | add raw detail (`nix:` lines, raw build log excerpts) |
| `-vv` | also bob's debug log on stderr |
| `-q` | errors and the summary only |
| `--json` | NDJSON events on stdout (§02.4); human output is suppressed |
| `--json-log` | with `--json`: also emit `log` events with build log lines |
| `--color=auto\|always\|never` | default `auto` (TTY rules in §02.5) |
| `-j <n>` | parallel local builds (Nix `max-jobs`); default = number of CPUs |
| `--offline` | no network: substituters off, no registry revalidation |
| `--server <url>` | pick a logged-in server when there are several (default: the one marked default) |

### Commands

| Command | Synopsis | Requirement |
|---|---|---|
| `build` | `bob build [patterns…] [--remote] [--platform P] [--variant V] [--print-paths] [--keep-going=true] [--lock]` | R-CLI-001, R-DEV-005 |
| `test` | `bob test [patterns…] [--remote] [--rerun] [--lock] [--require-all-tests] [-- test args]` (`--require-all-tests`: a test that cannot run on this machine, §04 R-PLAT, fails with exit 1 instead of `skipped`) | R-CLI-001 |
| `run` | `bob run <label> [--platform native] [-- args]` | R-CLI-001 |
| `dev` | `bob dev [--shell <name>] [--no-servers] [--servers-only] [-- cmd args]` | R-DEV-001/002 |
| `env` | `bob env [--shell <name>] [--format sh\|fish\|json] [--hook=bash\|zsh\|fish]` | R-DEV-003 |
| `hook` | `bob hook bash\|zsh\|fish` | R-DEV-003 |
| `allow` / `deny` | `bob allow [dir]`, `bob deny [dir]` | R-DEV-009 |
| `exec` | `bob exec [--lsp] [--project <root>] [--shell <name>] -- <tool> [args]` | R-DEV-008 |
| `editor` | `bob editor vscode\|nvim\|helix\|zed\|jetbrains [--write]` | R-DEV-011 |
| `check` | `bob check [--fix]` | R-CLI-001, D2 |
| `gen` | `bob gen [patterns…] [--check] [--force]`: writes native tooling files and materialised codegen (§03.0.2, §03.6.6) | R-FW-006/007 |
| `status` | `bob status [--json]`: per generated root `fresh\|stale\|missing\|edited\|orphan` (§03.6.6) and lock freshness | R-FW-007 |
| `lock` | `bob lock [--update [names…]] [--nixpkgs <rev>] [--offline]` | R-CLI-001 |
| `graph` | `bob graph [patterns…] [--why <from> <to>] [--rdeps] [--depth N] [--format text\|dot\|json]` | R-CLI-001 |
| `log` | `bob log <label> [--build <id>] [-f] [--attempt N]` | R-CLI-001, R-LOG-003 |
| `cancel` | `bob cancel <build-id>` | R-DEV-005 |
| `pipeline run` | `bob pipeline run <name> [--remote]` | R-FW-041 |
| `login` / `logout` | `bob login [<url>] [--device] [--token-stdin]`, `bob logout [<url>]` | R-CLI-003 |
| `install` | `bob install <ref> [--as <name>] [--bin-dir <dir>]` | R-INST-001 |
| `uninstall` / `upgrade` / `rollback` / `list` | see §02.8 | R-INST-001 |
| `registry` | `bob registry add <name> <url> \| remove <name> \| list \| default <name>` | R-INST-004 |
| `completion` | `bob completion bash\|zsh\|fish` | R-CLI-004 |
| `gc` | `bob gc [--dry-run]`: collects garbage in bob's store (keeps install generations, the dev shells of projects used in the last 30 days, `.bob` roots) | hint target of 6001 |
| `doctor` | `bob doctor [--repair]`: namespace check, store verify, config check | hint target of 7003 |
| `version` | `bob version [--json]`: bob version, embedded Nix version, nixpkgs default pin | — |

Label patterns: §01 (`//...`, `//libs/...`, `//libs/foo:all`). With no pattern, `build`/`test`
use `//...` below the current directory.

#### R-CLI-001 Command set
- **Statement:** `bob` provides every command in the table above with the given synopsis.
  `bob help <command>` and `bob <command> --help` print the synopsis and the flags.
- **Acceptance:** Given the harness command list, when it runs `bob <cmd> --help` for each one,
  then each exits 0 and prints a `usage: bob <cmd>` line that matches the table. When it runs
  `bob frobnicate`, bob exits 2 with `BOB-E-0001` and `did you mean …?` when a command is within
  edit distance 2.
- **Source:** R-CLI-001, D9, D10, ADR 0017.

#### R-CLI-005 Commands work without a server
- **Statement:** Every command except `login`, `logout`, `--remote`, `cancel`, `install`
  (registry), `upgrade`, and `log --build` works with no server configured.
- **Acceptance:** Given a fresh HOME with no `credentials.json`, `bob build //...`,
  `bob test //...`, `bob dev -- true`, `bob check` and `bob graph` on the fixture exit 0. Given the
  same HOME, `bob build --remote` exits 4 with `BOB-E-4009` (`no bob server configured`).
- **Source:** R-FW-009, D37.

---

## 02.2 Exit codes

| Exit | Meaning | Classes |
|---|---|---|
| 0 | success (warnings possible) | — |
| 1 | the user's code failed to build or test | build (2xxx) |
| 2 | command-line usage error | usage (0xxx) |
| 3 | definition error in bob.go / bob.lock (before building) | definition (1xxx) |
| 4 | download, cache, server or registry failure | fetch (3xxx), cache/server (4xxx) |
| 5 | a nixpkgs package failed to build from source | upstream (5xxx) |
| 6 | machine resources or limits (disk, memory, killed) | resource (6xxx) |
| 7 | bob's local store or host setup is damaged, busy or inaccessible | store/host (7xxx) |
| 70 | bob bug (internal) | internal (9xxx) |
| 130 | cancelled (SIGINT/SIGTERM) | interrupt (8xxx) |

- With several errors, the highest priority wins: interrupt > internal > store > resource > cache
  > fetch > upstream > definition > build > usage.
- `bob run` and `bob exec`: once the program has started, bob exits with the program's own exit
  status (signal N → 128+N). bob's codes apply only to failures before the start.

#### R-CLI-006 Exit codes
- **Statement:** bob's exit status is a function of the error classes raised, as in the table and
  priority order above.
- **Acceptance:** For every fault in the fault catalogue (HARNESS, from S15 `faults/expect.json`
  plus the §02 additions), the harness checks the exit status against the expected value. Given
  `bob run //tools:exit3` (a program that exits 3), bob exits 3. Given a program killed by
  SIGKILL, bob exits 137.
- **Source:** S15 §5.3, ADR 0017.

---

## 02.3 Error taxonomy

Format `BOB-E-NNNN`. The first digit is the class. Codes are never reused or renumbered.
`{x}` is an argument. The template is the error's first line; the hint is the default hint.
1001–9999 come from S15 §5.2 (`spikes/s15-errors/translate/codes.go`). Codes marked **new** are
added by §02.

| Code | Class | Message template | Default hint |
|---|---|---|---|
| 0001 | usage | `unknown {kind} "{name}"` | did you mean {suggest}? / run bob help |
| 0002 | usage | `not inside a bob repository (no bob.lock in {dir} or any parent)` | run bob from your project, or use -C <dir> |
| 0003 | usage | `pattern {pattern} matches no package` | run bob graph to list packages |
| 0004 | usage | `{flag} needs {what}` | — |
| 0005 | usage | `{label} is not runnable` | bob run needs a binary package |
| 0006 **new** | usage | `{label} does not support {platform} (it allows: {platforms})` | pick a supported platform, or build without --platform |
| 0007 **new** | usage | `variants "{a}" and "{b}" conflict: {reason}` | drop one of them |
| 0008 **new** | usage | `unsupported host architecture "{arch}" (bob runs on linux-amd64 and linux-arm64)` | — |
| 0009 **new** | usage | `{ref} has nothing to install (it contains: {kinds}); download its artifacts from {url}` | — |
| 1001 | definition | `nixpkgs has no package "{attr}"` | did you mean {suggest}? |
| 1002 | definition | `nixpkgs package "{attr}" ({version}) is marked broken` | pin a nixpkgs revision where it builds, or use another package |
| 1003 | definition | `nixpkgs package "{attr}" ({version}) is marked insecure: {cves}` | use a fixed version; bob refuses known-vulnerable packages unless allowed per package |
| 1004 | definition | `nixpkgs package "{attr}" has an unfree licence` | allow it explicitly for this package if your licence permits |
| 1005 | definition | `nixpkgs package "{attr}" is not available for {system}` | — |
| 1006 | definition | `dependency cycle: {cycle}` | a package cannot depend on itself; move the shared code into its own package |
| 1007 | definition | `{label} depends on unknown package {dep}` | — |
| 1008 | definition | `{label} needs Go {want} but nixpkgs {rev} provides Go {have}` | lower GoVersion or pin a newer nixpkgs |
| 1009 | definition | `{label} has no pinned hash in bob.lock` | run bob lock |
| 1010 | definition | `{label} targets {system}, which this machine cannot build` | build it remotely (bob build --remote) or for this machine's platform |
| 1011 **new** | definition | `generated file {file} was edited by hand` | move the change into bob.go; bob check --fix regenerates the file |
| 1012 **new** | definition | `bob.lock is out of date: {what}` | run bob lock |
| 1013 **new** | definition | `{path} is not ignored by git` | bob check --fix adds it to bob's block in .git/info/exclude (§03.0.2; bob never edits a user's .gitignore) |
| 1014 **new** | definition | `no dev shell "{name}" (have: {names})` | — |
| 1015 **new** | definition | `cannot resolve {ecosystem} dependencies for {root}: {reason}` | relax the conflicting requirement named above |
| 1016 **new** | definition | `{path} conflicts with generated output of {label}: {reason}` | move or delete the file, or run bob gen --force (keeps a backup) |
| 1017 **new** | definition | `nixpkgs package "{attr}" cannot be evaluated on {system}` | pin another nixpkgs revision or use another package |
| 1018 **new** | definition | `evaluating bob.go is not deterministic: {label}` | bob.go must not depend on time, randomness, environment or map order |
| 1019 **new** | definition | `bob.lock cannot be read: {reason} (line {line})` | resolve the merge conflict, or run bob lock --rebuild to resolve everything again from bob.go |
| 1020 **new** | definition | `{label}: invalid {option}: {reason}` | — (option validation: image `MaxLayers`, forbidden compiler flags, release version, unsupported cross target) |
| 1021 **new** | definition | `{label}: toolchain mismatch: {reason}` | align the versions named above (Go/Rust/generator runtime/target glibc) |
| 1022 **new** | definition | `{label} is not a static binary (it needs {deps})` | use tarball.Closure |
| 1023 **new** | definition | `{package} cannot be pinned: {reason}` | — (no checksum; no nixpkgs mapping for an opam `conf-*` package) |
| 1024 **new** | definition | the rule's text from §01 R-FW-002 (R1–R7), e.g. `{dir}/bob.go: the first line must be "//go:build bob" …` | — (all violations are listed, sorted by path) |
| 1025 **new** | definition | `{pos}: source pattern "{pattern}" leaves the repository` / `{pos}: bob.Glob("{pattern}") matches no files` / `{file}: symlink points outside the repository` | — |
| 2000 | build | `{label} failed to build` | — (excerpt = last 10 log lines) |
| 2001 | build | `{label}: compile error in {file}` | — |
| 2002 | build | `{label}: undefined symbol {symbol}` | the symbol is not provided by any linked library; a dependency is probably missing from Deps |
| 2003 | build | `{label}: header {header} not found` | add the package that provides {header} to Deps |
| 2004 | build | `{label}: library {lib} not found` | add the package that provides lib{lib} to Deps, or remove the link flag |
| 2005 | build | `{label}: tests failed` | run bob test {label} -v to see the full test output |
| 2006 | build | `{label}: dependency {module} needs Go {want} (toolchain is Go {have})` | use an older version of {module} or pin a newer nixpkgs |
| 2007 | build | `{label} timed out after {seconds}s` | raise the package's timeout if the step is legitimately slow |
| 2008 | build | `{label} produced no output for {seconds}s and was stopped` | a step is probably waiting for input or hanging |
| 2009 | build | `{label} tried to use the network during the build` | builds are offline; declare the download as a fetched dependency so bob.lock pins it |
| 2010 | build | `{label}: Rust crate {crate} not found` | add the crate to the package's dependencies |
| 2011 | build | `{label} wrote more than {bytes} bytes of log output and was stopped` | a step is printing in a loop; reduce its output or raise the log limit |
| 2012 **new** | build | `dev server {name} exited with status {status}` | see {log} |
| 2013 **new** | build | `{label}: {package} needs its {script} script ({reason})` | add npm.AllowScripts("{package}") to {label} |
| 2014 **new** | build | `{label}: {package} uses {dep} without declaring it` | add npm.Extend("{package}", npm.Peer("{dep}")) |
| 2015 **new** | build | `{label}: prune pattern "{pattern}" removes {file}, which {user} needs` | narrow the pattern |
| 3001 | fetch | `{label}: download does not match bob.lock` | if the upstream file legitimately changed, run bob lock; otherwise the source was tampered with |
| 3002 | fetch | `{label}: download failed: {reason}` | check the URL and your network; bob retried {attempts} times |
| 3003 | fetch | `{label}: download not found (HTTP {status})` | the file was removed or the URL in bob.go is wrong |
| 3004 **new** | fetch | `{label}: archive {url} contains an unsafe entry {path} ({reason})` | the archive is malformed or hostile; bob refused it |
| 4001 | cache | `cache {cache} rejected bob's credentials` | run bob login |
| 4002 | cache | `cache {cache} lists {what} but cannot deliver it (HTTP {status})` | the cache is inconsistent; report it to the cache admin |
| 4003 | cache | `cache {cache} is unreachable: {reason}` | bob builds locally when a cache is down |
| 4004 | cache | `cache {cache} served {what} without a trusted signature` | the cache's signing key is not trusted; check bob registry list |
| 4005 | cache | `cache {cache} served corrupted data for {what}` | report this to the cache admin; bob refused the download |
| 4006 | cache | `{label} is not in any cache and local builds are disabled` | build it remotely (bob build --remote) or allow local builds |
| 4007 | cache | `uploading {label} to the cache failed` | the build itself succeeded; the worker retries the upload |
| 4008 **new** | server | `your login to {server} has expired or is invalid` | run bob login {server} |
| 4009 **new** | server | `no bob server configured` / `bob server {server} is unreachable: {reason}` | run bob login <url> / check the URL and your network |
| 4010 **new** | server | `{server} denied {action} on project {project}` | ask an admin for the permission {permission} |
| 4011 **new** | server | `release {ref} not found on {registry}` | bob list --available {project} shows the versions |
| 4012 **new** | server | `remote build {id} failed on the server: {reason}` | bob log --build {id} |
| 5001 | upstream | `nixpkgs package {attr} failed to build from source (needed by {users})` | this is not your code: pin another revision or ask your bob server admin |
| 6001 | resource | `no space left on device while building {what}` | free disk space (bob gc) or move bob's store to a larger disk |
| 6002 | resource | `{what} was killed: out of memory` | reduce parallel jobs (bob build -j) or raise the memory limit |
| 6003 | resource | `{what} was killed by signal {signal}` | run the build again; if it repeats, reduce parallel jobs |
| 7001 | store | `bob's store is locked by another process` | another bob command is running |
| 7002 | store | `bob's store is not accessible: {reason}` | bob's state directory belongs to another user or is read-only |
| 7003 | store | `bob's store is damaged: {what}` | run bob doctor --repair |
| 7004 | store | `bob's cache database is busy` | another bob process is using the same state directory |
| 7005 **new** | host | `bob cannot create its private /nix: unprivileged user namespaces are blocked on this system` (+ `cause:` line; block of §01 R-NIX-022) | `fix (run once as an administrator):` + the exact fix of §01 R-NIX-022 / ADR 0002 Decision 4 |
| 7006 **new** | host | `shim {shim} belongs to {root}, which has no bob environment` | run bob env in {root}, or delete {shim} |
| 7007 **new** | host | `storage backend {endpoint} is not supported: {reason}` | bob-server needs an S3 backend with conditional writes (D63) |
| 7008 **new** | host | `{bucket} bucket policy is too permissive: {reason}` | apply the shipped policy (§05.2.3) |
| 7009 **new** | host | `the worker must run as root; builds themselves never run as root` | start bob-server worker as root (D56) |
| 7010 **new** | host | `worker cannot isolate builds: {reason}` | see the worker host requirements (§05.8) |
| 8001 | interrupt | `build cancelled` | — |
| 9001 | internal | `bob's builder for {label} is broken: {reason}` | this is a bob bug; please report it with bob log {label} |
| 9002 | internal | `{label} did not produce its output` | this is a bob bug; please report it with bob log {label} |
| 9003 | internal | `{label} refers to {ref}, which it must not reference` | this is a bob bug (missing fixup) |
| 9004 | internal | `bob generated an invalid build step for {label}: {reason}` | this is a bob bug; please report it with bob build -v |
| 9005 | internal | `Nix stopped unexpectedly ({reason})` | run with -v and report it |
| 9006 **new** | internal | `quirks entry {entry} has no justification or passing test` | (CI check over the quirks tables, §03.0.6) |
| 9007 **new** | internal | `embedded builder does not match its recorded hash` | this is a bob bug (stale builders.go table) |
| 9998 | internal | `unrecognised Nix log format (Nix {nixVersion})` | bob only supports the Nix it embeds |
| 9999 | internal | `unrecognised Nix error` | this is a bob bug; the raw message is shown with -v |

Warnings use the same codes with `severity: "warning"`. For example, 4001–4004 are warnings
when Nix worked around the problem by building locally (S15 fault 43).
S12's `BOB-AUTH-001` is renamed to `BOB-E-4008`. 7007–7010 are raised by `bob-server` (exit 7), except start-up refusals (unsupported backend, policy self-check), which exit 78 (`EX_CONFIG`).

#### Cross-section code assignment (integration)

Every error text in §01, §03, §04 and §05 uses the code below; their prose names stay as
aliases only in this table.

| Name / text in a section | Code |
|---|---|
| §03 `E-DRIFT`; "go.mod or go.sum was changed outside bob" | 1011 |
| §03 `E-LOCK-STALE`; §01 "bob.lock is stale" | 1012 (entry missing for one label: 1009) |
| §03 `E-RESOLVE` | 1015 |
| §03 `E-FETCH-HASH`; §01 fetcher mismatch | 3001 |
| §03 `E-GEN-CONFLICT`; nested/shared generated dirs; user-owned `.cargo/config.toml` | 1016 |
| §03 `E-SCRIPT-DENIED` | 2013 |
| §03 `E-PHANTOM-DEP` | 2014 |
| §03 `E-QUIRK-TEST` | 9006 |
| §03 forbidden compiler flag; §04 `MaxLayers`, roots > MaxLayers, invalid release version; OCaml cross | 1020 |
| §03 Go module needs newer Go | 2006 (at build) / 1008 (at evaluation); Rust mixed toolchains, generator/runtime mismatch, §04 nixpkgs target-library mismatch | 1021 |
| §03 opam package without checksum; `conf-*` without mapping | 1023 |
| §03 codegen generator failed | 2000 |
| §01 nixpkgs attribute cannot be evaluated | 1017 |
| §01 R1–R7 `bob.go` rule violations (R-FW-002) | 1024 |
| §01 source pattern leaves the repository / matches no files / symlink outside the repo (R-FW-021) | 1025 |
| §02 duplicate dev-shell names | 1020 |
| §01 non-deterministic evaluation | 1018 |
| §01 unreadable bob.lock | 1019 |
| §01 unsafe archive entry | 3004 |
| §01 embedded builder mismatch | 9007 |
| §01 user namespaces blocked | 7005 |
| §04 unknown platform / unknown variant token | 0001 (`kind` = platform / variant) |
| §04 package restricted to other platforms | 0006 |
| §04 conflicting profiles; Go `race` + `asan` | 0007 |
| §04 unsupported host architecture | 0008 |
| §04 non-static tarball | 1022 |
| §04 prune removes a needed library | 2015 |
| §05 unsupported storage backend / permissive policy / worker not root / no isolation | 7007 / 7008 / 7009 / 7010 |
| §02/§04 install of a release without closure artifacts | 0009 |

The server API uses its own `ErrorDetail.code` namespace `BOB-API-NNNN` (§06 §6.1). The CLI maps
API errors to BOB-E codes: Unauthenticated → 4008; PermissionDenied → 4010; NotFound of a
release → 4011, of anything else → 4010 (no existence leak); Unavailable/ResourceExhausted or no
connection → 4009; a remote build that ends FAILED → 4012 plus the worker's own BOB-E errors
(§05 `JobResult.Error`); Internal → 4012 with the server's `ref` id.

### Go types (`pkg/bob/diag`) [proposal]

```go
// Package diag defines bob's user-facing errors. Every error a user sees is a *Error with a
// stable Code; raw tool output is kept in Raw and shown only with -v.
package diag

// Code is a stable error number, e.g. 2003 for "header not found". Codes are never reused.
type Code uint16

// Class is the first digit of a Code. It decides the exit status (see ExitCode).
type Class uint8

const (
	ClassUsage      Class = 0
	ClassDefinition Class = 1
	ClassBuild      Class = 2
	ClassFetch      Class = 3
	ClassCache      Class = 4 // cache and server
	ClassUpstream   Class = 5
	ClassResource   Class = 6
	ClassStore      Class = 7 // store and host setup
	ClassInterrupt  Class = 8
	ClassInternal   Class = 9
)

// Severity is "error" or "warning".
type Severity string

// Pos is a source position in the repository, relative to the repo root ("libs/foo/bob.go:12:3").
type Pos struct {
	File      string
	Line, Col int
}

// Error is one diagnostic. Message and Hint come from the code's templates filled with Args.
type Error struct {
	Code      Code
	Severity  Severity
	Label     string            // package label, "" when not about a package
	DefinedAt Pos               // where the package was defined (bob.go position)
	Location  *Pos              // compiler diagnostic position, when one exists
	Cause     string            // one line
	Excerpt   []string          // ≤ 12 lines; store paths already rewritten (§02.5 rule 3)
	Args      map[string]string // template arguments
	Log       string            // "bob log //x:y" command, when a log exists
	Raw       []string          // raw Nix/tool text; output only with -v
}

// New returns an Error for code with the template arguments in args (key, value, key, value…).
// It panics if code is not in the table or an argument of the template is missing (tested).
//
//	err := diag.New(2003, "label", "//api:api", "header", "rcore.h")
//	err.Error() == "//api:api: header rcore.h not found"
func New(code Code, args ...string) *Error

// Warn is New with Severity "warning".
//
//	w := diag.Warn(4001, "cache", "bob.example.com")
func Warn(code Code, args ...string) *Error

// Error returns the filled message template (the first line of the human block).
func (e *Error) Error() string

// Class returns the code's class.
func (c Code) Class() Class

// ExitCode returns the exit status for a set of errors, using the priority order of §02.2.
// No errors (or warnings only) → 0.
//
//	diag.ExitCode([]*diag.Error{diag.New(2001, …), diag.New(4003, …)}) == 4
func ExitCode(errs []*Error) int
```

#### R-NIX-010 No raw Nix text by default
- **Statement:** No Nix message text reaches the user's default output (human or `--json`
  without `-v`). Every unmatched Nix error becomes `BOB-E-9999` and needs a regression fixture.
  The only Nix *evaluation* bob performs is `resolve.nix` (§01 §1.15); an error from it that is
  not classified per R-NIX-027 is a bob bug (9999) with a regression test.
- **Acceptance:** For each captured fault stream (≥ 48 faults × the embedded Nix), replayed
  through the translator, the default human output and the `--json` output contain none of these
  leak patterns: `^error:`, `Cannot build`, `Reason:`, `\.drv\b`, `/nix/store/[0-9a-z]{32}`,
  `builder for`, `derivation '`, `@nix `, `while evaluating`, and Nix source positions
  `/nix/store/…-source/….nix:`. With `-v`, the raw message appears in `nix:` lines (or
  in `raw[]`). An injected fault with a message missing from the pattern table yields
  `BOB-E-9999`, exit 70.
- **Source:** R-NIX-010, ADR 0017, S15 §3.2; merged with §01's evaluation part (integration).

#### R-NIX-012 Failures in bob terms
- **Statement:** A build failure is reported with the package label, the `bob.go` position where
  the package was defined, a log excerpt with repo paths, and a hint where one is known.
- **Acceptance:** For faults 6–15 and 22–33 of the catalogue, the golden output's error block
  always has `error[BOB-E-<code>]: <template>` and `package defined at <bob.go>:<line>:<col>`. The
  hint line is present iff the error table row has a hint; the excerpt lines (≥ 1) and
  `full log: bob log <label>` are present iff a builder log exists (a failure detected by bob
  before or without a builder run has neither). The block is byte-identical to
  `fixtures/<fault>/<nixver>/golden.txt`. A fault with `full log:` but no excerpt is a documented
  exception in the test (listed with its fault number), not a loosened rule.
- **Source:** R-NIX-012, S15 §5.4.

#### R-NIX-028 Error code stability
- **Statement:** The code table is append-only. Removing or changing the meaning of a code fails
  CI. Templates may be reworded only with a golden update.
- **Acceptance:** The harness compares `diag` codes with the protected `codes.lock` (code, class,
  template argument names). A removed code, a changed class or a changed argument set fails the
  check. `diag.New` panics on an unknown code or a missing argument; a test calls `New` for every
  code with its arguments.
- **Source:** ADR 0017 Decision 5.

#### R-NIX-029 Credential pre-check
- **Statement:** Before any Nix call that may substitute from a bob server, bob calls
  `AuthService/WhoAmI` (connect GET `/api/bob.auth.v1.AuthService/WhoAmI`) with the stored token, once per process. Unauthenticated (HTTP 401) → `BOB-E-4008`, PermissionDenied (403) →
  `BOB-E-4010`, unreachable → warning `4009` and the server's substituter is left out for this
  run.
- **Acceptance:** Given a server that returns 401 on `WhoAmI`, when `bob build //...` runs
  with a warm local store, then bob exits 4 with `BOB-E-4008` before Nix starts (no Nix process
  spawned, checked with a process counter). Given an unreachable server and a buildable target,
  bob prints warning 4009 and exits 0.
- **Source:** S12 §5, S15 fault 42/43, D61.

---

## 02.4 `--json` event stream, schema v1

NDJSON on stdout, one object per line. Every event has `"v":1`, `"event"` and `"time"` (RFC 3339
UTC with ms). Within v1, fields and events are only added, never renamed or removed. Nothing else
is written to stdout in `--json` mode; stderr gets only bob's own debug lines (`-vv`).

| event | fields |
|---|---|
| `run.start` | `command`, `args[]`, `requested[]` (labels after pattern expansion), `bob`, `nix` (versions) |
| `package.start` | `label`, `action` (`build`/`download`/`test`), `cache` (substituter name or `""`) |
| `package.wait` | `label`, `reason` |
| `upstream.start` | `name` (a nixpkgs package built from source) |
| `log` | `label`, `line` (only with `--json-log`) |
| `error` / `warning` | `error{code, severity, class, message, label, definedAt, location, cause, excerpt[], hint, log, args{}}`, `raw[]` only with `-v` |
| `test.result` | `label`, `status` (`pass`/`fail`/`cached`), `durationMs`, `failed[]` (test names, when the runner reports them) |
| `package.finish` | `label`, `status` (`built`, `cached`, `failed`, `skipped`, `cancelled`), `paths[]` (host paths, only with `--print-paths`) |
| `remote.submitted` **new** | `server`, `buildId`, `url` |
| `remote.state` **new** | `buildId`, `state` (§05 build states) |
| `result` **new** | `command`, `data` (command-specific object, below) |
| `run.finish` | `exitCode`, `counts{built,cached,failed,skipped,cancelled,upstream,downloaded}`, `drift[]`, `durationMs` |

`result.data` per query command:

| Command | `data` |
|---|---|
| `graph` | `{nodes:[{label, kind, definedAt}], edges:[{from, to}], why:[[label…]…]}` |
| `env` | `{root, shell, outside:{set:{K:V}, prepend:{PATH:[…]}}, insideOnly:[K…], tools:[name…]}` |
| `check` | `{findings:[error objects]}` (also emitted as `error` events) |
| `lock` | `{changes:[{ecosystem, name, from, to, op:"add"\|"update"\|"remove"}]}` |
| `list` | `{installed:[{name, ref, version, generation, bins:[…]}]}` |
| `version` | `{bob, nix, nixpkgs}` |
| `login` | `{server, user, expires}` |

Example (`bob build //api:api --json`, missing header):

```
{"v":1,"event":"run.start","time":"2026-10-21T10:00:00.000Z","command":"build","args":["//api:api"],"requested":["//api:api","//libs/foo:foo"],"bob":"0.1.0","nix":"2.34.9"}
{"v":1,"event":"package.start","time":"…","label":"//libs/foo:foo","action":"download","cache":"cache.nixos.org"}
{"v":1,"event":"package.finish","time":"…","label":"//libs/foo:foo","status":"cached"}
{"v":1,"event":"package.start","time":"…","label":"//api:api","action":"build","cache":""}
{"v":1,"event":"error","time":"…","error":{"code":"BOB-E-2003","severity":"error","class":"build","message":"//api:api: header rcore.h not found","label":"//api:api","definedAt":"api/bob.go:13:11","location":"api/cmd/api/main.go:7:10","cause":"","excerpt":["cmd/api/main.go:7:10: fatal error: rcore.h: No such file or directory"],"hint":"add the package that provides rcore.h to Deps","log":"bob log //api:api","args":{"header":"rcore.h","label":"//api:api"}}}
{"v":1,"event":"package.finish","time":"…","label":"//api:api","status":"failed"}
{"v":1,"event":"run.finish","time":"…","exitCode":1,"counts":{"built":0,"cached":1,"failed":1,"skipped":0,"cancelled":0,"upstream":0,"downloaded":1},"drift":[],"durationMs":2140}
```

#### R-CLI-002 Output modes
- **Statement:** Default output is concise (§02.5); `-v` adds raw detail; `--json` emits
  schema-v1 events and nothing else on stdout.
- **Acceptance:**
  - Every `--json` line of every golden run parses as JSON, has `v`=1, a known `event`, and
    validates against the protected JSON Schema `schema/events-v1.json`.
  - `run.finish` is the last line and its `exitCode` equals the process exit status.
  - Default output of a successful no-op build has ≤ 2 lines (a package line per requested label
    is suppressed when all are `cached` and stdout is not a TTY: only the summary line).
  - Every golden also exists with `-v` and differs from the default only by added lines.
- **Source:** R-CLI-002, S15 §5.5.

---

## 02.5 Human output

Error block and summary (S15 §5.4, unchanged):

```
FAILED  //api:api

error[BOB-E-2003]: //api:api: header rcore.h not found
  --> api/cmd/api/main.go:7:10
  package defined at api/bob.go:13:11
   | cmd/api/main.go:7:10: fatal error: rcore.h: No such file or directory
   |     7 | #include "rcore.h"
   |       |          ^~~~~~~~~
  hint: add the package that provides rcore.h to Deps
  full log: bob log //api:api

bob: 1 failed, 0 skipped, 0 built, 1 cached (exit 1)
```

Rules:
1. Package lines first: failed, then stopped, skipped, built, cached; sorted by label within each
   group. The words without a TTY are `FAILED`, `stopped`, `skipped`, `built`, `cached`; on a
   TTY the glyphs are `✗ - - ✓ ↓`. Without a TTY, package lines are printed as each package
   finishes.
2. Then one block per warning, then one block per error. Block lines, in order: `error[CODE]:` /
   `warning[CODE]:`, `-->`, `package defined at`, `cause:`, `|` excerpt (≤ 12 lines), `hint:`,
   `full log:`. Lines with no content are left out.
3. In excerpts, store paths are rewritten: a path inside an imported source becomes its repo
   path; any other `/nix/store/<hash>-<name>/<rest>` becomes `…/<name>/<rest>`.
4. Summary: `bob: N failed, N skipped, N built, N cached (exit E)`. Success without failures:
   `bob: N built, N cached`. For `test`: `bob: N passed, N failed, N cached (exit E)`.
5. `-v` adds `  nix: <raw line>` lines at the end of each block.
6. Colour only when stdout is a TTY and `TERM` is not `dumb`. Never with `NO_COLOR` set (any
   value). `BOB_COLOR=always` or `--color=always` forces it, but `NO_COLOR` still wins.
   Colours: error red bold, warning yellow bold, label bold, hint cyan, `-->` blue.
7. Live status line (TTY only, not `TERM=dumb`, not `-q`): one line on stderr, redrawn at most
   10×/s: `[done/total] building //x:y (12s) · downloading 3 toolchains 120 MB`. It is cleared
   before any other output.

Other output samples:

```
$ bob test //libs/...
PASS    //libs/foo:test (1.2s)
cached  //libs/bar:test
FAIL    //libs/greet:test (0.4s)

error[BOB-E-2005]: //libs/greet:test: tests failed
  package defined at libs/greet/bob.go:20:9
   | --- FAIL: TestHello (0.00s)
   |     greet_test.go:9: got "hi", want "hello"
  hint: run bob test //libs/greet:test -v to see the full test output
  full log: bob log //libs/greet:test

bob: 1 passed, 1 failed, 1 cached (exit 1)
```

```
$ bob graph --why //cmd/api:api nixpkgs:openssl
//cmd/api:api                   cmd/api/bob.go:9:5
└─ //libs/foo:foo               libs/foo/bob.go:12:9
   └─ nixpkgs:openssl           libs/foo/bob.go:15:20
```

`--why` prints the shortest path; `--why --all` prints up to 20 paths. `--format dot` prints a
Graphviz digraph with labels as node ids.

```
$ bob lock --update
bob.lock: 3 changes
  ~ go     golang.org/x/net   v0.30.0 → v0.31.0
  + npm    react-dom          19.3.0
  - crate  serde_derive       1.0.210
```

```
$ bob check
error[BOB-E-1011]: generated file web/package.json was edited by hand
  hint: move the change into bob.go; bob check --fix regenerates the file
error[BOB-E-1012]: bob.lock is out of date: //web:app needs npm zod (not pinned)
  hint: run bob lock

bob: 2 problems (exit 3)
```

#### R-CLI-007 Human output rules
- **Statement:** Human output follows rules 1–7.
- **Acceptance:** The S15 golden set (each fault, `TERM=dumb`, not a TTY) matches byte for byte.
  The same runs under a pseudo-TTY with `NO_COLOR=1` contain no `\x1b[` bytes; with
  `BOB_COLOR=always` and no `NO_COLOR` they contain colour sequences; with `TERM=dumb` and a
  pty, no `\r` redraws. No excerpt line in any golden matches `/nix/store/[0-9a-z]{32}`.
- **Source:** S15 §5.4, R-CLI-002.

---

## 02.6 Build, test, run, log, remote

### Local commands

- `bob build`: evaluates, runs the pre-checks, computes the derivations, realises them with the
  ADR 0017 invocation, checks output validity, and prints the results.
  - `--print-paths`: also prints, per package, the outputs' **host** paths
    (`<state>/nix/store/…`, §02.7.5). They can be read outside the namespace; binaries there run
    only through `bob run`/`bob exec`.
  - No `result` symlinks are created.
- `bob test`: builds the test derivations; cached test results count as passed (`cached`).
  `--rerun` re-runs the selected tests even when cached (Nix `--rebuild` on the test
  derivations) **[proposal]**. Test args after `--` become part of the test derivation (they
  change its hash, so the result is cached per argument set).
- `bob run <label> -- args`: builds the label for the native platform, then runs its main binary
  inside the namespace. The current directory, stdin, stdout and stderr are the caller's, and the
  environment is the caller's plus nothing (not the dev environment). A package with several
  binaries needs `--bin <name>`, else `BOB-E-0004`.
- `bob log <label>`: shows the full build log of the label's last local build (from Nix's log
  store), exactly as written, including raw text. `--build <id>` reads the log of a server build
  (§06 `LogService/GetLog`, from lake). `-f` follows a running server build by live pull (D55) and
  switches to lake on gaps. `--attempt N` picks the attempt.

### `--remote` (R-DEV-005)

```
$ bob build --remote //cmd/api:api
remote build b-01JB7Q8V3K queued on bob.example.com
  https://bob.example.com/p/acme/builds/b-01JB7Q8V3K
built   //libs/foo:foo
built   //cmd/api:api
downloading 2 outputs (14.2 MB)
bob: 2 built (remote), 0 cached (exit 0)
```

Steps:
1. Pre-check `whoami` (R-NIX-029).
2. Snapshot the source: the files that git tracks plus untracked files that are not ignored, as
   a content-addressed zstd tarball, uploaded with `PUT /upload/source/sha256/<hex>` (§06
   §6.9.7) unless the server already has its hash. The snapshot is **[open]**: the alternative is "commit must be pushed, send the
   SHA". Recommendation: snapshot, because a dirty tree is the normal developer case and the
   worker re-evaluates from source anyway; it is an input, not a cache push (D9 is about
   outputs).
3. `BuildService/CreateBuild{project_id, source.upload_sha256, targets, platforms, variants, test}` → build id.
4. `BuildService/WatchBuild` streams package events, shown with the same output rules. Errors arrive
   as `diag.Error` objects that the worker produced.
5. On success, the outputs are substituted from the server cache into the local store (negative
   narinfo TTL 0, S12), unless `--no-download`.
6. Ctrl-C detaches: the build continues on the server, and bob prints `bob cancel b-…` to stop
   it. A second Ctrl-C within 2 s cancels the server build (`BuildService/CancelBuild`).

#### R-DEV-005 Remote builds instead of pushes
- **Statement:** Developers never upload build outputs; `--remote` (on `build`, `test` and
  `pipeline run`) builds on the fleet and downloads the results.
- **Acceptance:**
  - Given a logged-in developer, the server's request log of `bob build --remote //...` contains
    no write to the cache endpoints, and the local store afterwards holds valid outputs with the
    server's signature.
  - The server's permission for a developer token rejects `PUT /nix/*` and the internal promote
    endpoint (403).
  - Ctrl-C during `--remote` leaves the server build running (`remote.state` = `running` 3 s
    later); `bob cancel <id>` moves it to `cancelled`.
- **Source:** R-DEV-005, D9, D61, S13 (dev stores not trustworthy).

#### R-CLI-008 Logs
- **Statement:** `bob log` shows the complete log of one package, local or from the server, and
  can follow a running server build.
- **Acceptance:** After fault 6 (C++ compile error), `bob log //libs/foo:foo` prints the full gcc
  output, and its last 10 lines match the excerpt. For a server build of a fixture that prints
  1,000 numbered lines, `bob log --build <id> //x:y` prints all 1,000 lines in order with no
  duplicates, and `bob log -f` started mid-build ends with line 1000, no gaps and no duplicates
  (dedupe by (build, package, attempt, seq), §05).
- **Source:** R-LOG-003, D55, S17.

---

## 02.7 Developer environment

The model (D51, D59, ADR 0002 Decision 5):
- **Inside the namespace** (`bob dev`, `bob exec`, `bob run`), the environment points at
  `/nix/store` toolchains like `nix develop`.
- **Outside the namespace** (the user's own shell, an editor started from the dock), only
  `.bob/bin` shims are put on `PATH`. A shim re-enters the namespace. Language servers are
  started behind a path-rewriting proxy, so locations into dependencies open from the host.
- **`cd` + start the editor works** through the shell hook (§02.7.1) for every editor that finds
  its language servers through `PATH` (Neovim, Helix, Emacs, Zed, Sublime) and for VS Code's
  Go, Rust and C/C++ extensions through the written settings. Editors that read toolchain files
  themselves (VS Code's built-in TypeScript, JetBrains IDEs) are started with `bob dev -- <editor>`.

### 02.7.0 `bob.DevShell` (declaration in `bob.go`) [proposal, added at integration]

Part of `pkg/bob` (L0, §01 §1.4); §01 lists it as provided by this section.

```go
// DevShell declares one development environment for bob dev, bob env, bob exec and the
// shell hook. Register it with bob.Define like any package; the shell named "default" (or the
// only one) is used when no --shell is given.
type DevShell struct {
	Name     string            // "default" when empty; [a-z0-9-]{1,32}
	Packages []Tool            // top-level tools/toolchains; their bin/ go on PATH in this order
	Env      map[string]string // extra variables (inside; exported outside per §02.7.2 rules)
	Hooks    []string          // shell commands run by bob dev before the shell starts
	Servers  []Package         // dev-server packages (e.g. npm.Vite, §03.4.8) started by bob dev
}

// NewShell validates s and returns it as a graph node (kind "bob.devshell"). The toolchains of
// every ecosystem used in the repo (Go, Rust, C/C++, Node, OCaml at the pins of §03) and their
// in-tree tools (e.g. node_modules/.bin) are added automatically; Packages adds extra tools.
//
//	var Dev = bob.Define(func(p *bob.P) *bob.Shell {
//		return bob.NewShell(p, bob.DevShell{
//			Packages: []bob.Tool{nixpkgs.Tool(p, "postgresql"), nixpkgs.Tool(p, "jq")},
//			Env:      map[string]string{"DATABASE_URL": "postgres://127.0.0.1/acme"},
//			Servers:  []bob.Package{web.Site(p)}, // web.Site: a bob.Define'd npm.Vite package
//		})
//	})
func NewShell(p *P, s DevShell) *Shell

// Shell is the graph node of a DevShell.
type Shell struct{ Node }
```

Errors: two shells with one name → `BOB-E-1020` (`option` = `Name`); `--shell` naming no shell
→ `BOB-E-1014`.

### 02.7.1 Shell hook (`bob hook`, `bob env --hook`)

Install once:

| Shell | Line in rc file |
|---|---|
| bash (`~/.bashrc`) | `eval "$(bob hook bash)"` |
| zsh (`~/.zshrc`) | `eval "$(bob hook zsh)"` |
| fish (`~/.config/fish/config.fish`) | `bob hook fish \| source` |

Exact hook output (`<bob>` = absolute path of the running bob, `os.Executable`):

```bash
# bob shell hook (bash)
_bob_hook() {
  local s=$?
  eval "$("<bob>" env --hook=bash)"
  return $s
}
if [[ ";${PROMPT_COMMAND[*]:-};" != *";_bob_hook;"* ]]; then
  if [[ "$(declare -p PROMPT_COMMAND 2>/dev/null)" == "declare -a"* ]]; then
    PROMPT_COMMAND=(_bob_hook "${PROMPT_COMMAND[@]}")
  else
    PROMPT_COMMAND="_bob_hook${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
  fi
fi
```

```zsh
# bob shell hook (zsh)
_bob_hook() { eval "$("<bob>" env --hook=zsh)" }
typeset -ag precmd_functions chpwd_functions
(( ${precmd_functions[(I)_bob_hook]} )) || precmd_functions=(_bob_hook $precmd_functions)
(( ${chpwd_functions[(I)_bob_hook]} )) || chpwd_functions=(_bob_hook $chpwd_functions)
```

```fish
# bob shell hook (fish)
function __bob_hook --on-event fish_prompt --on-variable PWD
    "<bob>" env --hook=fish | source
end
```

`bob env --hook=<shell>` algorithm (runs at every prompt; never enters the namespace):
1. If `BOB_DEV=1` (already inside `bob dev`): print nothing.
2. `root = FindRoot($PWD)`. If there is no root and `BOB_PROJECT` is set → print the **leave**
   script. If there is no root and none is active → print nothing.
3. If `root` is not allowed (R-DEV-009) → leave any active project; print to stderr, once per
   root per shell (tracked in `BOB_DENIED_SEEN`):
   `bob: <root> is not allowed; run 'bob allow' to load its environment`.
4. If `root == BOB_PROJECT` and `.bob/env.json` hash == `BOB_ENV_HASH` and the §01 eval key is
   unchanged → print nothing (**fast path**).
5. If the eval key changed or `.bob/env.json` is missing → evaluate and write `.bob/env.json`
   plus the shims (§02.7.2/3), with one progress line on stderr
   (`bob: loading /home/u/acme (dev shell default)…`). This does not realise toolchains;
   shims realise on first use. Ctrl-C aborts and nothing is exported. An evaluation error prints
   the human error block on stderr and keeps the previous environment.
6. Print **leave** for the previous project (if another one was active), then **enter**:
   - save the previous values of every variable it sets in `BOB_ENV_RESTORE`
     (base64url JSON `{K: value|null}`);
   - `PATH=<root>/.bob/bin:$PATH`;
   - the dev shell's `outside.set` variables (§02.7.2);
   - `BOB_PROJECT=<root>`, `BOB_ENV_HASH=<sha256 of env.json, 16 hex>`.
   **leave** restores from `BOB_ENV_RESTORE`, removes `<root>/.bob/bin` from `PATH`, and unsets
   `BOB_PROJECT`, `BOB_ENV_HASH` and `BOB_ENV_RESTORE`.
7. Values are quoted for the target shell (POSIX single quotes with `'\''`; fish `string escape`
   rules).

Example output on entering (bash):

```
export BOB_ENV_RESTORE='eyJQQVRIIjoiL3Vzci9iaW46L2JpbiIsIkNHT19FTkFCTEVEIjpudWxsfQ';
export PATH='/home/u/acme/.bob/bin:/usr/bin:/bin';
export CGO_ENABLED='0';
export BOB_PROJECT='/home/u/acme';
export BOB_ENV_HASH='9f2c41d07a1be355';
```

`bob env` without `--hook` prints the same enter script for the current project (for direnv:
`.envrc` = `eval "$(bob env)"`, and for editor launchers); `--format json` prints the `result`
event data.

#### R-DEV-003 Automatic activation per directory
- **Statement:** With the hook installed, `cd` into an allowed bob project puts its shims first
  on `PATH` and sets the dev shell's outside variables; `cd` out restores the previous
  environment exactly.
- **Acceptance:** For bash 5, zsh 5.9 and fish 3.7, a harness pty session runs: `cd acme` →
  `command -v gopls` = `/…/acme/.bob/bin/gopls` and `echo $CGO_ENABLED` = `0`; `cd /` → `PATH`
  and `CGO_ENABLED` are byte-equal to their values before `cd acme` (including unset); `cd acme`
  then `cd acme/libs` → no output from the hook, and `BOB_ENV_HASH` is unchanged. Moving from
  project A to nested project B switches to B; moving back switches to A.
- **Source:** R-DEV-003, D43, D59.

#### R-DEV-007 Hook cost
- **Statement:** The hook's fast path does no evaluation and no namespace entry.
- **Acceptance:** `bob env --hook=bash` on the fast path: p95 ≤ 10 ms [8c/15G], ≤ 20 ms
  [2c/4G], over 100 runs **[proposal; not measured yet: derived from bob's static Go start, S1
  shim cost 6.8–7.8 ms including the namespace entry, which the fast path skips]**. On the fast
  path the process opens no files besides `bob.lock` lookups, `.bob/env.json` and the §01 eval-key
  index (checked with strace in the harness).
- **Source:** R-DEV-006, R-NFR-008, D59.

#### R-DEV-009 Trust before automatic activation
- **Statement:** The hook evaluates `bob.go` (it runs project code) only in roots the user has
  allowed with `bob allow`. Explicit commands (`bob build`, `bob dev`, …) need no allow.
- **Acceptance:** Given a fresh HOME and a cloned project, `cd project` with the hook prints the
  "not allowed" line once, runs no evaluator (no process is spawned besides the hook's bob), and
  sets no `BOB_PROJECT`. After `bob allow`, the next prompt activates it. `bob deny` deactivates
  it at the next prompt. Allowed roots are stored in `$XDG_STATE_HOME/bob/allow.json`
  (`{"v":1,"roots":["/abs/path",…]}`, mode 0600). A root is trusted by path, not by content
  **[proposal]**: re-asking on every bob.go change would make the hook useless in daily work.
- **Source:** D43 (direnv-style), BOOTSTRAP §2 security principle.

### 02.7.2 `.bob/env.json`

Written atomically (temporary file + rename) by the hook, `bob env`, `bob dev` and every
evaluating command when its content changes. Gitignored (`.bob/`).

| Field | Type | Meaning |
|---|---|---|
| `v` | int | 1 |
| `root` | string | absolute repo root |
| `shell` | string | dev shell name |
| `evalKey` | string | §01 eval key this was computed from |
| `inside` | map string→string | the full `nix develop`-style variables: `PATH` (toolchain `bin` dirs, prepended to the caller's `PATH` minus `<root>/.bob/bin`), `GOROOT`, `CC`, `CXX`, `PKG_CONFIG_PATH`, `RUSTC`, `NODE_PATH`, …, plus the dev shell's own variables |
| `outside.set` | map string→string | variables exported outside: dev shell variables whose value contains no `/nix/store` path, plus variables whose value is exactly `/nix/store/<p>/bin/<tool>` with a shim for `<tool>`, rewritten to `<root>/.bob/bin/<tool>` |
| `insideOnly` | []string | variables that reference the store in any other way; never exported outside |
| `tools` | map name→{`path`, `lsp`, `env`} | every shimmed tool: the store or in-tree path inside the namespace, whether it is an LSP server (§02.7.4), and extra variables for this tool only |
| `closure` | []string | top-level store paths of the dev shell (realised by `bob exec`/`bob dev`, kept alive as GC roots) |
| `hooks` | []string | dev shell hook commands, run by `bob dev` before the shell starts |
| `servers` | []{`name`, `cmd`[], `cwd`, `ready`} | dev servers (§03 contract) |

Example:

```json
{"v":1,"root":"/home/u/acme","shell":"default","evalKey":"sha256:5b1d…",
 "inside":{"PATH":"/nix/store/x1qs…-go-1.26.8/bin:/nix/store/7zwy…-gopls-0.22.0/bin","GOTOOLCHAIN":"local","GOPROXY":"off","CGO_ENABLED":"0"},
 "outside":{"set":{"CGO_ENABLED":"0"}},
 "insideOnly":["GOROOT"],
 "tools":{"go":{"path":"/nix/store/x1qs…-go-1.26.8/bin/go","lsp":false,"env":{}},
          "gopls":{"path":"/nix/store/7zwy…-gopls-0.22.0/bin/gopls","lsp":true,"env":{"GOFLAGS":"-tags=bob"}},
          "tsc":{"path":"/home/u/acme/web/node_modules/.bin/tsc","lsp":false,"env":{}}},
 "closure":["/nix/store/x1qs…-go-1.26.8","/nix/store/7zwy…-gopls-0.22.0"],
 "hooks":[],"servers":[{"name":"web-vite","cmd":["vite","--config",".bob/vite.bob.config.mjs"],"cwd":"web","ready":"http"}]}
```

Tool set: every executable in `bin/` of each top-level package of the dev shell (not
transitive), in dev shell order (the first one wins on a name clash, like `PATH`), plus §03's
in-tree tools (`node_modules/.bin/*` of materialised importers). The Go package always gets
`gopls` with `env.GOFLAGS=-tags=bob` (S1-7).

### 02.7.3 Shims (`.bob/bin/<tool>`)

```sh
#!/bin/sh
# Code generated by bob. DO NOT EDIT.
exec "${BOB:-/home/u/.local/bin/bob}" exec --project '/home/u/acme' -- gopls "$@"
```

LSP servers (`tools[name].lsp`) get `exec --lsp`. Mode 0755. `.bob/bin` contains only shims; a
shim for a tool that left the dev shell is deleted at the next write. The `BOB` override exists
for moved bob binaries; the recorded path is the absolute `os.Executable()` at generation.

#### R-DEV-008 `bob exec` and shims
- **Statement:** `bob exec -- <tool> args` runs the tool inside the namespace with
  `inside` + `tools[tool].env`, realising the dev shell closure first if any path is invalid.
  Its stdin/stdout/stderr, cwd, exit status and signals are the tool's.
- **Acceptance:**
  - As a non-root user on a root without `/nix` (`-hide-nix` harness mode),
    `.bob/bin/go version` prints the pinned Go version and exits 0.
  - `.bob/bin/go` subprocesses started by gopls run in bob's mount namespace (S1 method:
    uid_map `0 <uid> 1`, mntns ≠ host).
  - `sh -c '.bob/bin/go run ./x; echo $?'` with `os.Exit(5)` prints 5; SIGTERM to the shim
    reaches the tool.
  - Shim start overhead for `/bin/true` ≤ 15 ms p95 [8c/15G] and ≤ 20 ms [2c/4G] **[proposal;
    S1 measured 6.8–7.8 ms, 20 runs × 5 rounds, bobtest, indicative]**.
  - A shim whose root has no `.bob/env.json` exits 7 with `BOB-E-7006`.
- **Source:** ADR 0002 Decision 5, D51, S1 §3.3, S4 §3.6.

### 02.7.4 LSP path-rewriting proxy (`bob exec --lsp`)

Problem: a language server runs inside the namespace and answers with `file:///nix/store/…`
locations (Go std, crates, npm types, C headers). An editor outside the namespace on a host
without `/nix` cannot open them (S1, S4 O-S4-2, O-DX-1). On a host with a system `/nix` the
path may exist in the *system* store by coincidence, but nothing guarantees it.

Fix: `/nix/store/X` inside is the same file as `<state>/nix/store/X` on the host, because
bob's store directory is a plain directory in the user's home (ADR 0002 Decision 1). The proxy
rewrites store paths between the two views in every message:
- **server → client:** `/nix/store` → `<state>/nix/store`;
- **client → server:** `<state>/nix/store` → `/nix/store`, so the server recognises a store file
  the editor opened (e.g. `GOROOT` files) as its own.

Rules (implemented and tested in the proof, see below):
1. Placement: the bob child process inside the namespace starts the server as its own child and
   pumps both directions. No extra process outside. stderr passes through unchanged.
2. Framing: LSP base protocol (`Content-Length` header, `\r\n\r\n`, body). Other headers are
   dropped; the output frame has only `Content-Length`. Messages keep their order per direction.
3. Fast path: a body that contains none of `<from>/`, the JSON-escaped form `\/nix\/store\/`, or
   the percent-encoded form of `<from>/` is forwarded byte for byte.
4. Otherwise the body is decoded (`UseNumber`, so ids and numbers are kept exactly), every
   **string value and every object key** is rewritten (keys: `WorkspaceEdit.changes` is keyed by
   URI), and it is re-encoded without HTML escaping.
5. String rewriting is one left-to-right pass; replaced text is never rescanned. Two patterns:
   - `file://<esc(from)>/` → `file://<esc(to)>/`, where `esc` is URI path percent-encoding (a
     host home such as `/home/a b` becomes `/home/a%20b`);
   - a bare `<from>/`, only at a boundary: start of string, or a preceding byte that is not
     `[A-Za-z0-9/._+~-]`. Reason: the host path `…/share/bob/nix/store/` itself contains
     `/nix/store/`, and must never be rewritten again.
6. A body that is not valid JSON is forwarded unchanged (never dropped).
7. In `--lsp` mode bob writes **nothing** on stdout itself. Its errors (namespace setup,
   realisation) go to stderr as human blocks, and it exits with the class exit code before the
   server starts.
8. The working tree is never under `/nix`, and its paths are identical inside and outside, so the
   workspace URIs are not touched.
9. tsserver's own protocol (newline-delimited JSON requests, `Content-Length` responses, `file`
   fields with plain paths) is **[open]**. Recommendation: support it as a second framing in v1
   only if VS Code's TypeScript is made to work outside `bob dev` (see §02.7.6); the
   `typescript-language-server`/`vtsls` LSP servers (used by Neovim, Helix, Zed) are covered by
   rules 1–8.

LSP server table (`lsp: true`) **[proposal]**: `gopls`, `clangd`, `rust-analyzer`,
`typescript-language-server`, `vtsls`, `ocamllsp`, `vscode-eslint-language-server`,
`vscode-json-language-server`, `svelteserver`, `tailwindcss-language-server`. **[open]**: a
`DevShell` option to mark more tools as LSP (§02.7.0). Recommendation: add it as
`Tool("x").LSP()`, only if a user needs it.

**Proof (2026-10-21).** Code: `/root/scratch-spec02/lspproof` (`rewrite/` package with unit
tests, `cmd/lspproxy`, `cmd/lspcheck` = a scripted LSP client acting as an outside editor);
runner `/root/scratch-spec02/run.sh`; results `/root/scratch-spec02/results/`. Setup: the S1
fixture `acme` and store of user `bobtest`, gopls 0.22.0 and go 1.26.8 through the S1
`.bob/bin/gopls` shim. The client runs in a root **without `/nix`** (`bobexec -hide-nix`, so the
shim nests into pivot mode).

| Run | Client view | `def fmt.Println` result | File opens for the client | Navigation inside the opened std file | Raw `/nix/store` refs seen by client |
|---|---|---|---|---|---|
| A: no proxy | no `/nix` | `file:///nix/store/x1qs…-go-1.26.8/share/go/src/fmt/print.go:313` | ✗ | — | 1 |
| B: proxy | no `/nix` | `file:///home/bobtest/s01/state/nix/store/x1qs…/fmt/print.go:313` | ✓ | hover `Fprintln` ✓; def `Fprintln` → print.go:302 ✓; def `os.Stdout` → `…/os/file.go:72` ✓; references 2/2 openable | 0 (27 of 35 frames rewritten) |
| C: proxy | real host with system `/nix` | same as B | ✓ | same as B | 0 |
| baseline: no proxy | real host with system `/nix` | `/nix/store/…` (opens only because root's system store has the same go) | ✓ (coincidence) | — | 26 |

Timing: warm `textDocument/definition` round trip, mean of 20: 0.55 ms (B), 0.72 ms (C), 0.55 ms
(baseline, no proxy) [8c/15G, busy host]; 0.56 ms (B) [2c/4G]. Rewriting one location message:
21–24 µs; the pass-through check runs at 8.5 GB/s [8c/15G]. Proxy overhead is not measurable at
the request level. Not verified: a real Neovim/VS Code session (the client is scripted), clangd,
rust-analyzer, typescript-language-server.

#### R-DEV-010 LSP locations open outside the namespace
- **Statement:** Every LSP server started through a shim answers with host paths for store
  files and accepts host paths for them, so go-to-definition into dependencies works for an
  editor outside the namespace on a host without `/nix`.
- **Acceptance:**
  - (harness, bobtest, `-hide-nix` client) On the Go fixture, `lspcheck` through
    `.bob/bin/gopls`: `def fmt.Println` returns a `file://<state>/nix/store/…/fmt/print.go` URI
    that `os.Stat` finds. After `didOpen` of that URI, hover and definition inside it succeed and
    return host URIs. 0 raw `/nix/store/` references reach the client.
  - The same script shape runs for clangd (`#include <stdio.h>` → glibc header), rust-analyzer
    (`String` → std source) and typescript-language-server (`useState` → `@types/react`).
  - The proxy unit tests cover: URI, bare path, boundary (`<state>/nix/store` not rewritten
    twice, `/foo/nix/store` untouched), percent-encoding, object keys, numbers > 2^53,
    pass-through byte equality, invalid JSON pass-through.
  - Warm definition latency through shim + proxy ≤ 2 ms mean [8c/15G] and ≤ 3 ms [2c/4G]
    **[proposal; measured 0.55–0.72 ms / 0.56 ms]**.
- **Source:** D59, D51, owner Round 1 answer 8, S1 §3.2–3.3, S4 §3.6, proof above.

### 02.7.5 Host paths of store files

`<state>` in this section is §01's state home `<home>` (R-NIX-024: `$BOB_HOME`, else
`${XDG_DATA_HOME:-$HOME/.local/share}/bob`).

`bob build --print-paths`, the LSP proxy and editor settings use the host form
`$XDG_DATA_HOME/bob/nix/store/<hash>-<name>` (default `~/.local/share/bob/nix/store/…`). Files
there are readable from outside but read-only. Executables there do not run outside the
namespace (ELF interpreter is a `/nix/store` path, S0b).

### 02.7.6 Editor settings (`bob editor`)

`bob editor <name>` prints the settings; `--write` merges them into the project file (JSON merge
by key; other keys are kept; a comment-free JSON file is required, else `BOB-E-0004` with the
snippet to paste by hand).

| Editor | Works after `cd` + start? | What bob provides |
|---|---|---|
| Neovim (lspconfig / `vim.lsp`) | yes, with the hook | nothing to configure: `gopls`, `clangd`, `rust-analyzer`, `typescript-language-server` resolve to `.bob/bin`; `GOFLAGS=-tags=bob` is in the gopls shim env (S1: same result as `buildFlags`) |
| Helix, Emacs (eglot/lsp-mode), Zed, Sublime LSP | yes, with the hook (via `PATH`) | same as Neovim; `bob editor zed` prints `lsp.<server>.binary.path` entries for Zed builds that ignore `PATH` |
| VS Code: Go, rust-analyzer, clangd extensions | yes, after `bob editor vscode --write` | `.vscode/settings.json` below (absolute paths are not needed: `${workspaceFolder}`) |
| VS Code: built-in TypeScript/JavaScript | **no**: needs `bob dev -- code --new-window .` | tsserver runs on VS Code's own Node and reads `node_modules` itself (symlinks into `/nix/store`) |
| JetBrains IDEs | **no**: needs `bob dev -- <ide> .` | the IDE reads SDK/toolchain files itself |

```json
{
  "go.alternateTools": { "go": "${workspaceFolder}/.bob/bin/go", "gopls": "${workspaceFolder}/.bob/bin/gopls" },
  "go.toolsManagement.autoUpdate": false,
  "gopls": { "build.buildFlags": ["-tags=bob"], "build.directoryFilters": ["-.bob"] },
  "rust-analyzer.server.path": "${workspaceFolder}/.bob/bin/rust-analyzer",
  "clangd.path": "${workspaceFolder}/.bob/bin/clangd",
  "files.watcherExclude": { "**/.bob/**": true }
}
```

Only keys for ecosystems the project uses are written. **[open]** VS Code's built-in TypeScript
outside `bob dev`: `typescript.tsdk` = host path of the typescript package plus
`typescript.tsserver.nodePath` = `.bob/bin/node` would run tsserver inside the namespace, but
its `file` paths need the tsserver-framing proxy (rule 9). Recommendation: v1 documents
`bob dev -- code`; revisit after Stage 3 has a real VS Code test. **[open]**
`bob dev -- code .` forwards to an already running VS Code instance (outside the namespace)
unless `--new-window --user-data-dir` is used. Recommendation: `bob editor vscode` prints the
exact launch command with a per-project `--user-data-dir` under `.bob/vscode`; verify manually.

#### R-DEV-011 Editor settings
- **Statement:** `bob editor <name>` prints, and with `--write` merges, the settings above for
  the ecosystems in use.
- **Acceptance:** On a Go+Rust+C fixture, `bob editor vscode --write` on an existing
  `.vscode/settings.json` with an unrelated key keeps that key and adds exactly the keys above
  (golden). Run twice → byte-identical file. On a Go-only fixture, no `rust-analyzer.*` or
  `clangd.*` key is written. (manual) VS Code with the Go extension, started from the dock in the
  fixture: go-to-definition on `fmt.Println` opens print.go.
- **Source:** ADR 0002 Decision 5, S1 §5.2, D59.

### 02.7.7 `bob dev`

```
$ bob dev
bob: dev shell default (go 1.26.8, gcc 15.3, node 24.11) · 0.4s
bob: vite (web) ready at http://localhost:5173 · log .bob/dev/web-vite.log
[bob dev] ~/acme $ exit
bob: stopped vite (web)
```

Steps:
1. Find the root and the dev shell (`--shell`, else the shell named `default`, else the only
   shell; otherwise `BOB-E-1014`).
2. Evaluate, write `.bob/env.json` and the shims, and realise the closure (progress as for
   `build`).
3. Prepare the working tree (§03 `Prepare`: native files, materialised codegen, `node_modules`).
4. Enter the namespace with `inside` plus `BOB_DEV=1`, `BOB_PROJECT=<root>`,
   `BOB_DEV_SHELL=<name>`. Run `hooks` in order (a failing hook → its exit code and stderr, bob
   exits 1).
5. Start the dev servers in the background: their output goes to `.bob/dev/<name>.log`, and their
   readiness line is printed once (`ready: http` = a TCP connect to the port succeeds, or the
   §03 contract's ready pattern in the log). `--no-servers` skips them. `--servers-only` runs
   the servers in the foreground with prefixed output (`[web-vite] …`) and no shell.
6. Start `$SHELL` (else `/bin/sh`) interactively, or `cmd args` after `--`. The user's rc files
   run normally; bob does not touch `PS1`. Prompt integration is left to the user (`BOB_DEV_SHELL`).
7. While the shell runs, watch `bob.go`, `bob.lock` and the §01 eval-key inputs (inotify):
   - on a dependency change, re-lock if needed, re-prepare the tree, swap `node_modules`
     atomically, and restart the affected servers;
   - if `inside` changed, print once: `bob: dev environment changed; exit and run bob dev again
     to use it`.
   A server that exits on its own → `BOB-E-2012` warning on stderr; it is not restarted.
8. When the shell or command exits: stop the servers (SIGTERM, then SIGKILL after 5 s) and exit
   with the shell's/command's exit status.

#### R-DEV-001 Dev shells
- **Statement:** `bob dev` enters the dev shell defined in `bob.go`: its toolchains, extra
  packages, variables and hooks are present inside, and nothing else changes.
- **Acceptance:** As a non-root user on a host without `/nix`:
  - `bob dev -- sh -c 'command -v go; go env GOROOT; echo $CGO_ENABLED $BOB_DEV'` prints a
    `/nix/store/…-go-<pinned>/bin/go` path, a `/nix/store` GOROOT, and `0 1`, and exits 0.
  - `bob dev -- false` exits 1.
  - `bob dev --shell nope` exits 3 with `BOB-E-1014` listing the shell names.
  - A dev shell hook `echo hooked > .bob/hooked` has run before the command.
- **Source:** R-DEV-001, D51.

#### R-DEV-002 Tree preparation and dev servers
- **Statement:** `bob dev` prepares the working tree for tooling and runs the declared dev
  servers with live reload; dependency changes in `bob.go` are applied while the session runs.
- **Acceptance:** On the S4 vite-react fixture: after `bob dev --servers-only &`, an HTTP GET of
  `/` returns 200 within the entry budget. 20 saves of `src/App.tsx` from outside the namespace
  each produce an HMR `update` message on the websocket, p95 ≤ 50 ms server side
  **[proposal; measured median 3.3 ms, max 29 ms [2c/4G]]**. Adding an npm dependency in
  `bob.go` → within 10 s `node_modules/<dep>` exists and the server answers again **[proposal;
  measured relock 2.1–2.5 s + materialise 0.7–0.8 s + restart ~0.3 s]**. On exit, no process of
  the session is left (checked by the cgroup / process tree).
- **Source:** R-DEV-002, D34, S4 §3.5, §5.5, A20.

#### R-DEV-006 Developer-visible budgets
- **Statement:** The developer loop has measured budgets.
- **Acceptance** (the harness budgets file; profile [2c/4G] unless marked) **[proposal]**:

| Operation | Budget | From |
|---|---|---|
| `bob dev -- true`, nothing changed (eval no-op + materialise no-op + namespace) | ≤ 1.0 s [2c/4G], ≤ 0.5 s [8c/15G] | §01 eval no-op ≤ 0.1 s [8c/15G] / ≤ 0.15 s [2c/4G] (§1.17); S4 materialise no-op 0.42 s [2c/4G]; namespace 5–7 ms |
| Vite ready inside `bob dev` | ≤ 1 s | S4 257–309 ms [8c] |
| no-op `bob build` (Go fixture) | ≤ 0.5 s [8c/15G] | S2 quiet: 0.26 s hybrid |
| one-file incremental build (Go fixture `main.go`) | ≤ 4 s [8c/15G] (= §03.8) | S2 quiet: 2.87 s hybrid |
| shim start (`/bin/true`) | ≤ 15 ms [8c/15G] | S1 6.8–7.8 ms |
| hook fast path | ≤ 20 ms p95 | R-DEV-007 (not measured yet) |
| first `bob build` of the Go fixture on a fresh machine with a warm server cache | ≤ 2 s + closure size / 100 MiB/s + cold eval | S12 quiet: 100.7 MiB substitute 0.61–0.67 s [8c]; S0b extraction 294 ms |

- **Source:** R-DEV-006, R-NFR-002, R-NFR-008, quiet logs `docs/spikes/measure/quiet/s02-go.log`,
  S1, S4, S12.

#### R-DEV-004 Cache setup after login
- **Statement:** `bob login` configures bob's own Nix: substituter URL, trusted public keys and the
  netrc entry. Without a login, only cache.nixos.org and the binary caches configured in bob's
  config are used.
- **Acceptance:** After `bob login https://srv` (test server), `<home>/etc/nix/nix.conf` (§01 R-NIX-024) lists
  `https://srv/nix` after cache.nixos.org in `substituters`, its key from `AuthService/WhoAmI` (`nix.public_keys`) is in
  `trusted-public-keys`, and `<home>/etc/nix/netrc` (mode 0600) has
  `machine srv login bob password <token>`. No file outside bob's state and config dirs changed
  (checked by a file tree diff of HOME and `/etc`). After `bob logout`, those three entries are
  gone.
- **Source:** R-DEV-004, R-NIX-024, ADR 0014, S12 §5.

---

## 02.8 Login, registries and `bob install`

### Files

| File | Content | Mode |
|---|---|---|
| `$XDG_CONFIG_HOME/bob/config.json` | `{"v":1,"defaultServer":"…","registries":[{"name","url","publicKeys":[…]}],"defaultRegistry":"…","binDir":"~/.local/bin","caches":[{"url","publicKey"}]}` | 0644 |
| `$XDG_CONFIG_HOME/bob/credentials.json` | `{"v":1,"servers":{"<url>":{"kind":"user"\|"service","accessToken","refreshToken","expires","user"}}}` | 0600 |
| `$XDG_DATA_HOME/bob/install/generations/<n>.json` | `{"v":1,"n":7,"created","entries":[{"name","registry","ref","version","platform","storePaths":[…],"bins":{"<bin>":"<store path>/bin/<bin>"}}]}` (create-only: temp file + `rename`) | 0644 |
| `$XDG_DATA_HOME/bob/install/current` | symlink → `generations/<n>.json` | — |
| `<home>/nix/var/nix/gcroots/bob-install/<n>` | GC roots of the last 5 generations | — |

`config.json` is written by commands (`bob registry`, `bob login`). Hand edits are allowed and
validated at load (unknown key → `BOB-E-0004`).

### `bob login`

```
$ bob login https://bob.example.com
Open https://bob.example.com/device and enter code WDJB-MJHT (expires in 10 min)
Waiting… logged in to bob.example.com as linus (token valid 30 days, refreshed automatically)
```

- Flow choice: authorization code + PKCE with a loopback redirect (`http://127.0.0.1:<random>/callback`)
  when a browser can be opened (`$BROWSER`, or `xdg-open` with `DISPLAY`/`WAYLAND_DISPLAY` set,
  and not over SSH). Otherwise, or with `--device`, the device flow. The server does OIDC or
  local-password login behind it (§06).
- `--token-stdin`: stores a service-account token read from stdin (kind `service`, no refresh).
- Refresh: before any server call, if `expires` is less than 60 s away, call `POST /oauth/token` with `grant_type=refresh_token`
  once; failure → `BOB-E-4008`.
- After login: `AuthService/WhoAmI`, write `nix.conf`/netrc (R-DEV-004), and set `defaultServer` if it
  was unset. The first server is also added as registry `default`.
- `bob logout [<url>]`: `POST /oauth/revoke` (best effort), delete the credentials entry, the
  netrc line and the substituter.

#### R-CLI-003 Login
- **Statement:** `bob login` gets a server token by PKCE or device flow, stores it per user
  (0600) and refreshes it automatically.
- **Acceptance:** Against the test server: the device flow completes when the harness approves
  the code via the server API, and `credentials.json` has mode 0600. With the access token set to
  expire in 30 s, `bob build //...` (server substituter configured) calls Refresh exactly once,
  then succeeds. With Refresh answering 401 → exit 4, `BOB-E-4008`. The PKCE flow with a fake
  browser (`BROWSER=` script that GETs the URL and follows the redirect) completes, and the token
  request carries `code_verifier` with an S256 challenge.
- **Source:** R-CLI-003, R-AUTH-007.

### Install references

```
<ref>      = [<registry>:]<project>/<release>[@<version>|@latest]
<version>  = semver of the release (§06), or "latest" (default)
```

Examples: `bob install acme/cli`, `bob install corp:infra/deploytool@1.4.2`.

```
$ bob install acme/cli@1.4.2
installed acme/cli 1.4.2 (generation 7): acme, acme-migrate → ~/.local/bin
$ bob list
acme/cli     1.4.2   default   acme, acme-migrate
infra/dt     0.9.0   corp      dt
$ bob upgrade
acme/cli  1.4.2 → 1.5.0
bob: 1 upgraded (generation 8)
$ bob rollback
generation 8 → 7: acme/cli 1.5.0 → 1.4.2
```

Install steps:
1. `ReleaseService/ResolveRelease{project_id, name, constraint, platform}` on the registry → the release's closure
   artifact (store paths) and entry binaries (§04).
2. Public releases are fetched without a token; private ones need the server's credentials
   (R-INST-003). Missing or denied → `BOB-E-4008`/`4010`.
3. Substitute the closure through the server's `/nix` cache (download proxy, D61), with the
   registry's pinned public keys. A path without a valid signature → `BOB-E-4004`.
4. Write generation n+1 (previous entries + this one), add the GC root, flip `current`
   atomically.
5. Write shims for each entry binary into `binDir` (default `~/.local/bin`):
   ```sh
   #!/bin/sh
   # Code generated by bob install (acme/cli 1.4.2). DO NOT EDIT.
   exec "${BOB:-/home/u/.local/bin/bob}" exec --installed acme/cli -- acme "$@"
   ```
   `bob exec --installed <name>` reads `current`, enters the namespace and runs the binary with
   the caller's environment.
6. A binary name already provided by another entry or by an existing non-bob file in `binDir` →
   `BOB-E-0004` (`acme is already provided by infra/dt; use --as`), and nothing changes.
   `--as <name>` installs under another entry name and prefixes the shims (`<as>-<bin>`).
7. If `binDir` is not on `PATH`, print one hint line with the exact rc line.

`uninstall <name>` writes a new generation without the entry and removes its shims. `upgrade
[names…]` resolves `@latest` (or the pinned major with `--minor-only` **[proposal]**) for each
entry and installs all changes as one generation. `rollback [--to N]` flips `current` to the
previous (or N-th) generation and rewrites the shims. `list --available <project>` lists the
registry's releases. Generations beyond the last 5 lose their GC root (and `bob gc` may then
collect them).

#### R-INST-001 Install, upgrade, rollback, uninstall, list
- **Statement:** As above; every change is one atomic generation switch. `install` picks the
  release's `closure` artifacts for the host platform (§04 §4.8 manifest) and substitutes their
  closures from the registry's `/nix` cache (and cache.nixos.org); `list` shows the entries of
  the current generation. Profile semantics: §04 R-INST-005.
- **Acceptance:** Against the test server with releases `acme/cli` 1.0.0 and 1.1.0: `install
  acme/cli@1.0.0` → `~/.local/bin/acme` prints `1.0.0`. `upgrade` → prints `1.1.0`. `rollback` →
  `1.0.0`. `uninstall acme/cli` → shim gone, `list` empty. Killing bob (SIGKILL) at any step of
  `upgrade` (harness kill points) leaves `current` pointing to a complete generation, and every
  shim works. `bob list --json` matches the current generation file.
- **Source:** R-INST-001, D10, R-NFR-007; merged with §04 §4.9 (integration).

#### R-INST-002 No system Nix, no build knowledge
- **Statement:** `bob install` works for a consumer with only the `bob` binary, on a host
  without `/nix`, and needs no project checkout.
- **Acceptance:** In a container image with no `/nix`, no Go/Nix and a non-root user, the
  `bob` binary alone runs `bob install acme/cli` against a public registry, and `acme --version`
  works from a new login shell. A release without `closure` artifacts is not installable:
  `bob install acme/imgonly@1` → `✗ acme/imgonly@1 has nothing to install (it contains: oci
  image "image"); download its artifacts from <server>/p/acme/releases/imgonly/1`, `BOB-E-0009`, exit 2.
  The installed shim's start overhead is ≤ 15 ms p95 [8c/15G]
  **[proposal; S1 shim 6.8–7.8 ms]**.
- **Source:** R-INST-002, R-NIX-020/021, D51, ADR 0002; merged with §04 §4.9.

#### R-INST-003 Public without auth, private with login
- **Statement:** Public releases install anonymously; private releases need a user login or a
  service-account token (`bob login` or `BOB_TOKEN`), checked with `AuthService/WhoAmI` before Nix
  runs (R-NIX-029) and passed to Nix only through bob's netrc.
- **Acceptance:** With no credentials: a public release installs, and the server's log shows no
  `Authorization` header on its requests; a private release fails with exit 4, `BOB-E-4010` (or
  `4008` with an invalid token) and the hint names `bob login`. After `bob login --token-stdin`
  with a service token that has the read permission, the private release installs. No Nix
  401 warning is ever shown. Server side (§06 §6.8): anonymous `ResolveRelease` on a public
  release → OK and every closure path's narinfo is 200 anonymously; on a private release →
  NotFound; with a download token of a permitted user → OK.
- **Source:** R-INST-003, D10, D58, D61, R-REG-001; merged with §04 §4.9 and §06 §6.8.

#### R-INST-004 Several registries
- **Statement:** `bob registry add|remove|list|default` manages registries (stored in
  `config.json`, §02.8 Files); `<registry>:<project>/<release>@<v>` selects one, no prefix selects
  the default; each generation entry records its registry.
- **Acceptance:** With registries `default` and `corp`, `bob install corp:infra/dt` resolves on
  `corp` only (the `default` server sees no request). `bob registry add` pins the server's public
  keys from `AuthService/GetServerInfo` (`nix.public_keys`, anonymous); a later key change makes installs fail with `BOB-E-4004` until
  `bob registry add --refresh-keys corp` **[proposal: trust on first use; an admin can pre-seed
  keys with `--key`]**. With two harness servers, `bob list` shows the registry column and
  `bob upgrade` updates each entry from its own registry.
- **Source:** R-INST-004, D43; merged with §04 §4.9.

#### R-INST-006 Installs never use project dev state
- **Statement:** Installed programs run with the caller's environment and no project's
  `.bob/env.json`; installing never touches a working tree.
- **Acceptance:** Running an installed shim from inside an activated project (hook active, `bob
  dev` running) gives the same `env` output for the program as from `/`, apart from `PWD`,
  `BOB_PROJECT`, `BOB_ENV_*` and the project's exported outside variables, which bob does not
  remove.
- **Source:** R-INST-002, ADR 0002 Consequences.

---

## 02.9 Completion

`bob completion bash|zsh|fish` prints a script that calls the hidden `bob __complete <words…>`.
Candidates: commands, flags, flag values (shells, editors, formats, `--platform`/`--variant`
from §04), dev shell names, installed entry names, registry names, and **labels**. Labels come
from the last evaluation's label index (`.bob/labels`, written at every evaluation); completion
never evaluates.

#### R-CLI-004 Shell completion including labels
- **Statement:** As above.
- **Acceptance:** In the fixture after one `bob build`, `bob __complete build //li` prints
  `//libs/foo:foo`, `//libs/greet:greet`, … (one per line), each starting with `//li`, in
  ≤ 50 ms p95 [2c/4G] **[proposal]**. Without `.bob/labels`, it prints no labels and exits 0.
  bash, zsh and fish scripts are golden-tested and sourced without errors in each shell.
- **Source:** R-CLI-004.

---

## 02.10 `check`, `lock`, `graph`, `pipeline run`

#### R-CLI-009 `bob check`
- **Statement:** `bob check` reports drift without changing anything: generated native files
  that differ from what bob would write (1011), lock entries that are missing, extra or stale
  (1012), `.bob/`, generated or materialised paths that git does not ignore (1013). `--fix` regenerates the
  files (as `bob gen --force`, with the backup of §03.0.2), adds the ignore entries, and runs
  `bob lock` for 1012. `--lock` on `build`/`test` re-resolves a stale lock section instead of
  failing with 1012 (§03.0.3).
- **Acceptance:** On the fixture: no findings → exit 0, output `bob: no problems`. After a hand
  edit of a generated `go.mod` (`go get` of a new module): exit 3 with 1011 naming `go.mod`.
  After `--fix`, `bob check` exits 0 and the hand edit is gone. Removing bob's block from
  `.git/info/exclude` → 1013. `bob check` changes no file (tree hash before = after).
- **Source:** R-CLI-001, D2, D25, D33.

#### R-CLI-010 `bob lock`
- **Statement:** `bob lock` adds missing pins and removes unused ones, without updating existing
  pins. `--update [names…]` re-resolves the named (or all) entries. `--nixpkgs <rev>` moves the
  nixpkgs pin. The output lists every change (sample in §02.5).
- **Acceptance:** On a locked fixture, `bob lock` is a no-op (file byte-identical, output
  `bob.lock: no changes`). After adding one npm dependency in bob.go: exactly one `+` line and
  one new entry. `--update golang.org/x/net` changes only that module's entries (plus the
  transitive ones it requires, listed). `--offline` with an uncached need → exit 4, `BOB-E-3002`.
- **Source:** R-CLI-001, R-FW-004.

#### R-CLI-011 `bob graph`
- **Statement:** `bob graph` prints the dependency graph of the patterns (text tree, `dot`,
  JSON). `--why A B` prints the shortest dependency path from A to B (`--all`: up to 20 paths).
  `--rdeps` reverses the edges.
- **Acceptance:** On the fixture, `bob graph --why //cmd/api:api nixpkgs:openssl` matches the
  golden in §02.5. Without a path → `bob: //cmd/api:api does not depend on nixpkgs:zlib`, exit 0.
  `--format json` passes the schema and the edge set equals the evaluator's graph.
- **Source:** R-CLI-001.

#### R-CLI-012 `bob pipeline run`
- **Statement:** `bob pipeline run <name>` runs the named `bob.go` pipeline locally, step by step,
  with the same output rules as `build`/`test`; `--remote` submits it to the server (§02.6).
- **Acceptance:** The fixture pipeline `ci` (build `//...`, test `//...`) prints one step header
  per step (`== step build`), then that step's package lines. Its exit code is that of the first
  failing step. An unknown pipeline → exit 2, `BOB-E-0001`.
- **Source:** R-FW-041.

---

## [open] items

1. **Remote source transfer** (§02.6 step 2): snapshot upload (recommended) vs. "commit must be
   pushed". §06 §6.9.7 specifies the upload endpoint and §05 §05.2.4 the `src/` prefix; open only
   as the owner-visible choice of default.
2. **tsserver framing** in the proxy (§02.7.4 rule 9) and VS Code's built-in TypeScript outside
   `bob dev`. Recommendation: v1 documents `bob dev -- code`.
3. **`bob dev -- code`** joining an already running VS Code instance: per-project
   `--user-data-dir`; verify manually.
4. **JetBrains** under `bob dev` (GUI app in the namespace: X11/Wayland sockets are mirrored in
   pivot mode, but this is unverified). Recommendation: manual Stage 3 check.
5. **Extra LSP tools** (`Tool(...).LSP()` in the `DevShell` API, §02.7.0): add only if needed.
6. **Trust model** of the hook: per path (recommended) vs. per content hash (direnv).
7. **Registry key pinning**: trust on first use (recommended) vs. requiring `--key`.
8. **Working-tree symlinks** (`node_modules`, materialised codegen) point to `/nix/store` and
   dangle outside the namespace on hosts without `/nix`. Editors therefore browse them only
   through the LSP. Pointing top-level links to the host store path would fix direct browsing
   only one level deep. Recommendation: keep `/nix/store` in v1; §03 decides.
9. **`bob test --rerun`** mapping to Nix `--rebuild` (check mode compares outputs and may fail on
   non-reproducible tests). Recommendation: implement it as a fresh derivation with a nonce
   input instead, if `--rebuild` proves noisy.
10. **Per-ecosystem dev preparation** (added at integration): §02 expects one hook
    `Prepare(ctx, shell) error` per ecosystem; §03 provides the pieces (`engine.Ecosystem.Native`,
    codegen materialisation §03.6.6, `node_modules` swap §03.4.8, the Vite contract) but no single
    interface. Recommendation: an optional interface in `pkg/engine`,
    `type DevPreparer interface{ PrepareDev(ctx context.Context, req PlanRequest, shell string) (DevPrep, error) }`
    with `DevPrep{Files []GeneratedFile; Tools map[string]string; Servers []DevServerSpec}`,
    implemented by npm and codegen; specify in Stage 3 Phase 3.1.
11. **First `bob lock` in a new repository** (added at integration): the repo root is the
    nearest directory with `bob.lock` (§01 §1.2), so a repo without one has no root.
    Recommendation: `bob lock` (only it) treats the nearest ancestor with a `.git` directory as
    the root when no `bob.lock` exists and creates `bob.lock` there.

## Escalations

None. Everything above stays within D51/D59 and ADR 0002 Decision 5. The LSP proxy is the
mechanism the owner asked for in the Round 1 answer to item 8.

# §03 — Ecosystems: C/C++, Go, Rust, npm/TS, OCaml, code generators


Scope: the public API of `pkg/cc`, `pkg/golang`, `pkg/rust`, `pkg/npm`, `pkg/ocaml`, `pkg/proto`,
`pkg/connect`, `pkg/graphql`, `pkg/openapi`; derivation layout per ecosystem; resolver rules;
`bob.lock` entry schemas; generated tooling files and drift rules; quirks tables; materialisation
of generated code; ecosystem budgets.
ADRs: 0004 (Go), 0005 (Rust), 0006 (npm), 0007 (OCaml), 0008 (codegen + materialisation), all
`Accepted (locked at Gate S)` with owner amendments.

## Interfaces

Expected from other sections (names are what this section uses; the coordinator reconciles):

- **§01 provides**
  - `bob.P` (evaluation context for one platform × variant), `bob.Define[T]`, labels
    (`bob.Label`), source positions, `bob.Sources` + `bob.Glob(patterns ...string) bob.Sources` +
    `bob.Files(paths ...string) bob.Sources`.
  - `bob.Linkable` + `bob.LinkInfo` (defined in core so that `nixpkgs` and every ecosystem can
    implement it without import cycles; `cc.Linkable` is an alias, §03.1.1).
  - `nixpkgs.Lib(p, attr, ...nixpkgs.Option)` (target-set library, implements `bob.Linkable`),
    `nixpkgs.Tool(p, attr)` (host-set tool), `*nixpkgs.Package` (struct, §01 §1.7), the nixpkgs
    index and pre-checks (attr exists, broken/insecure/unfree/platform).
  - The `bob.lock` container: a versioned JSON document with one top-level section per
    ecosystem (`"go"`, `"rust"`, `"npm"`, `"opam"`, `"cc"`), read/written through
    `lock.File`; the entry types `lock.GoModule`, `lock.Crate`, `lock.CrateDep`,
    `lock.NpmImporter`, `lock.NpmPackage`, `lock.OpamRepository`, `lock.OpamPackage`,
    `lock.CExternal` are **specified here** (§03.7) and live in `pkg/engine/lock` (§01 §1.8).
  - The derivation layer: `drv.Derivation`, `drv.Name(parts ...string) string` (store-name
    sanitiser: allowed chars `[A-Za-z0-9+._?=-]`, `/`→`_`, `@`→`-`, other → `-`, ≤ 211 chars),
    fixed-output store paths (`drv.FixedOutputPath`), the source import (`bob` fetches with
    `engine/fetch.Fetcher` and imports with `nix store add`/`nix-store --import`; Nix never runs
    an ecosystem fetcher, S13, R-NIX-008), fixup (strip, reference scrubbing, §04 budgets).
  - The `bob-builder` contract: one static Go binary, one JSON job in env `bobSpec`, job kinds
    registered by ecosystems (§03.0.4 lists the kinds this section needs). Success = exit 0
    **and** every output path exists (S15: the builtin `/bin/sh` has no tools).
  - `engine.Ecosystem` (registration interface in `pkg/engine`, §03.0.1 = §01 R-FW-051) and the
    engine that calls it.
- **§02 provides** `bob lock`, `bob gen [--check|--force]`, `bob build`, `bob test`, `bob dev`,
  `bob status`; the error taxonomy and message templates (names used here → §02 codes:
  `E-DRIFT` = BOB-E-1011, `E-LOCK-STALE` = 1012, `E-RESOLVE` = 1015, `E-FETCH-HASH` = 3001,
  `E-GEN-CONFLICT` = 1016, `E-SCRIPT-DENIED` = 2013, `E-PHANTOM-DEP` = 2014,
  `E-QUIRK-TEST` = 9006; §02.3 "Cross-section code assignment"); `.bob/bin` shims, `bob env`, editor settings and the LSP path-rewriting proxy
  (all generated tooling files below reference real `/nix/store/…` paths; tools run through
  shims inside bob's namespace, D51/D59).
- **§04 provides** variant projection rules per package kind (which of platform / profile /
  sanitiser / features enter each derivation of this section), cross toolchain selection,
  `bob.Platform`, `bob.Variant`, and the `Features` variant axis for Rust.
- **§03 provides** to §01/§02/§04: the `Ecosystem` implementations, `lock.*` entry types,
  `GeneratedFile` lists per ecosystem, the drift checker inputs, `TestResult` (§03.0.5),
  quirks tables, the codegen manifest (`.bob/gen/manifest.json`), dev-server contracts.

## §03.0 Rules common to all ecosystems

### §03.0.1 Ecosystem registration (request to §01)

Specified as `engine.Ecosystem` in `pkg/engine` (§01 R-FW-051, with the request/result types);
it is never linked into the evaluator. Registered from `pkg/<eco>/gen` with `engine.Register`.

```go
// Ecosystem is implemented once per pkg/<eco> and registered from that package's init.
// Adding an ecosystem is a self-contained package under pkg/ (REQUIREMENTS §7.7).
type Ecosystem interface {
	// Name is the bob.lock section key ("go", "rust", "npm", "opam", "cc").
	Name() string
	// Lock resolves the declared external dependencies of all packages of this ecosystem
	// in the graph and returns the new lock section. Network allowed. Called by `bob lock`
	// and by `bob build`/`bob dev` when the section is stale (§03.0.3).
	Lock(ctx context.Context, req LockRequest) (json.RawMessage, error)
	// Plan turns the ecosystem's packages into derivations. No network, no process
	// execution except reading the store; deterministic (R-FW-008).
	Plan(ctx context.Context, req PlanRequest) (PlanResult, error)
	// Native returns the tooling files (go.mod, Cargo.toml, …) for the working tree.
	Native(ctx context.Context, req PlanRequest) ([]GeneratedFile, error)
}

// GeneratedFile is one tooling file bob writes into the working tree (R-FW-006).
type GeneratedFile struct {
	Path    string      // repo-relative, clean
	Content []byte
	Mode    fs.FileMode // always 0o444
	Owner   bob.Label   // package whose definition produced it (for drift messages)
	Explain func(onDisk []byte) []DriftLine // maps a byte diff to bob.go terms (§03.0.2)
}
```

### §03.0.2 Generated tooling files and drift

#### R-FW-060 Generated native files are written read-only and tracked
- **Statement:** Every file listed in §03.0.2 table is written by bob with mode 0444, its
  sha256 is recorded in `.bob/state/native.json`, and it starts with the header given in the
  table where the format allows comments.
- **Acceptance:** Given a fixture repo per ecosystem, when `bob gen` runs, then every file of
  the table exists with mode 0444, its content equals the golden, and `.bob/state/native.json`
  holds `{"version":1,"files":{"<path>":{"sha256":"<hex>","owner":"<label>"}}}` for each.
- **Source:** R-FW-006, D2, ADR 0004–0008.

| Ecosystem | File (repo-relative) | Header | Committed? |
|---|---|---|---|
| Go | `<module dir>/go.mod`, `<module dir>/go.sum` | `// Code generated by bob from bob.go. DO NOT EDIT.` + `// Edit <module dir>/bob.go and run bob lock.` (go.mod only; go.sum has no comments) | **yes** (Go module consumers and `go install` need them, R-FW-022) |
| Go | `<dir>/zz_bob_cgo.go` (ADR 0003 R5) | `//go:build bob && cgo` | no |
| C/C++ | `compile_commands.json` (repo root) | none (JSON) | no |
| Rust | `rust-project.json`, `Cargo.toml` (root workspace), `<crate dir>/Cargo.toml`, `Cargo.lock`, `.cargo/config.toml`, `.bob/vendor/` | `# generated by bob from bob.go — do not edit` (TOML files) | no |
| npm | `<importer dir>/package.json`, `<importer dir>/tsconfig.json`, `<importer dir>/node_modules/` (directory of symlinks), `.bob/vite.bob.config.mjs` | package.json: first key `"//": "generated by bob from bob.go — do not edit"`; tsconfig.json: `// generated by bob from bob.go — do not edit` (JSONC) | no |
| OCaml | `<dir>/.merlin`; opt-in: `dune-project`, `<dir>/dune`, `<lib>.opam` | `# generated by bob from bob.go — do not edit` (`dune-project`: header on line 2, `(lang dune 3.21)` must be line 1) | no |
| codegen | owned directories (§03.6) | per generator | no (`.gitignore` with `*`) |

Not-committed files are kept out of git by a bob-managed block in `.git/info/exclude`
(`# >>> bob generated files (do not edit) >>>` … `# <<< bob <<<`); bob never edits a
user-owned `.gitignore`. **[open]** Alternative: require users to commit a `.gitignore` entry;
recommendation: `.git/info/exclude` (no user file touched, local to the clone).

#### R-FW-061 Drift is detected before every build and rejected
- **Statement:** Before `bob build`, `bob test`, `bob dev` and `bob gen`, bob regenerates every
  file of the table in memory and compares bytes with the working tree; any difference fails
  with `E-DRIFT` (BOB-E-1011) listing each difference in bob.go terms; `bob gen --force` overwrites (backup
  in `.bob/gen/backup/<UTC time>/`).
- **Acceptance:** Given the Go fixture, when `go get golang.org/x/text@v0.20.0` is simulated by
  editing go.mod/go.sum (chmod + write), then `bob build //...` exits 3 (BOB-E-1011) before any Nix call,
  prints exactly the template below (golden), and leaves the files unchanged; `bob gen --force`
  restores them byte-identical and moves the edited copies to the backup dir. Same pattern per
  ecosystem with one golden each (Cargo.toml dep added, package.json dep added, tsconfig option
  changed, `.merlin` edited).
- **Source:** R-FW-006, R-GO-003, D2, S2 §5.4.

```
✗ api/go.mod or go.sum was changed outside bob (they are generated from bob.go)
    ~ go 1.26.0 → 1.25.0 (set Go in api/bob.go)
    ~ golang.org/x/sync v0.24.0 → v0.25.0  (api/bob.go declares v0.24.0)
    + golang.org/x/text v0.20.0  (not declared in bob.go)
  hint: declare Go modules in bob.go (golang.Require("golang.org/x/text", "v0.20.0")) and run `bob lock`;
        `go get` and `go mod tidy` are not used with bob. To discard the edit: bob gen --force
```

A missing generated file is not drift: bob rewrites it silently.

### §03.0.3 Lock freshness

#### R-FW-062 Each ecosystem section records the declaration it was resolved from
- **Statement:** Every lock section stores `inputs`: the sha256 of the canonical JSON of the
  ecosystem's declared external requirements (names, version requirements, options that affect
  resolution). If it differs from the current declarations, `bob build`/`bob test` fail with
  `E-LOCK-STALE` (BOB-E-1012, "run bob lock") unless `--lock` is given; `bob dev` and `bob lock` re-resolve.
- **Acceptance:** Given a locked fixture, when a requirement is added in bob.go and `bob build`
  runs, then it exits 3 with `E-LOCK-STALE` (BOB-E-1012) naming the ecosystem and the bob.go position of the
  changed declaration, and makes no network request (harness: network namespace without
  routes); `bob lock` then succeeds and a second `bob lock` without changes makes no registry
  request when caches are warm and `--refresh` is not given, and rewrites nothing.
- **Source:** R-FW-004, R-FW-005, R-NIX-011, R-DEV-006.

### §03.0.4 Fetching and bob-builder job kinds

- Every external source is a **content-addressed store path imported by bob** whose hash comes
  from bob.lock. bob's fetcher (§01 R-NIX-014 `engine/fetch.Fetcher`) downloads, verifies the
  registry checksum **and** the lock hash, and imports the result (`nix store add` /
  `nix-store --import`, §01 R-NIX-009) at exactly the path a fixed-output derivation with the
  same name and hash would have (path identity, §01 R-NIX-008). bob generates no fixed-output
  derivation for these sources, so Nix never executes an ecosystem fetcher (S13: FODs get host
  network; workers never build user FODs). "FOD" in R-GO-002, R-NPM-002 and R-CC-005 is met by
  this path identity.

#### R-FW-063 Ecosystem fetches never run as Nix builds
- **Statement:** For all ecosystem sources (Go modules, crates, npm tarballs, opam sources and
  repository snapshot, `cc.External` sources), the output is realised by bob before `nix build`.
- **Acceptance:** Given each fixture with an empty store and network, when `bob build //...`
  runs, then no generated derivation of this section has `outputHash`, the Nix internal-json log
  contains no `build` activity for a source path, and every source path equals
  `drv.FixedOutputPath` for (name, hash mode, lock hash) and the path of `nix store add` of the
  same tree (flat: `--mode flat`).
- **Source:** R-FW-021, S13, ADR 0001.

bob-builder job kinds needed by this section (names are the `kind` field of `bobSpec`):

| Kind | Ecosystem | Purpose |
|---|---|---|
| `cc-compile-link` | C/C++ | compile all sources of one library/binary, archive or link |
| `cc-external` | C/C++ | configure/build/install a CMake/autotools/Meson/Make project |
| `go-std`, `go-tools` | Go | std library (`go list -export -deps std`) / `cmd/pack` |
| `go-compile`, `go-cgo`, `go-link`, `go-testmain`, `test-run` | Go | §03.2.3 |
| `rustc`, `rust-build-run` | Rust | §03.3.3 |
| `npm-unpack`, `npm-script`, `npm-node-modules`, `npm-tool` | npm | §03.4.3 |
| `opam-build`, `ml-compile`, `ml-link` | OCaml | §03.5.3 |
| `codegen` | codegen | run one generator with a bob-written config |
| `test-run` | all | run a test binary, write `TestResult` |

### §03.0.5 Tests are packages (all ecosystems)

Each test constructor yields one **test derivation per test unit** (Go: package; Rust: crate
test target; npm: test file set of one importer; OCaml: test executable; C/C++: test binary).
The run derivation (`test-run` job) executes the test binary with `HOME=$TMPDIR`, no network
(loopback allowed for `bob.Services`, R-FW-025), and writes `$out/test-result.json`:

```go
// TestResult is the output of every test-run derivation (schema version 1).
type TestResult struct {
	Version  int          `json:"version"`  // 1
	Label    string       `json:"label"`    // //pkg/foo:foo_test
	Passed   bool         `json:"passed"`
	Duration float64      `json:"durationSeconds"`
	Cases    []TestCase   `json:"cases"`    // empty if the framework gives no per-case data
}
type TestCase struct {
	Name     string  `json:"name"`
	Result   string  `json:"result"` // "pass" | "fail" | "skip"
	Duration float64 `json:"durationSeconds"`
	Output   string  `json:"output,omitempty"` // failing cases only, ≤ 64 KiB each
}
```

A failing test makes the derivation fail after writing the result to the build log (so
failures are never cached); passing results are cached and substitutable (R-FW-024).

Every test unit has a timeout: 10 minutes by default; a test constructor may set another
(`timeoutSeconds` of the `test-run` job, 0 = the default; ecosystems expose it as a
`Timeout time.Duration` field of their test options). A test still running at its timeout is
stopped and fails with BOB-E-2007 `<label> timed out after <n>s` (its result goes to the build
log, nothing is cached). [A92]

#### R-FW-064 Test results are cached per test unit
- **Acceptance:** Given the polyglot fixture, when `bob test //...` runs twice, then the second
  run builds 0 derivations, exits 0 and prints the cached result per unit; after editing one Go
  test file only that package's test derivation (compile + run) rebuilds.
- **Source:** R-FW-024.

### §03.0.6 Quirks and mapping tables (common admission rule)

bob ships three small Go tables: npm quirks (§03.4.5), Rust native-crate defaults (§03.3.6),
OCaml `conf-*` mapping (§03.5.5). Each entry has `Why` (justification, shown with `-v`) and a
`Test` run in bob's CI. A CI check fails if an entry lacks either, and a release note lists the
table size.

---

## §03.1 C / C++ (`pkg/cc`)

No spike measured C/C++ alone; the design follows backend B (gen-b §2, §7–8) and S7 (cross,
variants). Compilers are the nixpkgs cc-wrapper (`stdenv.cc`, gcc 15.3 at the pin), called
directly by bob-builder; no stdenv, no setup hooks. The wrapper adds RUNPATH for each `-L` store
directory (gen-b §2), so outputs run under `env -i`.

### §03.1.1 API

```go
package cc // github.com/megakuul/bob/pkg/cc

// Linkable is anything a C/C++/cgo/Rust/OCaml consumer can link: bob C/C++ libraries,
// Rust StaticLib/CDylib, cc.External, nixpkgs.Lib. Alias of the core type (§01).
type Linkable = bob.Linkable

// Std is the language standard (-std=…).
type Std string

const (
	C11 Std = "c11"; C17 Std = "c17"; C23 Std = "c23"
	CXX17 Std = "c++17"; CXX20 Std = "c++20"; CXX23 Std = "c++23"
)

// LibraryOpts configures StaticLibrary and SharedLibrary.
type LibraryOpts struct {
	Srcs     bob.Sources         // .c .cc .cpp .cxx .S; required
	Hdrs     string              // public include dir, relative to the bob.go dir ("include"); "" = none
	Private  bob.Sources         // private headers (not exported to consumers)
	Std      Std                 // default C17 for .c, CXX20 for C++ sources
	Defines  map[string]string   // -DKEY=VALUE ("" value = -DKEY)
	Flags    []string            // extra compile flags (validated: no -o, -I/nix/store, -fplugin)
	LinkOpts []string            // extra link flags (shared libs and binaries only)
	Deps     []Linkable          // public deps: their headers propagate to consumers
	PrivDeps []Linkable          // private deps: linked, headers not propagated
	Platforms []bob.Platform     // restrict; default all (§04)
}

// StaticLibrary builds lib<name>.a. Label //<dir>:<name>.
//
//	var Foo = bob.Define(func(p *bob.P) *cc.Library {
//		return cc.StaticLibrary(p, "foo", cc.LibraryOpts{Srcs: bob.Glob("src/*.cpp"), Hdrs: "include"})
//	})
func StaticLibrary(p *bob.P, name string, o LibraryOpts) *Library

// SharedLibrary builds lib<name>.so (soname lib<name>.so).
//
//	var Foo = bob.Define(func(p *bob.P) *cc.Library {
//		return cc.SharedLibrary(p, "foo", cc.LibraryOpts{
//			Srcs: bob.Glob("src/*.cpp"), Hdrs: "include", Std: cc.CXX20,
//			Deps: []cc.Linkable{nixpkgs.Lib(p, "openssl")},
//		})
//	})
func SharedLibrary(p *bob.P, name string, o LibraryOpts) *Library

// BinaryOpts configures Binary and Test.
type BinaryOpts struct {
	Srcs     bob.Sources
	Std      Std
	Defines  map[string]string
	Flags    []string
	LinkOpts []string
	Deps     []Linkable
	Platforms []bob.Platform
}

// Binary builds bin/<name>.
//
//	var Tool = bob.Define(func(p *bob.P) *cc.Binary {
//		return cc.Binary(p, "tool", cc.BinaryOpts{Srcs: bob.Glob("*.c"), Deps: []cc.Linkable{foo.Foo(p)}})
//	})
func Binary(p *bob.P, name string, o BinaryOpts) *Binary

// Test builds a binary and runs it in a test-run derivation; exit 0 = pass. Output is parsed
// as TAP if it starts with "TAP version", else one case named <name>.
//
//	var FooTest = bob.Define(func(p *bob.P) *cc.Test {
//		return cc.Test(p, "foo_test", cc.BinaryOpts{Srcs: bob.Files("foo_test.cpp"), Deps: []cc.Linkable{Foo(p)}})
//	})
func Test(p *bob.P, name string, o BinaryOpts) *Test

// Library implements Linkable. Shared libraries also expose the .so for bundling.
type Library struct{ /* unexported */ }
func (l *Library) LinkInfo() bob.LinkInfo
```

`bob.LinkInfo` (requested from §01) carries: include dirs (store paths), link dirs, link
names (`-l`), static archives (absolute paths), extra link args, runtime dirs (RUNPATH),
`pkg-config` dirs, and the transitive public set; consumers use it uniformly (cgo `CDeps`,
Rust `Native`, OCaml `Native`). `nixpkgs.Lib(p, "openssl")` links all pkg-config modules
of the package's dev output by default; `nixpkgs.Lib(p, "openssl", nixpkgs.Modules("libcrypto"))`
narrows (gen-b §8 IR gap).

### §03.1.2 `cc.External` (R-CC-005)

```go
// BuildSystem selects how an External is built inside its derivation.
type BuildSystem int

const (
	CMake BuildSystem = iota + 1 // cmake -G Ninja -DCMAKE_INSTALL_PREFIX=$out -DCMAKE_BUILD_TYPE=<profile>
	Autotools                    // ./configure --prefix=$out (autoreconf -fi if no configure)
	Meson                        // meson setup --prefix=$out --buildtype=<profile>; ninja
	Make                         // make PREFIX=$out install
)

// ExternalOpts describes a third-party C/C++ project with its own build system.
type ExternalOpts struct {
	URL     string        // https archive (.tar.gz .tar.xz .tar.bz2 .tar.zst .zip); required
	Strip   int           // leading path components to strip; default 1
	Build   BuildSystem   // required
	Args    []string      // extra configure args (-DFOO=ON, --disable-x, -Dfoo=false)
	Deps    []Linkable    // available to the build (CMAKE_PREFIX_PATH, PKG_CONFIG_PATH, CFLAGS/LDFLAGS)
	Tools   []*nixpkgs.Package // extra host tools (nixpkgs.Tool(p, "perl"))
	Libs    []string      // link names exported to consumers; default: from installed .pc files, else all lib*.so/lib*.a
	Include string        // include dir under $out; default "include"
}

// External fetches URL (pinned by narHash in bob.lock) and builds it with its own build
// system inside one derivation. To consumers it is an ordinary Linkable.
//
//	var Zstd = bob.Define(func(p *bob.P) *cc.External {
//		return cc.External(p, "zstd", cc.ExternalOpts{
//			URL:   "https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz",
//			Build: cc.CMake, Args: []string{"-S", "build/cmake", "-DZSTD_BUILD_PROGRAMS=OFF"},
//		})
//	})
func External(p *bob.P, name string, o ExternalOpts) *ExternalLib
```

Lock entry `lock.CExternal` (§03.7.6), keyed by label. Host tools come from nixpkgs (`cmake`,
`ninja`, `meson`, `autoconf`, `automake`, `libtool`, `gnumake`, `pkg-config`) and are inputs
only for the selected build system.

#### R-CC-005a cc.External sources are pinned and verified before building
- **Statement:** `bob lock` downloads the URL, unpacks it with `Strip`, normalises modes, and
  records `narHash`; `bob build` refuses an External whose URL is not in bob.lock or whose
  lock entry URL differs, and the fetcher refuses content whose NAR hash differs.
- **Acceptance:** Given the cc fixture with a zstd External, when (a) the entry is removed from
  bob.lock, then `bob build` exits 3 with BOB-E-1009 (no pinned entry) before Nix runs; when (b) the served
  archive is replaced by different bytes (harness HTTP server), then the build exits 4 with
  `E-FETCH-HASH` (BOB-E-3001) naming the label, URL, expected and actual hash; when (c) unchanged, a C
  consumer linking `Zstd(p)` builds and runs under `env -i`.
- **Source:** R-CC-005, D44, R-NIX-011.

### §03.1.3 Derivation layout

| Derivation | Name | Job | Inputs | Output |
|---|---|---|---|---|
| sources | `cc-src-<label>` | source import | the package's Srcs + Private + Hdrs files | store path with the files at bob.go-relative paths |
| library / binary | `cc-<name>[-<projection>]` | `cc-compile-link` | sources, cc-wrapper, binutils, deps' `LinkInfo` paths | `lib/lib<name>.{a,so}` + `include/` (copy of Hdrs) / `bin/<name>` |
| external source | `ccx-src-<name>-<narhash8>` | bob-imported source path (FOD-identical, recursive sha256) | — | unpacked tree |
| external build | `ccx-<name>[-<projection>]` | `cc-external` | source, build-system tools, deps | install tree (`lib`, `include`, `lib/pkgconfig`, `bin`) |
| test run | `cc-<name>-run` | `test-run` | test binary | `test-result.json` |

- Compile flags (fixed order): `-std=<Std>`, profile flags from §04 (release `-O2`, debug
  `-O0 -g`), sanitiser flags from §04, `-fPIC` for shared libs and for static libs consumed by
  shared outputs (default on), `-ffile-prefix-map=<src store path>=<label dir>`, Defines
  sorted by key, `-I` own Private/Hdrs, then `-I` of deps' include dirs in Deps order then
  transitive order, then Flags. Objects are compiled in parallel inside the derivation
  (`NIX_BUILD_CORES`).
- Shared libraries are linked with `-Wl,-soname,lib<name>.so`; RUNPATH comes from the wrapper.
- Granularity: one derivation per library/binary in v1. **R-CC-004** (per-object derivations):
  **[open]**, recommendation: keep per-library in v1 and measure per-object on a 200-file C++
  library in Stage 3 (adopt if one-file change is ≥ 2× faster on [2c/4G] and cold cost ≤ 1.3×);
  the `cc-compile-link` job already records per-object commands so a split stays local.

#### R-CC-001a C/C++ libraries, binaries and tests build hermetically
- **Acceptance:** Given `tests/fixtures/repos/cc` (static lib, shared lib using nixpkgs
  openssl, binary linking both, a test, an External), when `bob build //... && bob test //...`
  runs for linux-arm64 and linux-amd64 (§04 cross), then all succeed, the binary runs under
  `env -i` (native or qemu-user), and a rebuild on a second store is bit-identical.
- **Source:** R-CC-001, R-NFR-006.

#### R-CC-002a Typed options map to exact flags
- **Acceptance:** Golden test of the `bobSpec` job of each fixture package (flag order above);
  passing `Flags: []string{"-I/nix/store/x"}` fails at evaluation with
  `✗ //cc/foo:foo: flag "-I/nix/store/x" is not allowed; use Deps to add include paths`.
- **Source:** R-CC-002, R-NIX-013.

### §03.1.4 `compile_commands.json` (R-CC-003)

One file at the repo root, an array sorted by `file`, one entry per source file of every
C/C++ package and of every cgo package (its C files)
for the **host** platform and default variant:

```json
[{"directory": "/home/me/repo",
  "file": "libs/foo/src/foo.cpp",
  "arguments": ["/nix/store/…-gcc-wrapper-15.3.0/bin/g++", "-std=c++20", "-O2", "-fPIC",
                "-Ilibs/foo/include", "-I/nix/store/…-openssl-3.6.1-dev/include",
                "-c", "libs/foo/src/foo.cpp", "-o", "/dev/null"],
  "output": "/dev/null"}]
```

Own sources and own headers point to the working tree (repo-relative to `directory`);
dependency include dirs point to the store. clangd is started through `.bob/bin/clangd`
(§02), so the store paths resolve.

#### R-CC-003a compile_commands.json lets clangd resolve every include
- **Acceptance:** Given the cc fixture after `bob gen`, when `.bob/bin/clangd --check=libs/foo/src/foo.cpp`
  runs as a non-root user on a host without `/nix`, then it reports 0 errors and resolves
  `<openssl/ssl.h>`; the file equals the golden after replacing `directory`.
- **Source:** R-CC-003, D59.

---

## §03.2 Go (`pkg/golang`) — ADR 0004

### §03.2.1 API

```go
package golang // github.com/megakuul/bob/pkg/golang ("go" is a keyword)

// ModuleOpts describes one Go module; its go.mod/go.sum are generated from it (R-GO-003).
type ModuleOpts struct {
	Path      string          // module path, required ("example.com/api")
	Go        string          // "1.26": go directive (written "1.26.0") and toolchain minor, required (R-GO-004)
	Toolchain *nixpkgs.Package // default nixpkgs.Tool(p, "go_1_26"); must report version 1.26.x
	Require   []Requirement   // direct third-party requirements, exact versions (R-GO-002)
	Generated []Generated     // codegen outputs materialised into this module (§03.6)
	Tags      []string        // build tags for every package of the module
}

// Requirement is one direct module requirement.
type Requirement struct{ Path, Version string }

// Require returns a requirement; version must be a canonical semver ("v1.6.0") or a
// pseudo-version; ranges and "latest" are rejected at evaluation.
func Require(path, version string) Requirement

// NewModule declares the module whose root is the directory of the calling bob.go.
// Library packages need no declaration: bob follows the imports of binaries and tests.
// Label //<dir>:module.
//
//	var Module = bob.Define(func(p *bob.P) *golang.Module {
//		return golang.NewModule(p, golang.ModuleOpts{
//			Path: "example.com/api", Go: "1.26",
//			Require: []golang.Requirement{golang.Require("github.com/google/uuid", "v1.6.0")},
//		})
//	})
func NewModule(p *bob.P, o ModuleOpts) *Module

// BinaryOpts configures a main package.
type BinaryOpts struct {
	Name    string            // default: last element of dir
	Tags    []string          // added to the module's tags
	X       map[string]string // -ldflags -X importpath.name=value (sorted)
	CGo     CGo               // CGoAuto (default: on iff a package in the closure imports "C"), CGoOff
}

// CGo selects cgo for a binary's closure.
type CGo int

const (
	CGoAuto CGo = iota // on iff a package in the closure imports "C"
	CGoOff             // pure Go; importing "C" is an evaluation error
)

// Binary declares the main package in dir (module-relative, "./cmd/api").
// Label //<module dir>/<dir>:<Name>.
//
//	var API = bob.Define(func(p *bob.P) *golang.Binary {
//		return Module(p).Binary("./cmd/api", golang.BinaryOpts{})
//	})
func (m *Module) Binary(dir string, o BinaryOpts) *Binary

// CgoOpts gives a cgo package its typed C/C++/Rust dependencies (R-GO-001, R-RS-001).
type CgoOpts struct {
	Deps []cc.Linkable // -I → CGO_CFLAGS of this package; link info → final external link
}

// Cgo attaches C dependencies to the cgo package in dir. Packages that import "C" without
// a Cgo declaration may use only libc and their own C files.
//
//	var _ = bob.Define(func(p *bob.P) *golang.Package {
//		return Module(p).Cgo("./internal/ffi", golang.CgoOpts{Deps: []cc.Linkable{foo.Lib(p), rcore.Crate(p)}})
//	})
func (m *Module) Cgo(dir string, o CgoOpts) *Package

// Tests declares one test unit per package matching pattern ("./..." or "./pkg/x") that
// has _test.go files. Label //<module dir>:<name> ("tests" by default), units //<pkg dir>:go_test.
//
//	var Tests = bob.Define(func(p *bob.P) *golang.TestSet { return Module(p).Tests("./...", golang.TestOpts{}) })
func (m *Module) Tests(pattern string, o TestOpts) *TestSet

type TestOpts struct {
	Name     string
	Tags     []string
	Services []bob.Service // R-FW-025 (§01)
	Args     []string      // passed to the test binary (-test.run=…)
}

// CArchive / CShared build -buildmode=c-archive / c-shared of the main package in dir;
// the result implements cc.Linkable.
func (m *Module) CArchive(dir string, o BinaryOpts) *CLib
func (m *Module) CShared(dir string, o BinaryOpts) *CLib
```

One constructor per kind (R-FW-014); granularity is not user-configurable.

#### R-GO-001a Go binaries, cgo, tests and c-archives build per package
- **Acceptance:** Given `tests/fixtures/repos/go` (3 modules, a cgo package linking a cc shared
  library and a Rust staticlib, a protobuf codegen package, embeds, a test per package, an
  assembly package, a `.syso`), when `bob build //... && bob test //...` runs natively and
  cross (§04), then all succeed, outputs run under `env -i`, and every Go derivation rebuilds
  bit-identically on a second store.
- **Source:** R-GO-001, R-NFR-006, S2 §3.1.

### §03.2.2 Planning (evaluation time)

1. bob reads Go sources in-process (`go/build` contexts for the target GOOS/GOARCH, module tags,
   cgo on/off; `bob.go` excluded by the `bob` tag) and resolves imports itself: std (from the
   toolchain's `go list -export -deps std` index cached per toolchain), workspace module
   packages, generated packages (§03.6, source = codegen output path), third-party modules
   from the build list (§03.2.4). No `go list`, no network (S2-1).
2. Third-party module trees are realised before planning (two phases: fetch, then plan; S2-2).
3. Groups: one group per third-party **module** (only packages reachable from workspace
   imports), one per workspace **package**. Module groups that import each other cyclically
   are merged into one group (Tarjan SCC over module-level edges).
4. Unsupported in v1 (evaluation error with hint): `#cgo pkg-config:` (hint: use
   `Cgo(…, CgoOpts{Deps: nixpkgs.Lib(…)})`), `//go:linkname` to unexported std symbols in
   workspace code is allowed (it is a compiler matter), `replace` directives (none exist:
   go.mod is generated), `vendor/` directories (ignored with a note).

### §03.2.3 Derivation layout

| Derivation | Name | Job | Key inputs | Output |
|---|---|---|---|---|
| module source | `gomod-<path>-<version>` (`drv.Name`) | bob-imported source path (FOD-identical), recursive sha256 = lock `tree` | — | unpacked, mode-normalised module tree |
| std | `go-std-<goversion>-<goos>-<goarch>[-cgo][-<projection>]` | `go-std` | toolchain, cc (cgo), tags/flags | `$out/pkg/<importpath>.a` for all std + `$out/importcfg` |
| tools | `go-tools-<goversion>` | `go-tools` | toolchain | `bin/pack` |
| third-party group | `go-<module>-<version>[-<projection>]` | `go-compile` / `go-cgo` | module tree, std, deps' groups | `$out/pkg/<importpath>.a` per used package |
| workspace package | `go-<importpath>[-<projection>]` | `go-compile` / `go-cgo` | that package's files only (own source import), std, deps' archives, cgo: cc + `Cgo` deps | `$out/pkg/<importpath>.a` |
| link | `go-link-<name>[-<projection>]` | `go-link` | all transitive archives, C deps | `bin/<name>` only (no `.a`) |
| test compile+link | `go-test-<importpath>` | `go-testmain` | package + `_test.go` files, test deps | `bin/<pkg>.test` |
| test run | `go-test-<importpath>-run` | `test-run` | test binary, services | `test-result.json` |

Exact compile rules (S2 §5.2): `-p <importpath>`, `-lang=go<module go directive major.minor>`,
`-trimpath "<src>=><module>@<version>/<rel>"` (workspace: `<module>/<rel>`), `-nolocalimports`,
`-complete` when no cgo/asm, `-embedcfg` for embeds, `-importcfg`, `-c=min(cores,4)`;
assembly: `asm -gensymabis` → `compile -symabis` → `asm` → `pack r`; `.syso` appended with
`pack r`. Work directories are `$TMPDIR/b<sha256(importpath)[:12]>` (grouping-independent).
cgo sequence = cmd/go's (`cgo` → gcc objects → `_cgo_.o` → `cgo -dynimport` → compile →
`pack r`); `CGO_CFLAGS` per package = `-I` of its `Cgo` deps (no global cgo flags); `-L/-l`
recorded as cgo ldflags and used by the external link. Link: `-buildid=`, `-extld=<cc>`,
`-buildmode=exe|c-archive|c-shared`, modinfo from bob.lock. Job specs larger than 64 KiB use
`passAsFile` (MAX_ARG_STRLEN). Tests: bob writes `_testmain.go` itself (template per Go
minor version, golden-tested against `go test -c -work` output of the pinned toolchain) and
runs `<pkg>.test -test.v=test2json` piped through `go tool test2json` into `TestResult`.
**[open]** If the testmain template proves fragile across Go minors, fall back to
`go test -c` inside the test-compile derivation with a bob-written GOPROXY; recommendation:
own template (fast, no cmd/go loading).

#### R-GO-002a Granularity is hybrid: per third-party module, per workspace package
- **Statement:** A one-file change in a workspace package rebuilds that package, packages whose
  inputs changed transitively, and links; never a third-party module group or std.
- **Acceptance:** Given the Go fixture, when `cmd/api/main.go` changes, then exactly 2 Go
  derivations rebuild (package + link); when `internal/msg/msg.go` changes, exactly the
  package, its importers in the workspace and the link rebuild (counted from the Nix log);
  synthetic 200-package workspace bottom-layer change rebuilds 36 package derivations + link.
- **Source:** R-GO-002, R-FW-020, S2 §4, quiet log `s02-go.log`.

#### R-GO-005 Early cutoff for Go export data (A11)
- **Statement:** The planner supports a `cutoff` mode where dependents compile against export
  data (`-linkobj` split) that bob adds to the store by content, so a change that keeps a
  package's export data byte-identical rebuilds only that package and the link.
- **Acceptance:** Given syn200 with cutoff enabled, when a bottom-layer function body (not
  inlinable) changes, then 1 package derivation + link rebuild and the binary is bit-identical
  to the non-cutoff build.
- **Default:** **[open]** off in v1; recommendation: enable by default when the Stage 3
  measurement on bob's own repo (20 sampled one-file commits, [2c/4G]) shows a median gain
  ≥ 1.3× and cold cost ≤ 1.4× (quiet log: syn200 bottom-layer 3.03 → 1.76 s, but main-only
  1.22 → 1.74 s and cold 10.2 → 13.9 s [8c/15G]).
- **Source:** A11, S2 §4.4, `s02-go.log`.

### §03.2.4 Resolution (`bob lock`) and the lock entry

- Build list per workspace module = Minimal Version Selection over `.mod` files fetched from
  the configured GOPROXY (default `https://proxy.golang.org`, configurable in bob's user
  config, `direct` not supported in v1), with cmd/go's graph pruning for `go ≥ 1.17`
  modules; includes modules needed by tests of workspace packages; excludes nothing (no
  `exclude`/`retract` handling beyond warning on retracted versions).
- For every module of the pruned graph: `.mod` hash (`modH1`). For modules that provide
  packages to the build: the zip, its `h1:` (dirhash of the zip content), and `tree` (NAR
  sha256 of the unpacked tree, files 0444/0555, dirs 0555).
- Errors: `E-RESOLVE` (BOB-E-1015; `✗ cannot resolve Go modules for example.com/api: golang.org/x/net@v0.99.0: not found on proxy.golang.org`),
  toolchain check (BOB-E-1008; `✗ github.com/foo/bar@v1.2.0 needs go 1.27.0, but example.com/api uses go 1.26 (set Go in api/bob.go)`).

#### R-GO-002b bob's build list equals the go command's
- **Acceptance:** On a corpus of ≥ 10 real modules (incl. the fixture, bob itself,
  kubernetes/client-go, grpc-go), with bob's generated go.mod/go.sum and its GOPROXY dir,
  `go list -m all` (GOFLAGS=-mod=readonly, GOSUMDB=off, GOTOOLCHAIN=local) exits 0 and lists
  exactly bob's build list; `go build ./... && go vet ./...` succeed without network.
- **Source:** R-GO-002, R-GO-003, S2 §3.5.

`lock.GoModule` and the `go` section:

```go
// GoSection is bob.lock's "go" section.
type GoSection struct {
	Inputs     string                 `json:"inputs"`     // §03.0.3
	Proxy      string                 `json:"proxy"`      // "https://proxy.golang.org"
	Workspaces map[string]GoBuildList `json:"workspaces"` // key: workspace module path
	Modules    []GoModule             `json:"modules"`    // sorted by path, then semver
}
type GoBuildList struct {
	Go      string   `json:"go"`      // "1.26.0"
	Modules []string `json:"modules"` // "path@version", sorted; every module of the pruned graph
}
// GoModule is one third-party module version.
type GoModule struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	ModH1    string `json:"modH1"`              // h1: of the .mod file
	H1       string `json:"h1,omitempty"`       // h1: of the zip (only if Packages)
	Tree     string `json:"tree,omitempty"`     // SRI sha256 NAR hash of the unpacked tree (only if Packages)
	Go       string `json:"go,omitempty"`       // the module's own go directive
	Packages bool   `json:"packages"`           // provides packages to some build
}
```

```json
{"go": {"inputs": "sha256-…", "proxy": "https://proxy.golang.org",
  "workspaces": {"example.com/api": {"go": "1.26.0",
     "modules": ["github.com/google/go-cmp@v0.7.0", "golang.org/x/sync@v0.24.0"]}},
  "modules": [
   {"path": "github.com/google/go-cmp", "version": "v0.7.0",
    "modH1": "h1:pXiqmnSA92OHEEa9HXL2W4E7lf9JzCmGVUdgjX3N/iU=", "packages": false},
   {"path": "golang.org/x/sync", "version": "v0.24.0",
    "modH1": "h1:sUUOizhqBxiL6pEWpqNLUiaJn1ShEbZ6BBqskPbjZm0=",
    "h1": "h1:iqiDrGoDgyuvAKrhLQthUEzU/R378N78gXbBjxed7oA=",
    "tree": "sha256-H9UJMd7zzra4wt24bLTr3y/ooOhJeXlTmULNtrm3gdc=", "go": "1.23.0", "packages": true}]}}
```

The fetcher checks both `h1` and `tree`; proxy zips are not byte-stable across sources, so
the zip is never the imported path (S2 §3.6).

### §03.2.5 go.mod / go.sum generation (R-GO-003)

1. go.mod: the two header lines (§03.0.2), `module <Path>`, `go <X.Y.Z>`, one `require` block
   of direct requirements (Require + codegen runtimes §03.6.4 + the build-definition
   requirements §01 contributes for the module containing `bob.go` files), one block of
   indirect requirements with `// indirect`, both sorted by path. No `toolchain`, `replace`,
   `exclude`, `retract`.
2. go.sum: `<path> <version> <h1>` for each `packages:true` module and `<path> <version>/go.mod <modH1>`
   for every module of the pruned graph, sorted bytewise by line, `\n` endings.
3. User environment (`bob env`, shims, `bob dev`; §02): `GOPROXY=file://$XDG_CACHE_HOME/bob/goproxy`
   (bob writes `<escaped path>/@v/{list,<v>.info,<v>.mod,<v>.zip}` for every lock module, case
   escaping `!x`; zips written by bob from the tree), `GOFLAGS=-mod=readonly`, `GOSUMDB=off`,
   `GOTOOLCHAIN=local`, `GOROOT`/`PATH` → pinned toolchain, `CC` → cc-wrapper,
   `CGO_CFLAGS`/`CGO_LDFLAGS` = union over the module's `Cgo` deps; gopls gets `-tags=bob`
   (ADR 0003).

#### R-GO-003a Go tooling works on generated files, edits are blocked
- **Acceptance:** In the Go fixture via shims as a non-root user: `go build ./...`, `go vet ./...`,
  `gopls check` on a file importing a third-party and a generated package succeed offline;
  `go get golang.org/x/text@latest` fails (read-only go.mod or proxy miss) and leaves files
  unchanged; `go mod tidy` is detected as drift at the next `bob build` (R-FW-061).
- **Source:** R-GO-003, S2 §3.5.

#### R-GO-004a Toolchain per module
- **Acceptance:** Given two modules with `Go: "1.25"` and `Go: "1.26"`, then each package is
  compiled with its module's toolchain (job golden shows the toolchain store path), std is
  built once per toolchain variant, and a module requirement whose `go` directive exceeds the
  module's Go fails at `bob lock` with the toolchain error above.
- **Source:** R-GO-004.

---

## §03.3 Rust (`pkg/rust`) — ADR 0005

### §03.3.1 API

```go
package rust // github.com/megakuul/bob/pkg/rust

type Edition string

const (
	Edition2018 Edition = "2018"; Edition2021 Edition = "2021"; Edition2024 Edition = "2024"
)

// Dep is a dependency of a crate: a crates.io crate (Crate) or a local crate (*Lib, (*Lib).With).
type Dep interface{ rustDep() }

// DepOpt modifies a crates.io dependency.
type DepOpt func(*depOpts)

// Crate is a crates.io dependency; req is a Cargo version requirement ("1", "^0.13", "=1.38.0").
// Resolved into bob.lock by `bob lock` (R-RS-002).
func Crate(name, req string, opts ...DepOpt) Dep
func Features(fs ...string) DepOpt  // enable features of the dependency
func NoDefaultFeatures() DepOpt     // default-features = false
func Rename(codeName string) DepOpt // `use codeName::…`

// Option is a graph-wide setting attached to any local crate's Options.
type Option interface{ rustOption() }

// Uses gives a crates.io crate's build script native packages (PATH, PKG_CONFIG_PATH,
// include/lib dirs). Applies to every unit of that crate in the graph.
//
//	rust.Uses("openssl-sys", nixpkgs.Lib(p, "openssl"))
func Uses(crate string, libs ...cc.Linkable) Option

// LibOpts configures Library, ProcMacro, StaticLib and CDylib.
type LibOpts struct {
	Name        string              // crate name; default directory name with '-' kept, code name '-'→'_'
	Edition     Edition             // default Edition2021
	Root        string              // default "src/lib.rs"
	Srcs        bob.Sources         // default bob.Glob("src/**", "build.rs")
	Deps        []Dep
	BuildDeps   []Dep               // build-dependencies (host units)
	DevDeps     []Dep               // tests only
	Features    map[string][]string // this crate's feature table (Cargo syntax: "dep:x", "x/f", "x?/f")
	Default     []string            // default features
	BuildScript string              // "build.rs" or "" (default: "build.rs" if the file exists)
	Native      []cc.Linkable       // bob C/C++ libs, cc.External or nixpkgs.Lib linked into this crate
	Headers     string              // StaticLib/CDylib only: include dir exported to C consumers
	Toolchain   *nixpkgs.Package     // default nixpkgs.Tool(p, "rustc") (R-RS-004)
	Options     []Option
}

// BinOpts configures Binary.
type BinOpts struct {
	Name, Root  string               // Root default "src/main.rs"
	Edition     Edition
	Srcs        bob.Sources
	Deps, BuildDeps, DevDeps []Dep
	Features    map[string][]string
	Default     []string
	Enable      []string             // features enabled when built as a root target
	BuildScript string
	Native      []cc.Linkable
	Toolchain   *nixpkgs.Package
	Options     []Option
}

// Library is an rlib, usable as Dep. Label //<dir>:<Name>.
//
//	var Core = bob.Define(func(p *bob.P) *rust.Lib {
//		return rust.Library(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("serde", "1", rust.Features("derive"))}})
//	})
func Library(p *bob.P, o LibOpts) *Lib

// ProcMacro is a host-compiled proc-macro crate, usable as Dep.
//
//	var Macros = bob.Define(func(p *bob.P) *rust.Lib {
//		return rust.ProcMacro(p, rust.LibOpts{Deps: []rust.Dep{rust.Crate("syn", "2", rust.Features("full")), rust.Crate("quote", "1")}})
//	})
func ProcMacro(p *bob.P, o LibOpts) *Lib

// With enables features of a local crate for this edge.
func (l *Lib) With(features ...string) Dep

// Binary builds bin/<Name>.
//
//	var Demo = bob.Define(func(p *bob.P) *rust.Bin {
//		return rust.Binary(p, rust.BinOpts{Deps: []rust.Dep{rust.Crate("tokio", "1", rust.Features("rt", "macros")), Core(p)}})
//	})
func Binary(p *bob.P, o BinOpts) *Bin

// StaticLib builds lib<name>.a (+ Headers); implements cc.Linkable for C and cgo consumers.
//
//	var RCore = bob.Define(func(p *bob.P) *rust.CLib {
//		return rust.StaticLib(p, rust.LibOpts{Name: "rcore", Headers: "include"})
//	})
func StaticLib(p *bob.P, o LibOpts) *CLib

// CDylib builds lib<name>.so; implements cc.Linkable.
//
//	var Plugin = bob.Define(func(p *bob.P) *rust.CLib { return rust.CDylib(p, rust.LibOpts{Name: "plugin"}) })
func CDylib(p *bob.P, o LibOpts) *CLib

// TestOpts selects what to test: the unit tests of Crate (--test) and every tests/*.rs
// integration test (one unit per file).
type TestOpts struct {
	Crate    interface{ rustCrate() } // *Lib, *Bin or *CLib
	Args     []string
	Services []bob.Service
}

// Test declares the tests of a local crate. Units //<dir>:<crate>_test and
// //<dir>:<crate>_test_<file stem>.
//
//	var CoreTest = bob.Define(func(p *bob.P) *rust.Test { return rust.Test(p, rust.TestOpts{Crate: Core(p)}) })
func Test(p *bob.P, o TestOpts) *Test
```

Features also come from the variant (`bob.Variant.Features`, §04) and are added on top of
`Enable`/edge features.

### §03.3.2 Resolution (`bob lock`) and evaluation-time features

- Index: crates.io sparse index `https://index.crates.io/` (ETag/`Last-Modified` cache under
  `$XDG_CACHE_HOME/bob/crates-index`); alternative registries and git/path-to-outside-repo
  dependencies are rejected in v1.
- Lockfile resolver (S3 §5.4): one resolution for all local crates of the repo; all features
  of local crates; all platforms; dev-deps of local crates; weak `x?/f` counts as a dependency;
  one version per semver-compatibility class (`1.x`, `0.y`, `0.0.z`); highest non-yanked match;
  prefer versions with `rust_version` ≤ toolchain; at most one crate per `links` value; then
  download each crate once, verify the index `cksum`, unpack, compute `narHash`, read `meta`.
- Evaluation (each build): Cargo resolver-2 feature unification per (crate, platform role);
  build-deps, proc-macros and their deps are **host** units; target-specific deps filtered by
  bob's built-in cfg tables per triple; dev-deps only for test units.

#### R-RS-002a bob's resolution equals Cargo's
- **Acceptance:** On a corpus of ≥ 10 workspaces (incl. the 4 S3 graphs: 12, 25, 173, 393
  packages), the Cargo.lock bob generates is byte-identical to `cargo generate-lockfile`
  (pinned cargo, same index snapshot, CI corpus job); cfg tables equal `rustc --print cfg
  --target <t>` of the pinned toolchain for both targets.
- **Source:** R-RS-002, S3 §3.2.

#### R-RS-005 bob.lock carries the crate graph facts (D58)
- **Statement:** Each crate entry stores edges, feature table, `links` and the Cargo.toml facts
  needed to plan, so evaluation needs no network and no crate sources.
- **Acceptance:** Given the Rust fixture locked, with network disabled and an empty crate
  cache, `bob build --dry-run //...` (plan only) succeeds and prints the same derivation set
  as with sources present. *Clarification (Gate H, ruling G24):* `--dry-run` (plan only, no
  build) is added to the `bob build` flags in §02 by the milestone that builds it; "empty crate
  cache" means a fresh `BOB_HOME` in a user+net namespace.
- **Source:** D58, O-S3-1 (a), S3-2.

`lock.Crate` and the `rust` section:

```go
// RustSection is bob.lock's "rust" section.
type RustSection struct {
	Inputs string  `json:"inputs"`
	Index  string  `json:"index"`  // "sparse+https://index.crates.io/"
	Crates []Crate `json:"crates"` // sorted by name, then semver
}
// Crate is one crates.io crate version.
type Crate struct {
	Name     string              `json:"name"`
	Version  string              `json:"version"`
	Checksum string              `json:"checksum"`        // SRI sha256 of the .crate (= index cksum)
	NarHash  string              `json:"narHash"`         // SRI sha256 NAR hash of the unpacked source
	Links    string              `json:"links,omitempty"`
	Deps     []CrateDep          `json:"deps,omitempty"`
	Features map[string][]string `json:"features,omitempty"`
	Meta     CrateMeta           `json:"meta"`
}
type CrateDep struct {
	Name            string   `json:"name"`                      // name used in code (after rename)
	Crate           string   `json:"crate"`                     // resolved "name@version"
	Kind            string   `json:"kind,omitempty"`            // "" (normal) | "build" | "dev"
	Target          string   `json:"target,omitempty"`          // cfg expression or triple
	Optional        bool     `json:"optional,omitempty"`
	NoDefault       bool     `json:"noDefault,omitempty"`       // default-features = false
	Features        []string `json:"features,omitempty"`
}
type CrateMeta struct {
	Edition   string `json:"edition"`
	LibName   string `json:"libName,omitempty"`   // if ≠ name with '-'→'_'
	LibPath   string `json:"libPath,omitempty"`   // if ≠ "src/lib.rs"
	ProcMacro bool   `json:"procMacro,omitempty"`
	Build     string `json:"build,omitempty"`     // build script path
}
```

```json
{"name": "zstd-sys", "version": "2.1.2+zstd.1.5.7",
 "checksum": "sha256-<base64>", "narHash": "sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0=",
 "links": "zstd",
 "deps": [{"name": "cc", "crate": "cc@1.2.41", "kind": "build", "features": ["parallel"]},
          {"name": "pkg-config", "crate": "pkg-config@0.3.32", "kind": "build", "optional": true}],
 "features": {"default": ["legacy", "arrays", "zdict_builder"], "pkg-config": ["dep:pkg-config"]},
 "meta": {"edition": "2018", "build": "build.rs"}}
```

Optional deps no workspace feature can enable are omitted. Size guide: 392 crates ≈ 413 KB
indented JSON (S3 §5.3), written in §01 R-FW-004's canonical form (2-space indent, sorted
keys; crates sorted by name, version), which keeps diffs per crate reviewable.

### §03.3.3 Derivation layout per crate unit

Units are keyed by (crate, version or "local", host|target, features, projection).

| Derivation | Name | Job | Inputs | Output |
|---|---|---|---|---|
| source (registry) | `crate-<name>-<version>` | bob-imported source path (FOD-identical, recursive), hash = `narHash` (Fetcher, not a Nix fetch) | — | unpacked crate |
| build-script compile | `<name>-<version>[-host]-build-script` | `rustc` (bin, opt 0) | source, build-deps (host units) | `bin/build_script_build` |
| build-script run | `<name>-<version>[-host]-build-run` | `rust-build-run` | source, script, `links` deps' runs, `Uses`/`Native` packages | `out-dir/`, `output`, `directives.json` |
| compile | `<name>-<version>[-host][-<projection>]` | `rustc` | source, deps' outputs, own run, transitive runs (linking types) | `lib/lib<n>-<H>.rlib` / `.so` (proc-macro) / `lib/lib<n>.{a,so}` + `include/` / `bin/<n>` |
| test compile / run | `<name>-test[-<stem>]` / `…-run` | `rustc --test` / `test-run` | crate + dev-deps | test binary / `test-result.json` |

Toolchain inputs: nixpkgs `rustc`, `stdenv.cc`, `stdenv.cc.bintools`; nothing else (bob-builder
does the rest). rustc flags exactly as S3 §3.4 (`-C metadata=` first 16 hex of sha256(name,
version, source checksum or "local", role, features, toolchain id, deps' hashes, projection
tag); `--cap-lints allow` for registry crates; `--check-cfg` for local crates; host units
opt-level 0; `--remap-path-prefix=<store src>=<crate>-<version>` for registry and
`=<repo-relative dir>` for local crates). Build-script contract (env, directives, consumer side)
exactly as S3 §3.3; `rerun-if-*` ignored; `cargo::error` fails; unknown directives recorded
as `unsupported` and printed as a note. bob-builder is versioned separately from the CLI (it is
an input of every Rust derivation; changing it rebuilds all).

#### R-RS-001a Crates, build scripts, proc-macros and C interop
- **Acceptance:** Given `tests/fixtures/repos/rust` (S3 demo graph: serde+derive, clap+derive,
  tokio+macros, regex, zstd-sys with C, a local proc-macro, a StaticLib linked by the Go cgo
  fixture, openssl-sys via `rust.Uses`), when built natively and cross, then all outputs run
  under `env -i`, the cgo binary calls into the StaticLib, Cargo is never executed (no `cargo`
  in any derivation's inputs), and a second-store rebuild is bit-identical.
- **Source:** R-RS-001, R-RS-002, S3 §3.1.

### §03.3.4 Generated files (R-RS-003, A10)

- `rust-project.json` (repo root): `sysroot_src` = `<rust-src>/lib/rustlib/src/rust/library`;
  `crates[]` in topological order with `root_module`, `edition`, `deps[{crate,name}]`, `cfg`
  (features + build-script cfgs), `env` (`OUT_DIR` = the run's `out-dir`, `CARGO_PKG_*`,
  `rustc-env`), `is_proc_macro` + `proc_macro_dylib_path` (store), `is_workspace_member`
  (local crates true), `source.include_dirs`. Local crates point to the working tree, registry
  crates to their store source. Editor settings (§02) set rust-analyzer `linkedProjects` to it.
- `Cargo.toml` (root: `[workspace] resolver = "3"`, `members` sorted) and per local crate
  (`[package] name, version = "0.0.0", edition, publish = false`, `[lib]/[[bin]]` path,
  `[features]`, `[dependencies]` with `version = "=<locked>"`, `features`, `default-features`,
  `package` for renames, local crates as `path`), `Cargo.lock` (format version 4, from
  bob.lock), `.cargo/config.toml` (`[source.crates-io] replace-with = "bob-vendor"`,
  `[source.bob-vendor] directory = ".bob/vendor"`), `.bob/vendor/<name>-<version>` symlinks to
  store sources with `.cargo-checksum.json`. A user-owned `.cargo/config.toml` is a conflict
  (`E-GEN-CONFLICT`, BOB-E-1016).

#### R-RS-003a rust-analyzer and cargo metadata work
- **Acceptance:** In the Rust fixture after `bob gen`, through shims as non-root on a host
  without `/nix`: `rust-analyzer analysis-stats .` reports 0 unresolved imports and 0
  unresolved macro calls (proc-macros expand from store dylibs); `cargo metadata --locked
  --offline` exits 0.
- **Source:** R-RS-003, A10, S3 §3.5.

#### R-RS-004a Toolchain per package
- **Acceptance:** A crate with `Toolchain: nixpkgs.Tool(p, "<other rustc attr>")` compiles with
  it (job golden); mixing toolchains along one dependency edge fails at evaluation with
  `✗ //a:a (rustc 1.95.0) depends on //b:b built with rustc 1.94.1; Rust crates linked together need the same toolchain`.
  **[open]** The pin has one `rustc`; other versions need a second nixpkgs pin (§01) —
  recommendation: allow `Toolchain` only from the project's pins in v1.
- **Source:** R-RS-004.

### §03.3.5 Errors (examples, §02 templates)

`✗ cannot resolve crates: app@0.1.0 depends on tokio 1: no version of tokio satisfies all of: ^1.40, =1.38.0`;
`✗ serde_json@1.0.154 is not in bob.lock (run bob lock)`;
`✗ crates.io:foo@1.2.3: download does not match bob.lock`;
build-script failure: label of the crate unit, the script's stderr excerpt, and for `-sys`
crates without native inputs the hint `rust.Uses("<crate>", nixpkgs.Lib(p, "<attr>"))`.

### §03.3.6 Native-crate defaults table

Shipped Go table mapping `-sys` crates to nixpkgs packages, used when no `rust.Uses` names the
crate (same admission rule as §03.0.6):

```go
type NativeDefault struct {
	Crate string   // exact crate name
	Range string   // semver range
	Libs  []string // nixpkgs attributes (target set)
	Why   string
	Test  string   // crate of bob's CI corpus that must build and run with this entry
}
// v1 entry (verified in S3): {Crate: "openssl-sys", Range: ">=0.9", Libs: ["openssl"], ...}
```

---

## §03.4 TypeScript / npm (`pkg/npm`) — ADR 0006

The npm/pnpm/yarn CLIs are never used (D15). Runtime is Node (nixpkgs `nodejs` for builds,
`nodejs-slim` for runtime outputs); Bun selectable for final outputs.

### §03.4.1 API

```go
package npm // github.com/megakuul/bob/pkg/npm

// Dependency is a registry package, an alias, a peer, or a local *Lib.
type Dependency interface{ npmDep() }
type DepOpt func(*depOpts)

// Dep is a registry dependency; req is a node-semver range or dist-tag ("^19.1.0", "latest").
func Dep(name, req string, opts ...DepOpt) Dependency
func Alias(as, name, req string) Dependency // "lodash4" → npm:lodash@^4
func Peer(name string) Dependency           // only inside Extend
func Dev() DepOpt                           // build/tooling only; never in runtime closures
func Optional() DepOpt

// Option is a package-level fix declared by the user (not a quirk). Options apply to every
// instance of the named package in the importer's graph.
type Option interface{ npmOption() }
func AllowScripts(pkgs ...string) Option            // lifecycle allowlist, exact names (R-NPM-004)
func Extend(pkg string, deps ...Dependency) Option  // phantom deps: npm.Extend("@fastify/cors", npm.Peer("fastify"))
func Hoisted() Option                               // per-importer hoisted layout fallback (D30)

type Runtime int
const ( Node Runtime = iota; Bun )

// TSConfig generates tsconfig.json (R-NPM-008). Zero value = defaults per constructor.
type TSConfig struct {
	Target           string            // default "ES2022"
	Module           string            // default "ESNext" (Vite) / "NodeNext" (Library, App)
	ModuleResolution string            // default "Bundler" (Vite) / "NodeNext"
	Strict           *bool             // default true
	JSX              string            // "", "react-jsx", "preserve"
	Lib, Types       []string
	Extends          string            // e.g. "./.svelte-kit/tsconfig.json" (set automatically for Kit)
	Include, Exclude []string          // default ["src"], []
	Extra            map[string]any    // other compilerOptions; keys validated against tsc's list
}

// LibOpts configures Library.
type LibOpts struct {
	Name     string        // package name ("@demo/ui"); required
	Srcs     bob.Sources   // default bob.Glob("src/**")
	Entry    string        // default "src/index.ts"
	Deps     []Dependency
	Peers    []Dependency  // provided by the consumer
	TSConfig TSConfig
	Generated []Generated // codegen outputs materialised into this importer (§03.6)
	Options  []Option
}

// Library compiles with tsc into dist/ (+ .d.ts) and is linked into consumers' node_modules
// under Name (R-NPM-006). Label //<dir>:<last element of Name>.
//
//	var UI = bob.Define(func(p *bob.P) *npm.Lib {
//		return npm.Library(p, npm.LibOpts{Name: "@demo/ui", Peers: []npm.Dependency{npm.Dep("react", "^19")}})
//	})
func Library(p *bob.P, o LibOpts) *Lib

// AppOpts configures App.
type AppOpts struct {
	Name     string       // default directory name
	Entry    string       // "src/server.ts"; required
	Srcs     bob.Sources
	Deps     []Dependency
	Runtime  Runtime      // Node (default) | Bun (R-NPM-009)
	Bundle   *bool        // default true: esbuild bundle; false: ship node_modules
	TSConfig TSConfig
	Generated []Generated // codegen outputs materialised into this importer (§03.6)
	Options  []Option
}

// App is a Node/Bun program: bin/<Name> wrapper calling the runtime by store path.
//
//	var Server = bob.Define(func(p *bob.P) *npm.App {
//		return npm.App(p, npm.AppOpts{Entry: "src/server.ts", Deps: []npm.Dependency{npm.Dep("fastify", "^5")}})
//	})
func App(p *bob.P, o AppOpts) *App

// ViteOpts configures Vite.
type ViteOpts struct {
	Name     string
	Srcs     bob.Sources    // default bob.Glob("src/**", "public/**", "index.html", "vite.config.*", "svelte.config.*")
	Deps     []Dependency
	Config   string         // default "vite.config.ts" (user-owned, D33)
	Kit      bool           // SvelteKit with adapter-static
	Dev      DevServer      // Port (default 5173), Host (default "localhost")
	TSConfig TSConfig
	Generated []Generated // codegen outputs materialised into this importer (§03.6)
	Options  []Option
}

// Vite runs `vite build` hermetically; `bob dev` runs its dev server (R-NPM-007, R-NPM-010).
// Output: the static site directory.
//
//	var Web = bob.Define(func(p *bob.P) *npm.Site {
//		return npm.Vite(p, npm.ViteOpts{Deps: []npm.Dependency{
//			npm.Dep("svelte", "^5"), npm.Dep("vite", "^7.1.0", npm.Dev()),
//			npm.Dep("@sveltejs/vite-plugin-svelte", "^6", npm.Dev()), ui.UI(p)}})
//	})
func Vite(p *bob.P, o ViteOpts) *Site

type Runner int
const ( Vitest Runner = iota; NodeTest )

// TestOpts configures Test. Of names the importer whose deps and sources are used.
type TestOpts struct {
	Of       interface{ npmImporter() } // *Lib, *App, *Site
	Runner   Runner
	Files    bob.Sources               // default bob.Glob("src/**/*.test.ts")
	Services []bob.Service
}

// Test runs vitest (`--reporter=json`) or `node --test --test-reporter=tap` in a derivation.
//
//	var WebTest = bob.Define(func(p *bob.P) *npm.Test { return npm.Test(p, npm.TestOpts{Of: Web(p)}) })
func Test(p *bob.P, o TestOpts) *Test
```

Each Library/App/Site is an **importer** (one generated `package.json`, `tsconfig.json`,
`node_modules`). git/file/URL specs are rejected in v1 (S4-10).

### §03.4.2 Resolution (`bob lock`)

Against the registry's abbreviated metadata (`Accept: application/vnd.npm.install-v1+json`,
ETag cache `$XDG_CACHE_HOME/bob/npm-meta`; per-version documents only for packages with
`os`/`cpu` to read `libc`). Rules (S4 ADR 0006 §1): node-semver with the prerelease rule;
`latest` dist-tag wins when it satisfies, else highest satisfying non-deprecated version;
reuse an already chosen version when it satisfies; breadth-first, alphabetical per level;
optional deps kept for all platforms; `npm:` aliases; bundled deps not resolved; required
peers auto-installed, optional peers linked only when present; workspace packages link by name.
Instance key = `name@version` + `(peer@version,…)` sorted when it has resolved peers (pnpm-style;
two importers with different peers get two instances). Revalidation is skipped when
`inputs` is unchanged and caches are fresh (≤ 24 h) unless `--refresh`.

#### R-NPM-001a bob's resolution matches npm and is peer-correct
- **Acceptance:** On the six S4 graphs (69–411 packages) plus ≥ 4 more (SvelteKit, Vite+React,
  fastify, a monorepo with two peer-split importers), the name@version set and edges equal
  `npm install --package-lock-only` (pinned npm, same registry snapshot from the harness
  mirror), except peer-split cases where bob's lock gives each importer its own peer (asserted
  explicitly); registry documents over 128 MB, invalid names or unknown integrity algorithms
  are rejected with `E-RESOLVE` (BOB-E-1015).
- **Source:** R-NPM-001, D15, S4 §3.1.

`lock.NpmImporter`, `lock.NpmPackage` and the `npm` section:

```go
// NpmSection is bob.lock's "npm" section.
type NpmSection struct {
	Inputs    string                 `json:"inputs"`
	Registry  string                 `json:"registry"`  // "https://registry.npmjs.org/"
	Importers map[string]NpmImporter `json:"importers"` // key: repo-relative importer dir
	Packages  map[string]NpmPackage  `json:"packages"`  // key: instance key
}
type NpmImporter struct {
	Deps map[string]string `json:"deps"`          // alias → instance key or "link:<dir>"
	Dev  []string          `json:"dev,omitempty"` // aliases that are Dev()
}
type NpmPackage struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Integrity        string            `json:"integrity"`              // sha512-… (sha256/sha1 legacy accepted)
	Tarball          string            `json:"tarball,omitempty"`      // only if not the default registry URL
	Deps             map[string]string `json:"deps,omitempty"`         // alias → instance key
	Peers            []string          `json:"peers,omitempty"`
	Optional         bool              `json:"optional,omitempty"`
	OS               []string          `json:"os,omitempty"`
	CPU              []string          `json:"cpu,omitempty"`
	Libc             []string          `json:"libc,omitempty"`
	Bundled          []string          `json:"bundled,omitempty"`
	HasInstallScript bool              `json:"hasInstallScript,omitempty"`
	Bin              map[string]string `json:"bin,omitempty"`          // command → relative path
}
```

```json
{"npm": {"inputs": "sha256-…", "registry": "https://registry.npmjs.org/",
  "importers": {"web": {"deps": {"react": "react@19.3.1", "@demo/ui": "link:packages/ui",
                                  "vite": "vite@7.3.7"}, "dev": ["vite"]}},
  "packages": {
    "use-sync-external-store@1.7.0(react@19.3.1)": {"name": "use-sync-external-store",
      "version": "1.7.0", "integrity": "sha512-…", "deps": {"react": "react@19.3.1"}, "peers": ["react"]},
    "@rollup/rollup-linux-arm64-gnu@4.64.12": {"name": "@rollup/rollup-linux-arm64-gnu",
      "version": "4.64.12", "integrity": "sha512-…", "optional": true,
      "os": ["linux"], "cpu": ["arm64"], "libc": ["glibc"]}}}}
```

Platform filtering (`os`/`cpu`/`libc`, `!x` negation) happens at build time per target; a
non-optional package that does not match is an error naming it.

### §03.4.3 Derivation layout

| Derivation | Name | Job | Inputs | Output |
|---|---|---|---|---|
| tarball | `npm-<name>-<version>.tgz` | bob-imported flat path (FOD-identical), hash = integrity | — | tarball |
| package instance | `npm-<instance key>` | `npm-unpack` (+ `npm-script` if allowed) | tarball, dep instances; coreutils/tar/gzip; script toolchain only if allowed | `node_modules/<name>/` + symlinks `node_modules/<dep alias>` → dep instance |
| cycle group | `npm-scc-<hash8>` | `npm-unpack` | members' tarballs | `<i>/node_modules/<name>` per member |
| importer node_modules | `node_modules-<importer>` | `npm-node-modules` | instances, nodejs | direct-dep symlinks, workspace libs, `.bin/<cmd>` wrappers (`exec <node> <realpath>`) |
| hoisted (fallback) | `node_modules-<importer>-hoisted` | `npm-node-modules` | tarballs | npm-style tree |
| workspace lib | `npm-lib-<name>` | `npm-tool` (tsc) | sources, node_modules | `node_modules/<name>/{package.json,dist/}` |
| app / site / test | `<label>` | `npm-tool` | sources, node_modules (copied as a writable symlink dir, `cp -a`), tools | bundle + `bin/` / site dir / `test-result.json` |

- Package instances do not depend on node (a node bump does not rebuild them, S4-4).
- Bundles have `/nix/store/<hash>-npm-` references scrubbed (S4-8).
- The working tree gets `node_modules` as a real directory of symlinks, swapped in by atomic
  rename, keeping `.vite` and `.cache`; `.bob/node_modules.path` records the store path.

#### R-NPM-003a Isolated layout by default, hoisted on request
- **Acceptance:** In the Vite+React fixture, `node_modules/react` is a symlink into the store,
  a package cannot `require` an undeclared package (test asserts `ERR_MODULE_NOT_FOUND`), and
  adding one dependency rebuilds ≤ 4 derivations (S4 quiet: 4); with `npm.Hoisted()` the
  importer gets one hoisted derivation and the same app output runs.
- **Source:** R-NPM-002, R-NPM-003, D30.

### §03.4.4 Lifecycle scripts (R-NPM-004)

- Denied by default; once per lock bob prints `note: better-sqlite3@12.11.1 has an install
  script; bob does not run it. If the package needs it: npm.AllowScripts("better-sqlite3")`.
- Allowed: `preinstall`, `install` (or `node-gyp rebuild` if `binding.gyp` and neither
  install nor preinstall), `postinstall`, run by bob-builder `npm-script` with
  `bash -euo pipefail -c`, cwd = package dir, no network, `HOME=$TMPDIR`, PATH = deps' bins +
  node-gyp wrapper + nodejs, python3, gnumake, cc, sed, grep, coreutils;
  `npm_config_nodedir=<nodejs>`; afterwards only `build/Release/*.node` of the build tree is kept.
- Runtime translations (§02 `E-SCRIPT-DENIED` = BOB-E-2013, `E-PHANTOM-DEP` = BOB-E-2014):
  `Could not locate the bindings file` from a store package with a denied script →
  `✗ better-sqlite3 needs its install script (native addon). Add npm.AllowScripts("better-sqlite3") to //web:web`;
  `TS2307 Cannot find module 'X'` or `ERR_MODULE_NOT_FOUND` raised inside `/nix/store/…-npm-P-…` →
  `✗ P uses X without declaring it. Add npm.Extend("P", npm.Peer("X"))`.

#### R-NPM-004a Scripts denied by default, allowed per package, sandboxed
- **Acceptance:** Given the native fixture (better-sqlite3): without AllowScripts the test
  fails with the `E-SCRIPT-DENIED` text above (golden) and no script ran (log has no
  `node-gyp`); with AllowScripts the addon builds offline, the test opens `:memory:`, and the
  runtime closure contains only the `.node` file, glibc and libgcc beyond node.
  *Clarification (Gate H, G24):* the native fixture gets a `//native:test` target that opens
  `:memory:`, and "builds offline" is observed with the build wrapped in a network namespace; both
  are added by the milestone that builds R-NPM-004a.
- **Source:** R-NPM-004, D31, S4 §3.2.

### §03.4.5 Quirks table (R-NPM-005)

```go
// Quirk is one curated entry; the table is Go data shipped with bob (D32).
type Quirk struct {
	Package string            // exact npm name
	Range   string            // node-semver range
	Why     string            // required justification
	Skip    bool              // never run the script
	Tools   []string          // extra nixpkgs attributes for the script
	Env     map[string]string // extra env
	Test    string            // command run in the package dir after the build (required)
}
```

v1 entries (both verified in S4): `{esbuild, ">=0.15", Skip: postinstall only re-checks the
@esbuild/<platform> binary}`, `{better-sqlite3, ">=8", Env: npm_config_build_from_source=true,
Test: open :memory: and select sqlite_version()}`. Admission: only if the package cannot work
with deny-or-allow alone; phantom dependencies are never quirks (S4-6).

#### R-NPM-005a Every quirk has a justification and a passing test
- **Acceptance:** CI iterates the table: each entry has non-empty `Why` and `Test`, its Test
  passes in a fixture that depends on the package; an entry without them fails CI (`E-QUIRK-TEST`, BOB-E-9006).
- **Source:** R-NPM-005, D32.

### §03.4.6 Generated files (R-NPM-008)

- `package.json`: keys in this order: `"//"` header, `name`, `version` (`"0.0.0"`), `private:
  true`, `type: "module"`, Library only: `exports` (`{".": {"types": "./dist/index.d.ts",
  "import": "./dist/index.js"}}`), `types`, `peerDependencies` (ranges as declared);
  `dependencies` / `devDependencies` (registry: exact locked version; local libs: `"file:<rel dir>"`),
  2-space indent, trailing newline.
- `tsconfig.json`: header comment, `extends` if set, `compilerOptions` (TSConfig defaults +
  Extra, keys sorted), `include`, `exclude`.
- `vite.config.*`, `svelte.config.js` stay user-owned (D33).

#### R-NPM-008a package.json and tsconfig.json come from bob.go
- **Acceptance:** Goldens for each fixture importer; `tsc --noEmit -p .` through the shim
  passes; editing `"strict": false` into tsconfig.json is rejected as drift (R-FW-061).
- **Source:** R-NPM-008, D33.

### §03.4.7 Builders and runtimes

#### R-NPM-007a TypeScript, bundles, Vite and SvelteKit build hermetically
- **Acceptance:** Fixtures build: tsc library (dist + d.ts), fastify App (esbuild bundle,
  runs under `env -i`), Vite React site, Svelte site with a workspace library, SvelteKit with
  adapter-static (`.svelte-kit` written inside the build dir only), vitest and ESLint
  (type-checked) as tests (*clarification, Gate H G24:* the ESLint entry point, a runner or check
  package of `pkg/npm`, is specified by the milestone that builds it; until then only vitest is
  asserted); rebuilds are bit-identical. **[open]** SvelteKit was not built in S4;
  `Kit: true` runs `svelte-kit sync` before `vite build` and in `bob dev` — verify in Stage 3.
- **Source:** R-NPM-007, S4 §3.2.

#### R-NPM-009a Node by default, Bun for final outputs
- **Acceptance:** `npm.App` with `Runtime: npm.Bun` produces `bin/<name>` that runs with
  nixpkgs bun; `Runtime` is not a field of Library/Vite (compile-time); the Node App's runtime
  closure uses `nodejs-slim`.
- **Source:** R-NPM-009, D15, S4-9.

### §03.4.8 `bob dev` contract for Vite (R-NPM-010, A20)

1. If bob.go changed: relock (if `inputs` changed), build importer `node_modules`, swap it
   into the tree atomically.
2. Write `.bob/vite.bob.config.mjs`: imports the user's `vite.config.*` (object or function),
   merges `server.fs.allow: [searchForWorkspaceRoot(cwd), "/nix/store"]`, `server.port`/`host`
   from `Dev`.
3. Start `vite --config .bob/vite.bob.config.mjs` inside the namespace with the dev env;
   workspace libraries link to the **working tree** so their edits hot-reload.
4. On a dependency change in bob.go: relock, rebuild `node_modules`, swap, restart Vite
   (old process gets SIGTERM, 5 s, then SIGKILL).
5. Codegen outputs regenerate into the tree (§03.6) and reach Vite through its watcher.

#### R-NPM-010a Live reload and dependency restarts
- **Acceptance:** In the Vite fixture under `bob dev` as non-root: saving `src/App.svelte` (the fixture is Svelte) emits an
  HMR update over the dev-server websocket within the budget (§03.8); adding a dependency in
  bob.go restarts Vite with the new package importable within 10 s [8c/15G]; editing a
  `.proto` used via `connect.TS` changes the served module.
- **Source:** R-NPM-010, D34, A20, S4 §3.5.

---

## §03.5 OCaml (`pkg/ocaml`) — ADR 0007

Packages come from a pinned opam-repository commit resolved by bob; nixpkgs only for the
toolchain (A21). Toolchain at the pin: OCaml 5.4.1, dune 3.21.1, findlib 1.9.8.

### §03.5.1 API

```go
package ocaml // github.com/megakuul/bob/pkg/ocaml

// Dep is an opam package, a findlib library of one, or a local *Lib.
type Dep interface{ mlDep() }

// Opam is an opam package; req is an opam version formula ("", ">= 5.0", "= 6.1.2").
// As a Dep it means the findlib library of the same name.
func Opam(name, req string) *OpamPkg

// Lib selects one findlib library of the package: ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix").
func (o *OpamPkg) Lib(findlib string) Dep

// Option is a graph-wide setting attached to a local library or executable.
type Option interface{ mlOption() }

// Conf maps an opam conf-* package to nixpkgs packages for this project (added to the
// shipped table, §03.5.5).
func Conf(opamName string, libs ...cc.Linkable) Option

// LibOpts configures Library.
type LibOpts struct {
	Name    string        // default directory name; module namespace = capitalised name
	Srcs    bob.Sources   // default bob.Glob("*.ml", "*.mli")
	Deps    []Dep
	Stubs   bob.Sources   // C stub files
	Native  []cc.Linkable // linked into the stubs
	Wrapped *bool         // default true (dune semantics: Lib__Mod)
	Flags   []string      // extra ocamlopt flags (validated)
	Dune    bool          // fallback mode: generated dune file built inside the derivation (ppx, menhir)
	Options []Option
}

// Library is a native OCaml library, usable as Dep. Label //<dir>:<Name>.
//
//	var Geom = bob.Define(func(p *bob.P) *ocaml.Lib {
//		return ocaml.Library(p, ocaml.LibOpts{Deps: []ocaml.Dep{ocaml.Opam("fmt", "")}})
//	})
func Library(p *bob.P, o LibOpts) *Lib

// ExeOpts configures Executable and Test.
type ExeOpts struct {
	Name    string
	Main    string        // module, default "main"
	Srcs    bob.Sources
	Deps    []Dep
	Native  []cc.Linkable
	Dune    bool
	Options []Option
}

// Executable builds a native executable bin/<Name> (build references scrubbed).
//
//	var Demo = bob.Define(func(p *bob.P) *ocaml.Exe {
//		return ocaml.Executable(p, ocaml.ExeOpts{Deps: []ocaml.Dep{
//			ocaml.Opam("cmdliner", ""), ocaml.Opam("lwt", ">= 5.7").Lib("lwt.unix"), Geom(p)}})
//	})
func Executable(p *bob.P, o ExeOpts) *Exe

// Test builds an executable and runs it; exit 0 = pass; TAP output parsed if present.
//
//	var GeomTest = bob.Define(func(p *bob.P) *ocaml.Test {
//		return ocaml.Test(p, ocaml.ExeOpts{Name: "geom_test", Main: "test", Deps: []ocaml.Dep{Geom(p)}})
//	})
func Test(p *bob.P, o ExeOpts) *Test
```

v1 scope (S5 §5.7): native libraries/executables/tests, opam packages, conf-* table, C stubs
linking bob C libs and nixpkgs, `.merlin` + shims, both Linux platforms natively. Deferred
(evaluation error with hint): bytecode, js_of_ocaml/melange, ppx/menhir/ocamllex in direct
mode (hint: `Dune: true`), opam `patches`/`extra-source`, OCaml version other than the pin's,
cross-compilation (`✗ OCaml cross-compilation is not supported in v1`), OCaml libs as
`cc.Linkable`.

### §03.5.2 Resolution (`bob lock`)

- The opam-repository snapshot (`github:ocaml/opam-repository@<commit>` tarball) is a
  bob-imported source path (NAR-hashed in the lock, FOD-identical); bob reads only the opam files it needs.
- Globals: `os=linux`, `arch=arm64|x86_64`, `os-family=os-distribution=nixos`,
  `opam-version=2.5.1`, `ocaml:native=true`; flags `build=post=true`,
  `with-test=with-doc=dev=with-dev-setup=false`.
- Provided (never chosen from the repo): `ocaml`, `ocaml-base-compiler`, `ocaml-compiler`,
  `ocaml-config`, `ocaml-options-vanilla`, `base-{unix,threads,bigarray,domains,nnp,effects}`,
  `dune`, `ocamlfind` at the nixpkgs versions.
- Algorithm: prune versions never installable against the provided set (fixpoint), then a
  conflict-driven search (PubGrub-style) preferring newest versions; `depopts` present in the
  solution become build-order edges and set `%{pkg:installed}%`. The S5 greedy search was
  enough for 8 graphs but blew up before pruning on 5; production uses the conflict-driven
  solver.
- Packages without sha256/sha512 checksum are rejected (`✗ opam:foo.1.0 has no sha256/sha512 checksum; bob cannot pin it`).

#### R-ML-002 bob's opam resolution is valid and close to opam's
- **Acceptance:** On a 20-graph corpus (incl. the 8 S5 graphs), every bob solution satisfies
  all constraints (checked by an independent validator) and equals `opam install --dry-run`
  (pinned opam, same repository commit) on ≥ 70 %; unsatisfiable inputs give `E-RESOLVE` (BOB-E-1015) with
  the conflicting constraint chain, e.g. `✗ cannot resolve opam packages:
  ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0": no available version matches (dune is fixed to 3.21.1 by the toolchain)`.
- **Source:** R-ML-001, A21, S5 §3.2.

`lock.OpamRepository`, `lock.OpamPackage` and the `opam` section:

```go
type OpamSection struct {
	Inputs     string                 `json:"inputs"`
	Repository OpamRepository         `json:"repository"`
	Packages   map[string]OpamPackage `json:"packages"` // key: opam name
}
type OpamRepository struct {
	URL     string `json:"url"`     // "github:ocaml/opam-repository"
	Commit  string `json:"commit"`  // 40 hex
	NarHash string `json:"narHash"` // SRI sha256 of the unpacked snapshot
}
type OpamPackage struct {
	Version string   `json:"version"`
	URL     string   `json:"url"`
	Hash    string   `json:"hash"`           // SRI sha512 or sha256 from the opam file's checksum (flat)
	Opam    string   `json:"opam"`           // sha256 hex of the opam file in the snapshot
	Deps    []string `json:"deps,omitempty"` // depends + present depopts, minus provided packages
}
```

```json
{"opam": {"inputs": "sha256-…",
  "repository": {"url": "github:ocaml/opam-repository",
                 "commit": "af5ddb0addf6e75660ee9d7485b27425459d1d66", "narHash": "sha256-…"},
  "packages": {"fmt": {"version": "0.11.0",
    "url": "https://erratique.ch/software/fmt/releases/fmt-0.11.0.tbz",
    "hash": "sha512-P0AVX8anMVIC5BBYWWQwfWNBbIAB/SQ2Z+2djRoCtn3uyssl6cL+tAnFN7vft4F9kRaN5N3WQ1Mv9R1sHGlqSg==",
    "opam": "02bb57a07d169eef9bde16e0a50a00423a4f756d589f7d1fed0d4d3aa367d11c",
    "deps": ["cmdliner", "ocamlbuild", "topkg"]}}}}
```

### §03.5.3 Derivation layout

| Derivation | Name | Job | Inputs | Output |
|---|---|---|---|---|
| source | `opam-src-<name>-<version>` | bob-imported flat path (FOD-identical), hash from lock | — | archive |
| opam package | `opam-<name>-<version>` | `opam-build`: unpack, render the opam `build:`/`install:` commands in Go (variables substituted, filters applied), then `opam-installer --prefix=$out --libdir=$out/lib --stubsdir=$out/lib/stublibs <name>.install` if present | source, toolchain, closure of dep outputs on `OCAMLPATH`, conf-mapped nixpkgs | `lib/<findlib>/…`, `bin/`, `lib/stublibs/` |
| conf-* | `opam-conf-<x>-<v>` | none (empty output) | mapped nixpkgs packages | empty; consumers get `PKG_CONFIG_PATH`/`C_INCLUDE_PATH`/`LIBRARY_PATH` |
| local library | `ml-<name>` | `ml-compile`: `ocamldep -sort`, alias module, `ocamlfind ocamlopt` per module, stubs + `ar`, `-a`, META | own sources, closure, C libs | `lib/<name>/{*.cmi,*.cmx,*.cmt*,<name>.cmxa,<name>.a,lib<name>_stubs.a,META}` |
| executable | `ml-<name>` | `ml-link`: `ocamlfind ocamlopt -linkpkg`, then scrub build-only refs (ocaml, gcc, glibc-dev, binutils) | own sources, closure | `bin/<name>` |
| dune mode | `ml-<name>` | generated dune file + `dune build` in the sandbox | as above + dune | same as direct |

Toolchain inputs as S5 §5.2. Reference scrubbing is pattern-based (`ocaml-*`, `gcc-*`,
`glibc-*-dev`, `binutils-*`); every fixture executable is run under `env -i` after scrubbing
(R-ML-001a), so a scrubbed path the program needs shows up as a test failure.

#### R-ML-001a OCaml libraries, executables and tests build fine-grained
- **Acceptance:** Given `tests/fixtures/repos/ocaml` (S5 demo: 11 opam packages incl. lwt,
  cmdliner, yojson, fmt; a local library with C stubs linking a bob C library; an executable;
  a test), when built and tested, then the executable runs under `env -i`, its runtime closure
  is ≤ 80 MB (S5: 55 MB), each opam package is its own derivation, a library change rebuilds
  only the library and its dependents, and direct and `Dune: true` modes give working
  executables.
- **Source:** R-ML-001, S5 §3.1, §3.6.

### §03.5.4 Tooling files

Default: `<dir>/.merlin` per local library/executable (`S .`, `B .`, `PKG <findlib>…`,
`B <store>/lib/<locallib>` for local deps, `FLG -open <Lib>__` inside wrapped libraries) and
shims `.bob/bin/{ocamllsp,ocamlmerlin,dot-merlin-reader,dune,ocamlfind,ocaml,ocamlc,ocamlopt}`
with `OCAMLPATH` into the store (§02). Opt-in per project (`ocaml.Editor(ocaml.DuneFiles)`,
an `Option`): `dune-project` (`(lang dune 3.21)` first line), `dune` per directory,
`<lib>.opam`; `bob dev` then runs `.bob/bin/dune build @check` after changes.

#### R-ML-001b ocaml-lsp works without an in-tree build
- **Acceptance:** In the OCaml fixture after `bob gen`, `ocamllsp` through the shim answers
  hover and go-to-definition for an identifier from an opam package and one from a local
  library (scripted LSP client), with no `_build` directory in the tree.
- **Source:** R-ML-001, S5 §3.5.

### §03.5.5 conf-* table

Shipped Go table `{Opam: "conf-gmp", Libs: ["gmp"], Why, Test}` (same admission rule,
§03.0.6); `ocaml.Conf` adds project entries. Unmapped conf-* fails at `bob lock`:
`✗ opam:conf-foo.1: no nixpkgs mapping for this system package (add ocaml.Conf("conf-foo", nixpkgs.Lib(p, "<attr>")) or extend bob's table)`.
v1 entries: exactly those the 20-graph corpus (R-ML-002) needs; each entry's `Test` is a
corpus graph that builds with it.

---

## §03.6 Code generators and materialisation — ADR 0008

### §03.6.1 API

Codegen values are recipes: a typed `golang.Generated` or `npm.Generated` that the consuming
Go module or npm importer realises in its own `Generated` list (the module supplies the Go
import path, the importer the TS output location). A TS output cannot be passed to a Go
module (R-FW-010). Each realised recipe is one package labelled
`//<consumer dir>:gen-<owned dir relative to consumer, '/'→'-'>` (e.g. `//svc:gen-proto-greet-v1`).

```go
package proto // github.com/megakuul/bob/pkg/proto

// LibraryOpts configures a protobuf library: the *.proto files of the calling bob.go's directory.
type LibraryOpts struct {
	Deps []*Library // imported libraries
	Root string     // import root, repo-relative; default: nearest ancestor directory named "proto", else repo root
}

// Library is one proto package (one directory). .proto files need no go_package (bob sets M options).
//
//	var Greet = bob.Define(func(p *bob.P) *proto.Library {
//		return proto.Library(p, proto.LibraryOpts{Deps: []*proto.Library{commonv1.Common(p)}})
//	})
func Library(p *bob.P, o LibraryOpts) *Library

// Lint and Breaking are check packages (buf lint / buf breaking) usable in pipelines.
// Breaking compares with the library at a git revision of the same repo.
//
//	var GreetLint = bob.Define(func(p *bob.P) *proto.Check { return proto.Lint(p, Greet(p)) })
func Lint(p *bob.P, lib *Library) *Check
//	var GreetCompat = bob.Define(func(p *bob.P) *proto.Check { return proto.Breaking(p, Greet(p), "origin/main") })
func Breaking(p *bob.P, lib *Library, againstRev string) *Check
```

```go
package connect // github.com/megakuul/bob/pkg/connect

type GoOpts struct {
	Dir    string // owned dir relative to the module; default "gen/proto/<proto dir relative to Root>"
	Simple bool   // connect-go simple API
}
// Go generates protoc-gen-go + protoc-gen-connect-go code for lib and every library it imports.
// Adds google.golang.org/protobuf and connectrpc.com/connect at the generator versions (§03.6.4).
//
//	golang.ModuleOpts{Generated: []golang.Generated{connect.Go(greetv1.Greet(p), connect.GoOpts{})}}
func Go(lib *proto.Library, o GoOpts) golang.Generated

type TSOpts struct{ Dir string } // default "gen/proto/<proto dir>"
// TS generates protoc-gen-es (target=ts, import_extension=js); adds @bufbuild/protobuf at the
// generator version. @connectrpc/connect(-web|-node) are ordinary npm deps.
//
//	npm.ViteOpts{Generated: []npm.Generated{connect.TS(greetv1.Greet(p), connect.TSOpts{})}}
func TS(lib *proto.Library, o TSOpts) npm.Generated
```

```go
package graphql // github.com/megakuul/bob/pkg/graphql

// Schema is a set of .graphqls files.
//
//	var API = bob.Define(func(p *bob.P) *graphql.Schema { return graphql.NewSchema(p, bob.Glob("*.graphqls")) })
func NewSchema(p *bob.P, files bob.Sources) *Schema

type GqlgenOpts struct {
	Dir      string            // default "gen/graph" (+ "gen/graph/model")
	Models   map[string]string // GraphQL type → Go type
	Autobind []string          // Go packages
}
// Gqlgen generates the executable schema and models; never resolver stubs. Adds
// github.com/99designs/gqlgen (exact generator version) and its gqlparser.
//
//	golang.ModuleOpts{Generated: []golang.Generated{graphql.Gqlgen(schema.API(p), graphql.GqlgenOpts{})}}
func Gqlgen(s *Schema, o GqlgenOpts) golang.Generated

type TSOpts struct {
	Dir       string            // default "gen/graphql"
	Documents bob.Sources       // operation documents
	Scalars   map[string]string // GraphQL scalar → TS type
}
// TS runs graphql-codegen (typescript + typescript-operations) through bob's driver and tool lock.
//
//	npm.ViteOpts{Generated: []npm.Generated{graphql.TS(schema.API(p), graphql.TSOpts{Documents: bob.Glob("src/**/*.graphql")})}}
func TS(s *Schema, o TSOpts) npm.Generated
```

```go
package openapi // github.com/megakuul/bob/pkg/openapi

//	var Items = bob.Define(func(p *bob.P) *openapi.Spec { return openapi.NewSpec(p, "items.yaml") })
func NewSpec(p *bob.P, file string) *Spec

type Server int
const ( NoServer Server = iota; StdHTTP )

type GoOpts struct {
	Dir     string // default "gen/<Package>"
	Package string // required
	Server  Server
	Models  *bool  // default true
	Client  bool
}
// Go runs oapi-codegen; adds github.com/oapi-codegen/runtime.
//
//	golang.ModuleOpts{Generated: []golang.Generated{openapi.Go(schema.Items(p), openapi.GoOpts{Package: "rest", Server: openapi.StdHTTP})}}
func Go(s *Spec, o GoOpts) golang.Generated

type TSOpts struct{ Dir string } // default "gen/openapi"
// TS generates TypeScript types with openapi-typescript (bob tool lock). [open: not built in S6]
//
//	npm.ViteOpts{Generated: []npm.Generated{openapi.TS(schema.Items(p), openapi.TSOpts{})}}
func TS(s *Spec, o TSOpts) npm.Generated
```

### §03.6.2 Derivation rules

- One derivation per realised recipe, job `codegen`; generators from the pinned nixpkgs
  (`buf`, `protoc-gen-go`, `protoc-gen-connect-go`, `protoc-gen-es`, `gqlgen`,
  `oapi-codegen`); graphql-codegen/openapi-typescript from bob-shipped tool locks (§03.6.5).
- bob writes `buf.yaml`, `buf.gen.yaml`, `gqlgen.yml` (`skip_mod_tidy: true`,
  `skip_validation: true`), the oapi-codegen config inside the derivation; users never write
  them. Inputs = only files the generator reads, laid out at repo-relative paths (errors need
  no translation): proto files of the library and its imports; schema files; spec + go.mod;
  gqlgen also go.sum + the module proxy dir of the consuming module.
- `HOME`, `BUF_CACHE_DIR` = `$TMPDIR`; no network.
- Output = exactly the owned directory's content: regular files only, no `.gitignore`
  (checked, else bob error naming the generator).

#### R-GEN-001a protobuf/connect for Go and TypeScript
- **Acceptance:** Given `tests/fixtures/repos/codegen` (S6 tree: two proto libraries with an
  import, a Go server and client, a TS web client), `bob build //...` generates, compiles and
  an e2e test calls the Go server from both clients successfully; each codegen derivation
  rebuilds bit-identically; a proto syntax error prints
  `✗ //svc:gen-proto-greet-v1 code generation failed (defined at svc/bob.go:<line>)` +
  `│ proto/greet/v1/greet.proto:9:1:unexpected '}' after definition` (golden), no raw Nix text.
- **Source:** R-GEN-001, R-GEN-004, S6 §3.1, §3.4.

#### R-GEN-002a GraphQL for Go and TypeScript
- **Acceptance:** In the codegen fixture, gqlgen output compiles with the user's resolvers and
  graphql-codegen output type-checks the TS operations; an undefined type in the schema gives
  `│ failed to load schema: schema/schema.graphqls:9:18: Undefined type Nope.` (gqlgen) and
  `│ Unknown type: "Nope".` (TS), no Node stack trace into the store.
- **Source:** R-GEN-002, S6 §3.3–3.4.

#### R-GEN-003a OpenAPI for Go and TypeScript
- **Acceptance:** oapi-codegen `StdHTTP` server + models compile and serve the fixture's
  `items.yaml`; `openapi.TS` types type-check a fetch client in the web fixture; a bad `$ref`
  prints the generator's message with the spec path.
- **Source:** R-GEN-003.

### §03.6.3 Early cutoff for codegen (A11)

Consumers do not depend on the codegen derivation directly: bob builds codegen packages of the
requested closure first, adds each output's content to the store as a content-addressed path
(`gen-<label slug>`, `nix store add --mode nar`), and plans consumers against that path. A
byte-identical output therefore rebuilds nothing downstream.

#### R-GEN-005 Codegen early cutoff
- **Acceptance:** In the codegen fixture, a whitespace-only change to `greet.proto` followed
  by `bob build //...` rebuilds only the codegen derivations (2) and no Go/TS consumer; the
  added two-phase cost on a no-change build is ≤ 0.5 s [8c/15G] (S6 quiet: 14.5 s → 1.43 s
  with cutoff).
- **Source:** A11, ADR 0008 Consequences, `s06-codegen.log`.

### §03.6.4 Generator ↔ runtime pinning

For each recipe bob adds the runtime library at the version matching the generator of the
pin (`google.golang.org/protobuf` ≥ protoc-gen-go, `connectrpc.com/connect` ≥ the version the
connect-go output needs, `github.com/99designs/gqlgen` exact, `@bufbuild/protobuf` = protoc-gen-es,
`github.com/oapi-codegen/runtime`). An explicit conflicting pin fails before any build:
`✗ //svc:gen-graph: generator gqlgen 0.17.90 (bob's nixpkgs pin) does not match its runtime: the generated code needs github.com/99designs/gqlgen 0.17.90, but svc/bob.go requires v0.17.89`
`hint: remove the Require of github.com/99designs/gqlgen; bob adds the matching version`.

#### R-GEN-006 Runtime mismatch is a pre-check
- **Acceptance:** Fixture faults F4 (gqlgen runtime v0.17.89) and F5 (@bufbuild/protobuf
  2.10.0) each exit 3 (BOB-E-1021) before Nix runs, with the golden text.
- **Source:** R-NIX-011, S6 §3.3.

### §03.6.5 Tool locks

npm-based generators not in nixpkgs ship as `lock.ToolLock` documents embedded in bob (one per
tool, pinned at bob release time, same schema as `NpmSection` without `inputs`, one importer
`"tool"`), installed with the §03.4 installer and driven by a bob-owned driver script calling
the plugin API (no CLI, no `codegen.yml`). Users do not declare them; security updates come
with bob releases.

### §03.6.6 Materialisation (R-FW-007, D25)

Owned directories: one per realised recipe (defaults in §03.6.1, relative to the consumer
dir); never nested, never overlapping, never inside `.git` or `.bob`, clean relative paths.
Errors: `✗ //p:b generates into gen/b/, which is inside gen/ owned by //p:a` /
`hint: generated directories may not be nested`; `✗ //p:a and //p:b both generate into gen/` /
`hint: give one of them another output directory`.

Manifest `.bob/gen/manifest.json` (hostile input: ≤ 64 MiB, all paths re-validated):

```go
type Manifest struct {
	Version int                     `json:"version"` // 1
	Roots   map[string]ManifestRoot `json:"roots"`   // key: owned dir, repo-relative
}
type ManifestRoot struct {
	Label         string                  `json:"label"`
	Source        string                  `json:"source"`        // store path of the output
	Files         map[string]ManifestFile `json:"files"`         // path relative to the root
	PendingSource string                  `json:"pendingSource"` // set during a write
	Pending       map[string]ManifestFile `json:"pending"`
}
type ManifestFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Exec   bool   `json:"exec,omitempty"`
	MTime  int64  `json:"mtime"` // ns; size+mtime only enable the no-hash fast path
}
```

Classification per file on disk (ownership is decided by sha256):

| On disk | Manifest/pending | New content | Action |
|---|---|---|---|
| hash = new | any | present | keep (fix mode if needed) |
| hash = manifest or pending | known | present / absent | overwrite / delete |
| hash ≠ manifest | known | — | conflict **edited** |
| not in manifest | — | — | conflict **foreign** (unless hash = new: adopt) |
| `.bob-tmp-*` temp file | — | — | delete (interrupted write) |
| git-tracked | — | — | conflict **tracked** (not overridable, even with `--force`) |
| missing | known | present | rewrite (not a conflict) |

Write protocol: `flock .bob/gen/lock` → manifest with `pending` → per file: temp in the same
dir, write, fsync, chmod 0444 (0555 if exec), rename, fsync dir → delete stale files and empty
dirs → write the dir's `.gitignore` (`# Generated by bob. Do not edit files in this directory:
change the inputs and run \`bob gen\`.` + `*`) → final manifest (atomic; skipped if unchanged).
A root with conflicts is skipped entirely, others proceed, exit status 3 (`E-GEN-CONFLICT` = BOB-E-1016,
S6 §3.5 text). `--force` renames conflicting files to `.bob/gen/backup/<UTC time>/<path>` first;
nothing is deleted. `.bob/.gitignore` = `*`.

When: `bob gen [patterns]` (all codegen, prunes roots no longer defined), `bob gen --check`
(no build, no write; exit 3 with BOB-E-1011 unless all fresh), `bob build` (codegen in the targets' closure;
never prunes; `note: N generated directories are out of date but not needed by this build … run bob gen`),
`bob dev` (on entry and on every change of a codegen input or bob.go; conflicts printed,
watching continues). Remote workers and pipeline runs never materialise. `bob status` lists
`fresh | stale | missing | edited | orphan` per root. Source import excludes owned dirs, so
materialising never invalidates a build.

#### R-FW-066 Materialisation never overwrites foreign or edited files
- **Acceptance:** In a git copy of the codegen fixture: (a) editing a generated file and adding
  a foreign file to the same root makes `bob gen` exit 3 (BOB-E-1016) with the S6 conflict text (golden),
  leave that root untouched and update other roots; (b) `--force` moves both to the backup dir
  and rewrites; (c) a git-tracked file in a root is refused with and without `--force`;
  (d) removing a recipe from bob.go removes its root (files, `.gitignore`, empty parents);
  (e) deleting the manifest and rerunning adopts identical files without writing; (f) killing
  bob (SIGKILL) at each step of the write protocol (fault injection) and rerunning leaves no
  temp file and a consistent manifest.
- **Source:** R-FW-007, R-GEN-004, D25, D2, S6 §3.5.

#### R-FW-067 Editors see generated code
- **Acceptance:** After `bob gen`, gopls (through the shim, `-tags=bob`) resolves an import of
  `example.com/svc/gen/proto/greet/v1` and tsserver resolves `./gen/proto/greet/v1/greet_pb.js`
  with 0 diagnostics; in `bob dev`, a `.proto` edit reaches the tree within the budget (§03.8).
- **Source:** R-FW-007, R-DEV-002.

---

## §03.7 bob.lock entry schemas (summary for §01)

All types live in `pkg/engine/lock`; JSON keys are exactly the struct tags above; maps are written
with sorted keys, arrays in the documented order; hashes are SRI strings (`sha256-…`,
`sha512-…`) except Go `h1:` and the opam file digest. Every section has `inputs` (§03.0.3).

| Section key | Type | Entry types | Defined in |
|---|---|---|---|
| `go` | `lock.GoSection` | `lock.GoBuildList`, `lock.GoModule` | §03.2.4 |
| `rust` | `lock.RustSection` | `lock.Crate`, `lock.CrateDep`, `lock.CrateMeta` | §03.3.2 |
| `npm` | `lock.NpmSection` | `lock.NpmImporter`, `lock.NpmPackage` | §03.4.2 |
| `opam` | `lock.OpamSection` | `lock.OpamRepository`, `lock.OpamPackage` | §03.5.2 |
| `cc` | `lock.CCSection` | `lock.CExternal` | §03.7.6 |
| (embedded in bob) | `lock.ToolLock` | as `NpmSection` | §03.6.5 |

All lock content is hostile input when read (§5.4 BOOTSTRAP): size cap 64 MiB per bob.lock,
every name, version and hash validated against its ecosystem's grammar before use.

### §03.7.6 `lock.CExternal`

```go
type CCSection struct {
	Inputs    string               `json:"inputs"`
	Externals map[string]CExternal `json:"externals"` // key: label ("//third_party/zstd:zstd")
}
type CExternal struct {
	URL     string `json:"url"`
	Strip   int    `json:"strip"`
	NarHash string `json:"narHash"` // SRI sha256 of the unpacked, stripped, mode-normalised tree
}
```

```json
{"cc": {"inputs": "sha256-…", "externals": {"//third_party/zstd:zstd": {
  "url": "https://github.com/facebook/zstd/releases/download/v1.5.7/zstd-1.5.7.tar.gz",
  "strip": 1, "narHash": "sha256-…"}}}}
```

#### R-FW-065 Lock files round-trip and are deterministic
- **Acceptance:** For every fixture, `bob lock` twice gives byte-identical bob.lock; decoding
  and re-encoding each section is byte-identical; a fuzz test of each section decoder
  (10⁵ iterations in the slow suite) never panics and rejects invalid hashes/names.
- **Source:** R-FW-004, R-FW-008, BOOTSTRAP §5.4.

---

## §03.8 Ecosystem budgets **[proposal]**

From `docs/spikes/measure/quiet/*.log` (2026-10-21) unless noted. Cells: [8c/15G] / [2c/4G].
Stores warm (toolchains, std, third-party outputs substituted or built) unless "cold".

| Budget | [8c/15G] | [2c/4G] | Measured (quiet) |
|---|---|---|---|
| **Go** no-op build, fixture | ≤ 0.5 s | ≤ 0.5 s | hybrid 0.26 / 0.27 s |
| Go one-file change in cgo main package (fixture) | ≤ 4 s | ≤ 4 s | 2.87 / 2.89 s |
| Go library change with dependents (fixture `internal/msg`) | ≤ 4.5 s | ≤ 4.5 s | 3.28 / 2.99 s |
| Go bottom-layer change, 200-pkg workspace (36 dependents) | ≤ 4.5 s | ≤ 4.5 s | 3.03 / 3.12 s |
| Go graph → derivations, 1,000 packages, new / again | ≤ 5 / ≤ 2 s | ≤ 5 / ≤ 2 s | 3.53/1.33, 3.43/1.19 s |
| Go cold fixture incl. std build | ≤ 40 s | ≤ 75 s | 25.0 / 48.7 s |
| Go cold fixture, std cached | ≤ 10 s | ≤ 11 s | 6.11 / 7.15 s |
| Go peak RSS of bob during a build | ≤ 300 MB | ≤ 300 MB | ≤ 160 MB (S2, busy) |
| **Rust** no-op, 49-crate demo | ≤ 1 s | ≤ 1 s | 0.56 / 0.47 s |
| Rust leaf crate change | ≤ 4.5 s | ≤ 7 s | 2.92 / 4.60 s |
| Rust local lib change (with dependents) | ≤ 5.5 s | ≤ 8 s | 3.80 / 5.36 s |
| Rust cold demo vs `cargo build --release` | ≤ 1.3× | ≤ 1.3× | 48.3/40.7 = 1.19×, 130.9/119.3 = 1.10× |
| Rust per-derivation cost (trivial crate chain) | ≤ 0.25 s | ≤ 0.3 s | 0.146 s (chain30 [8c]) |
| Rust plan + derivations, demo (no build) | ≤ 1 s | ≤ 2 s | 0.49 s [8c] |
| Rust `bob lock`, 392 crates, warm index | ≤ 2 s | ≤ 4 s | 0.85 s offline [8c]; 1.9 s live (S3, busy) |
| **npm** `bob lock`, ~200 packages, warm cache (revalidation) | ≤ 4 s | ≤ 5 s | 2.90 s ts-lib [8c]; 3.4 s [2c] (S4, busy) |
| npm `bob lock`, inputs unchanged | ≤ 0.5 s | ≤ 1 s | offline 0.11–0.42 s [8c] |
| npm materialise node_modules, no-op | ≤ 0.75 s | ≤ 0.75 s | 0.58 s [8c]; 0.42 s [2c] (busy) |
| npm materialise after adding one dependency | ≤ 1.5 s | ≤ 1.5 s | 0.87 s [8c] |
| npm cold node_modules, ~66 packages, tarballs fetched | ≤ 6 s | ≤ 8 s | 4.79 s [8c] |
| npm Vite site, one source file changed (`vite build`) | ≤ 7 s | ≤ 12 s **[unmeasured 2c]** | 5.79 s [8c] |
| npm save → HMR update (server side), p95 | ≤ 50 ms | ≤ 50 ms | median 2.4–3.3 ms, max 29 ms (S4, busy) |
| npm tsserver first diagnostics through a shim | ≤ 4 s | ≤ 6 s | 2.1–2.6 s [8c] (S4, busy) |
| **OCaml** cold demo (11 opam + C + lib + exe) | ≤ 25 s | ≤ 40 s | 15.9 / 25.5 s |
| OCaml no-op | ≤ 0.5 s | ≤ 0.5 s | 0.33 / 0.29 s |
| OCaml executable source change | ≤ 2.5 s | ≤ 2.5 s | 1.46 / 1.36 s |
| OCaml library change (lib + exe) | ≤ 3 s | ≤ 3 s | 1.86 / 1.77 s |
| OCaml resolve, ≤ 110-package graph | ≤ 0.5 s | ≤ 1 s | 0.15 s (S5, busy) |
| **codegen** `bob gen` no-op | ≤ 0.3 s | ≤ 0.3 s | 0.16 / 0.16 s |
| codegen `bob dev` proto edit → Go+TS files in tree | ≤ 2.5 s | ≤ 2.5 s | 1.39 / 1.35 s |
| codegen whitespace-only proto change, full build (cutoff) | ≤ 2.5 s | ≤ 2.5 s | 1.43 / 1.36 s |
| codegen `bob status` | ≤ 0.3 s | ≤ 0.3 s | 0.13 / 0.13 s |
| **C/C++** no-op build, fixture | ≤ 0.6 s | ≤ 0.8 s | backend B fixture 0.33 / 0.45 s (`b-quiet.md`) + eval no-op; = §01 §1.17 polyglot row |
| C/C++ synthetic 100-package cold build | ≤ 15 s | ≤ 15 s | 10.0 / 9.4 s (`b-quiet.md`) |

Budgets marked "busy" derive from spike runs on a loaded host and must be re-measured on the
quiet profile in Phase 3.1 before Gate H. The harness runs these with the budget tolerance of
§01/HARNESS.

---

## §03.9 Open points

- **[open] Generated-file ignore mechanism** (§03.0.2): `.git/info/exclude` block; recommended.
- **[open] R-CC-004 per-object C/C++ derivations** (§03.1.3): per-library in v1; measure.
- **[open] R-GO-005 default for Go export-data cutoff**: off until the Stage 3 measurement.
- **[open] Go test main** (§03.2.3): bob's own `_testmain.go` template; fallback `go test -c`.
- **[open] Second toolchain versions** (R-RS-004a, R-GO-004a): need §01 to allow more than one
  nixpkgs pin; recommendation: named extra pins in bob.go, toolchains only from pins.
- **[open] SvelteKit** (R-NPM-007a): `svelte-kit sync` in build and dev, unverified.
- **[open] OpenAPI TypeScript** (R-GEN-003a): openapi-typescript tool lock, unbuilt.
- **[open] `proto.Breaking` against a git revision** needs §01 source import from git objects;
  recommendation: bob reads the revision's files with `git archive` into a store path.
- cc.External from git: same item as §01 Open point 2 (listed once there).
- Rust lock layout: closed at integration — §01 R-FW-004 fixes the canonical form (2-space
  indent, sorted keys, arrays in schema order), so each crate is its own object block.

## Escalations

None. All owner decisions relevant here (D2, D15, D25, D30–D34, D44, D58, D59) are applied;
O-DX-1 is superseded by D59 and the §02 LSP proxy.

# §04 Outputs and platforms


Scope: cross-compilation rules, toolchain selection, variant projection per package kind, OCI
images, AppImages, tarballs, closures as outputs, releases as built objects, the consumer side of
`bob install` (profile model), and output size/time budgets. ADRs: 0009 (cross part), 0010.

## Interfaces

- **§01 provides** `bob.P`, `bob.Define`, `bob.Platform` (`LinuxAMD64`, `LinuxARM64`, `Host()`),
  `bob.Variant` and its label grammar (`//pkg:name@<platform>+<token>…`), `bob.Package`
  (anything with store outputs: `Outputs() []bob.Output`), `bob.Executable` (a `bob.Package`
  with `MainProgram() string`, a path relative to its output), the derivation layer
  (`drv.Derivation`, structured attributes, `Name`), `bob-builder` subcommands, the per-package
  fixup step (§01 §1.13: strip; references are prevented with prefix maps such as
  `--remap-path-prefix`/`-trimpath` instead of rewritten, and a forbidden reference fails with
  BOB-E-9003), and the embedded Nix config.
  **Required from §01:** the derivation layer supports `__structuredAttrs` with
  `unsafeDiscardReferences` (R-OUT-011). No experimental feature is needed: `discard-references`
  is stable in Nix 2.34 (enabling it only warns `unknown experimental feature`; §01 R-NIX-024).
- **§01 provides** `nixpkgs.Lib(p, attr)` / `nixpkgs.Tool(p, attr)` typing (this section
  defines the semantics, R-FW-032) and the nixpkgs index lookup (`cached on cache.nixos.org?`).
- **§02 provides** CLI syntax for `--platform`, `--variant`, `bob install|upgrade|rollback|uninstall|list`,
  `bob exec`, JSON event output, the error template (`✗ <title>` + position + hint).
- **§03 provides** per-ecosystem builders that consume `Toolchain` (R-FW-033) and
  `Projection` (R-FW-038): `cc`, `golang`, `rust`, `npm`, `ocaml`.
- **§05 provides** the worker upload of a job's outputs into `staging/<buildID>/<jobID>/<attempt>/…` (§05.2.4; `COMPLETE.json` schema in §06 §6.6.4)
  (`COMPLETE.json` with `images[]`) and the release state machine.
- **§06 provides** promotion (verify, sign, publish), the OCI registry and Nix cache endpoints,
  the download proxy (D61), `ReleaseService` (`GetRelease`, `ListReleases`, `ResolveRelease`) and release
  visibility.
- **§04 provides** `oci.Image`, `appimage.Bundle`, `tarball.Tarball`, `release.Release`, the
  `bob.release/v1` manifest (R-OUT-023), the OCI layout in the store (R-OUT-011), the profile
  layout (R-INST-005) and the `Toolchain`/`Projection` types.

## 4.1 Platforms

#### R-PLAT-001 Supported systems
- **Statement:** bob builds for, runs on, and accepts as host and target exactly `linux-amd64`
  (Nix system `x86_64-linux`) and `linux-arm64` (`aarch64-linux`).
- **Acceptance:** Given `bob build //x:y@linux-riscv64`, then exit code 2 and stderr
  `✗ unknown platform "linux-riscv64" (known: linux-amd64, linux-arm64)`; no Nix process starts.
  Given the CLI started on a host whose `uname -m` is neither `x86_64` nor `aarch64`, then
  `✗ unsupported host architecture "<m>" (bob runs on linux-amd64 and linux-arm64)`, exit 2.
- **Source:** R-PLAT-001, D4.

#### R-PLAT-002 darwin stays possible
- **Statement:** `bob.Platform` is a string type with a closed v1 value set; no API exposes
  Linux-only types (e.g. ELF details) in public signatures except in `appimage`.
- **Acceptance:** (manual) review: adding `darwin-arm64` needs no change to a public signature
  outside `pkg/output/appimage`.
- **Source:** R-PLAT-002.

#### R-PLAT-004 Host builds, target compiles
- **Statement:** Every bob-generated derivation has `system` = the **host** Nix system; the
  target platform changes only the toolchain, flags and target libraries that are inputs. No
  emulated builder, binfmt, or remote builder of the other architecture is used.
- **Acceptance:** Given the polyglot fixture evaluated for `@linux-amd64` on an arm64 host,
  then every generated `.drv` has `system = "aarch64-linux"`, and `bob build` succeeds with
  `extra-platforms` unset. Output binaries are `ELF 64-bit LSB … x86-64` (`debug/elf` check:
  `Machine == EM_X86_64`).
- **Source:** ADR 0009 §1, S7 §4.1.

#### R-PLAT-005 v1 platform × ecosystem matrix
- **Statement:** v1 supports, for C, C++, Go (pure and cgo) and Rust (lib, bin, staticlib,
  cdylib, proc-macro, build script): native arm64, native amd64, arm64→amd64, amd64→arm64.
  npm/Node outputs are platform-independent JS plus a target `nodejs` (`nixpkgs.Lib`); OCaml is
  native-only in v1 (`@<other platform>` → error below).
- **Acceptance:** CI builds every fixture repo (BOOTSTRAP §6.4) for both platforms on a host of
  each architecture (4 combinations); cross outputs run under `qemu-<arch>` (R-FW-039) and print
  the golden line. OCaml cross request: `✗ //ml:tool: OCaml packages cannot be cross-compiled
  yet (requested linux-amd64 on a linux-arm64 host)` + hint `build it on a linux-amd64 worker
  with --remote`, exit 2.
- **Source:** R-FW-030, R-GO-001, R-RS-001, S7 §5.5. amd64→arm64 is unverified (S7 had no
  amd64 host) → Stage 3 verification task.

#### R-PLAT-006 Per-package platform restriction
- **Statement:** Any package option struct may set `Platforms []bob.Platform`; requesting a
  package (directly or as a dependency) for another platform is an evaluation error.
- **Acceptance:** Given `cc.Library(p, "foo", cc.LibraryOpts{Platforms: []bob.Platform{bob.LinuxARM64}})`
  and `bob build //app:app@linux-amd64` where app depends on foo, then exit 2 and
  `✗ //libs/foo:foo does not support linux-amd64 (it allows: linux-arm64)` with foo's `bob.go`
  position and the dependency chain `//app:app → //libs/foo:foo`.
- **Source:** S7 §5.1.

#### R-PLAT-007 glibc and rustc consistency pre-check
- **Statement:** Before generating derivations for a cross target, bob checks (from the nixpkgs
  index, no build) that (a) the cross glibc and the target system's native glibc have the same
  version when target libraries come from the native set, and (b) the host `rustc` and the
  target system's `rustc` report the same version string.
- **Acceptance:** Given a test index where `legacyPackages.x86_64-linux.glibc.version = "2.42"`
  and the cross glibc is `"2.43"`, then `✗ nixpkgs mismatch: target libraries for linux-amd64
  use glibc 2.42 but the cross toolchain uses glibc 2.43` with hint `set Libs: cc.CrossLibs on
  the packages that link them, or move the nixpkgs pin`. Same for rustc (`rustc 1.95.0` vs
  `…`), hint `the nixpkgs pin must provide the same rustc for both systems`.
- **Source:** S7 §5.4, §6 risks.

## 4.2 Toolchains and dependencies

#### R-FW-032 Typed nixpkgs dependencies
- **Statement:** nixpkgs dependencies are typed: `nixpkgs.Tool(p, attr)` (runs at build time:
  always taken from the host's package set) and `nixpkgs.Lib(p, attr)` (linked into or shipped
  with the output: taken from the target set per R-FW-036). A `Tool` is not a `cc.Linkable`;
  a `Lib` is not a `bob.Tool` (compile-time type error).
- **Acceptance:** Golden test: `//api:api@linux-amd64` on arm64 references
  `legacyPackages.aarch64-linux.protobuf` for `Tool("protobuf")` and
  `legacyPackages.x86_64-linux.openssl` for `Lib("openssl")`. A `bob.go` passing
  `nixpkgs.Tool(p,"openssl")` in `cc.LibraryOpts.Deps` fails `go build` (example program in the
  error-quality suite asserts the bob-translated compile error).
- **Source:** S7 §3.2 "untyped nixpkgs deps", STATUS findings (S7).

```go
package nixpkgs // pkg/nixpkgs

// Lib is a nixpkgs package used as a target library: linked into, or shipped with, an
// output. For a cross target it comes from the target system's package set (default) or
// the pkgsCross set (cc.CrossLibs). Lib values satisfy cc.Linkable and bob.Package.
func Lib(p *bob.P, attr string) *Package

// Tool is a nixpkgs package that only runs during a build (code generators, compilers,
// build-time utilities). It always comes from the host's package set and never enters an
// output's closure unless the build output references it.
func Tool(p *bob.P, attr string) *ToolPackage
```

Example:

```go
var Gen = bob.Define(func(p *bob.P) *bob.RunStep {
	return bob.Run(p, "schema", bob.RunOpts{
		Tools: []bob.Tool{nixpkgs.Tool(p, "protobuf")},
		Cmd:   []string{"protoc", "--descriptor_set_out=$out/schema.pb", "schema.proto"},
		Srcs:  bob.Glob("schema.proto"),
	})
})
```

#### R-FW-033 Toolchain selection
- **Statement:** For each (ecosystem, host, target) bob selects the toolchain by the table
  below; `Toolchain` values are computed once per (host, target, ecosystem, toolchain version)
  per evaluation and shared by all packages.
- **Acceptance:** Golden derivations for the fixture on both hosts × both targets contain the
  attributes, env vars and flags of the table. Two packages of the same kind and target share
  one toolchain derivation path (graph test).
- **Source:** ADR 0009 §2, S7 §5.4.

| Ecosystem | Native | Cross host→target |
|---|---|---|
| C/C++ | `stdenv.cc` of the host set; `gcc`, `g++`, `ar` | `pkgsCross.<gnu64 \| aarch64-multiplatform>.stdenv.cc`; commands `<triple>-gcc`, `<triple>-g++`, `<triple>-ar`; triple `x86_64-unknown-linux-gnu` / `aarch64-unknown-linux-gnu` |
| Go | host `go` | host `go`; env `GOOS=linux GOARCH=<amd64\|arm64> CC=<triple>-gcc CXX=<triple>-g++`; link `-extld=<triple>-gcc`; tools from `pkg/tool/linux_<hostarch>`; every cgo package derivation has `CC` set (cgo calls `$CC`) |
| Go std | one derivation per (go version, target, cgo on/off, Go projection) | same; compiled package by package by bob (no `go build std`, which rejects prefixed compilers for `-asan`/`-race`) |
| Rust | host `rustc` | host `rustc --target <triple> --sysroot <rust-std-<triple>> -C linker=<triple>-cc` |
| C/C++ target libs | host set `<attr>` | R-FW-036 |
| Node | `nodejs` (Lib) | `legacyPackages.<target-system>.nodejs` |

```go
package bob

// Toolchain describes the compilers an ecosystem uses for one (host, target) pair.
// Builders in pkg/<ecosystem> receive it from p.Toolchain; users never construct one.
type Toolchain struct {
	Host, Target Platform
	Triple       string        // "x86_64-unknown-linux-gnu"; empty for native
	CC           drv.Ref       // C compiler wrapper derivation
	Prefix       string        // "x86_64-unknown-linux-gnu-" for cross, "" for native
}

// Toolchain returns the C/C++ toolchain for the package's target. Example:
//   tc := p.Toolchain()          // inside a bob.Define body
//   _ = tc.Prefix + "gcc"
func (p *P) Toolchain() Toolchain
```

#### R-FW-034 Rust target std
- **Statement:** For a cross target, bob builds a derivation `rust-std-<triple>` whose output is
  exactly the files listed in `lib/rustlib/manifest-rust-std-<triple>` of
  `legacyPackages.<target-system>.rustc.unwrapped`, laid out as a sysroot. Upstream
  `rust-std` tarballs are never used.
- **Acceptance:** `//rdemo:rdemo@linux-amd64` on arm64 builds and under `qemu-x86_64` prints
  `rdemo target=x86_64-unknown-linux-gnu answer=42 bits=64 x86cfg=true`. A test asserts no
  derivation fetches from `static.rust-lang.org`.
- **Source:** S7 §3.1 (E0514 with upstream std).

#### R-FW-035 Host units
- **Statement:** Rust proc-macros, build-script compilations and their dependency crates are
  generated by a host-role generator, also for native builds: their derivation depends only on
  the host platform and the crate's features, never on target or profile (always opt-level 0).
  Build-script **runs** are per target and profile (`TARGET`, `CARGO_CFG_*`, `PROFILE`,
  `OPT_LEVEL`, `DEBUG`, `CARGO_FEATURE_*`).
- **Acceptance:** Graph test on one host: `rmac` (proc-macro) and `rdemo-build-script` have the
  same `.drv` path in `@linux-arm64`, `@linux-amd64`, `+debug`, `@linux-amd64+debug`.
- **Source:** ADR 0009 §3, S7 §3.3.

#### R-FW-036 Source of target C/C++ libraries
- **Statement:** `nixpkgs.Lib` for a cross target defaults to `legacyPackages.<target-system>.<attr>`
  (`cc.NativeLibs`); a package may set `Libs: cc.CrossLibs` to use `pkgsCross.<x>.<attr>`
  instead. Before building, bob reports target libraries that are not on cache.nixos.org.
- **Acceptance:** Default: `//api:api@linux-amd64` closure contains both
  `glibc-x86_64-unknown-linux-gnu-2.42-*` and `glibc-2.42-*` and runs. With `cc.CrossLibs`:
  only the cross glibc. Given an uncached Lib, then before any build the CLI prints
  `note: openssl for linux-amd64 is not in the binary cache and will be built from source (several minutes)`
  (JSON event `{"type":"note","code":"uncached-target-lib","attr":"openssl","platform":"linux-amd64"}`).
- **Source:** A19, S7 §3.1 (pkgsCross openssl 369 s), ADR 0009 §2.

#### R-FW-037 Cross sanitiser runtime path
- **Statement:** For sanitised C/C++/cgo binaries on a cross target, bob adds the cross
  `gcc-lib` `lib` directory to the binary's RUNPATH (link flag `-Wl,-rpath,<gcc-lib>/lib`).
- **Acceptance:** `readelf -d` of `//api:api@linux-amd64+asan` lists the cross gcc-lib dir in
  `RUNPATH`; on an amd64 worker the binary starts without `LD_LIBRARY_PATH` and prints the
  golden line.
- **Source:** S7 §3.2 (nixpkgs packaging gap), ADR 0009 open item.

## 4.3 Variants

#### R-FW-031 Build variants
- **Statement:** A variant is a typed configuration (profile, sanitisers, Rust features, named
  variants) defined in §01; it changes a package's identity only through the package's
  **projection** (R-FW-038) and propagates to dependencies as the projection table says.
- **Acceptance:** `matrix` test over the fixture's 11 configurations of S7 §3.3: the number of
  distinct derivations equals the golden (S7: 99, ±0 after the fixture is frozen), and no two
  configurations with different projections of one package yield the same path.
- **Source:** R-FW-031, D28, ADR 0009 §4.

#### R-FW-038 Projection per package kind
- **Statement:** Each package kind sees only the variant fields of the table; only the
  projection enters its derivation (env, args, `-C metadata`, name). Dropped fields produce a
  **note**, never an error.
- **Acceptance:** Golden table test: for each row, flipping a field marked `–` leaves the
  `.drv` path unchanged; flipping a `✔` field changes it. Requesting `+asan` for a Rust-only
  target prints `note: //rcore:rcore is built without asan (not supported for Rust in v1)`.
- **Source:** S7 §5.3.

| Package kind | Platform | Profile | ASan/UBSan | Go race | Rust features |
|---|---|---|---|---|---|
| fetch / unpack (bob-imported source paths, FOD-identical; §01 R-NIX-008) | – | – | – | – | – |
| codegen (host tool) | – | – | – | – | – |
| `nixpkgs.Tool` | – | – | – | – | – |
| `nixpkgs.Lib` | ✔ | – | – | – | – |
| C/C++ object/library/binary | ✔ | ✔ `-O2` / `-O0 -g` | ✔ `-fsanitize=<list> -fno-omit-frame-pointer` | – | – |
| Go std, Go package (pure) | ✔ | ✔ (`-N -l`) | ASan only (`-asan`) | ✔ | – |
| Go cgo package, Go link | ✔ | ✔ | ✔ (all sanitiser runtimes of linked C deps) | ✔ | – |
| Rust target crate | ✔ | ✔ opt 2 / opt 0 + debuginfo + debug-assertions | – | – | own crate |
| Rust host unit | host | – (opt 0) | – | – | own crate |
| Rust build-script run | ✔ | ✔ | – | – | ✔ |
| npm/TS compile, bundle | – | ✔ (`mode=development` / `production`) | – | – | – |
| npm app runtime (`nodejs` Lib) | ✔ | – | – | – | – |
| OCaml | host | ✔ (`-g` / `-O3` with flambda if available, else `-g` off) | – | – | – |
| OCI image, AppImage, tarball, release | union of their roots' projections (they are rebuilt only if a root changes) | | | | |

- Derivation name: `<dir-label>-<name>[-host][-<platform if cross>][-<tag>]`, tag = projection
  tokens joined by `-` in canonical order (`dbg`, `address`, `undefined`, `race`, `f-<hash8>`
  for features), e.g. `libs-foo-foo-linux-amd64-address`. Length ≤ 200 bytes (Nix limit 211);
  longer names are cut and end in `-<hash8>`.

#### R-FW-039 Foreign-architecture and sanitiser tests
- **Statement:** A test package for platform T runs on a worker of T; with none available
  (local build or no such worker), bob runs it under `qemu-<arch>` (nixpkgs `qemu-user`,
  `nixpkgs.Tool`) unless the test has a sanitiser or `race` projection; those report
  `not run: needs a native linux-amd64 machine` (status `skipped`, exit 0 unless
  `--require-all-tests`, then exit 1).
- **Acceptance:** On arm64 locally, `bob test //api:test@linux-amd64` passes via qemu
  (JSON event `{"type":"test","runner":"qemu-x86_64",…}`); `…@linux-amd64+asan` → status
  `skipped` with the text above. On the server, the scheduler (§05) assigns T-tests to T-workers
  when any is registered.
- **Source:** A18, S7 §3.2.

#### R-PLAT-008 Cross build budgets **[proposal]**
- **Statement / Acceptance:**
  - Cold cross toolchain (C cross cc + rust target std) substitution for one target:
    ≤ 700 MiB download, ≤ 15 s [8c/15G] (quiet: cc wrapper 2.2 s, rustc 6.6 s, qemu-user 1.0 s).
  - Cold build of fixture `//api:api` with toolchains present: native ≤ 35 s [8c/15G] /
    ≤ 75 s [2c/4G] (quiet 25.2 / 56.9 s); `@linux-amd64` ≤ 35 / 75 s (24.7 / 55.5 s);
    `+asan` ≤ 35 / 85 s (26.6 / 63.8 s); `+debug` ≤ 30 / 60 s (21.6 / 45.4 s).
  - Adding a variant to a built configuration rebuilds only projection-affected derivations
    (graph assertion) and the Go std per new (platform, Go projection) ≤ 60 s [2c/4G].
- **Source:** `docs/spikes/measure/quiet/s07-cross.log`; budgets = quiet × 1.3, rounded up.

## 4.4 Outputs in general

#### R-OUT-001 Closure as output
- **Statement:** Every `bob.Package` is a closure output: `bob build` makes its outputs
  available in bob's store, `bob install` installs `release.Closure` artifacts, and the worker
  uploads closures to the cache via §05/§06.
- **Acceptance:** `bob build //cmd/api:api --json` emits `{"type":"result","label":"//cmd/api:api","outputs":{"out":"/nix/store/…"}}`;
  `bob build --remote` of the same label then has the path substitutable from the server
  (`nix path-info --store <server>` in the harness).
- **Source:** R-OUT-001, R-NIX-003.

#### R-OUT-006 Bundles are build targets
- **Statement:** `oci.Image`, `appimage.Bundle` and `tarball.Tarball` are `bob.Package`s whose
  outputs are store paths built by `bob-builder` subcommands (`oci-layer`, `oci-image`,
  `appimage`, `tarball`) in ordinary derivations; they cache, upload and promote like any
  package. The builders never run Docker, a daemon, or network access.
- **Acceptance:** `bob build //deploy:image` twice → second run is a no-op (no derivation built).
  Building in the sandbox with network disabled succeeds.
- **Source:** ADR 0010, S8 §5.1.

#### R-OUT-007 Per-output pruning
- **Statement:** Bundles may remove files of nixpkgs paths from **their copy** of the closure
  through `Prune` globs (matched against `/nix/store/<hash>-<name>/<path>`; `*` does not cross
  `/`, `**` does). nixpkgs store paths themselves are never modified. A pruned file that the
  bundle's ELF files need (`DT_NEEDED` resolution) is a build error.
- **Acceptance:** AppImage of fixture `api` with `appimage.DefaultPrune` ≤ 12 MB and runs.
  Given `Prune: []string{"/nix/store/*-openssl-*/lib/libssl.so*"}`, then
  `✗ //deploy:bundle: prune pattern "/nix/store/*-openssl-*/lib/libssl.so*" removes libssl.so.3, which api needs`.
- **Source:** S8 §3.1, §5.4, S8-7.

#### R-OUT-008 Licence manifest
- **Statement:** Every bundle contains `licences.json` (AppImage: `.bob/licences.json`; OCI:
  config label `org.opencontainers.image.licenses` = SPDX expression of the roots, plus a file
  `/.bob/licences.json` in the customisation layer; tarball: `bob/licences.json`):
  `[{"path":"<store path>","name":"<pname>","version":"…","licenses":["<SPDX id>",…]}]` from
  nixpkgs `meta.license` (bob paths: the `License` field of their package, default `"unknown"`).
- **Acceptance:** The fixture AppImage's `licences.json` lists glibc with `LGPL-2.1-or-later`
  and openssl with `Apache-2.0`; entries sorted by path.
- **Source:** S8 §5.8, D16, D52.

## 4.5 OCI images

#### R-OUT-002 OCI images from closures
- **Statement:** `oci.Image` produces an OCI image layout (`oci-layout`, `index.json`,
  `blobs/sha256/…`) with one image index over the requested platforms (default amd64 +
  arm64, cross-built per R-PLAT-004), written by bob's own Go code (stdlib `archive/tar`,
  `compress/gzip`, `encoding/json`; no go-containerregistry, no Docker).
- **Acceptance:** `crane validate --remote` of the pushed fixture index → PASS for each platform;
  index media type `application/vnd.oci.image.index.v1+json`, platforms
  `[{linux,amd64},{linux,arm64}]` sorted amd64 first; rootless `podman run` (bobtest) of the
  host-platform image prints the golden line; `id` → `uid=65534(app) gid=65534(app)`.
- **Source:** R-OUT-002, D3, ADR 0010 §1, S8 §3.2.

```go
package oci // pkg/output/oci

// Image is an OCI image built from closures, without Docker. Each platform gets one image
// manifest; the result is an image index. Program and Extra are built for each platform.
type Image struct{ /* unexported */ }

// ImageOpts configures an image. Zero values give the defaults in the field comments.
type ImageOpts struct {
	Program    bob.Executable      // closure root; Entrypoint defaults to its MainProgram
	Extra      []bob.Package       // more roots (e.g. a debug shell); each gets its own layer
	Entrypoint []string            // default: []string{<Program out>/<MainProgram>}
	Cmd        []string
	Env        map[string]string   // merged over the defaults PATH, SSL_CERT_FILE, TZDIR
	User       string              // default "65534:65534"; "0:0" must be written explicitly
	WorkDir    string              // default "/"
	Ports      []int               // ExposedPorts "<n>/tcp"
	Labels     map[string]string   // org.opencontainers.image.* from a release are added
	NoCACerts  bool                // default false: nss-cacert + /etc/ssl/certs symlinks
	NoTZData   bool                // default false: tzdata + /usr/share/zoneinfo symlink
	Platforms  []bob.Platform      // default {LinuxAMD64, LinuxARM64}
	MaxLayers  int                 // default 100, allowed 2..125
	Prune      []string            // R-OUT-007; default none
}

// NewImage declares an image. Example:
//
//	var Image = bob.Define(func(p *bob.P) *oci.Image {
//		return oci.NewImage(p, "api", oci.ImageOpts{
//			Program: api.Binary(p),
//			Ports:   []int{8080},
//		})
//	})
func NewImage(p *bob.P, name string, opts ImageOpts) *Image
```

#### R-OUT-009 Layer algorithm
- **Statement:** Per platform: (1) closure of roots (Program + Extra); (2) popularity(p) =
  number of closure paths whose closure contains p; (3) non-root paths sorted by
  (popularity desc, path asc); with `L = MaxLayers − 1 − |roots|`, the first `L−1` get one
  layer each, the rest share one layer; (4) one layer per root in root order; (5) the
  customisation layer (R-OUT-012) last.
- **Acceptance:** Fixture `api` v1 → 14 layers; v2 (one string changed in `api`) differs from
  v1 in exactly one layer blob and the config/manifest; new compressed bytes ≤ 16 % of the image
  (S8: 3.78 / 25.4 MB). With `MaxLayers: 4`: 4 layers, the root alone in layer 3.
- **Source:** S8 §3.2, §5.2, S8-2.

#### R-OUT-010 Reproducibility
- **Statement:** Image bytes depend only on the closure contents and options. Tar: lexical
  order, parents first, each layer includes `nix/` and `nix/store/` dirs (0555, uid 0),
  `mtime` = 1 s since epoch, uid/gid 0, no uname/gname, permissions from the store, symlinks as
  symlinks, hard links as regular files, no xattrs, format chosen by `archive/tar`. Gzip:
  `compress/gzip` `DefaultCompression`, no name, `ModTime` zero. Config: no `created`;
  `history[i].created` = `1970-01-01T00:00:01Z`; JSON from structs (`encoding/json`, fixed
  field order). Layer identity = `diff_id`; promoted blobs are reused by `diff_id` and never
  recompressed.
- **Acceptance:** Build the fixture image twice into fresh stores (different machines in the
  nightly suite, `[8c/15G]` and `[2c/4G]`) → identical index digest. Golden: index digest of
  the frozen fixture.
- **Source:** S8 §5.3, R-NFR-006.

#### R-OUT-011 Image layout in the store
- **Statement:** Each layer is its own derivation `oci-layer-<storehash8>-<name>`
  (`bob-builder oci-layer`), keyed by the store paths it contains, the prune set and the layer
  format version `bob-oci-layer-v1`; its output is `layer.tar.gz` + `layer.json`
  (`{"diffID","digest","size","paths":[…]}`) and it sets `unsafeDiscardReferences.out = true`
  (structured attrs), so a layer never pulls the paths it contains into its closure. The image
  derivation's output is an OCI layout whose layer blobs are symlinks to the layer outputs;
  config, manifests and index are regular files.
- **Acceptance:** `nix-store -q --references <layer out>` is empty; the image output's closure
  = image + its layers only (no glibc). `skopeo copy --all oci:<out>:<tag> docker://…` succeeds
  (follows symlinks). Changing only `api` rebuilds exactly one layer derivation plus the image
  derivation. Verified (integration, 2026-10-21) on Nix 2.34.8 and the embedded 2.34.9 with
  `experimental-features = nix-command` only (no `discard-references`):
  `unsafeDiscardReferences.out = true` produced an output containing an input's path with no
  registered reference; the control without it registered the reference.
- **Source:** S8 §5.2 (layer cache key), verification below.

#### R-OUT-012 Customisation layer and defaults
- **Statement:** The last layer contains: `/tmp` (1777), `/etc/passwd`
  (`root:x:0:0::/root:/sbin/nologin` and `app:x:65534:65534::/tmp:/sbin/nologin`),
  `/etc/group` (`root:x:0:`, `app:x:65534:`), `/etc/nsswitch.conf` (`hosts: files dns`),
  unless disabled: `/etc/ssl/certs/ca-certificates.crt` and `/etc/ssl/certs/ca-bundle.crt`
  symlinks to nss-cacert, `/usr/share/zoneinfo` → tzdata; `/.bob/licences.json`. Config env
  defaults: `PATH=<roots' bin dirs joined>:/usr/bin:/bin`, `SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt`,
  `TZDIR=/usr/share/zoneinfo`.
- **Acceptance:** In rootless podman: HTTPS GET to the harness's TLS server (CA from nss-cacert
  test chain) returns 200; `TZ=Europe/Zurich date -d @0` → `Thu Jan  1 01:00:00 CET 1970`.
- **Source:** S8 §3.2.

#### R-OUT-013 Images in releases and the registry
- **Statement:** When a build includes an `oci.Image`, the worker uploads its blobs (layers,
  configs, manifests, index) into `<staging>oci/blobs/sha256/<hex>` (`<staging>` = `staging/<buildID>/<jobID>/<attempt>/`) and lists the
  image in `COMPLETE.json.images[]` as `{"repo","tags","index":"sha256:…","mediaType","blobs":[…]}`;
  promotion and tags are §06. v1 layers are gzip only (`application/vnd.oci.image.layer.v1.tar+gzip`).
- **Acceptance:** Harness: release pipeline of the fixture → `podman pull <server>/acme/api:<version>`
  (service-account token) and `crane manifest` returns the same index digest as the local build.
- **Source:** A17, S12 §5.1, R-REG-003.

## 4.6 AppImages

#### R-OUT-003 AppImage
- **Statement:** `appimage.Bundle` produces one `<name>-<arch>.AppImage` (`arch` = `x86_64` |
  `aarch64`) that runs on the hosts of R-OUT-018 without Nix, FUSE, or unprivileged user
  namespaces, bundling ld.so, glibc, all libraries and data of the closure.
- **Acceptance:** Distro matrix (R-OUT-018) green for the fixture `api` and `curl` bundles.
- **Source:** R-OUT-003, ADR 0010 §2.

```go
package appimage // pkg/output/appimage

// Bundle is a self-contained AppImage for one platform.
type Bundle struct{ /* unexported */ }

type BundleOpts struct {
	Program      bob.Executable            // main entry
	Entries      map[string]bob.Executable // extra entries, chosen by BOB_ENTRY or argv[0]
	Env          map[string]string         // "${STORE}" expands to the bundled store at run time
	NoCACerts    bool                      // default false: nss-cacert, SSL_CERT_FILE set
	Locales      []string                  // default {"C.UTF-8"}; others add a locale-archive
	NoIconv      bool                      // true prunes glibc lib/gconv (iconv only builtin charsets)
	HostGraphics bool                      // append host GPU library dirs (R-OUT-020)
	Desktop      *Desktop                  // nil → Terminal=true entry, built-in placeholder icon
	Platform     bob.Platform              // default host
	Prune        []string                  // default DefaultPrune; nil-vs-empty: []string{} = no pruning
	Mode         Mode                      // AutoMode (default), UsernsOnly
}

type Desktop struct {
	Name       string     // Name= in the .desktop file
	Icon       bob.Source // SVG or PNG, 256x256
	Categories []string
	Terminal   bool
}

// Mode restricts the launcher. UsernsOnly is required for programs that exec other
// store paths or are shebang scripts (R-OUT-019).
type Mode int

const (
	AutoMode Mode = iota
	UsernsOnly
)

// DefaultPrune is the prune set used when Prune is nil.
var DefaultPrune = []string{
	"/nix/store/*-glibc-*/share/i18n/**",
	"/nix/store/*-glibc-*/share/locale/**",
	"/nix/store/*-gcc-*-lib/lib/lib?san.so*",
	"/nix/store/*-gcc-*-lib/lib/libhwasan.so*",
}

// NewBundle declares an AppImage. Example:
//
//	var Bundle = bob.Define(func(p *bob.P) *appimage.Bundle {
//		return appimage.NewBundle(p, "api", appimage.BundleOpts{
//			Program:  api.Binary(p),
//			Platform: bob.LinuxAMD64,
//		})
//	})
func NewBundle(p *bob.P, name string, opts BundleOpts) *Bundle
```

#### R-OUT-015 AppImage file format and runtime
- **Statement:** File = bob runtime ELF (static Go, `CGO_ENABLED=0`, for the bundle's
  platform) + squashfs image appended at the ELF's end. Bytes 8–10 = `AI\x02`. The squashfs is
  written by nixpkgs `mksquashfs` (`nixpkgs.Tool`) inside the `appimage` derivation with
  `-all-root -no-xattrs -all-time 1 -mkfs-time 1 -comp zstd -Xcompression-level 19 -b 1M`.
  AppDir: `AppRun` (= the runtime binary, launcher mode), `<name>.desktop`, `<name>.svg|png`,
  `.DirIcon`, `.bob/launch.json`, `.bob/licences.json`, `nix/store/…`. The runtime never uses
  FUSE in v1: it extracts once to `$XDG_CACHE_HOME/bob/appimage/<sha256 of file>/` (fallback
  `~/.cache`, then `$TMPDIR/bob-appimage-<uid>/`) via a temp dir + `rename`, verifies a
  `.complete` marker, and then acts as launcher. Flags: `--appimage-extract [<glob>]` (to
  `./squashfs-root`), `--appimage-offset`, `--appimage-version`, `--appimage-help`.
- **Acceptance:** Byte-identical AppImage from two fresh builds (golden sha256 of the frozen
  fixture). `file` reports an ELF; `--appimage-offset` prints the squashfs offset and
  `unsquashfs -o <offset>` lists `AppRun`. First run extracts (≤ 0.5 s for the 10.8 MB fixture,
  [8c/15G]); second run starts ≤ 0.1 s; two concurrent first runs both succeed and leave one
  cache dir. The bundle contains no LGPL/GPL runtime code (licence check on the runtime binary's
  Go module graph).
- **Source:** A15, A16, S8 §3.3, §5.5. FUSE mount is **ROADMAP** (it needs a setuid
  `fusermount3`, absent on most targets; S8 §3.3).

`launch.json` schema (all paths are `/nix/store/…` paths inside the bundle):

```json
{
  "schema": "bob.appimage.launch/v1",
  "entry": "/nix/store/<h>-api/bin/api",
  "entries": { "curl": "/nix/store/<h>-curl-8.x/bin/curl" },
  "interp": "/nix/store/<h>-glibc-2.42/lib/ld-linux-aarch64.so.1",
  "libDirs": ["/nix/store/<h>-openssl-3.6.5/lib", "/nix/store/<h>-glibc-2.42/lib"],
  "env": ["SSL_CERT_FILE=${STORE}/<h>-nss-cacert/etc/ssl/certs/ca-bundle.crt",
          "GCONV_PATH=${STORE}/<h>-glibc-2.42/lib/gconv"],
  "mode": "auto",
  "hostGraphics": false
}
```

#### R-OUT-016 Launcher modes
- **Statement:** `BOB_APPIMAGE_MODE=auto|userns|ldso` (default from `launch.json.mode`).
  `userns`: new user + mount namespace mapping the caller's uid/gid, tmpfs root, bind every host
  top-level entry except `nix`, bind the bundled store at `/nix/store`, chroot, restore cwd,
  drop ambient caps, exec entry. `ldso`: exec bundled `interp --library-path <libDirs>[:<host LD_LIBRARY_PATH>] <entry>`
  with `${STORE}` = the extracted store. `auto` = userns, on any namespace setup error ldso.
  `BOB_APPIMAGE_DEBUG=1` prints the choice to stderr. Exit codes: 127 launcher error (message
  `bobrun: <text>`: `user namespace: <err>`, `unknown entry "<n>"`, `read launch.json: <err>`),
  child's code otherwise, 128+signal when the child is killed; SIGINT/TERM/HUP/QUIT forwarded.
- **Acceptance:** Matrix rows run in both modes; `BOB_APPIMAGE_MODE=userns` under
  `bwrap --disable-userns` → exit 127 with `bobrun: user namespace:`; `kill -TERM` of the
  AppImage process → child receives TERM, exit 143.
- **Source:** S8 §3.4, §5.5, S8-4.

#### R-OUT-017 Data paths
- **Statement:** The builder sets in `launch.json.env`: `SSL_CERT_FILE` (unless `NoCACerts`),
  `GCONV_PATH` (glibc in closure, unless `NoIconv`), `LOCALE_ARCHIVE` (when `Locales` contains
  anything other than `C.UTF-8`; built with nixpkgs `glibcLocales` restricted to the list),
  `TZDIR` when tzdata is in the closure. User `Env` entries override them.
- **Acceptance:** On every matrix distro: HTTPS 200, 0 locale warnings with
  `LANG=en_US.UTF-8` and `Locales: {"C.UTF-8","en_US.UTF-8"}`, `iconv` ä→ISO-8859-15 = `e4`.
- **Source:** S8 §3.5.

#### R-OUT-018 Supported hosts for AppImages
- **Statement:** Supported: glibc distros Ubuntu ≥ 22.04, Debian ≥ 12, Fedora ≥ 43 and musl
  Alpine ≥ 3.22, amd64 and arm64, kernel ≥ the bundled glibc's minimum (3.10). Ubuntu ≥ 23.10
  with the AppArmor userns restriction is served by ldso mode.
- **Acceptance:** Nightly harness runs the fixture bundles in unprivileged `bwrap` as `bobtest`
  on the 5 pinned rootfs images (digests of S8 §3.5) × {userns, nouserns} × {arm64 native,
  amd64 native worker}: `api` golden line, HTTPS 200, locale, `date`, `iconv`, all pass.
  Real Ubuntu 24.04 host with `kernel.apparmor_restrict_unprivileged_userns=1`: (manual) once
  per release.
- **Source:** S8 §3.5, §5.6.

#### R-OUT-019 Build-time fidelity warnings
- **Statement:** In `AutoMode`, the `appimage` builder warns when an entry is not an ELF
  executable (shebang script) or the closure contains `makeWrapper`-style wrappers (ELF or
  shell scripts that exec another `/nix/store` path): these fail in ldso mode.
- **Acceptance:** Bundle with a shell-script entry → `warning: //deploy:tools: entry "deploy" is
  a script; it cannot run on hosts without user namespaces. Set Mode: appimage.UsernsOnly to
  make this explicit.`; with `UsernsOnly` no warning, and in ldso-only hosts the launcher exits
  127 `bobrun: this bundle needs user namespaces`.
- **Source:** S8 §6 risks, ADR 0010 Consequences.

#### R-OUT-020 Host graphics (best effort)
- **Statement:** With `HostGraphics: true`, the launcher appends host library dirs
  (`/run/opengl-driver/lib`, `/usr/lib/<multiarch>`, `/usr/lib64`, `/usr/lib`, existing ones
  only) after the bundled ones (ldso: `--library-path`; userns: `LD_LIBRARY_PATH`). Bundles
  always carry the pin's recent glibc, never an older one.
- **Acceptance:** Fedora 43 rootfs with `mesa-vulkan-drivers`: bundled `vulkaninfo` with
  `HostGraphics: true` reports `llvmpipe` in both modes. Real GPUs: (manual).
- **Source:** S8 §3.6, S8-5.

## 4.7 Tarballs

#### R-OUT-004 Tarballs
- **Statement:** `tarball.Tarball` produces `<name>-<platform>.tar.gz` in one of two kinds:
  `Closure` (all closure paths under `nix/store/…`, plus `bob/roots` listing roots and
  `bob/licences.json`) or `Static` (one statically linked executable at `bin/<name>`;
  evaluation error if the program's closure has more than its own path). Same tar/gzip rules as
  R-OUT-010.
- **Acceptance:** Static tarball of a pure-Go fixture binary contains exactly `bin/<name>` and
  `bob/licences.json`; a cgo binary as `Static` → `✗ //cmd/api:api is not a static binary (it
  needs glibc-2.42, openssl-3.6.5); use tarball.Closure`. Two builds byte-identical.
- **Source:** R-OUT-004, D43.

```go
package tarball // pkg/output/tarball

type Kind int

const (
	Closure Kind = iota // whole closure under nix/store
	Static              // a single static executable
)

type TarballOpts struct {
	Program  bob.Executable
	Kind     Kind
	Platform bob.Platform // default host
}

// New declares a tarball. Example:
//
//	var Tar = bob.Define(func(p *bob.P) *tarball.Tarball {
//		return tarball.New(p, "cli", tarball.TarballOpts{Program: cli.Binary(p), Kind: tarball.Static})
//	})
func New(p *bob.P, name string, opts TarballOpts) *Tarball
```

## 4.8 Releases as built objects

#### R-OUT-022 Release definition
- **Statement:** A release is declared in `bob.go` with `release.New` and lists its artifacts;
  building `//path:release` (with a version) builds every artifact and a release manifest
  (R-OUT-023). The version enters **only** the manifest, OCI tags, the OCI label
  `org.opencontainers.image.version` and file names, never the artifacts' package derivations,
  so an unchanged program is byte-identical across versions.
- **Acceptance:** Building the fixture release as `1.0.0` and `1.0.1` without source changes:
  identical closure store paths, identical AppImage bytes, identical OCI layer blobs; only the
  manifest, image config/manifests/index differ. `bob build //rel:api` without `--version` uses
  version `0.0.0-dev` and prints `note: no version given; using 0.0.0-dev`.
- **Source:** R-REG-004, R-FW-003, R-FW-040, principle 4.

```go
package release // pkg/release

// Release is a versioned set of artifacts. Publishing (immutability, visibility, promotion)
// happens on the server (§06); this type only describes and builds it.
type Release struct{ /* unexported */ }

type ReleaseOpts struct {
	Public    bool          // installable and downloadable without authentication
	Closures  []bob.Executable // installable with bob install; built for each of Platforms
	Images    []*oci.Image
	AppImages []*appimage.Bundle
	Tarballs  []*tarball.Tarball
	Platforms []bob.Platform // default {LinuxAMD64, LinuxARM64}, applies to Closures
	Notes     bob.Source     // optional Markdown file shown in the UI
}

// New declares a release. The release name is the label's name; artifact names must be
// unique within it. Example:
//
//	var API = bob.Define(func(p *bob.P) *release.Release {
//		return release.New(p, "api", release.ReleaseOpts{
//			Public:    true,
//			Closures:  []bob.Executable{api.Binary(p)},
//			Images:    []*oci.Image{deploy.Image(p)},
//			AppImages: []*appimage.Bundle{deploy.Bundle(p)},
//		})
//	})
func New(p *bob.P, name string, opts ReleaseOpts) *Release
```

#### R-OUT-023 Release manifest `bob.release/v1`
- **Statement:** The release derivation's output is `release.json` (schema below; keys sorted;
  arrays sorted by `name`, then `platform`), consumed by §06 (publish) and `bob install`.
- **Acceptance:** Golden `release.json` of the fixture; JSON-schema test (the schema file lives
  in `pkg/release/schema/release-v1.json`); `version` matches
  `^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$` (else evaluation error
  `✗ invalid release version "<v>"`).
- **Source:** R-REG-004, R-INST-001.

| Field | Type | Meaning |
|---|---|---|
| `schema` | `"bob.release/v1"` | format version |
| `project` | string | server project id from `bob.go` module config (§01); `""` when built without a server |
| `release` | string | release name (label name) |
| `label` | string | `//rel:api` |
| `version` | string | given version |
| `public` | bool | visibility request; the server enforces it |
| `artifacts[]` | object | one per artifact and platform |
| `artifacts[].name` | string | artifact name (package name) |
| `artifacts[].kind` | `closure` \| `oci` \| `appimage` \| `tarball` | |
| `artifacts[].platform` | `linux-amd64` \| `linux-arm64` \| `multi` | `multi` only for `oci` |
| `artifacts[].storePath` | string | the artifact's output path |
| `artifacts[].entrypoints` | map name→relative path | `closure` only: executables installed by `bob install` (default `{<name>: <MainProgram>}`; all of `bin/*` when `MainProgram` is empty) |
| `artifacts[].file` | string | `appimage`/`tarball`: file name, e.g. `api-1.0.1-x86_64.AppImage` |
| `artifacts[].sha256`, `size` | string, int | `appimage`/`tarball`: of the file |
| `artifacts[].repo`, `tags`, `index` | string, []string, string | `oci`: repo, `[<version>]`, index digest |

```json
{
  "artifacts": [
    {"entrypoints": {"api": "bin/api"}, "kind": "closure", "name": "api", "platform": "linux-amd64",
     "storePath": "/nix/store/biszb9qipkwb6z81yh4745icllxq2kzk-cmd-api-api-linux-amd64"},
    {"file": "api-1.0.1-x86_64.AppImage", "kind": "appimage", "name": "bundle", "platform": "linux-amd64",
     "sha256": "0e5a4e01…", "size": 10848776, "storePath": "/nix/store/…-bundle-linux-amd64"},
    {"index": "sha256:40e73051…", "kind": "oci", "name": "image", "platform": "multi",
     "repo": "acme/api", "storePath": "/nix/store/…-image", "tags": ["1.0.1"]}
  ],
  "label": "//rel:api", "project": "acme", "public": true, "release": "api",
  "schema": "bob.release/v1", "version": "1.0.1"
}
```

## 4.9 Installing releases (consumer side)

CLI syntax and output samples are §02; the server endpoints are §06. This section fixes the
semantics and the on-disk profile.

R-INST-001…004 (install, upgrade, rollback, uninstall, list; no system Nix; authentication;
several registries) are specified once, in §02 §02.8, which also owns the file layout. This
section fixes what an installable release is (§4.8: the `closure` artifacts and entry binaries
of the `bob.release/v1` manifest) and the profile semantics below.

#### R-INST-005 Profile semantics
- **Statement:** Installed software forms profile generations in the layout of §02.8 Files
  (`$XDG_DATA_HOME/bob/install/`): `generations/<n>.json` is create-only (temp file +
  `rename`), `current` is a symlink replaced atomically, and every store path of each of the
  last 5 generations has a GC root (`<home>/nix/var/nix/gcroots/bob-install/<n>`); older
  generations lose their roots. Shims are `0755` POSIX sh files in `binDir` (default
  `~/.local/bin`) with the text of §02.8 step 5 (`bob exec --installed <name> -- <bin>`).
- **Acceptance:** Golden `generations/1.json` and shim text (§02.8); an existing non-bob file at
  the shim path → `BOB-E-0004` (`~/.local/bin/api exists and was not created by bob; remove it or
  use --as`), nothing changes; after 7 installs exactly generations 3–7 have GC roots.
- **Source:** R-INST-001, R-NFR-007; aligned with §02.8 at integration.

## 4.10 Output budgets **[proposal]**

#### R-OUT-024 Output build-time budgets
- **Statement / Acceptance** (fixture `api`, toolchains and closure present; quiet:
  `docs/spikes/measure/quiet/s08-bundles.log` for [8c/15G], S8 §3.7 for [2c/4G]; budget =
  measurement × 1.5 rounded):
  - OCI index cold (per-path layers): ≤ 3 s [8c/15G] (1.91 s), ≤ 4 s [2c/4G] (2.3 s).
  - OCI incremental (one root changed): ≤ 1 s [8c/15G] (0.40 s), ≤ 1 s [2c/4G] (0.57 s).
  - AppImage `api` with default prune: ≤ 5 s [8c/15G] (3.00 s), ≤ 18 s [2c/4G] (11.6 s,
    busy host). 100 MiB closure ≤ 30 s [2c/4G] at zstd-19; if the budget fails, the default
    level becomes 15 (decision recorded, not a test change).
  - AppImage start: first run (extract) ≤ 0.5 s, cached ≤ 0.1 s [8c/15G].
- **Source:** S8 §3.7, §5.7. The per-layer derivation design (R-OUT-011) adds one derivation
  per store path; re-measure in Stage 3 and keep the budgets.

#### R-OUT-025 Output size budgets
- **Statement / Acceptance:**
  - Fixture `api` AppImage, default prune: ≤ 12 MB (measured 10.85 MB); without prune ≤ 23 MB
    (21.2 MB).
  - Fixture `api` OCI arm64 image compressed: ≤ 26 MB (25.4 MB); v1→v2 new bytes ≤ 16 %.
  - Native `api` closure after fixup ≤ 74 MiB, ≤ 12 paths (§01 R-NIX-030 is the single source:
    75.9 MiB measured without fixup, S8 §3.1; strip of `api` −2.5 MiB and dropping `itoa-src`
    give 73.4 MiB) and must not contain the full `gcc-*` path (only `gcc-*-lib`); cross
    `@linux-amd64` with default libs ≤ 112 MiB (113.8 MB = 108.5 MiB, S7). Aligned at integration.
  - The bob AppImage runtime binary ≤ 3 MiB per platform (launcher prototype 2.3 MiB).
- **Source:** S8 §3.1, §3.2; S7 §3.2; STATUS (S7 366 MB closure finding).

## 4.11 Error messages (this section)

| Condition | Text (stderr, after `✗ `) | Code (§02) | Exit |
|---|---|---|---|
| unknown platform | `unknown platform "<p>" (known: linux-amd64, linux-arm64)` | 0001 | 2 |
| unsupported host architecture | `unsupported host architecture "<m>" (…)` | 0008 | 2 |
| package restricted | `<label> does not support <p> (it allows: <list>)` + chain | 0006 | 2 |
| unknown variant token | `unknown variant "<t>" (known: release, debug, asan, ubsan, race, <named…>)` | 0001 | 2 |
| conflicting profiles | `variants "release" and "debug" conflict: both set the profile` | 0007 | 2 |
| `race` with `asan` on Go | `variants "race" and "asan" conflict: Go cannot combine them (in //…)` | 0007 | 2 |
| pruned needed library | R-OUT-007 | 2015 | 1 |
| non-static tarball | R-OUT-004 | 1022 | 3 |
| OCI `User` root implicit | n/a (default is 65534; `"0:0"` allowed) | – | – |
| `MaxLayers` out of range | `//…: invalid MaxLayers: must be between 2 and 125 (got <n>)` | 1020 | 3 |
| too many roots for MaxLayers | `//…: invalid MaxLayers: <n> roots need MaxLayers ≥ <n+2>` | 1020 | 3 |
| nothing installable in a release | §02 R-INST-002 | 0009 | 2 |

Exit statuses follow §02.2 (aligned at integration: option errors in `bob.go` are definition
errors, exit 3).

## Open points

- **[open] O-04-1 Version stamping into binaries.** Recommendation: `golang.BinaryOpts.Stamp`
  (`-X` ldflags from `release.Version(p)`) affecting only the Go link derivation of the release
  build, so the cache is shared up to the link step; C/Rust: a generated `version.h` /
  `env!("BOB_VERSION")` only for the final binary. Not in v1 unless pipelines need it.
- **[open] O-04-2 AppImage runtime delivery.** Recommendation: bob embeds prebuilt static runtime
  binaries for both platforms (built by bob's own release, R-ARCH-005) and adds them with
  `nix store add` (content-addressed, no compile on first bundle). Alternative: build from
  embedded Go source in a derivation (+~10–20 s first bundle, no binary blobs in bob).
- **[open] O-04-3 squashfs reader in the runtime.** The runtime needs a squashfs reader with zstd.
  Recommendation: own minimal reader (read-only, ~600 lines) + `github.com/klauspost/compress/zstd`
  (BSD-3). A third-party Go squashfs library needs a licence and correctness check first.
- **[open] O-04-4 Fleet-wide popularity for layer order.** Recommendation: no (closure-local
  popularity in v1, deterministic per image); revisit with registry dedupe measurements.
- **[open] O-04-5 OCaml cross.** Recommendation: native only in v1 (R-PLAT-005), cross on the
  roadmap.

## Escalations

None. (No owner decision conflicts with this section. D61 only affects how §06 serves release
files; D56/D53 affect where foreign-arch tests run, which §05 schedules per R-FW-039.)

## Verifications run while drafting

- `unsafeDiscardReferences` (R-OUT-011): `__structuredAttrs = true`, Nix 2.34.8 (drafting, with
  the feature flag) and re-run at integration on 2.34.8 and 2.34.9 **without** any
  `discard-references` flag (same result; the flag only produces `warning: unknown experimental
  feature 'discard-references'`; scratch `/root/scratch-specint/dr/`). Output containing an input's store path: with
  `unsafeDiscardReferences.out = true` → `nix-store -q --references` empty; without → the input
  is registered. Scratch: `/root/scratch-spec04/t.nix`, `b.sh`.
- Budgets read from `docs/spikes/measure/quiet/s07-cross.log` and `s08-bundles.log`.

# §05 Server data: buckets, keys, state machines, fleet, logs


Scope: the four buckets and their policies, the dynamitedb key schema (GET-only, D60), the
`internal/store` primitives, the state machines for build, job, worker and release, leases and
epochs, polling plus wake-up (D54), scheduling and batch splitting (D53), worker registration
and authentication (D57), worker isolation (D56), logs in lake with live pull (D55), and
orchestrator HA without a coordinator.

ADRs: 0011, 0012, 0013, 0015, 0016, 0019 (all `Accepted (locked at Gate S)` with owner amendments).
Evidence: spikes S9, S10, S11, S13, S14, S17 and the §05 verification runs in
`/root/scratch-spec05/RESULTS.txt` (V1–V4 below).

## Interfaces

§05 provides:
- `internal/store`: `store.Bucket`, `store.Get`, `store.GetCached`, `store.Create`, `store.CAS`,
  `store.PutOwned`, `store.Delete`, `store.SeqLog`, `store.PagedIndex`, `store.ShardSet`,
  `store.Lease` (§05.3). Every other server package reaches S3 only through it (lake excepted).
- Record types: `JobRecord`, `JobSpec`, `BuildRecord`, `BuildPage`, `ReleaseRecord`,
  `WorkerRecord`, `FleetConfig`, `ProjectRecord`, `UserRecord`, `RoleRecord`, `TokenRecord`,
  `ServiceAccountRecord`, `ForgeRecord`, `SettingRecord` with their keys (§05.4). §06 owns the
  fields of the identity/forge/release records that the API exposes; §05 owns keys and access ops.
- State machines: build, job, worker, release, controller lease (§05.6).
- `proto/bob/worker/v1/worker.proto` (complete, §05.9): `WorkerControlService` (Attach,
  GetCheckoutCredential). Worker-token RPCs are outside user RBAC (§05.9.3).
- Log tables `log_line`, `package_run`, partition naming, `logstore.Reader` for §06's
  `LogService` (GetLog/FollowLog/SearchLog) (§05.10).
- Artifacts bucket key prefixes (§05.2.4); the promotion protocol itself is §06's.
- Budgets R-NFR-100…109 (§05.12).

§05 expects:
- §06: `LogService.FollowLog` server stream that calls `logstore.Follow` (§05.10.4);
  `LogLine`, `Cursor`, `Restart`, `Final` in `bob/log/v1/log.proto` with the fields of
  §05.9.1's `LiveLine`/`LiveRestart`/`LiveFinal`; promotion (`staging/` → `nix/`, `oci/`) per job
  with a create-only narinfo; the visibility document format (D58): **one JSON document per
  store path / OCI object** (`nix/vis/<storehash>.json`, §06 §6.6.2), so a check is one GET
  (resolved at integration); forge adapter `IssueCheckout`/`RevokeCheckout` (D62, §06 §6.9.1)
  used by `GetCheckoutCredential`; RBAC permission ids for the user RPCs that read these records.
- §01: graph IR (fd 3) and derivation generation run on the worker; `bob-builder` contract;
  embedded static Nix with a root-mode config (§05.8).
- §02: `bob build --remote` creates builds through §06's `BuildService`; error templates use
  the §02 taxonomy (`✗ …` / `hint:`).

---

## 05.1 Rules for all S3 access

#### R-STORE-100 GET-only hot paths
- **Statement:** bob-server never issues `ListObjects`/`ListObjectsV2` on the control or fleet
  bucket, and never calls `dynamitedb.Query`. Every read is a GET of a key computed from known
  data (ids, counters, sequence numbers, index documents); every write is a PUT/DELETE on a known
  key, conditional where the record is shared.
- **Acceptance:**
  - Given the harness S3 counting proxy in front of RustFS, when the full server e2e suite runs
    (registration, 300-job drain, chaos, every UI list page, retention), then the proxy counts
    **0** LIST requests on `bob-control` and `bob-fleet`.
  - `go run ./tools/ci` fails if any package other than `internal/store/sweep` references
    `ListObjectsV2`, `ListObjects`, `NewListObjectsV2Paginator` or `dynamitedb.Query`.
- **Source:** D60, R-STORE-004, ADR 0011 (amended).

#### R-STORE-101 Cold sweeps are the only LIST
- **Statement:** LIST is allowed only in `internal/store/sweep`, only on the analytics and artifacts
  buckets, and only from two controllers that run at most once per hour: lake partition drop
  (§05.10.6) and the staging sweep (§05.2.4). `bob-server store fsck` (manual) may also LIST.
- **Acceptance:** the counting proxy attributes every LIST to the `User-Agent` suffix
  `bob-sweep/<controller>`; over a 2 h soak run, there are ≤ 2 sweep runs per controller and 0
  LISTs from other code.
- **Source:** D60 (dynamitedb only), R-STORE-007. **[open]** If the owner wants zero LIST
  anywhere, lake needs a `DropTable`/delete API (S10 F2) and staging needs a working S3
  lifecycle rule. Recommendation: keep the sweep as an interim.

#### R-STORE-102 Supported backends and start-up conformance probe
- **Statement:** Only AWS S3 and fully AWS-compatible servers with conditional writes are
  supported (AWS S3, RustFS ≥ 1.0.3 as the reference). SeaweedFS and Garage are not supported.
  At start, each bob-server process probes every configured bucket under
  `_bob/probe/<random>` and refuses to start when a check fails.
- **Acceptance:** Given RustFS 1.0.3, when bob-server starts, all checks pass:
  1. PUT `If-None-Match: *` twice → second is 412;
  2. PUT `If-Match: <stale>` → 412;
  3. GET `If-None-Match: <current ETag>` → 304;
  4. GET missing key → 404 `NoSuchKey`;
  5. DELETE `If-Match: <stale>` → 412;
  6. GET of `_bob/probe/<r>/` after deleting `_bob/probe/<r>/x` → 404;
  7. every GET returns an `ETag` header.

  Given a fake backend that ignores `If-None-Match`, the process exits 78 with
  `✗ storage backend <endpoint> is not supported: conditional create (If-None-Match) is ignored`
  and `hint: bob needs an AWS-S3-compatible server with conditional writes, e.g. RustFS ≥ 1.0.3 or AWS S3`.
- **Source:** D63, D64, R-STORE-006, S9 §3.1, V1 (§05.13).

#### R-STORE-005 Panic-safe store calls (kept)
- **Statement:** every dynamitedb call runs inside `store.call`, which recovers panics into
  `store.ErrPanic` (logged with the stack, metric `bob_store_panics_total`).
- **Acceptance:** unit tests feed each misuse panic from S9 §3.6 (filter operator in an update,
  nil model, missing `ETag` header from a fake backend). Each returns `ErrPanic` and the process
  keeps running.
- **Source:** R-STORE-005, STATUS (panics occur only on non-compliant backends or misuse).

#### R-STORE-103 dynamitedb usage rules
- **Statement:** bob uses dynamitedb HEAD (D46) unchanged (D64) with these rules:
  1. All model fields are `DataField` with short CBOR names and `omitempty`; there are no static
     fields (S9 B8).
  2. No dynamitedb filters: decisions are made in Go on the fetched value (B5–B7).
  3. No `WithExpires` (B10); expiry is a field that a controller acts on.
  4. A shared record is changed only by `store.CAS`: `dynamitedb.Get`, then decide, then
     `dynamitedb.Put` with the Get's `ETagField` (one `PUT If-Match`). A new record is written
     with `dynamitedb.Create`. Only a record with exactly one legitimate writer may use
     `store.PutOwned` (unconditional `Put`).
  5. `dynamitedb.Update` is not used.
  6. After a failed call, `ctx.Err()` is checked first (B11). A 404 from `Put` with an ETag
     maps to `store.ErrNotFound` (B13).
  7. On `ErrConcurrencyConflict`/`ErrAlreadyExists`, the caller re-reads and treats the result as
     success when the stored value carries its own write id (`wid` + epoch, or request id `rq`).
- **Acceptance:** a table-driven test per record type covers create, CAS win, CAS loss, lost
  response (fault proxy drops the response after the server applied the write → caller reports
  success), and decode of the stored CBOR.
- **Source:** ADR 0011, S9 §3.6/§5.5, STATUS.

#### R-STORE-104 Key segment rules
- **Statement:** keys are `<pk>/<pkval>` or `<pk>/<pkval>/<sk>/<skval>`. Every value matches
  `^[A-Za-z0-9@._-]{1,100}$` and is never empty. A pk name is used either by pk-only types or by
  pk+sk types, never both. Encodings:
  - `num10` = `%010d`;
  - `seq20` = `%020d`;
  - `enc(s)` = `b` + lowercase base32 without padding of UTF-8 `s` when `len(s) ≤ 60`, else `h`
    + the first 40 hex chars of SHA-256(s);
  - `hash64(s)` = hex SHA-256 (64 chars);
  - `sh2(id)` = `%02d` of `fnv32a(id) mod shards`.

  Ids: `ULID` = 26-char Crockford ULID (48-bit ms time + 80 random bits).
- **Acceptance:** fuzz test: no input to the key builders yields an invalid or colliding
  segment (10⁶ random strings, including `/`, empty, 1 KiB). `sh2` is total over non-empty strings;
  ids are validated at the API boundary, not in the key builders. An empty segment returns
  `store: key segment invalid: must match [A-Za-z0-9@._-]{1,100}`.
- **Source:** S9 §5.1, B12.

---

## 05.2 Buckets and policies

### 05.2.1 Buckets

| Bucket (default name) | Engine | Orchestrator | Worker | Contents |
|---|---|---|---|---|
| `bob-control` | dynamitedb | read/write | **none** (Deny) | identity, projects, builds, releases, settings, orchestrator signing keys |
| `bob-fleet` | dynamitedb | read/write | read all; write only the prefixes in §05.2.3 | fleet config, workers, queues, jobs, leases, heartbeats; ids only (R-SEC-003) |
| `bob-analytics` | lake | read/write | read/write (D38) | log and run tables in partitions (§05.10) |
| `bob-artifacts` | plain S3 | read/write | read all; write `staging/` | Nix cache, OCI, release assets |

#### R-STORE-002 Bucket split (kept, refined)
- **Statement:** as in the table above. All four bucket names, the endpoint and the region are
  configuration (`storage.*`, §05.11.1). Credentials come from the AWS SDK default chain (env,
  shared file, IMDSv2, web identity). How a host gets them is a deployment concern (D57).
- **Acceptance:** Given the shipped policies on RustFS, when the harness runs the S11 policy
  matrix with the new prefixes (§05.2.3), then every allowed cell succeeds and every denied cell
  returns 403 (≥ 40 operations, including a worker GET, PUT and DELETE on `bob-control`).
- **Source:** R-STORE-002, R-SEC-001/002, D13, D38, D57, ADR 0013.

### 05.2.2 Credentials

#### R-SEC-100 bob never mints S3 credentials
- **Statement:** bob has no STS, AssumeRole or credential-vending code path. Orchestrator and
  worker processes use the credentials their host provides. Every further secret a worker gets
  (worker token, forge checkout credential) is obtained **through S3 first** (sealed token in
  `worker/<wid>`), then through RPCs authenticated with that token. Never the other way round.
- **Acceptance:** `go run ./tools/ci` fails if a server package imports
  `github.com/aws/aws-sdk-go-v2/service/sts` or `service/iam`. A protected test checks that an
  RPC without a worker token cannot return any S3 credential or worker token.
- **Source:** D57; ADR 0013 decision 8 struck.

#### R-SEC-101 Compromised worker host runbook
- **Statement:** the operator cuts off a compromised worker host in two steps:
  1. revoke its worker id (`bob-server worker revoke <wid>` or the UI). The token and PoP are then
     refused within 10 s;
  2. rotate the shared worker S3 credential in the deployment.

  bob documents this in `docs/operations/worker-compromise.md`.
- **Acceptance (manual):** the runbook exists and its commands work in the harness's local
  deployment.
- **Source:** D57, OD-S11-1 a.

### 05.2.3 Shipped policies

#### R-SEC-002 Policies (kept, prefixes updated)
- **Statement:** bob ships `deploy/policies/{worker,orchestrator}.json` (identity policies) and
  `deploy/policies/control-bucket.json` (bucket policy that denies everyone except the
  orchestrator principals). The worker policy grants:

| Bucket | Actions | Resources |
|---|---|---|
| control | explicit Deny `s3:*` | bucket and `/*` |
| fleet | `s3:ListBucket` (needed only so that missing keys return 404, not 403; bob never lists) | bucket |
| fleet | `s3:GetObject` | `/*` |
| fleet | `s3:PutObject` | `wreg/*`, `wrs/*`, `wrt/*`, `whb/*`, `job/*`, `qs/*`, `qh/*` |
| fleet | `s3:DeleteObject` | none |
| analytics | Get/Put/Delete object, multipart abort/list parts; ListBucket | bucket, `/*` |
| artifacts | `s3:GetObject` | `/*` |
| artifacts | `s3:PutObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts` | `staging/*` |

  Orchestrator: `s3:*` on all four buckets. Workers cannot write `worker/`, `fleet/`, `orch/`,
  `ctl/`, `qt/`, `jact/` or `widx/`.
- **Acceptance:** extends S11's 27-operation matrix. Each new prefix gets one PUT allowed and
  one denied: a worker PUT on `worker/x`, `fleet/cfg`, `qt/x` and `jact/00` → 403. A worker
  PUT on `job/x` → 200.
- **Source:** R-SEC-002, S11 §5.4.

#### R-SEC-102 Start-up policy self-check
- **Statement:** when `storage.worker_probe_credential` is configured, the orchestrator checks
  at start that this credential gets 403 on `HeadObject bob-control/_bob/probe` and on
  `PutObject bob-fleet/worker/_probe`. It also checks that `GetBucketPolicy bob-control`
  contains the deny statement. If a check fails, the orchestrator refuses to start
  (`✗ control bucket is readable with the worker credential`).
- **Acceptance:** the harness starts an orchestrator against a control bucket without the
  bucket policy → exit 78 with that text.
- **Source:** S11 §5.4, T3.

### 05.2.4 Artifacts bucket prefixes

| Prefix | Writer | Notes |
|---|---|---|
| `staging/<buildID>/<jobID>/<attempt>/…` | worker | NARs, unsigned narinfo, OCI blobs, `COMPLETE.json` written last (S12 §5.1, scoped per job, S14) |
| `nix/nar/<storehash>.nar.zst`, `nix/ls/<storehash>.ls`, `nix/narinfo/<storehash>.narinfo` | orchestrator | narinfo create-only, written last |
| `nix/vis/<storehash>.json`, `oci/vis/sha256/<hex>.json` | orchestrator (C `If-None-Match: *`, CAS `If-Match`) | visibility document, one per served object: `{"v":1,"projects":[…],"public":[<release id>…]}` (format §06 §6.6.2); a check is 1 GET (cached) |
| `src/<projectID>/<sha256hex>.tar.zst` | orchestrator (upload endpoint, §06 §6.9.7) | `--remote` source snapshots, create-only; lifecycle 7 days |
| `oci/…` | orchestrator | S12 layout |
| `rel/<projectID>/<num10>/<asset>` | orchestrator | release assets, immutable. Release id (API, visibility documents) = `<projectID>.<num10>` |

#### R-STORE-105 Staging cleanup without hot-path LIST
- **Statement:**
  - After a job's outputs are promoted, the promoting orchestrator deletes the staged objects
    named in `COMPLETE.json` (known keys).
  - Staging of failed or abandoned attempts is removed by the S3 lifecycle rule
    `staging/` → expire after 7 days, when the backend supports it.
  - Otherwise, the hourly `staging-sweep` (R-STORE-101) lists `staging/` and deletes objects
    older than 7 days.
- **Acceptance:** after a 300-job drain with 20 % killed workers, the `staging/` objects of
  successful attempts are 0 within 1 promotion pass. After the sweep with the clock advanced 7 d,
  `staging/` is empty.
- **Source:** S12 §5.1, R-STORE-007.

---

## 05.3 `internal/store` primitives

All signatures below are exact. Doc comments are part of the contract.

```go
package store // import "github.com/megakuul/bob/internal/store"

// Bucket is one dynamitedb bucket plus a raw S3 client for conditional GETs.
// It is safe for concurrent use.
type Bucket struct{ /* unexported */ }

// BucketConfig selects one bucket. Credentials come from the AWS SDK default chain.
type BucketConfig struct {
	Endpoint string // e.g. "https://s3.eu-central-1.amazonaws.com" or "http://127.0.0.1:9000"
	Region   string // e.g. "us-east-1"
	Name     string // bucket name, e.g. "bob-fleet"
	Role     string // "control" | "fleet" (metrics label only)
}

// Open connects to the bucket, runs the conformance probe (R-STORE-102) once per process,
// and returns a Bucket. It never lists.
func Open(ctx context.Context, cfg BucketConfig) (*Bucket, error)

// Errors. All are wrapped with the key, e.g. "store: job/01J…: not found".
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrConflict = errors.New("changed concurrently")
	ErrPanic    = errors.New("dynamitedb panicked")
	ErrNoChange = errors.New("no change") // returned by a CAS func to skip the write
)

// Get reads the record whose key fields are set in key. The result carries the ETag.
func Get[T any](ctx context.Context, b *Bucket, key *T) (*T, error)

// Cache keeps the last value and ETag per key for GetCached. Zero value is ready to use.
type Cache[T any] struct{ /* unexported */ }

// GetCached sends GET If-None-Match with the cached ETag. On 304 it returns the cached value
// and changed=false (1 request). On 200 it reads the record with dynamitedb.Get
// (2 requests in total) and updates the cache. On 404 it drops the entry and returns ErrNotFound.
func GetCached[T any](ctx context.Context, b *Bucket, c *Cache[T], key *T) (v *T, changed bool, err error)

// Create writes v only if its key does not exist (PUT If-None-Match: *).
func Create[T any](ctx context.Context, b *Bucket, v *T) error

// CAS reads the record at key, calls fn with it (nil if absent), and writes fn's result with
// If-Match of that read (or If-None-Match when absent). On ErrConflict it re-reads and calls fn
// again, up to 8 times with jittered backoff (10–200 ms). fn returning ErrNoChange ends CAS
// without writing; fn returning nil, nil deletes the record (DELETE If-Match).
func CAS[T any](ctx context.Context, b *Bucket, key *T, fn func(cur *T) (*T, error)) (*T, error)

// PutOwned writes v unconditionally. Only for records with exactly one legitimate writer
// (orch/<self>, whb/<self>).
func PutOwned[T any](ctx context.Context, b *Bucket, v *T) error

// Delete removes the record if its ETag still matches v's ETag (2 requests, dynamitedb.Delete).
func Delete[T any](ctx context.Context, b *Bucket, v *T) error
```

### 05.3.1 Sequence log (queues, registration inbox, promotion log)

A **sequence log** is a dense, append-only series of slots `<pk>/<name>/s/<seq20>` with
`seq ≥ 1`, plus two hint records: tail `<tailpk>/<name>` and head `<headpk>/<name>`. Hints only
make things faster. The truth is which slots exist (`If-None-Match` makes each slot number
unique).

```go
// SeqLog is a dense append-only log of slots on known keys. Readers find new entries by
// GETting the next slot number; a 404 means "nothing new". It never lists.
type SeqLog[T any] struct {
	Bucket  *Bucket
	SlotPK  string // e.g. "qs"
	TailPK  string // e.g. "qt"
	HeadPK  string // e.g. "qh"
	Name    string // e.g. "x86_64-linux.k3j9d0a1b2"
}

// NewSeqLog returns the log; it performs no I/O.
//
// Example:
//
//	q := store.NewSeqLog[QueueSlot](fleet, "qs", "qt", "qh", "x86_64-linux.k3j9d0a1b2")
//	seq, err := q.Append(ctx, &QueueSlot{Job: dynamitedb.Set(jobID)})
func NewSeqLog[T any](b *Bucket, slotPK, tailPK, headPK, name string) *SeqLog[T]

// Append reads the tail hint t (1 GET), Creates slot t, t+1, … until one succeeds, then raises
// the tail hint to seq+1 with a best-effort CAS (a conflict is ignored). Returns the slot number.
func (l *SeqLog[T]) Append(ctx context.Context, v *T) (seq uint64, err error)

// Read GETs one slot. ErrNotFound means seq is at or past the end of the log.
func (l *SeqLog[T]) Read(ctx context.Context, seq uint64) (*T, error)

// Tail and Head return the hints (GetCached; 1 request when unchanged).
func (l *SeqLog[T]) Tail(ctx context.Context) (uint64, error)
func (l *SeqLog[T]) Head(ctx context.Context) (uint64, error)

// AdvanceHead raises the head hint to n if it is lower (CAS, monotonic; never lowers it).
func (l *SeqLog[T]) AdvanceHead(ctx context.Context, n uint64) error
```

Invariants:
- Slots are never deleted while `seq ≥ head − 1000`. GC deletes older slots by known key.
- A writer never skips a number unless that slot exists, so the log has no gaps (V1: 20
  appenders × 50 → 1,000 dense unique slots, 0 lost writes).

### 05.3.2 Paged index (newest-first lists without LIST)

```go
// PagedIndex is a list of entries numbered 0,1,2,… stored P per page document
// <pagePK>/<scope>/p/<num10>. The entry count is a counter field in a record the caller names
// (e.g. ProjectCounters.Builds). Page k holds entries [k*P, (k+1)*P).
type PagedIndex[E any] struct {
	Bucket *Bucket
	PagePK string // e.g. "bpage"
	Scope  string // e.g. projectID
	P      int    // entries per page, 50 unless stated
}

// NewPagedIndex performs no I/O.
//
// Example:
//
//	idx := store.NewPagedIndex[BuildEntry](control, "bpage", projectID, 50)
//	page, err := idx.Page(ctx, idx.LastPage(count))
func NewPagedIndex[E any](b *Bucket, pagePK, scope string, p int) *PagedIndex[E]

// Set writes entry n (CAS on its page; creates the page on first use).
func (x *PagedIndex[E]) Set(ctx context.Context, n uint64, e E) error

// Page returns page k (GetCached). Missing entries in a page are zero values.
func (x *PagedIndex[E]) Page(ctx context.Context, k uint64) ([]E, error)

// LastPage returns the page number of entry count-1.
func (x *PagedIndex[E]) LastPage(count uint64) uint64
```

### 05.3.3 Sharded set (membership docs for reconcilers)

```go
// ShardSet is a set of string ids spread over N shard documents <pk>/<sh2>, each a bounded
// map id -> E (≤ 1,000 entries). Reconcilers read all N shards with GetCached each pass,
// so an unchanged set costs N × 304.
type ShardSet[E any] struct {
	Bucket *Bucket
	PK     string // e.g. "jact"
	N      int    // shard count, from FleetConfig
}

// NewShardSet performs no I/O.
//
// Example:
//
//	active := store.NewShardSet[ActiveJob](fleet, "jact", cfg.JobShards)
//	err := active.Add(ctx, jobID, ActiveJob{Build: dynamitedb.Set(buildID)})
func NewShardSet[E any](b *Bucket, pk string, n int) *ShardSet[E]

func (s *ShardSet[E]) Add(ctx context.Context, id string, e E) error    // CAS on shard sh2(id)
func (s *ShardSet[E]) Remove(ctx context.Context, id string) error      // CAS; absent = nil
func (s *ShardSet[E]) All(ctx context.Context) (map[string]E, error)    // N × GetCached
```

Shard count changes go through a new `FleetConfig.Gen`. Entries are rehashed by the
`index-repair` controller, which reads the old and the new shards.

### 05.3.4 Lease

```go
// Lease is a CAS-held lease on one record: owner, epoch (incremented on every takeover),
// expires. Used for controller leadership (ctl/<name>).
type Lease struct {
	Bucket *Bucket
	Key    string        // e.g. "ctl/job-reconciler"
	Owner  string        // this process's orchestrator id
	TTL    time.Duration // 3 × controller interval
}

// NewLease performs no I/O.
//
// Example:
//
//	l := store.NewLease(fleet, "ctl/job-reconciler", self.ID, 30*time.Second)
//	if held, epoch, err := l.TryAcquire(ctx); held { reconcile(ctx, epoch) }
func NewLease(b *Bucket, key, owner string, ttl time.Duration) *Lease

// TryAcquire creates the lease if absent, renews it if owned, or takes it over (epoch+1) when
// expires + skew < now. 1 GET + at most 1 PUT.
func (l *Lease) TryAcquire(ctx context.Context) (held bool, epoch uint64, err error)
```

#### R-STORE-106 Primitives are the only data access
- **Statement:** server packages use only the §05.3 API. No package other than `internal/store`
  imports `github.com/megakuul/dynamitedb` or `service/s3`. The exceptions are
  `internal/logstore` (lake) and `internal/artifacts` (plain S3 on the artifacts bucket).
- **Acceptance:** layering check in `go run ./tools/ci`. Unit tests for each primitive run
  against RustFS: SeqLog with 20 concurrent appenders × 50 → 1,000 dense unique slots;
  PagedIndex with 20 writers on one page → no lost entry; Lease with 3 contenders → exactly one
  holder per epoch.
- **Source:** D60, V1.

---

## 05.4 Key schemas

Access op legend: **G** GET, **GC** conditional GET (`GetCached`), **C** Create, **CAS**
Get+Put(If-Match), **P** PutOwned, **D** Delete(If-Match). "Hot" marks paths that run
periodically or per request.

### 05.4.1 Fleet bucket (`bob-fleet`)

| Key | Record / value fields (CBOR name) | Writer (op) | Reader (op) | Hot |
|---|---|---|---|---|
| `fleet/cfg` | `FleetConfig`: `g` gen; `m` members [{`o` orchID, `u` endpoint, `fp` SPKI pin, `s` since}]; `c` classes [{`id`, `pl` platform, `cp` caps, `lb` labels}]; `k` orchestrator public keys [{`kid`, `pub`, `na` notAfter}]; `pr` params {poll, heartbeat, lease TTL, flush, shard counts} | orchestrators (CAS) | workers, orchestrators (GC every 30 s) | yes |
| `orch/<orchID>` | `OrchRecord`: `u`, `fp`, `h` heartbeat, `st` started, `v` version | that orchestrator (P, every 10 s) | membership controller (G) | yes |
| `ctl/<controller>` | `ControllerLease`: `o` owner, `ep` epoch, `ex` expires, `cu` cursor (controller-specific, e.g. registration seq) | orchestrators (CAS) | orchestrators (G) | yes |
| `wreg/<wid>` | `WorkerReg`: `b` canonical CBOR RegBody, `s` Ed25519 sig (S11 §5.2) | worker (C) | registrar (G) | no |
| `wrs/reg/s/<seq20>` | `RegSlot`: `w` wid, `at` time | worker (SeqLog Append) | registrar (G next seq) | yes |
| `wrt/reg` | `SeqHint`: `n` | worker (best-effort CAS) | worker (G) | no |
| `worker/<wid>` | `WorkerRecord`: `st` state, `g` gen, `n` serial, `sp` sigpub, `su` suite, `kp` kempub, `pl`, `cp`, `lb`, `sl` slots, `te` token exp, `ct` sealed token (S11 §5.2), `rq` request id | registrar, admin RPC (CAS) | worker (GC 60 s; G 1 s while waiting), orchestrators (GC ≤ 10 s cache) | yes |
| `whb/<wid>` | `Heartbeat`: `at`, `fr` free slots, `j` [{job, epoch}], `s` Ed25519 sig over (wid, at, j) | worker (P, every 15 s) | job reconciler, registrar (G) | yes |
| `widx/<sh2>` | `WorkerIndexShard`: map wid → {`pl`, `st`, `hb` last heartbeat seen, `fr`, `nj`} | registrar (CAS) | UI fleet page, wake selection (GC) | yes |
| `qt/<classID>` | `SeqHint` tail | orchestrators (CAS best effort) | orchestrators, workers (G) | yes |
| `qh/<classID>` | `SeqHint` head | workers, orchestrators (CAS monotonic) | workers (G) | yes |
| `qs/<classID>/s/<seq20>` | `QueueSlot`: `j` jobID, `at` enqueued, `tk` taken-by (wid \| `stale` \| `orphan`) | orchestrators (C); workers/orchestrators (CAS `tk`) | workers (G) | yes |
| `job/<jobID>` | `JobRecord` (§05.4.3) | build controller (C, CAS cancel/requeue); worker (CAS claim/start/result) | workers, reconciler (G) | yes |
| `jact/<sh2>` | `ActiveJobShard`: map jobID → {`b` buildID, `c` class, `at` created} | build controller (CAS) | job reconciler (GC every pass) | yes |

Class id = `<platform>.<first 10 base32 chars of SHA-256(canonical caps ‖ labels)>`, e.g.
`aarch64-linux.k3j9d0a1b2`. `FleetConfig.c` holds at most 256 classes.

### 05.4.2 Control bucket (`bob-control`)

| Key | Value | Writer (op) | Reader (op) | Hot |
|---|---|---|---|---|
| `okey/<kid>` | Ed25519 seed, created, notAfter | key controller (C) | orchestrators (G at start + on unknown kid) | no |
| `proj/<projectID>` | `ProjectRecord` (name, repo, forge id, settings, visibility, `lb` last build summary) | ProjectService (CAS) | API, controllers (GC) | yes |
| `pname/<enc(name)>` | projectID | ProjectService (C: uniqueness) | API (G) | yes |
| `pctr/<projectID>` | `ProjectCounters`: `b` builds, `r` releases | build/release controller (CAS) | API (GC) | yes |
| `pidx/all/p/<num10>` + `pidxm/all` | projects paged index (P = 200): {id, name, last build state, time}; `pidxm` holds the count | ProjectService (CAS) | UI projects page (GC) | yes |
| `build/<buildID>` | `BuildRecord` (§05.4.4) | build controller (C, CAS) | API, controllers (G/GC) | yes |
| `bpage/<projectID>/p/<num10>` | `BuildPage`: 50 × `BuildEntry` {n, buildID, state, branch, commit[:12], trigger, created, dur} | build controller (CAS) | UI/API build list (GC) | yes |
| `bbr/<projectID.enc(branch)>/p/<num10>` + `bbrm/<projectID.enc(branch)>` | per-branch paged index of build numbers (P = 50), count in `bbrm` | build controller (CAS) | branch filter, supersede rule (GC) | yes |
| `bidx/running` | map buildID → {project, n, started} (≤ 2,000; over that, `bidx/running.<sh2>` with N in settings) | build controller (CAS) | UI overview (GC) | yes |
| `bidx/failed` | ring of the last 200 failed builds | build controller (CAS) | UI overview (GC) | yes |
| `bnodes/<buildID>` | `BuildNodes`: drv count, bitmaps assigned/promoted/failed, job → drv index ranges (S14 §5.2) | build controller (CAS) | build controller, UI graph (G) | yes |
| `rel/<projectID>/n/<num10>` | `ReleaseRecord` (§05.6.4) | release controller (C, CAS) | API, cache auth (GC) | yes |
| `relv/<projectID>/<enc(name)>/v/<enc(version)>` | release number (uniqueness of name + version) | release controller (C: uniqueness) | API, `bob install` (G) | yes |
| `rpage/<projectID>/p/<num10>` | 50 release entries | release controller (CAS) | UI (GC) | yes |
| `ridx/all/p/<num10>` + `ridxm/all` | registry-wide release index (P = 50) | release controller (CAS) | UI registry (GC) | yes |
| `user/<userID>` | `UserRecord`: name, email, kind, pw hash, `ro` role bindings [{role, project \| `*`, source manual\|oidc}] (truth; §06's `RoleBinding`), `tk` token ids (≤ 100), `ss` session ids (≤ 100), disabled | AccessService (CAS) | auth (GC ≤ 10 s) | yes |
| `uname/<enc(name)>`, `oidcsub/<hash64(iss‖0‖sub)>` | userID | AccessService, OIDC login (C) | login (G) | yes |
| `uidx/all/p/<num10>` + `uidxm/all` | users paged index (P = 200) | AccessService (CAS) | UI access (GC) | no |
| `role/<roleID>` | `RoleRecord`: name, perms (≤ 2,000), builtin | AccessService (CAS) | auth (GC ≤ 10 s) | yes |
| `rname/<enc(name)>` | roleID | AccessService (C) | API (G) | no |
| `roles/all` | role index (≤ 1,000) | AccessService (CAS) | UI (GC) | no |
| `rmem/<roleID>` | derived member list (pending/ok per user) | AccessService (CAS), index-repair | UI "users of role" (GC) | no |
| `oidcmap/all` | OIDC claim → role mappings (≤ 500) | AccessService (CAS) | OIDC login (GC) | yes |
| `sa/<saID>`, `saidx/all` | `ServiceAccountRecord` (incl. `ro` role bindings as in `UserRecord`, `tk` token ids); index (≤ 1,000) | AccessService (CAS) | auth, UI (GC) | yes |
| `tok/<hash64(token)>` | `TokenRecord`: owner, kind, scopes, exp, created | AccessService (C, D) | every API call (G, cache 10 s) | yes |
| `forge/<forgeID>`, `forges/all` | `ForgeRecord` (forge integration; secrets sealed with the orchestrator data key); index | ForgeService (CAS) | pipeline controller (GC) | yes |
| `ftok/<sh2(jobID)>` | `CheckoutRecord`s: outstanding per-job forge tokens {jobID → epoch, forge, handle (token id), expiresAt, forgeExpires}; never the token value (D62) | `GetCheckoutCredential`, revoke at job end, sweeper (CAS) | same + sweeper (GC) | yes |
| `set/<name>` | `SettingRecord`: setting value | SettingsService (CAS) | all (GC 30 s) | yes |
| `promo/<yyyymmdd>/s/<seq20>` + `promot/<yyyymmdd>` | promotion log: {storehash, project, build, at} (SeqLog without head) | promoting orchestrator (Append, **before** the narinfo write) | cache GC (G, walks days older than retention) | no |

### 05.4.3 `JobRecord` and `JobSpec`

```go
// JobRecord is the fleet-side truth of one job while it is not finalised.
type JobRecord struct {
	ID   dynamitedb.KeyField  `pk:"job"`
	ETag dynamitedb.ETagField `etag:"true" cbor:"-"`

	Spec    dynamitedb.DataField[[]byte] `cbor:"sp,omitempty"` // canonical CBOR JobSpec, ≤ 64 KiB
	SpecSig dynamitedb.DataField[[]byte] `cbor:"ss,omitempty"` // Ed25519("bob-jobspec-v1"‖0‖LV(sp)) by FleetConfig key kid
	Kid     dynamitedb.DataField[string] `cbor:"kid,omitempty"`

	State    dynamitedb.DataField[string]    `cbor:"st,omitempty"` // queued|claimed|running|succeeded|failed|cancelled
	Owner    dynamitedb.DataField[string]    `cbor:"o,omitempty"`  // wid while claimed/running
	Epoch    dynamitedb.DataField[int64]     `cbor:"ep,omitempty"` // +1 on every claim
	Attempt  dynamitedb.DataField[int64]     `cbor:"at,omitempty"` // 1 on first claim, +1 on each requeue-claim
	Claimed  dynamitedb.DataField[time.Time] `cbor:"ca,omitempty"`
	Changed  dynamitedb.DataField[time.Time] `cbor:"ch,omitempty"`
	Queued   dynamitedb.DataField[time.Time] `cbor:"qa,omitempty"` // last time it became queued
	Cancel   dynamitedb.DataField[bool]      `cbor:"cx,omitempty"` // cancel requested while running

	Result    dynamitedb.DataField[[]byte] `cbor:"rs,omitempty"` // canonical CBOR JobResult
	ResultSig dynamitedb.DataField[[]byte] `cbor:"rg,omitempty"` // Ed25519 by Owner's sigpub over ("bob-jobres-v1"‖jobID‖epoch‖rs)

	// Promotion of a succeeded job (§06 §6.6.4), written by the promoting orchestrator (CAS):
	// "" (pending) | "done" | "blocked: staging incomplete" | "failed: artifact verification".
	Promotion       dynamitedb.DataField[string]   `cbor:"pm,omitempty"`
	NonReproducible dynamitedb.DataField[[]string] `cbor:"nr,omitempty"` // store paths (R-NFR-006)
}

// JobSpec is written once by the build controller and signed; workers refuse unsigned or
// mis-signed specs (a worker can overwrite another job's record, ADR 0013 T10).
type JobSpec struct {
	V        int      `cbor:"1,keyasint"` // 1
	JobID    string   `cbor:"2,keyasint"`
	BuildID  string   `cbor:"3,keyasint"`
	Project  string   `cbor:"4,keyasint"` // projectID only, no names (R-SEC-003)
	Kind     string   `cbor:"5,keyasint"` // evalbuild | eval | batch
	Targets  []string `cbor:"6,keyasint"` // ≤ 64; labels for evalbuild, drv paths for batch
	Class    string   `cbor:"7,keyasint"`
	Repo     string   `cbor:"8,keyasint"` // forge id + repo id (no URL credentials)
	Commit   string   `cbor:"9,keyasint"`
	Staging  string   `cbor:"10,keyasint"` // staging/<buildID>/<jobID>/<attempt>/ (§05.2.4)
	EstSlotS float64  `cbor:"11,keyasint"`
	Prefer   string   `cbor:"12,keyasint"` // affinity hint (wake order only)
	Created  int64    `cbor:"13,keyasint"`
}

// JobResult is written by the worker that holds the lease.
type JobResult struct {
	V        int      `cbor:"1,keyasint"`
	Status   string   `cbor:"2,keyasint"` // ok | failed | infra
	Complete string   `cbor:"3,keyasint"` // key of COMPLETE.json, "" on failure
	CompSHA  string   `cbor:"4,keyasint"` // hex SHA-256 of COMPLETE.json
	Failed   []string `cbor:"5,keyasint"` // failing packages (labels)
	Error    string   `cbor:"6,keyasint"` // translated error (§02 taxonomy), ≤ 8 KiB
	LogLast  map[string]int64 `cbor:"7,keyasint"` // package → last persisted seq
	Finished int64    `cbor:"8,keyasint"`
}
```

### 05.4.4 `BuildRecord`

Fields: `pr` projectID, `n` number, `st` state, `ph` phase (`evaluating|building|promoting`),
`tr` trigger {kind push|pr|api|remote|retry, by userID, forge event id}, `br` branch, `cm` commit,
`au` commit author (control only), `cr` created, `sa` started, `fi` finished, `pl` platforms,
`sp` superseded-by build number, `rt` retry-of build number, `src` source {git {ref, sha,
fetch_ref} | upload sha256}, `tg` targets, `va` variants, `pi` pipeline runs [{name, state,
release id}], `rp` reported forge statuses {context → last state sent} (status controller,
§06 §6.9.4), `jb` jobs [≤ 1,024 × {id, kind,
class, st, at attempts, ep, worker, dur}], `rs` result {failed packages, error}, `lg` log
partition id (§05.10.1), `rq` last request id.

### 05.4.5 Control-bucket records for §06 (added at integration)

Names as §06 uses them. Same legend as §05.4.2; all keys are known from the request, so every
access is a GET or a conditional write (D60). `hash64` = lowercase hex SHA-256.

| Key | Record / value | Writer (op) | Reader (op) | Hot |
|---|---|---|---|---|
| `repo/<forgeID>/<enc(forge repo id)>` | `RepoLinkRecord`: projectID | ProjectService (C, D) | webhook intake (GC) | yes |
| `sess/<hash64(token)>` | `SessionRecord`: user, kind `cli`\|`browser`, user agent, created, last-used day, idle/max expiry (refresh token `bob_rt_` or cookie `bob_st_`) | AuthService / OAuth token endpoint (C, CAS, D) | every cookie request, refresh (G, cache 10 s) | yes |
| `oac/<hash64(code)>` | `OAuthCodeRecord`: user, client, redirect URI, PKCE challenge, expiry (60 s) | `/oauth/authorize` (C) | `/oauth/token` (G, D If-Match = single use) | no |
| `devc/<hash64(device_code)>` + `devu/<user_code>` | `DeviceCodeRecord`: user code, state pending\|approved\|denied, user, expiry (600 s), last poll; `devu` → device-code hash | `/oauth/device/code` (C), `/device` approval (CAS) | token polling (G) | no |
| `oidcst/<hash64(state)>` | OIDC login state: provider, nonce, PKCE verifier, next, expiry (10 min) | `/auth/oidc/start` (C) | callback (G, D) | no |
| `oidcp/<providerID>`, `oidcps/all` | `OidcProviderRecord` (issuer, client id, sealed secret, `require_mapping`); index (≤ 50) | OidcService (CAS) | login (GC) | yes |
| `oidcmap/all` | `OidcMappingRecord` list (§05.4.2) | OidcService (CAS) | OIDC login (GC) | yes |
| `adminidx` | `AdminIndex`: count of `admin@*` bindings of enabled principals | AccessService (CAS, together with the binding write) | same | no |
| `fdel/<forgeID>/<enc(delivery id)>` | `ForgeDeliveryRecord`: received, event, result, build id (dedupe) | webhook intake (C: create-only) | webhook intake (G) | yes |
| `fdelr/<forgeID>` | ring of the last 200 deliveries (for `ListForgeDeliveries`) | webhook intake (CAS best effort) | UI (GC) | no |
| `skey/<keyname>`, `skeys/all` | `SigningKeyRecord`: Ed25519 seed (sealed), state `ACTIVE`\|`VERIFY_ONLY`\|`RETIRED`, created; index | SettingsService (C, CAS) | promotion, `GetServerInfo` (GC) | yes |
| `ocihmac/<kid>`, `ocihmacs/all` | `OciHmacKey`: 32-byte key (sealed), created, notAfter | key controller (C) | OCI token endpoint + `/v2/*` (GC) | yes |
| `nhook/<id>`, `nhooks/all` | `NotifyWebhookRecord` (URL, sealed secret, events, project filter); index | NotifyService (CAS) | notifier (GC) | yes |

Mappings of other §06 names: `OrchestratorRecord` = `OrchRecord` (`orch/<orchID>`, its public
keys are in `FleetConfig.k`); `ForgeIntegrationRecord` = `ForgeRecord`; `SettingsRecord` =
`SettingRecord`; `CheckoutRecord` = one entry of `ftok/<sh2(jobID)>`; `ReleaseIndex` = `relv` +
`rpage` + `ridx`; `RoleBindingRecord` = the `ro` entries of `UserRecord`/`ServiceAccountRecord`;
`RefHeadRecord` is not used (the concurrency rule reads `bbr`, §05.6.2). Signing keys (`skey`,
`okey`) and every sealed secret live only in the control bucket, which workers cannot read.

#### R-STORE-004 Lists without scans (kept, made exact)
- **Statement:** every UI and API list reads index documents or paged indexes on known keys.
  The access plan per page is the table in §05.5.2. No list needs more than
  `1 + ceil(page size / P)` index GETs plus detail GETs for at most the visible rows.
- **Acceptance:** for each row of §05.5.2, a test counts the requests through the proxy and
  checks them against the column "requests (page 1)". Only rows reachable through the §06 API are
  listed in the acceptance; "Build by number" and "Release by version" join it when the by-number
  and by-version lookups exist (Gate H, G19; follow-up in `plan/m1.8-ledger.md`).
- **Source:** R-STORE-004, D60.

---

## 05.5 Hot paths

### 05.5.1 Fleet hot paths (per occurrence)

| Path | Who, how often | Requests | Ops |
|---|---|---|---|
| Idle poll | worker, per eligible class, every `fleet.poll_interval` (30 s ± 20 %) | **1** | G `qs/<cls>/s/<cursor>` → 404 |
| Config refresh | worker and orchestrator, every 30 s | 1 (304) | GC `fleet/cfg` |
| Worker record refresh | worker, every 60 s | 1 (304) | GC `worker/<wid>` |
| Heartbeat | worker, every `fleet.heartbeat` (15 s) | 1 | P `whb/<wid>` |
| Enqueue job | build controller | 5–7 | CAS `jact/<sh2>` (2), C `job/<id>`, G `qt`, C `qs/…` (+1 per collision), CAS `qt` best effort (+2) |
| Claim (targeted by wake-up) | worker | 4 (+2 when advancing head) | G slot, G job, CAS-PUT job (If-Match from that GET), CAS-PUT slot `tk` |
| Claim (polling, backlog) | worker | 5 | G `qt` for the spread window, then as above |
| Job watch | worker, per held job, every 30 s | 1 | G `job/<id>` (sees cancel, requeue) |
| Start / result | worker | 2 each | G + PUT If-Match `job/<id>` |
| Finalise | build controller | 7 | G job, CAS build (2), D job (2); `jact` removal CAS (2) batched per pass |
| Requeue | job reconciler | 4 | CAS job (2), Append slot (2) |
| Reconcile pass | job-reconciler leader, every 10 s | N_shards (304) + busy workers / 15 s | GC `jact/*`, G `whb/<owner>` once per owner per 15 s |
| Registration | worker once, registrar | worker 3–4, registrar 4 | C `wreg`, Append `wrs`; registrar G `wrs` next, G `wreg`, C `worker`, CAS `widx` |

#### R-FLEET-009 Request budget per job
- **Statement:** a job that is enqueued, claimed by a woken worker, run and finalised costs
  ≤ 26 S3 requests on control+fleet (enqueue 7 + claim 6 + start 2 + result 2 + finalise 7, plus
  2 spare for collisions), excluding heartbeats (shared per worker) and job watch (1 per 30 s).
- **Acceptance:** 300-job drain on RustFS with wake-ups on, counted by the proxy (no idle polls
  counted): mean ≤ 26 requests per job, 0 LIST. **[proposal]** [8c/15G].
- **Source:** S9 §3.2 (12 requests with LIST), V2 (§05.13), D60.

#### R-FLEET-010 Idle fleet cost
- **Statement:** an idle worker eligible for one class makes ≤ 0.15 S3 requests/s on average.
  100 idle workers cost ≤ 15 req/s, all GET except heartbeats (100/15 s = 6.7 PUT/s).
- **Acceptance:** a 10 min run of 100 in-process idle workers: total ≤ 15 req/s, PUT ≤ 7/s,
  LIST 0. **[proposal]** [8c/15G].
- **Source:** S9 §3.5 (27 req/s with LIST at 5 s); D54 (slow polling). At AWS list prices:
  GET ≈ 8.3/s ≈ $8.6/month, PUT ≈ 6.7/s ≈ $87/month. Raising `fleet.heartbeat` to 60 s
  cuts PUT to $22/month; a dead worker's jobs then requeue later (R-FLEET-003).

### 05.5.2 UI and API read plan

| Page / RPC (§06) | Requests (page 1) | Keys |
|---|---|---|
| Overview | 3 + classes | GC `bidx/running`, GC `bidx/failed`, GC `fleet/cfg`; queue depth per class = G `qt` + G `qh` (approximate: tail − head) |
| Projects | 1 + pages | GC `pidxm/all`, GC last `pidx/all/p/*` page |
| Builds of a project (newest first, 50) | 2–3 | GC `pctr/<p>`, GC `bpage/<p>/p/<last>` (+ previous page when the last is partial) |
| Builds of a branch | 2–3 | GC `bbrm/…`, GC `bbr/…/p/<last>` (+1) |
| Build detail | 2 + live jobs | G `build/<id>`, G `bnodes/<id>`, G `job/<id>` per non-terminal job (≤ visible rows) |
| Build by number `/p/<name>/b/<n>` | 3 | G `pname`, GC `bpage/<p>/p/<n/50>`, G `build/<id>` |
| Releases of a project | 2–3 | GC `pctr`, GC `rpage/<p>/p/<last>` |
| Release by version | 2 | G `relv/…`, G `rel/…` |
| Registry | 2 | GC `ridxm/all`, GC `ridx/all/p/<last>` |
| Fleet: workers | N_widx (default 4) | GC `widx/*` |
| Fleet: orchestrators | 1 + members | GC `fleet/cfg`, G `orch/<id>` per member |
| Access: users / roles / SAs / OIDC | 2 / 1 / 1 / 1 | `uidxm`+`uidx` page; `roles/all`; `saidx/all`; `oidcmap/all` |
| Users of a role | 1 | GC `rmem/<role>` |
| Auth of an API call (cache miss) | 2–3 | G `tok/<hash>`, GC `user/<id>` or `sa/<id>`, GC `role/<id>` per role (cached 10 s) |

Filters (R-UI-006) work client-side on the loaded pages. State and branch filters use the
dedicated indexes (`bidx/*`, `bbr/*`).

---

## 05.6 State machines

Notation: every "→" is one `store.CAS` on the named record unless stated. "skew" =
`fleet.skew` (5 s **[proposal]**). Terminal states are absorbing.

### 05.6.1 Job (`job/<jobID>.st`)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| — | enqueue (build controller) | build running, job id in `bnodes` | CAS `jact` add (intent first); C `job` {queued, `qa`=now, signed spec}; Append slot `qs/<cls>` → `j` | queued |
| queued | claim (worker W) | spec signature valid; W eligible for class; `st`=queued | `o`=W, `ep`+1, `at`+1, `ca`=now; then CAS slot `tk`=W | claimed |
| queued | slot reaches worker but `st`≠queued | — | CAS slot `tk`=`stale` | (unchanged) |
| queued | re-slot (reconciler) | `qa` + `fleet.reslot_after` (60 s) < now | Append new slot; `qa`=now | queued |
| claimed | start (W) | `o`=W ∧ `ep`=mine | `ch`=now | running |
| claimed, running | result (W) | `o`=W ∧ `ep`=mine ∧ result signed by W | `rs`, `rg` set | succeeded \| failed |
| claimed, running | lease expired (reconciler) | `o`=W ∧ max(`ca`, `whb/W.at`) + `fleet.lease_ttl` + skew < now, or `worker/W.st` ∈ {revoked, gone} | `o`="", `qa`=now; then Append slot | queued |
| queued | cancel (controller) | — | — | cancelled |
| claimed, running | cancel (controller) | — | `cx`=true; Wake/CancelJob to W | (unchanged) |
| claimed, running | cancel ack (W) or `cx` ∧ lease expired | `o`=W ∧ `ep`=mine (ack) | — | cancelled |
| succeeded, failed, cancelled | finalise (build controller) | build record holds the job's final row | copy summary into `build.jb`; D `job` (If-Match); CAS `jact` remove | (deleted) |

Requeue keeps `ep` (the next claim increments it). A paused worker that writes after a requeue
fails the If-Match or the `ep` guard and gets `fleet: lease lost (job requeued or taken by
another worker)`.

#### R-FLEET-003 Claims, leases, requeue (kept, made exact)
- **Statement:** as in the table. Liveness of a claimed job comes from the owner's heartbeat
  `whb/<wid>`, not from per-job renewals. A claim gives `fleet.lease_ttl` (60 s) of grace before
  the first heartbeat counts.
- **Acceptance:** chaos run on RustFS with 3 orchestrators (no leader and with leader), 20
  workers, 300 jobs; 20 % of workers die (SIGKILL) or pause for 2 × TTL + 1 s and then try to
  complete. Result: 0 double successful acceptances, 0 lost jobs (every job reaches a terminal
  state), 0 accepted stale results, 0 LIST. Dead-worker jobs are queued again within
  `lease_ttl + reconcile interval + skew` (75 s at defaults; a test profile uses 3 s TTL).
- **Source:** R-FLEET-003, S9 §3.3, ADR 0011 (amended: heartbeat-based liveness).

#### R-FLEET-011 Queue liveness
- **Statement:**
  - A queued job is always reachable through at least one open slot at or after the head hint,
    or it is re-slotted within `reslot_after`.
  - The build controller writes `job` **before** its slot (job-first order).
  - A worker that finds a slot whose job is missing marks it `orphan`. A worker that finds a
    slot whose job is not queued marks it `stale`.
  - Workers never skip an open slot of a queued job except after losing the claim CAS.
- **Acceptance:** 300-job drain with 20 claimers and 3 enqueuers: 300/300 claimed, 0 doubles.
  The protected regression test inverts the order (slot first) and must fail by losing a job,
  as V2 run 1 did.
- **Source:** V2.

### 05.6.2 Build (`build/<buildID>.st`)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| — | trigger (forge event, API, remote, retry) | project exists; dedupe key (event id) not seen | CAS `pctr.b`+1 → n; C `build` {pending}; `bpage`/`bbr` Set n (intent first); concurrency rule | pending |
| pending | plan (build controller) | not superseded | create the eval or evalbuild job (§05.7) | queued |
| queued, running | evaluation result: no pipeline matched the event (§06 §6.9.4) | forge-triggered build | `fi`; forge status cleared | skipped |
| queued | first job claimed | — | `sa`=now; `bidx/running` add | running |
| running | job finished | other jobs remain or new batches are ready | update `jb`, `bnodes`; enqueue ready batches | running |
| running | all requested targets promoted | — | `fi`; `bidx/running` remove; forge status | succeeded |
| running | a job failed (not infra) | — | cancel queued jobs; let running jobs finish; then `fi`; `bidx/failed` add | failed |
| running | job infra failure | attempts < `fleet.max_attempts` (3) | requeue is the job machine's | running |
| running | job infra failure | attempts ≥ 3 | as failed, error `build failed: job <id> failed 3 times on workers (infrastructure)` | failed |
| pending, queued, running | cancel (user) | permission | cancel all jobs | cancelled |
| pending, queued, running | newer build on the same branch with `concurrency=supersede` | project setting | cancel jobs; `sp`=n′ | superseded |
| terminal | retry (user) | — | new build with `rt`=n (never reopens) | — |

Concurrency rule (R-PIPE-005): per project, setting `concurrency` ∈ `queue` (default for
the default branch and for tags) | `supersede` (default for other branches and PRs) |
`parallel`. The rule is evaluated on `bbr/<branch>` (last two entries; tags use the ref name).
A build that runs a release pipeline is never superseded. "Waits in queue" = stays `pending`
until the older build is terminal. (§06 §6.9.5 uses this rule; aligned at integration.)

### 05.6.3 Worker (`worker/<wid>.st`; no record = registering)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| — | registrar reads `wrs/reg/s/<cursor>` | `wreg/<wid>` valid (S11 §5.2 checks); ≤ 50 approvals per pass; pending ≤ 1,000 | C `worker/<wid>` {active, g 0, n 1, sealed token}; `widx` add; `ctl/registrar.cu`+1 | active |
| — | invalid registration | — | `ctl/registrar.cu`+1, log `worker registration rejected: <reason>` | — |
| active | token half-life | `te` − now < TTL/2 ∧ heartbeat age < 2 × heartbeat | `n`+1, new sealed token | active |
| active | heartbeat stale | `whb.at` + `fleet.gone_after` (5 min) < now | — (jobs requeue by their own rule) | stale |
| stale | heartbeat fresh | — | — | active |
| active, stale | drain (admin) | — | Drain over Attach | draining |
| draining | undrain (admin) | — | — | active |
| any | revoke (admin) | — | `g`+1, clear `ct` | revoked |
| stale, revoked | GC | stale > 24 h, or revoked > 7 d | D `worker`, `wreg`, `whb`; `widx` remove | (deleted) |

Worker-side: `START → REGISTERING (C wreg, Append wrs) → WAITING (G worker/<wid> every 1 s, up
to 5 min, then re-register with new keys) → ACTIVE`. A worker exits when its record says
`revoked`; a new process means new keys and a new wid (S11 §5.1).

#### R-FLEET-001 Zero-setup registration (kept)
- **Acceptance:** a worker started with only fleet/analytics/artifacts S3 credentials becomes
  `active` and holds a valid token within **5 s** p99 (registrar poll 2 s) **[proposal]**
  [8c/15G]. No admin step, 0 LIST.
- **Source:** R-FLEET-001, D13′, D57, S11.

### 05.6.4 Release (`rel/<projectID>/n/<num10>.st`)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| — | create (API or pipeline step) | build succeeded; name + version unique (C `relv`) | CAS `pctr.r`+1; C `rel` {pending, artifacts from build}; `rpage` Set | pending |
| pending | promote (release controller) | all artifact store paths promoted | copy assets to `rel/…` prefix (artifacts bucket), set visibility (§06) | publishing |
| publishing | assets written | every asset object exists (G) | `ridx` Set if public | published |
| pending, publishing | error | — | error text | failed |
| failed | retry | — | — | pending |
| published | visibility change (admin) | — | update markers | published |
| published | delete (explicit) | permission `ReleaseService.DeleteRelease` | remove markers and assets; `relv` kept as a tombstone | deleted |

Published releases are immutable apart from visibility. Retention never selects a release
(R-STORE-007).

### 05.6.5 Controller lease (`ctl/<name>`)

| State | Event | Guard | Action | Next |
|---|---|---|---|---|
| free (absent) | TryAcquire | — | C {o=me, ep=1, ex=now+TTL} | held(me) |
| held(me) | TryAcquire | o=me | ex=now+TTL | held(me) |
| held(X) | TryAcquire | ex + skew < now | o=me, ep+1 | held(me) |
| held(me) | shutdown | — | ex=now (CAS) | free |

TTL = 3 × interval. Correctness never depends on the lease: every action of a controller is a
CAS that is safe if two leaders overlap (S9 §3.3 measured no-leader runs as correct).

---

## 05.7 Scheduling (D53) and dispatch (D54)

#### R-FLEET-005 Scheduling unit (resolved)
- **Statement:** a job realises a set of ≤ 64 derivations on one worker. By default each build has
  one `evalbuild` closure job. The build controller splits the build into an `eval` job plus
  ready-set `batch` jobs only when the S14 planner predicts a gain of ≥ max(10 s, 30 % of the closure-job time) with the
  idle eligible workers (`split = I ≥ 2 and T1 − Tsplit ≥ max(10 s, 0.3 × T1)`; amended 2026-10-22,
  ADR 0016). Promotion is per job; jobs depend on each other only through promoted
  paths, never through `staging/`.
- **Acceptance:**
  - Planner unit tests on the S14 graphs: fixture → no split; go200, rust129 and wide2000 at
    W = 100 with 2 slots → split (fixture: 14.7 s vs 19.4 s, no split). Boundary rows: a predicted
    gain of exactly 10 s and exactly 30 % splits; just below either bound does not; just above
    both does.
  - The predicted gain is within 25 % of the S14 model.
  - A real-run test on 4 local workers: a split build of the 73-drv layer graph is faster than
    the closure job (S14 quiet: closure 20.6 s; per-drv 9.3 s; a batch build must be ≤ 15 s)
    **[proposal]** [8c/15G].
- **Source:** D53, ADR 0016, S14 §5.1, `measure/quiet/s14-sched.log`.

Planner, batching and fair share: exactly S14 §5.1 and §5.4 rules 2–4. `ready` drvs come from
`bnodes`; `idle eligible workers` = Σ free slots in `widx` (refreshed every 15 s) / median
slots, counting only workers whose heartbeat is fresh.

#### R-FLEET-012 Dispatch: slow polling plus wake-up
- **Statement:**
  - Workers poll their class queues every `fleet.poll_interval` (30 s ± 20 %), and again at once
    after finishing a job.
  - After an enqueue, the enqueuing orchestrator sends `Wake{class, slot, job}` over Attach to
    one idle eligible worker attached to it: the `Prefer` worker first, otherwise the worker
    with the most free slots, ties broken at random.
  - If the job is still queued after 2 s, it wakes a second worker.
  - A woken worker GETs exactly that slot (no herd).
  - Claims stay CAS in the bucket. A lost wake-up costs at most one poll interval.
  - A polling worker with a backlog picks a random slot in `[cursor, tail)` (spread) before
    falling back to its cursor.
- **Acceptance:**
  - With wake-ups: enqueue → claim p99 ≤ 1 s with 20 idle workers (V2 spread run: p50 106 ms,
    p99 182 ms) **[proposal]** [8c/15G].
  - Wake-ups disabled: p99 ≤ poll_interval × 1.2 + 1 s.
  - Duplicate wake-ups never cause a double claim (the chaos run includes them).
- **Source:** D54, S9 O-S9-1, V2.

#### R-FLEET-004 Platform, capabilities, labels (kept)
- **Acceptance:** a job of class `x86_64-linux.<h>` is never claimed by an aarch64 worker, or
  by a worker that lacks a required cap or label (property test over 1,000 random cap/label
  sets). Workers only read slots of classes they satisfy.

---

## 05.8 Worker isolation (D56)

#### R-FLEET-008 / R-SEC-000 Worker isolation (kept, made exact)
- **Statement:**
  - **Root worker.** `bob-server worker` runs as uid 0 and refuses to start otherwise
    (`✗ the worker must run as root; builds themselves never run as root` /
    `hint: run it as a systemd system service or a privileged container`).
  - **Job container.** Each job runs in a bob-built job container (no OCI runtime, no daemon).
    The container has:
    - its own cgroup v2 leaf `bob-worker.slice/job-<jobID>`, with `memory.max`, `pids.max` and
      `cpu.max` from the slot config;
    - new mount, pid, ipc, uts and cgroup namespaces;
    - a private `/tmp`;
    - `/nix` bound from the worker's persistent store `/var/lib/bob/worker/nix`;
    - no worker credentials: no env, no files, and the S3 credential file is not mounted.
  - **Nix as root inside the container.** bob's embedded static Nix runs with:
    `sandbox = true`, `sandbox-fallback = false`, `build-users-group =` (empty),
    `auto-allocate-uids = true`, `use-cgroups = true`, `allowed-users = root`,
    `trusted-users = root`, `substituters = <bob cache> https://cache.nixos.org`,
    `require-sigs = true`. **Every build runs as an auto-allocated non-root uid** in Nix's
    sandbox (own net ns: no host network, no IMDS). The store is root-owned, so a build cannot
    rewrite input paths. The per-job overlay `/nix` from S13 is not used.
  - **Evaluation.** `bob.go` evaluation runs in evalbox (ADR 0015 decision 1) inside the job
    container. Its userns maps uid 1000 to a dedicated unprivileged host uid
    `fleet.eval_uid_base + slot` (default 2,000,000,000 + slot), never to 0 or to the worker uid.
  - **Fetches.** Workers never build user-influenced FODs. bob fetches and runs `nix store add`
    (ADR 0015 decision 3).
  - **Refusal.** The worker refuses to start without cgroup v2, Landlock ABI ≥ 4, or Nix
    `auto-allocate-uids` support (`✗ worker cannot isolate builds: <reason>`).
- **Acceptance:**
  - The S13 hostile suite (15 evaluator cases) passes on a root worker.
  - Additional cases:
    - a build that tries `chmod`/write on an input store path gets EPERM/EROFS, and the
      persistent store is unchanged (`nix store verify --check-contents` = 0 errors);
    - `id -u` inside a build ≠ 0;
    - a build cannot read `/proc/<worker pid>/environ` or the S3 credential file;
    - a build cannot connect to 169.254.169.254 or to the host loopback;
    - after the job, 0 processes remain in the job cgroup.
- **Source:** D56, ADR 0015 (amended), S13 §5.1/§5.4.

#### R-FLEET-013 Concurrent jobs per worker
- **Statement:** a worker runs up to `worker.slots` jobs (default `max(1, nproc / 2)`), each in
  its own job container, against one shared persistent store (Nix store locking). The worker
  keeps ≥ `worker.min_free` (15 %) of the store disk free and runs `nix store gc` with the
  newest 24 h of outputs as GC roots.
- **Acceptance:** 2 slots, 2 jobs that build overlapping closures at the same time: both
  succeed, and the shared paths are built once.
- **Source:** S14 (slots), S13.

---

## 05.9 Worker protocol and authentication (D57)

### 05.9.1 `proto/bob/worker/v1/worker.proto`

```proto
syntax = "proto3";

package bob.worker.v1;

import "bob/authz/v1/authz.proto";

option go_package = "github.com/megakuul/bob/internal/gen/bob/worker/v1;workerv1";

// WorkerControlService is served by every orchestrator and called only by workers.
// Authentication: header "Authorization: Bob-Worker <bwt1 token>" and "Bob-PoP" (§05.9.2).
// The token is obtained only through the fleet bucket (worker/<wid>, sealed). User tokens
// are refused with PERMISSION_DENIED. Transport: HTTP/2 + TLS; the worker pins the
// orchestrator's SPKI hash from FleetConfig.
service WorkerControlService {
  // Attach is the long-lived control channel between one worker and one orchestrator.
  // A worker keeps one Attach per member in FleetConfig. The first worker message is Hello.
  // The worker reopens the stream before its token expires (new token, new PoP).
  rpc Attach(stream AttachRequest) returns (stream AttachResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_WORKER};
  }

  // GetCheckoutCredential returns the job's source credentials: a forge credential scoped to the
  // job's repository (or the key of an uploaded source snapshot) and a download token, valid
  // only while the caller holds the job (owner + epoch checked on job/<id>). Flow: §06 §6.9.6.
  rpc GetCheckoutCredential(GetCheckoutCredentialRequest) returns (GetCheckoutCredentialResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_WORKER};
  }
}

message AttachRequest {
  oneof msg {
    Hello hello = 1;
    SlotState slots = 2;
    JobEvent job_event = 3;
    LiveChunk live_chunk = 4;
    Pong pong = 5;
  }
}

message AttachResponse {
  oneof msg {
    Welcome welcome = 1;
    Wake wake = 2;
    LiveSubscribe live_subscribe = 3;
    LiveUnsubscribe live_unsubscribe = 4;
    CancelJob cancel_job = 5;
    Drain drain = 6;
    Ping ping = 7;
  }
}

message Hello {
  string worker_id = 1;
  string platform = 2;              // "x86_64-linux" | "aarch64-linux"
  repeated string class_ids = 3;    // classes this worker satisfies (from FleetConfig)
  int32 slots_total = 4;
  int32 slots_free = 5;
  string version = 6;               // bob-server version
}

message Welcome {
  string orchestrator_id = 1;
  int64 fleet_config_gen = 2;       // worker re-reads fleet/cfg when it is older
}

message SlotState {
  int32 slots_free = 1;
  repeated string running_job_ids = 2;
}

enum JobEventKind {
  JOB_EVENT_KIND_UNSPECIFIED = 0;
  JOB_EVENT_KIND_CLAIMED = 1;
  JOB_EVENT_KIND_RUNNING = 2;
  JOB_EVENT_KIND_FINISHED = 3;      // result written to job/<id>
  JOB_EVENT_KIND_ABANDONED = 4;     // worker gave up (lease lost, cancel acknowledged)
}

// JobEvent is a hint so controllers react without waiting for their next pass.
// The job record stays the truth; orchestrators re-read it before acting.
message JobEvent {
  string job_id = 1;
  int64 epoch = 2;
  JobEventKind kind = 3;
}

// Wake asks the worker to try one slot now (D54).
message Wake {
  string class_id = 1;
  uint64 slot = 2;
  string job_id = 3;
}

// LiveSubscribe asks the worker for its in-memory log of a job (D55): first everything after
// the cursors, then new lines every 250 ms, until expires_unix_ms (renewed by repeating the
// message) or LiveUnsubscribe.
message LiveSubscribe {
  string subscription_id = 1;
  string job_id = 2;
  map<string, LiveCursor> cursors = 3; // package label -> last line the orchestrator has
  int64 expires_unix_ms = 4;           // now + 15 s
}

message LiveUnsubscribe {
  string subscription_id = 1;
}

message LiveCursor {
  int64 seq = 1;
  int32 attempt = 2;
}

message LiveChunk {
  string subscription_id = 1;
  string job_id = 2;
  int64 epoch = 3;
  repeated LiveLine lines = 4;         // ≤ 5,000 lines and ≤ 4 MiB per chunk
  repeated LiveRestart restarts = 5;
  repeated LiveFinal finals = 6;
}

message LiveLine {
  string package = 1;
  int64 seq = 2;
  int32 attempt = 3;
  int32 stream = 4;                    // 1 stdout, 2 stderr, 3 bob
  int64 ts_unix_nano = 5;
  string text = 6;                     // UTF-8, ≤ 4 KiB
}

message LiveRestart {
  string package = 1;
  int32 attempt = 2;
  int64 from_seq = 3;
}

message LiveFinal {
  string package = 1;
  int64 last_seq = 2;
  int32 attempt = 3;
}

message CancelJob {
  string job_id = 1;
}

message Drain {
  bool drain = 1;                      // true: finish running jobs, claim nothing new
}

message Ping {
  int64 unix_ms = 1;
}

message Pong {
  int64 unix_ms = 1;
}

message GetCheckoutCredentialRequest {
  string job_id = 1;
  int64 epoch = 2;
}

message GetCheckoutCredentialResponse {
  // HPKE (ADR 0013 suite) to the worker KEM key, info = "bob-wco-v1"‖0‖LV(wid)‖LV(job_id)‖LV(epoch).
  // Plaintext: canonical CBOR {1: clone_url (https, no credentials), 2: username,
  // 3: token (short-lived, repository-scoped; D62 for Forgejo), 4: expires_unix, 5: fetch_ref, 6: sha}.
  // Empty for upload sources.
  bytes sealed_checkout = 1;
  string source_key = 2;               // artifacts key src/<projectID>/<hex>.tar.zst, else ""
  string download_token = 3;           // bob_dt_, download scope, the job's project only, deleted at job end
  int64 expires_unix = 4;
}
```

### 05.9.2 Tokens and proof of possession

Exactly S11 §5.3 (HPKE MLKEM768-X25519, `bwt1` token, Ed25519 PoP, 1 h TTL, rotation at half
life, record cache ≤ 10 s). STS (S11 §3.6, ADR 0013 decision 8) is removed (D57).
Orchestrator signing keys: the seeds are in control `okey/<kid>`, the public keys in
`fleet/cfg.k`. Job specs (`ss`) and tokens are signed with them.

#### R-FLEET-002 No impersonation (kept)
- **Acceptance:** S11's 31 registration checks, plus:
  - a worker's token used with another worker's key → `unauthenticated: proof of possession invalid`;
  - a job result signed by a non-owner → ignored by the controller (metric
    `bob_fleet_forged_results_total`);
  - a `JobSpec` with a bad signature → the worker marks the slot `stale` and does not run it;
  - worker A's Attach cannot subscribe to or cancel worker B's jobs.
- **Source:** R-FLEET-002, S11, ADR 0013.

#### R-FLEET-006 Checkout credentials only for the held job (kept)
- **Statement:** Workers get a checkout credential only for the job they hold (owner + epoch on
  `job/<id>`, state claimed or running), for one repository, revoked at job end. The flow and the
  forge-side token handling are §06 §6.9.6; the record is a `CheckoutRecord` in `ftok/<sh2>`.
- **Acceptance:**
  - `GetCheckoutCredential` with a job the caller does not own, or a stale epoch →
    `permission_denied: worker holds no lease on job <id>`.
  - Live Forgejo: worker A with the lease gets a credential that clones repo R and gets 403 on
    repo R2 the bot can read (the forge decides the status for a repository outside the token's
    scope; Forgejo answers 403, measured in S16); after the job ends, the token returns 401 within 30 s.
  - Orchestrator killed between mint and revoke: the `forge-sweeper` (60 s, over `ftok/*`)
    revokes the token within 60 s + skew after the job is terminal; a token without a record is
    removed by the Forgejo name-timestamp sweep (§06 §6.9.6).
  - Fork PR: fetches `refs/pull/N/head` from the base repo with the base-repo credential.
- **Source:** R-FLEET-006, D62, ADR 0018, S16 §3.3; merged with §06 §6.9.6 at integration.

### 05.9.3 RBAC for worker RPCs

| RPC | Principal | Permission | Check |
|---|---|---|---|
| `bob.worker.v1.WorkerControlService/Attach` | worker token + PoP | `worker` (built-in, not assignable to roles) | worker record active, gen/serial |
| `bob.worker.v1.WorkerControlService/GetCheckoutCredential` | worker token + PoP | `worker` | as above + job owner + epoch |

The RBAC endpoint test (R-AUTH-006) lists both as `principal=worker` endpoints. There is no
`bob.peer.v1` service (D55).

#### R-SRV-100 Worker reachability
- **Statement:** workers open outbound connections to every orchestrator endpoint in
  `fleet/cfg.m`. Orchestrators never connect to workers. Without any Attach stream a worker
  still works fully (poll, claim, result through S3); only wake-ups, live logs and checkout
  credentials need an orchestrator.
- **Acceptance:** with all Attach streams blocked by the test proxy, a 50-job drain completes.
  The pickup latency is then bounded by the poll interval, and a job that needs checkout fails
  with an infrastructure error after 3 attempts.
- **Source:** D54, D55, D57.

---

## 05.10 Logs (D55)

### 05.10.1 Lake partitions

lake is used at HEAD (`045e301`, L2 fixed) through the URL-prefix trick. Each partition is one
lake "bucket": `lake.NewFromClient(ctx, s3.New(…, BaseEndpoint: <endpoint>/<analytics bucket>),
"<partition>")`. lake writes `<analytics bucket>/<partition>/lakedb.catalog` and
`<partition>/<table>/…`, and never lists (V4).

| Partition id | Tables | Writers | Contents |
|---|---|---|---|
| `log-<yyyymmdd>-<k>` | `log_line` | workers | lines of builds whose ULID time is that UTC day; `k = fnv32a(buildID) mod log.shards` (4) |
| `run-<yyyymm>` | `package_run` | workers | one row per finished package build |
| `job-<yyyymm>` | `job_run` | build controller | one row per finalised job (duration, worker, class, drv count), feeds the planner |

The tables are S10 §5.1 `LogLine` and `PackageRun`, without a `sort` tag (L1 still open), plus
`JobRun {BuildID, JobID lake.String; Class, Worker string; Drvs, SlotMs, WallMs int64}`.
`BuildRecord.lg` stores the log partition id.

#### R-LOG-001 Periodic batched writes (kept, D55 interval)
- **Statement:**
  - A worker buffers lines in memory and commits to its partition every `log.flush_interval`
    (30 s), or at 10,000 lines / 2 MiB, and at package end and job end.
  - The bucket-wide budget is ≤ 5 commits/s per partition. The interval grows to
    `max(30 s, active workers on that partition / 5 s)`.
  - A non-`ErrOptimisticLock` commit error is ambiguous (see STATUS). The batch is retried, and
    readers dedupe by `(build, package, attempt, seq)`.
  - Lines survive crashes up to the last successful commit.
- **Acceptance:**
  - 50 workers × 200 lines/s for 5 min on RustFS with lake HEAD: after the run, every line
    committed before each worker's last commit is readable exactly once (after dedupe); 0 lost
    lines among committed batches.
  - A worker killed with SIGKILL loses only lines after its last commit.
- **Source:** R-LOG-001, D20, D55, S10 §5.2, lake 045e301.

#### R-LOG-100 lake workarounds until upstream fixes
- **Statement:** until the owner fixes L1, L3 and L4 upstream:
  - no `sort` tags;
  - compaction only by the `lake-maintenance` leader, with a `lake.Bucket` object used by
    nothing else;
  - readers retry a query once with a fresh `lake.Bucket` on `NoSuchKey`;
  - compaction only for partitions that are older than 10 min and written within the last
    2 days (F7).
- **Acceptance:** the S10 repro tests L3 (two compactors) and L4 (read during compaction) pass
  with these rules: no duplicates, and no failed query.
- **Source:** S10 §3.7. **[open]** Owner: fix L1/L3/L4 upstream (reference patch
  `spikes/s10-lake/lake-s10.patch`).

### 05.10.2 Worker in-memory log

Per job, the worker keeps every line since the last successful commit, plus the last 10,000
committed lines per package (ring). The buffer is never blocked by S3 or by subscribers. Seq is
per (build, package) and continues across attempts. Attempt n ≥ 2 starts after the last
persisted seq (`JobResult.LogLast` of the previous attempt, or a lake read) and emits a
`LiveRestart`.

### 05.10.3 Live pull

#### R-LOG-002 Live log is pulled on demand (replaces fan-out/peering)
- **Statement:**
  - When at least one follower on orchestrator O watches build B, O does the following for each
    non-terminal job of B: it reads `job/<id>` to find the owner W and sends `LiveSubscribe`
    over W's Attach stream to O, with the cursors O already has.
  - O renews the subscription every 5 s (expiry 15 s) and sends `LiveUnsubscribe` when the last
    follower leaves.
  - W sends `LiveChunk` every 250 ms while the subscription is alive. One subscription per
    (orchestrator, job), whatever the number of followers.
  - Without followers, a worker sends no log data to any orchestrator.
  - There is no orchestrator↔orchestrator connection.
- **Acceptance:**
  - 20 builds × 200 lines/s, 50 followers on one build spread over 3 orchestrators: line →
    follower p99 ≤ 1 s **[proposal]** (S17 fan-out at 200 ms push: p99 223 ms) [8c/15G].
  - With 0 followers, the bytes from workers to orchestrators are ≤ 1 KiB/s per worker
    (Hello/SlotState/Pong only).
  - Kill the worker's Attach target → the follower stalls ≤ 3 s (its orchestrator falls back
    to the lake), 0 gaps and 0 duplicates.
- **Source:** D55 (supersedes D12 and S17's push), ADR 0019 (rewritten).

#### R-LOG-101 Merge and fallback
- **Statement:** orchestrators merge lake rows and live chunks per follower with the S17 §5.4
  algorithm (valid attempt for seq s = `max{a : from(a) ≤ s}`).
  - O reads the lake for a build when:
    - a follower has a gap for 1 s;
    - no live chunk arrived for 6 s;
    - the job's worker is not attached to O.
  - While degraded, O reads the lake every 5 s, shared per (O, build) and cached by object key,
    and sets `delayed = true`.
- **Acceptance:** the S17 failure matrix (12 runs) with pull instead of push: 0 gaps, 0
  duplicates, final stream = lake content. Degraded latency p99 ≤ `flush_interval` + 10 s
  **[proposal]**.
- **Source:** S17 §5.4/§5.5.

#### R-LOG-003 Attribution per package (kept)
- **Acceptance:** `GetLog(build, package)` returns only the lines of that package; the
  failing-package view of a 50-package build shows the lines of one package. p99 ≤ 500 ms on the
  S10 100M-line data set **[proposal]** (S10: 111 ms).

#### R-LOG-102 Hostile worker input
- **Statement:** an orchestrator accepts a `LiveChunk` only for a job whose record has
  `o`=caller and `ep`=chunk.epoch, with ≤ 4 KiB per line, ≤ 5,000 lines and ≤ 4 MiB per chunk,
  and ≤ 1 MiB/s per worker (4 MiB burst). It drops a chunk that violates these limits and logs
  `invalid_argument: log line invalid: <package>/<seq>`. The UI renders log text inert.
- **Acceptance:** S17's 14 security cases adapted to pull: oversize, flood, cross-job chunk and
  stale epoch are all rejected, and follower output is unchanged. The forged-chunk tests need a
  worker Attach client (`tests/serverenv/workerclient`, Gate H G20 follow-up for the milestone that
  implements worker registration).

### 05.10.4 Reader API (for §06)

```go
package logstore // import "github.com/megakuul/bob/internal/logstore"

// Reader reads persisted logs from lake partitions.
type Reader struct{ /* unexported */ }

// NewReader returns a reader over the analytics bucket; it keeps the last 4 downloaded
// shards (≤ 256 MiB) in memory keyed by object key.
//
// Example:
//
//	r := logstore.NewReader(s3client, "bob-analytics")
//	lines, err := r.Package(ctx, buildID, "//svc/api:server", 0, 10000)
func NewReader(client *s3.Client, analyticsBucket string) *Reader

// Package returns the lines of one package after afterSeq, ordered by seq, deduplicated by
// (attempt, seq) and filtered by the restart rule.
func (r *Reader) Package(ctx context.Context, buildID, pkg string, afterSeq int64, limit int) ([]Line, error)

// Build returns all lines of a build ordered by package, seq.
func (r *Reader) Build(ctx context.Context, buildID string, limit int) ([]Line, error)

// Follow merges persisted and live lines for one follower and calls emit in order until
// ctx ends or every package has its final marker.
func Follow(ctx context.Context, src *Sources, buildID string, cur map[string]Cursor, emit func(Batch) error) error
```

### 05.10.5 Retention

#### R-STORE-007 Retention (kept, made exact for §05 data)
- **Statement:** the `retention` controller runs hourly under its lease:
  - log partitions older than `retention.logs` (30 d) are dropped as whole partitions
    (R-STORE-101 sweep);
  - `run-*` and `job-*` partitions older than `retention.analytics` (400 d) are dropped;
  - terminal build records older than `retention.builds` (365 d), together with their
    `bnodes`, are deleted by walking `bpage` pages from the oldest page (known keys); page
    entries become tombstones;
  - fleet `qs` slots below `head − 1000` and `wrs` slots below `cursor − 1000` are deleted by
    known key;
  - cache paths are removed by walking `promo/<day>` logs older than `retention.cache` (90 d)
    that no release or recent build references (§06 owns the reference rule).

  Releases are never collected.
- **Acceptance:** with the clock advanced by 31 d / 366 d / 91 d, each class of data is gone and
  releases remain. Only the R-STORE-101 sweeps issued a LIST.

---

## 05.11 Orchestrator HA without a coordinator

#### R-SRV-004 Reconciliation (kept, controller list fixed)
- **Statement:** every orchestrator runs every controller. A controller works only while it
  holds `ctl/<name>`, and every write it makes is a CAS, so two leaders at once are safe.

| Controller | Interval | Work (all GET/CAS on known keys) |
|---|---|---|
| `membership` | 10 s | own `orch/<id>` heartbeat (P); add self to `fleet/cfg.m`; remove members with heartbeat > 30 s old |
| `registrar` | 2 s | read `wrs/reg/s/<cu+1…>` until 404; approve; token rotation sweep over `widx` every 60 s; `widx` summaries |
| `build` | event + 5 s | plan, enqueue, batch (S14), finalise jobs, update `build`/`bpage`/`bidx`, concurrency rule |
| `job-reconciler` | 10 s | GC `jact/*`; requeue by heartbeat rule; re-slot after 60 s; finalise orphaned terminal jobs |
| `promotion` | event + 10 s | promote `succeeded` jobs' staging (§06 protocol), delete staged keys |
| `release` | event + 30 s | release state machine |
| `lake-maintenance` | 30 s | compaction (R-LOG-100) |
| `retention` | 1 h | R-STORE-007 |
| `forge-sweeper` | 60 s | revoke expired per-job forge tokens (`ftok/*`, D62) |
| `index-repair` | 10 min | finish intent-first entries older than 5 min (`bpage`, `rmem`, `jact`, `pidx`); rehash shards after a gen change |
| `key` | 1 h | create the next orchestrator key 7 d before `notAfter`; publish it in `fleet/cfg.k` |

- **Acceptance:** with 3 orchestrators and `kill -9` of a random orchestrator every 20 s during a
  300-job drain plus 20 build triggers: every build reaches a terminal state, no job runs
  without a lease, every controller lease changes owner within 3 × interval after its holder
  dies, and 0 LIST.
- **Source:** R-SRV-003/004/005, R-NFR-007, ADR 0011.

#### R-SRV-101 Intent-first membership
- **Statement:** an index or membership entry is written before the record it points to, and
  removed after the record is gone. Readers skip entries whose record is missing. `index-repair`
  removes such entries after 5 min. Index entries are hints; records are the truth.
- **Acceptance:** fault injection kills the writer between each pair of writes for build create,
  job enqueue, role binding and project create. After `index-repair`, every index matches its
  records, and no reader showed a phantom record.

#### R-SRV-102 Orchestrator membership and discovery
- **Statement:** an orchestrator publishes `orch/<id>` every 10 s and adds itself to
  `fleet/cfg.m` at start (CAS). Workers learn members only from `fleet/cfg` (GC every 30 s)
  and pin each member's TLS SPKI hash. A member is removed when its heartbeat is older than
  30 s.
- **Acceptance:** a new orchestrator receives Attach streams from all workers within 40 s. A
  killed one is removed from `fleet/cfg` within 40 s. A redirected address with a wrong key is
  refused (S17 security case).

### 05.11.1 Configuration (`bob-server.yaml`, env `BOB_<UPPER_SNAKE>`)

| Key | Default | Meaning |
|---|---|---|
| `storage.endpoint`, `storage.region` | — | S3 endpoint and region |
| `storage.buckets.{control,fleet,analytics,artifacts}` | `bob-control`, `bob-fleet`, `bob-analytics`, `bob-artifacts` | |
| `storage.worker_probe_credential` | unset | R-SEC-102 |
| `fleet.poll_interval` | `30s` | idle poll per class (± 20 %) |
| `fleet.heartbeat` | `15s` | `whb` interval |
| `fleet.lease_ttl` | `60s` | claim grace and heartbeat expiry |
| `fleet.skew` | `5s` | clock skew allowance |
| `fleet.reslot_after` | `60s` | re-slot a queued, unclaimed job |
| `fleet.gone_after` | `5m` | worker `stale` |
| `fleet.max_attempts` | `3` | infra retries per job |
| `fleet.job_shards`, `fleet.worker_shards` | `16`, `4` | `jact`, `widx` shard counts |
| `worker.slots` | `max(1, nproc/2)` | concurrent jobs |
| `worker.min_free` | `15%` | store disk headroom |
| `log.flush_interval` | `30s` | lake commit interval |
| `log.shards` | `4` | log partitions per day |
| `live_log.chunk_interval` | `250ms` | worker chunk cadence while subscribed |
| `retention.logs`, `.analytics`, `.builds`, `.cache` | `30d`, `400d`, `365d`, `90d` | |
| `server.listen`, `server.admin_listen`, `server.trusted_proxies`, `server_name`, `max_parallel_downloads` | `:8443`, `127.0.0.1:9090`, —, —, `256` | HTTP listeners and proxy settings, §06 §6.2/§6.6.3 |

Config changes that workers need (`fleet.*`, `log.*`) are published in `fleet/cfg.pr` by the
orchestrators. The highest `fleet/cfg.g` wins. Workers read only `fleet/cfg`.

---

## 05.12 Budgets [proposal]

Measured values come from loaded-host runs where noted. S9, S10, S11 and S17 have no
quiet-machine logs; their numbers are indicative and must be re-measured in Stage 3 (harness
budgets file).

| R-id | Budget | Basis | Profile |
|---|---|---|---|
| R-NFR-100 | Job pickup with wake-up: enqueue → claim p99 ≤ 1 s | V2: p50 106 ms, p99 182 ms (burst, 20 claimers) | [8c/15G] |
| R-NFR-101 | Job pickup polling only: p99 ≤ 1.2 × poll_interval + 1 s | S9 §3.5 model | [8c/15G] |
| R-NFR-102 | Requests per job ≤ 26 (no LIST) | protocol count §05.5.1 | — |
| R-NFR-103 | Idle worker ≤ 0.15 req/s | §05.5.1 | — |
| R-NFR-104 | Dead worker's job queued again ≤ lease_ttl + 10 s + skew (75 s) | S9 chaos (3 s TTL scaled) | [8c/15G] |
| R-NFR-105 | Live log line → screen p99 ≤ 1 s; degraded p99 ≤ flush_interval + 10 s | S17 fan-out 223 ms; S10 lake 2.7 s at 5 s flush | [8c/15G] |
| R-NFR-106 | Registration → active p99 ≤ 5 s | registrar 2 s poll; S11 crypto < 1 ms | [8c/15G] |
| R-NFR-107 | Revocation effective ≤ 10 s | S11 record cache | — |
| R-NFR-108 | Per-job worker overhead (excluding work) ≤ 1 s; evalbox setup ≤ 25 ms | S14 quiet: drv-closure fetch 0.24 s + upload 0.29 s; S13 quiet: 18.7 ms [8c], 15.0 ms [2c] | [8c/15G], [2c/4G] |
| R-NFR-109 | Total steady S3 load (workers + orchestrators) at 100 workers / 100 running jobs ≤ 30 req/s, 0 LIST | §05.5.1: idle 15 + reconcile 7 + jobs | — |

#### R-NFR-007 Crash safety (kept)
- **Acceptance:** R-FLEET-003 chaos + R-SRV-004 orchestrator kill run + R-SRV-101 fault
  injection together. 0 lost accepted work, 0 corrupt records (every record decodes and
  validates), 0 stale results accepted.

#### R-NFR-005 Scale target (§05 part)
- **Statement:** v1 targets ≤ 5,000 projects, ≤ 1,000 workers, ≤ 2,000 running builds and
  ≤ 1,024 jobs per build. Each limit is enforced, and a request over a limit is refused with an
  error that names the limit.
- **Acceptance:** each limit has a test at limit and limit + 1, at the lowest layer that enforces
  it (store layer with in-process fakes for projects, workers and jobs; the 2,000 running builds
  row stays black-box). The seeding hooks are built by the milestone that implements each limit
  (Gate H, G18).

---

## 05.13 Verifications run for this section

All on RustFS 1.0.3 (single node, `127.0.0.1:22550`), 2026-10-21, [8c/15G], load ≈ 1.5. The
code and outputs are in `/root/scratch-spec05/` (`RESULTS.txt`).

| # | What | Result |
|---|---|---|
| V1 | Raw GET `If-None-Match` (current ETag) → 304; changed → 200 with the new body; missing → 404 `NoSuchKey`; the ETag that `dynamitedb.Get` injects works for a raw conditional GET → 304 | all PASS |
| V1b | SeqLog append by Create-probing: 20 appenders × 50, no tail hint | 1,000 dense, unique slots; 17,594 412s (the reason for the tail hint) |
| V2 | Queue design (tail/head hints, slots, job CAS), 20 claimers, 3 enqueuers, 300 jobs | slot-first order lost 1 job (→ R-FLEET-011 job-first); job-first: 300/300, 0 doubles, 0 LIST; spread pick p50 106 ms, p99 182 ms; cursor-only p50 1.07 s (herd) |
| V3 | SigV4 with URL-prefix endpoint (`<ep>/analytics`, bucket = partition): PUT `If-None-Match`, stale `If-Match` → 412, `If-Match` OK, GET 404, DELETE | all PASS; objects land under `<partition>/` |
| V4 | lake 045e301 through the prefix: 2 partitions × 3 commits × 100 rows, filtered Scan | 300/300 rows each; one catalog per partition; lake made 0 LIST calls |

The herd runs (20 idle claimers at 20 Hz) cost 42–57 requests per job. The targeted wake-up path
avoids this (§05.7). R-FLEET-009 is checked with wake-ups on.

---

## 05.14 Open items

- **[open]** R-STORE-101: the remaining cold-path LIST (lake partition drop, staging sweep).
  Recommendation: keep it until lake has a delete API and S3 lifecycle is verified on RustFS.
- **[open]** lake L1/L3/L4 upstream fixes (R-LOG-100). bob works around them.
- **[open]** `fleet.heartbeat` default 15 s vs 60 s: a trade-off between AWS PUT cost and
  dead-worker detection. Recommendation: 15 s, documented.
- **[open]** Worker container hardening beyond namespaces (a seccomp filter on the Nix process
  against nested userns in builds, S13 risk 3). Recommendation: v1 without it; revisit with
  the Stage 3 security review.
- Re-measure S9/S10/S11/S17 numbers on a quiet machine. None of them has a quiet log.

## Escalations

None. D55 supersedes R-LOG-002's peering and D12. D56 limits ADR 0002's rootless rule to
developer machines (workers run as root). D57 removes STS. All three are owner decisions and
need no conflict resolution.

# §06 — Server API, auth, cache/registry, releases, pipelines, UI


Status: draft for the coordinator merge (Phase 2.3, 2026-10-21).
Scope: `proto/` (complete), RBAC per RPC, identity and tokens, HTTP endpoints (Nix cache, OCI,
release downloads, webhooks, OAuth), promotion, releases, forge adapters, web UI.
ADRs updated: 0014, 0018.

## 6.0 Interfaces

Provided by other sections (names as used here; the coordinator reconciles):

| From | Name | Used for |
|---|---|---|
| §05 | `ProjectRecord`, `RepoLinkRecord` (forge id + forge repo id → project id, §05.4.5) | project CRUD, webhook routing |
| §05 | `BuildRecord` (state machine §05.6.2, trigger, source `src`, pipelines `pi`, reported statuses `rp`, retry-of `rt`) | BuildService, statuses, promotion trigger |
| §05 | `JobRecord` (owner = worker id, epoch, kind, state, staging prefix, promotion `pm`), job state machine §05.6.1 | JobService, `GetCheckoutCredential`, promotion per job |
| §05 | `WorkerRecord` (state §05.6.3, platform, labels, caps), `OrchRecord` (endpoint, pin, heartbeat; public keys in `FleetConfig.k`) | FleetService |
| §05 | `UserRecord`, `RoleRecord`, role bindings (`ro` in `UserRecord`/`ServiceAccountRecord`), `ServiceAccountRecord`, `TokenRecord` (key = SHA-256 of token), `SessionRecord`, `OAuthCodeRecord`, `DeviceCodeRecord`, `OidcProviderRecord`, `OidcMappingRecord`, `AdminIndex` (count of admin bindings, CAS) (§05.4.2, §05.4.5) | auth, RBAC |
| §05 | `ForgeRecord` (credentials, sealed), `ForgeDeliveryRecord` (dedupe, create-only), `CheckoutRecord` (entry of `ftok/<sh2>`: job, epoch, handle, expiry, forge-expires; no token value) | forges |
| §05 | `ReleaseRecord` (state machine §05.6.4), release indexes `relv`/`rpage`/`ridx`; concurrency rule over `bbr` (§05.6.2) | releases, concurrency |
| §05 | `SettingRecord`, `SigningKeyRecord` (control, orchestrator only), `OciHmacKey`, `NotifyWebhookRecord` (§05.4.5) | settings |
| §05 | index documents / sequence slots for every list RPC (D60: GET only, no LIST/`Query`) and the page-token encoding over them | all `List*` RPCs |
| §05 | artifacts bucket layout (`staging/<buildID>/<jobID>/<attempt>/…`, `nix/…`, `oci/…`, `rel/<projectID>/<num10>/…`, `src/…`) and policies; lifecycle rules | cache, registry, uploads |
| §05 | lake tables `log_line`, `package_run`; worker in-memory live buffer; merge rule (ADR 0019) | LogService, AnalyticsService |
| §05 | wake-up semantics (D54), live-log pull (D55) and drain over the worker's outbound `WorkerControlService/Attach` stream, worker token `bwt1` + `Bob-PoP` (ADR 0013), `proto/bob/worker/v1/worker.proto` (§05.9.1) | LogService, FleetService, checkout |
| §01/§02 | pipeline definition (triggers, release outputs) in the evaluated graph IR; trigger matcher `pipeline.Match(event)` in `pkg/` | pipeline selection |
| §02 | CLI error codes (BOB-E-4001 cache credentials, 4008 login invalid, 4009 server unreachable, 4010 denied; BOB-API → BOB-E mapping in §02.3) and the `bob login`/`--remote` client side | auth flows, remote builds |
| §04 | release artifact kinds (closure, OCI index, AppImage, tarball) as build outputs | releases |

Provided by this section to others: all `proto/` packages except `bob/worker/v1` (§05.9.1) (§6.5), the authz option `bob.authz.v1.rule`,
HTTP endpoint map (§6.2), token formats (§6.3), `forge.Adapter` (§6.9), visibility document
format (§6.6), promotion protocol (§6.6.4), UI tokens (§6.10).

## 6.1 Conventions

- Module layout: `proto/buf.yaml` (one module), packages `bob.<area>.v1`, files
  `proto/bob/<area>/v1/<area>.proto`. Generated Go in `internal/gen/bob/<area>/v1`
  (`protoc-gen-go`, `protoc-gen-connect-go`), TypeScript in `web/src/lib/gen`
  (`@bufbuild/protoc-gen-es` v2). `pkg/` never imports generated server packages (R-ARCH-002); the
  `bob` CLI imports only `bob.auth.v1`, `bob.build.v1`, `bob.log.v1`, `bob.release.v1`.
- connectrpc handlers are mounted under `/api/` (e.g. `POST /api/bob.build.v1.BuildService/GetBuild`).
  Protocols: Connect (JSON and binary) and gRPC. Methods marked `idempotency_level = NO_SIDE_EFFECTS`
  also accept Connect GET.
- Ids: build, job, delivery ids are 26-char time-sortable ids (48-bit ms + 80 random bits,
  Crockford base32, lower case). Slugs (project, role, service account, release name, image name,
  integration, OIDC provider, webhook): `^[a-z][a-z0-9-]{1,38}[a-z0-9]$`. Users: `u` + 26-char id;
  local login names `^[a-z][a-z0-9._-]{1,62}$`.
- Lists: `page_size` (default 50, max 500), `page_token` (opaque, from §05's index encoding),
  response `next_page_token` (empty = end). Filters are fields of the request; free-text filter
  `query` matches name/id prefixes only (no scans, R-STORE-004).
- Updates: full resource + `google.protobuf.FieldMask update_mask` + `etag` (optimistic
  concurrency; mismatch → `Aborted`). Every resource message carries `etag`.
- Errors: connect codes as below, each with one `bob.common.v1.ErrorDetail`.

| Code | When | `ErrorDetail.code` |
|---|---|---|
| InvalidArgument | validation failure (field named) | `BOB-API-1001` |
| Unauthenticated | missing/invalid/expired credential | `BOB-API-2001` |
| PermissionDenied | principal has some permission on the project but not this method | `BOB-API-2002` |
| NotFound | missing, **or** principal has no permission at all on the project (no existence leak) | `BOB-API-3001` |
| AlreadyExists | create on an existing id | `BOB-API-3002` |
| FailedPrecondition | state forbids the action (e.g. retry a running build, delete last admin) | `BOB-API-3003` |
| Aborted | etag mismatch, or CAS lost 5 times | `BOB-API-3004` |
| ResourceExhausted | rate limit, stream limit | `BOB-API-4001` |
| Unavailable | storage unavailable / timeouts | `BOB-API-5001` |
| Internal | bug; message `internal error (ref <id>)`, details only in the server log | `BOB-API-9001` |

#### R-API-001 connectrpc + buf
- **Statement:** The API is the protobuf schema in §6.4, served with connectrpc; Go server and TS
  client are generated by `buf generate`.
- **Acceptance:** Given a clean checkout, when `buf lint` and `buf generate` run, then both exit 0
  and the generated trees equal the committed ones (`git diff --exit-code`). A connect-go client
  and a connect-es client each call `bob.auth.v1.AuthService/WhoAmI` against a test server and get
  the same JSON-decoded response.
- **Source:** R-API-001, D5.

#### R-API-002 CRUD resources
- **Statement:** Every resource of R-API-002 has the RPCs listed in §6.4 (Get/List/Create/Update/
  Delete where the resource is user-managed; read-only where it is system-produced).
- **Acceptance:** A table-driven test enumerates resources {projects, builds, jobs, workers,
  releases, images, cache entries, users, roles, permissions, service accounts, OIDC mappings,
  settings} and asserts the methods named in the §6.4 coverage table exist in the descriptors.
- **Source:** R-API-002.

#### R-API-003 lint and breaking checks
- **Statement:** CI runs `buf lint` (STANDARD rules) and `buf breaking --against` the main branch.
- **Acceptance:** A fixture change that renumbers a field fails `buf breaking`; the unchanged tree passes.
- **Source:** R-API-003.

#### R-API-004 UI uses only the public API
- **Statement:** The UI calls only `/api/*` connect endpoints and the documented auth endpoints of
  §6.2; there is no UI-only server route.
- **Acceptance:** Browser e2e: with request interception over all UI pages, every request path
  matches `^/(api/bob\.[a-z]+\.v1\.[A-Za-z]+/[A-Za-z]+|auth/oidc/(start|callback)|oauth/(authorize|consent)|_app/.*|[a-z0-9/_.-]*)$`
  and no non-static request goes outside `/api/`, `/auth/`, `/oauth/`.
- **Source:** R-API-004, R-UI-001.

#### R-API-150 Error model (new)
- **Statement:** Every error has a connect code and one `ErrorDetail` per the table above; NotFound
  is used instead of PermissionDenied when the principal has no permission on the project.
- **Acceptance:** Given user `u1` without any binding on project `p2`, when `u1` calls
  `GetBuild` for an existing build of `p2` and for a non-existent build id, then both answers are
  NotFound with identical messages `build not found`. Given `u1` with only `GetBuild` on `p2`,
  `CancelBuild` returns PermissionDenied `permission denied: bob.build.v1.BuildService/CancelBuild on project p2`.
- **Source:** R-AUTH-002, R-SEC-004.

#### R-API-151 List RPCs are GET-only (new)
- **Statement:** No RPC handler issues an S3 LIST or dynamitedb `Query` on control/fleet; lists
  read §05 index documents by known key.
- **Acceptance:** With an S3 proxy that counts operations, running the full API conformance suite
  issues 0 `ListObjects*` requests against control and fleet buckets.
- **Source:** D60, R-STORE-004.

## 6.2 HTTP endpoint map (orchestrator)

One listener (`server.listen`, default `:8443`, TLS or behind a TLS proxy with
`server.trusted_proxies`). A second listener `server.admin_listen` (default `127.0.0.1:9090`)
serves `/metrics`, `/healthz`, `/readyz` only.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST/GET | `/api/<package>.<Service>/<Method>` | per RPC (§6.5) | connectrpc API |
| GET | `/nix/nix-cache-info` | none; invalid token → 401 | Nix cache info |
| GET/HEAD | `/nix/<hash>.narinfo`, `/nix/nar/<hash>.nar.zst`, `/nix/<hash>.ls` | download token or none + visibility (§6.6) | Nix binary cache |
| GET | `/v2/`, `/v2/<repo>/manifests/<ref>`, `/v2/<repo>/blobs/<digest>`, `/v2/<repo>/tags/list`, `/v2/<repo>/referrers/<digest>` | OCI Bearer (§6.7) | OCI pull |
| GET | `/oci/token` | Basic (service account token or download/API token) or none | OCI token endpoint |
| GET/HEAD | `/r/<project>/<release>/<version>/<file>` | none if public, else Bearer or Basic | release asset download (§6.8) |
| GET | `/r/<project>/<release>/latest/<file>` | same | 302 to the highest version's URL |
| PUT | `/upload/source/sha256/<hex>` | Bearer API token; header `Bob-Project` | source snapshot for `--remote` (§6.9.7) |
| POST | `/hooks/<integration-id>` | webhook signature (§6.9.3) | forge webhooks |
| GET | `/oauth/authorize` | browser session (else redirect to `/login`) | OAuth code + PKCE (§6.3) |
| POST | `/oauth/token`, `/oauth/device/code`, `/oauth/revoke` | client `bob-cli` (public client) | token endpoint, device flow |
| GET | `/auth/oidc/start`, `/auth/oidc/callback` | — | OIDC relying party |
| GET | `/*` | — | embedded UI; unknown paths → `200.html` (SPA fallback); `Cache-Control: no-cache` for HTML, `immutable` for `/_app/immutable/*` |

All download endpoints stream from S3 through the orchestrator (D61); there are no presigned URLs
and no redirects to the storage backend.

## 6.3 Identity, tokens and login

### 6.3.1 Principals and credentials

| Principal | Credential | Format | TTL | Stored |
|---|---|---|---|---|
| user (local or OIDC) | API access token | `bob_at_` + base32(32 random bytes) | 1 h | `TokenRecord` key SHA-256(token) |
| user | refresh token (CLI) | `bob_rt_` + base32(32 B) | session: idle 7 d, max 30 d (settings) | `SessionRecord` |
| user | browser session cookie `bob_session` | `bob_st_` + base32(32 B) | idle 12 h, max 7 d | `SessionRecord` |
| user / service account | download token (netrc, OCI Basic, release Basic) | `bob_dt_` + base32(32 B) | = its session, or SA token expiry | `TokenRecord` with `scope=download` |
| service account | SA token | `bob_sa_` + base32(32 B) | default 90 d, max 1 y, or none if admin sets `no_expiry` | `TokenRecord` |
| OCI client | OCI bearer | `bob-oci-v1.<b64url(json)>.<b64url(HMAC-SHA256)>` (ADR 0014) | 15 min | stateless; HMAC key `OciHmacKey` |
| worker | worker token | `bwt1.` (ADR 0013) + `Bob-PoP` header | 1 h | stateless; worker record check |

- Token scopes: `api` (connect API + download endpoints) and `download` (only GET/HEAD on `/nix/*`,
  `/oci/token`, `/r/*`). A `download` token on `/api/` → Unauthenticated `token not valid for the API`.
  SA tokens carry a scope chosen at creation (default `download`).
- Lookup: `TokenRecord` GET by SHA-256(token); orchestrators cache positive results ≤ 10 s and
  negative results ≤ 2 s. Revocation (delete record) is effective ≤ 10 s.
- Passwords: argon2id (`golang.org/x/crypto/argon2`), m = 64 MiB, t = 3, p = 4, 16-byte salt,
  32-byte key, PHC string; at most 2 concurrent hash computations per orchestrator (queue, 5 s
  timeout → Unavailable). Length 12–256 bytes, no other composition rules.
- Login throttle: per login name 5 failures / 5 min, per client IP 30 / 5 min (per orchestrator,
  in memory) → ResourceExhausted `too many login attempts, try again in <n>s`. Failure message is
  always `invalid login or password`.
- Secrets never logged: `Secret` types redact in `String`, `GoString`, `MarshalJSON`, `slog.LogValuer`.

### 6.3.2 Flows

**UI login.** `/login` page offers local password (`AuthService/LoginPassword`) and each OIDC
provider (`/auth/oidc/start?provider=<id>&next=<path>`). Success sets `bob_session`
(`HttpOnly; Secure; SameSite=Strict; Path=/`). Cookie-authenticated `/api/` requests must send
`Bob-Csrf: 1` (forces a CORS preflight; the server sends no CORS headers), else Unauthenticated.
Connect GET with only a cookie is refused.

**CLI login (`bob login <url>`), authorization code + PKCE (RFC 7636, RFC 8252 loopback).**
```
CLI: listen 127.0.0.1:<random>; open browser
  GET /oauth/authorize?response_type=code&client_id=bob-cli&redirect_uri=http://127.0.0.1:<port>/callback
      &code_challenge=<S256>&code_challenge_method=S256&state=<rand>
server: no browser session → 302 /login?next=<authorize URL>; then consent page /oauth/consent
        ("Allow the bob CLI on <hostname from User-Agent> to act as <user>?") → 302 redirect_uri?code&state
CLI: POST /oauth/token grant_type=authorization_code&code&code_verifier&redirect_uri&client_id=bob-cli
server → {"access_token":"bob_at_…","token_type":"Bearer","expires_in":3600,
          "refresh_token":"bob_rt_…","bob_download_token":"bob_dt_…","bob_user":"u…"}
```
Only `redirect_uri` with host `127.0.0.1` or `[::1]`, path `/callback`, any port, is accepted.
Codes are single-use (`OAuthCodeRecord` deleted with If-Match), 60 s TTL.

**Device flow (RFC 8628, `bob login --device`).** `POST /oauth/device/code client_id=bob-cli` →
`{device_code, user_code: "ABCD-EFGH" (8 chars from BCDFGHJKLMNPQRSTVWXZ), verification_uri:
"<base>/device", expires_in: 600, interval: 5}`; the user enters the code on `/device` (needs a
browser session); polling answers `authorization_pending`, `slow_down`, `expired_token`,
`access_denied` per RFC 8628.

**Refresh.** `grant_type=refresh_token` returns a new access token; the refresh token is not
rotated (no CAS race between orchestrators); the session's idle timer is extended.

**OIDC (server is a relying party).** `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2`
(Apache-2.0/BSD-3). Code flow with PKCE, `state` and `nonce` stored in a 10 min record keyed by
SHA-256(state). After ID-token verification (issuer, audience, expiry, nonce): user = `UserRecord`
keyed by SHA-256(issuer ‖ 0 ‖ sub); email/name stored in control only (R-SEC-003). Mappings are
evaluated at each login: a mapping `{provider, claim, value, role, project}` matches when the claim
equals `value` (string) or contains it (array of strings). The user's `source=oidc` bindings are
replaced by the matched set; manual bindings are kept. If the provider has
`require_mapping = true` (default) and nothing matches, login fails with
`your account has no access to this bob server (no role mapping matched)`.

**Bootstrap.** `bob-server admin create-user --login <name> --admin` (reads the password from
stdin, uses the orchestrator's control credentials) creates a local user bound to `admin`. It is
the only way to create the first admin.

#### R-AUTH-001 OIDC and local users
- **Statement:** Users log in with a local password or through any configured OIDC provider; the
  server then issues its own tokens.
- **Acceptance:** Harness with a test OIDC provider (`bobtest-idp`, in-process): (1) local user
  `alice` with correct password → session cookie, `WhoAmI` returns `alice`; wrong password →
  Unauthenticated `invalid login or password`. (2) OIDC user with claim `groups:["dev"]` and a
  mapping `groups=dev → role dev on p1` → login works, `WhoAmI.bindings` contains `dev@p1`.
  (3) ID token with a wrong nonce → `/auth/oidc/callback` answers 400 and no session exists.
- **Source:** R-AUTH-001, D14, D19.

#### R-AUTH-004 OIDC claim → role mappings
- **Statement:** Mappings are re-evaluated at every OIDC login and replace only OIDC-sourced bindings.
- **Acceptance:** Given user with OIDC binding `dev@p1` and manual binding `viewer@*`, when the IdP
  drops `dev` from `groups` and the user logs in again, then bindings are exactly `viewer@*`.
  With `require_mapping=true` and no match, login fails with the message above.
- **Source:** R-AUTH-004, D14.

#### R-AUTH-005 Service accounts
- **Statement:** Service accounts have role bindings like users and any number of tokens with a
  scope (`api` or `download`) and expiry.
- **Acceptance:** Create SA `ci`, token scope `download` → token works for `/oci/token` Basic and
  `/nix/*` netrc, and gets Unauthenticated on `/api/`. Revoke the token → within 10 s all three
  endpoints answer 401/Unauthenticated. `CreateServiceAccountToken` returns the secret once;
  `ListServiceAccountTokens` returns only id, name, scope, expiry, last-used day.
- **Source:** R-AUTH-005, D19.

#### R-AUTH-007 Server-issued tokens
- **Statement:** The server issues and validates the token kinds of §6.3.1; tokens are stored only
  as SHA-256.
- **Acceptance:** After a CLI login, the control bucket contains no object whose bytes include the
  plaintext access, refresh or download token (grep of all objects). An expired access token gives
  Unauthenticated `token expired`; a refresh then succeeds.
- **Source:** R-AUTH-007, R-SEC-005.

#### R-AUTH-150 OAuth flows for the CLI (new)
- **Statement:** The server implements authorization code + PKCE with loopback redirect and the
  device flow for client `bob-cli`, as specified in §6.3.2.
- **Acceptance:** Scripted client: PKCE flow with a wrong `code_verifier` → `invalid_grant`; reused
  code → `invalid_grant`; `redirect_uri=http://evil.example/callback` → 400 without redirect;
  device flow before approval → `authorization_pending`, polling faster than 5 s → `slow_down`,
  after approval → tokens.
- **Source:** R-CLI-003, R-AUTH-007.

#### R-AUTH-151 Credential hygiene (new)
- **Statement:** argon2id parameters, throttles and CSRF rule as in §6.3.1/§6.3.2.
- **Acceptance:** (1) stored password hashes start with `$argon2id$v=19$m=65536,t=3,p=4$`.
  (2) 6 wrong passwords for one login within 5 min → the 6th gets ResourceExhausted.
  (3) cookie-only POST to `/api/bob.project.v1.ProjectService/CreateProject` without `Bob-Csrf`
  → Unauthenticated. (4) server log after the full auth suite contains none of the test passwords
  or tokens.
- **Source:** R-SEC-005, R-SEC-004.

## 6.4 RBAC

### 6.4.1 Model

```
Permission  = { method: "<package>.<Service>/<Method>" | "<package>.<Service>/*" | "*",
                project: "<project-id>" | "*" }
Role        = { id, description, permissions: [Permission] }        // built-in: admin = [{*, *}]
RoleBinding = { principal: "user:<id>" | "sa:<id>", role, project: "<project-id>" | "*", source: manual | oidc }
```

Effective permission of a binding for each role permission `rp`:
`project = rp.project` if `binding.project == "*"`; `binding.project` if `rp.project == "*"`;
`rp.project` if both are equal; otherwise none. A call is allowed iff some effective permission
matches the method (exact, service wildcard, or `*`) and the call's project (exact or `*`).

Each RPC declares its authorization rule with the method option `bob.authz.v1.rule` (§6.4.2 proto):

| Kind | Check |
|---|---|
| `PROJECT` | permission on the project named by `project_field` in the request (a top-level string field, or a dotted path to a nested one such as `project.id`; for ids like `build_id`, the handler resolves the build's project first; unknown id → NotFound) |
| `GLOBAL` | permission with project `*` |
| `LIST_FILTERED` | authenticated; results contain only projects where the principal has the method; an explicit `project_id` filter without permission → NotFound |
| `AUTHENTICATED` | any valid API principal (own data only) |
| `PUBLIC` | no credential needed; handler applies resource visibility, then `PROJECT` for private resources |
| `WORKER` | `bwt1` token + valid `Bob-PoP` (ADR 0013); not part of RBAC; only on `bob.worker.v1.WorkerControlService` (§05.9) |

Built-in `admin`: cannot be updated or deleted (FailedPrecondition `role admin is built in`).
`AdminIndex` counts `admin@*` bindings of enabled principals; any write that would make it 0
(remove binding, disable/delete user, delete SA) fails with FailedPrecondition
`at least one admin must remain`. The check and the binding write are CAS on `AdminIndex`.

HTTP downloads reuse RPC permissions: Nix cache paths → `bob.cache.v1.CacheService/GetPath`, OCI
pulls → `bob.registry.v1.RegistryService/GetRepository`, release assets →
`bob.release.v1.ReleaseService/GetRelease`, source upload → `bob.build.v1.BuildService/CreateBuild`,
each on the owning project.

#### R-AUTH-002 RPC × project permissions
- **Statement:** Authorization is evaluated exactly per §6.4.1.
- **Acceptance:** Table test over (role permission, binding project, call project, method) with
  ≥ 30 rows, including: role `{BuildService/*, *}` bound `@p1` allows `CancelBuild` on p1, denies
  on p2; role `{BuildService/GetBuild, p1}` bound `@p2` grants nothing; `{*, *}` bound `@*` allows all.
- **Source:** R-AUTH-002, D14′.

#### R-AUTH-003 Only built-in role is admin
- **Statement:** A fresh server has exactly one role, `admin`; it is immutable and at least one admin remains.
- **Acceptance:** Fresh server: `ListRoles` returns `[admin]`. `UpdateRole(admin)` and
  `DeleteRole(admin)` → FailedPrecondition. With one admin, `RemoveRoleBinding` of that binding,
  `UpdateUser{disabled:true}` and `DeleteUser` → FailedPrecondition `at least one admin must remain`;
  two orchestrators removing the two last admin bindings concurrently leave exactly one.
- **Source:** R-AUTH-003, D39.

#### R-AUTH-006 Every RPC has a rule
- **Statement:** Every method in every service carries `bob.authz.v1.rule` with a kind other than
  `UNSPECIFIED`, and `PROJECT` rules name an existing string field; the interceptor refuses
  methods without a rule.
- **Acceptance:** Go test over `protoregistry.GlobalFiles` fails on a missing rule (proved by a
  fixture service without options). For each `PROJECT`/`GLOBAL` method, a generated test calls it
  with a principal that has no bindings and asserts NotFound or PermissionDenied (never OK).
- **Source:** R-AUTH-006.

#### R-AUTH-152 No privilege escalation through bindings (new)
- **Statement:** `CreateRoleBinding` succeeds only if every effective permission of the new
  binding is also an effective permission of the caller.
- **Acceptance:** Caller with role `{RoleService/*, *}` bound `@p1` and `{BuildService/GetBuild, *}`
  bound `@p1`: binding `viewer(GetBuild)@p1` to another user → OK; binding `admin@p1` →
  PermissionDenied `binding would grant permissions you do not have`; any binding `@*` → PermissionDenied.
- **Source:** R-AUTH-002, R-SEC-004.

## 6.5 `proto/` (complete draft)

### 6.5.1 Module files

```yaml
# proto/buf.yaml
version: v2
modules:
  - path: .
lint:
  use: [STANDARD]
breaking:
  use: [FILE]
```

```yaml
# proto/buf.gen.yaml
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: github.com/megakuul/bob/internal/gen
plugins:
  - remote: buf.build/protocolbuffers/go
    out: ../internal/gen
    opt: paths=source_relative
  - remote: buf.build/connectrpc/go
    out: ../internal/gen
    opt: paths=source_relative
  - remote: buf.build/bufbuild/es
    out: ../web/src/lib/gen
    opt: target=ts
```

Plugins are pinned in the repo's `bob.go` toolchain (local plugins from nixpkgs in CI; remote
names above only identify them). `buf breaking` runs against `main`.

Shorthand used below: every `rpc` carries `option (bob.authz.v1.rule) = {...}`. Read-only RPCs
also set `option idempotency_level = NO_SIDE_EFFECTS;` (written as `// GET` to save space; the
real file contains the option). All files start with:

```proto
syntax = "proto3";
import "google/protobuf/timestamp.proto";
import "google/protobuf/field_mask.proto";
import "bob/authz/v1/authz.proto";
import "bob/common/v1/common.proto";
```
(imports a file does not use are left out in the real file; `buf lint` checks this).

Verified 2026-10-21 (re-run at integration): the 15 files below plus §05.9.1's
`bob/worker/v1/worker.proto`, expanded mechanically (syntax line, needed imports, `// GET`
markers kept as comments; script `/root/scratch-specint/extract.py`), pass `buf build` and
`buf lint` (STANDARD) with buf 1.73.0; 98 RPCs, 98 `rule` options.

### 6.5.2 `bob/authz/v1/authz.proto`

```proto
syntax = "proto3";
package bob.authz.v1;
import "google/protobuf/descriptor.proto";

enum Kind {
  KIND_UNSPECIFIED = 0;
  KIND_PROJECT = 1;        // permission on the request's project (see project_field, resolve)
  KIND_GLOBAL = 2;         // permission with project "*"
  KIND_LIST_FILTERED = 3;  // authenticated; results filtered to permitted projects
  KIND_AUTHENTICATED = 4;  // any valid API principal
  KIND_PUBLIC = 5;         // no credential; handler applies visibility, then PROJECT for private data
  KIND_WORKER = 6;         // bwt1 worker token + Bob-PoP (bob.worker.v1 only)
  reserved 7;              // was KIND_ORCHESTRATOR: workers serve no RPCs (outbound Attach, §05.9)
  reserved "KIND_ORCHESTRATOR";
}

enum Resolve {
  RESOLVE_UNSPECIFIED = 0; // project_field holds a project id
  RESOLVE_BUILD = 1;       // project_field holds a build id
  RESOLVE_JOB = 2;         // job id
  RESOLVE_RELEASE = 3;     // release id
  RESOLVE_REPOSITORY = 4;  // OCI repository name "<project>/<image>"
}

message Rule {
  Kind kind = 1;
  string project_field = 2;        // KIND_PROJECT only; string field of the request, or a dotted path to a nested string field ("project.id")
  Resolve resolve = 3;
  bool allow_download_scope = 4;   // KIND_AUTHENTICATED: also accept bob_dt_ tokens
}

extend google.protobuf.MethodOptions {
  Rule rule = 50001;
}
```

### 6.5.3 `bob/common/v1/common.proto`

```proto
package bob.common.v1;

message ErrorDetail {
  string code = 1;   // BOB-API-NNNN (§6.1)
  string field = 2;  // request field path for InvalidArgument
  string hint = 3;
}

enum Visibility {
  VISIBILITY_UNSPECIFIED = 0;
  VISIBILITY_PRIVATE = 1;
  VISIBILITY_PUBLIC = 2;
}

message Principal {
  string id = 1;            // "user:u01j…" | "sa:ci"
  string display_name = 2;
}

message RoleBindingRef {
  string role = 1;
  string project = 2;       // project id or "*"
}

// A bob label and where it is defined (R-FW-016).
message PackageRef {
  string label = 1;         // //svc/api:server
  string platform = 2;      // linux/amd64 | linux/arm64
  string variant = 3;       // "" = default
  string defined_at = 4;    // svc/api/bob.go:17:9
}
```

### 6.5.4 `bob/auth/v1/auth.proto`

```proto
package bob.auth.v1;

service AuthService {
  rpc GetServerInfo(GetServerInfoRequest) returns (GetServerInfoResponse) {          // GET
    option (bob.authz.v1.rule) = {kind: KIND_PUBLIC};
  }
  rpc WhoAmI(WhoAmIRequest) returns (WhoAmIResponse) {                               // GET
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED, allow_download_scope: true};
  }
  // Sets the bob_session cookie. Local users only.
  rpc LoginPassword(LoginPasswordRequest) returns (LoginPasswordResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_PUBLIC};
  }
  // Ends the calling session (cookie or refresh token); clears the cookie.
  rpc Logout(LogoutRequest) returns (LogoutResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED};
  }
  rpc ChangePassword(ChangePasswordRequest) returns (ChangePasswordResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED};
  }
  rpc ListMySessions(ListMySessionsRequest) returns (ListMySessionsResponse) {       // GET
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED};
  }
  rpc RevokeMySession(RevokeMySessionRequest) returns (RevokeMySessionResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED};
  }
}

message NixInfo {
  string substituter = 1;            // https://bob.example/nix
  repeated string public_keys = 2;   // all ACTIVE and VERIFY_ONLY keys, "name:base64"
}
message LoginOption {
  string provider_id = 1;            // "" = local password
  string display_name = 2;
  string start_url = 3;              // /auth/oidc/start?provider=<id>
}
message GetServerInfoRequest {}
message GetServerInfoResponse {
  string server_name = 1;
  string base_url = 2;
  string version = 3;
  NixInfo nix = 4;
  repeated LoginOption login_options = 5;
  string oci_registry = 6;           // host[:port] for docker/podman
}
message WhoAmIRequest {}
message WhoAmIResponse {
  bob.common.v1.Principal principal = 1;
  string login = 2;
  repeated bob.common.v1.RoleBindingRef bindings = 3;
  repeated string projects = 4;      // projects with at least one permission ("*" if any {*, *})
  string token_scope = 5;            // api | download | session
  google.protobuf.Timestamp token_expires_at = 6;
  NixInfo nix = 7;
  bool must_change_password = 8;
}
message LoginPasswordRequest { string login = 1; string password = 2; }
message LoginPasswordResponse { bob.common.v1.Principal principal = 1; bool must_change_password = 2; }
message LogoutRequest {}
message LogoutResponse {}
message ChangePasswordRequest { string old_password = 1; string new_password = 2; }
message ChangePasswordResponse {}   // all other sessions of the user are revoked
message Session {
  string id = 1;
  string kind = 2;                   // cli | browser
  string user_agent = 3;
  google.protobuf.Timestamp created_at = 4;
  google.protobuf.Timestamp last_used_at = 5;   // day precision
  bool current = 6;
}
message ListMySessionsRequest {}
message ListMySessionsResponse { repeated Session sessions = 1; }
message RevokeMySessionRequest { string session_id = 1; }
message RevokeMySessionResponse {}
```

### 6.5.5 `bob/access/v1/access.proto`

```proto
package bob.access.v1;

service UserService {
  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetUser(GetUserRequest) returns (GetUserResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }            // GET
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }      // GET
  rpc UpdateUser(UpdateUserRequest) returns (UpdateUserResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteUser(DeleteUserRequest) returns (DeleteUserResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc SetUserPassword(SetUserPasswordRequest) returns (SetUserPasswordResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

service RoleService {
  rpc CreateRole(CreateRoleRequest) returns (CreateRoleResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetRole(GetRoleRequest) returns (GetRoleResponse) { option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED}; }     // GET
  rpc ListRoles(ListRolesRequest) returns (ListRolesResponse) { option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED}; } // GET
  rpc UpdateRole(UpdateRoleRequest) returns (UpdateRoleResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteRole(DeleteRoleRequest) returns (DeleteRoleResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }    // refused while bound
  // Every RPC with its rule, generated from the descriptors (for the role editor and matrix).
  rpc ListPermissionCatalog(ListPermissionCatalogRequest) returns (ListPermissionCatalogResponse) { option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED}; } // GET
  // project "*" in the binding needs a {CreateRoleBinding, *} permission (KIND_PROJECT with value "*").
  rpc CreateRoleBinding(CreateRoleBindingRequest) returns (CreateRoleBindingResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project"}; }
  rpc DeleteRoleBinding(DeleteRoleBindingRequest) returns (DeleteRoleBindingResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project"}; }
  rpc ListRoleBindings(ListRoleBindingsRequest) returns (ListRoleBindingsResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; } // GET
}

service ServiceAccountService {
  rpc CreateServiceAccount(CreateServiceAccountRequest) returns (CreateServiceAccountResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetServiceAccount(GetServiceAccountRequest) returns (GetServiceAccountResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }     // GET
  rpc ListServiceAccounts(ListServiceAccountsRequest) returns (ListServiceAccountsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc UpdateServiceAccount(UpdateServiceAccountRequest) returns (UpdateServiceAccountResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteServiceAccount(DeleteServiceAccountRequest) returns (DeleteServiceAccountResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc CreateServiceAccountToken(CreateServiceAccountTokenRequest) returns (CreateServiceAccountTokenResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc ListServiceAccountTokens(ListServiceAccountTokensRequest) returns (ListServiceAccountTokensResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc RevokeServiceAccountToken(RevokeServiceAccountTokenRequest) returns (RevokeServiceAccountTokenResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

service OidcService {
  rpc CreateOidcProvider(CreateOidcProviderRequest) returns (CreateOidcProviderResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetOidcProvider(GetOidcProviderRequest) returns (GetOidcProviderResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }      // GET
  rpc ListOidcProviders(ListOidcProvidersRequest) returns (ListOidcProvidersResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc UpdateOidcProvider(UpdateOidcProviderRequest) returns (UpdateOidcProviderResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteOidcProvider(DeleteOidcProviderRequest) returns (DeleteOidcProviderResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc CreateOidcMapping(CreateOidcMappingRequest) returns (CreateOidcMappingResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc ListOidcMappings(ListOidcMappingsRequest) returns (ListOidcMappingsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }    // GET
  rpc DeleteOidcMapping(DeleteOidcMappingRequest) returns (DeleteOidcMappingResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

enum UserSource { USER_SOURCE_UNSPECIFIED = 0; USER_SOURCE_LOCAL = 1; USER_SOURCE_OIDC = 2; }
message User {
  string id = 1;                       // u + 26-char id (output only)
  string login = 2;                    // local: chosen; OIDC: username_claim value (display only)
  string display_name = 3;
  string email = 4;
  UserSource source = 5;               // output only
  string oidc_provider_id = 6;         // output only
  bool disabled = 7;
  bool must_change_password = 8;
  google.protobuf.Timestamp created_at = 9;
  google.protobuf.Timestamp last_login_at = 10;
  string etag = 11;
}
message CreateUserRequest { User user = 1; string initial_password = 2; }  // creates a LOCAL user
message CreateUserResponse { User user = 1; }
message GetUserRequest { string id = 1; }
message GetUserResponse { User user = 1; }
message ListUsersRequest { int32 page_size = 1; string page_token = 2; string query = 3; UserSource source = 4; bool include_disabled = 5; }
message ListUsersResponse { repeated User users = 1; string next_page_token = 2; }
message UpdateUserRequest { User user = 1; google.protobuf.FieldMask update_mask = 2; } // display_name, email (local), disabled, must_change_password
message UpdateUserResponse { User user = 1; }
message DeleteUserRequest { string id = 1; string etag = 2; }  // revokes sessions and tokens, deletes bindings
message DeleteUserResponse {}
message SetUserPasswordRequest { string id = 1; string new_password = 2; bool must_change = 3; }
message SetUserPasswordResponse {}

message Permission {
  string method = 1;    // "bob.build.v1.BuildService/CancelBuild" | "bob.build.v1.BuildService/*" | "*"
  string project = 2;   // project id | "*"
}
message Role {
  string id = 1;
  string description = 2;
  repeated Permission permissions = 3;   // ≤ 500
  bool built_in = 4;                     // output only; true only for admin
  string etag = 5;
}
message CreateRoleRequest { Role role = 1; }
message CreateRoleResponse { Role role = 1; }
message GetRoleRequest { string id = 1; }
message GetRoleResponse { Role role = 1; }
message ListRolesRequest { int32 page_size = 1; string page_token = 2; string query = 3; }
message ListRolesResponse { repeated Role roles = 1; string next_page_token = 2; }
message UpdateRoleRequest { Role role = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateRoleResponse { Role role = 1; }
message DeleteRoleRequest { string id = 1; string etag = 2; }
message DeleteRoleResponse {}
message CatalogEntry {
  string method = 1;          // full method name
  string kind = 2;            // PROJECT | GLOBAL | LIST_FILTERED | AUTHENTICATED | PUBLIC
  string description = 3;     // first line of the rpc comment
}
message ListPermissionCatalogRequest {}
message ListPermissionCatalogResponse { repeated CatalogEntry entries = 1; }  // excludes WORKER / ORCHESTRATOR
enum BindingSource { BINDING_SOURCE_UNSPECIFIED = 0; BINDING_SOURCE_MANUAL = 1; BINDING_SOURCE_OIDC = 2; }
message RoleBinding {
  string id = 1;              // output only
  bob.common.v1.Principal principal = 2;
  string role = 3;
  string project = 4;         // project id | "*"
  BindingSource source = 5;   // output only
  google.protobuf.Timestamp created_at = 6;
}
message CreateRoleBindingRequest { string principal_id = 1; string role = 2; string project = 3; }
message CreateRoleBindingResponse { RoleBinding binding = 1; }
message DeleteRoleBindingRequest { string id = 1; string project = 2; }    // project must equal the binding's
message DeleteRoleBindingResponse {}
message ListRoleBindingsRequest { int32 page_size = 1; string page_token = 2; string principal_id = 3; string role = 4; string project = 5; }
message ListRoleBindingsResponse { repeated RoleBinding bindings = 1; string next_page_token = 2; }

message ServiceAccount {
  string id = 1;              // slug
  string description = 2;
  bool disabled = 3;
  google.protobuf.Timestamp created_at = 4;
  string etag = 5;
}
enum TokenScope { TOKEN_SCOPE_UNSPECIFIED = 0; TOKEN_SCOPE_API = 1; TOKEN_SCOPE_DOWNLOAD = 2; }
message ServiceAccountToken {
  string id = 1;
  string name = 2;
  TokenScope scope = 3;
  google.protobuf.Timestamp expires_at = 4;    // unset = no expiry (needs no_expiry)
  google.protobuf.Timestamp last_used_on = 5;  // day precision
  google.protobuf.Timestamp created_at = 6;
}
message CreateServiceAccountRequest { ServiceAccount service_account = 1; }
message CreateServiceAccountResponse { ServiceAccount service_account = 1; }
message GetServiceAccountRequest { string id = 1; }
message GetServiceAccountResponse { ServiceAccount service_account = 1; }
message ListServiceAccountsRequest { int32 page_size = 1; string page_token = 2; string query = 3; }
message ListServiceAccountsResponse { repeated ServiceAccount service_accounts = 1; string next_page_token = 2; }
message UpdateServiceAccountRequest { ServiceAccount service_account = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateServiceAccountResponse { ServiceAccount service_account = 1; }
message DeleteServiceAccountRequest { string id = 1; string etag = 2; }
message DeleteServiceAccountResponse {}
message CreateServiceAccountTokenRequest { string service_account_id = 1; string name = 2; TokenScope scope = 3; int32 ttl_days = 4; bool no_expiry = 5; }
message CreateServiceAccountTokenResponse { ServiceAccountToken token = 1; string secret = 2; }   // secret returned once
message ListServiceAccountTokensRequest { string service_account_id = 1; }
message ListServiceAccountTokensResponse { repeated ServiceAccountToken tokens = 1; }             // ≤ 20 tokens per SA
message RevokeServiceAccountTokenRequest { string service_account_id = 1; string token_id = 2; }
message RevokeServiceAccountTokenResponse {}

message OidcProvider {
  string id = 1;
  string display_name = 2;
  string issuer_url = 3;            // https only
  string client_id = 4;
  string client_secret = 5;         // input only; never returned
  bool has_client_secret = 6;       // output only
  repeated string scopes = 7;       // default ["openid","profile","email"]
  string username_claim = 8;        // default "preferred_username"
  bool require_mapping = 9;         // default true
  string redirect_url = 10;         // output only: <base>/auth/oidc/callback
  string etag = 11;
}
message OidcMapping {
  string id = 1;
  string provider_id = 2;
  string claim = 3;                 // e.g. "groups"
  string value = 4;
  string role = 5;
  string project = 6;               // project id | "*"
}
message CreateOidcProviderRequest { OidcProvider provider = 1; }
message CreateOidcProviderResponse { OidcProvider provider = 1; }
message GetOidcProviderRequest { string id = 1; }
message GetOidcProviderResponse { OidcProvider provider = 1; }
message ListOidcProvidersRequest {}
message ListOidcProvidersResponse { repeated OidcProvider providers = 1; }   // ≤ 10
message UpdateOidcProviderRequest { OidcProvider provider = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateOidcProviderResponse { OidcProvider provider = 1; }
message DeleteOidcProviderRequest { string id = 1; string etag = 2; }
message DeleteOidcProviderResponse {}
message CreateOidcMappingRequest { OidcMapping mapping = 1; }
message CreateOidcMappingResponse { OidcMapping mapping = 1; }
message ListOidcMappingsRequest { string provider_id = 1; }
message ListOidcMappingsResponse { repeated OidcMapping mappings = 1; }      // ≤ 500 per provider
message DeleteOidcMappingRequest { string id = 1; }
message DeleteOidcMappingResponse {}
```

### 6.5.6 `bob/project/v1/project.proto`

```proto
package bob.project.v1;

service ProjectService {
  rpc CreateProject(CreateProjectRequest) returns (CreateProjectResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetProject(GetProjectRequest) returns (GetProjectResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id"}; }  // GET
  rpc ListProjects(ListProjectsRequest) returns (ListProjectsResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; }          // GET
  rpc UpdateProject(UpdateProjectRequest) returns (UpdateProjectResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project.id"}; }
  // Refused while non-terminal builds exist. Releases must be deleted first (R-STORE-007).
  rpc DeleteProject(DeleteProjectRequest) returns (DeleteProjectResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Pipelines from the newest evaluation of the default branch.
  rpc ListPipelines(ListPipelinesRequest) returns (ListPipelinesResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project_id"}; } // GET
}

enum LinkHealth { LINK_HEALTH_UNSPECIFIED = 0; LINK_HEALTH_OK = 1; LINK_HEALTH_REPO_NOT_FOUND = 2; LINK_HEALTH_AUTH_FAILED = 3; LINK_HEALTH_WEBHOOK_MISSING = 4; }
message Project {
  string id = 1;                          // slug; immutable; used in OCI repos and release URLs
  string display_name = 2;
  string description = 3;
  string forge_integration_id = 4;
  string repo_full_name = 5;              // "acme/api"
  int64 repo_id = 6;                      // output only (adapter LookupRepo)
  string default_branch = 7;              // output only
  string repo_web_url = 8;                // output only
  LinkHealth link_health = 9;             // output only
  google.protobuf.Timestamp created_at = 10;
  string etag = 11;
}
message CreateProjectRequest { Project project = 1; }     // links the repo, ensures the webhook
message CreateProjectResponse { Project project = 1; }
message GetProjectRequest { string id = 1; }
message GetProjectResponse { Project project = 1; }
message ListProjectsRequest { int32 page_size = 1; string page_token = 2; string query = 3; }
message ListProjectsResponse { repeated Project projects = 1; string next_page_token = 2; }
message UpdateProjectRequest { Project project = 1; google.protobuf.FieldMask update_mask = 2; } // display_name, description, forge_integration_id, repo_full_name
message UpdateProjectResponse { Project project = 1; }
message DeleteProjectRequest { string id = 1; string etag = 2; }
message DeleteProjectResponse {}
enum Concurrency { CONCURRENCY_UNSPECIFIED = 0; CONCURRENCY_SUPERSEDE = 1; CONCURRENCY_QUEUE = 2; CONCURRENCY_PARALLEL = 3; }
message Pipeline {
  string name = 1;
  repeated string triggers = 2;           // human form: "push main", "pull_request", "tag v*"
  Concurrency concurrency = 3;
  bool releases = 4;                      // produces releases
  bob.common.v1.PackageRef defined_at = 5;
  string last_build_id = 6;
  string last_state = 7;                  // BuildState name
}
message ListPipelinesRequest { string project_id = 1; }
message ListPipelinesResponse { repeated Pipeline pipelines = 1; string evaluated_sha = 2; }
```

### 6.5.7 `bob/build/v1/build.proto`

Enum values mirror §05's `BuildRecord`/`JobRecord` state machines; §05 is authoritative and the
coordinator aligns names.

```proto
package bob.build.v1;

service BuildService {
  rpc CreateBuild(CreateBuildRequest) returns (CreateBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project_id"}; }
  rpc GetBuild(GetBuildRequest) returns (GetBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }   // GET
  rpc ListBuilds(ListBuildsRequest) returns (ListBuildsResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; }                                    // GET
  rpc CancelBuild(CancelBuildRequest) returns (CancelBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }
  // New build (trigger RETRY, retry_of set, attempt+1) of the failed/cancelled pipelines; succeeded packages are cache hits.
  rpc RetryBuild(RetryBuildRequest) returns (RetryBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }
  // New build with the same trigger and source (trigger kind RERUN, rerun_of set).
  rpc RerunBuild(RerunBuildRequest) returns (RerunBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }
  rpc GetBuildGraph(GetBuildGraphRequest) returns (GetBuildGraphResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; } // GET
  // Server stream: current build first, then changes; ends when the build is terminal.
  rpc WatchBuild(WatchBuildRequest) returns (stream WatchBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }
}

service JobService {
  rpc GetJob(GetJobRequest) returns (GetJobResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_JOB}; }            // GET
  rpc ListJobs(ListJobsRequest) returns (ListJobsResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; } // GET
}

// States and phases are §05.6.2's (BuildRecord `st`, `ph`).
enum BuildState {
  BUILD_STATE_UNSPECIFIED = 0;
  BUILD_STATE_PENDING = 1;      // created, waiting for planning or for an older build (queue rule)
  BUILD_STATE_QUEUED = 2;       // first job enqueued, not yet claimed
  BUILD_STATE_RUNNING = 3;
  BUILD_STATE_SUCCEEDED = 4;
  BUILD_STATE_FAILED = 5;
  BUILD_STATE_CANCELLED = 6;
  BUILD_STATE_SUPERSEDED = 7;   // cancelled by a newer build (concurrency rule)
  BUILD_STATE_SKIPPED = 8;      // no pipeline matched the event
}
enum BuildPhase {
  BUILD_PHASE_UNSPECIFIED = 0;
  BUILD_PHASE_EVALUATING = 1;
  BUILD_PHASE_BUILDING = 2;
  BUILD_PHASE_PROMOTING = 3;
}
enum TriggerKind {
  TRIGGER_KIND_UNSPECIFIED = 0;
  TRIGGER_KIND_PUSH = 1;
  TRIGGER_KIND_TAG = 2;
  TRIGGER_KIND_PULL_REQUEST = 3;
  TRIGGER_KIND_API = 4;         // API or UI
  TRIGGER_KIND_REMOTE = 5;      // bob build/test --remote
  TRIGGER_KIND_RERUN = 6;
  TRIGGER_KIND_RETRY = 7;       // RetryBuild (§05: a retry is a new build with retry-of set)
}
message Trigger {
  TriggerKind kind = 1;
  string ref = 2;               // refs/heads/main, refs/tags/v1.4.0, refs/pull/7/head
  int64 pull_request = 3;
  bool from_fork = 4;
  string sender = 5;            // forge login (display only)
  string delivery_id = 6;
  bob.common.v1.Principal principal = 7;   // API/REMOTE/RERUN
  string rerun_of = 8;
  string superseded_by = 9;
  string retry_of = 10;
}
message GitSource {
  string ref = 1;
  string sha = 2;               // empty on create = resolved by the worker at checkout, then set
  string fetch_ref = 3;         // PRs: refs/pull/N/head | refs/merge-requests/N/head
}
message Source {
  oneof kind {
    GitSource git = 1;
    string upload_sha256 = 2;   // PUT /upload/source/sha256/<hex> first
  }
}
message PackageCounts {
  int32 total = 1; int32 cached = 2; int32 built = 3; int32 running = 4;
  int32 queued = 5; int32 failed = 6; int32 skipped = 7; int32 cancelled = 8;
}
message ErrorSummary {
  string code = 1;              // BOB-E-NNNN (§02 taxonomy)
  string message = 2;
  bob.common.v1.PackageRef package = 3;
  string hint = 4;
}
message PipelineRun {
  string name = 1;
  BuildState state = 2;
  string release_id = 3;        // set when the pipeline published a release
}
message Build {
  string id = 1;
  string project_id = 2;
  int64 number = 3;             // per-project sequence, shown as #N
  BuildState state = 4;
  Trigger trigger = 5;
  Source source = 6;
  repeated PipelineRun pipelines = 7;
  repeated string targets = 8;  // labels / patterns for API and REMOTE builds
  repeated string platforms = 9;
  repeated string variants = 10;
  int32 attempt = 11;           // 1 + number of retries in the retry-of chain
  PackageCounts counts = 12;
  repeated ErrorSummary errors = 13;   // ≤ 20, first failures
  google.protobuf.Timestamp created_at = 14;
  google.protobuf.Timestamp started_at = 15;
  google.protobuf.Timestamp finished_at = 16;
  string web_url = 17;          // <base>/p/<project>/builds/<id>
  string etag = 18;
  BuildPhase phase = 19;        // while RUNNING
}
message CreateBuildRequest {
  string project_id = 1;
  Source source = 2;
  string pipeline = 3;          // run one pipeline (ignores its triggers), or
  repeated string targets = 4;  // build/test these labels (REMOTE when sent by the CLI)
  repeated string platforms = 5;   // default: pipeline/package defaults
  repeated string variants = 6;
  bool test = 7;                // targets are tests (bob test --remote)
  string reason = 8;            // ≤ 200 bytes, shown in the UI
}
message CreateBuildResponse { Build build = 1; }
message GetBuildRequest { string id = 1; }
message GetBuildResponse { Build build = 1; }
message ListBuildsRequest {
  int32 page_size = 1; string page_token = 2;
  string project_id = 3; repeated BuildState states = 4; string ref = 5;
  string pipeline = 6; TriggerKind trigger_kind = 7;
  google.protobuf.Timestamp created_after = 8;
}
message ListBuildsResponse { repeated Build builds = 1; string next_page_token = 2; }   // newest first
message CancelBuildRequest { string id = 1; }
message CancelBuildResponse { Build build = 1; }
message RetryBuildRequest { string id = 1; }
message RetryBuildResponse { Build build = 1; }
message RerunBuildRequest { string id = 1; }
message RerunBuildResponse { Build build = 1; }

enum PackageState {
  PACKAGE_STATE_UNSPECIFIED = 0;
  PACKAGE_STATE_PENDING = 1;    // waiting for dependencies
  PACKAGE_STATE_QUEUED = 2;
  PACKAGE_STATE_BUILDING = 3;
  PACKAGE_STATE_CACHED = 4;     // substituted (bob cache or cache.nixos.org)
  PACKAGE_STATE_SUCCEEDED = 5;
  PACKAGE_STATE_FAILED = 6;
  PACKAGE_STATE_SKIPPED = 7;    // a dependency failed
  PACKAGE_STATE_CANCELLED = 8;
}
message PackageNode {
  bob.common.v1.PackageRef package = 1;
  string drv = 2;               // /nix/store/…drv (shown only in detail/-v views)
  PackageState state = 3;
  string job_id = 4;
  repeated int32 deps = 5;      // indexes into GetBuildGraphResponse.nodes
  google.protobuf.Timestamp started_at = 6;
  google.protobuf.Timestamp finished_at = 7;
  ErrorSummary error = 8;
  repeated string outputs = 9;  // store paths (after success)
}
message GetBuildGraphRequest { string build_id = 1; }
message GetBuildGraphResponse { repeated PackageNode nodes = 1; bool complete = 2; }  // complete=false before evaluation finished
message WatchBuildRequest { string id = 1; }
message WatchBuildResponse {
  Build build = 1;                       // always set
  repeated int32 changed_nodes = 2;      // indexes; client re-fetches GetBuildGraph when the length changes
  repeated PackageNode nodes = 3;        // the changed nodes
}

// Kinds and states are §05's (JobSpec.Kind, JobRecord `st`); promotion progress is Job.promotion.
enum JobKind { JOB_KIND_UNSPECIFIED = 0; JOB_KIND_EVALBUILD = 1; JOB_KIND_BATCH = 2; JOB_KIND_EVAL = 3; }
enum JobState {
  JOB_STATE_UNSPECIFIED = 0;
  JOB_STATE_QUEUED = 1;
  JOB_STATE_CLAIMED = 2;
  JOB_STATE_RUNNING = 3;
  JOB_STATE_SUCCEEDED = 4;
  JOB_STATE_FAILED = 5;
  JOB_STATE_CANCELLED = 6;
}
message Job {
  string id = 1;
  string build_id = 2;
  JobKind kind = 3;
  JobState state = 4;
  string platform = 5;
  string worker_id = 6;
  int32 attempt = 7;
  int64 epoch = 8;
  int32 drv_count = 9;
  string promotion = 10;        // "", "blocked: staging incomplete", "failed: artifact verification", "done"
  repeated string non_reproducible = 11;   // store paths (R-NFR-006)
  google.protobuf.Timestamp created_at = 12;
  google.protobuf.Timestamp claimed_at = 13;
  google.protobuf.Timestamp finished_at = 14;
  ErrorSummary error = 15;
}
message GetJobRequest { string id = 1; }
message GetJobResponse { Job job = 1; }
message ListJobsRequest { string build_id = 1; int32 page_size = 2; string page_token = 3; }
message ListJobsResponse { repeated Job jobs = 1; string next_page_token = 2; }
```

### 6.5.8 `bob/log/v1/log.proto`

Replaces the push-based `LiveLogService` and `PeerLogService` of ADR 0019 (D55): orchestrators
pull from workers over the worker-opened `Attach` stream (`LiveSubscribe`/`LiveChunk`, §05.9.1,
§05.10.3); there is no orchestrator↔orchestrator service. `LogLine`, `Cursor`, `Restart` and
`Final` carry the fields of §05.9.1's `LiveLine`/`LiveCursor`/`LiveRestart`/`LiveFinal`.

```proto
package bob.log.v1;

service LogService {
  // Persisted log of a build or one package, ordered by (package, seq).
  rpc GetLog(GetLogRequest) returns (GetLogResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; }   // GET
  // Server stream: persisted + live lines after the cursor, gap-free and duplicate-free (ADR 0019 merge).
  // While at least one follower exists, the orchestrator pulls the running workers' in-memory logs.
  rpc FollowLog(FollowLogRequest) returns (stream FollowLogResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; }
  rpc SearchLog(SearchLogRequest) returns (SearchLogResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; } // GET
}

enum Stream { STREAM_UNSPECIFIED = 0; STREAM_STDOUT = 1; STREAM_STDERR = 2; STREAM_BOB = 3; }
message LogLine {
  string package = 1;          // label
  int64 seq = 2;               // per (build, package), from 1, continues across attempts
  int32 attempt = 3;
  Stream stream = 4;
  int64 ts_unix_nano = 5;
  string text = 6;             // UTF-8, ≤ 4 KiB; rendered inert by every client
}
message Cursor { int64 seq = 1; int32 attempt = 2; }
message Restart { string package = 1; int32 attempt = 2; int64 from_seq = 3; }
message Final { string package = 1; int64 last_seq = 2; int32 attempt = 3; }

message GetLogRequest { string build_id = 1; string package = 2; int64 after_seq = 3; int32 limit = 4; }  // limit default 10,000, max 100,000
message GetLogResponse { repeated LogLine lines = 1; bool truncated = 2; }
message FollowLogRequest { string build_id = 1; string package = 2; map<string, Cursor> cursor = 3; }
message FollowLogResponse {
  repeated LogLine lines = 1;
  repeated Restart restarts = 2;   // client drops its lines of that package with seq >= from_seq
  repeated Final end = 3;
  bool done = 4;
  bool delayed = 5;                // true while no live source is reachable (lake only)
}
message SearchLogRequest { string build_id = 1; string term = 2; string package = 3; }  // term 3..200 bytes, case-sensitive
message SearchLogResponse { repeated LogLine hits = 1; bool truncated = 2; }            // ≤ 1,000 hits
```

### 6.5.9 `bob/fleet/v1/fleet.proto`

```proto
package bob.fleet.v1;

service FleetService {
  rpc ListWorkers(ListWorkersRequest) returns (ListWorkersResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }      // GET
  rpc GetWorker(GetWorkerRequest) returns (GetWorkerResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }            // GET
  // Worker finishes current jobs and claims no new ones.
  rpc DrainWorker(DrainWorkerRequest) returns (DrainWorkerResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc UndrainWorker(UndrainWorkerRequest) returns (UndrainWorkerResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // ADR 0013 revocation: state=revoked, gen+1; effective ≤ 10 s; the process must restart with new keys.
  rpc RevokeWorker(RevokeWorkerRequest) returns (RevokeWorkerResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc ListOrchestrators(ListOrchestratorsRequest) returns (ListOrchestratorsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
}

enum WorkerState {
  WORKER_STATE_UNSPECIFIED = 0;
  WORKER_STATE_ACTIVE = 1;
  WORKER_STATE_DRAINING = 2;
  WORKER_STATE_STALE = 3;      // heartbeat older than fleet.gone_after (5 min, §05.6.3)
  WORKER_STATE_REVOKED = 4;
}
message Worker {
  string id = 1;
  WorkerState state = 2;
  string platform = 3;
  map<string, string> labels = 4;
  repeated string capabilities = 5;     // kvm, big-parallel, …
  string version = 6;
  int32 cpus = 7;
  int64 memory_bytes = 8;
  repeated string job_ids = 9;
  google.protobuf.Timestamp registered_at = 10;
  google.protobuf.Timestamp last_heartbeat = 11;
}
message Orchestrator {
  string id = 1;
  string endpoint = 2;
  string version = 3;
  google.protobuf.Timestamp started_at = 4;
  google.protobuf.Timestamp last_heartbeat = 5;
  repeated string leader_of = 6;        // controller leader leases held (ADR 0011)
}
message ListWorkersRequest { int32 page_size = 1; string page_token = 2; string platform = 3; repeated WorkerState states = 4; string query = 5; }
message ListWorkersResponse { repeated Worker workers = 1; string next_page_token = 2; }
message GetWorkerRequest { string id = 1; }
message GetWorkerResponse { Worker worker = 1; }
message DrainWorkerRequest { string id = 1; }
message DrainWorkerResponse { Worker worker = 1; }
message UndrainWorkerRequest { string id = 1; }
message UndrainWorkerResponse { Worker worker = 1; }
message RevokeWorkerRequest { string id = 1; string reason = 2; }
message RevokeWorkerResponse { Worker worker = 1; }
message ListOrchestratorsRequest {}
message ListOrchestratorsResponse { repeated Orchestrator orchestrators = 1; }
```

### 6.5.10 `bob/cache/v1/cache.proto`

```proto
package bob.cache.v1;

service CacheService {
  rpc GetCacheStats(GetCacheStatsRequest) returns (GetCacheStatsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }   // GET
  // Metadata of one served store path. Visible paths only (§6.6.2); otherwise NotFound.
  // Also the permission name that gates /nix/* downloads of private paths.
  rpc GetPath(GetPathRequest) returns (GetPathResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }                       // GET
  rpc ListTopPackages(ListTopPackagesRequest) returns (ListTopPackagesResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; } // GET
}

message GetCacheStatsRequest {}
message GetCacheStatsResponse {
  int64 nar_bytes = 1;            // compressed bytes under nix/nar/ (from the daily GC sweep)
  int64 paths = 2;
  double hit_rate_24h = 3;        // narinfo 200 / (200 + 404), all orchestrators
  double hit_rate_7d = 4;
  int32 retention_unreferenced_days = 5;
  google.protobuf.Timestamp measured_at = 6;
}
message GetPathRequest { string store_hash = 1; }   // 32-char Nix base32
message GetPathResponse {
  string store_path = 1;
  int64 nar_size = 2;
  int64 file_size = 3;
  repeated string references = 4;
  repeated string signatures = 5;
  repeated string projects = 6;   // only projects the caller may see
  bool public = 7;
  string build_id = 8;            // first publication
  google.protobuf.Timestamp promoted_at = 9;
}
message TopPackage { string project_id = 1; string label = 2; int64 hits_7d = 3; int64 nar_bytes = 4; }
message ListTopPackagesRequest { string project_id = 1; int32 limit = 2; }    // limit ≤ 100
message ListTopPackagesResponse { repeated TopPackage packages = 1; }
```

### 6.5.11 `bob/release/v1/release.proto`

```proto
package bob.release.v1;

service ReleaseService {
  rpc ListReleases(ListReleasesRequest) returns (ListReleasesResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }   // GET; public + permitted
  rpc GetRelease(GetReleaseRequest) returns (GetReleaseResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }         // GET
  // bob install: "<project>/<name>@<constraint>" + platform → release and closure roots.
  rpc ResolveRelease(ResolveReleaseRequest) returns (ResolveReleaseResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; } // GET
  rpc UpdateRelease(UpdateReleaseRequest) returns (UpdateReleaseResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "release.id", resolve: RESOLVE_RELEASE}; }
  // The only way a release is removed (R-STORE-007).
  rpc DeleteRelease(DeleteReleaseRequest) returns (DeleteReleaseResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_RELEASE}; }
}

enum ReleaseState { RELEASE_STATE_UNSPECIFIED = 0; RELEASE_STATE_PENDING = 1; RELEASE_STATE_PUBLISHING = 2; RELEASE_STATE_PUBLISHED = 3; RELEASE_STATE_FAILED = 4; RELEASE_STATE_DELETED = 5; }  // §05.6.4
enum AssetKind { ASSET_KIND_UNSPECIFIED = 0; ASSET_KIND_APPIMAGE = 1; ASSET_KIND_TARBALL = 2; ASSET_KIND_FILE = 3; }
message PlatformClosure {
  string platform = 1;
  repeated string roots = 2;      // store paths installed by bob install
  int64 closure_nar_bytes = 3;    // bob-served part only
}
message Asset {
  string file = 1;                // ^[A-Za-z0-9._+-]{1,128}$
  AssetKind kind = 2;
  string platform = 3;
  int64 size = 4;
  string sha256 = 5;              // hex
  string url = 6;                 // <base>/r/<project>/<name>/<version>/<file>
}
message ImageRef { string repository = 1; string tag = 2; string digest = 3; }
message Release {
  string id = 1;
  string project_id = 2;
  string name = 3;                // release name from bob.go (slug)
  string version = 4;             // semver 2.0 without leading v
  bob.common.v1.Visibility visibility = 5;
  ReleaseState state = 6;
  string build_id = 7;
  string sha = 8;
  repeated PlatformClosure closures = 9;
  repeated Asset assets = 10;
  repeated ImageRef images = 11;
  string description = 12;        // ≤ 10 KiB, plain text
  google.protobuf.Timestamp created_at = 13;
  string etag = 14;
}
message ListReleasesRequest { int32 page_size = 1; string page_token = 2; string project_id = 3; string name = 4; }
message ListReleasesResponse { repeated Release releases = 1; string next_page_token = 2; }   // newest version first
message GetReleaseRequest { string id = 1; }
message GetReleaseResponse { Release release = 1; }
message ResolveReleaseRequest {
  string project_id = 1;
  string name = 2;
  string constraint = 3;          // "" = highest; "1.4" = highest 1.4.x; "1.4.2" exact (pre-releases only exact)
  string platform = 4;
}
message ResolveReleaseResponse { Release release = 1; PlatformClosure closure = 2; repeated string substituters = 3; repeated string public_keys = 4; }
message UpdateReleaseRequest { Release release = 1; google.protobuf.FieldMask update_mask = 2; }   // visibility, description only
message UpdateReleaseResponse { Release release = 1; }
message DeleteReleaseRequest { string id = 1; string etag = 2; }
message DeleteReleaseResponse {}
```

### 6.5.12 `bob/registry/v1/registry.proto`

```proto
package bob.registry.v1;

service RegistryService {
  rpc ListRepositories(ListRepositoriesRequest) returns (ListRepositoriesResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; } // GET; public + permitted
  // Also the permission name that gates /v2/ pulls of private manifests and blobs.
  rpc GetRepository(GetRepositoryRequest) returns (GetRepositoryResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }          // GET
  rpc ListTags(ListTagsRequest) returns (ListTagsResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }                         // GET
}

message Repository {
  string name = 1;                // "<project>/<image>"
  string project_id = 2;
  bool has_public_tags = 3;
  int32 tag_count = 4;
  google.protobuf.Timestamp updated_at = 5;
  string pull_ref = 6;            // <registry host>/<name>
}
message Tag {
  string tag = 1;
  string digest = 2;
  string media_type = 3;          // OCI index or manifest
  repeated string platforms = 4;
  int64 size = 5;                 // sum of layer sizes, largest platform
  string release_id = 6;
  bool public = 7;
  google.protobuf.Timestamp created_at = 8;
}
message ListRepositoriesRequest { int32 page_size = 1; string page_token = 2; string project_id = 3; string query = 4; }
message ListRepositoriesResponse { repeated Repository repositories = 1; string next_page_token = 2; }
message GetRepositoryRequest { string name = 1; }
message GetRepositoryResponse { Repository repository = 1; }
message ListTagsRequest { string repository = 1; int32 page_size = 2; string page_token = 3; }
message ListTagsResponse { repeated Tag tags = 1; string next_page_token = 2; }   // visible tags only
```

### 6.5.13 `bob/settings/v1/settings.proto`

```proto
package bob.settings.v1;

service SettingsService {
  rpc GetSettings(GetSettingsRequest) returns (GetSettingsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }         // GET
  rpc UpdateSettings(UpdateSettingsRequest) returns (UpdateSettingsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc ListSigningKeys(ListSigningKeysRequest) returns (ListSigningKeysResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; } // GET; public keys only
  // New ACTIVE key; the previous ACTIVE key becomes VERIFY_ONLY.
  rpc CreateSigningKey(CreateSigningKeyRequest) returns (CreateSigningKeyResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Re-signs every narinfo signed only by this key, then marks it RETIRED (removed from public_keys).
  rpc RetireSigningKey(RetireSigningKeyRequest) returns (RetireSigningKeyResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

message Retention {
  int32 logs_days = 1;                 // default 30
  int32 analytics_days = 2;            // default 400
  int32 builds_days = 3;               // build/job records, default 365 (= §05 `retention.builds`)
  int32 cache_unreferenced_days = 4;   // paths not reachable from a release or a build younger than this, default 90 (= §05 `retention.cache`)
  int32 staging_days = 5;              // lifecycle rule, default 7
}
message SessionPolicy {
  int32 cli_idle_days = 1;             // default 7
  int32 cli_max_days = 2;              // default 30
  int32 browser_idle_hours = 3;        // default 12
  int32 browser_max_days = 4;          // default 7
  int32 sa_token_max_days = 5;         // default 365
}
message Settings {
  string server_name = 1;              // default "bob"; Nix key name prefix
  string base_url = 2;                 // https://bob.example (status links, OCI realm)
  Retention retention = 3;
  SessionPolicy sessions = 4;
  bool live_log_enabled = 5;           // default true (D55 pull)
  int32 max_parallel_downloads = 6;    // per orchestrator, default 256
  string etag = 7;
}
message GetSettingsRequest {}
message GetSettingsResponse { Settings settings = 1; }
message UpdateSettingsRequest { Settings settings = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateSettingsResponse { Settings settings = 1; }
enum KeyState { KEY_STATE_UNSPECIFIED = 0; KEY_STATE_ACTIVE = 1; KEY_STATE_VERIFY_ONLY = 2; KEY_STATE_RETIRING = 3; KEY_STATE_RETIRED = 4; }
message SigningKey {
  string name = 1;                     // <server_name>-<n>
  string public_key = 2;               // "name:base64(32 bytes)"
  KeyState state = 3;
  google.protobuf.Timestamp created_at = 4;
}
message ListSigningKeysRequest {}
message ListSigningKeysResponse { repeated SigningKey keys = 1; }
message CreateSigningKeyRequest {}
message CreateSigningKeyResponse { SigningKey key = 1; }
message RetireSigningKeyRequest { string name = 1; }
message RetireSigningKeyResponse { SigningKey key = 1; }   // RETIRING until the re-sign sweep finishes
```

### 6.5.14 `bob/forge/v1/forge.proto`

```proto
package bob.forge.v1;

service ForgeService {
  // Returns the webhook secret once.
  rpc CreateForgeIntegration(CreateForgeIntegrationRequest) returns (CreateForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetForgeIntegration(GetForgeIntegrationRequest) returns (GetForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }       // GET
  rpc ListForgeIntegrations(ListForgeIntegrationsRequest) returns (ListForgeIntegrationsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc UpdateForgeIntegration(UpdateForgeIntegrationRequest) returns (UpdateForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Refused while projects link to it.
  rpc DeleteForgeIntegration(DeleteForgeIntegrationRequest) returns (DeleteForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc RotateWebhookSecret(RotateWebhookSecretRequest) returns (RotateWebhookSecretResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Checks credentials (and optionally one repository) against the forge.
  rpc TestForgeIntegration(TestForgeIntegrationRequest) returns (TestForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Last 100 deliveries (ring document).
  rpc ListForgeDeliveries(ListForgeDeliveriesRequest) returns (ListForgeDeliveriesResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }       // GET
}

enum ForgeKind { FORGE_KIND_UNSPECIFIED = 0; FORGE_KIND_GITHUB = 1; FORGE_KIND_FORGEJO = 2; FORGE_KIND_GITLAB = 3; }
enum Health { HEALTH_UNSPECIFIED = 0; HEALTH_OK = 1; HEALTH_AUTH_FAILED = 2; HEALTH_UNREACHABLE = 3; }
message GitHubCredentials { int64 app_id = 1; int64 installation_id = 2; string private_key_pem = 3; /* input only */ }
message ForgejoCredentials { string bot_login = 1; string bot_password = 2; /* input only */ }
message GitLabCredentials { string access_token = 1; /* input only; project/group token, Maintainer, api */ bool allow_plaintext_secret = 2; /* GitLab < 19.0 */ }
message ForgeIntegration {
  string id = 1;
  ForgeKind kind = 2;
  string display_name = 3;
  string base_url = 4;              // https only (github: https://api.github.com)
  oneof credentials {
    GitHubCredentials github = 5;
    ForgejoCredentials forgejo = 6;
    GitLabCredentials gitlab = 7;
  }
  bool has_credentials = 8;         // output only
  string webhook_url = 9;           // output only: <base>/hooks/<id>
  Health health = 10;               // output only
  string health_message = 11;       // output only
  google.protobuf.Timestamp created_at = 12;
  string etag = 13;
}
message CreateForgeIntegrationRequest { ForgeIntegration integration = 1; }
message CreateForgeIntegrationResponse { ForgeIntegration integration = 1; string webhook_secret = 2; }
message GetForgeIntegrationRequest { string id = 1; }
message GetForgeIntegrationResponse { ForgeIntegration integration = 1; }
message ListForgeIntegrationsRequest {}
message ListForgeIntegrationsResponse { repeated ForgeIntegration integrations = 1; }   // ≤ 50
message UpdateForgeIntegrationRequest { ForgeIntegration integration = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateForgeIntegrationResponse { ForgeIntegration integration = 1; }
message DeleteForgeIntegrationRequest { string id = 1; string etag = 2; }
message DeleteForgeIntegrationResponse {}
message RotateWebhookSecretRequest { string id = 1; }
message RotateWebhookSecretResponse { string webhook_secret = 1; }   // old secret accepted for 24 h; per-repo hooks re-ensured
message TestForgeIntegrationRequest { string id = 1; string repo_full_name = 2; }
message TestForgeIntegrationResponse { Health health = 1; string message = 2; int64 repo_id = 3; }
message Delivery {
  string delivery_id = 1;
  string event = 2;                 // forge event name
  string result = 3;                // accepted | duplicate | ignored: <reason> | rejected: signature | rejected: payload
  string build_id = 4;
  google.protobuf.Timestamp received_at = 5;
}
message ListForgeDeliveriesRequest { string integration_id = 1; }
message ListForgeDeliveriesResponse { repeated Delivery deliveries = 1; }
```

### 6.5.15 `bob/notify/v1/notify.proto`

```proto
package bob.notify.v1;

service NotifyService {
  rpc CreateNotifyWebhook(CreateNotifyWebhookRequest) returns (CreateNotifyWebhookResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetNotifyWebhook(GetNotifyWebhookRequest) returns (GetNotifyWebhookResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }       // GET
  rpc ListNotifyWebhooks(ListNotifyWebhooksRequest) returns (ListNotifyWebhooksResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc UpdateNotifyWebhook(UpdateNotifyWebhookRequest) returns (UpdateNotifyWebhookResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteNotifyWebhook(DeleteNotifyWebhookRequest) returns (DeleteNotifyWebhookResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

message NotifyWebhook {
  string id = 1;
  string url = 2;                       // https only
  repeated string events = 3;           // build.finished | release.published
  repeated string projects = 4;         // empty = all
  bool enabled = 5;
  string last_result = 6;               // output only: "200 at <time>" | "error: <reason>"
  string etag = 7;
}
message CreateNotifyWebhookRequest { NotifyWebhook webhook = 1; }
message CreateNotifyWebhookResponse { NotifyWebhook webhook = 1; string secret = 2; }   // once
message GetNotifyWebhookRequest { string id = 1; }
message GetNotifyWebhookResponse { NotifyWebhook webhook = 1; }
message ListNotifyWebhooksRequest {}
message ListNotifyWebhooksResponse { repeated NotifyWebhook webhooks = 1; }             // ≤ 50
message UpdateNotifyWebhookRequest { NotifyWebhook webhook = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateNotifyWebhookResponse { NotifyWebhook webhook = 1; }
message DeleteNotifyWebhookRequest { string id = 1; }
message DeleteNotifyWebhookResponse {}
```

### 6.5.16 `bob/analytics/v1/analytics.proto`

```proto
package bob.analytics.v1;

service AnalyticsService {
  // Overview page; every list filtered to permitted projects; fleet numbers are aggregate counts.
  rpc GetOverview(GetOverviewRequest) returns (GetOverviewResponse) { option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED}; }      // GET
  rpc PackageDurations(PackageDurationsRequest) returns (PackageDurationsResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project_id"}; } // GET
  rpc RecentFailures(RecentFailuresRequest) returns (RecentFailuresResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; } // GET
  rpc BuildHistory(BuildHistoryRequest) returns (BuildHistoryResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project_id"}; } // GET
}

message PlatformFleet { string platform = 1; int32 active = 2; int32 busy = 3; int32 stale = 4; int32 queued_jobs = 5; }
message GetOverviewRequest {}
message GetOverviewResponse {
  repeated bob.build.v1.Build running = 1;        // ≤ 20
  repeated PlatformFleet fleet = 2;
  double cache_hit_rate_24h = 3;
  repeated bob.build.v1.Build recent_failures = 4; // ≤ 10
  int32 queue_depth = 5;
}
message PackageDuration { string label = 1; string day = 2; /* YYYY-MM-DD */ double avg_ms = 3; int64 count = 4; }
message PackageDurationsRequest { string project_id = 1; string label = 2; int32 days = 3; }   // days ≤ 400
message PackageDurationsResponse { repeated PackageDuration rows = 1; }
message RecentFailuresRequest { string project_id = 1; int32 limit = 2; }                   // ≤ 100
message RecentFailuresResponse { repeated bob.build.v1.Build builds = 1; }
message HistoryDay { string day = 1; int32 succeeded = 2; int32 failed = 3; int32 cancelled = 4; double p50_duration_s = 5; double p90_duration_s = 6; }
message BuildHistoryRequest { string project_id = 1; int32 days = 2; string pipeline = 3; }
message BuildHistoryResponse { repeated HistoryDay days = 1; }
```

### 6.5.17 `bob/worker/v1/worker.proto` (worker → orchestrator)

Specified completely in §05.9.1 (owner §05): `WorkerControlService` with `Attach` (the
long-lived stream each worker opens **outbound** to every orchestrator; it carries Wake (D54),
live-log subscriptions (D55), CancelJob and Drain) and `GetCheckoutCredential` (§6.9.6). Both
carry `rule = {kind: KIND_WORKER}`. Workers expose no port and serve no RPC; there is no
`bob.workeragent.v1` package (integration decision, closes E-06-1). Worker-side live buffer caps:
last 20,000 lines or 8 MiB per job, whichever is smaller; a `LiveChunk` is ≤ 4 MiB.

### 6.5.19 RPC coverage and rule summary

The `rule` option in the files above is the source of truth (R-AUTH-006). Summary:

| Kind | RPCs |
|---|---|
| PUBLIC | AuthService/GetServerInfo, LoginPassword; CacheService/GetPath; ReleaseService/ListReleases, GetRelease, ResolveRelease; RegistryService/*; SettingsService/ListSigningKeys |
| AUTHENTICATED | AuthService/WhoAmI (also download scope), Logout, ChangePassword, ListMySessions, RevokeMySession; RoleService/GetRole, ListRoles, ListPermissionCatalog; AnalyticsService/GetOverview |
| LIST_FILTERED | ProjectService/ListProjects; BuildService/ListBuilds; RoleService/ListRoleBindings; CacheService/ListTopPackages; AnalyticsService/RecentFailures |
| PROJECT | ProjectService/GetProject, UpdateProject, ListPipelines; BuildService/CreateBuild, GetBuild, CancelBuild, RetryBuild, RerunBuild, GetBuildGraph, WatchBuild; JobService/*; LogService/*; ReleaseService/UpdateRelease, DeleteRelease; RoleService/CreateRoleBinding, DeleteRoleBinding; AnalyticsService/PackageDurations, BuildHistory |
| GLOBAL | UserService/*; RoleService/CreateRole, UpdateRole, DeleteRole; ServiceAccountService/*; OidcService/*; ProjectService/CreateProject, DeleteProject; FleetService/*; CacheService/GetCacheStats; SettingsService/GetSettings, UpdateSettings, CreateSigningKey, RetireSigningKey; ForgeService/*; NotifyService/* |
| WORKER | WorkerControlService/Attach, GetCheckoutCredential (§05.9.1) |

PUBLIC methods on private data (private release, private repository, private path) apply the
PROJECT check with the permission named by the method itself, and answer NotFound when it fails.

Resource coverage (R-API-002): projects → ProjectService; builds → BuildService; jobs →
JobService; workers → FleetService; releases → ReleaseService; images → RegistryService; cache
entries → CacheService; users → UserService; roles and permissions → RoleService; service
accounts → ServiceAccountService; OIDC mappings → OidcService; settings → SettingsService.

## 6.6 Nix binary cache, visibility, download proxy, promotion

### 6.6.1 Endpoints

| Path | Answer |
|---|---|
| `GET /nix/nix-cache-info` | `StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n`; with an invalid/expired token → 401 |
| `GET/HEAD /nix/<hash>.narinfo` | 200 signed narinfo / 404 not visible or absent / 401 invalid token / 503 storage |
| `GET/HEAD /nix/nar/<hash>.nar.zst` | 200, 206 (one `Range`), 404, 416; same visibility as the narinfo |
| `GET /nix/<hash>.ls` | JSON listing v1; same visibility |

- `<hash>` must match `^[0-9a-df-np-sv-z]{32}$`, else 404 without S3 access.
- 401 carries `WWW-Authenticate: Basic realm="bob"` and body
  `unauthorized: token invalid or expired (run 'bob login')`.
- Credentials: netrc Basic with user `bob` and password = download token (or API/SA token). No
  `Authorization` header = anonymous.
- Headers: public narinfo `Cache-Control: public, max-age=300`; private narinfo
  `Cache-Control: private, max-age=300`; NARs `Cache-Control: public|private, max-age=31536000, immutable`.
- Narinfo field order: `StorePath, URL, Compression, FileHash, FileSize, NarHash, NarSize,
  References, Deriver, Sig, CA`; `URL: nar/<hash>.nar.zst`, `Compression: zstd`, hashes
  `sha256:<nix32>`, references as base names sorted. `Deriver` is omitted (it reveals source
  layout and Nix does not need it).

### 6.6.2 Visibility documents (D58)

One document per served object, artifacts bucket, written only by orchestrators:

| Key | Value | Writer | Reader | Access |
|---|---|---|---|---|
| `nix/vis/<storehash>.json` | `{"v":1,"projects":["<project>",…],"public":["<release-id>",…]}` | promotion (adds project), release publish/visibility change (adds/removes release id) | every `/nix/*` request | GET; create `If-None-Match: *`, update `If-Match` (≤ 5 CAS retries) |
| `oci/vis/sha256/<hex>.json` | same schema, for manifests, indexes, configs and layers | same | `/v2/*` | same |

Rule: an object is visible to a caller iff `public` is non-empty, or the caller has
`bob.cache.v1.CacheService/GetPath` (Nix) or `bob.registry.v1.RegistryService/GetRepository`
(OCI) on at least one project in `projects`. Orchestrators cache documents in an LRU (≤ 200,000
entries, 30 s TTL); so revoking public visibility takes effect ≤ 30 s (documented, accepted).
Absent document = not visible. `projects` is never shrunk except by GC; `public` lists ≤ 10,000
release ids (beyond that the release publish fails with `too many releases share <path>` **[open]**).

#### R-REG-001 Nix binary cache served by orchestrators
- **Statement:** Orchestrators serve §6.6.1 so stock Nix substitutes natively, with per-project
  privacy and anonymous access to public paths.
- **Acceptance:** Harness (RustFS, 2 orchestrators): (1) embedded Nix with bob's config and a
  download token for p1 substitutes a p1 closure; `nix store verify --trusted-public-keys <bob key>`
  exits 0. (2) Same closure without credentials → narinfo 404, Nix prints no bob path as copied.
  (3) After the closure is in a public release, anonymous substitution works. (4) A user of p2
  only → 404. (5) A wrong token → `nix-cache-info` 401.
- **Source:** R-REG-001, D58, ADR 0014, S12 §3.2.

#### R-REG-150 Visibility documents (new)
- **Statement:** Visibility is decided only from the §6.6.2 documents, one GET per object (cached).
- **Acceptance:** With an S3 operation counter: 1,000 narinfo requests for 10 distinct paths cause
  ≤ 20 GETs on `nix/vis/` and 0 LIST requests. Making a release private makes its exclusive
  paths 404 for anonymous callers within 31 s.
- **Source:** D58, D60, O-S12-1 (a).

### 6.6.3 Download proxy (D61)

- Every NAR, OCI blob and release asset byte goes S3 → orchestrator → client as a stream
  (`io.CopyBuffer`, 256 KiB buffer); no presigned URL, no redirect to the backend.
- `Range`: a single `bytes=a-b` / `a-` range is forwarded to S3 `GetObject Range`; multi-range →
  200 full body. S3 416 → 416.
- Limits: `max_parallel_downloads` (default 256) per orchestrator; beyond it 503 with
  `Retry-After: 1`. Client idle 60 s → connection closed.
- Integrity: the server does not re-hash on download; objects are immutable after promotion.

#### R-REG-151 Streaming download proxy (new)
- **Statement:** Downloads stream through the orchestrator with bounded memory and Range support.
- **Acceptance:** (1) 10 parallel downloads of a 1 GiB NAR: orchestrator RSS ≤ 64 MiB
  **[proposal]** (measured 19–33 MiB for 1–2 streams, S12 §3.4). (2) Connection cut at
  10,000,000 bytes: Nix resumes with `Range` and the path verifies. (3) No response of the server
  contains a `Location` header pointing to the S3 endpoint, and no URL with `X-Amz-Signature`.
- **Source:** D61, O-S12-2 (a), S12 §3.4.

### 6.6.4 Promotion protocol (per job, ADR 0016)

Trigger: the promotion controller sees a `JobRecord` in `succeeded` (the worker wrote
`<staging>/COMPLETE.json` and its signed result) with `pm` = "" (not yet promoted); all transitions are CAS on the
`JobRecord` (§05). Workers stage only paths that neither cache.nixos.org (HEAD narinfo) nor bob's
cache (HEAD own narinfo via the orchestrator with its download token) already has (R-REG-005).

Staging layout (worker writes; `<staging>` = `staging/<buildID>/<jobID>/<attempt>/`, §05.2.4):
`<staging>nar/<hash>.nar.zst`, `<staging>narinfo/<hash>.narinfo` (unsigned claim),
`<staging>oci/blobs/sha256/<hex>`, `<staging>assets/<file>`, `<staging>COMPLETE.json` last:
```json
{"v":1,"job":"01j…","build":"01j…","project":"api",
 "paths":["/nix/store/…-api-1.4.2"],
 "images":[{"repo":"api/server","tag":"1.4.2","manifest":"sha256:…","mediaType":"application/vnd.oci.image.index.v1+json","blobs":["sha256:…"]}],
 "assets":[{"file":"api-1.4.2-x86_64.AppImage","sha256":"…","size":31457280}],
 "skipped":["/nix/store/…-glibc-2.40"]}
```

Steps (orchestrator, idempotent, re-run until done):

1. GET `COMPLETE.json` (≤ 4 MiB, schema-validated). Missing → job promotion `blocked: staging
   incomplete`, job requeued per §05.
2. Parse every claim (≤ 4 MiB each; ≤ 100,000 paths). Topologically order paths (references in the set first).
3. Per level, 4 in parallel, for each path:
   a. narinfo already served → compare NarHash; differ → add to `non_reproducible` (keep the
      published one); add the project to the visibility document; done.
   b. GET the staged NAR, remember its ETag; stream: zstd decode → NAR structure check (magic,
      sorted unique names, no `/`, `.`, `..`, NUL, zero padding, nothing after the root; caps:
      NAR ≤ 64 GiB, ≤ 5,000,000 entries, names ≤ 255 B, symlink targets ≤ 4,095 B, depth ≤ 256)
      → NarHash, NarSize, FileHash, FileSize recomputed → **reference scan**: every 32-char
      substring matching a store hash of a path in this job's closure or in an already-served
      narinfo that the claim does not list → `failed: artifact verification` (refs are no longer
      blind worker claims).
   c. `CopyObject` to `nix/nar/<hash>.nar.zst` with `x-amz-copy-source-if-match: <ETag>`.
   d. PUT `nix/ls/<hash>.ls`; update `nix/vis/<hash>.json` (add project).
   e. Sign (ed25519, fingerprint `1;<path>;<NarHash>;<NarSize>;<refs full paths sorted, ","-joined>`,
      `Sig: <keyname>:<base64>`); PUT `nix/narinfo/<hash>.narinfo` with `If-None-Match: *`;
      412 = another orchestrator won = success.
4. Images: stream-verify each blob digest, copy to `oci/blobs/sha256/<hex>` (ETag guard) unless
   present, update `oci/vis/sha256/<hex>.json`, then update `oci/repos/<repo>/index.json` (tag
   last, CAS). Non-release builds tag `b-<build-id>`; release tags are written by §6.8.
5. Assets are verified (sha256, size) and stay in staging until a release copies them (§6.8);
   the release controller copies before the staging lifecycle expires (7 d) or the asset is lost
   and the release fails with `release asset expired; re-run the release pipeline`.
6. CAS `JobRecord.pm` → `done`. Staged objects of promoted paths are deleted by known key (§05 R-STORE-105); assets stay until a release copies them; the rest expires by the lifecycle rule.

Invariants: a served narinfo always has its NAR, `.ls`, visibility document and all in-set
references served; a crash leaves only orphans without narinfo (GC after 24 h).
Signing keys: `SigningKeyRecord` (`skey/<keyname>`, §05.4.5) in the control bucket only. Key name `<server_name>-<n>`.

#### R-REG-002 Signing only on orchestrators
- **Statement:** Only orchestrators hold signing keys; they sign only after their own verification.
- **Acceptance:** (1) A worker credential cannot read any `SigningKeyRecord` (S3 403). (2) Go
  signature equals `nix store sign` output byte for byte for 4 fixture paths. (3) Tamper case: a
  staged NAR replaced after upload → that path and dependents stay 404, job promotion
  `failed: artifact verification`, and the security event is logged with the worker id.
- **Source:** R-REG-002, S12 §3.1, §3.5.

#### R-REG-152 Crash-safe, concurrent promotion (new)
- **Statement:** Promotion follows §6.6.4 and is safe under orchestrator kill and concurrency.
- **Acceptance:** (1) Two orchestrators promote the same job: one reports `promoted N`, the other
  `alreadyThere N`; `nix store verify` OK. (2) SIGKILL at random points in 20 runs of a 1.1 GiB
  promotion: never a narinfo whose reference is 404; a re-run completes. (3) Same input-addressed
  path with different bytes → `non_reproducible` lists it and the first publication is unchanged.
  (4) A claim that omits a reference whose hash appears in the NAR → `failed: artifact verification`.
- **Source:** R-REG-002, R-SRV-004, R-NFR-006/007, S12 §3.5, risk 1.

#### R-REG-005 Only bob-built outputs
- **Statement:** The cache never serves or stores a path available from cache.nixos.org at
  staging time, and never proxies upstream caches.
- **Acceptance:** After a build whose closure contains `hello` from nixpkgs, `GET /nix/<hello-hash>.narinfo`
  → 404 and `COMPLETE.json.skipped` lists it; the server has no code path that fetches from
  cache.nixos.org (dependency check: no outbound HTTP client in `internal/cache`).
- **Source:** R-REG-005, D37.

#### R-REG-153 Signing key rotation (new)
- **Statement:** `CreateSigningKey` adds an ACTIVE key and keeps the old one VERIFY_ONLY;
  `RetireSigningKey` re-signs affected narinfos (maintenance LIST over `nix/narinfo/`, not a hot
  path) and then drops the key from `public_keys`.
- **Acceptance:** Rotate, promote a new path: it carries only the new signature; old paths still
  verify with the old key. Retire the old key: after the sweep every narinfo verifies with the new
  key alone; a narinfo rewrite uses `If-Match`.
- **Source:** R-REG-002, S12 §5.2.

### 6.6.5 Client configuration (written by the `bob` CLI; server side of R-DEV-004)

`WhoAmI.nix` / `GetServerInfo.nix` give the values. §02 writes:
```
substituters = https://cache.nixos.org https://<server>/nix
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY= <WhoAmI.nix.public_keys…>
netrc-file = <home>/etc/nix/netrc        # §01 R-NIX-024; machine <server> login bob password <bob_dt_…>, mode 0600
narinfo-cache-negative-ttl = 60
```
The CLI calls `AuthService/WhoAmI` with the download token before any Nix call that may hit the
server (Nix hides 401 behind a level-1 warning and then builds locally, STATUS finding S15), and
passes `--option narinfo-cache-negative-ttl 0` to the Nix call that fetches a remote build result
or a release.

#### R-REG-154 Download token works for WhoAmI and Nix, not the API (new)
- **Statement:** `bob_dt_` tokens are accepted by WhoAmI and download endpoints only.
- **Acceptance:** WhoAmI with a download token → `token_scope: "download"`; `ListProjects` with it
  → Unauthenticated `token not valid for the API`.
- **Source:** S12-2, R-AUTH-007.

## 6.7 OCI registry (pull only)

| Path | Answer |
|---|---|
| `GET /v2/` | no/invalid Bearer → 401 `WWW-Authenticate: Bearer realm="<base>/oci/token",service="bob"`; valid → 200 `{}` |
| `GET/HEAD /v2/<repo>/manifests/<tag or digest>` | manifest, `Docker-Content-Digest`, `Content-Type` = stored media type |
| `GET/HEAD /v2/<repo>/blobs/<digest>` | blob stream, single Range |
| `GET /v2/<repo>/tags/list?n=&last=` | `{name, tags}` visible tags only, from `oci/repos/<repo>/index.json` |
| `GET /v2/<repo>/referrers/<digest>` | empty OCI image index |
| any other method under `/v2/` | 405 `{"errors":[{"code":"UNSUPPORTED","message":"this registry is pull-only"}]}` |
| `GET /oci/token?service=bob&scope=repository:<repo>:pull[&scope=…]` | `{token, access_token, expires_in: 900, issued_at}` |

- Repository names `<project>/<image>`, distribution-spec regex, ≤ 255 bytes; tags
  `^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`.
- `oci/repos/<repo>/index.json`: `{"v":1,"tags":{"1.4.2":{"d":"sha256:…","r":"<release-id>","t":1761000000}}}`,
  CAS-written; `latest` points to the highest semver release tag and is the only tag that moves.
  Release tags are create-only within the document (an existing tag → release fails).
- Token endpoint: Basic with any API/download/SA token (user name ignored) or anonymous. Granted
  scopes = requested `pull` scopes on repositories with at least one object visible to the caller
  (§6.6.2); others are left out silently; `push`/`delete` never granted. Token =
  `bob-oci-v1.<b64url(json{sub, exp, acc:{"<repo>":["pull"]}})>.<b64url(HMAC-SHA256(OciHmacKey))>`.
  Invalid Basic credentials → 401 `{"errors":[{"code":"UNAUTHORIZED","message":"invalid credentials"}]}`.
- Per object, `/v2/` checks the token's repo scope **and** the object's visibility document.
  Missing and not-allowed give the same 401 `insufficient_scope` / 404 `MANIFEST_UNKNOWN`
  consistently (no existence leak). Blobs are content-addressed and shared across repositories;
  a blob is served when visible to the caller, regardless of which repository path asked.

#### R-REG-003 OCI pull registry
- **Statement:** Stock OCI clients pull bob-built images; only promotion writes.
- **Acceptance:** crane, skopeo, podman (rootful and rootless): anonymous pull of a public tag OK;
  private tag anonymous → `UNAUTHORIZED`; with SA download token (docker config) → OK; user of
  another project → same error as missing; `skopeo copy` push with valid creds → refused; HTTP
  `PUT /v2/x/manifests/y` → 405.
- **Source:** R-REG-003, D19, S12 §3.7.

## 6.8 Releases

- A release is produced by a pipeline whose definition has release outputs (§01/§04). After all
  jobs of the build are promoted, the release controller:
  1. Creates `ReleaseRecord` in `pending` (§05.6.4) create-only on (project, name, version) via `relv`; exists →
     pipeline fails `release <project>/<name>@<version> already exists; releases are immutable`.
  2. Moves the record to `publishing` and copies assets `<staging>assets/<file>` →
     `rel/<projectID>/<num10>/<file>` (sha256 verified, ETag guard).
  3. If visibility is public: adds the release id to `public` of every visibility document of the
     closure paths and image objects (bob-served part only).
  4. Writes image tags `<version>` and moves `latest` (CAS on the repo index).
  5. Updates `rpage`/`ridx` (CAS), sets `published`, sends `release.published` notifications.
     Release id = `<projectID>.<num10>` (§05.2.4).
- Visibility default comes from the release definition in `bob.go`; `UpdateRelease` changes it
  and the controller adds/removes the release id in the documents (idempotent sweep).
- `DeleteRelease` → `deleted` (§05.6.4): removes the release id from documents and tags,
  deletes `rel/<projectID>/<num10>/`; the record stays as a tombstone (`relv` kept, so the
  version is never reused). Its paths become ordinary cache paths subject to GC.
- Version: semver 2.0 from the tag (`v1.4.2` → `1.4.2`) or from the pipeline definition.

Release downloads: `GET /r/<project>/<name>/<version>/<file>` — public: anonymous; private:
Bearer (API token) or Basic (any token, user ignored) with `ReleaseService/GetRelease` on the
project; else 404. Headers: `Content-Type: application/octet-stream`,
`Content-Disposition: attachment; filename="<file>"`, `ETag: "<sha256>"`, `Cache-Control:
public|private, max-age=31536000, immutable`. `/latest/<file>` → 302 to the versioned path,
`Cache-Control: no-cache`.

#### R-REG-004 Releases
- **Statement:** Releases are immutable sets of artifacts, public or private, downloadable over
  HTTP and installable with `bob install`.
- **Acceptance:** Tag push `v1.4.2` on fixture project `acme` with release `api`: a release
  `acme/api@1.4.2` appears with closure roots per platform, an AppImage asset and an OCI tag.
  `curl -fsS <base>/r/acme/api/1.4.2/<file> | sha256sum` equals `Asset.sha256`. A second run of
  the same tag fails with the "already exists" message. `ResolveRelease(constraint "1.4")` returns
  1.4.2 when 1.4.0, 1.4.2 and 1.5.0 exist; `""` returns 1.5.0; `1.6.0-rc.1` only by exact match.
- **Source:** R-REG-004, D10.

Server side of **R-INST-003** (specified in §02 §02.8): `ResolveRelease` and the narinfo/NAR
endpoints of a public release answer anonymously; a private release answers NotFound without a
permitted token.

#### R-STORE-150 Releases are never collected (new)
- **Statement:** Cache GC never deletes an object whose visibility document lists a release, and
  never deletes `rel/` objects of a non-deleted release.
- **Acceptance:** With `cache_unreferenced_days = 0`, a GC run after 2 builds and 1 release
  deletes the unreleased build's paths and keeps every path and asset of the release; after
  `DeleteRelease` and another GC run they are gone.
- **Source:** R-STORE-007.

## 6.9 Pipelines and forges

### 6.9.1 Adapter interface (`internal/forge`)

Eight methods (ADR 0018 had six; `LookupRepo` and `EnsureWebhook` are added for project linking).

```go
// Package forge is bob's only view of GitHub, Forgejo and GitLab. One Adapter value exists per
// forge integration (one credential set); it is safe for concurrent use.
package forge

type Kind string

const (
	GitHub  Kind = "github"
	Forgejo Kind = "forgejo"
	GitLab  Kind = "gitlab"
)

// MaxWebhookBody is GitHub's documented payload limit; larger bodies are refused with ErrTooLarge.
const MaxWebhookBody = 25 << 20

var (
	ErrSignature   = errors.New("forge: webhook signature missing or invalid") // HTTP 401
	ErrPayload     = errors.New("forge: webhook payload malformed")            // HTTP 400
	ErrTooLarge    = errors.New("forge: webhook payload too large")            // HTTP 413
	ErrStale       = errors.New("forge: webhook timestamp outside tolerance")  // HTTP 401 (GitLab)
	ErrUnsupported = errors.New("forge: operation not supported by this forge")
	ErrAuth        = errors.New("forge: forge rejected bob's credentials")     // integration health → AUTH_FAILED
	ErrNotFound    = errors.New("forge: repository or object not found")
)

type Adapter interface {
	Kind() Kind
	// ParseWebhook verifies the signature on the raw body first, then normalizes. No I/O.
	ParseWebhook(h http.Header, body []byte, now time.Time) (Event, error)
	// SetStatus writes a commit status with context s.Context ("bob/<pipeline>").
	SetStatus(ctx context.Context, repo Repo, s Status) error
	// IssueCheckout mints a read-only credential for exactly req.Repo.
	IssueCheckout(ctx context.Context, req CheckoutRequest) (Checkout, error)
	// RevokeCheckout is idempotent; an already revoked or expired credential is success.
	RevokeCheckout(ctx context.Context, c Checkout) error
	// SweepCheckouts revokes bob-minted credentials created before cutoff. Returns 0, nil where
	// the forge expires credentials itself.
	SweepCheckouts(ctx context.Context, cutoff time.Time) (int, error)
	// LookupRepo resolves "owner/name" (GitLab: full path) to the forge's repository record.
	LookupRepo(ctx context.Context, fullName string) (Repo, error)
	// EnsureWebhook creates or updates bob's hook on repo (push, tag, PR/MR events) with url and
	// secret. GitHub App hooks are app-level: returns ErrUnsupported, which callers treat as OK.
	EnsureWebhook(ctx context.Context, repo Repo, url string, secret Secret) error
}

type Repo struct {
	Forge         Kind
	ID            int64
	FullName      string
	CloneURL      string // https only
	WebURL        string
	DefaultBranch string
	Private       bool
}

type State string // pending running success failure error cancelled

type Status struct {
	SHA, Context string
	State        State
	Description  string // truncated to the forge limit (GitHub 140 runes, others 255)
	TargetURL    string
}

type CheckoutRequest struct {
	Repo  Repo
	JobID string
	TTL   time.Duration // ≤ 1 h
}

type Checkout struct {
	CloneURL, Username string
	Token              Secret    // redacts in String/GoString/MarshalJSON/LogValue
	ExpiresAt          time.Time
	ForgeExpires       bool   // false only for Forgejo
	Handle             string // opaque; stored in CheckoutRecord for RevokeCheckout
}

// Secret is a string that never prints or marshals; Reveal returns the value.
type Secret struct{ v string }

func NewSecret(v string) Secret
func (s Secret) Reveal() string
```

Events (normalized, after `CheckEvent` invariants: repo id > 0, clone URL `https://`, SHAs
`^([0-9a-f]{40}|[0-9a-f]{64})$`, names ≤ 255 bytes, PR number > 0):

```go
type EventType string // push tag pull_request ping ignored

type Event struct {
	Forge         Kind
	DeliveryID    string
	Type          EventType
	Repo          Repo
	Sender        string
	Push          *Push        // exactly one of Push/Tag/PullRequest for push/tag/pull_request
	Tag           *Tag
	PullRequest   *PullRequest
	IgnoredReason string       // Type == "ignored"
}
type Push struct{ Branch, Before, After string; Created, Deleted bool }
type Tag struct{ Name, Commit string; Deleted bool } // Commit = peeled commit
type PullRequest struct {
	Number     int64
	Action     PRAction // opened synchronized reopened closed; others → Type ignored
	Title, HeadSHA, HeadBranch, HeadRepo, BaseBranch string
	FromFork, Merged bool
	FetchRef   string // refs/pull/N/head | refs/merge-requests/N/head (in the base repo)
}
type PRAction string
```

Constructors (each refuses a non-https base URL with `forge: base URL must be https`):

```go
// github.New builds a GitHub App adapter (google/go-github v88; App JWT RS256 via stdlib).
// Same shape in each package: func New(cfg Config) (forge.Adapter, error)
// github.Config{BaseURL, AppID, InstallationID, PrivateKeyPEM forge.Secret, WebhookSecret forge.Secret}
// forgejo.New: codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3 (A12); Config{BaseURL, BotLogin, BotPassword, WebhookSecret}
// gitlab.New: gitlab.com/gitlab-org/api/client-go; Config{BaseURL, AccessToken, SigningToken, AllowPlaintextSecret}
```
Example:
```go
a, err := forgejo.New(forgejo.Config{
	BaseURL: "https://git.acme.example", BotLogin: "bob-bot",
	BotPassword: forge.NewSecret(pw), WebhookSecret: forge.NewSecret(hookSecret),
})
if err != nil { return err }
repo, err := a.LookupRepo(ctx, "acme/api")
```

### 6.9.2 Per-forge mapping

| bob | GitHub | Forgejo | GitLab |
|---|---|---|---|
| Event header | `X-GitHub-Event` | `X-Forgejo-Event` (+`-Type`) | `X-Gitlab-Event` |
| Delivery id | `X-GitHub-Delivery` | `X-Forgejo-Delivery` | `webhook-id` |
| Signature | `X-Hub-Signature-256: sha256=<hex>` HMAC(body) | `X-Forgejo-Signature: <hex>` HMAC(body) | `webhook-signature: v1,<b64>` HMAC(`id.ts.body`), key = b64-decoded part after `whsec_` |
| Replay window | dedupe only | dedupe only | `webhook-timestamp` ±5 min + dedupe |
| push / tag | `push` `refs/heads/*` / `refs/tags/*` (commit `head_commit.id`) | same | `Push Hook` / `Tag Push Hook` (commit `checkout_sha`) |
| PR opened / new commits / reopened / closed | `opened`/`synchronize`/`reopened`/`closed` | `opened`/`synchronized`/`reopened`/`closed` | `open`/`update` with `oldrev`/`reopen`/`close`,`merge` |
| Fork | head.repo.id ≠ base.repo.id | head.repo_id ≠ base.repo_id | source_project_id ≠ target_project_id |
| Fetch ref | `refs/pull/N/head` | `refs/pull/N/head` | `refs/merge-requests/IID/head` |
| ignored | create, delete, release, others | create, delete, release, others | Release Hook, others |
| ping | `ping` | (test = push) | (test = push) |
| status pending / running | `pending` / `pending` | `pending` / `pending` | `pending` / `running` |
| success / failure / error / cancelled | `success`/`failure`/`error`/`error`+"cancelled" | `success`/`failure`/`error`/`warning` | `success`/`failed`/`failed`/`canceled` |
| bob credential | GitHub App (app id, key, installation id) | bot account password (D62); status token minted by bob (`write:repository`, repo-scoped) | project/group access token, Maintainer, `api` |
| Checkout credential | installation token, `repository_ids:[id]`, `contents: read`, 1 h fixed | PAT `read:repository`, `repositories:[repo]`, no expiry → revoke + sweep | deploy token `read_repository`, `expires_at` = job deadline |
| Clone user | `x-access-token` | bot login | `gitlab+deploy-token-N` |

### 6.9.3 Webhook intake (`POST /hooks/<integration-id>`)

1. Unknown integration → 404. Read ≤ `MaxWebhookBody`+1 bytes; more → 413.
2. `ParseWebhook` verifies on the raw bytes (constant-time); no secret configured → refuse. During
   a secret rotation the previous secret is accepted for 24 h. Failure → 401 `invalid signature`
   (the reason goes to the log, no header values).
3. Create `ForgeDeliveryRecord(integration, SHA-256(delivery id))` with `If-None-Match: *`;
   exists → 200 `duplicate`. Records live 30 days (> forge retry windows).
4. GET `RepoLinkRecord(integration, repo id)`; none → 202 `ignored: repository not linked`.
5. Create the `BuildRecord` (trigger from the event, state QUEUED) and respond 202 within 10 s.
   `ignored` events → 202 and only the delivery ring is updated.
6. Refs and SHAs reach git only as validated separate arguments, never through a shell.

#### R-PIPE-001 Forge integrations
- **Statement:** GitHub, Forgejo and GitLab adapters implement §6.9.1 with the mapping of §6.9.2.
- **Acceptance:** Contract test (every CI run): all three adapters pass the shared suite on the
  recorded fixtures (real Forgejo 16 and GitLab 19 deliveries, documented GitHub payloads): push,
  tag, PR opened/sync/fork, ignored kinds; unsigned and oversize bodies refused; GitLab ±5 min.
  Live Forgejo in the harness (every run) and live GitLab (slow suite, A13): hook → build →
  statuses → checkout → revoke round trip.
- **Source:** R-PIPE-001, R-PIPE-007, D48, A12, A13, S16.

#### R-PIPE-002 One repository per project
- **Statement:** A project links one repository via `RepoLinkRecord`; events of other repositories are ignored.
- **Acceptance:** A correctly signed delivery for repo id 2 on an integration whose only link is
  repo id 1 → 202, delivery result `ignored: repository not linked`, no build.
  `CreateProject` with a repo already linked to another project → AlreadyExists.
- **Source:** R-PIPE-002.

#### R-PIPE-008 Webhook security (new)
- **Statement:** Intake follows §6.9.3 exactly.
- **Acceptance:** Fixture tests: wrong signature → 401 `invalid signature`; replayed delivery id →
  200 and still one build; GitLab timestamp 6 min old → 401; body 25 MiB + 1 → 413; a push with
  `after = "--upload-pack=x"` → 400 (CheckEvent) and no git process started.
- **Source:** R-SEC-004, S16 §5.4.

### 6.9.4 From event to pipelines and statuses

- The build's first job is an `EVALBUILD` (ADR 0016). The worker evaluates `bob.go` at the commit,
  runs `pipeline.Match(trigger)` (§01) and reports the matched pipelines and their targets with the
  evaluation result. The orchestrator re-runs `pipeline.Match` on the reported pipeline
  definitions and the stored trigger and rejects the job result on mismatch (hostile input).
  No match → `SKIPPED` (no forge status beyond clearing `bob/evaluate`).
- API/UI `CreateBuild{pipeline}` runs that pipeline regardless of its triggers;
  `CreateBuild{targets}` runs no pipeline (REMOTE/API ad-hoc builds).
- Statuses, written by the status controller from `BuildRecord.reported` (idempotent, retried with
  backoff 5 s → 5 min, ≤ 24 h): `bob/evaluate` pending at QUEUED, success/failure after
  evaluation; then one `bob/<pipeline>` per matched pipeline: pending (queued), running,
  success/failure/error/cancelled. Description `#<number> <state>: <first error message>`
  (truncated); target URL = `Build.web_url`. Status credentials never leave the orchestrator.
- `ErrAuth` from any adapter call → integration health `AUTH_FAILED`, message
  `forge: integration <id>: forge rejected bob's credentials (HTTP 401); re-enter the credentials in Settings → Forges`.

#### R-PIPE-003 Pipelines run as defined and report back
- **Statement:** Matched pipelines run and report per-pipeline statuses with a link to the build.
- **Acceptance:** Live Forgejo: push to `main` with pipelines `ci` (push main) and `release`
  (tag v*) → only `bob/ci` appears, pending → running → success, target URL opens the build
  page (HTTP 200). A failing test → `failure` with description containing `BOB-E-2005`.
  A forged evaluation result that adds `release` → job result rejected, build FAILED with
  `evaluation result rejected: pipeline match mismatch` (this test needs the worker Attach client of
  Gate H G20: `tests/serverenv/workerclient`, built with worker registration).
- **Source:** R-PIPE-003, R-FW-040, R-SEC-004.

#### R-PIPE-004 Triggers by API, UI and --remote
- **Statement:** `CreateBuild` starts builds for a pipeline, for labels from a git ref, or from an uploaded source snapshot.
- **Acceptance:** `CreateBuild{project, git{sha}, pipeline:"ci"}` → build with trigger API;
  `bob build --remote //svc/api:server` (§02) → upload + build with trigger REMOTE; afterwards
  the CLI substitutes the result with negative TTL 0 (cache 200 on the narinfo).
- **Source:** R-PIPE-004, R-DEV-005.

### 6.9.5 Cancel, retry, rerun, concurrency (R-PIPE-005)

| Action | Allowed in | Effect |
|---|---|---|
| CancelBuild | PENDING, QUEUED, RUNNING | → CANCELLED (§05.6.2: queued jobs cancelled, running jobs get `cx` + CancelJob over Attach); statuses `cancelled`. On a terminal build → FailedPrecondition `build is already finished` |
| RetryBuild | FAILED, CANCELLED, SUPERSEDED | new build (trigger RETRY, `retry_of`, attempt+1) of the failed/cancelled pipelines; succeeded packages are cache hits |
| RerunBuild | any terminal state | new build, trigger RERUN, same source and pipelines |

Concurrency is §05.6.2's rule (project setting per ref, evaluated on `bbr`; default `queue`
for the default branch and tags, `supersede` for other branches and PRs, `parallel` optional).
`supersede`: a newer build for the same ref moves the older non-terminal build to SUPERSEDED,
`Trigger.superseded_by` set, forge status `cancelled` with description `superseded by #<n>`.
`queue`: the newer build stays PENDING until the older is terminal. Builds that run a release
pipeline are never superseded.

#### R-PIPE-005 Cancel, retry, re-run, concurrency
- **Statement:** As in the table and rules above.
- **Acceptance:** (1) Two pushes to a feature branch 1 s apart: the first build ends SUPERSEDED with
  `superseded_by` = second build, its forge status `cancelled`. (2) Two tag pushes with a `Queue`
  release pipeline run one after the other. (3) RetryBuild on a build with 1 failed of 10
  packages rebuilds only that package and its dependents (others CACHED/SUCCEEDED unchanged).
  (4) CancelBuild on a SUCCEEDED build → FailedPrecondition.
- **Source:** R-PIPE-005.

### 6.9.6 Checkout credentials (R-FLEET-006, D62)

```
worker holds lease (job J, epoch E) → WorkerControlService/GetCheckoutCredential(J, E)  (§05.9.1)
orchestrator: GET JobRecord; owner = caller wid and epoch = E, state CLAIMED|RUNNING, else PermissionDenied `permission_denied: worker holds no lease on job <id>`
  CheckoutRecord(J) in ftok/<sh2(J)> exists (repeat call, e.g. worker restart of the fetch) → RevokeCheckout(old handle) first; token values are never stored
  adapter.IssueCheckout({Repo, JobID: J, TTL: min(1h, job deadline − now)})
  CAS-add CheckoutRecord{J, epoch E, handle, expiresAt, forgeExpires} to ftok/<sh2(J)> (no token value)
  return sealed checkout (HPKE to the worker key) + download token
worker: git init; git -c "http.extraHeader=Authorization: Basic <b64(user:token)>" fetch --depth=1 <cloneURL> <sha|fetchRef>
        verify HEAD = expected SHA; credential never written to .git/config; discarded after fetch
job terminal or lease lost/requeued → orchestrator RevokeCheckout(handle) (idempotent), remove CheckoutRecord
forge-sweeper every 60 s (§05.11 controller) → revoke CheckoutRecords of terminal/missing jobs;
  every 5 min also SweepCheckouts(now − max job duration − 10 min) for tokens without a record
```
Forgejo (D62): bob stores the bot password (encrypted in the control bucket), mints
`bob-job-<unix-seconds>-<jobID>` tokens limited to the one repository with `read:repository`,
revokes at job end, sweeps by the timestamp in the name. The bot account must be a collaborator
only on linked repositories and cannot use 2FA (documented in the admin guide).

R-FLEET-006 is specified once, in §05 §05.9.2 (its acceptance includes the live-Forgejo
checks of this flow).

### 6.9.7 Source upload for `--remote`

`PUT /upload/source/sha256/<hex>` with `Authorization: Bearer bob_at_…`, `Bob-Project: <id>`,
`Content-Type: application/zstd`, body = zstd tar of the files §02 selects (tracked + untracked
not ignored). Caps: 512 MiB compressed **[proposal]**, ≤ 200,000 entries, regular files,
directories and symlinks only (no absolute or `..` targets; checked by the worker on unpack).
The server streams to `src/<project>/<hex>.tar.zst` with `If-None-Match: *` while hashing; hash
mismatch → 400 `upload does not match its sha256`, object deleted; existing → 200 without reading
the body beyond the headers (client sends `Expect: 100-continue`). Lifecycle 7 days.

#### R-PIPE-009 Remote source snapshots (new)
- **Statement:** Uploads are content-addressed, size-capped, project-scoped and permission-checked.
- **Acceptance:** Upload with a wrong hash → 400 and no object; without `CreateBuild` on the
  project → 404; same upload twice → second answers 200 in < 100 ms with ≤ 1 MiB body sent;
  a tar with `../x` → job fails `source snapshot contains an unsafe path: ../x`.
- **Source:** R-PIPE-004, R-DEV-005, R-SEC-004.

### 6.9.8 Outgoing notifications (R-PIPE-006)

Events `build.finished` `{event, build: Build JSON (proto JSON), project}` and
`release.published` `{event, release}`. POST with `Content-Type: application/json`,
`Bob-Event`, `Bob-Delivery: <id>`, `Bob-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>`.
Delivery from the notify controller: timeout 10 s, attempts at 0, 10 s, 1 min, 10 min, 1 h; 2xx = done.
https only.

#### R-PIPE-006 Outgoing webhooks
- **Statement:** Notifications are signed and retried as above.
- **Acceptance:** A test receiver returning 500 twice then 200 sees 3 deliveries with the same
  `Bob-Delivery`, valid signatures, and `last_result` shows `200 at …`.
- **Source:** R-PIPE-006, D43.

#### R-PIPE-007 Adapter testing
- **Statement:** As in ADR 0018 Decision 7 (fixtures every CI run, GitHub replay server, live
  Forgejo in the harness, live GitLab in the slow suite).
- **Acceptance:** CI job list contains `forge-fixtures`, `forge-github-replay`, `forge-forgejo-live`
  (fast suite) and `forge-gitlab-live` (slow suite); each passes on main.
- **Source:** R-PIPE-007, D48, A13.

## 6.10 Web UI

### 6.10.1 Stack and structure

- SvelteKit 2 + Svelte 5 runes, `@sveltejs/adapter-static` with `fallback: '200.html'`,
  `ssr = false`, `prerender = false` except `/login`. Tailwind CSS v4 (`@tailwindcss/vite`).
  Client: `@connectrpc/connect-web` + generated `@bufbuild/protobuf` v2 types. No other runtime
  dependencies except a small SVG graph layout written in bob (no component or chart library;
  charts are hand-written SVG). Banned: `clsx`, `cva`, `tailwind-merge`, `@apply` piles, any UI kit.
- Built by bob (R-ARCH-005) into `web/build`, embedded with `embed.FS` in `internal/ui`, served by
  the orchestrator (§6.2).
- Layout: fixed left nav (200 px; collapses to a top bar < 900 px), page header (title, breadcrumb,
  primary action right), content. Theme switch in the nav footer: Light / Dark / System;
  default **Light** regardless of OS; stored in `localStorage["bob.theme"]`; applied as
  `data-theme` on `<html>` before first paint (inline script in `app.html`).
- Live data: `WatchBuild` and `FollowLog` streams on the build page; lists refresh every 10 s
  while visible (Page Visibility API), never faster.
- Every list has a filter bar (text `query` + the request's enum filters) whose state is in the URL
  query string; every id (build, job, worker, release, digest, user, role, SA) renders as a link
  to its detail route. Ids, hashes, store paths, labels and logs use the mono font.
- Log view: virtualised list, ANSI colours mapped to tokens (no other escape handling; text is
  inserted as text nodes only), per-package filter, search via `SearchLog`, "follow" toggle.

### 6.10.2 Design tokens (Tailwind v4 theme, `web/src/app.css`)

```css
@import "tailwindcss";

@theme {
  --font-sans: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  --font-mono: ui-monospace, "SFMono-Regular", Menlo, Consolas, monospace;
  --text-xs: 11px;  --text-sm: 12px;  --text-base: 13px;  --text-lg: 15px;  --text-xl: 18px;
  --radius-xs: 2px;
  --spacing: 4px;

  --color-bg: #ffffff;        /* page */
  --color-surface: #f6f6f3;   /* nav, table header, code blocks */
  --color-line: #deded8;      /* 1px borders */
  --color-fg: #111110;        /* text */
  --color-muted: #5e5e58;     /* secondary text */
  --color-accent: #ffd400;    /* yellow: fills only (primary button, running, selection bar) */
  --color-on-accent: #111110; /* text on yellow */
  --color-focus: #111110;     /* 2px focus outline */
  --color-ok: #1e7b34;
  --color-fail: #b42318;
  --color-warn: #8a5a00;
  --color-idle: #77776f;      /* queued, cancelled, skipped */
}

[data-theme="dark"] {
  --color-bg: #0e0e0d;
  --color-surface: #171715;
  --color-line: #2c2c29;
  --color-fg: #edede8;
  --color-muted: #a3a39b;
  --color-accent: #ffd400;
  --color-on-accent: #111110;
  --color-focus: #ffd400;
  --color-ok: #4cc26b;
  --color-fail: #f2685c;
  --color-warn: #e0a526;
  --color-idle: #8c8c84;
}
```

Rules: yellow is never a text colour on the light theme (contrast); it is a fill with
`on-accent` text, a 2 px left bar for the active nav item and the selected table row, or the
running state. Status = small square-cornered badge (`rounded-xs`, 1px border in the state
colour, text in the state colour; running = yellow fill). Tables: row height 28 px, header in
`surface`, 1px `line` borders, numeric columns right-aligned with tabular figures. Only
`rounded-xs` or none; no shadows beyond none; no gradients, glass, blur, glow, emoji, hero cards.

Components (hand-written, `web/src/lib/ui/`): `Button` (primary/secondary/danger), `Input`,
`Select`, `Checkbox`, `Badge`, `StatusBadge`, `Table` (+ `Th`, `Td`), `FilterBar`, `Pagination`,
`Tabs`, `Dialog`, `Confirm`, `KeyValue`, `Mono` (copy button), `CodeBlock`, `LogView`, `Graph`,
`Waterfall`, `BarChart`, `LineChart`, `EmptyState` (one line of text, no illustration), `Toast`.

### 6.10.3 Pages and wireframes

Routes: `/` overview · `/projects` · `/p/<project>` (tabs builds, pipelines, releases, settings,
permissions) · `/p/<project>/builds/<id>` · `/fleet` · `/fleet/workers/<id>` · `/cache` ·
`/registry` (releases, images) · `/registry/<repo>` · `/p/<project>/releases/<name>/<version>` ·
`/access` (users, roles, matrix, service accounts, OIDC) · `/settings` (general, forges, signing
keys, retention, webhooks) · `/analytics` · `/login` · `/device` · `/oauth/consent` · `/account`.

Overview `/`
```
┌ bob ─────────┬──────────────────────────────────────────────────────────────────────┐
│ Overview  ▌  │ Overview                                                             │
│ Projects     │ ┌ Running 4 ─┬ Queue 7 ─────┬ Workers amd64 6/8 arm64 2/2 ┬ Cache 91% ┐ │
│ Fleet        │ └────────────┴──────────────┴─────────────────────────────┴──────────┘ │
│ Cache        │ Running builds                                          [filter    ] │
│ Registry     │ ID          Project  Pipeline  Ref        State     Progress  Started │
│ Access       │ 01jb…k2p    api      ci        main       RUNNING   41/120    2m      │
│ Settings     │ Recent failures                                                       │
│ Analytics    │ 01jb…7aa    web      ci        pr/88      FAILED    BOB-E-2005 …  14m │
│ ─────────    │                                                                       │
│ Light|Dark   │                                                                       │
└──────────────┴──────────────────────────────────────────────────────────────────────┘
```
Stat strip = four bordered cells with number + label (not cards with icons).

Project `/p/api` (tab Builds)
```
api  ·  acme/api (forgejo) · link OK                               [Run pipeline ▾]
[Builds] Pipelines  Releases  Settings  Permissions
filter: [text     ] state [any ▾] pipeline [any ▾] trigger [any ▾]
#    ID          Pipeline  Trigger        Ref/SHA          State      Dur    Created
412  01jb…k2p    ci        push alice     main  3f2a91c    RUNNING    2:11   2m
411  01jb…h0d    ci        push alice     main  77c0e1b    CANCELLED  0:40   9m  superseded by #412
                                                              ‹ prev  next ›
```

Build detail `/p/api/builds/<id>`
```
#412  api · ci · main 3f2a91c · push by alice           RUNNING   [Cancel] [Retry] [Re-run]
platforms linux/amd64 linux/arm64 · variant release · attempt 1 · 41/120 · cached 63
┌ Packages ───────────────── [table|graph] ┬ Log  //svc/api:server  [follow ●] [search  ] ┐
│ ● //svc/api:server   amd64 BUILDING 0:42 │ 0041 compiling internal/http …               │
│ ✓ //lib/db:db        amd64 CACHED        │ 0042 …                                       │
│ ✗ //lib/auth:test    amd64 FAILED   0:09 │                                              │
│   └ BOB-E-2005 tests failed  lib/auth/bob.go:12:3                                       │
├ Timing waterfall ───────────────────────────────────────────────────────────────────── ┤
│ //lib/db:db       ███                                                                    │
│ //svc/api:server      ██████████▒▒                                                       │
├ Artifacts ─────────────────────────────────────────────────────────────────────────────┤
│ /nix/store/…-api-server   1.2 MiB   [copy]    image api/server:b-01jb…k2p  sha256:…     │
└────────────────────────────────────────────────────────────────────────────────────────┘
```
(✓ ✗ ● here stand for the coloured StatusBadge text OK/FAIL/RUN; no emoji or icon fonts.)
Graph mode: layered left-to-right SVG (longest-path layering, ≤ 300 nodes; above that the toggle
is disabled with `graph has <n> packages; use the table`).

Fleet `/fleet`
```
Workers   filter [text] platform [any ▾] state [any ▾]
ID              Platform     State     Jobs  CPU  Mem     Heartbeat  Labels         Actions
w3k…p0q         linux/amd64  ACTIVE    1     8    15 GiB  4s         ssd            [Drain][Revoke]
Orchestrators
ID     Endpoint                 Version  Started   Heartbeat  Leader of
o-1    https://10.0.0.5:8443    0.1.0    3d        2s         promotion, gc
```

Cache `/cache`: stat strip (size, paths, hit rate 24 h / 7 d, retention days), hit-rate line
chart (30 days), top packages table (project, label, hits 7 d, size), path lookup input
(store hash → `GetPath` key/value view).

Registry `/registry`: tabs Releases (project, name, version, visibility, platforms, date) and
Images (repository, tags, public, updated); release detail shows `bob install acme/api@1.4.2`,
assets with size/sha256/download link, closure roots, image refs (`podman pull …`).

Access `/access`: tabs Users (login, source, roles, last login, disabled), Roles (id,
permissions count; editor = method picker from `ListPermissionCatalog` × project), Matrix
(rows principals, columns projects, cells role names; filter by project/role), Service accounts
(tokens table, "create token" dialog shows the secret once with a copy button), OIDC (providers,
mappings `claim = value → role @ project`).

Settings `/settings`: General (server name, base URL, session policy), Forges (integrations,
health, webhook URL, "rotate secret", recent deliveries), Signing keys (name, state, public key,
rotate/retire), Retention (days per kind), Webhooks (outgoing).

Analytics `/analytics`: project selector, build history bars (succeeded/failed per day),
p50/p90 duration line, slowest packages table (`PackageDurations`), recent failures.

Login `/login`
```
            bob
            ┌──────────────────────────┐
            │ Login       [          ] │
            │ Password    [          ] │
            │ [ Log in ]               │
            │ ──────── or ──────────── │
            │ [ Continue with Corp SSO ]│
            └──────────────────────────┘
```

#### R-UI-001 Static SvelteKit app served by Go
- **Statement:** The UI is a static build embedded in `bob-server`.
- **Acceptance:** `web/build` contains `200.html` and no server output; `svelte.config.js` uses
  `adapter-static`; `GET /p/x/builds/y` on a running server returns `200.html` with
  `Cache-Control: no-cache`; `/_app/immutable/*` returns `immutable`.
- **Source:** R-UI-001, D5.

#### R-UI-002 Pages
- **Statement:** All routes of §6.10.3 exist and show the listed data.
- **Acceptance:** Browser e2e against a seeded fixture server: each route renders without console
  errors and shows the seeded entity (by id text); build page shows graph table, live log lines
  appearing within the R-LOG budget, waterfall bars and artifacts.
- **Source:** R-UI-002.

#### R-UI-003 Visual style
- **Statement:** Tokens and rules of §6.10.2.
- **Acceptance:** (manual) code-steward review with the checklist: no gradients, no `rounded-*`
  other than `rounded-xs`/`rounded-none`, no shadow/blur/glow, no emoji, yellow only as fill.
  A helper script `web/scripts/style-check.sh` greps for these and is run by the steward (not a
  CI gate, D45).
- **Source:** R-UI-003, D21, D45.

#### R-UI-004 Light default, dark optional
- **Statement:** Light is the default even when the OS prefers dark; the user can choose Dark or System.
- **Acceptance:** e2e with `prefers-color-scheme: dark` and empty storage → `<html data-theme="light">`
  and computed body background `rgb(255, 255, 255)`; choose Dark → `#0e0e0d`, persists after reload.
- **Source:** R-UI-004, D35.

#### R-UI-005 Tailwind with hand-written components
- **Statement:** No component or class-composition libraries.
- **Acceptance:** `web/package.json` dependencies ⊆ {svelte, @sveltejs/kit, @sveltejs/adapter-static,
  @sveltejs/vite-plugin-svelte, vite, tailwindcss, @tailwindcss/vite, @connectrpc/connect,
  @connectrpc/connect-web, @bufbuild/protobuf, typescript, svelte-check, test tooling}; a test fails on any other.
- **Source:** R-UI-005, D21.

#### R-UI-006 Filterable lists, linked ids
- **Statement:** Every list has a filter; every id links to its detail page.
- **Acceptance:** e2e over all list pages: typing a seeded id prefix in the filter reduces rows to
  the matching ones and updates the URL query; every cell with class `id` is an `<a href>` whose
  target route renders the same id.
- **Source:** R-UI-006.

#### R-UI-007 First load budget (new)
- **Statement:** The UI loads fast on small machines.
- **Acceptance:** **[proposal]** initial JS ≤ 250 KiB gzip, CSS ≤ 30 KiB gzip; overview first
  contentful paint ≤ 1.0 s [8c/15G] and ≤ 2.0 s [2c/4G] against a local server (no measurement
  yet; derived from the bundle cap and API p99 below).
- **Source:** R-NFR-004, R-NFR-008.

## 6.11 Budgets [proposal]

| Budget | Value | Derived from |
|---|---|---|
| 100 MiB closure: worker upload to staging | ≤ 1.5 s [8c/15G]; ≤ 3 s [2c/4G] | quiet 0.64–0.76 s [8c/15G]; loaded 1.19–2.13 s [2c/4G] (S12 §3.8) |
| 100 MiB closure: promotion | ≤ 1.5 s [8c/15G]; ≤ 3 s [2c/4G] | quiet 0.49–0.54 s; loaded 0.89–2.27 s |
| 100 MiB closure: Nix substitution from bob, same LAN | ≤ 1.5 s [8c/15G]; ≤ 3 s [2c/4G] | quiet 0.61–0.67 s; loaded 1.11–1.84 s |
| narinfo latency (warm visibility cache) | p50 ≤ 5 ms, p99 ≤ 20 ms | 2–5 ms incl. a visibility HEAD (S12 §5.7) |
| Orchestrator RSS at 10 × 1 GiB parallel downloads | ≤ 64 MiB | 19–33 MiB for 1–2 |
| API unary p99 (Get*, warm caches, RustFS local) | ≤ 50 ms; List* ≤ 150 ms | S12 narinfo 2–5 ms + one dynamitedb GET per index slot (§05) |
| Webhook → BuildRecord created | p99 ≤ 500 ms after receipt | delivery latency is the forge's (Forgejo 2–3 s, S16 §3.2) |
| Checkout mint (Forgejo / GitLab) | ≤ 500 ms p99 | 153 / 145 ms median (S16 §3.2, loaded) |
| Live log line → UI (D55 pull over Attach, 250 ms chunks) | = §05 R-NFR-105: p99 ≤ 1 s; lake fallback p99 ≤ `log.flush_interval` + 10 s (40 s at the 30 s default) | S17 fan-out p99 223 ms; S10 lake p99 2.7 s at 5 s flush |
| Token revocation effective | ≤ 10 s (API), ≤ 30 s visibility | cache TTLs §6.3.1/§6.6.2 |

#### R-NFR-150 Cache throughput (new)
- **Statement:** The cache budgets above hold on the reference backend.
- **Acceptance:** `measure.sh`-style harness benchmark (RustFS, 5 runs) meets the upload,
  promotion and substitution rows in both profiles (median) and the narinfo latency row.
- **Source:** R-NFR-003, R-NFR-008, S12 quiet rerun.

## 6.12 Open points

- **[open] O-06-1** `public` list cap of 10,000 release ids per visibility document.
  Recommendation: keep the cap; if hit in practice, switch that document to a counter
  `{"public_count": n}` maintained by CAS.
- **[open] O-06-2** Source snapshot cap 512 MiB for `--remote`. Recommendation: keep, make it a
  setting; measure on real monorepos in Stage 3.
- ~~O-06-3~~ Live-log pull interval: closed at integration — the worker sends `LiveChunk`s every
  `live_log.chunk_interval` (250 ms, §05.11.1) over Attach while subscribed.
- **[open] O-06-4** GitHub not exercised live (S16). Recommendation: keep replay tests; add a
  manual live check against a test org before the first release (manual acceptance).
- **[open] O-06-5** Visibility LRU TTL 30 s (making a release private takes ≤ 30 s).
  Recommendation: accept and document.

## 6.13 Escalations

- **E-06-1 — closed at integration.** The inbound `WorkerAgentService` is replaced by §05's
  `WorkerControlService/Attach`, a stream the worker opens outbound to each orchestrator, which
  carries Wake (D54), live-log pull (D55), CancelJob and Drain. Workers need no inbound port;
  without Attach they still work by polling and lake-only logs (R-SRV-100).
