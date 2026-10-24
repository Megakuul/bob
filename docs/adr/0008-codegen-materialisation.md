# ADR 0008 — Code generators as packages and materialisation into the working tree

- **Status:** Accepted (locked) — owner at Gate S, 2026-10-21
- **Requirements:** R-GEN-001…004, R-FW-007, R-FW-021, R-FW-010/011, R-DEV-002, R-NPM-010,
  R-API-001/003, D25, D2, D34
- **Evidence:** `docs/spikes/s06-codegen.md`, code in `spikes/s06-codegen/`

## Context

D25 says generated code is materialised into the working tree and gitignored, so gopls and
tsserver see it. D2 says hand edits to generated files are detected and rejected. S6 had to
show that the generators bob needs (buf + protoc-gen-go/connect-go/es, gqlgen,
graphql-codegen, oapi-codegen) run as ordinary derivations on backend B (ADR 0001), and to
define when and how bob writes their outputs into the tree.

## Decision

1. **Every codegen step is one derivation** whose output is exactly the content of one
   directory in the tree. Generators run offline in the sandbox with tools from the pinned
   nixpkgs. bob writes the generator configuration (`buf.yaml`, `buf.gen.yaml`, `gqlgen.yml`,
   oapi-codegen config) inside the derivation; users never write these files.
2. **One owned directory per codegen package.** Defaults: Go `<module>/gen/proto/<proto dir>`,
   `<module>/gen/graph`, `<module>/gen/<package>`; TypeScript `<project>/gen/proto/<proto dir>`,
   `<project>/gen/graphql`. Owned directories never nest, never overlap and never hold
   hand-written files. bob decides Go import paths (`M` options), so `.proto` files need no
   `go_package`.
3. **Manifest `.bob/gen/manifest.json`** records, per owned directory, the label, the store
   path and the sha256 of every file. A file on disk is bob's only if its hash matches the
   manifest, the write-ahead set, or the new content. Anything else is a conflict.
4. **Conflicts are never overwritten.** The directory is skipped and the build fails with a
   bob error. `bob gen --force` moves conflicting files to `.bob/gen/backup/<time>/` before
   overwriting; nothing is deleted. Git-tracked files inside an owned directory are refused
   even with `--force`.
5. **Atomic writes:** temp file in the same directory, fsync, rename, directory fsync; the
   manifest is written ahead (pending) and after; a no-op writes nothing. Files are mode 0444.
   A file lock serialises concurrent bob processes.
6. **Git:** each owned directory gets a `.gitignore` containing `*`; `.bob/` gets one too. bob
   never edits user-owned `.gitignore` files.
7. **When bob writes:** `bob gen` (all codegen, prunes removed packages), `bob build` (codegen
   in the closure of the targets; reports stale directories outside it), `bob dev` (on entry
   and on every input change). Source hashing of the consuming package excludes owned
   directories, so materialising never invalidates a build.
8. **Generators and runtimes are pinned together.** bob adds the runtime library
   (google.golang.org/protobuf, connectrpc.com/connect, github.com/99designs/gqlgen,
   @bufbuild/protobuf, …) at the version that matches the generator from the nixpkgs pin, and
   a Go pre-check refuses a conflicting explicit pin before anything is built.
9. **npm-based generators not in nixpkgs** (graphql-codegen) are bob-shipped *tool locks*:
   a package-lock pinned at bob release time, installed with S4's installer (one fixed-output
   fetch per tarball, sha512 integrity), driven by a small bob-owned driver script that calls
   the plugin API (no CLI, no `codegen.yml`).

## Consequences

- All generators of the brief work end to end (generate → compile Go server and client and
  TS client → calls succeed); every codegen derivation rebuilt bit-identical.
- Changing one `.proto` rebuilds only codegen packages that include it and their consumers;
  but without early cutoff a byte-identical generated output still rebuilds consumers. An
  optional bob-level cutoff (dependents reference a content-addressed copy of the codegen
  output) cut a whitespace-only `.proto` change from 20.0 s to 1.6 s [8c/15G] (66.0 s → 2.0 s
  [2c/4G]); adopting it is an owner decision (write-up §6).
- `bob gen` no-op 0.22 s / 0.29 s, `bob dev` edit-to-tree 1.44 s / 1.67 s ([8c/15G] / [2c/4G]).
- gopls and tsserver see the materialised code, but gopls needs bob's module proxy
  (`GOPROXY=file://…`, `GOFLAGS=-mod=readonly`, `GOSUMDB=off`) from the environment of ADR 0002
  Decision 5, and tsserver needs `node_modules` (S4).
- Per-module Go builds still rebuild the whole module after any codegen change (S2).

## Owner amendments

- **A11** (owner decision in S6 §6, option b): bob-level early cutoff for codegen outputs is
  **adopted by default**: consumers are planned against a content-addressed copy of each
  codegen output (SPEC R-GEN-005; quiet: whitespace-only proto change 14.5 s → 1.43 s
  [8c/15G], 33.98 s → 1.36 s [2c/4G]). Nix CA derivations are not used.
- **D25 / D2** unchanged. **D59**: gopls/tsserver see materialised code through shims.
- Changed (API shape): codegen constructors return typed recipes (`golang.Generated`,
  `npm.Generated`) listed in the consuming module's/importer's `Generated` field; labels are
  `//<consumer dir>:gen-<owned dir slug>` (SPEC §03.6.1).
- Added: generator↔runtime mismatch pre-check is R-GEN-006; OpenAPI TypeScript via an
  openapi-typescript tool lock is [open] (not built in S6).
