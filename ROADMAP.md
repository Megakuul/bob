# bob — Roadmap

Status as of 2026-10-21. This file is committed and kept honest: it is updated with every
finished task (BOOTSTRAP §4, Phase 3.2 step 5).

## What exists now

- **No product code.** There is no `go.mod`, no `pkg/`, `cmd/`, `internal/`, `proto/` or `web/`
  yet, and nothing can be built or installed.
- Stage 1 requirements (`REQUIREMENTS.md`, owner decisions D1–D64).
- Stage 2 results: 17 spikes plus the two foundation spikes (throwaway code in `spikes/`,
  write-ups in `docs/spikes/`), ADRs 0001–0019 (0001 and 0002 locked at Gate 0; the rest are
  "Accepted (Gate S pending)"), the integrated `SPEC.md` (252 requirement ids, 97 proposed
  budgets, 38 open items with recommendations), `plan/architecture.md` and
  `plan/execution-plan.md`.
- Pending for Gate S: `HARNESS.md`, `.claude/agents/`, owner approval of SPEC, ADRs, harness and plan.

## Milestones

| Milestone | Scope | Status |
|---|---|---|
| M0 | Workspace skeleton, CI command `go run ./tools/ci` | not started |
| M1 | Complete harness, red acceptance tests → **Gate H** | not started |
| M2 | Rootless embedded Nix store, derivation layer | not started |
| M3 | Core API, graph IR, sandboxed evaluation, `bob.lock` | not started |
| M4 | bob-builder, error translation, build pipeline, CLI core | not started |
| M5 | Go ecosystem; **bob builds itself**, CI becomes `bob pipeline run ci` | not started |
| M6 | Developer environment (`bob dev`, hook, shims, LSP proxy, editors) | not started |
| M7 | C/C++, platforms, cross-compilation, variants | not started |
| M8 | Rust | not started |
| M9 | TypeScript / npm (Vite, SvelteKit); web UI built by bob | not started |
| M10 | Code generators and materialisation | not started |
| M11 | OCaml | not started |
| M12 | Outputs: OCI images, AppImages, tarballs, releases | not started |
| M13 | Server storage, fleet, worker isolation | not started |
| M14 | Logs and orchestrator HA | not started |
| M15 | API, identity, RBAC | not started |
| M16 | Cache, OCI registry, promotion, releases, `--remote`, `bob install` | not started |
| M17 | Pipelines and forges (GitHub, Forgejo, GitLab) | not started |
| M18 | Web UI | not started |
| M19 | Hardening, examples, first release | not started |

M6–M12 (framework) and M13–M17 (server) run as two parallel tracks after M5.

## Known risks

- **Rootless Nix on hostile hosts.** Distros that block user namespaces get a refusal, not a
  workaround. The Ubuntu 24.04 AppArmor profile line is unverified (O-01-5).
- **Error translation.** No raw Nix text may leak. The fault catalogue covers 48 faults on two
  Nix versions (S15), but new Nix releases can change messages.
- **Upstream storage components.** lake HEAD has known data-loss, hang and duplicate bugs
  (L1, L3, L4), which bob works around (O-05-2). dynamitedb and lake are used at HEAD, not
  pinned (D46). Only S3 backends with full conditional writes are supported (D63).
- **Budgets measured on a busy host.** The S9, S10, S11 and S17 numbers have no quiet-machine
  logs; they are re-measured before Gate H (O-05-5). Some 2-core budgets are unmeasured.
- **Own resolvers** (Cargo, npm, opam, Go build list) must keep matching upstream tools. They
  matched on the spike graphs only; opam matched on 6 of 8.
- **GitHub** is tested only through the SDK and recorded payloads until a manual live check
  (O-06-4).
- **Worker hardening** beyond namespaces (seccomp against nested userns) is not in v1 (O-05-4).
- **Scope.** Six ecosystems, cross-compilation, three bundle formats, an HA server and a UI is
  a large v1. The plan puts the riskiest parts first, so problems show up early.
