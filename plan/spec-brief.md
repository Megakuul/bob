# SPEC section brief (Phase 2.3) — read this first

You draft **one section** of bob's SPEC.md. bob is a typed Go framework that makes Nix an
invisible build engine (`pkg/`), plus a separate HA build/cache server (`internal/`, `cmd/`). The
server uses only S3 buckets, through dynamitedb and lake, plus an artifacts bucket. Stage 3 agents
will build bob from SPEC.md alone, with the harness checking every acceptance criterion. Write
for them: exact, testable, no hand-waving.

## Sources (in priority order)

1. **Owner decisions** in `REQUIREMENTS.md` §24, **D1–D64**. They override everything else.
   D49–D64 are new since the spikes, and several overturn spike recommendations:
   - D53: scheduling hybrid.
   - D54: slow polling plus a wake-up call.
   - D55: workers write logs to lake in batches. The orchestrator **pulls** a worker's in-memory
     log only while a user follows the build live. There is no continuous streaming and no
     orchestrator↔orchestrator peering.
   - D56: the worker runs as root, builds run non-root and isolated, probably in a container.
   - D57: auth hierarchy is S3 credential → token, never the other way round. bob never mints S3
     credentials (no STS).
   - D58: visibility markers per path; extra crate data in bob.lock.
   - D59: `cd` + start the editor must just work.
   - D60: dynamitedb is **GET-only on hot paths: no LIST, no `Query`**.
   - D61: downloads go through an orchestrator proxy, never presigned URLs.
   - D62: Forgejo uses a bot password and per-job tokens.
   - D63: only fully AWS-S3-compatible backends with conditional writes. SeaweedFS and Garage
     are not supported; RustFS is the reference test backend.
   - D64: no dynamitedb changes.
2. `REQUIREMENTS.md` (all R-ids), `BOOTSTRAP.md` §2, §3 (Phase 2.3), §5, §6 and §8.
3. ADRs in `docs/adr/`. 0001 and 0002 are locked. The others are Proposed. **Update the ADRs of
   your section:** set the status to `Accepted (Gate S pending)` and add an "Owner amendments"
   section that names the D-ids that change it. If an owner decision makes the ADR's main choice
   wrong, rewrite the decision.
4. Spike write-ups in `docs/spikes/`. Use the quiet-machine numbers in
   `docs/spikes/measure/quiet/*.log` for budgets.
5. `plan/STATUS.md`: the "Findings to merge into SPEC" section contains corrections. Examples:
   a 401 is a warning (S15); lake's bucket argument works as a prefix (lake partitions); the lake
   L2 fix is in 045e301; the dynamitedb ETag "bugs" were overstated.
6. `plan/decisions.md` A1–A21: agent decisions. Follow them unless an owner decision conflicts.

## Output

