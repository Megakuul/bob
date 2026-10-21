# bob — Requirements (Stage 1)

Status: **Stage 1 draft, for owner review** (2026-10-20).
Module: `github.com/megakuul/bob` · License: **AGPL-3.0** (owner, 2026-10-20).

This document records **what** bob must do and **why**. It is the input for Stage 2, which turns
it into an exact specification (`SPEC.md`) with testable acceptance criteria, measured budgets and
locked stack decisions (ADRs), using spikes and proofs of concept. Process, gates and guardrails
for the agents are in [`BOOTSTRAP.md`](BOOTSTRAP.md).

Conventions
- Requirement ids `R-<AREA>-NNN`. Stage 2 keeps the ids and adds acceptance criteria; it may
  split a requirement (`R-GO-004a`) but never silently drop one.
- **MUST** = v1. **SHOULD** = v1 unless Stage 2 shows a cost the owner should weigh.
  **ROADMAP** = designed for, not built in v1.
- `[D-n]` points to the owner decision log (§24). `[open]` marks something Stage 2 must decide
  (by spike or by asking the owner at the start of Stage 2).
- "Owner" = the human this project is built for. "Developer" = someone writing `bob.go` files.
  "Consumer" = someone installing released software with `bob install`.

---

## 1. Vision

Nix is the best build and packaging engine available: pinned, reproducible dependency graphs,
dynamic linking without version conflicts, software that *requires* its dependencies instead of
installing them, and one mechanism for every language. But the Nix language is complex, its
tooling and LSP support are weak, its errors are hostile, and it is not high level.

**bob makes Nix the invisible engine of a company's whole build pipeline.** Developers describe
their software in **typed Go** (`bob.go` files) using high-level, premade building blocks for
the languages and ecosystems used in production. bob turns that into Nix builds, splits software
into many fine-grained packages for caching, and gives developers dev shells, editor tooling and
remote builds. A separate server (same repository, decoupled from the framework) runs the CI
pipeline on a worker fleet, acts as a Nix binary cache, OCI registry and release host, and offers
a clean web UI.

bob **replaces** the native build systems of the languages it supports (go.mod as source of
truth, Cargo, CMake, npm/pnpm). It is not a dev-environment layer on top of them. Native files
still exist where tooling needs them, but they are generated.

Two products, one monorepo:

| Product | Location | Knows about the other? |
|---|---|---|
| **Framework** — Go library + `bob` CLI: build definitions, Nix backend, dev shells, install | `pkg/` (+ `cmd/bob`) | **No.** The framework never imports server code and has no concept of "the bob server". |
| **Build server** — orchestrators, workers, cache, registry, releases, API, web UI | `internal/`, `cmd/…`, `proto/`, `web/` | Yes (it uses the framework). |

The `bob` CLI is the only place where both meet (e.g. `bob login`, `bob build --remote`), and it
does so through generic interfaces (substituter, remote executor) the framework exposes.

---

## 2. Users and core use cases

- **Developer:** writes `bob.go`, runs `bob build`, `bob test`, `bob dev`; gets cached results from
  the company server; never writes Nix, CMake, Cargo manifests or package.json by hand.
- **Platform team / admin:** runs the server (HA, S3 only), manages users, roles, workers,
  retention, forge integrations.
- **Consumer:** `bob install acme/api@1.4` from a server's registry (public or authenticated);
  never deals with the build system at all. Non-Nix systems can use AppImages or OCI images.

---

## 3. Platform & scope (PLAT)

- **R-PLAT-001 (MUST)** Supported systems: **Linux x86_64 and Linux aarch64** only, for the
  framework, CLI, workers and server. [D4]
- **R-PLAT-002 (MUST)** No design decision may make darwin impossible later, but no effort is
  spent on it now.
- **R-PLAT-003 (MUST)** Single tenant: one server installation per company. No multi-tenancy. [D14]

---

## 4. Repository & architecture (ARCH)

