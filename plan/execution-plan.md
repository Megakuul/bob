# bob — Execution plan (Stage 3)

Status: draft for Gate S, 2026-10-21. Inputs: SPEC.md (252 requirement ids), ADRs 0001–0019,
BOOTSTRAP §3–§7, `plan/architecture.md`. HARNESS.md (written in parallel) defines the suites.

## Conventions

- **Task id** `M<milestone>.<n>`, used in commit messages (`M5.2: go per-package derivations`).
- **Covers:** the SPEC requirement ids the task finishes. **Tests that must turn green:** always
  the acceptance tests for every covered R-id (red since Gate H), plus the extras listed
  (goldens, fault-catalogue entries, budgets). A task is done when those are green and the CI
  command passes: `go run ./tools/ci` until M5.6, then `bob pipeline run ci`.
- **Role** (BOOTSTRAP §7): `arch` = architect (risky design, Nix backend, ecosystem mappings,
  reconciliation), `impl` = implementer. Every milestone ends with a **reviewer** pass against
  SPEC and a **code-steward** pass (Phase 3.2 step 4); the explorer/test-runner runs CI and
  summarises failures throughout. Usage goes into `plan/usage.md`.
- **Size:** S ≤ 1 day of agent work, M ≈ 2–4 days, L ≈ a week or more.
- **∥:** tasks in the same milestone with the same letter own disjoint packages and may run in
  parallel (separate build caches, merge only on green CI). `—` = sequential.
- **Open first:** Appendix A items the task must settle before coding (the SPEC recommendation
  applies unless the owner decided otherwise at the Phase 3.0 kickoff).

## Order and tracks

```
M0 ─► M1 ═ Gate H ═► M2 ┐
                     M3 ┴► M4 ─► M5 (bob builds itself; CI → bob pipeline run ci)
                                  │
            Track A (framework)   ├─► M6 ─► M7 ─► M8 ─► M9 ─► M10 ─► M11 ─► M12
            Track B (server)      └─► M13 ─► M14 ─► M15 ─► M16 ─► M17
                                                     M9 + M10 + M15 ──► M18 (UI) ─► M19
```

Risk order (BOOTSTRAP §3 Phase 2.4): Nix backend and rootless store (M2), error translation
(M4.2), Go ecosystem (M5) come first; UI polish (M18) last. Tracks A and B touch disjoint
trees (`pkg/<eco>`, `pkg/output*` vs `internal/`) and run in parallel after M5. Track B
generates `proto/` Go code with `buf` from Go `tool` directives until M10.2, then with bob.

---

## M0 — Workspace skeleton and CI command (Phase 3.1, part 1)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M0.1 | Module `github.com/megakuul/bob`, `pkg/` layer packages, `cmd/{bob,bob-server,bob-builder}`, `internal/` and `proto/` stubs, `web/` SvelteKit skeleton; every feature returns a "not implemented" `diag` error. | R-ARCH-001, R-ARCH-004 | `bob --help` lists no server command | — | arch | M | — | — |
| M0.2 | `tools/ci`: fmt, vet, linters incl. no-panic and import-boundary rules, `TestLayering` encoding the layer table, LIST-only-in-sweep rule, `buf lint`/`buf breaking`, `svelte-check` | R-ARCH-002, R-ARCH-003 | layering test rejects a planted upward import | M0.1 | impl | M | a | — |
| M0.3 | Licence check, `ATTRIBUTION.md` skeleton (static Nix LGPL/GPL busybox, D52), protected-file report | — | licence check fails on a planted GPL module | M0.1 | impl | S | a | — |
| M0.4 | Parity catalogue: R-id ↔ test mapping, generated `docs/parity.md`, pass-count floor | — | parity generator lists all 252 ids | M0.1 | impl | S | a | — |
| M0.5 | Budgets file generated from SPEC entries; fast budget runner in CI, slow benchmark command | — | 97 proposal rows present | M0.1 | impl | S | a | — |
| M0.6 | Golden framework (IR, `.drv`, `bob.lock`, generated native files) and HTML diff review workflow | — | golden mismatch fails CI | M0.1 | impl | S | a | — |
| M0.7 | `pkg/bob/diag`: `Error`, `Code`, `Class`, complete code table and templates (§02.3) | R-NIX-028 | code-table golden | M0.1 | arch | S | a | — |