- Write only `spec/<NN>-<slug>.md` and the ADRs listed for your section. Never edit
  REQUIREMENTS.md, BOOTSTRAP.md, plan/*, or other sections. The coordinator merges everything.
- Requirement entries: `#### R-<AREA>-<NNN> <title>`, keeping the ids from REQUIREMENTS.md.
  New sub-requirements use the next free numbers in your area; check REQUIREMENTS.md for free
  numbers. Each entry has:
  - **Statement:** one or two sentences.
  - **Acceptance:** Given/When/Then, or a measurable threshold. Each one can be checked by the
    harness, unless it is marked `(manual)`.
  - **Source:** R-ids / D-ids / ADR / spike section.
  - Budgets are marked **[proposal]**, with the quiet-machine measurement they derive from and
    the profile (`[8c/15G]` or `[2c/4G]`).
- Be **exact** where BOOTSTRAP §3 Phase 2.3 asks for it:
  - Go type and function signatures with doc comments, plus at least one example per public
    constructor (R-FW-013).
  - CLI syntax and output samples.
  - File formats as a schema plus an example.
  - Key schemas as tables (key pattern, value fields, writer, reader, access op).
  - State machines as transition tables (state, event, guard, action, next state).
  - Proto files as complete `.proto` text.
  - The RBAC permission for every RPC.
  - UI pages: a wireframe plus design tokens.
- Mark open points `**[open]**` with a recommendation. Don't ask the owner. Escalate only true
  architecture conflicts, and put them in a final "Escalations" list.
- Interfaces to other sections: name them in a short "Interfaces" list at the top (e.g.
  "§05 provides `JobRecord`"). Use the names from the section map below, so the coordinator can
  reconcile them.

## Section map (owner → file)

| § | File | Scope | ADRs you update |
|---|---|---|---|
| 01 | `spec/01-core.md` | `pkg/` core API: `bob.go` convention and evaluator, `bob.Define`, labels, graph, platforms and variants model, `bob.lock` format (container + per-ecosystem entry schemas, with §03 detailing them), derivation layer (ATerm, store paths), `bob-builder` contract, fixup and closure budgets, source import (bob fetches, `nix store add`), embedded rootless Nix and its config, nixpkgs lookup/index | 0003, 0009 (model part) |
| 02 | `spec/02-cli-dev.md` | CLI commands, flags, output formats (human/TTY/JSON events), exit codes, the error taxonomy and message templates (S15), `bob dev`, `bob env`, the direnv-style shell hook, `.bob/bin` shims, the **LSP path-rewriting proxy** (`/nix/store/…` ↔ `~/.local/share/bob/nix/store/…`; build a tiny proof with gopls + a scripted LSP client as bobtest, record the result), editor settings, `bob install`, `--remote` | 0017 |
| 03 | `spec/03-ecosystems.md` | C/C++ (incl. `cc.External`), Go, Rust, npm/TS/Node/Bun, OCaml, codegen + materialisation: API per ecosystem, derivation layout, resolver rules, lock entries, generated tooling files, quirks tables, ecosystem budgets | 0004, 0005, 0006, 0007, 0008 |
| 04 | `spec/04-outputs-platforms.md` | Cross-compilation rules, toolchain selection, variants per package kind, OCI images, AppImages, closures as outputs, release artifacts as built objects, size budgets | 0009 (cross part), 0010 |
| 05 | `spec/05-server-data.md` | Bucket layout (control, fleet, analytics/lake, artifacts) and policies. **dynamitedb key schema, GET-only (D60):** first study dynamitedb's code in `spikes/s09-dynamitedb/vendor-src` (keys, ETags, conditional ops, CBOR). Then design every hot access as a GET or conditional PUT on a known key (counters/pointers, sequence slots, index documents). Verify a conditional GET (`If-None-Match` → 304) on RustFS (the binary from spike S09/S11 tools). State machines for build, job, worker and release; leases/epochs; polling + wake-up (D54); scheduling and batch splitting (D53); worker registration (D57, no STS); worker isolation (D56: root worker, non-root isolated builds, container per job, Nix build users); logs: lake partitions via prefix, batch writes, live pull (D55, verify SigV4 with the URL-prefix trick on RustFS); orchestrator HA without a coordinator | 0011, 0012, 0013, 0015, 0016, 0019 |
| 06 | `spec/06-server-api.md` | Complete `proto/` drafts (connectrpc + buf) for every service; RBAC permission per RPC; roles (built-in `admin` only, plus custom roles, D-entries); local users (password only), OIDC, scope→role mappings, service accounts; Nix binary cache + OCI registry HTTP endpoints, auth flows (netrc, token flow), the download proxy (D61), visibility markers (D58), promotion; releases; pipelines and forge adapters (S16, D62); UI pages (wireframes, design tokens, yellow/black, rounded-xs, no gradients, light default + dark) | 0014, 0018 |

## Machine rules (shared host, others running)

- Spike machine rules from `docs/spikes/BRIEF.md` apply if you run anything: your own
  `XDG_CACHE_HOME=/root/.cache-spec<NN>`, ports 22000 + NN·100 … +99, scratch under
  `/root/scratch-spec<NN>/`, no GC, no `/etc/nix`, no `nix profile add`, no git commits, and
  nothing left running.
- Be economical with tokens:
  - Read the answer and SPEC-input sections of the write-ups first, and dig deeper only where
    you need exact facts.
  - Keep tool outputs short.
- Length guide: as long as exactness needs, but no filler. Don't restate the requirements in
  prose.

## Final reply to the coordinator (≤ 250 words)

- Path of the section.
- The ADRs you updated.
- New R-ids added.
- Interfaces you expect from other sections.
- `[open]` items.
- Escalations, if any.
- Verifications you ran, with their results.
