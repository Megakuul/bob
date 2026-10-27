# Implementation brief (Stage 3, Phase 3.2) — read after AGENTS.md, HARNESS.md, your SPEC sections

Gate H was recorded on owner instruction 2026-10-22 (tag `gate-h`; the hash manifest
`tests/protected.sha256` is written by the owner/`protect -write`, which the permission system
reserves). Tests are protected NOW: never edit, skip, loosen or re-bless anything under `tests/`,
`tests/fixtures`, goldens, `budgets.json`, `tests/parity`. If a test looks wrong, do not touch it:
write the evidence in `plan/progress/<unit>.md` under "TEST PROBLEM" and continue with other work.
Tests of the task turn green by implementing the feature in `pkg/`, `internal/`, `cmd/` (replace the
`diag.NotImplemented` stubs; keep the SPEC signatures). Open questions: self-answer with AGENTS.md
section 2, record in `plan/progress/<unit>.md` under "DECISIONS" (coordinator copies to decisions.md).
Ask the owner only for what AGENTS.md section 0 lists.

Done for a task = its covered R-id tests pass WITHOUT any `red:` text (run them directly with
`go test <pkg> -run <Test> -count=1`, `export CGO_ENABLED=0` if gcc is missing), `gofmt -l` empty,
`GOWORK=off go vet` clean for packages you touched, `go run ./tools/ci -only fmt,vet,lint,layering`
passes, and the full set of tests you did not intend to change does not get worse.
Docs (AGENTS.md §5.9): update `website/content/docs/` in the same change as any public-surface
change and add `docs:` notes to your progress file; `go run ./tools/ci -only docs` must pass
(or the change carries `docs-exempt: <reason>`).
Guardrails: no panic/log.Fatal/os.Exit outside main; recover at goroutine/RPC boundaries;
`pkg/` never imports `internal/`; errors wrapped with %w; context on I/O; no global mutable state;
licences: any new dependency needs a licence check (AGPL-compatible) and an ATTRIBUTION.md row.
Rules for agents: no `git add/commit` (coordinator commits); own build caches
(`export GOCACHE=/var/tmp/gc-<unit> XDG_CACHE_HOME=/var/tmp/xdg-<unit>`, NOT /tmp: it is a small
tmpfs); use `\rm`/`\cp`; own only your listed directories; read-only everywhere else (if you need a
change in another unit's package, write it under "NEEDS" in your progress file).
Resumability: checkpoint every ~15 min in `plan/progress/<unit>.md` (done / next / files /
DECISIONS / NEEDS / TEST PROBLEM). If you are relaunched, continue from that file. Finish each task
in your chain before starting the next; report after each task (<= 25 lines: task id, files,
tests green, tests still red and why).

## Shared-tree safety (added 2026-10-23 after the I-B15 incident, A147)
A unit once restored a pre-run tar snapshot over `internal cmd/bob-server tests` twice and silently reverted
other units' uncommitted work (logstore rebase, two test corrections). Rules for every unit:
- NEVER restore, extract, copy back or `git checkout` a snapshot over shared directories. Backups and
  experiments happen in a private copy (`/var/tmp/<unit>/repo`), changes come back by editing the files you own.
- Mass rewriters (AST tools, sed over many files) touch only files you own or have just listed in your
  progress file; run them on a private copy first and diff.
- Do not run `git` commands in the shared repo (the coordinator commits named paths).

## Typed surface and per-package modules (added 2026-10-24, owner D71/D72, ADR 0020, unit Q1)
Non-negotiable (AGENTS.md §5.10). Every new or changed public API parameter is a typed value unless
it is data of a category of SPEC §1.4.5; `TestRFW072TypedSurface` fails on any unclassified raw
string and its allowlist is protected (ask the owner, never extend it yourself). nixpkgs packages
are only the generated handles (`nixpkgs.<Name>.Tool/Lib/Pkg/Exe(p)`; bob's own packages use
`pkg/nixpkgs/nixext` for computed attributes). Every fixture, test repository and documented
`bob.go` keeps one package per directory, sources only in its own region, Go declarations in
the Go package's own `bob.go`, main-package binaries reached through `bob.Slot`
(`TestRFW070FixtureLocality`, `TestDocsPackageLocality`, faults 82–92). The implementation plan is
`plan/progress/Q1-impl-plan.md` (Q2: generator, core, nixpkgs migration, docs; Q3: cc and the
other ecosystems).