- **R-ARCH-001 (MUST)** One Go monorepo `github.com/megakuul/bob`:
  - `pkg/` — the public framework (importable by users' `bob.go`).
  - `cmd/` — executables (`bob`, and the server binaries; see R-ARCH-004).
  - `internal/` — server, worker, storage, auth, forge integrations.
  - `proto/` — buf modules for the connectrpc API.
  - `web/` — SvelteKit app (static adapter).
- **R-ARCH-002 (MUST)** `pkg/` never imports `internal/`, `proto/`-generated server packages, or
  anything server-specific. Enforced by an automated check in CI.
- **R-ARCH-003 (MUST)** The framework is layered internally (proposal; Stage 2 finalises in an
  ADR): core graph/IR → Nix backend → language/ecosystem builders → outputs/bundles → CLI glue.
  Lower layers do not import higher ones (CI-enforced).
- **R-ARCH-004 (MUST)** Two separate binaries: **`bob`** (developer/consumer tooling: framework
  CLI, dev shells, install) and **`bob-server`** (orchestrator and worker modes). The server has
  nothing to do with user tooling; `bob` contains no server code. [D36]
- **R-ARCH-005 (MUST)** bob **builds itself with bob** as soon as its build needs anything
  `go build` cannot provide (e.g. an embedded Nix/Lix binary, the web UI bundle). A documented
  bootstrap path exists for the very first build. [D17]

---

## 5. Nix backend (NIX)

### 5.1 Generation strategy

- **R-NIX-001 (MUST)** The owner chooses between two strategies at **Gate 0**, based on a
  measured spike that implements both on the same mixed graph [D1]:
  - **A′ — data + bob-owned Nix library:** the Go side emits only data (JSON graph); a fixed,
    versioned Nix library shipped inside bob turns it into nixpkgs calls. Generated per-project
    Nix code does not exist, so evaluation errors can only be bugs in bob's library.
  - **B — direct derivations:** bob computes `.drv` files itself and hands them to the Nix
    store/daemon. nixpkgs is used as a package source (derivations of `openssl`, `gcc`, … looked
    up once per pinned revision). bob owns its builders and fixup phase.
  - Comparison criteria (minimum): evaluation + build time at ~10, ~100, ~1,000 packages;
    implementation effort; error quality; behaviour when nixpkgs is bumped one release; cache
    compatibility; how much of nixpkgs' builder logic must be reimplemented.
- **R-NIX-002 (MUST)** Whatever strategy wins: **every nixpkgs package and every Nix store
  output remains usable**. Uncached derivations are built by Nix as usual. Losing access to
  nixpkgs is not acceptable.
- **R-NIX-003 (MUST)** Outputs are ordinary Nix store paths, compatible with standard binary
  caches (`narinfo`/`nar`) and with cache.nixos.org substitution.

### 5.2 Nix never leaks

- **R-NIX-010 (MUST)** **No raw Nix evaluation error ever reaches the user.** Every Nix
  evaluation error a user can trigger is a bob bug and gets a regression test.
- **R-NIX-011 (MUST)** Errors are checked in Go *before* Nix runs wherever possible: unknown
  nixpkgs attribute, package marked broken/insecure/unfree, unsupported platform, dependency
  cycle, stale lock entry, missing hash.
- **R-NIX-012 (MUST)** Build failures (compiler errors, test failures, Nix-specific failures like
  hash mismatches) are reported in bob terms: the bob package label, **the Go source position
  where the package was defined**, the relevant log excerpt, and a hint where one is known. Raw
  Nix output only with `-v` / `bob log`.
- **R-NIX-013 (MUST)** The public API contains no Nix concepts except `Nixpkgs` as a package
  source. Users never see a `.nix` file in their project. Flakes may be used internally. [D6]

### 5.3 Embedded, rootless Nix

- **R-NIX-020 (MUST)** `bob` is the **only executable** a developer or consumer installs. Nix
  (or Lix) is embedded or bundled; nothing else is required on the machine. [D6]
- **R-NIX-021 (MUST)** bob works **fully rootless**. The store must live at `/nix/store`
  (store paths are part of the hashes; relocating it would make every binary cache useless), so
  bob provides it through unprivileged user namespaces (nix-portable-style approach; Stage 2
  spikes the mechanism). [D11]
- **R-NIX-022 (MUST)** If unprivileged user namespaces are unavailable (e.g. distro security
  policy), bob **refuses to run** and prints the exact one-time admin fix for that system. No slow
  fallback. [D11]
- **R-NIX-023 [open]** Nix vs. Lix: decided in Stage 2 by evidence (static/embeddable build,
  required store/daemon features, error quality, licence: both LGPL-2.1 — check embedding
  obligations under AGPL-3.0 bob).
- **R-NIX-024 (MUST)** Because bob owns its store and Nix configuration, it configures
  substituters, trusted keys and authentication itself. No `/etc/nix/nix.conf` edits, no
  trusted-user setup.
- **R-NIX-025 (SHOULD)** On machines that already have a system Nix, bob does not interfere with
  it (separate state, no shared config).

---

## 6. Framework core & API (FW)

### 6.1 Build definitions

- **R-FW-001 (MUST)** Builds are defined in Go files named `bob.go`. They are ordinary, typed Go
  that compiles with the standard toolchain and works with gopls (completion, go-to-definition,
  type errors).
- **R-FW-002 (MUST)** `bob.go` files live **next to the code they describe** (one per
  directory), if a Stage 2 spike shows this works cleanly with Go's one-package-per-directory rule
  and with gopls. **Fallback:** one build module `bob/` at the repo root that mirrors the tree.
  [D22]
- **R-FW-003 (MUST)** `bob.go` + `bob.lock` are the **single source of truth** for packages,
  dependencies, versions, toolchains, tests, variants, pipelines, releases and dev shells. [D2]
- **R-FW-004 (MUST)** `bob.lock` holds only resolved pins and hashes (nixpkgs revision, external
  sources, ecosystem dependency hashes). It is generated and updated by bob, never edited by hand.
- **R-FW-005 (MUST)** Dependencies are added by **editing `bob.go` by hand** (no `bob add` in
  v1). bob then resolves and updates `bob.lock`. [D26]
- **R-FW-006 (MUST)** Native ecosystem files (`go.mod`, `go.sum`, `Cargo.toml`, `Cargo.lock`,
  `package.json`, `tsconfig.json`, dune/opam files, `compile_commands.json`, …) are **generated**
  so native tooling works. If they are edited by hand (e.g. `go get`), bob **detects the drift
  and rejects it** with a message pointing to the `bob.go` change to make instead. [D2]
- **R-FW-007 (MUST)** Generated code (protobuf/connect stubs, GraphQL/OpenAPI clients, …) is
  **materialised into the working tree** at predictable paths and gitignored, so gopls/tsserver
  and other tooling see it. [D25]
- **R-FW-008 (MUST)** Evaluating build definitions is deterministic: the same sources + lock
  produce the identical package graph. The `bob` API is the only sanctioned input (it hashes
  sources and reads the lock); a check catches non-determinism (evaluate twice, compare).
- **R-FW-009 (MUST)** The framework and `bob` CLI are **fully functional without a bob server**:
  they substitute from cache.nixos.org and any configured standard Nix binary cache. A bob
  server's cache is one optional substituter among others, never a requirement. [D37]

### 6.2 API design principles

- **R-FW-010 (MUST)** **Values, not strings:** packages and their outputs are typed Go values
  (e.g. `Shared()` returns a linkable library type; passing a binary where a library is expected
  fails to compile). Strings appear only as CLI labels.
- **R-FW-011 (MUST)** Cross-package references use **memoised constructor functions**
  (`foo.Lib(p)` returns the same package each time it is called). [D23]
- **R-FW-012 (MUST)** API style is **builder chaining or option structs, whichever reads more
  naturally** for the case. The bar: it must feel intuitive. [D24]
- **R-FW-013 (MUST)** API usability is tested: a set of reviewed **example programs** (a C++
  library, a Go service with cgo, a Rust crate, a Svelte frontend, a polyglot monorepo,
  cross-repo use) must compile, build, and read well. They are the acceptance tests for API
  ergonomics and serve as documentation.
- **R-FW-014 (MUST)** One obvious way per task: each ecosystem package offers one constructor
  per kind (library, binary, test, …).
- **R-FW-015 (MUST)** Package labels for the CLI: `//path/to/dir:name`, with `//...` patterns.
- **R-FW-016 (MUST)** Every package records the source position of its definition (used by
  R-NIX-012 and the UI).

### 6.3 Graph features

- **R-FW-020 (MUST)** Fine-grained packages: software is split into many derivations (per module,
  crate, library, codegen step, …) so changes rebuild little and caches hit often. Granularity
  per ecosystem is decided in Stage 2 by measurement (e.g. per Go module vs. per Go package).
- **R-FW-021 (MUST)** Sources: directories and globs from the repo, with content hashing that
  ignores generated and gitignored files.
- **R-FW-022 (MUST)** External inputs: nixpkgs packages (pinned revision), other derivations, and
  other bob repositories. **Cross-repo use goes through Go modules**: a bob repo exports its build
  packages as a Go module; consumers import it in their build definitions, getting typed handles
  and Go-module versioning.
- **R-FW-023 (MUST)** Hermetic custom step `bob.Run` (declared tools, inputs, command, outputs;
  sandboxed and cached) for anything without a dedicated builder. **No raw-Nix escape hatch.** [D27]
- **R-FW-024 (MUST)** Tests are packages: running a test is a build of a test derivation; results
  are cached.
- **R-FW-025 (MUST)** Hermetic integration tests may start services from nixpkgs (e.g. Postgres)
  on loopback inside the sandbox (e.g. `bob.Services(...)`). Stage 2 verifies loopback inside the
  sandbox.

### 6.4 Platforms and variants

- **R-FW-030 (MUST)** Packages are platform-generic: defined once and built for
  `linux/amd64` or `linux/arm64`, natively or **cross-compiled**. [D3]
- **R-FW-031 (MUST)** **Build variants** in addition to platforms: at least debug/release
  profiles, feature flags, sanitiser builds. A variant changes the package's identity (cache key)
  and propagates to dependencies where meaningful. [D28]

### 6.5 Pipelines (framework concept)

- **R-FW-040 (MUST)** Pipelines are defined in `bob.go`: triggers (pull request, push to branch,
  tag pattern, …) and what runs (checks: tests and lints; releases). Pipelines are a framework
  concept; the server is one executor of them.
- **R-FW-041 (MUST)** `bob pipeline run <name>` runs the same pipeline locally.
- **R-FW-042 (ROADMAP)** Impure steps (tests against external systems, secrets, hardware): not in
  v1; evaluating a solution is a roadmap item. [D29]

---

## 7. Ecosystems (LANG)

Principle: the work is the **packaging ecosystem**, not just the compiler. Where an ecosystem has
its own package registry (Go, Rust, npm, OCaml), bob maps it onto its own packages; native
package managers and build tools are not used as build drivers (they may run inside a derivation
as a compiler driver only where Stage 2 shows this is necessary and still fine-grained).
Prior art (gobuild.nix, gomod2nix, crane, crate2nix, opam-nix, …) is studied, not copied (see
BOOTSTRAP §cleanroom).

### 7.1 C / C++ (CC)
- **R-CC-001 (MUST)** Libraries (static/shared), binaries, tests. Dependencies come from nixpkgs or
  other bob packages. No CMake/Meson/Make in user projects.
- **R-CC-002 (MUST)** Compiler, language standard, flags, defines, include paths, linking as
  typed options; per-variant flags (sanitisers, debug).
- **R-CC-003 (MUST)** Generates `compile_commands.json` for clangd.
- **R-CC-004 (SHOULD)** Granularity finer than one derivation per library (e.g. per object file)
  if Stage 2 shows a net benefit.
- **R-CC-005 (MUST)** `cc.External`: a third-party C/C++ library that is not in nixpkgs and ships
  its own CMake/autotools/Meson is fetched and built with its own build system **inside a
  derivation**. To the user it is just another package; the user's project never contains CMake
  or similar files. The fetched source is a fixed-output input **verified by its Nix hash
  (narHash), pinned in `bob.lock`**; an unpinned or mismatching source fails before the build
  with a bob error. [D44]

### 7.2 Go (GO)
- **R-GO-001 (MUST)** Modules, binaries, libraries, tests, cgo, cross-compilation.
- **R-GO-002 (MUST)** Third-party Go modules declared in `bob.go` with exact versions; fetched as
  fixed-output derivations; built as fine-grained packages (per module or per package, e.g. via
  `GOCACHEPROG` as in gobuild.nix — Stage 2 decides).
- **R-GO-003 (MUST)** Generated `go.mod`/`go.sum` keep `go` tooling and gopls working; drift is
  rejected (R-FW-006).
- **R-GO-004 (MUST)** Go toolchain version per module, from nixpkgs or pinned.

### 7.3 Rust (RS)
- **R-RS-001 (MUST)** Crates (lib, bin, staticlib, cdylib), tests, features, cross-compilation,
  linking with C/C++ and Go (cgo) packages.
- **R-RS-002 (MUST)** crates.io dependencies declared in `bob.go`; one derivation per crate.
- **R-RS-003 (MUST)** Generated `Cargo.toml`/`Cargo.lock` keep rust-analyzer working.
- **R-RS-004 (MUST)** Rust toolchain version per package.

### 7.4 TypeScript / npm (NPM)
- **R-NPM-001 (MUST)** npm registry packages are declared in `bob.go`. bob's **own resolver**
  resolves them (semver, peer and optional dependencies, platform-specific optional binaries) and
  pins exact versions + integrity in `bob.lock`. **The npm/pnpm/yarn CLIs are not used.** [D15]
- **R-NPM-002 (MUST)** Each tarball is a fixed-output derivation keyed by its integrity hash.
- **R-NPM-003 (MUST)** `node_modules` is built from the store with an **isolated (pnpm-style,
  strict) layout by default**, with per-package hoisting as an explicit fallback. [D30]
- **R-NPM-004 (MUST)** Lifecycle scripts (`install`, `postinstall`, node-gyp) are **denied by
  default**; allowed explicitly per package in `bob.go`; always run sandboxed (bob provides the C
  toolchain for native add-ons). [D31]
- **R-NPM-005 (MUST)** A small, curated **quirks table** shipped with bob handles packages that
  misbehave (e.g. download binaries in `postinstall`) by mapping them to nixpkgs. It is a last
  resort with tests per entry, and must not grow into "we handle everything there". Every new
  entry needs a justification in the table. [D32]
- **R-NPM-006 (MUST)** Workspace packages: internal TypeScript libraries are bob packages, linked
  into consumers' `node_modules` under their package name.
- **R-NPM-007 (MUST)** Builders for TypeScript compilation, bundling and **frontend builds (Vite,
  SvelteKit)**, as hermetic derivations.
- **R-NPM-008 (MUST)** `package.json` and `tsconfig.json` are generated from `bob.go`.
  `vite.config.ts`, `svelte.config.js` and similar config-that-is-code stay **user-owned files**.
  [D33]
- **R-NPM-009 (MUST)** Runtime is **Node**; features Node cannot provide are unsupported.
  **Bun is selectable as the runtime of a final output** (`Runtime: Bun`). [D15]
- **R-NPM-010 (MUST)** Live reload: `bob dev` runs the dev server (Vite) against the working tree
  with HMR; dependency changes in `bob.go` re-materialise `node_modules` and restart the dev
  server; codegen outputs regenerate on change and reach the dev server. [D34]

### 7.5 OCaml (ML)
- **R-ML-001 (MUST)** Libraries, executables and tests; opam packages declared in `bob.go` and
  built as fine-grained packages; generated dune/opam files keep ocaml-lsp working.

### 7.6 Code generators (GEN)
- **R-GEN-001 (MUST)** protobuf with buf and connectrpc (Go, TypeScript at minimum; others where
  the target language is supported).
- **R-GEN-002 (MUST)** GraphQL (e.g. gqlgen for Go, graphql-codegen for TypeScript).
- **R-GEN-003 (MUST)** OpenAPI / REST (e.g. oapi-codegen for Go, a TypeScript client generator).
- **R-GEN-004 (MUST)** Each codegen step is its own cached package and a typed input to other
  packages; output is materialised (R-FW-007).

### 7.7 Further ecosystems (ROADMAP, owner may strike)
Python (pyproject/uv), Zig, Java/Kotlin (Maven/Gradle, high effort), shell scripts with declared
runtime deps, and maybe C#/.NET, Haskell, Elixir, Swift, Dart, PHP, Ruby. The framework design
must make adding an ecosystem a self-contained package under `pkg/`.

---

## 8. Outputs & bundles (OUT)

- **R-OUT-001 (MUST)** Nix closure (installable with `bob install`, copyable to a cache).
- **R-OUT-002 (MUST)** **OCI images** built directly from the closure, multi-platform
  (amd64+arm64 index), no Dockerfile, no Docker daemon. [D3]
- **R-OUT-003 (MUST)** **AppImage** for the final binary on non-Nix systems, bundling all runtime
  dependencies (dynamic loader, libraries, data). Must run on mainstream distros without Nix.
  Stage 2 identifies the hard cases (glibc, OpenGL/Vulkan drivers, locale, certificates).
- **R-OUT-004 (SHOULD)** Plain tarball of a closure or a static binary.
- **R-OUT-005 (ROADMAP)** deb/rpm.

---

## 9. Developer experience (DEV)

- **R-DEV-001 (MUST)** Dev shells defined in `bob.go` (toolchains of chosen packages, extra
  packages, env vars, hooks), entered with `bob dev`.
- **R-DEV-002 (MUST)** `bob dev` also prepares the working tree for tooling: generated native
  files, materialised codegen and `node_modules`, and runs the dev servers the shell declares
  (e.g. Vite) with live reload. [D34]
- **R-DEV-003 (SHOULD)** direnv-style automatic activation per directory.
- **R-DEV-004 (MUST)** When a bob server is used, cache setup is automatic after `bob login`
  (R-CLI): substituters, keys and tokens configured in bob's own Nix configuration. Without a
  server, bob works with cache.nixos.org and any configured binary caches (R-FW-009).
- **R-DEV-005 (MUST)** Developers **never push** to the shared cache. Shared, cached results come
  from **`bob build --remote`**, which runs the build on the fleet. [D9]
- **R-DEV-006 (MUST)** Developer-visible performance (Stage 2 sets budgets, see §20): no-op build,
  incremental build after a one-file change, `bob dev` entry time, time to first build on a fresh
  machine with a warm server cache.

---

## 10. CLI (CLI)

- **R-CLI-001 (MUST)** Commands at least: `build`, `test`, `run`, `dev`, `check` (drift and lock
  consistency), `graph` (incl. "why does X depend on Y"), `log`, `pipeline run`, `lock` (update
  pins), `login`/`logout`, `install`/`uninstall`/`upgrade`/`list` (R-INST), `--remote` for build
  and test.
- **R-CLI-002 (MUST)** Output is concise and readable by default; `-v` for raw detail; a JSON
  output mode for scripts and agents.
- **R-CLI-003 (MUST)** `bob login`: OAuth (authorization code with PKCE or device flow) against
  the server, which in turn uses OIDC or local users (R-AUTH). Tokens are stored per user and
  refreshed.
- **R-CLI-004 (SHOULD)** Shell completion including package labels.

---

## 11. Installing software (INST)

- **R-INST-001 (MUST)** `bob install <project>/<release>@<version>` installs released software
  from a bob server's registry into a per-user bob profile, with upgrade, rollback, uninstall and
  list. [D10]
- **R-INST-002 (MUST)** Works on machines without a system Nix (through bob's rootless store,
  R-NIX-021) and does not require the consumer to understand any build system.
- **R-INST-003 (MUST)** Public releases install without authentication; private ones need
  `bob login` or a service-account token.
- **R-INST-004 (SHOULD)** Several registries configurable (e.g. company + a public one).

---

## 12. Server overview (SRV)

- **R-SRV-001 (MUST)** The server runs the **whole pipeline** except source hosting: receives
  forge events, schedules, evaluates, builds, tests, caches, publishes releases and images, and
  reports status back to the forge. **No deployment** of built software, **no infrastructure
  secrets management**. [D5]
- **R-SRV-002 (MUST)** Roles: **orchestrators** (API, UI, reconciliation, cache/registry serving,
  signing) and **workers** (evaluation and builds).
- **R-SRV-003 (MUST)** **High availability and horizontal scaling**: any number of orchestrators
  and workers; losing any single node loses no data and no accepted work. [D7]
- **R-SRV-004 (MUST)** **Reconciliation design** (Kubernetes-style): desired and observed state
  live in the database; controllers on every orchestrator converge it; every state transition is
  a conditional (compare-and-swap) write, so concurrent orchestrators are safe without a
  coordinator. [D7]
- **R-SRV-005 (MUST)** Orchestrators do **not need to talk to each other**. Direct
  orchestrator-to-orchestrator connections are allowed only as an **optional** optimisation
  (live logs, R-LOG-002). [D7, D12]
- **R-SRV-006 (MUST)** User code (`bob.go` evaluation and all builds) runs **only on workers**,
  never inside an orchestrator.
- **R-SRV-007 (MUST)** The server is configurable by file and environment variables, deployable
  as a binary or an OCI image built by bob itself, with documented S3 bucket policies (R-SEC-002).

---

## 13. Storage (STORE)

- **R-STORE-001 (MUST)** **S3-compatible object storage is the only storage dependency.** No
  Postgres, no Redis, no message broker. [D8]
- **R-STORE-002 (MUST)** Buckets:
  | Bucket | Engine | Access | Contents |
  |---|---|---|---|
  | **control** | dynamitedb | orchestrators only | users, roles, permissions, OIDC mappings, service accounts, tokens, projects, forge integration credentials, settings |
  | **fleet** | dynamitedb | orchestrators + workers | worker registrations, job queue, leases, job status, worker token exchange |
  | **analytics** | lake | orchestrators + workers (read/write; workers write logs and metrics directly) [D38] | build logs, metrics, history |
  | **artifacts** | plain S3 | orchestrators; workers write `staging/` only | Nix cache (`narinfo`/`nar`), OCI blobs/manifests, release assets |
  [D8, D13]
- **R-STORE-003 (MUST)** Use **dynamitedb and lake as they are today**. Do not design for a
  future version. Upstream changes only for real bugs or practically missing features found
  during the work, and then as separate, minimal contributions. [D8]
- **R-STORE-004 (MUST)** Data models are designed around dynamitedb's partition/sort-key access
  patterns (e.g. time-sortable ids); list views in the UI must not depend on full-bucket scans.
- **R-STORE-005 (MUST)** dynamitedb panics on operator misuse (per its README). bob wraps all
  calls so a panic can never crash an orchestrator or worker, and tests every query/update shape.
- **R-STORE-006 (MUST)** Supported S3 backends need conditional writes (dynamitedb README:
  e.g. RustFS, SeaweedFS, Ceph RGW; **not Garage**). The test harness runs against at least one.
- **R-STORE-007 (MUST)** Retention and garbage collection for cache, logs, artifacts and
  analytics, configurable; releases are never collected unless explicitly deleted.

---

## 14. Fleet & scheduling (FLEET)

- **R-FLEET-001 (MUST)** **Zero-setup worker registration through the fleet bucket.** Having
  credentials for the fleet bucket is the proof of being a worker. A starting worker writes a
  registration request; an orchestrator reconciles it and writes back a token for connecting to
  the orchestrators. No admin step. [D13′]
- **R-FLEET-002 (MUST)** Workers cannot impersonate each other: the registration request carries
  a public key generated by the worker; the token is written **encrypted to that key**; the worker
  proves possession of the private key when connecting.
- **R-FLEET-003 (MUST)** Workers claim jobs with conditional writes and renew a lease; a dead
  worker's lease expires and the job is requeued. Duplicate execution must be harmless (builds are
  idempotent; results are content-addressed).
