# AGENTS — instructions for the agents building bob

bob is a typed Go framework that makes Nix an invisible build engine (`pkg/`), plus a separate
HA build/cache server (`internal/`, `cmd/bob-server`) that uses only S3 (dynamitedb, lake, an
artifacts bucket). Project: `github.com/megakuul/bob`, AGPL-3.0. Owner: the human you work for.

It is built as a three-stage cleanroom implementation. Stages 1 (REQUIREMENTS.md) and 2
(spikes, ADRs, SPEC.md, HARNESS.md, plan) are done; **Gate S passed 2026-10-21** (D65–D69). This
file replaced BOOTSTRAP.md (kept for history).

**Precedence:** owner decisions (REQUIREMENTS.md §24) > SPEC.md > locked ADRs (`docs/adr/`) >
HARNESS.md > plan > spike write-ups.

**Stage 3 specifics decided at Gate S:** local commits per task on branch `bob-v1`, named with
the task id, never pushed without asking (D67). Protected-file changes are reported by CI, not
blocked (D68). arm64 tested natively; x86_64 via cross builds + qemu-user only; the real-distro
rootless matrix is (manual) (D66). Supported S3 backends: AWS-compatible with conditional writes;
RustFS is the test backend (D63).

---

## 0. Start every session here

1. Read `plan/STATUS.md` (stage, phase, current task, open questions, blockers, last gate).
2. Read the `SPEC.md` entries for your task (requirement ids are binding), `HARNESS.md` for how
   tests run, and `plan/execution-plan.md` for the task list.
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

## 3. Stage 3 — Build (current)

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

### 5.9 Docs stay upstream of the implementation
- The user documentation is `website/content/docs/` (HARNESS.md §17). A change to a public
  surface (CLI commands and flags, the `bob.go` API packages, error codes, `bob-server`
  variables and modes, `spec/`) updates the docs **in the same change**, and the unit writes a
  `docs:` note in its progress file (which pages, or why none).
- Documented flows must work: never document a feature that does not work as if it works;
  put it on the status page (`07-reference/03-status.md`) with the workaround.
- The CI stage `docs` enforces both (tests in `tests/docs`, change-coupling rule). A change
  that needs no docs carries `docs-exempt: <reason>` in the commit message or its progress
  file / `plan/decisions.md` row.

### 5.10 Typed surface and per-package modules are non-negotiable (owner D71/D72, ADR 0020)
- No public API takes a string that names a package, nixpkgs attribute, target, tool or library:
  nixpkgs is the generated typed surface (`nixpkgs.Openssl.Lib(p)`), registry names are written
  once in a declaration constructor and referenced by handle. Every `bob.go` describes only its
  own directory (no `..`, no subpackage files, no central source lists), one library/binary per
  directory package. Guards: HARNESS.md §18 (`tests/core/typesafe_test.go` allowlist is
  owner-protected; never move a violation into it).

---

## 6. Harness requirements (designed in `HARNESS.md`, built in Phase 3.1)

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
8. **Server tests** against a real S3 backend with conditional writes (RustFS, D63):
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

## 7. Roles & delegation (`.claude/agents/`)

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

## 9. Files

```
AGENTS.md                this file
REQUIREMENTS.md          Stage 1 requirements + owner decision log (§24)
SPEC.md                  exact spec (generated from spec/*.md; regenerate after edits)
HARNESS.md               harness design
ROADMAP.md               committed status
docs/adr/NNNN-*.md       locked decisions
docs/spikes/*.md         spike evidence
docs/out-of-scope.md
docs/harness-validation.md
docs/parity.md           generated
plan/STATUS.md           current state, gates, open questions
plan/decisions.md        agent decisions (owner may overturn)
plan/architecture.md
plan/execution-plan.md
plan/usage.md
```