## M1 — Complete harness, ends at **Gate H** (Phase 3.1, part 2)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M1.1 | Fixture repos `tests/fixtures/repos/` (one per ecosystem, polyglot monorepo, cross-repo pair) and `examples/` skeletons | — | fixtures evaluate as stubs | M0 | arch | L | a | — |
| M1.2 | Error-quality suite: fault catalogue runner (code, Go source position, no raw Nix text) | — | every catalogue entry runs (red) | M0.7 | impl | M | a | — |
| M1.3 | Determinism and reproducibility harness (evaluate twice, build twice on two workers) | — | — | M0 | impl | S | a | — |
| M1.4 | Rootless matrix runner (supported distros; userns-blocked distro expects the R-NIX-022 text) | — | — | M0 | impl | M | a | O-01-5 |
| M1.5 | Server test environment: RustFS launcher, S3 counting proxy (LIST detector), kill -9 chaos driver, recorded GitHub/Forgejo/GitLab payloads, real `nix`/`podman` client conformance runners | — | proxy counts a planted LIST | M0 | impl | L | a | — |
| M1.6 | All-in-one local dev mode (orchestrator + worker + local RustFS) for agents. Not specified in SPEC; HARNESS.md defines its command | — | starts and stops cleanly | M1.5 | impl | M | — | name/flags (HARNESS.md) |
| M1.7 | UI test harness: headless screenshots of every page, light and dark, against goldens; API-state checks first | — | — | M0.1 | impl | M | a | — |
| M1.8 | Red acceptance tests for every SPEC R-id, registered in the parity catalogue; specify `engine.DevPreparer` | — | parity: 252/252 ids have ≥ 1 test | M1.1–M1.7 | arch + impl (per section) | L | b | O-02-10 |
| M1.9 | Re-measure busy-host numbers on a quiet host; finalise budgets file | — | — | M0.5 | impl | S | b | O-05-5 |
| M1.10 | Harness self-validation (§6.10: drop an edge, leak raw Nix text, break drift detection, skip a permission check, worker reads control bucket, lose a log flush) → `docs/harness-validation.md` | — | each fault makes CI fail | M1.8 | arch | M | — | — |

**Gate H:** owner reviews fixtures, goldens, budgets and the validation report. Tests are
protected from here on (BOOTSTRAP §5.2).

## M2 — Rootless store and derivation layer (E0/E1, highest risk)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M2.1 | `engine/drv`, `engine/nar`: structured-attrs derivations, `MakeStorePath`, FOD paths, NAR | R-NIX-001, R-NIX-006, R-NIX-003 | `.drv` goldens; path oracle vs `nix` | Gate H | arch | M | a | — |
| M2.2 | `engine/store`: embedded static Nix, namespace entry, refusal text, bob-owned config, coexistence, single-user concurrency; `scripts/fetch-nix.sh` | R-NIX-020, R-NIX-021, R-NIX-022, R-NIX-024, R-NIX-025, R-NIX-026 | rootless matrix; namespace-entry budget | Gate H | arch | L | a | O-01-5 |
| M2.3 | Derivation import and caching | R-NIX-007 | — | M2.1, M2.2 | impl | S | — | — |
| M2.4 | `engine/fetch`: bob-side fetcher, no user-influenced FODs | R-NIX-014, R-NIX-008, R-FW-063 | fault: wrong hash (BOB-E-3001) | M2.1 | impl | M | b | O-01-1 |
| M2.5 | `engine/srcs`: repo source import and source hashing | R-NIX-009, R-FW-021 | 10,000-dir import budget | M2.2 | impl | M | b | O-01-3 |

## M3 — Core API, graph IR and evaluation (parallel to M2: L0, E0-ir, E3-eval)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M3.1 | Labels, label patterns, source positions | R-FW-015, R-FW-016 | — | Gate H | impl | S | a | — |
| M3.2 | `pkg/bob` core API: values not strings, memoised constructors, API style, `bob.Run`, tests as packages | R-FW-001, R-FW-010, R-FW-011, R-FW-012, R-FW-014, R-FW-023, R-FW-024 | gopls completion check | M3.1 | arch | M | — | — |
| M3.3 | `pkg/bob/ext`, Graph IR v1, `engine.Ecosystem` registry | R-FW-046, R-FW-047, R-FW-051 | IR goldens | M3.2 | arch | M | — | — |
| M3.4 | `bob.go` rules, evaluator generation, version lock, eval key, determinism | R-FW-002, R-FW-043, R-FW-044, R-FW-008 | eval budgets (§1.17) | M3.3 | impl | M | b | O-01-4 |
| M3.5 | evalbox sandbox (cgroup, namespaces, Landlock, seccomp, fd-3 cap), local and worker | R-FW-045 | S13 hostile cases | Gate H | arch | L | a | — |
| M3.6 | `engine/lock`: `bob.lock` container, locking on demand, unparseable lock, round-trip, nixpkgs pin | R-FW-004, R-FW-005, R-FW-048, R-FW-065, R-NIX-005 | `bob.lock` goldens | M3.3 | impl | M | b | O-02-11 |
| M3.7 | Platform/variant model in core, configured labels, platform-generic packages | R-FW-030, R-FW-050 | — | M3.2 | impl | S | b | — |