- **R-FLEET-004 (MUST)** Scheduling respects platform (amd64/arm64), capabilities and labels.
- **R-FLEET-005 [open]** Scheduling unit: one job per target closure on one worker vs. splitting
  the graph into single derivations across the fleet. Stage 2 decides by measurement; the design
  must allow the finer model later.
- **R-FLEET-006 (MUST)** Workers get forge checkout credentials **only for the job they hold**,
  scoped to that repository and short-lived.
- **R-FLEET-007 (MUST)** Workers are not provisioned or autoscaled by bob (no infrastructure
  management); they appear and disappear and the system adapts. [D5]
- **R-FLEET-008 (MUST)** Evaluating `bob.go` runs arbitrary Go: it runs on workers in a sandbox
  (Stage 2 defines the isolation level).

---

## 15. Cache, registry & releases (REG)

- **R-REG-001 (MUST)** Nix binary cache protocol (`narinfo`/`nar`) served by orchestrators, so
  Nix substitutes natively. Authentication for private projects; public releases' closures
  readable anonymously.
- **R-REG-002 (MUST)** **Signing stays on orchestrators.** Workers upload to `staging/`; an
  orchestrator verifies, signs and promotes. Workers never hold the signing key.
- **R-REG-003 (MUST)** **OCI registry**: distribution-spec pull API for images bob built
  (`docker`/`podman pull`), with service-account tokens for authenticated pulls. Only workers
  (via promotion) push. [D19]
