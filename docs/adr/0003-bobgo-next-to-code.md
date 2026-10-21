# ADR 0003 — `bob.go` next to the code, behind the `bob` build tag

- **Status:** Accepted (locked) — owner at Gate S, 2026-10-21
- **Requirements:** R-FW-001 (typed Go, gopls), R-FW-002 (next to code, else `bob/` tree), R-FW-008
  (deterministic), R-FW-010/011 (values, memoised constructors), R-FW-015/016 (labels, source
  positions), R-FW-022 (cross-repo through Go modules), R-DEV-001/002, ADR 0002 Decision 5 (shims).
- **Evidence:** `docs/spikes/s01-bobgo.md`, code in `spikes/s01-bobgo/`, raw output in
  `spikes/s01-bobgo/results/`.

## Context

Go allows one package per directory. A `bob.go` next to the code shares its directory with the
real Go package (or with C, Rust or npm sources). It must be invisible to `go build/vet/test`,
visible to gopls with completion, go-to-definition and type errors, and importable by other
`bob.go` files for typed cross-references (`foo.Lib(p)`).

## Decision

1. **File convention.** Build definitions live in files named exactly `bob.go`, at most one per
   directory. The first line is `//go:build bob`. The package clause is the package name of the
   ordinary Go files in that directory; in a directory without Go files it is the directory
   name made into an identifier (`my-lib` → `mylib`). No other file in a bob repo or in the
   framework may be named `bob.go` (the framework's own `pkg/bob` package uses `core.go` etc.).
2. **Normal Go tooling ignores them** through the tag (verified: `go build/vet/test ./...`).
   `go mod tidy` keeps the framework requirement because tidy ignores build tags (verified).
3. **gopls sees them through `-tags=bob`.** The `.bob/bin/gopls` shim sets `GOFLAGS=-tags=bob`
   for gopls only; bob's editor settings also set `build.buildFlags: ["-tags=bob"]`. Both were
   verified separately with gopls 0.22.0 from the pinned nixpkgs.
4. **Evaluation:** bob generates one evaluator `main` package that blank-imports every `bob.go`
   package of the repo and calls `bob.Main()`. Definitions register themselves through
   `bob.Define` at package initialisation. It is built with
   `go build -tags=bob -trimpath -overlay=… -modfile=…` in GOWORK=off mode:
   - the overlay hides every other compilable file in a `bob.go` directory (the evaluator never
     compiles user code, so missing generated code or cgo can't break evaluation);
   - a `bob.go` in a `package main` directory gets an overlay copy with a renamed package
     clause (same line numbers, compiler errors mapped back to the original path);
   - the evaluator main itself is an overlay-only virtual file (`<root>/_bobeval/main.go`);
   - `bob.go` files of other bob repos (Go module dependencies) are copied (go.mod + `bob.go`
     only) and swapped in through a generated `-modfile`, because the go command refuses
     overlays below GOMODCACHE (verified).
   - `-trimpath` makes positions module-relative, so the graph is identical for every checkout
     path (verified by evaluating two copies and comparing bytes).
5. **Labels** come from the position of the builder call: `//<dir>:<name>` for the own repo,
   `<module path>//<dir>:<name>` for another bob repo, `nixpkgs:<attr>` for nixpkgs packages.
6. **Rules bob enforces before compiling** (each with a fixed error text, see the write-up):
   R1 tag present; R2 package name matches the Go code of the directory; R3 a `bob.go` in a
   `package main` directory cannot be imported by another `bob.go`; R4 no identifier of
   `bob.go` may also be declared in the directory's Go code; R5 directories with C/C++/ObjC/
   Fortran/SWIG sources next to `bob.go` get a generated, gitignored `zz_bob_cgo.go` stub
   (`//go:build bob && cgo` + `import "C"`), otherwise gopls reports "C source files not
   allowed" whenever CGO_ENABLED=1.

## Consequences

- Positive: definitions sit next to their sources; one `go.mod` per bob repo; gopls works for
  bob.go and normal code in the same session (completion, definition, hover, type errors
  measured); cross-repo typed handles work through the module cache.
- Negative (measured, not hypothetical):
  - In the editor, `bob.go` identifiers share the package scope with real code: completion in
    real code offers them (`greet.` → `Hello`, `Test`), and a clash is a redeclaration error in
    the editor only (R4 turns it into a bob error).
  - A main package's definitions are leaves (gopls: "is a program, not an importable package").
  - Import cycles can exist only under the tag (real A imports B, B's bob.go imports A's bob.go).
    gopls reports "import cycle not allowed"; bob must report it as an R-rule error too.
  - Evaluation needs a Go toolchain (pinned nixpkgs `go`, 279 MB closure) and a bob-owned
    GOCACHE even in repos without Go code.
  - gopls holds more in memory with the tag (synthetic 1,000 bob.go: 239 MB vs 90 MB RSS).
- The fallback `bob/` tree (separate module, no tag, `go.work`) was built and works without any
  of the negatives above; it is option (b) below.

## Owner decision

R-FW-002 asks for an owner decision only if the fallback is chosen. Next-to-code works, but with
the visible rules R3–R5, so the owner should confirm:

- **(a) next to code with rules R1–R5 (recommended).** Meets D22 as written.
- **(b) `bob/` tree:** `bob/<dir>/bob.go` in a separate `bob/` module, no tag, no rules R2–R5;
  definitions are away from their code; sources are referenced across trees.
- **(c) both layouts supported:** rejected by the agent (two ways to do one thing, R-FW-014).

## Owner amendments

- **D59** (cd + start the editor must just work): the tag must reach gopls without the user
  configuring it: the `.bob/bin/gopls` shim sets `GOFLAGS=-tags=bob`, and the direnv-style shell
  hook / `bob env` (SPEC §02) put `.bob/bin` first in PATH, so an editor started from the project
  directory uses the shim. Editor settings (`build.buildFlags`) remain for editors that ignore PATH.
- **A14** (agent, owner informed): option (a) next to code; the `bob/` tree fallback is not built.
- SPEC §01 adds rule **R6** (tag-only import cycles, now an error with fixed text) and **R7**
  (`bob.go` may import only the standard library, framework API packages and other `bob.go`
  packages; never `pkg/engine`, `pkg/builder`, `pkg/cli`), pins the framework version used by the
  evaluator to the running bob (embedded module zip), and runs the evaluator in the S13 evalbox
  locally too (S13-4).