## M4 — Build pipeline, builder, error translation, CLI core

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M4.1 | `bob-builder` + `bspec` job v1, core ops, logging/exit codes, structural success | R-NIX-015, R-NIX-016, R-NIX-017, R-NIX-018 | reproducible builder store path | M2.3 | arch | M | a | — |
| M4.2 | `engine/translate`: internal-json → `diag`, failures in bob terms, no raw Nix text | R-NIX-010, R-NIX-012 | full core fault catalogue (both Nix versions) | M4.1, M0.7 | arch | L | — | — |
| M4.3 | `pkg/nixpkgs`, `engine/nixidx`: nixpkgs as package source, pre-checks in Go, `resolve.nix` index | R-NIX-002, R-NIX-011, R-NIX-013, R-NIX-027 | index budget | M3.6, M2.2 | impl | M | a | — |
| M4.4 | Fixup contract; per-derivation overhead | R-NIX-019, R-FW-020 | N=100 `bob.Run` chain budget | M4.1 | impl | M | b | — |
| M4.5 | Tests and loopback services, per-test-unit result cache | R-FW-025, R-FW-064 | — | M4.1, M3.2 | impl | M | b | O-02-9 |
| M4.6 | `engine/build.Run` stage order, works without a server; `pkg/cli`: command set (stubs where later), output modes and `--json`, exit codes, human output, local logs, `check`, `lock`, `graph` | R-FW-049, R-FW-009, R-CLI-001, R-CLI-002, R-CLI-005, R-CLI-006, R-CLI-007, R-CLI-008, R-CLI-009, R-CLI-010, R-CLI-011 | `--json` stage-sequence golden | M4.2–M4.5, M3.4 | impl | L | — | O-02-11 |