- **R-REG-004 (MUST)** Releases: versioned, immutable sets of artifacts (closure, OCI image,
  AppImage, tarball, …), public or private, downloadable over HTTP and installable with
  `bob install`. [D10]
- **R-REG-005 (MUST)** The server cache holds **only bob-built outputs**. It does not proxy or
  mirror cache.nixos.org; clients and workers substitute from cache.nixos.org directly. [D37]

---

## 16. Pipelines & forges (PIPE)

- **R-PIPE-001 (MUST)** Forge integrations in v1: **GitHub, Forgejo and GitLab** — webhooks
  (push, PR/MR, tag), commit/PR status checks, checkout credentials. [D5]
- **R-PIPE-002 (MUST)** A project is linked to exactly one repository.
- **R-PIPE-007 (MUST)** Forges sit behind one small, clear adapter interface; each adapter uses the
  forge's official Go SDK. Forgejo (and GitLab) are tested live against local instances; the
  GitHub adapter is tested with recorded payloads and must use the official GitHub SDK. [D48]
- **R-PIPE-003 (MUST)** Pipelines run as defined in `bob.go` (R-FW-040); results report back to
  the forge with a link to the build in the UI.
- **R-PIPE-004 (MUST)** Builds can also be triggered by API, UI and `bob build --remote`.
- **R-PIPE-005 (MUST)** Cancel, retry, and re-run of builds; concurrency rules per project/branch
  (e.g. supersede older pushes) — Stage 2 specifies.
