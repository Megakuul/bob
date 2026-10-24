# BOOTSTRAP — instructions for the agents building bob

> **You are reading a bootstrapper, not the final AGENTS.md.**
> bob is built as a **three-stage cleanroom implementation**:
>
> | Stage | Who | Output |
> |---|---|---|
> | **1. Requirements** (done) | owner + agent, in chat | `REQUIREMENTS.md` (what & why) and this file (how to work) |
> | **2. Refinement** | agent, autonomous | spikes, ADRs, **exact** `SPEC.md` (what *and* how, testable), `HARNESS.md`, execution plan |
> | **3. Build** | agent, autonomous | harness first, then the product, against the locked spec |
>
> At the end of Stage 2 you rewrite this file into the permanent `AGENTS.md` (keep §0, §2, §5–§8;
> replace §3/§4 with the Stage 3 loop; summarise §1).
> Owner: the human you work for. Project: `github.com/megakuul/bob`, AGPL-3.0.

---

## 0. Start every session here

1. Read `plan/STATUS.md`. If missing, you are at the start of Stage 2: create `plan/STATUS.md`
   (stage, phase, current task, open questions, blockers, last gate passed with date and the
   owner's words).
2. Read `REQUIREMENTS.md` (Stage 2) or `SPEC.md` (Stage 3). Requirement ids are binding.
3. Do the next task. Do not skip phases or start a phase whose gate is not recorded as passed.
4. Update `plan/STATUS.md` at the end of every work arc.

### Talking to the owner

- **Questions are asked in the chat, never only inside a document.** Documents may mirror them
  (an "open questions" list in STATUS.md), and must record the owner's answer verbatim with date.
- **Ask at the start of a stage**, in one batch (≤ 8 per message, more messages if needed),
  everything you can foresee. Then work autonomously.
- **Answer side questions yourself** using §2 (Decision principles) and record the decision and
  reasoning in `plan/decisions.md` so the owner can review and overturn it.
- **Only these go back to the owner mid-stage:** the gates below, decisions listed as
  "owner decision" in §3/§4, anything that would contradict or drop a requirement, licences
  (GPL/AGPL/LGPL dependencies or restrictive data), spending money, publishing anything.

---

## 1. Product summary

See `REQUIREMENTS.md` §1. In one line: a typed Go framework that makes Nix the invisible engine of
a company's whole build pipeline, plus a separate HA build server (S3-only storage) that runs
pipelines on a worker fleet and hosts the Nix cache, OCI images and releases, with a web UI.

---

## 2. Decision principles (use these to self-answer)

In priority order; when two conflict, the earlier one wins.

1. **Never lose data, never crash.** Correctness of builds, cache contents and server state beats
   everything.
2. **Nix stays invisible.** If an option leaks Nix concepts, syntax or raw errors to users, it is
   wrong. A Nix evaluation error reaching a user is a bob bug.
3. **Developer experience over implementation convenience.** Typed, discoverable, gopls-friendly,
   one obvious way. If a developer has to understand Nix, Cargo, CMake or npm to use bob, the
   design failed.
4. **Fine-grained and cacheable.** Prefer designs that make packages smaller and cache hits more
   likely, as long as evaluation stays within budget.
5. **Security boundaries are structural**, not procedural: enforce with credentials, bucket
   splits, sandboxes and types, not with "the code doesn't do that".
6. **Operational simplicity:** S3 is the only dependency; `bob` is the only executable; zero-setup
   workers; no coordinator. Do not introduce a database, broker or daemon.
7. **Use what exists now** (dynamitedb, lake, nixpkgs, Go toolchain features) as it is today; no
   designs that depend on future versions of anything.
8. **Simple, boring code.** Fewer abstractions, fewer dependencies, explicit over clever.
9. **Measure, don't assume.** Any capability claim about a library, protocol or tool must be
   backed by a test, measurement, documentation or source link — never memory.

---

## 3. Stage 2 — Refinement

### Phase 2.0 — Kickoff (questions first)
Read REQUIREMENTS.md fully, list every ambiguity, and ask the owner in one batched round
(include: ecosystems to strike from §7.7, if the owner has not done so yet).

### Phase 2.1 — Boxed foundation spikes (exit: **Gate 0**, owner decision)
Only these two spikes until Gate 0. Work only in `spikes/gen/`, `spikes/rootless/`, `docs/adr/`,
`docs/spikes/`, `docs/environment.md`. No product code.

**S0a — Generation strategy A′ vs B** (R-NIX-001).
- Same test graph for both: a C++ shared library using nixpkgs `openssl`; a Rust staticlib with
  two crates.io deps; a Go binary (cgo) linking both, with two third-party Go modules; one
  protobuf → Go codegen step.
- Measure: eval+build cold/warm; eval time for synthetic graphs of 10/100/1,000/10,000 packages;
  lines of bob code needed; behaviour after bumping nixpkgs by one release; cache compatibility.
- Fault injection: unknown nixpkgs attribute, wrong hash, missing link flag, compile error,
  dependency cycle, broken/insecure package. For each: does any raw Nix text reach the user?
  What would the bob-formatted error be?
- ADR 0001 with comparison table, measurements, recommendation.

**S0b — Rootless embedded Nix** (R-NIX-020…025).
- Provide `/nix/store` via unprivileged user namespaces from a single static binary; Nix vs Lix
  (embeddable static build, needed features, error quality, licence vs AGPL-3.0).
- Distro matrix: which mainstream distros allow it by default, which need an admin fix, the exact
  fix text per distro (R-NIX-022). Verify, don't recall.
- Substituting from cache.nixos.org and from a test binary cache with auth, inside the namespace.
- ADR 0002.

**Gate 0:** owner reads ADR 0001/0002 and decides A′ vs B (and Nix vs Lix). The ADRs become
**Accepted (locked)**.

### Phase 2.2 — Remaining spikes (autonomous; owner decisions only where marked)
All build on the locked Gate 0 decisions. One ADR per decision. Spikes are throwaway.

| Spike | Question | Owner decision? |
|---|---|---|
| S1 | `bob.go` next to code: package-name collisions, build tags, gopls behaviour; else fallback `bob/` tree (R-FW-002) | yes, if fallback |
| S2 | Go ecosystem: per-module vs per-package (`GOCACHEPROG`) builds, generated go.mod/go.sum, drift detection | no |
| S3 | Rust: per-crate derivations, features, build scripts, generated Cargo files, linking with cgo | no |
| S4 | npm: own resolver (semver, peer/optional/platform deps), isolated layout, lifecycle allowlist, quirks table, Vite HMR under `bob dev`, Bun runtime | no |
| S5 | OCaml/opam mapping | no |
| S6 | Codegen (buf/connect, gqlgen/graphql-codegen, oapi-codegen) + materialisation into the tree | no |
| S7 | Cross-compilation amd64↔arm64 for C/C++/Go/Rust; variants (debug/release, features, sanitisers) and their effect on cache keys | no |
| S8 | OCI multi-arch images and AppImages from closures; AppImage on plain Ubuntu/Fedora/Debian (glibc, GL/Vulkan, certs, locale) | no |
| S9 | dynamitedb as reconciliation store: CAS job claim, leases, requeue, access patterns for every UI list, polling cost with 100 workers; panic wrapping | no |
| S10 | lake for logs: periodic small Parquet writes + compaction, query by build/package, volume | no |
| S11 | Worker registration (key pair, encrypted token in fleet bucket), S3 policies on RustFS + SeaweedFS, proof that worker creds fail on control bucket | no |
| S12 | Nix binary cache serving + signing + promotion from `staging/`; OCI distribution pull API; auth for both | no |
| S13 | Sandbox for `bob.go` evaluation on workers; loopback services inside the Nix sandbox (R-FW-025) | no |
| S14 | Scheduling unit: whole closure per job vs per-derivation across the fleet (R-FLEET-005) | yes |
| S15 | Error translation: structured Nix logs → bob errors with Go source positions | no |
| S16 | Forges: webhook payloads, status APIs, short-lived checkout tokens for GitHub, Forgejo, GitLab | no |
| S17 | Optional orchestrator peering for live logs without breaking "no coordinator" | no |

### Phase 2.3 — SPEC.md
- Every requirement becomes numbered `R-…` entries with **testable acceptance criteria**
  (Given/When/Then or measurable thresholds), each verifiable by the harness unless marked
  `(manual)`.
- **Exact** where it matters: public API surface of `pkg/` (types, constructors, examples per
  R-FW-013), CLI commands and output formats, `bob.lock` format, generated file formats, proto
  definitions (full `proto/` draft), data model per bucket (keys, access patterns), state machines
  (build, job, worker, release), RBAC permission list (every RPC), UI pages with wireframes and
  design tokens.
- Performance budgets for R-NFR-001…007 as concrete numbers, marked **[proposal]**, based on
  spike measurements.
- `docs/out-of-scope.md` from REQUIREMENTS §23.

### Phase 2.4 — Harness design & plan
- `HARNESS.md`: how §6 is implemented concretely (commands, directories, fixtures, budgets file,
  fault catalogue).
- `plan/architecture.md` from the ADRs; `plan/execution-plan.md`: milestones → tasks, each naming
  requirement ids and the tests it must turn green; ordered by dependency and risk (Nix backend,
  error translation, rootless store and Go ecosystem first; UI polish last; bob builds itself as
  early as R-ARCH-005 requires).
- `ROADMAP.md` (committed) with honest status.
- `.claude/agents/` from §7.

**Gate S (end of Stage 2):** owner approves SPEC.md (incl. budgets), the ADRs, HARNESS.md and the
plan. Then this file is rewritten into `AGENTS.md`.

---

## 4. Stage 3 — Build

### Phase 3.0 — Kickoff
One batched question round for anything the plan left open. Then autonomous.

### Phase 3.1 — Harness first (exit: **Gate H**)
Build the workspace skeleton and the **complete harness (§6) before feature code**. Features are
stubs returning "not implemented" errors; their acceptance tests start red. Prove the harness
catches bugs (§6.10).
**Gate H:** owner reviews fixtures, goldens, budgets and the harness-validation report. After
Gate H, tests are protected (§5.2).

### Phase 3.2 — Implementation loop
1. Pick the next task from `plan/execution-plan.md`.
2. Implement until its tests are green and the CI command passes.
3. For UI work: render headless, look at the screenshot, compare with SPEC wireframes and tokens.
4. Code-steward review (§7) for every finished milestone.
5. Commit with the task id (`M2.3: go module fetcher`). Update STATUS.md and ROADMAP.md.
6. Repeat. Stop only for owner decisions (§0).

---

## 5. Guardrails (non-negotiable)

### 5.1 Locked decisions
- ADRs marked **Accepted (locked)** and the owner decision log (REQUIREMENTS §24) are binding.
  Never switch, replace or add a parallel alternative on your own, not even temporarily.
- If you hit a real limit: write a change proposal ADR (status Proposed) with the measured problem,
  options and costs, ask the owner, continue with unblocked work.

### 5.2 Tests are protected (after Gate H)
- Never weaken, delete, skip, loosen tolerances of, or re-bless tests, goldens, fixtures, budgets
  or the parity floor to make work pass. If a test seems wrong: stop, explain with evidence in
  STATUS.md, ask the owner.
- Protected paths are hashed; the CI command reports every change to them.

### 5.3 Never crash, never lose data (Go)
- No `panic`, `log.Fatal`, `os.Exit` outside `main`; enforced by linters. Errors are returned and
  wrapped (`%w`); sentinel errors for conditions callers act on.
- `recover` at every goroutine and RPC boundary, and around every dynamitedb call (it panics on
  misuse, R-STORE-005), converting to errors.
- Every state change in the server is a conditional write; every crash fix lands with a
  regression test; chaos tests kill orchestrators and workers at arbitrary points.
- `context.Context` on everything that does I/O; no goroutine leaks (tested).
- No mutable global state except explicitly designed memoisation (R-FW-011).

### 5.4 Hostile input
Webhooks, API params, `bob.go` evaluation output, registry metadata (npm, crates, Go modules,
opam), uploaded NARs and OCI blobs, lock files: bounds-checked, size-capped, validated, never
trusted.

### 5.5 Nix stays invisible
- Any test or example that shows raw Nix error text to a user without `-v` is a failure.
- No `.nix` file may appear in a user project or in `pkg/` public API docs.

### 5.6 Framework boundary
`pkg/` never imports `internal/` or server code; checked in CI. The CLI connects them only through
the framework's generic interfaces.

### 5.7 Cleanroom, licences, assets
- **Cleanroom, applied pragmatically:** bob is implemented from its spec. Prior art
  (gobuild.nix, crane, crate2nix, opam-nix, nix-portable, Hydra, attic, …) may be **read for
  understanding**; ideas taken are cited in the ADR. By default **no code is copied**; reuse
  happens as declared dependencies. Exceptions are judged case by case: a small piece of code may
  be taken only if its licence is compatible with AGPL-3.0, it is credited in `ATTRIBUTION.md`, and
  the decision and reason are recorded in `plan/decisions.md`. [D42]
- Every dependency's licence is checked in CI. bob is AGPL-3.0; GPL/AGPL/LGPL dependencies are
  avoided unless they add substantial value and the owner approves. Nix/Lix embedding obligations
  are documented in ADR 0002.
- Every bundled asset (fonts, icons, binaries) has a row in `ATTRIBUTION.md`.

### 5.8 Honesty
Never claim a library, API or protocol supports something without verifying it. Never report a
task done unless the CI command passed. Label measurements with machine and conditions.

---

## 6. Harness requirements (designed in Stage 2, built in Phase 3.1)

1. **One CI command.** Before bob can build itself: `go run ./tools/ci` (tools pinned via Go
   `tool` directives). After R-ARCH-005: `bob pipeline run ci`. It runs fmt, vet, linters
   (incl. no-panic and import-boundary rules), tests, layering check, licence check, `buf lint` /
   `buf breaking`, `svelte-check`, goldens, acceptance suite + parity floor,
   fast budgets, protected-file report.
2. **Layering checks:** `pkg/` ↛ `internal/`; framework layers (R-ARCH-003) only import downward.
3. **Golden tests** for everything generated: the graph IR, A′ JSON or B `.drv` output, `bob.lock`,
   generated native files (`go.mod`, `Cargo.toml`, `package.json`, `tsconfig.json`,
   `compile_commands.json`). Review workflow produces an HTML diff; only the owner approves new
   goldens after Gate H.
4. **Fixture repositories** under `tests/fixtures/repos/`: one per ecosystem plus a polyglot
   monorepo (C++ + Rust + Go cgo + codegen + Svelte frontend) and a cross-repo pair. Each is built
   end-to-end for both platforms (cross where native is unavailable), in all variants.
5. **Error-quality suite:** a fault catalogue (bad attribute, wrong hash, compile error, link
   error, cycle, drift in go.mod, denied lifecycle script, …). Each fault must produce the
   expected bob error with the right Go source position and **no raw Nix text**.
6. **Determinism & reproducibility:** evaluate twice → identical graph; build twice on different
   workers → bit-identical outputs (or a reported deviation).
7. **Rootless matrix:** the store works on the supported distros; on distros that block user
   namespaces, bob refuses with the exact fix text (Stage 2 decides VMs vs containers).
8. **Server tests** against a real S3 backend with conditional writes (RustFS or SeaweedFS):
   multi-orchestrator races, kill -9 chaos on orchestrators and workers, lease expiry and
   requeue, worker credentials refused on the control bucket, **every RPC has a permission
   mapping**, RBAC matrix tests, recorded webhook payloads for GitHub/Forgejo/GitLab, cache and OCI
   protocol conformance with real `nix` and `podman` clients.
9. **UI tests:** headless screenshots of every page in light and dark theme compared to approved
   goldens; agents verify via API state first, screenshots second.
10. **Harness self-validation:** deliberate faults on a scratch copy (drop a dependency edge,
    leak a raw Nix error, break drift detection, skip a permission check, let a worker read the
    control bucket, lose a log flush) must each make the CI command fail.
    Results in `docs/harness-validation.md`.
11. **Parity catalogue:** every SPEC requirement id maps to its tests; a generated `docs/parity.md`
    and a pass-count floor that may only rise.
12. **Budgets** from SPEC in one budgets file; CI fails on regressions beyond a tolerance; full
    benchmarks in a separate slow command.
13. **Agent-drivable:** every CLI command has JSON output; an all-in-one local dev mode runs
    orchestrator + worker + local S3 so agents can test the server end-to-end without the owner.

---

## 7. Roles & delegation (`.claude/agents/`, created in Phase 2.4)

| Role | Model | Job |
|---|---|---|
| **architect** | most capable | ADRs, Nix backend, ecosystem mappings, reconciliation design, reviewing risky diffs |
| **implementer** | capable | tasks against existing tests |
| **reviewer** | capable, fresh context | reviews finished milestones against SPEC without having written them |
| **code steward** | capable, fresh context | **mandates clean code**: removes dead code, needless abstraction, duplication; enforces Go idiom and §8 Svelte/Tailwind rules; may block a milestone until fixed |
| **explorer / test-runner** | fast, cheap | code search, running CI, summarising failures, docs updates |

Log in `plan/usage.md` which role handled which task. Parallel agents own disjoint packages,
use separate build caches, and merge only with a green CI command.

---

## 8. Code style rules (guardrails in spirit, upheld by the code steward; not CI-enforced) [D45]

**Go:** standard layout, small packages, no frameworks for the server beyond connectrpc and the
standard library where reasonable, no reflection-heavy magic in bob's own code, table-driven tests,
comments explain *why*.

**Svelte / Tailwind (web/):**
- Svelte 5 runes; hand-written components only; no component libraries. [D21]
- Classes are written literally and kept short; **no class-composition helpers** (`clsx`,
  `cva`, `tailwind-merge` and similar are banned); no `@apply` piles. A repeated class set
  becomes a component, not a helper.
- Design tokens (colours, spacing, radius, fonts) defined once in the Tailwind theme; components
  use tokens only. Light theme default, dark optional. [D35]
- Avoid (the point is "must not look AI-made", judge in that spirit): gradients, `rounded-full` on containers and buttons, `rounded-*`
  larger than `rounded-xs`, glow or heavy shadows, `backdrop-blur`, emoji in UI text.
- Inline styles only for dynamic values.

---

## 9. Files this process produces

```
REQUIREMENTS.md          Stage 1 (this round), owner-approved input
BOOTSTRAP.md             this file → becomes AGENTS.md at Gate S
SPEC.md                  Stage 2, exact spec
HARNESS.md               Stage 2, harness design
ROADMAP.md               Stage 2+, committed status
docs/adr/NNNN-*.md       decisions (locked after approval)
docs/spikes/*.md         spike results with measurements
docs/out-of-scope.md
docs/harness-validation.md
plan/STATUS.md           current state, gates, open questions
plan/decisions.md        side questions answered by the agent (owner may overturn)
plan/architecture.md
plan/execution-plan.md
plan/usage.md
spikes/                  throwaway spike code
```
