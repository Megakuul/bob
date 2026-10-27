# bob — HARNESS (Stage 2, Phase 2.4)

Status: draft for Gate S, 2026-10-21. Owner: Linus Moser.

This file says how BOOTSTRAP §6 (items 1–13) is built in Phase 3.1. It covers commands,
directories, fixtures, the budgets file and the fault catalogue. SPEC.md is the source of every
acceptance criterion. This file only says *how* the harness checks them. Precedence: owner
decisions (REQUIREMENTS §24) > SPEC > this file. One case needs care: BOOTSTRAP §6.8 names
SeaweedFS. D63 overrides that. The only supported backends are AWS-S3-compatible ones with full
conditional writes, **RustFS is the reference test backend**, and no harness suite runs SeaweedFS
or Garage.

Markers: **(manual)** means a person runs the check, because this host cannot. **[open]** means a
decision is still missing; each one carries a recommendation, which Phase 3.1 follows unless the
owner says otherwise. Every other check runs on the Stage 2/3 host (docs/environment.md):
aarch64, 8 vCPU / 15 GiB, no KVM, no container runtime, working unprivileged user namespaces, a
test user `bobtest` (uid 1000), and root.

References: a bare `§N` is a section of this file. Others are written `BOOTSTRAP §…` or
`SPEC §…`; a leading zero (`§03.8`, `§05.12`) also means SPEC.

---

## 1. The CI command (BOOTSTRAP §6.1, §6.13)

### 1.1 Entry points

| Phase | Command |
|---|---|
| Before self-hosting | `go run ./tools/ci` |
| After R-ARCH-005 | `bob pipeline run ci`. The pipeline's single step runs the same `tools/ci` binary, which bob builds; stages and output are identical. |

`tools/ci` is plain Go (standard library plus `tools/` packages). It never shells out to anything
except pinned tools (§1.4) and the binaries it built itself.

### 1.2 Flags

| Flag | Meaning |
|---|---|
| (none) | **Gate run.** Every stage of §1.3 except those marked *slow*. "The CI command passed" (BOOTSTRAP §5.8) means this run exited 0. |
| `-fast` | Inner loop for implementers: stages 1–12 and stage 15 only, plus `go test -short`. Fixture builds are limited to no-op builds against a warm store. This run never counts as "CI passed". |
| `-slow` | The gate run plus all *slow* stages: full benchmarks, GitLab live, soak, long fuzzing, the bundle distro matrix and the [2c/4G] profile. This is the "nightly" run. There is no CI service here, so the test-runner role runs it at the end of every milestone and before every gate. |
| `-only a,b` | Run only the named stages or suites (names from §1.3 and §14). |
| `-json` | NDJSON on stdout: one `{"v":1,"event":"stage.start"|"stage.end"|"test.fail"|"budget"|"protected.change"|"summary",…}` line per event. Human output is suppressed. The schema is `tools/ci/events.schema.json`. |
| `-profile 8c15g\|2c4g` | Budget profile (§12). `2c4g` re-execs itself under the cgroup of §12.3. |
| `-update` | Writes golden *candidates* into `out/golden-candidates/` and never touches `tests/goldens/` (§3). |
| `-keep` | Keeps scratch stores, RustFS data and logs under `out/ci/<run-id>/scratch`. |
| `-run-id X` | Fixes the output directory, for reruns. |

Exit status: 0 = all green; 1 = a failure in any stage; 2 = harness error (the harness itself
broke, e.g. a pinned tool would not start); 3 = protected-path violation after Gate H. The full
report is always written to `out/ci/<run-id>/report.json` with per-stage duration, failures,
budget rows and the protected report. `out/` is gitignored.

### 1.3 Stages, in order

The order puts cheap checks before expensive ones. Stage 1 always runs and always reports, even
when a later stage fails.

| # | Stage | What runs |
|---|---|---|
| 1 | `protected` | hash manifest check and report (§2.1) |
| 2 | `fmt` | `gofmt -l` (must print nothing); `buf format -d proto/` (must print nothing); `prettier --check` is **not** used (owner style rules are reviewed, D45) |
| 3 | `gen` | regenerates `proto/` (buf generate), `tests/budgets/budgets.json` (§12.1), `codes.lock` check (R-NIX-028), `docs/parity.md` (§11) and the IR schema, then fails if `git diff --exit-code` shows a change |
| 4 | `vet` | `go vet ./...` and `GOWORK=off go list ./...` (R-ARCH-001 path rule) |
| 5 | `lint` | staticcheck plus bob's analyzers (§1.5) |
| 6 | `layering` | `go test ./pkg -run TestLayering` (R-ARCH-002/003) and the `importboundary` analyzer |
| 7 | `licence` | Go dependency licences (`go-licenses report` over `./cmd/...`, checked against `tools/ci/licences.allow`: MIT, BSD-2/3, Apache-2.0, ISC, MPL-2.0, Unlicense, CC0, Zlib. Anything else fails unless it is listed with an owner-approval reference.) Web dependencies are checked through `web/package-lock.json` licence fields with the same allowlist. A check also verifies that every file under `assets/`, `web/static/` and every embedded binary (static Nix, AppImage runtimes, busybox) has a row in `ATTRIBUTION.md`. |
| 8 | `proto` | `buf lint`; `buf breaking --against '.git#branch=main'` (before the first release, `main`; after it, the last release tag) |
| 9 | `web` | `svelte-check --fail-on-warnings`, the web dependency allowlist test (R-UI-005), `vite build`, bundle-size budget (R-UI-007) |
| 9 | `docs` | documentation guardrail (§17): `go test ./tests/docs` (docs against the CLI table, the API packages, the error codes, the server variables; snippets, links, frontmatter) and the change-coupling rule |
| 10 | `unit` | `go test -race -shuffle=on -count=1 ./...` including fuzz seed corpora and goleak checks |
| 11 | `golden` | golden suite (§3) |
| 12 | `errors` | error-quality suite (§5) |
| 13 | `acceptance` | the suites of §14 marked *gate* (fixtures, determinism, rootless, sandbox, store, server, chaos, RBAC, forges, cache/OCI, UI, CLI/devenv, bootstrap) |
| 14 | `parity` | parity catalogue and floor (§11) |
| 15 | `budgets` | fast budgets (§12) |
| 16 | `summary` | writes `report.json`; prints the failures and the protected report again |
| S1–S6 | *slow* | `bench`, `forge-gitlab-live`, `soak`, `fuzz-long`, `bundles-distro`, `budgets` at `2c4g` |

Stages 10–15 can run in parallel where they use disjoint resources. Each suite has its own
scratch store, RustFS instance and port range.

### 1.4 Tool pinning

- `tools/` is its own module, `github.com/megakuul/bob/tools`, with `tools/go.mod` and `tool`
  directives (Go ≥ 1.24) for:
  - `honnef.co/go/tools/cmd/staticcheck`
  - `golang.org/x/tools/go/analysis/...` (bob's analyzers, §1.5)
  - `github.com/google/go-licenses`
  - `github.com/bufbuild/buf/cmd/buf`
  - `google.golang.org/protobuf/cmd/protoc-gen-go`
  - `connectrpc.com/connect/cmd/protoc-gen-connect-go`
  - `golang.org/x/vuln/cmd/govulncheck` (report only, not gating)

  A root `go.work` lists `.` and `./tools`, so `go run ./tools/ci` works from the repo root.
  `tools/ci` calls the tools as `go tool -modfile=tools/go.mod <tool>`. Versions and hashes
  therefore live in `tools/go.mod`/`go.sum`, and none of the tool dependencies enter bob's own
  module graph or its licence report.
- **R-ARCH-001 note:** SPEC says every package of `go list ./...` is under `pkg/`, `cmd/`,
  `internal/` or `proto/`. With a workspace, the check runs `GOWORK=off go list ./...`, so the
  `tools/` module stays outside the main module. **[open]** Recommendation: add this sentence to
  R-ARCH-001 at Gate S.
- Non-Go tools are pinned in `tools/pins.json`, each with URL, sha256 and size. `tools/ci` fetches
  them into `~/.cache/bob-harness/tools/<sha256>/` and never uses `$PATH`:
  - **RustFS 1.0.3** (upstream musl release, sha256 as in S9/S11; RustFS is not in nixpkgs at the
    pin).
  - From the nixpkgs pin (`nixos-26.05@cd4ea8d5`), realised through the host Nix or, once it
    exists, bob's embedded Nix: Node 22 + npm (only for `web/` until bob's npm ecosystem builds
    it), Chromium (UI), Forgejo 16, GitLab 19.4.2 (slow), bash 5 / zsh 5.9 / fish 3.7 (shell-hook
    pty tests), bubblewrap, podman (§7.5), qemu-user (amd64 runs), strace, gopls, and the
    clients `nix` (cache conformance) and `curl`.
  - The five rootfs images of S8 §3.5, by digest.