- **R-PIPE-006 (SHOULD)** Notifications (at least webhooks out; email/chat later).

---

## 17. Identity & access (AUTH)

- **R-AUTH-001 (MUST)** Users come from **OIDC** (external identity provider; the server is an
  OIDC client) or are **local users with password login**. [D14, D19]
- **R-AUTH-002 (MUST)** RBAC: users ↔ roles ↔ permissions. **A permission names an RPC endpoint
  and a project scope** (RPC × project; "all projects" possible). [D14′]
- **R-AUTH-003 (MUST)** The only built-in role is **`admin`** (all permissions on all projects;
  cannot be deleted or reduced; at least one admin must always exist). All other roles are
  created via API/UI. [D39]
- **R-AUTH-004 (MUST)** OIDC claims/scopes map to roles (configurable mappings).
- **R-AUTH-005 (MUST)** **Service accounts** with tokens, in particular for OCI pulls and
  automation. [D19]
- **R-AUTH-006 (MUST)** **Every** RPC endpoint is covered by the permission system; a CI test
  fails if an endpoint has no permission mapping.
- **R-AUTH-007 (MUST)** The server issues its own tokens for its API (CLI, UI, service accounts)
  after OIDC or local login.

---

## 18. API (API)

- **R-API-001 (MUST)** connectrpc API, schemas in `proto/`, generated with **buf** (Go server,
  TypeScript client for the UI). [D5]