## M5 — Go ecosystem and self-hosting (R-ARCH-005)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M5.1 | Common ecosystem rules: generated native files (read-only, tracked), drift detection, lock records its declaration | R-FW-060, R-FW-061, R-FW-062 | faults: drift (1011), stale lock (1012) | M4.6 | arch | M | — | O-03-1 |
| M5.2 | `pkg/golang` + gen + bop: hybrid granularity, std, cgo (nixpkgs C deps; `cc.Lib` deps finish in M7.1), export-data cutoff, test main | R-GO-001a, R-GO-002a, R-GO-005 | Go budgets (§03.8) | M5.1 | arch | L | — | O-03-3, O-03-4 |
| M5.3 | Go resolution in `bob lock` (build list = go command's) | R-GO-002b | — | M5.1 | impl | M | a | — |
| M5.4 | `go.mod`/`go.sum` generation, edits blocked, toolchain per module | R-GO-003a, R-GO-004a | `go.mod` goldens | M5.3 | impl | M | — | O-03-5 |
| M5.5 | Cross-repo use (`bob.Import`) | R-FW-022 | `examples/cross-repo` | M5.4 | impl | S | b | — |
| M5.6 | **bob builds itself:** bob's own `bob.go` files (bob, bob-builder, bob-server, embedded static Nix), `scripts/bootstrap.sh`, `bob pipeline run`; bob's `ci` pipeline wraps the `tools/ci` checks as tests; **CI switches to `bob pipeline run ci`** (`go run ./tools/ci` stays as the bootstrap fallback) | R-ARCH-005, R-CLI-012 | bootstrap ends in a bit-reproducible builder | M5.2–M5.4 | arch | M | b | — |

From M5.6 on every task's done-criterion uses `bob pipeline run ci`. The web UI bundle joins
the self-build at M9.5; `buf` generation moves into bob at M10.2.

## M6 — Developer environment (Track A)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M6.1 | `bob.DevShell`, `bob dev` (shell part) | R-DEV-001 | — | M5 | impl | M | a | O-02-5 |
| M6.2 | Shell hook, automatic activation, trust (`allow`/`deny`), hook cost | R-DEV-003, R-DEV-007, R-DEV-009 | hook budget | M6.1 | impl | M | — | O-02-6 |
| M6.3 | `.bob/bin` shims and `bob exec` | R-DEV-008 | shim budget | M6.1 | impl | S | b | O-02-8 |
| M6.4 | LSP path-rewriting proxy (`bob exec --lsp`) | R-DEV-010 | LSP budget | M6.3 | arch | M | — | O-02-2 |
| M6.5 | Editor settings (`bob editor`) | R-DEV-011 | settings goldens | M6.3 | impl | S | c | O-02-3, O-02-4 |
| M6.6 | Shell completion incl. labels | R-CLI-004 | completion budget | M5 | impl | S | a | — |
| M6.7 | Tree preparation (`DevPreparer`), dev-server framework, developer-visible budgets | R-DEV-002, R-DEV-006 | `bob dev -- true` budget | M6.1 | impl | M | c | — |

## M7 — C/C++, platforms, cross-compilation, variants (Track A)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M7.1 | `pkg/cc`: hermetic libraries, binaries, tests; typed options → exact flags; Go cgo with `cc.Lib` | R-CC-001a, R-CC-002a | C/C++ budgets | M5 | arch | M | a | O-03-2 |
| M7.2 | `compile_commands.json` | R-CC-003a | golden | M7.1 | impl | S | — | — |
| M7.3 | `cc.External` (pinned, verified sources) | R-CC-005a | — | M7.1 | impl | M | b | O-01-2 |
| M7.4 | Platforms and toolchains: supported systems, darwin stays possible, host builds/target compiles, matrix, per-package restriction, glibc/rustc pre-check, typed nixpkgs deps, toolchain selection, host units, target C libs | R-PLAT-001, R-PLAT-002, R-PLAT-004, R-PLAT-005, R-PLAT-006, R-PLAT-007, R-FW-032, R-FW-033, R-FW-035, R-FW-036 | cross fixture under qemu-user | M5 | arch | L | a | — |
| M7.5 | Variants and projections; cross sanitiser runtime; foreign-arch and sanitiser tests | R-FW-031, R-FW-038, R-FW-037, R-FW-039 | variant-sharing count | M7.4 | impl | M | — | — |
| M7.6 | Cross and closure budgets | R-PLAT-008, R-NIX-030 | size/time budgets | M7.5, M7.1 | impl | S | — | — |

## M8 — Rust (Track A)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M8.1 | Crate resolver (= Cargo) and lock facts | R-RS-002a, R-RS-005 | 4 graphs vs `cargo generate-lockfile` | M7.4 | arch | M | a | — |
| M8.2 | Per-crate drvs, build scripts, proc-macros, C interop; target std | R-RS-001a, R-FW-034 | Rust budgets | M8.1 | arch | L | — | — |
| M8.3 | Generated `Cargo.toml`/rust-analyzer support; toolchain per package | R-RS-003a, R-RS-004a | goldens | M8.1 | impl | M | b | O-03-5 |

## M9 — TypeScript / npm (Track A)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M9.1 | npm resolver (= npm, peer-correct) | R-NPM-001a | 6 graphs vs npm lock | M5 | arch | M | — | — |
| M9.2 | Isolated layout, hoisted fallback, per-instance drvs | R-NPM-003a | — | M9.1 | impl | M | a | — |
| M9.3 | Lifecycle scripts (deny, allowlist, sandbox); quirks table | R-NPM-004a, R-NPM-005a | fault: denied script (2013) | M9.1 | impl | M | a | — |
| M9.4 | `package.json`/`tsconfig.json` generation | R-NPM-008a | goldens | M9.1 | impl | S | b | — |
| M9.5 | TS, bundles, Vite, SvelteKit builds; Node/Bun runtimes; **`web/` built by bob, `svelte-check` in the `ci` pipeline** | R-NPM-007a, R-NPM-009a | npm budgets | M9.2 | impl | M | — | O-03-6 |
| M9.6 | Vite live reload and dependency restarts in `bob dev` | R-NPM-010a, R-DEV-002 (Vite part) | HMR budget | M9.5, M6.7 | impl | M | — | — |

## M10 — Code generators and materialisation (Track A)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M10.1 | Manifest-based materialisation; editors see generated code | R-FW-066, R-FW-067 | fault: gen conflict (1016) | M6.7 | arch | M | — | — |
| M10.2 | protobuf/connect for Go and TS; `proto.Breaking`; **bob's `proto/` generation moves into bob** | R-GEN-001a | codegen budgets | M10.1, M9.5 | impl | M | a | O-03-8 |
| M10.3 | GraphQL for Go and TS | R-GEN-002a | — | M10.1, M9.5 | impl | S | a | — |
| M10.4 | OpenAPI for Go and TS | R-GEN-003a | — | M10.1, M9.5 | impl | S | a | O-03-7 |
| M10.5 | Codegen early cutoff; runtime-mismatch pre-check | R-GEN-005, R-GEN-006 | whitespace-change budget | M10.2 | impl | S | — | — |

## M11 — OCaml (Track A)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M11.1 | opam resolver | R-ML-002 | 8 graphs vs opam | M5 | arch | M | — | — |
| M11.2 | Per-opam-package drvs, own code without dune, ocaml-lsp | R-ML-001a, R-ML-001b | OCaml budgets | M11.1, M7.1 | impl | M | — | O-04-5 |

## M12 — Outputs and releases as built objects (Track A)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M12.1 | Closures as outputs, bundles as targets, per-output pruning, licence manifest | R-OUT-001, R-OUT-006, R-OUT-007, R-OUT-008 | — | M7 | impl | M | — | — |
| M12.2 | OCI images: layers, reproducibility, store layout, customisation layer | R-OUT-002, R-OUT-009, R-OUT-010, R-OUT-011, R-OUT-012 | crane validate, rootless podman run | M12.1 | impl | M | a | O-04-4 |
| M12.3 | AppImage: format, runtime, launcher modes, data paths, hosts, fidelity warnings, graphics | R-OUT-003, R-OUT-015, R-OUT-016, R-OUT-017, R-OUT-018, R-OUT-019, R-OUT-020 | distro matrix | M12.1 | arch | L | a | O-04-2, O-04-3 |
| M12.4 | Tarballs | R-OUT-004 | — | M12.1 | impl | S | a | — |
| M12.5 | Release definition, `bob.release/v1` manifest, profile semantics | R-OUT-022, R-OUT-023, R-INST-005 | manifest golden | M12.2–M12.4 | impl | M | — | O-04-1 |
| M12.6 | Output budgets (time, size) | R-OUT-024, R-OUT-025 | budgets | M12.5 | impl | S | — | — |

## M13 — Server storage, fleet and workers (Track B)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M13.1 | `internal/store` primitives (SeqLog, PagedIndex, ShardSet, Lease), GET-only hot paths, LIST only in `sweep`, dynamitedb rules, key segments, panic safety | R-STORE-100, R-STORE-101, R-STORE-103, R-STORE-104, R-STORE-106, R-STORE-005 | counting proxy: 0 LIST | M5 | arch | L | — | O-05-1 |
| M13.2 | Backend conformance probe, bucket split, credentials, shipped policies, policy self-check, no S3 minting | R-STORE-102, R-STORE-002, R-SEC-002, R-SEC-100, R-SEC-102 | S11 policy matrix (≥ 40 ops) | M13.1 | impl | M | a | — |
| M13.3 | Key schemas and records, lists without scans, staging cleanup | R-STORE-004, R-STORE-105 | — | M13.1 | impl | M | a | — |
| M13.4 | Worker registration, sealed tokens, no impersonation, compromised-host runbook | R-FLEET-001, R-FLEET-002, R-SEC-101 | registration budget | M13.2 | arch | M | — | O-05-3 |
| M13.5 | Jobs: claims, leases, requeue, queue liveness, dispatch with wake-ups, request budgets | R-FLEET-003, R-FLEET-011, R-FLEET-012, R-FLEET-009, R-FLEET-010 | chaos: 0 double claims, 0 lost jobs | M13.3, M13.4 | arch | L | — | — |
| M13.6 | Scheduling unit (closure job, batch split), platform/caps/labels, concurrent jobs per worker | R-FLEET-005, R-FLEET-004, R-FLEET-013 | batch-build budget | M13.5 | impl | M | b | — |
| M13.7 | Worker isolation: root worker, job container, Nix as root with non-root builds, evalbox in container | R-FLEET-008, R-SEC-000 | S13 hostile suite + extra cases | M13.5, M3.5 | arch | L | b | O-05-4 |
| M13.8 | Checkout credentials only for the held job (fake adapter until M17.1) | R-FLEET-006 | — | M13.5 | impl | S | b | — |

## M14 — Logs and orchestrator HA (Track B)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M14.1 | `internal/logstore`: batched writes, lake workarounds, per-package attribution | R-LOG-001, R-LOG-100, R-LOG-003 | 50 workers × 200 lines/s, GetLog budget | M13.5 | impl | M | a | O-05-2 |
| M14.2 | Live log pull over Attach, merge and fallback, hostile worker input, worker reachability | R-LOG-002, R-LOG-101, R-LOG-102, R-SRV-100 | live-line budget | M14.1 | arch | M | — | — |
| M14.3 | Controllers and reconciliation, intent-first membership, orchestrator discovery, crash safety | R-SRV-004, R-SRV-101, R-SRV-102, R-NFR-007 | 3 orchestrators, kill -9 every 20 s | M13.5 | arch | L | a | — |
| M14.4 | Retention | R-STORE-007 | — | M14.3 | impl | S | — | — |

## M15 — API, identity, RBAC (Track B)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M15.1 | `proto/` modules, connectrpc server, CRUD conventions, lint/breaking, error model, GET-only list RPCs | R-API-001, R-API-002, R-API-003, R-API-150, R-API-151 | buf breaking | M13.3 | impl | M | — | — |
| M15.2 | Identity: OIDC + local users, service accounts, server-issued tokens, credential hygiene | R-AUTH-001, R-AUTH-005, R-AUTH-007, R-AUTH-151 | — | M15.1 | impl | M | a | — |
| M15.3 | OAuth flows for the CLI (code + PKCE, device, refresh, revoke) | R-AUTH-150 | — | M15.2 | impl | M | — | — |
| M15.4 | RBAC: RPC × project permissions, only `admin` built in, claim → role mappings, every RPC has a rule, no escalation | R-AUTH-002, R-AUTH-003, R-AUTH-004, R-AUTH-006, R-AUTH-152 | RBAC matrix | M15.1 | arch | M | a | — |

## M16 — Cache, registry, releases, remote builds, install (Track B, joins Track A at M12)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M16.1 | Nix binary cache, visibility documents, streaming download proxy, download-token scope | R-REG-001, R-REG-150, R-REG-151, R-REG-154 | real `nix` client conformance | M15.4, M13.3 | impl | M | a | O-06-1, O-06-5 |
| M16.2 | Promotion: verify, sign (Go = `nix store sign`), crash-safe concurrent promotion, only bob-built outputs, key rotation | R-REG-002, R-REG-152, R-REG-005, R-REG-153 | promotion chaos | M16.1, M14.3 | arch | M | — | — |
| M16.3 | OCI pull registry; images in releases and the registry | R-REG-003, R-OUT-013 | real `podman` pull | M16.2, M12.2 | impl | M | b | — |
| M16.4 | Releases on the server; releases never collected | R-REG-004, R-STORE-150 | — | M16.2, M12.5 | impl | M | b | — |
| M16.5 | `bob login`, cache setup after login, credential pre-check, `bob build --remote` | R-CLI-003, R-DEV-004, R-NIX-029, R-DEV-005 | — | M15.3, M16.1, M14.2 | impl | M | b | O-02-1 |
| M16.6 | `bob install` / upgrade / rollback / uninstall / list; no system Nix; public vs private; several registries; no dev state | R-INST-001, R-INST-002, R-INST-003, R-INST-004, R-INST-006 | installed-shim budget | M16.4, M12.5 | impl | M | — | O-02-7 |
| M16.7 | Cache throughput budgets | R-NFR-150 | §6.11 budgets | M16.2 | impl | S | — | — |

## M17 — Pipelines and forges (Track B)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M17.1 | `forge.Adapter`; Forgejo (live), GitLab (live), GitHub (SDK + recorded payloads); webhook security; adapter testing | R-PIPE-001, R-PIPE-007, R-PIPE-008 | recorded payload replays | M15.4 | impl | L | a | O-06-4 |
| M17.2 | One repository per project; pipelines run as defined and report back; triggers by API, UI, `--remote` | R-PIPE-002, R-PIPE-003, R-PIPE-004 | webhook → BuildRecord budget | M17.1, M16.5 | impl | M | — | — |
| M17.3 | Cancel, retry, re-run, concurrency; remote source snapshots | R-PIPE-005, R-PIPE-009 | — | M17.2 | impl | M | — | O-06-2 |
| M17.4 | Outgoing webhooks | R-PIPE-006 | — | M17.2 | impl | S | a | — |

## M18 — Web UI (polish last)

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M18.1 | Static SvelteKit app served by Go, public API only | R-UI-001, R-API-004 | — | M15, M9.5, M10.2 | impl | M | — | — |
| M18.2 | Tokens, hand-written Tailwind components, light default + dark, visual style | R-UI-003, R-UI-004, R-UI-005 | screenshot goldens (both themes) | M18.1 | impl | M | — | — |
| M18.3 | Every page; filterable lists with linked ids | R-UI-002, R-UI-006 | screenshot goldens per page | M18.2, M16, M17 | impl | L | — | — |
| M18.4 | First-load budget | R-UI-007 | JS/CSS/FCP budget | M18.3 | impl | S | — | — |

## M19 — Hardening and v1

| Id | Task | Covers | Extra tests | Deps | Role | Size | ∥ | Open first |
|---|---|---|---|---|---|---|---|---|
| M19.1 | Example programs (`examples/`) for every ecosystem and output | R-FW-013 | examples build in CI | M12, M11 | impl | M | a | — |
| M19.2 | Scale target and full benchmark run (slow command) | R-NFR-005 | all budgets on both profiles | M17 | impl | M | a | — |
| M19.3 | Security review (fresh reviewer), manual checks (VS Code, JetBrains, GitHub live), `ATTRIBUTION.md`, release process for the first public release | — | — | M18 | reviewer | M | — | O-02-3, O-02-4, O-06-4 |

## Not placeable as requirements

- R-NFR-001/002 (core budgets) and R-NFR-100…109 (fleet budgets) have no `####` entry in SPEC;
  their rows are in Appendix B and are covered by the budgets tasks named above (M1.9, M3.4,
  M4.4, M5.2, M13.5, M14.2).
- The all-in-one local dev mode (BOOTSTRAP §6.13) has no SPEC entry; M1.6 builds it per HARNESS.md.
- R-ARCH-003 package names were aligned with §03/§04 in Stage 2 (spec/01-core.md layer table).

## Coverage

Count check (2026-10-21): the SPEC id set is every `R-…` id on a `#### R-` heading line
(`grep -o '^#### R-[A-Z]*-[0-9]*[a-z]*' SPEC.md` gives 250 headings; two headings carry two ids,
`R-FW-012 / R-FW-014` and `R-FLEET-008 / R-SEC-000`, so 252 unique ids). A script read the
"Covers" column of every task row above: **252 of 252 ids covered, 0 missing, 0 ids not in SPEC**;
110 tasks in 20 milestones (M0–M19). Re-run this check whenever SPEC or this plan changes.

| R-id | Task(s) |
|---|---|
| R-API-001 | M15.1 |
| R-API-002 | M15.1 |
| R-API-003 | M15.1 |
| R-API-004 | M18.1 |
| R-API-150 | M15.1 |
| R-API-151 | M15.1 |
| R-ARCH-001 | M0.1 |
| R-ARCH-002 | M0.2 |
| R-ARCH-003 | M0.2 |
| R-ARCH-004 | M0.1 |
| R-ARCH-005 | M5.6 |
| R-AUTH-001 | M15.2 |
| R-AUTH-002 | M15.4 |
| R-AUTH-003 | M15.4 |
| R-AUTH-004 | M15.4 |
| R-AUTH-005 | M15.2 |
| R-AUTH-006 | M15.4 |
| R-AUTH-007 | M15.2 |
| R-AUTH-150 | M15.3 |
| R-AUTH-151 | M15.2 |
| R-AUTH-152 | M15.4 |
| R-CC-001a | M7.1 |
| R-CC-002a | M7.1 |
| R-CC-003a | M7.2 |
| R-CC-005a | M7.3 |
| R-CLI-001 | M4.6 |
| R-CLI-002 | M4.6 |
| R-CLI-003 | M16.5 |
| R-CLI-004 | M6.6 |
| R-CLI-005 | M4.6 |
| R-CLI-006 | M4.6 |
| R-CLI-007 | M4.6 |
| R-CLI-008 | M4.6 |
| R-CLI-009 | M4.6 |
| R-CLI-010 | M4.6 |
| R-CLI-011 | M4.6 |
| R-CLI-012 | M5.6 |
| R-DEV-001 | M6.1 |
| R-DEV-002 | M6.7, M9.6 |
| R-DEV-003 | M6.2 |
| R-DEV-004 | M16.5 |
| R-DEV-005 | M16.5 |
| R-DEV-006 | M6.7 |
| R-DEV-007 | M6.2 |
| R-DEV-008 | M6.3 |
| R-DEV-009 | M6.2 |
| R-DEV-010 | M6.4 |
| R-DEV-011 | M6.5 |
| R-FLEET-001 | M13.4 |
| R-FLEET-002 | M13.4 |
| R-FLEET-003 | M13.5 |
| R-FLEET-004 | M13.6 |
| R-FLEET-005 | M13.6 |
| R-FLEET-006 | M13.8 |
| R-FLEET-008 | M13.7 |
| R-FLEET-009 | M13.5 |
| R-FLEET-010 | M13.5 |
| R-FLEET-011 | M13.5 |
| R-FLEET-012 | M13.5 |
| R-FLEET-013 | M13.6 |
| R-FW-001 | M3.2 |
| R-FW-002 | M3.4 |
| R-FW-004 | M3.6 |
| R-FW-005 | M3.6 |
| R-FW-008 | M3.4 |
| R-FW-009 | M4.6 |
| R-FW-010 | M3.2 |
| R-FW-011 | M3.2 |
| R-FW-012 | M3.2 |
| R-FW-013 | M19.1 |
| R-FW-014 | M3.2 |
| R-FW-015 | M3.1 |
| R-FW-016 | M3.1 |
| R-FW-020 | M4.4 |
| R-FW-021 | M2.5 |
| R-FW-022 | M5.5 |
| R-FW-023 | M3.2 |
| R-FW-024 | M3.2 |
| R-FW-025 | M4.5 |
| R-FW-030 | M3.7 |
| R-FW-031 | M7.5 |
| R-FW-032 | M7.4 |
| R-FW-033 | M7.4 |
| R-FW-034 | M8.2 |
| R-FW-035 | M7.4 |
| R-FW-036 | M7.4 |
| R-FW-037 | M7.5 |
| R-FW-038 | M7.5 |
| R-FW-039 | M7.5 |
| R-FW-043 | M3.4 |
| R-FW-044 | M3.4 |
| R-FW-045 | M3.5 |
| R-FW-046 | M3.3 |
| R-FW-047 | M3.3 |
| R-FW-048 | M3.6 |
| R-FW-049 | M4.6 |
| R-FW-050 | M3.7 |
| R-FW-051 | M3.3 |
| R-FW-060 | M5.1 |
| R-FW-061 | M5.1 |
| R-FW-062 | M5.1 |
| R-FW-063 | M2.4 |
| R-FW-064 | M4.5 |
| R-FW-065 | M3.6 |
| R-FW-066 | M10.1 |
| R-FW-067 | M10.1 |
| R-GEN-001a | M10.2 |
| R-GEN-002a | M10.3 |
| R-GEN-003a | M10.4 |
| R-GEN-005 | M10.5 |
| R-GEN-006 | M10.5 |
| R-GO-001a | M5.2 |
| R-GO-002a | M5.2 |
| R-GO-002b | M5.3 |
| R-GO-003a | M5.4 |
| R-GO-004a | M5.4 |
| R-GO-005 | M5.2 |
| R-INST-001 | M16.6 |
| R-INST-002 | M16.6 |
| R-INST-003 | M16.6 |
| R-INST-004 | M16.6 |
| R-INST-005 | M12.5 |
| R-INST-006 | M16.6 |
| R-LOG-001 | M14.1 |
| R-LOG-002 | M14.2 |
| R-LOG-003 | M14.1 |
| R-LOG-100 | M14.1 |
| R-LOG-101 | M14.2 |
| R-LOG-102 | M14.2 |
| R-ML-001a | M11.2 |
| R-ML-001b | M11.2 |
| R-ML-002 | M11.1 |
| R-NFR-005 | M19.2 |
| R-NFR-007 | M14.3 |
| R-NFR-150 | M16.7 |
| R-NIX-001 | M2.1 |
| R-NIX-002 | M4.3 |
| R-NIX-003 | M2.1 |
| R-NIX-005 | M3.6 |
| R-NIX-006 | M2.1 |
| R-NIX-007 | M2.3 |
| R-NIX-008 | M2.4 |
| R-NIX-009 | M2.5 |
| R-NIX-010 | M4.2 |
| R-NIX-011 | M4.3 |
| R-NIX-012 | M4.2 |
| R-NIX-013 | M4.3 |
| R-NIX-014 | M2.4 |
| R-NIX-015 | M4.1 |
| R-NIX-016 | M4.1 |
| R-NIX-017 | M4.1 |
| R-NIX-018 | M4.1 |
| R-NIX-019 | M4.4 |
| R-NIX-020 | M2.2 |
| R-NIX-021 | M2.2 |
| R-NIX-022 | M2.2 |
| R-NIX-024 | M2.2 |
| R-NIX-025 | M2.2 |
| R-NIX-026 | M2.2 |
| R-NIX-027 | M4.3 |
| R-NIX-028 | M0.7 |
| R-NIX-029 | M16.5 |
| R-NIX-030 | M7.6 |
| R-NPM-001a | M9.1 |
| R-NPM-003a | M9.2 |
| R-NPM-004a | M9.3 |
| R-NPM-005a | M9.3 |
| R-NPM-007a | M9.5 |
| R-NPM-008a | M9.4 |
| R-NPM-009a | M9.5 |
| R-NPM-010a | M9.6 |
| R-OUT-001 | M12.1 |
| R-OUT-002 | M12.2 |
| R-OUT-003 | M12.3 |
| R-OUT-004 | M12.4 |
| R-OUT-006 | M12.1 |
| R-OUT-007 | M12.1 |
| R-OUT-008 | M12.1 |
| R-OUT-009 | M12.2 |
| R-OUT-010 | M12.2 |
| R-OUT-011 | M12.2 |
| R-OUT-012 | M12.2 |
| R-OUT-013 | M16.3 |
| R-OUT-015 | M12.3 |
| R-OUT-016 | M12.3 |
| R-OUT-017 | M12.3 |
| R-OUT-018 | M12.3 |
| R-OUT-019 | M12.3 |
| R-OUT-020 | M12.3 |
| R-OUT-022 | M12.5 |
| R-OUT-023 | M12.5 |
| R-OUT-024 | M12.6 |
| R-OUT-025 | M12.6 |
| R-PIPE-001 | M17.1 |
| R-PIPE-002 | M17.2 |
| R-PIPE-003 | M17.2 |
| R-PIPE-004 | M17.2 |
| R-PIPE-005 | M17.3 |
| R-PIPE-006 | M17.4 |
| R-PIPE-007 | M17.1 |
| R-PIPE-008 | M17.1 |
| R-PIPE-009 | M17.3 |
| R-PLAT-001 | M7.4 |
| R-PLAT-002 | M7.4 |
| R-PLAT-004 | M7.4 |
| R-PLAT-005 | M7.4 |
| R-PLAT-006 | M7.4 |
| R-PLAT-007 | M7.4 |
| R-PLAT-008 | M7.6 |
| R-REG-001 | M16.1 |
| R-REG-002 | M16.2 |
| R-REG-003 | M16.3 |
| R-REG-004 | M16.4 |
| R-REG-005 | M16.2 |
| R-REG-150 | M16.1 |
| R-REG-151 | M16.1 |
| R-REG-152 | M16.2 |
| R-REG-153 | M16.2 |
| R-REG-154 | M16.1 |
| R-RS-001a | M8.2 |
| R-RS-002a | M8.1 |
| R-RS-003a | M8.3 |
| R-RS-004a | M8.3 |
| R-RS-005 | M8.1 |
| R-SEC-000 | M13.7 |
| R-SEC-002 | M13.2 |
| R-SEC-100 | M13.2 |
| R-SEC-101 | M13.4 |
| R-SEC-102 | M13.2 |
| R-SRV-004 | M14.3 |
| R-SRV-100 | M14.2 |
| R-SRV-101 | M14.3 |
| R-SRV-102 | M14.3 |
| R-STORE-002 | M13.2 |
| R-STORE-004 | M13.3 |
| R-STORE-005 | M13.1 |
| R-STORE-007 | M14.4 |
| R-STORE-100 | M13.1 |
| R-STORE-101 | M13.1 |
| R-STORE-102 | M13.2 |
| R-STORE-103 | M13.1 |
| R-STORE-104 | M13.1 |
| R-STORE-105 | M13.3 |
| R-STORE-106 | M13.1 |
| R-STORE-150 | M16.4 |
| R-UI-001 | M18.1 |
| R-UI-002 | M18.3 |
| R-UI-003 | M18.2 |
| R-UI-004 | M18.2 |
| R-UI-005 | M18.2 |
| R-UI-006 | M18.3 |
| R-UI-007 | M18.4 |
