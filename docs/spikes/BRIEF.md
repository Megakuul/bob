# Spike brief (Phase 2.2) — read this first

You are running one spike of Stage 2 (Refinement) of **bob**, a cleanroom project. bob is
a typed Go framework that makes Nix an invisible build engine (`pkg/`), plus a separate HA
build/cache server (`internal/`, `cmd/`) that uses only S3 (dynamitedb, lake, an artifacts
bucket). Spikes are throwaway proof-of-concept code. Their output is **evidence and exact
proposals for SPEC.md**, not product code.

## Read before you start

1. `REQUIREMENTS.md`: the sections your spike names, plus §24 (decision log, D1–D52).
2. `BOOTSTRAP.md` §2 (decision principles) and §5 (guardrails).
3. Locked Gate 0 decisions:
   - `docs/adr/0001-generation-strategy.md`: **strategy B**. Go computes `.drv` files itself;
     no Nix language for bob's packages.
   - `docs/adr/0002-rootless-nix.md`: embedded static Nix, the userns store, and Decision 5
     (`bob dev` environment, `.bob/bin` shims).
4. `docs/environment.md` (host facts) and `plan/decisions.md` (agent decisions so far).
5. If your spike builds packages, read `docs/spikes/gen-b.md` and the code in `spikes/gen/b/`
   (B backend), `spikes/gen/ir/` (graph IR) and `spikes/gen/fixture/` (shared fixture).
   Nixpkgs pin: `nixos-26.05@cd4ea8d57de16f4810bc5f14207bac45fbf19f67`.

## Where you may write

- Code: `spikes/sNN-<slug>/` only, with its own Go module `bobspike/sNN`. If you need B, the IR
  or the fixture, **copy** them into your directory or import them read-only through a `replace`
  directive. Never edit `spikes/gen/`, `spikes/rootless/` or another spike's directory.
- Write-up: `docs/spikes/sNN-<slug>.md`.
- ADR, if your spike decides something: `docs/adr/<number>-<slug>.md` with status
  **Proposed**. The number is fixed: **ADR number = spike number + 2** (S1 → 0003,
  S9 → 0011, S17 → 0019). For a second ADR in the same spike, append `b` (e.g. `0011b-…`).
- Do **not** edit REQUIREMENTS.md, BOOTSTRAP.md, plan/*, other write-ups or ADRs. The
  coordinator merges your results.

## Shared machine rules (several spikes run in parallel)

- Host: 8 vCPU aarch64, 15 GiB RAM, 151 GB disk, Fedora 44. Nix 2.34.8 runs single-user as root.
  `bobtest` (uid 1000) is available for rootless tests.
- Run every `nix` command with `XDG_CACHE_HOME=/root/.cache-sNN` (a shared cache gives "SQLite
  busy" errors). Limit Nix to `NIX_CONFIG=$'cores = 4\nmax-jobs = 2'` unless you are measuring.
- Never run `nix store gc` or `nix-collect-garbage` on root's store, never touch `/etc/nix`,
  never `nix profile add` (concurrent edits). For tools use
  `nix shell nixpkgs/<pin>#tool -c …` or `nix build -o spikes/sNN-…/.tools/<tool>`.
- Build Go spike binaries with `CGO_ENABLED=0` unless the spike is about cgo.
- Network ports: only **20000 + N·100 … 20000 + N·100 + 99** (S9 → 20900–20999).
- Temporary files go in `spikes/sNN-…/tmp/` or `/root/scratch-sNN/`, never in shared `/tmp`
  paths that someone else could also use.
- The network may be used to fetch sources and packages. Never push, publish or create
  accounts on external services, and never spend money.
- No git commits.
- Don't leave background processes running when you finish.

## Measurements

- Mark every number with its profile, `[8c/15G]` or `[2c/4G]`. The low-power profile is
  `systemd-run --scope -p AllowedCPUs=0-1 -p MemoryMax=4G -- <cmd>`.
- Other spikes are running, so your timings are **indicative**. Provide a `measure.sh` that the
  coordinator can rerun on a quiet machine, and say in the write-up which numbers depend on that.
- Measure, don't assume (BOOTSTRAP §2). If you could not verify something, say so plainly.

## Decisions

- Answer side questions yourself from BOOTSTRAP §2. List them in the write-up under
  "Agent decisions", each with one line of reasoning.
- Only architecture decisions, gates, licences, spending and publishing go to the owner. Write
  them under "Owner decision needed" with **a/b/c options** and a recommendation. The owner
  wants concrete options, never vague ones. Don't ask the owner anything yourself.

## Write-up structure (`docs/spikes/sNN-<slug>.md`)

1. Question and requirement ids.
2. What was built (short; paths).
3. Results: tables with measurements and the verifications you ran.
4. Answer: the decision or recommendation.
5. **SPEC input**: exact proposals the SPEC can copy, such as API signatures and examples,
   file formats, key schemas, state machines, budgets marked **[proposal]**, error texts.
6. Agent decisions, owner decisions needed (a/b/c), open risks, and what still has to be
   verified.

Style: plain English, short sentences, no marketing words. Every claim either comes with
evidence or is marked unverified.

## Final reply to the coordinator (≤ 300 words)

- The answer.
- 3–6 key numbers.
- Owner decisions needed (a/b/c).
- Risks.
- Paths of the write-up, the ADR and the code.