- **R-API-002 (MUST)** Clean, simple CRUD-style resources: projects, builds, jobs, workers,
  releases, images, cache entries, users, roles, permissions, service accounts, OIDC mappings,
  settings.
- **R-API-003 (MUST)** `buf lint` and `buf breaking` in CI.
- **R-API-004 (MUST)** The UI uses only the public API — nothing UI-only on the server.

---

## 19. Logs (LOG)

- **R-LOG-001 (MUST)** Build logs are written **periodically during the build** to lake as small
  Parquet files; lake's compactor later merges them into large files. Logs survive worker and
  orchestrator crashes up to the last flush. [D20]
- **R-LOG-002 (MUST)** Live log viewing uses the optional direct connection when available; the
  lake data is the fallback and the persistent store. [D12]
- **R-LOG-003 (MUST)** Logs are attributed per package/derivation within a build, so the UI and CLI
  can show the log of the failing package only.

---

## 20. Web UI (UI)

- **R-UI-001 (MUST)** SvelteKit app with the **static adapter**, embedded in and served by the Go
  server. No SvelteKit server-side code. Talks to the connectrpc API only. [D5]
- **R-UI-002 (MUST)** Pages: overview (running builds, queue depth, fleet health, cache hit rate,
  recent failures); projects (pipelines, builds, releases, settings, permissions); build detail
  (package graph with status, live log, timing waterfall, artifacts, variant/platform); fleet
  (workers, orchestrators); cache (size, hit rate, top packages, retention); registry (releases,
  OCI images); access (users, roles, permission matrix, service accounts, OIDC mappings); settings
  (forges, signing key, retention); analytics (history from lake).
- **R-UI-003 (MUST)** Visual style: **yellow and black**, modern, **not "AI-looking"**:
  `rounded-xs` (never `rounded-full` on containers/buttons), 1px borders, no gradients, no
  glassmorphism, no glow, no emoji, no hero cards; dense tables; monospace for hashes/ids/logs.
  [D21]
- **R-UI-004 (MUST)** **Light theme by default, dark theme optional.** [D35]
- **R-UI-005 (MUST)** Tailwind with **hand-written components**, no component libraries; basic
  utility classes, no class-composition tricks. [D21]
- **R-UI-006 (MUST)** Usable for an overview of everything at a glance; every list is filterable;
  every id links to its detail page.

---

## 21. Security (SEC)