- Until R-ARCH-005, `web/` builds with the pinned Node through `npm ci --ignore-scripts` from the
  committed lock file. **[open]** Recommendation: accept this as a harness-only exception to D15
  (D15 concerns users' builds), and switch to bob's own npm path as soon as the npm ecosystem
  builds `web/`.

### 1.5 Linters (`tools/lint`, go/analysis multichecker)

Every analyzer has a positive and a negative testdata package (`analysistest`). The allowlist
`tools/lint/allow.txt` is protected (§2.1); each entry names a function and a reason.

| Analyzer | Rule | Source |
|---|---|---|
| `nopanic` | no `panic`, `log.Fatal*`, `log.Panic*` outside `package main` under `cmd/`. Allowlisted: `diag.New` (panics on an unknown code by design, R-NIX-028; reachable only with constant codes, which a test proves), and `regexp.MustCompile`/`template.Must` in package-level `var` initialisers. | BOOTSTRAP §5.3 |
| `noexit` | `os.Exit` only inside `func main` of `cmd/*/main.go` | BOOTSTRAP §5.3 |
| `nolist` | the identifiers `ListObjects`, `ListObjectsV2`, `NewListObjectsV2Paginator`, `ListObjectVersions`, `ListMultipartUploads` and `dynamitedb.Query` are referenced only in `internal/store/sweep` | R-STORE-100/101, D60 |
| `safego` | no bare `go` statement in `internal/` or `cmd/` (non-test); the only exempt package is `internal/safe`, which holds the one `go` statement. Use `internal/safe.Go`, which recovers and converts a panic to an error. `pkg/` uses `pkg/engine/safe` the same way. | BOOTSTRAP §5.3 |
| `storecall` | no package outside `internal/store` imports `dynamitedb`; `internal/store` wraps every call in `store.Call` (recover → `ErrPanic`, S9 §3.6) | R-STORE-005 |
| `importboundary` | the R-ARCH-003 layer table, encoded once in `tools/lint/layers.go` and shared with `TestLayering` | R-ARCH-002/003, BOOTSTRAP §5.6 |
| `ctxio` | exported funcs in `internal/` that take an `io.Reader`/`*http.Client`/store handle, or start a process, have `context.Context` as their first parameter | BOOTSTRAP §5.3 |
| `noglobals` | no package-level `var` of mutable type outside the allowlist (memoised handles, R-FW-011) | BOOTSTRAP §5.3 |
| `wrapcheck-lite` | `fmt.Errorf` with an error argument uses `%w` | BOOTSTRAP §5.3 |

Non-Go checks in the `lint` stage:
- No `*.nix` file under `tests/fixtures/repos/`, `examples/` or `docs/` user guides, and none in a
  `go doc -all ./pkg/...` output (`.nix` or `nix-build` in public docs fails). (BOOTSTRAP §5.5)
- The web dependency allowlist test also rejects `clsx`, `cva`, `tailwind-merge`. The style
  checklist script (`web/scripts/style-check.sh`) is **not** a gate (D45). The code steward runs it.

---

## 2. Directory layout

```
tools/ci/                 the CI command (stages, report, events.schema.json, licences.allow)
tools/lint/               analyzers, layers.go, allow.txt (protected)
tools/golden/             golden review/approve tool (§3.2)
tools/s3count/            counting + fault-injecting S3 proxy (§8.1)
tools/devstack/           all-in-one local dev mode (§13.2)
tools/regmirror/          offline registry mirror (npm, crates, Go proxy, opam) (§4.3)
tools/synth/              deterministic generators for the large synthetic graphs (§4.2)
tools/pins.json           non-Go tool pins
tests/fixtures/repos/<name>/      fixture repositories (§4)
tests/fixtures/registry/          registry snapshot manifests (sha256 per blob; blobs in cache)
tests/fixtures/forges/{forgejo,gitlab,github}/   recorded webhook deliveries + API responses (S16)
tests/fixtures/evil/              hostile bob.go cases (S13)
tests/faults/<fault>/             fault catalogue entries (§5)
tests/faults/expect.json          fault → code → exit (from S15, plus §02 additions)
tests/goldens/<suite>/...         approved goldens (§3)
tests/budgets/budgets.json        budgets file (generated, §12.1)
tests/budgets/baseline-<profile>.json   last accepted measurements
tests/parity/floor                pass-count floor (§11)
tests/parity/manual.json          (manual) requirement sign-offs
tests/protected.sha256            protected-path manifest (§2.1)
tests/owner.allowed_signers       owner's SSH signing key(s) (§2.1)
tests/<suite>/                    Go test packages of each suite in §14 (package `…/tests/<suite>`)
pkg/bob/diag/codes.lock               error code table lock (R-NIX-028, protected)
docs/harness-validation.md        self-validation report (§10)
docs/parity.md                    generated parity catalogue (§11)
out/                              CI output, gitignored
```

`tests/` packages live in the main module under `tests/…`. **[open]** R-ARCH-001's path rule then
needs `tests/` in its list. Recommendation: allow `tests/` (test-only packages, never imported by
`cmd/`; the `importboundary` analyzer enforces that).

**SPEC names:** SPEC refers to `examples/acme`, `examples/polyglot`, `examples/cpp-lib` and
`examples/cross-repo` as well as `tests/fixtures/repos/<eco>`. In the harness they are all fixture
repositories: `examples/<name>` in SPEC means `tests/fixtures/repos/<name>`. The user-facing
`examples/` directory holds symlinks to the fixtures that are good documentation (acme, polyglot),
so they cannot drift. Also, SPEC's `fixtures/<fault>/<nixver>/golden.txt` (R-NIX-012) means
`tests/faults/<fault>/<nixver>/golden.txt`. **[open]** Recommendation: SPEC wording fix at Gate S.

Large binary data (registry tarballs, rootfs images, recorded Nix streams above 1 MB, Chromium
reference screenshots stay in git) is kept out of git: the repo holds a manifest with sha256, and
`tools/ci` fetches into `~/.cache/bob-harness/blobs/<sha256>`. A missing blob is a harness error
(exit 2), never a skipped test.

### 2.1 Protected paths (BOOTSTRAP §5.2)

- **Protected set** (glob list at the top of `tests/protected.sha256`): `tests/**` (except
  `tests/*/scratch`), `pkg/bob/diag/codes.lock`, `tools/ci/**`, `tools/lint/**`, `tools/s3count/**`,
  `tools/golden/**`, `tools/pins.json`, `tools/go.mod`, `tools/go.sum`, `schema/events-v1.json`, and every `*_test.go`
  file whose package doc carries `//bob:protected` (acceptance tests outside `tests/`, e.g.
  `pkg/layering_test.go`). The harness code itself is protected, so a linter cannot be weakened to
  pass.
- **Manifest:** one line `sha256  path` per file, sorted, generated by
  `go run ./tools/ci protect -write`.
- **Report:** stage 1 recomputes the hashes and lists added, removed and changed files against
  the manifest, and against the manifest at `main` (so a branch that rewrites both is still
  reported). The report goes to stdout, `report.json` (`protected[]`) and `protected.change` JSON
  events.
- **Enforcement after Gate H:** every commit that changes the manifest must be signed by an owner
  key from `tests/owner.allowed_signers` (`git log --format=%G? -- tests/protected.sha256` is
  checked with `gpg.ssh.allowedSignersFile`). An unsigned change → exit 3. Agents can therefore
  propose test changes (§3.2), but cannot land them. Before Gate H the stage only reports.
- **[open]** The owner provides an SSH signing key for `tests/owner.allowed_signers` at Gate H.
  Recommendation: an SSH key the owner already uses for git (`git config gpg.format ssh`).

---

## 3. Golden tests (BOOTSTRAP §6.3)

### 3.1 What is golden

Golden suite `golden`: `go test ./tests/golden/...`. Comparison is byte-exact after a fixed
normalisation: the repo root becomes `$REPO`, `$HOME` becomes `$HOME`, and timestamps become
`<t>` in human output only. Store hashes are **not** normalised (determinism is part of the
claim).

| Golden set | Path | Source requirement |
|---|---|---|
| Graph IR of each fixture | `tests/goldens/ir/<fixture>.json` | R-FW-047, R-FW-043 |
| Derivations (ATerm) and export stream, incl. nixpkgs `hello` recomputation | `tests/goldens/drv/<fixture>/<label>.drv` | R-NIX (SPEC §1.10) |
| `bob.lock` per fixture | `tests/goldens/lock/<fixture>.lock` | SPEC §1.8, §03.7 |
| Generated native files: `go.mod`, `go.work`, `Cargo.toml`, `package.json`, `tsconfig.json`, `compile_commands.json`, editor settings, `.git/info/exclude` block | `tests/goldens/native/<fixture>/…` | R-FW (D2, D25), §03.0, R-DEV editor settings |
| Error output: plain, TTY and JSONL per fault | `tests/faults/<fault>/<nixver>/golden{.txt,.tty.txt,.jsonl}` | R-NIX-012, §02.5 |
| CLI help text and completion scripts (bash, zsh, fish) | `tests/goldens/cli/` | R-CLI-001, §02.9 |
| `--json` event sequence of a cold polyglot build | `tests/goldens/events/polyglot-cold.jsonl` (event names and order only) | SPEC §1.16 |
| OCI index digest, AppImage sha256 of frozen fixtures | `tests/goldens/outputs/` | SPEC §4.5, §4.6 |
| Cross build derivation count (99, ±0) | `tests/goldens/outputs/cross-drvcount.txt` | SPEC §04 (S7) |
| UI screenshots | `tests/goldens/ui/<route>.<theme>.png` | §9 |

### 3.2 Review workflow

1. `go run ./tools/ci -update -only golden,errors,ui` writes candidates to
   `out/golden-candidates/` and a list of changed goldens. Nothing in `tests/` changes.
2. `go run ./tools/golden review` builds `out/golden-review/index.html`: one entry per changed
   golden with a side-by-side and unified text diff (JSON pretty-printed and key-sorted before
   diffing), and for PNGs the old image, the new image and a red-highlight difference image, plus
   the test that produced it and the requirement ids it covers. The page is a static file and
   needs no server.
3. Before Gate H, agents may run `go run ./tools/golden approve <ids|all>`, which copies
   candidates into place and rewrites `tests/protected.sha256`. After Gate H, only the owner runs
   `approve` and signs the commit (§2.1). An agent that believes a golden is wrong writes the
   evidence into `plan/STATUS.md` and attaches the review page path (BOOTSTRAP §5.2).
4. The review page is also published as a private Artifact for the owner when the owner asks for
   it.

---

## 4. Fixture repositories (BOOTSTRAP §6.4)

### 4.1 List

Each fixture is a real git repository (`git init` by the harness from a committed tree, so the
outer repo does not contain nested `.git` directories). Each has `bob.go` files, a committed
`bob.lock`, and no `.nix` file. **Every fixture is built from per-directory packages and the typed
surface (ADR 0020, D71/D72, §18):** each directory with sources has its own `bob.go`, no `bob.go`
references files outside its package region or of a subpackage, one library/binary per directory,
nixpkgs only through generated handles (`nixpkgs.Openssl.Lib(p)`), Go binaries declared in their
main package's own `bob.go` and reached from other packages through `bob.Slot`.
`TestRFW070FixtureLocality` enforces this for every fixture and test repository. Each is built end-to-end by suite `fixtures` for
`linux-arm64` natively and `linux-amd64` by cross-compilation, in every variant it declares.

| Fixture | Contents | What it proves |
|---|---|---|
| `acme` | Go-centric repo with `bob.go` next to the code, several packages, labels, a dev shell, the S1 LSP script `bobtest/lsp/acme.steps` | `bob.go` convention R1–R7, labels, `definedAt`, IR independent of checkout path, gopls/shim/LSP proxy, shell hook (SPEC §1.2–§1.6, §02.7) |
| `cc` | per-directory packages: static lib `libs/mathx`, shared lib `libs/pack` using nixpkgs zlib/openssl, exe `app` (headers only through `Deps`), `cc.External` (Make) in `third_party/zstd` | `pkg/cc`, compile_commands.json, header/library errors, per-package modules R-CC-006 and their faults 82–85, 91 (§03.1) |
| `cpp-lib` | small C++ library at the root (`src/`, `include/`, `test/`) and its tool as its own package `tool/bob.go` | embedded static Nix with no host Nix, `env -i` (R-NIX-020/023); one unit per package |
| `go` | 3 modules, cgo package linking a cc shared lib, tests, `internal/msg` | `pkg/golang`, go.mod generation and drift detection, incremental budgets (§03.2) |
| `rust` | S3 demo graph (serde+derive, clap+derive, …; 49 crates), features, build scripts | `pkg/rust`, Cargo.toml generation, crate lock in `bob.lock` (§03.3) |
| `npm` | `ts-lib` (~200 packages), a Vite site, a native package (better-sqlite3) | own resolver, isolated layout, lifecycle scripts denied/allowed, Vite dev/HMR (§03.4) |
| `ocaml` | S5 demo: 11 opam packages incl. lwt, a C stub, lib + exe | `pkg/ocaml`, opam resolution (§03.5) |
| `codegen` | S6 tree: two proto libraries with an import, connect Go + TS, GraphQL, OpenAPI | generators, materialisation into the working tree, cutoff on whitespace changes, conflict detection (§03.6) |
| `polyglot` | C++ lib + Rust lib + Go cgo binary `//cmd/api:api` + codegen + Svelte frontend; variants debug/release/asan; OCI image, AppImage, tarball, release | everything together: cross builds, variants, outputs, releases, budgets of SPEC §1.17 and §04 |
| `cross-repo/{dep,app}` | a pair: `app` imports `dep` through `bob.Import`, served from a `file://` GOPROXY | R-FW-022 typed handles across repos, one lock for both |
| `evil` (= `tests/fixtures/evil`) | the 15 S13 hostile `bob.go` cases | evaluator sandbox (§7.4) |

Not committed, generated by `tools/synth` with a fixed seed into the scratch dir: 1,000-package
wide `bob.go` graph, N=1,000 and N=10,000 IR graphs, 200-package Go workspace, 100-package C/C++
graph, 30-crate Rust chain, 100-step `bob.Run` chain, 10,000 small source dirs. The generator
code is protected, so these inputs are protected too.

### 4.2 Platforms and variants on this host

- `linux-arm64`: native build and run.
- `linux-amd64`: cross build. Binaries run under `qemu-x86_64` from the pin (invoked directly,
  no binfmt registration). **Native amd64 builds** and "amd64 native worker" rows (R-OUT-018)
  need an x86_64 machine: **(manual)** now. **[open]** Recommendation: add an x86_64 CI runner
  (e.g. a GitHub-hosted `ubuntu-24.04` runner or a small x86_64 VM) as a later harness item, and
  until then record those rows as `manual` in the parity catalogue.

### 4.3 Network

Gate runs are offline except for forge/IdP processes the harness starts itself on loopback.
`tools/regmirror` serves npm, crates.io (sparse index + crate files), the Go module proxy and
opam from `tests/fixtures/registry/` manifests plus the blob cache. A `-refresh-registry` run
(manual, online) recaptures it; refreshed manifests are reviewed like goldens. Suites that test
fetch failures (404, 403, DNS, refused, 500, stall) use a harness HTTP server with injectable
faults (like S15's `s15cache`).

---

## 5. Error-quality suite (BOOTSTRAP §6.5)

Suite `errors`: `go test ./tests/errors/...`.

### 5.1 Fault entry format

One directory per fault, `tests/faults/<NN>-<slug>/`:

```
fault.json        {"id":"06-cxx-compile","fixture":"polyglot","inject":{"kind":"patch","file":"libs/foo/src/foo.cpp"},
                   "command":["build","//libs/foo:foo"],"code":"BOB-E-2001","exit":1,
                   "position":"marker","excerpt":true,"hint":true,"nixver":["2.34.9"]}
patch.diff        the fault (or "inject": {"kind":"env"|"cache-fault"|"limit"|"signal"|"disk"|"ir-script"})
2.34.9/stream.log recorded internal-json stream (replay input; gzipped if > 1 MB)
2.34.9/meta.json  bob context at capture time
2.34.9/golden.txt, golden.tty.txt, golden.jsonl
```

`tests/faults/expect.json` is the protected index: fault → code(s) → exit. It is seeded from S15
`faults/expect.json` (48 faults plus the ok scenarios, faults 1–48 in S15 §3.2) and extended with
the BOOTSTRAP and §02 additions:

| Added fault | Expected |
|---|---|
| hand edit of generated `go.mod` (`go get` drift) | BOB-E-1011, exit 3 |
| **ADR 0020 (D71/D72), faults 82–92:** source `../` into a sibling package (82) / root `bob.go` lists subpackage sources (83) / public headers outside the package (91) | BOB-E-1028 (`outside` / `subpackage`), exit 3, position, hint |
| second C/C++ unit in one package (84) | BOB-E-1029, exit 3 |
| hand-written `-I` of another package (85) | BOB-E-1020 (`use Deps to add include paths`) |
| nixpkgs package named by a string, old API (86) / misspelt handle (87) | BOB-E-1020 compile error with the typed-handle / `did you mean` hint |
| `bob.go` imports `pkg/nixpkgs/nixext` (88) / assigns to a framework value (89) | BOB-E-1024 R7 / R8 |
| typed handle missing at the pinned revision (90, hook `BOB_TEST_NIXPKGS_MISSING`) | BOB-E-1001 variant `pin` |
| unbound typed slot of a Go binary (92) | BOB-E-1020 |
| `bob.lock` out of date / merge conflict markers | BOB-E-1012 / BOB-E-1019 |
| npm lifecycle script denied | BOB-E-2013 with the `E-SCRIPT-DENIED` text, no script ran |
| undeclared npm peer | BOB-E-2014 |
| prune pattern removes a needed file | BOB-E-2015 |
| unsafe archive entry (`../`, absolute, device) | BOB-E-3004 |
| `bob.go` rule violations R1–R7 | BOB-E-1024 per rule |
| source pattern leaves repo / glob matches nothing | BOB-E-1025 |
| non-deterministic `bob.go` | BOB-E-1018 |
| toolchain mismatch (Go/Rust/generator) | BOB-E-1021 |
| generated-file conflict | BOB-E-1016 |
| user namespaces blocked (simulated) | BOB-E-7005, exit 7, exact fix text |
| unsupported S3 backend (no conditional writes; tested with the proxy stripping `If-Match`) | BOB-E-7007 |
| bucket policy too permissive | BOB-E-7008 |
| unknown Nix message | BOB-E-9999, exit 70 |
| drift + all-unknown failure | BOB-E-9998 |
| no server configured with `--remote` | BOB-E-4009, exit 4 |
| expired login | BOB-E-4008 |
| evaluator limits (pids, memory, time, output size) | the S13 messages (§7.4) |

### 5.2 Checks per fault

1. **Code and exit:** each code from `expect.json` appears as `error[BOB-E-NNNN]`, and the
   process exit equals `exit` (R-CLI exit codes).
2. **Go source position:** `package defined at <bob.go>:<line>:<col>` must equal the position
   of the marker comment `//bob:fault <id>` in the fixture's `bob.go` (the test finds the marker
   and computes line:col of the constructor call that follows). File-level positions
   (`--> libs/foo/src/foo.cpp:13:13`) are checked the same way against a `bob:fault-site`
   marker in the injected patch.
3. **No raw Nix text** in default output (plain, TTY and JSONL, without `-v`). The test fails on
   any of: `/nix/store/[0-9a-z]{32}`, `.drv`, `derivation '`, `builder for`, `Cannot build`,
   `substituter`, `@nix `, `while evaluating`, `…-source/….nix:`, Nix option names (from the
   embedded Nix's option list), `error:` at line start without `BOB-E`. The same patterns run
   over every golden of §3 and every CLI output captured by any suite (a shared `leakcheck`
   helper that suites call on all captured stdout/stderr). (BOOTSTRAP §5.5)
4. **`-v` superset:** the `-v` output differs from the default only by added lines, and the raw
   Nix message appears in `nix:` lines (R-NIX-010).
5. **Golden:** byte-identical to `golden*.txt` (R-NIX-012, §02.5 `TERM=dumb` rules).
6. **JSON:** every JSONL line parses, has `v`=1 and a known `event`.
7. **Drift never turns failure into success** (`TestDrift`).

### 5.3 Two modes

- **Replay (gate, fast):** feeds `stream.log` + `meta.json` into the translator, like the S15
  golden harness. It runs in milliseconds and covers all faults.
- **Live capture (gate for faults 1–31 and the added faults on this host; `-slow` for all):**
  injects the fault into a scratch copy of the fixture and runs real `bob`. Faults that need host
  conditions run as follows. Disk full uses a 24 MB tmpfs in a user namespace. OOM uses a
  `systemd-run --scope -p MemoryMax=300M` cgroup. SIGKILL/SIGINT use signals. Cache faults use
  the fault HTTP cache. Fault 34 (`OOMPolicy=stop` race) accepts either documented code
  (8001/6003) and is the only fault with two allowed outcomes.
- **Nix bump gate:** a change to the embedded Nix pin forces `go run ./tools/ci -only errors
  -recapture`. All codes in `expect.json` must still hold. New goldens are candidates (§3.2).
  Toolchain bumps (gcc, rustc, go) recapture the 2xxx faults.

### 5.4 Code table

`pkg/bob/diag/codes.lock` is protected. The `gen` stage fails when a code is removed, a class
changes, or an argument set changes (R-NIX-028). A test calls `diag.New` for every code with its
arguments.

---

## 6. Determinism and reproducibility (BOOTSTRAP §6.6)

Suite `determinism`: `go test ./tests/determinism/...`.

1. **Evaluation:** each fixture evaluated twice, in two copies at different absolute paths, with
   different `$HOME`, `TZ`, locale and env order → byte-identical IR (R-FW, SPEC §1.6 (a)). Also with
   the evaluator cache cleared between runs.
2. **Derivations:** IR → drvs twice → identical drv hashes.
3. **Builds on two workers:** the harness starts two worker processes with separate stores
   (separate state dirs and `bobtest`-style uids) and builds every fixture output on each. Then
   it compares NAR hashes of every output path. Each deviation is reported as a diffoscope-style
   listing (file, offset, first differing bytes) in `out/ci/<run>/determinism/`. A deviation
   fails unless the path is in `tests/determinism/known-deviations.json` (protected, each entry
   with a reason and owner sign-off).
4. **Outputs:** OCI index digest and AppImage sha256 identical across two fresh builds and both
   profiles (R-OUT, golden §3.1). The builder binary store path equals `pkg/engine/store/builders.go`
   (R-ARCH-005).
5. **Limit on this host:** "different workers" means different processes and stores on the same
   aarch64 kernel. Cross-machine and cross-architecture reproducibility (an amd64 worker
   rebuilding an arm64 drv is not a thing; an amd64 worker rebuilding the same amd64 drv is)
   is **(manual)** until an x86_64 runner exists (§4.2).

---

## 7. Rootless matrix (BOOTSTRAP §6.7)

The host has no KVM and no container runtime, but unprivileged user namespaces work. That
decides the approach: **distro root filesystems run through `unshare`/`bwrap` as a non-root user
(bobtest)**, and real kernels are tested on real hosts later. This is honest about one point:
a rootfs changes userland (libc, `/etc`, missing `/nix`, CA files, locale), not the kernel or its
LSM policy.

### 7.1 Suite `rootless` (gate)

`go test ./tests/rootless/...`, run as `bobtest`:

| Case | How | Requirement |
|---|---|---|
| store works with no `/nix` on the host | `bob` (static) under `env -i PATH=/usr/bin:/bin` in each of the 5 pinned rootfs images (ubuntu 22.04, 24.04, debian 12, fedora 43, alpine 3.22, digests from S8 §3.5), entered with `bwrap --unshare-user` as bobtest; builds `cpp-lib` and the Go fixture, runs `bob dev -- true` | R-NIX-020/023, R-NIX-022 positive path |
| host `nix` never executed | `strace -f -e execve` shows no `nix` outside `<home>/bin` | R-NIX-020 |
| `max_user_namespaces = 0` | written inside a throwaway user namespace (per-namespace setting, host untouched), then `bob build` → BOB-E-7005 + exact fix text | R-NIX-022 (S0b-verified) |
| ENOSPC / EPERM / EACCES clone errors | fault hook in `bobns` (test build tag) returns each errno → the matching cause line and fix text | R-NIX-022 |
| mount EPERM inside the namespace (child exit 125) | fault hook only; the real AppArmor behaviour cannot be reproduced here (S0b) | R-NIX-022 (partial) |
| NixOS detection | rootfs with `/etc/NIXOS` + simulated ENOSPC → NixOS fix text | R-NIX-022 |
| namespace entry ≤ 10 ms | budget row (§12) | SPEC §1.17 |

### 7.2 Real-host matrix (manual / later)

| Host | Status |
|---|---|
| Ubuntu 24.04 / 26.04 with `kernel.apparmor_restrict_unprivileged_userns=1` (verifies the AppArmor fix text and the `abi <abi/4.0>` line, O-01-5) | **(manual)**, once per release, on a real Ubuntu VM or cloud instance |
| Debian with `kernel.unprivileged_userns_clone=0`, Arch `linux-hardened`, RHEL 9 with STIG `max_user_namespaces=0`, Fedora with SELinux enforcing | **(manual)** |
| amd64 hosts of all of the above | **(manual)** |

**[open]** Recommendation (BOOTSTRAP §6.7 "VMs vs containers"): **VMs**, because the property under test is
the kernel's userns policy, which a container shares with its host. Use either GitHub-hosted
`ubuntu-24.04` and `ubuntu-24.04-arm` runners (real AppArmor policy, both architectures, no cost
for public repos) or a few small cloud VMs, started by a `go run ./tools/ci -only rootless-hosts`
job. Add it after Gate H. Until then the parity catalogue lists these rows as `manual`, and
`docs/harness-validation.md` says they are not covered.

### 7.3 Suite `bundles-distro` (slow)

R-OUT-018: fixture bundles (AppImage, tarball) in unprivileged `bwrap` as bobtest on the 5
rootfs images × {userns, nouserns (`bwrap --disable-userns`)} × {arm64 native}: `api` golden
line, HTTPS 200 against the harness TLS server, locale, `date`, `iconv`. The amd64 rows run under
qemu-user where possible; amd64-native rows are **(manual)**. `HostGraphics: true` → `llvmpipe`
headless (Mesa from the pin); real GPUs **(manual)**.

### 7.4 Sandbox suite `sandbox` (gate)

`go test ./tests/sandbox/...` runs the 15 hostile cases from `tests/fixtures/evil/` (S13 §3.1)
through the evaluator sandbox with the local and the worker limits. Every case must be contained
with its exact bob message: secrets ENOENT; worker env not visible; no network (Landlock, netns);
`AF_VSOCK`, `ptrace`, `mount`, `unshare`, `io_uring`, `bpf` blocked; read-only toolchain files;
fork/thread bombs → "more than N processes or threads"; memory bomb → "used more than …";
CPU loop → "took longer than …"; disk fill; oversized graph output. The run must leave 0 `eval-*`
cgroups. **Layer-removal runs** (test build tag disables Landlock, then seccomp) show that each
case stays contained by the remaining layers, as S13 measured.

### 7.5 podman on this host

podman is not installed. The OCI suites (R-OUT OCI run, SPEC §6.7 registry pull) use rootless podman
from the nixpkgs pin as `bobtest`. **[open]** Unverified here: rootless podman needs
`newuidmap`/`newgidmap` (setuid) and `/etc/subuid` ranges for bobtest. Recommendation: Phase 3.1
installs the pinned `shadow` uidmap binaries setuid and adds a subuid range for bobtest (a host
change, logged in docs/environment.md). If that fails, the fallback is `podman --storage-driver
vfs` with a single-uid mapping (`ignore_chown_errors`). A switch to another client (skopeo) would
change a SPEC acceptance and would therefore go to the owner.

---

## 8. Server tests (BOOTSTRAP §6.8)

All server suites run against **RustFS 1.0.3** (pinned binary, §1.4), one fresh single-node
instance per suite on its own port, behind the counting proxy. SeaweedFS and Garage are not run
(D63).

### 8.1 Counting S3 proxy (`tools/s3count`)

- A reverse proxy in front of RustFS. It classifies each request by bucket, key prefix and
  operation. LIST covers `GET /<bucket>?list-type=2`, `GET /<bucket>` without a key,
  `?versions`, and `?uploads` listing. It also records the `User-Agent` suffix
  (`bob-sweep/<controller>`, R-STORE-101) and the credential (worker vs orchestrator, from the
  SigV4 access key id).
- Fault injection like S9's transport: HTTP 500/503, connection reset, response lost after the
  write, stripped `ETag`, stripped `If-Match` support (to simulate a non-compliant backend →
  BOB-E-7007), added latency. Faults are scheduled by a seeded script, so chaos runs are
  replayable.
- Counters are read at `GET /_s3count` (JSON) and reset per test. Every server e2e test ends with
  `assertNoList(t, "bob-control", "bob-fleet")`. The soak run asserts ≤ 2 sweep runs per
  controller over 2 h and 0 LISTs from other code (R-STORE-101).

### 8.2 Suites

| Suite | Command | Content |
|---|---|---|
| `store` | `go test ./tests/store/...` | R-STORE: conditional-write probes on RustFS (If-Match / If-None-Match, lost-response rule: re-read and compare owner+epoch), `store.Call` recovers every dynamitedb panic case from S9 §3.6, key builder fuzz (R-STORE key rules), start-up policy self-check (R-SEC-102), storage-backend refusal (BOB-E-7007) |
| `policy` | `go test ./tests/policy/...` | the S11 matrix on RustFS: 27 S3 operations × 6 bucket/prefix targets + 8 global/admin operations with the shipped worker and orchestrator policies; 0 deviations from the expected table. Includes **worker credentials refused on `bob-control` for all 27 operations**, and refused RustFS admin API, STS AssumeRole and CreateBucket |
| `server-e2e` | `go test ./tests/server/...` | via `tools/devstack` (§13.2): registration → active (R-FLEET-001), 300-job drain with wake-ups (R-FLEET-012), polling-only pickup, requests per job ≤ 26 (R-FLEET-009, counted), idle fleet cost (R-FLEET-010), live log pull and lake fallback (R-LOG), retention, every UI list page (R-API-151), revocation ≤ 10 s, worker as root / builds not as root (D56, BOB-E-7009) |
| `chaos` | `go test ./tests/chaos/...` | (a) R-FLEET-003: 3 orchestrators (without and with leader lease), 20 workers, 300 jobs; 20 % of workers **SIGKILLed** or paused 2×TTL+1 s; asserts 0 double acceptances, 0 lost jobs, 0 stale results accepted, 0 LIST, requeue ≤ lease_ttl + 10 s + skew. (b) R-SRV-004: `kill -9` of a random orchestrator every 20 s during a 300-job drain + 20 build triggers; every build terminal, no job without a lease, controller leases change owner ≤ 3 × interval. (c) R-SRV-101: proxy fault injection (500, reset, lost responses). (d) R-NFR-007: afterwards every record decodes and validates. Kill points are seeded and logged, so a failure is replayable with `-chaos-seed`. |
| `rbac` | `go test ./tests/rbac/...` | see §8.3 |
| `auth` | `go test ./tests/auth/...` | in-process OIDC provider `bobtest-idp` (R-AUTH-001), local users, claim → role mappings, service accounts, device flow, token revocation (R-AUTH-150/151) |
| `forge-fixtures` | `go test ./tests/forge -run Fixtures` | recorded deliveries from S16: 15 Forgejo 16.0.6 deliveries (exact bytes + headers), 8 live GitLab 19.4.2 deliveries, client-go test data and derived variants, octokit GitHub payload examples, GitHub and Standard Webhooks test vectors; tamper / missing / stale / oversize → the sentinel errors. Contract test over all adapters |
| `forge-github-replay` | `go test ./tests/forge -run GitHubReplay` | httptest TLS server answering with GitHub's documented responses; asserts JWT, `repository_ids=[id]`, `contents: read` only, status token cached. GitHub is never contacted live (O-06-4: live check **(manual)** before the first release) |
| `forge-forgejo-live` | `go test -tags live ./tests/forge -run ForgejoLive` | the harness starts Forgejo 16 from the pin (sqlite, TLS, < 1 s): hooks, statuses for all 6 states, per-job repo-scoped token (D62), clone, other repo 403, push 403, revoke, sweeper, fork PR ref, shallow fetch, webhook → BuildRecord ≤ 500 ms |
| `forge-gitlab-live` (slow, A13) | `go test -tags live,slow ./tests/forge -run GitLabLive` | GitLab 19.4.2 from the pin without NixOS (S16 `gitlab/up.sh` setup); same as Forgejo plus deploy-token expiry. About 3.8 GiB RSS and minutes to start |
| `cache-conformance` | `go test ./tests/cache/...` | real `nix` client from the pin against the server: `nix-cache-info`, narinfo, NAR substitution, signatures, private visibility (D58), the download proxy (D61), credentials (R-REG); `nix path-info --store <server>` |
| `oci-conformance` | `go test ./tests/oci/...` | rootless `podman pull` (bobtest) of the release image of `polyglot`; `podman run` → golden line, `id` → `uid=65534`; HTTPS to the harness TLS server inside the container (§7.5) |
| `soak` (slow) | `go test -tags slow ./tests/soak -timeout 3h` | 2 h of mixed load: LIST accounting (R-STORE-101), goroutine and memory growth bounded, orchestrator RSS at 10 × 1 GiB downloads ≤ 64 MiB |

Goroutine leaks: every server test package uses `goleak.VerifyTestMain` (go.uber.org/goleak, MIT).

### 8.3 RBAC matrix generated from the proto rule options

- `tools/ci gen` reads `protoregistry.GlobalFiles` after the proto build and writes
  `tests/rbac/zz_matrix_test.go`. For **every** method it emits one table row per
  combination of (principal: anonymous, authenticated without bindings, project role bound
  `@p1`, bound `@p2`, `@*`, admin, worker token, service account) × (call project p1, p2).
  The expected outcome comes from the method's `bob.authz.v1.rule` (kind, project field,
  `allow_download_scope`) and SPEC §6.4.1 evaluation, computed by a separate reference evaluator in
  the test, never by the server's own code.
- **Every RPC has a mapping (R-AUTH-006):** the generator fails if any method has no rule or
  `UNSPECIFIED`, or a `PROJECT` rule names a missing string field. A fixture service without
  options proves the check fails (also a self-validation case, §10).
- Hand-written rows on top: the ≥ 30 rows of R-AUTH-002, the admin invariants of R-AUTH-003
  (incl. two orchestrators removing the last two admin bindings concurrently), and the
  escalation cases of R-AUTH-152.
- Worker credentials: a worker token on any non-`bob.worker.v1` method → refused.

---

## 9. UI tests (BOOTSTRAP §6.9)

Suite `ui`: `go test ./tests/ui/...`.

- The browser is headless **Chromium from the nixpkgs pin**, driven from Go through the DevTools
  protocol (`github.com/chromedp/chromedp`, MIT; licence-checked like any dependency). One
  browser per test run, fixed window 1440×900 and 390×844, device scale factor 1, fonts only from
  `web/static/fonts` (bundled), animations disabled by a test-only CSS flag.
- The server is `tools/devstack` seeded with a fixed fixture data set (`tests/ui/seed.json`:
  projects, builds incl. a running one with a live log, releases, workers, users, roles). Fixed
  clock and ids, so pages render the same every time.
- **Agents verify API state first, screenshots second:** each test first asserts the page's
  data through the public API and DOM (seeded ids present, R-UI-002; filters update rows and the
  URL, R-UI-006; every `.id` cell is a link that resolves; no console errors). Only then does it
  take screenshots.
- Screenshots: every route × {light, dark} × {desktop, phone} →
  `tests/goldens/ui/<route>.<theme>.<size>.png`. Comparison: per-pixel, a difference image is
  written; tolerance ≤ 0.1 % of pixels with a channel delta > 16 (covers anti-aliasing jitter).
  The tolerance itself is protected.
- Theme: `prefers-color-scheme: dark` with empty storage → `data-theme="light"` and body
  `rgb(255, 255, 255)`; choosing Dark → `#0e0e0d`, persists after reload (R-UI-004).
- R-UI-001 static serving checks (`200.html`, cache headers) and the first-load budget
  (R-UI-007: JS ≤ 250 KiB gz, CSS ≤ 30 KiB gz, FCP from `PerformanceObserver`) also run here.
- Style review (R-UI-003) is **(manual)**, by the code steward (D45).

---

## 10. Harness self-validation (BOOTSTRAP §6.10)

Command: `go run ./tools/ci selfcheck`. For each case it makes a scratch copy of the repo
(`git worktree add` into `out/selfcheck/<case>`), applies a mutation patch from
`tools/ci/selfcheck/<case>.patch`, runs `go run ./tools/ci -only <stages>` in the copy, and
expects **exit ≠ 0 with the named suite failing for the named reason** (the expected failing test
name is part of the case). A case that passes is a harness bug, and selfcheck fails. The results
table is written to `docs/harness-validation.md` (case, mutation, expected failing test, observed
failing test, duration, date, machine). Gate H needs every row red as expected.

| # | Mutation | Must be caught by |
|---|---|---|
| 1 | drop a dependency edge in the IR → drv generator | `golden` (drv), `fixtures` (build fails / missing header) |
| 2 | leak a raw Nix error (bypass the translator for one fault) | `errors` leak check |
| 3 | break drift detection (ignore a hand-edited `go.mod`) | `errors` fault "go.mod drift" |
| 4 | skip a permission check in the interceptor for one method | `rbac` generated matrix |
| 5 | remove the rule option from one RPC | `gen` / `rbac` (R-AUTH-006) |
| 6 | let a worker read the control bucket (loosen `worker.json`) | `policy`, R-SEC-102 self-check |
| 7 | lose a log flush (drop the last batch on shutdown) | `server-e2e` log completeness |
| 8 | add a `ListObjectsV2` call in `internal/fleet` | `lint` (`nolist`) |
| 9 | the same LIST through a renamed wrapper (evades the linter) | counting proxy (`server-e2e`) |
| 10 | add `panic(...)` / `os.Exit` in `internal/` | `lint` |
| 11 | bare `go` statement without recover | `lint` (`safego`) |
| 12 | import `internal/…` from `pkg/` | `layering` |
| 13 | accept a stale completion (drop the epoch fence) | `chaos` (S9) |
| 14 | claim without CAS (plain PUT) | `chaos` double-acceptance count |
| 15 | lose the lost-response re-read rule | `store` fault test |
| 16 | remove one evaluator sandbox layer **and** the read-only bind | `sandbox` |
| 17 | let the evaluator see `AWS_*` env | `sandbox` secrets case |
| 18 | make IR depend on the checkout path | `determinism` |
| 19 | non-reproducible output (embed a timestamp) | `determinism` |
| 20 | wrong `definedAt` column | `errors` position check |
| 21 | change a code's class in `codes.lock` | `gen` |
| 22 | change an error template without a golden update | `golden` / `errors` |
| 23 | accept an unsigned webhook | `forge-fixtures` |
| 24 | job token not revoked at job end | `forge-forgejo-live` |
| 25 | userns refusal prints the wrong fix | `rootless` |
| 26 | 20 % slower no-op build (sleep injected) | `budgets` |
| 27 | edit a golden (the `protected` stage reports every change, no blocking; D68) | `protected` |
| 28 | goroutine leak in a server handler | `unit` (goleak) |
| 29 | UI page renders the wrong theme default | `ui` |
| 30 | add a GPL dependency | `licence` |
| 31 | quirks-table entry without justification (BOB-E-9006) | `unit` |
| 32 | delete a test that covers a requirement | `parity` floor |
| T1 | add `func Tool(p *bob.P, attr string, …)` back to `pkg/nixpkgs` (stringly-typed API) | `unit` (`TestRFW072TypedSurface`: unclassified raw string) |
| T2 | move a violation row (`pkg/golang.(*Module).Binary(dir)`) into the allowlist | `protected` (hash change reported) + review |
| T3 | add `Srcs: bob.Glob("../libs/mathx/src/*.c")` to `cc/app/bob.go` | `unit` (`TestRFW070FixtureLocality`) and `errors` fault 82 |
| T4 | let a root `bob.go` glob `libs/mathx/src/*.c` | `unit` (`TestRFW070FixtureLocality`), `errors` fault 83 |
| T5 | define a second `cc.StaticLibrary` in `libs/mathx/bob.go` | `unit` (`TestRFW070FixtureLocality`), `errors` fault 84 |
| T6 | accept relative `-I` flags in `pkg/cc` (`pathFlag` checks absolute paths only) | `errors` fault 85 |
| T7 | hand-edit one doc comment in `pkg/nixpkgs/attrs_gen.go` | `unit` (`TestRNIX031GeneratedSurface`: `nixpkgsgen -check`) |
| T8 | drop the `python3Packages` exclusion from `cmd/nixpkgsgen/exclude.json` | `unit` (`TestRNIX031GeneratedSurface`: excluded handle present) |
| T9 | make `**` descend into subpackages in `pkg/engine/srcs` | `unit` (`TestRFW070GlobStopsAtSubpackage`) |
| T10 | write `nixpkgs.Lib(p, "openssl")` in a documented `bob.go` | `docs` (`TestDocsPackageLocality`, after Q2) |

---

## 11. Parity catalogue (BOOTSTRAP §6.11)

- **Mapping:** each test that checks a requirement calls `parity.Covers(t, "R-FW-001", …)`
  (`tests/parity` package, test-only). At run time it appends `{id, test, result}` to
  `$BOB_PARITY_OUT` (set by `tools/ci`). A static scan (go/ast over all `_test.go` files) finds
  the same calls, so a requirement whose test did not run shows up as "not run" rather than
  silently absent. Shell/browser suites call the same API from their Go drivers.
- **Requirement list:** extracted from SPEC.md headings `#### R-<AREA>-<NNN>` (250 entries at
  this draft). An acceptance text that starts with `(manual)` or `Acceptance (manual)` makes the
  requirement `manual`. Its sign-off lives in `tests/parity/manual.json` (date, who, evidence
  link).
- **`docs/parity.md`** (generated in stage `gen` + `parity`): one row per requirement: id,
  title, SPEC section, tests, status (`pass` / `fail` / `not run` / `unmapped` / `manual` /
  `manual-signed`), plus totals per area.
- **Floor:** `tests/parity/floor` holds one integer, the number of requirements whose tests all
  pass. Stage `parity` fails if the count is below the floor. When the count rises, `tools/ci`
  writes the new floor in the same run (it may only rise). Lowering it is a protected change
  (§2.1). At Gate H the floor is the count of the stubbed harness: features return "not
  implemented", so most acceptance tests start red (BOOTSTRAP §4 Phase 3.1).
- An `unmapped` requirement is listed in the report. After Gate H, an unmapped non-manual
  requirement fails `parity`. Before Gate H it is a warning.

---

## 12. Budgets (BOOTSTRAP §6.12)

### 12.1 Budgets file

`tests/budgets/budgets.json` is **generated from the SPEC requirement entries** (Appendix B is
the review view), by `go run ./tools/ci gen` (§1.3 stage 3). The generator reads the budget
tables of SPEC §1.17, §02, §03.8, §04.10, §05.12 and §6.11 and the budget lines in acceptance texts.
A row it cannot parse fails `gen`, so the 97 Appendix B rows cannot drift silently. Format:

```json
{"v":1,"tolerance":{"time":0.10,"latency_p99":0.15,"size":0,"count":0},
 "rows":[
  {"id":"B-01-007","req":"R-NFR-001","what":"examples/polyglot no-op bob build (all stages)",
   "metric":"wall_s","stat":"median","runs":5,
   "limit":{"8c15g":0.6,"2c4g":0.8},"suite":"fast",
   "bench":"go test ./tests/bench -run BenchPolyglotNoop","busy":false}
 ]}
```

`stat` is one of `median`, `p95`, `p99`, `max`, `value`. `suite` is `fast` (runs in the gate,
must take < 2 min in total) or `slow` (only with `-slow`). Example fast rows: no-op builds of each
fixture, namespace entry, shim start, hook fast path, `bob gen`/`bob status` no-op, eval
key-hit, narinfo latency, request counts per job and 0 LIST, closure/binary/image sizes from
already-built outputs, UI bundle sizes. Cold builds, N=10,000 graphs, 100 MiB transfers, chaos
latencies, Vite/HMR and tsserver timings are `slow`.

### 12.2 Tolerance and regressions

- A row fails when its statistic is greater than `limit × (1 + tolerance)`. Time tolerance is
  10 %, latency percentiles 15 %, sizes and counts 0 (e.g. "0 LIST" and "≤ 74 MiB" are exact).
- **Regression check:** `tests/budgets/baseline-<profile>.json` holds the last accepted
  measurement per row. A row also fails when it is more than 25 % worse than its baseline, even
  while it is under the limit. An improvement updates the baseline automatically. A worse
  baseline is a protected change.
- Each result is labelled with machine and profile (BOOTSTRAP §5.8) and the 1-minute load
  average. A run with load > 1.0 before start is marked `busy`, and its timing failures are
  re-run once before they count.
- Rows marked `busy` in Appendix B (numbers from a loaded host) are re-measured on the quiet
  host in Phase 3.1 **before Gate H** (O-05-5). At Gate H the owner reviews the
  re-measured limits.

### 12.3 Profiles

- `[8c/15G]`: the full host.
- `[2c/4G]`: `go run ./tools/ci -profile 2c4g -only budgets,bench` re-executes itself as
  `systemd-run --scope -p AllowedCPUs=0-1 -p MemoryMax=4G -- <tools/ci binary> …`
  (docs/environment.md, R-NFR-008). This needs root or a user systemd instance. On this host the
  test-runner runs it as root, and the build itself still runs rootless as bobtest inside the
  scope (`setpriv` to bobtest).
- Full benchmarks: `go run ./tools/ci -slow -only bench` (both profiles, 5 runs each,
  `measure.sh`-style; results to `out/ci/<run>/bench.json`).

---

## 13. Agent-drivable (BOOTSTRAP §6.13)

### 13.1 JSON output

Suite `cli`: `go test ./tests/cli/...`. Generated from the command table of R-CLI-001: for every
`bob` and `bob-server` command it runs `--help` (golden) and the command with `--json` on a
fixture. Every stdout line must parse as schema-v1 NDJSON (§02.4) with nothing else on stdout.
Exit codes for every fault in the catalogue are checked here too. Suite `devenv`
(`go test ./tests/devenv/...`) covers the shell hook in bash 5, zsh 5.9 and fish 3.7 through a
pty, shims, `bob env`, editor settings, the LSP proxy (`lspcheck` with a `-hide-nix` client),
`bob dev` and Vite live reload. VS Code and JetBrains under `bob dev` are **(manual)** (O-02-3,
O-02-4).

### 13.2 All-in-one local dev mode

`go run ./tools/devstack up [-orchestrators N] [-workers M] [-dir D] [-json]` starts:
- RustFS (pinned) on a free loopback port, with the three buckets and the shipped policies and
  credentials applied (orchestrator, worker, admin);
- the counting proxy in front of it;
- N `bob-server orchestrator` processes (default 1) and M `bob-server worker` processes
  (default 1; workers run as root as D56 requires, builds as bobtest-style uids);
- optionally Forgejo (`-forgejo`) and the test IdP (`-idp`);
- an admin user and a token.

It prints one JSON line with URLs, ports, credentials and the state dir, and writes the same to
`<dir>/devstack.json`. `devstack down` stops everything; `devstack kill <process>` is for manual
chaos. Agents use it to test the server end-to-end, including `bob login`, `bob build --remote`,
the UI and the cache, without the owner. All server suites start their stacks through the same
package (`tests/serverenv/stack`).

**Contract of the stack** (package `tests/serverenv/stack`; `tools/devstack` is its CLI, the package
lives in the root module so test packages can import it under `GOWORK=off`):
- `devstack.json` fields: `api_url`, `api_urls` (all orchestrators), `orchestrators[i].api_url`,
  `admin_token`, `attach_proxy_url` (control API of the TCP fronts of the orchestrators: block,
  unblock, redirect, byte and stream counters), `policies_applied`, `worker_access_key`,
  `worker_secret_key`, `orchestrator_access_key`, `orchestrator_secret_key`.
- Environment of the server processes: `BOB_LISTEN`, `BOB_ADMIN_TOKEN` (orchestrators);
  `BOB_ORCHESTRATOR_ADDRS` (workers; the attach proxy fronts, entry i = orchestrator i);
  `BOB_STORAGE_WORKER_PROBE_CREDENTIAL=<access>:<secret>` (orchestrators; R-SEC-102);
  `BOB_TEST_CLOCK_OFFSET` (orchestrators, only when a test asks; honoured only by `-tags bobtest`
  builds); `BOB_WORKER_SLOTS` and other `BOB_*` settings are inherited from the test process.
- S3 credentials: `deploy/policies/*.json` are applied to RustFS through its admin API. Users
  `worker-bob` and `orchestrator-bob` hold the worker and orchestrator policies; the bucket policy
  on `bob-control` denies everyone but `orchestrator-bob`. Workers and orchestrators get their role
  keys, not the root key; the counting proxy reports the role from the key prefix.
**[open]** Recommendation: keep this as a harness tool, not a `bob-server dev` product command.
SPEC has no such command, and adding one is a product change. The R-SEC-101 runbook check
**(manual)** runs against this stack.

---

## 14. Suite index

Every suite below is also reachable as `go run ./tools/ci -only <name>`.

| # | Suite | Command | Run |
|---|---|---|---|
| 1 | `protected` | `go run ./tools/ci -only protected` | gate |
| 2 | `fmt` | `go run ./tools/ci -only fmt` | gate |
| 3 | `gen` | `go run ./tools/ci -only gen` | gate |
| 4 | `vet` | `go vet ./...` | gate |
| 5 | `lint` | `go tool -modfile=tools/go.mod staticcheck ./... && go run ./tools/lint ./...` | gate |
| 6 | `layering` | `go test ./pkg -run TestLayering` | gate |
| 7 | `licence` | `go run ./tools/ci -only licence` | gate |
| 8 | `proto` | `go tool -modfile=tools/go.mod buf lint && … buf breaking --against '.git#branch=main'` | gate |
| 9 | `web` | `go run ./tools/ci -only web` (svelte-check, deps allowlist, vite build) | gate |
| 10 | `unit` | `go test -race -shuffle=on -count=1 ./...` | gate |
| 11 | `golden` | `go test ./tests/golden/...` | gate |
| 12 | `errors` | `go test ./tests/errors/...` (replay; live capture with `-live`) | gate |
| 13 | `determinism` | `go test ./tests/determinism/...` | gate |
| 14 | `fixtures` | `go test ./tests/fixtures/...` | gate |
| 15 | `rootless` | `go test ./tests/rootless/...` (as bobtest) | gate |
| 16 | `sandbox` | `go test ./tests/sandbox/...` | gate |
| 17 | `store` | `go test ./tests/store/...` | gate |
| 18 | `policy` | `go test ./tests/policy/...` | gate |
| 19 | `server-e2e` | `go test ./tests/server/...` | gate |
| 20 | `chaos` | `go test ./tests/chaos/...` | gate |
| 21 | `rbac` | `go test ./tests/rbac/...` | gate |
| 22 | `auth` | `go test ./tests/auth/...` | gate |
| 23 | `forge-fixtures` | `go test ./tests/forge -run Fixtures` | gate |
| 24 | `forge-github-replay` | `go test ./tests/forge -run GitHubReplay` | gate |
| 25 | `forge-forgejo-live` | `go test -tags live ./tests/forge -run ForgejoLive` | gate |
| 26 | `cache-conformance` | `go test ./tests/cache/...` | gate |
| 27 | `oci-conformance` | `go test ./tests/oci/...` | gate (after §7.5 is resolved) |
| 28 | `ui` | `go test ./tests/ui/...` | gate |
| 29 | `cli` | `go test ./tests/cli/...` | gate |
| 30 | `devenv` | `go test ./tests/devenv/...` | gate |
| 31 | `bootstrap` | `scripts/bootstrap.sh` in a clean worktree (R-ARCH-005) | gate |
| 32 | `parity` | `go run ./tools/ci -only parity` | gate |
| 33 | `budgets` | `go run ./tools/ci -only budgets` | gate (fast rows) |
| 34 | `bench` | `go run ./tools/ci -slow -only bench` | slow |
| 35 | `forge-gitlab-live` | `go test -tags live,slow ./tests/forge -run GitLabLive` | slow |
| 36 | `soak` | `go test -tags slow ./tests/soak -timeout 3h` | slow |
| 37 | `fuzz-long` | `go run ./tools/ci -slow -only fuzz-long` (every `Fuzz*` target 10⁵ iterations or 60 s, whichever comes first; IR parser, lock decoders, key builders, Job, NAR/OCI/webhook parsers) | slow |
| 38 | `bundles-distro` | `go test -tags slow ./tests/bundles/...` | slow |
| 39 | `budgets-2c4g` | `go run ./tools/ci -slow -profile 2c4g -only budgets,bench` | slow |
| 40 | `selfcheck` | `go run ./tools/ci selfcheck` | before Gate H and after any change to the harness |

40 suites: 33 in the gate run, 6 slow, plus selfcheck. The typed-surface and per-package guards of
§18 run inside `unit` (tests/core), `docs` (tests/docs) and `errors` (faults 82–92).

---

## 15. Gate H

Gate H is the end of Phase 3.1 (BOOTSTRAP §4). Before asking the owner, the test-runner checks:

1. The workspace skeleton exists. Features are stubs returning "not implemented". Every suite of
   §14 exists, runs, and is red only for "not implemented" reasons. `go run ./tools/ci` produces
   a complete `report.json`; failing stages are acceptance failures, never harness errors
   (exit 2).
2. `go run ./tools/ci selfcheck` gives every row of §10 the expected red, and
   `docs/harness-validation.md` is up to date.
3. All fixtures of §4 build their parts that the harness can build without bob features (the
   recorded `bob.lock`, the trees, the registry snapshot), and their goldens have been reviewed.
4. Fault catalogue complete: `tests/faults/expect.json` has every S15 fault plus the §5.1
   additions, each with a recorded stream (replay mode is green for the translator once the
   translator task lands; at Gate H the replay harness must at least load and validate every
   entry).
5. Budgets file generated; `busy` rows re-measured on the quiet host; baselines recorded for both
   profiles.
6. `docs/parity.md` generated; every non-manual requirement mapped to ≥ 1 test; floor set.
7. Protected manifest written; the owner's signing key is in `tests/owner.allowed_signers`.
8. The list of **(manual)** and **[open]** items below is presented with status.

The owner reviews fixtures, goldens (via the §3.2 review page), budgets and the harness-validation
report. After approval, protection (§2.1) is enforced.

---

## 16. Manual and open items

**(manual)** (not verifiable on this host):
- Ubuntu ≥ 24.04 AppArmor userns refusal and the `abi <abi/4.0>` line (R-NIX-022, O-01-5).
- Other real-kernel userns policies (Debian sysctl, Arch hardened, RHEL STIG, SELinux enforcing).
- Native amd64 builds, amd64-native bundle rows, cross-machine reproducibility.
- Real GPU bundles; GitHub live check before the first release (O-06-4).
- VS Code / JetBrains under `bob dev` (O-02-3/4); UI style review (R-UI-003); R-SEC-101 runbook.

**[open]** (with recommendation):
1. Real-host rootless matrix: VMs, via GitHub-hosted x86_64 and arm64 Ubuntu runners or small
   cloud VMs, after Gate H (§7.2).
2. x86_64 runner for native amd64 builds and determinism (§4.2): same mechanism as 1.
3. Rootless podman on this host: setuid uidmap + subuid for bobtest, vfs fallback (§7.5).
4. R-ARCH-001 path rule: run it with `GOWORK=off` and allow `tests/` (§1.4, §2).
5. SPEC naming: `examples/<x>` = `tests/fixtures/repos/<x>`; `fixtures/<fault>` =
   `tests/faults/<fault>` (§2).
6. Pinned Node + `npm ci` for `web/` until bob builds it (§1.4).
7. Owner SSH signing key for protected changes (§2.1).
8. `tools/devstack` stays a harness tool, not a `bob-server dev` command (§13.2).

---

## 17. Documentation guardrail (stage `docs`)

The user documentation lives in `website/content/docs/<NN-section>/<NN-page>.md` (rendered by
the static site in `website/`). It must stay upstream of the implementation (AGENTS.md §5.9).
Stage `docs` (order 9, part of `go run ./tools/ci` and `-fast`) runs two checks.

### 17.1 `go test ./tests/docs`

Plain Go tests (no build tag, no network, no Nix; they also run in the `unit` stage):

| Test | Fails when |
|---|---|
| `TestFrontmatter` | a page lacks exactly the keys `title`, `section`, `order`, `summary`; `order` differs from the file's `NN`; a section title differs within a directory; a page has an `#` heading or does not end with `## Next steps`. Pages with `placeholder: true` (the website unit's stand-ins) are skipped. |
| `TestLinks` | a relative link names a file that is not a page, or an anchor that no heading of the target produces (anchors as the website renders them: `website/src/lib/server/markdown.ts` `slugify`); absolute and repository-file links are refused |
| `TestCLIReference` | a command or flag of the command table (`commands()` in `pkg/cli/cli.go`, read with go/ast) or a global flag (`globalFlags` in `pkg/cli/args.go`) has no `### bob <cmd>` section or flags-table row in `05-cli/01-commands.md`; the page documents a command or flag that does not exist; `## Exit codes` lacks a status that `diag.ExitCode` can return |
| `TestAPIReference` | an exported function of a public API package (`apiPackages` in `surface_test.go`: `pkg/bob`, `pkg/nixpkgs`, the ecosystem, codegen, service and output packages, `pkg/release`) is not written as `pkg.Func` in the prose of some page; a package under `pkg/output` or `pkg/services` is missing from the list; the generated page `07-reference/02-api-index.md` differs from the source |
| `TestErrorTable` | the generated page `05-cli/02-errors.md` differs from `diag.Codes()` / `diag.Lookup` |
| `TestEnvironmentVariables` | a `BOB_*` literal of the non-test, non-`bobtest` Go files under `internal/` and `cmd/bob-server/` has no table row in `06-server/03-configuration.md`; one under `pkg/` and `cmd/bob` appears on no page; a page names a `BOB_*` variable that no code reads (`BOB_TEST_*` hooks are excluded) |
| `TestServerModes` | a mode in bob-server's usage text is not documented in `06-server/` |
| `TestSnippetsTypeCheck` | the `bob.go` files of a page's example project, or a standalone `verify` block, fail `go vet -tags=bob` in a scratch module whose `github.com/megakuul/bob` is replaced by this checkout (`GOFLAGS=-mod=mod GOPROXY=off`, the repository's `go.sum`) |
| `TestDocsPackageLocality` | a documented `bob.go` (a page's `file=…/bob.go` blocks as one repository, or a `verify` block) lists a source outside its package or of a subpackage, names a Go package of another directory or a nixpkgs package by string, imports `nixext`, or defines two units in one package (§18; `tests/harness/boblint`). It waits (skips) until `pkg/nixpkgs/attrs_gen.go` exists, because the docs move to the typed API in the same change as the framework (Q2). |

Generated pages are regenerated with `BOB_DOCS_UPDATE=1 go test ./tests/docs`; review the diff.
Their whole body is generated, because the website renders raw HTML (and so HTML comments) as
text, so there is no invisible marker for a generated region.

**Markers** are words after the language in a code fence's info string; the website shows only
the language:

| Fence | Meaning |
|---|---|
| ` ```go file=cmd/hello/bob.go ` | a file of the page's example project (any language; written in order, later blocks replace earlier ones) |
| ` ```go verify ` | a complete `bob.go` file, type-checked on its own |
| ` ```bash run ` (optionally `exit=<n>`) | commands the end-to-end test runs in the project directory |
| ` ```text expect ` | lines the preceding run block must print, in order; `…` matches any text, a line of only `…` is skipped, white space runs compare equal |

### 17.2 End-to-end run of the documented projects (opt-in)

`TestQuickstartsE2E` builds every page's example project with a real bob: an empty git
repository, the `file=` blocks written, the `run` blocks executed with `bash -e`, the `expect`
blocks matched. It needs network and minutes per page, so it is skipped unless
`BOB_DOCS_E2E` names a bob binary:

```
BOB_DOCS_E2E=$PWD/bob BOB_HOME=/var/tmp/docs-home BOB_DOCS_E2E_DIR=/var/tmp/docs-e2e \
  go test ./tests/docs -run TestQuickstartsE2E -v -timeout 3h          # default: ^03-quickstart/
BOB_DOCS_E2E_PAGES='^04-guides/0[36]' …                                 # other pages
```

Use a fresh `BOB_HOME` (outputs then say `built`, not `cached`). Background processes a block
starts are killed when its page ends.

### 17.3 Change-coupling rule

After the tests, the stage compares the tree with a base commit: `$BOB_DOCS_BASE`, else the
merge base of `HEAD` with the newest `docs-verified*` tag, `origin/HEAD`, `main` or `master`;
on the default branch itself the base is `HEAD` when the work tree is dirty, else `HEAD~1`.
Committed, uncommitted and untracked files count. git runs read-only
(`GIT_OPTIONAL_LOCKS=0`). Without git or history the rule is skipped with a notice.

The rule fails when the change touches a public surface and nothing under
`website/content/docs/`:

- `pkg/cli/cli.go`, `pkg/cli/args.go`, `pkg/bob/diag/codes.go`, `cmd/bob-server/main.go`,
  `SPEC.md`, `spec/*.md`;
- non-test Go files directly in the public API packages (`apiDirs` in
  `tools/ci/stage_docs.go`);
- non-test Go files under `internal/` and `cmd/bob-server/` whose changed lines add or remove a
  `"BOB_…` literal.

An exemption is a line `docs-exempt: <reason>` in a commit message since the base, or an added
line of `plan/decisions.md` or a `plan/progress/*.md` file. Tagging a commit `docs-verified-<n>`
after a docs review narrows the base for later changes.

### 17.4 Validation

Mutation results are in `docs/harness-validation.md` §4.

---

## 18. Typed surface and per-package build modules (ADR 0020, owner D71/D72, 2026-10-24)

The owner, verbatim: "I think in the harness I did absolutely specify that everything must be
typesafe... nixpkgs.Tool(p, "gcc") for example is 100% not typesafe... there must be a generator
that builds a go prop for every nixpkgs package... i want that cpp is also split up into modules
same for other systems so you create sub 'bob' files for each package to group it not a central
one where you hardcode all cpp files used... Please rewrite the harness if it doesnt already
clearly say this by force". These two rules are **non-negotiable** (AGENTS.md §5.10). The harness
enforces them with guards that do not depend on a working build, so they hold in every stage:

| Guard | Test / location | Requirement | Fails (hard) on | Red (tolerated) while |
|---|---|---|---|---|
| typed public API | `TestRFW072TypedSurface` (`tests/core/typesafe_test.go`) | R-FW-072, R-FW-010 | any exported parameter/field of type `string`, `[]string`, `...string`, `map[string]T` in the L0–L3 packages that the protected allowlist does not classify (categories of SPEC §1.4.5); stale allowlist rows | an audited violation (`typedSurfaceViolations`, docs/review/typesafety-audit.md V01–V31) still exists |
| generated nixpkgs surface | `TestRNIX031GeneratedSurface` | R-NIX-031, R-FW-032 | wrong header, no `//go:generate`, a handle without the `<Name> is <attr> <version>: … Licence: …` doc, a missing required handle, an excluded set present, string role functions present, `go run ./cmd/nixpkgsgen -check` failing (offline regeneration differs, snapshot rev ≠ `lock.DefaultRev`, handle not in the pinned index) | `pkg/nixpkgs/attrs_gen.go` does not exist yet |
| LSP | S1 script steps `complete libs/foo/bob.go "nixpkgs.Op"` → `Openssl`, hover with `Licence:` (`TestRFW001…`, tests/core/bobgo_test.go) | R-NIX-031, R-FW-001 | completion/hover missing | the surface does not exist |
| fixture locality | `TestRFW070FixtureLocality` | R-FW-070, R-FW-071, R-CC-006 | any `bob.go` of `tests/fixtures/repos`, `tests/{core,cli,devenv}/testdata/repos` (except the fault-by-design repos listed in `lintExempt` with reasons) with a `..`/absolute/subpackage path in `bob.Glob/Files/Dir/IncludeDir`, a string argument to a Go `Module` method, a string nixpkgs attribute, a `nixext` import, or two units of one ecosystem in one package | never (green since Q1) |
| linter self-check | `TestBoblintCatchesViolations` | — | the linter misses one violation class or flags a valid repository | never |
| `**` stops at subpackages | `TestRFW070GlobStopsAtSubpackage` (cc fixture) | R-FW-070 | a change below `libs/mathx` changes the root step's sources, or a change in a plain subdirectory does not | `build.SourcePaths` / the new API missing |
| typed slots | `TestRFW071Slots` (repos `define/slot`, `define/slot2`) | R-FW-071 | slot value not the bound constructor's, missing errors for unbound/twice bound | `bob.Slot` missing (the repo does not compile) |
| bob.go rules | `TestRFW002BobGoRules` cases `r7nixext`, `r8` | R-FW-002 R7/R8 | wrong text | rules not implemented |
| documented projects | `TestDocsPackageLocality` (tests/docs) | R-FW-070/071, R-NIX-031 | any documented `bob.go` violating the rules | skipped until the surface exists (docs migrate with it) |
| error quality | faults 82–92 (§5.1) | R-FW-070/071, R-CC-006, R-NIX-031/032, R-FW-002 | wrong code, position, hint, raw Nix text | the feature answers BOB-E-9008 |
| budgets | rows B-098…B-103 (`tests/budgets/budgets.json`, SPEC Appendix B) | R-NIX-031 | regression beyond tolerance once measured | `status: proposal`, not measured |

The shared linter lives in `tests/harness/boblint` (go/ast only: no type checking, no bob binary).
Mutation checks T1–T10 are in §10 and `docs/harness-validation.md` §5. The allowlist and the
violation list are protected (§2.1): moving a row from violation to allowed is a protected change
that the `protected` stage reports to the owner.