- **R-SEC-000 (MUST)** **Workers are the primary security boundary:** every build and every
  `bob.go` evaluation is properly isolated (Nix sandbox plus the isolation Stage 2 specifies,
  R-FLEET-008), so user code cannot take over a worker.
- **R-SEC-001 (MUST)** The control/fleet bucket split is **defence in depth for emergencies**,
  enforced by S3 credentials: a worker compromised despite R-SEC-000 still cannot read user
  information or change admin data. Analytics and artifact staging are not hidden from workers;
  that is accepted. [D13, D38]
- **R-SEC-002 (MUST)** bob ships the S3 policies for every role and a test proves a
  worker-scoped credential is refused on the control bucket.
- **R-SEC-003 (MUST)** The fleet bucket contains no personal data (user ids only, no emails or
  names, no role data).
- **R-SEC-004 (MUST)** All input is hostile: webhooks (signature-verified), API params, uploaded
  artifacts, npm/crate/module metadata, `bob.go` evaluation output.
- **R-SEC-005 (MUST)** Passwords hashed with a modern KDF; tokens stored hashed; secrets never
  logged.

---

## 22. Non-functional (NFR)

Stage 2 turns each into a measured budget, proposed to the owner (numbers here are placeholders
for the questions to answer, not commitments):
- **R-NFR-001** Evaluation time vs. graph size (10 / 100 / 1,000 / 10,000 packages).
- **R-NFR-002** No-op `bob build` and one-file incremental build times.
- **R-NFR-003** Cache: substitution throughput and latency from the server.
- **R-NFR-004** Server: job pickup latency, live log latency (with and without peering), API p99,
  UI first load.
- **R-NFR-005** Scale target: number of projects, concurrent builds, workers, cache size.
- **R-NFR-006** Reproducibility: the same package built twice on different workers is
  bit-identical (or deviations are reported).
- **R-NFR-007** Crash safety: killing any orchestrator or worker at any point loses no accepted
  work and corrupts no state.
- **R-NFR-008** **Low-power machines are a first-class target:** builds, evaluation and `bob dev`
  must be fast on small machines (reference: 2 vCPU, ~4 GiB RAM, aarch64). Budgets are measured
  on this profile, not only on large hardware. [D47]

---

## 23. Out of scope (v1)

- Source hosting / versioning (a forge sits in front).
- Deployment of built software; infrastructure secrets management.
- Multi-tenancy.
- darwin, Windows, BSD.
- Developer pushes to the shared cache (use `--remote`).
- `bob add` and other code-rewriting commands.
- Raw Nix escape hatch.
- Impure pipeline steps (roadmap evaluation).
- Worker provisioning / autoscaling.
- Component libraries in the UI.

---

## 24. Owner decision log (2026-10-20)

| # | Decision |
|---|---|
| D1 | Phase 0 spikes **A′** (Go → JSON → bob-owned Nix library) vs **B** (direct derivations); owner decides at Gate 0 by speed, usability and error behaviour. |
| D2 | `bob.go` + `bob.lock` are the only source of truth, declared in code. Native files generated; hand edits (e.g. `go get`) detected and rejected. |
| D3 | Ecosystems: C, C++, Go, Rust, TypeScript/npm, OCaml + codegen (protobuf/connect, GraphQL, OpenAPI). Outputs: Nix closure, AppImage, OCI images. Cross-compilation in v1. Owner strikes from the extended list (§7.7) as they like. |
| D4 | Linux x86_64 and aarch64 only. |
| D5 | Server does the full CI pipeline and hosts images, artifacts, outputs; no deployment, no infra secrets. GitHub, Forgejo, GitLab in v1. connectrpc + buf; static SvelteKit UI served by Go. |
| D6 | `bob` is the only executable. No `.nix` files in user projects; flakes allowed internally; Lix fine if it has advantages. |
| D7 | HA and scalable via reconciliation; orchestrators don't talk to each other unless something requires it. |
| D8 | Storage = S3 only: dynamitedb (CRUD), lake (analytics), artifacts bucket. Use dynamitedb/lake as they are now; upstream changes only for real bugs/missing features. |
| D9 | Developers don't push to the cache; `bob build --remote` instead. |
| D10 | `bob install` for end users; releases may be public. |
| D11 | Fully rootless; if user namespaces are blocked, refuse with exact admin instructions. |
| D12 | Optional orchestrator-to-orchestrator live log streaming; persisted logs are the fallback. |
| D13 | Workers auto-register via the fleet bucket (access = proof); two dynamitedb buckets (orchestrator-only; orchestrator+worker) so a compromised worker can't read users or change admin data. |
| D14 | Single tenant; org → projects (one repo each); RBAC with users/roles/permissions; OIDC + local users; OIDC claim→role mappings; default + custom roles via UI/API. |
| D14′ | Permissions are scoped per RPC endpoint **and** per project. |
| D15 | npm: own resolver/installer on the npm registry, no npm/pnpm CLI; Node runtime only; Bun optional as runner of final outputs; frontend builds in v1 with Vite live reload. |
| D16 | bob is AGPL-3.0; avoid GPL/AGPL dependencies unless they add substantial value. |
| D17 | bob builds itself with bob once it needs anything `go build` can't provide. |
| D18 | Stages 2–3 are autonomous: questions at stage start, side questions self-answered from the principles, only important architecture decisions go to the owner. A code-steward role keeps bob's code clean (Go and especially Svelte/Tailwind). |
| D19 | Local users: password only. Service accounts, especially for OCI. |
| D20 | Logs written periodically to lake as small Parquet files, compacted later. |
| D21 | UI: Tailwind + hand-written components, not AI-styled. |
| D22 | `bob.go` next to the code, decided by a spike; `bob/` build tree as fallback. |
| D23 | Memoised constructor functions for cross-package handles. |
| D24 | Builder chaining or options, whichever is intuitive per case. |
| D25 | Generated code materialised into the working tree, gitignored. |
| D26 | Dependencies added by manual code edits (no `bob add` for now). |
| D27 | No raw-Nix escape hatch. |
| D28 | Build variants (debug/release, features, sanitisers) in v1. |
| D29 | No impure steps in v1; solution evaluation on the roadmap. |
| D30 | npm: isolated layout by default, per-package hoisting as fallback. |
| D31 | npm lifecycle scripts denied by default, allowed per package. |
| D32 | npm quirks table allowed, but must not become a catch-all. |
| D33 | `package.json`/`tsconfig.json` generated; `vite.config.ts` etc. user-owned. |
| D34 | `bob dev` runs dev servers with live reload. |
| D35 | UI light theme by default, dark optional. |
| D36 | Two binaries: `bob` (user tooling) and `bob-server`; the server has nothing to do with user tooling. |
| D37 | The framework is independent of the server: a bob server cache may be used but is never required; no mirroring of cache.nixos.org. |
| D38 | Workers read/write the analytics bucket directly. Workers must already be safe through build isolation; the bucket split only hides user information from compromised nodes in an emergency. |
| D39 | Only one built-in role: `admin`. |
| D40 | Gate 0 decides both foundation spikes together: A′ vs B and Nix vs Lix (rootless store). |
| D41 | Gate H (owner reviews the harness before tests become protected) stays. |
| D42 | Cleanroom applied pragmatically: no copied code by default; case-by-case exceptions with a compatible licence, attribution and a recorded reason. |
| D43 | Smaller additions accepted: tarballs, direnv-style activation, several registries for `bob install`, outgoing webhooks. |
| D44 | `cc.External` for third-party C/C++ with its own build system, built inside a derivation; source verified by narHash pinned in `bob.lock`. |
| D45 | UI style rules are an agent guardrail in the spirit of "not AI-looking", reviewed by the code steward. No CI enforcement. |
| D46 | dynamitedb and lake are used at their current HEAD, not pinned to a commit (owner: no breaking changes expected). |
| D47 | Low-power machines are a target: builds should be fast on small machines like the Stage 2 host. Spikes use simple packages, not massive ones. |
| D48 | Forge adapters: small clear interface, official SDK per forge; Forgejo tested live, GitHub via SDK + recorded payloads. |
| D49 | Gate 0: generation strategy **B** (Go computes derivations directly; ADR 0001). |
| D50 | Gate 0: embedded **static Nix** (not Lix), pinned by bob (ADR 0002). |
| D51 | Gate 0: `bob dev` sets a `nix develop`-style environment inside the namespace; outside it, generated `.bob/bin` shims (`bob exec`) plus `bob env` and editor settings (ADR 0002 Decision 5). |
| D52 | Gate 0: shipping static Nix (LGPL-2.1, GPL-2.0 busybox) is accepted; obligations go into `ATTRIBUTION.md` and the release process. |
| D53 | Scheduling: one job builds the whole closure by default; bob splits into batches of ready derivations when idle workers give a predicted gain (ADR 0016 option c). |
| D54 | Job dispatch: workers poll the fleet store slowly; an orchestrator may send a "wake up" so workers re-read sooner. Claims stay CAS in the bucket. |
| D55 | Logs: workers write logs to lake in batches (not every second) and keep a live in-memory buffer. Only when a user follows a build live does the orchestrator ask the workers for their in-memory log. No continuous worker→orchestrator streaming, no orchestrator↔orchestrator log peering. |
| D56 | Workers: the worker software runs as root; builds never run as root and run isolated (probably in a container). |
| D57 | Worker auth hierarchy: workers always have S3 credentials (how they get them is a deployment concern, e.g. IMDSv2); every further token is obtained via S3 (S3 credential → token), never the other way round. bob does not mint S3 credentials. |
| D58 | Private cache paths: visibility marker per path, one cache URL (ADR 0014). `bob.lock` may contain crate dependency edges, feature tables and the needed Cargo.toml facts (ADR 0005). |
| D59 | Editors: `cd` into the project + starting the editor must just work (direnv/nix-develop-like environment); only where an editor needs more (VS Code, IntelliJ), "open it with `bob dev`" is acceptable. |
| D60 | dynamitedb access is designed for GET, not LIST: no LIST (and no dynamitedb `Query`, which lists) in polling, claiming or reconciling. Key schemas use known keys (e.g. counters/pointers, sequence-numbered slots, index documents) so every hot access is a GET or conditional PUT. Owner: "stop using list... this should not list but get." |
| D61 | Cache/release downloads go through a lightweight streaming proxy in the orchestrator; no presigned S3 URLs (would add the bucket's own auth system; buckets must stay interchangeable). |
| D62 | Forgejo checkout: bob stores the bot password, mints a per-job repo-scoped token, revokes it at job end, a sweeper removes leftovers (ADR 0018 O1 a). |
| D63 | Supported storage: only AWS-S3-compatible backends with full conditional-write semantics (If-Match / If-None-Match). SeaweedFS and Garage are not supported. RustFS is the reference test backend. |
| D64 | dynamitedb: no upstream changes required for bob; compliant S3 servers always return the ETag. |
| D65 | Gate S: SPEC.md approved (incl. budgets [proposal]); ADRs 0003–0019 locked; HARNESS.md and the execution plan approved. |
| D66 | Platforms in tests: arm64 is tested natively; no native x86_64 test machines needed (cross builds + qemu-user are enough). No GitHub runners or cloud VMs; the real-distro rootless matrix stays (manual). |
| D67 | Stage 3: local git commits per task on branch `bob-v1`; never pushed without asking. |
| D68 | Protected test files: changes are reported by CI (hash manifest), not blocked; no signing key. |
| D69 | Host setup for rootless podman/OCI tests (setuid newuidmap/newgidmap, subuid ranges) is approved. |
