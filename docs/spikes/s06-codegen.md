# Spike S6 — code generators as bob packages, materialised into the tree

Status: **done (indicative numbers)**, 2026-10-20, host `vibe-engine`. Other spikes were running.
Code: `spikes/s06-codegen/` (throwaway, module `bobspike/s06`). ADR: `docs/adr/0008-codegen-materialisation.md`.

## 1. Question and requirements

Can bob's code generators be ordinary bob packages (derivations on backend B), and how does
bob write their outputs into the working tree so tools see them? Requirements: R-GEN-001…004,
R-FW-007 (D25), R-FW-006/D2 (hand edits rejected), R-FW-021 (source hashing ignores generated
files), R-DEV-002, R-NPM-010 (codegen regenerates in `bob dev`), R-API-001/003 (bob's own API).

## 2. What was built

- `cg/` — typed constructors that compute derivations directly (B's `drv/`, `nixidx/`, `run/`
  copied; FOD paths generalised to sha512 for npm integrity): `ProtoLibrary`, `GoModule.ConnectGo`,
  `TSProject.ConnectTS`, `GoModule.Gqlgen`, `TSProject.GraphQLTS`, `GoModule.OpenAPIGo`,
  `GoModule.Binaries`, `NodeModulesFromLock`, `TSProject.Build`, module proxy from go.sum.
- `mat/` — materialiser: manifest, conflict detection, atomic writes, pruning, status; 11 unit tests.
- `cmd/bob-s06` — `build`, `gen [--force|--check]`, `status`, `dev`, `outs`, `e2e`;
  `cmd/bob-s06/graph.go` is the spike's `bob.go`.
- `fixture/` — 3 proto libraries (`greet/v1` imports `common/v1`; `billing/v1` independent),
  a GraphQL schema + one operation, an OpenAPI spec, Go module `svc` (connect server + gqlgen
  + oapi handlers, connect Go client, hand-written resolvers), TS project `web` (connect-es
  client + typed GraphQL query).
- `tools/graphql-codegen/` — bob-shipped tool lock (41 npm packages) + 34-line driver.
- `lock.json` + `cmd/lock` — sha256 of all 51 Go module files from go.sum (bob.lock stand-in).
- `measure.sh`, `faults.sh`, `verify-ide.sh`; outputs in `measure-results.md` and `tmp/out/`.

Tools, all from the nixpkgs pin: buf 1.73.0, protoc-gen-go 1.36.11, protoc-gen-connect-go
1.20.0, protoc-gen-es 2.11.0, gqlgen 0.17.90, oapi-codegen 2.5.1, go 1.26.8, nodejs 24.21.0,
typescript 5.9.3. Not in nixpkgs: graphql-codegen, protoc-gen-connect-es (not needed: since
connect-es v2, protoc-gen-es emits the service descriptors).

Graph: 110 derivations = 97 fetches (51 Go files, 46 npm tarballs) + 8 codegen + goproxy +
2 node_modules + `//svc:bin` + `//web:client`.

## 3. Results

### 3.1 End to end (`bob-s06 e2e`)

```
go client: hello go-client (request_id=r-1)
go client: empty name -> code=invalid_argument
ts client: hello ts-client (request_id=r-1)          # connect-node, protoc-gen-es output
ts graphql: hello ts-gql length=12                    # gqlgen server, graphql-codegen types
rest (oapi-codegen server): {"id":"42","name":"item 42"}
```
Server on 127.0.0.1:20600, all binaries from the store.

### 3.2 Verifications

| Check | Result |
|---|---|
| Every non-fetch derivation rebuilt with `nix build --rebuild` (13) | all bit-identical (`tmp/out/repro.txt`) |
| gopls 0.24.0 `check` on 4 user files | no diagnostics, 2.2 s warm (12 s cold module cache) |
| gopls `definition` of `greetv1connect.NewGreetServiceHandler`, `model.Hello` | resolves into `svc/gen/...` |
| gopls on a probe with `GreetRequest{Nmae:…}` | `unknown field Nmae` (it really type-checks the generated code) |
| gopls with `svc/gen/proto/greet` moved away | `could not import example.com/svc/gen/proto/greet/v1` |
| tsserver (protocol): diagnostics of `src/client.ts` / definition of `GreetService` | `[]` / `web/gen/proto/greet/v1/greet_pb.ts:59` |
| tsc `--noEmit`; wrong type vs generated `HelloQuery`; gen dir removed | ok; TS2322; TS2307 |
| `git add -A` in a copy of the tree | 0 files under `gen/` or `.bob/` staged; `git check-ignore` names the per-dir `.gitignore` |
| Build after `bob gen` (materialised files inside the Go module dir) | no-op: owned dirs are excluded from source import |
| `buf lint` / `buf breaking --against <dir>` offline (plain commands, not yet derivations) | work; exit 100 with file:line messages |
| mat unit tests (edit, foreign file, force+backup, stale removal, restore deleted, orphan prune, edited orphan kept, crash recovery, hostile manifest paths, nested/duplicate roots, lost manifest) | pass |

Gopls needed `GOPROXY=file://<goproxy store path> GOFLAGS=-mod=readonly GOSUMDB=off
GOTOOLCHAIN=local`. Not verified: gopls/tsserver noticing a regeneration while running
(needs an LSP client with file watching), and gopls through the `.bob/bin` shim.

### 3.3 Generator findings

| Generator | What it needs in the sandbox | Notes |
|---|---|---|
| buf + protoc-gen-go/connect-go/es | `HOME`, `BUF_CACHE_DIR` writable; local plugins; no network | bob writes `buf.yaml`/`buf.gen.yaml`; Go import paths via `M` options, so `.proto` files need no `go_package` |
| gqlgen | **go toolchain, go.mod + go.sum, the module proxy with the gqlgen runtime** (it loads packages with go/packages: without them `unable to find type …graphql.String`) | defaults run `go mod tidy` and a validation compile (8.7 s, needs gcc unless `CGO_ENABLED=0`); bob sets `skip_mod_tidy` and `skip_validation` — the following Go build type-checks anyway (F7). bob never generates resolver stubs |
| oapi-codegen | go.mod (reads the `go` version for `std-http-server`; warns without it) | generated code imports `github.com/oapi-codegen/runtime` |
| graphql-codegen | node + node_modules of `@graphql-codegen/core`, `typescript`, `typescript-operations`, `graphql` (41 packages, no install scripts) | bob driver calls the plugin API; no CLI (the CLI pulls in a much larger tree), no `codegen.yml` |

What graphql-codegen needs from S4 (npm): (1) an installer that turns a lock into a
node_modules derivation (the spike lays out npm's hoisted lock paths, one FOD per tarball;
S4's strict layout must also work for it); (2) a way for bob to ship *tool locks* that users
do not declare; (3) sha512 FODs (implemented here, Nix accepted the paths). No resolver at
build time: bob pins the tool lock when bob is released. I did not use an FOD of a whole
`npm ci` tree.

Generator ↔ runtime coupling: generated code only works with a matching runtime library
(gqlgen exact; `@bufbuild/protobuf` = protoc-gen-es version; `google.golang.org/protobuf` ≥
protoc-gen-go; connect ≥ 1.13 for connect-go 1.20 output). The spike pre-checks this (F4, F5).

### 3.4 Fault catalogue (`faults.sh`, `tmp/out/faults.txt`)

Sources are laid out at their repo paths inside the sandbox, so generator errors already name
repo files; no path translation was needed.

| Fault | bob prints (excerpt) |
|---|---|
| F1 proto syntax | `✗ //proto/greet/v1:go code generation failed (defined at graph.go:21)` `│ proto/greet/v1/greet.proto:9:1:unexpected '}' after definition` (same for `:ts`) |
| F2 missing import | `│ proto/greet/v1/greet.proto:5:1:imported file does not exist` |
| F3 GraphQL schema | gqlgen: `│ failed to load schema: schema/schema.graphqls:9:18: Undefined type Nope.`; graphql_ts: `│ Unknown type: "Nope".` (driver prints the message, not a Node stack into the store) |
| F4 gqlgen runtime pinned v0.17.89 | before any build: `✗ //schema:gqlgen_go: generator gqlgen 0.17.90 (nixpkgs cd4ea8d57de1) does not match its runtime: the generated code needs github.com/99designs/gqlgen 0.17.90, but svc/go.mod has v0.17.89` + hint |
| F5 @bufbuild/protobuf 2.10.0 | same shape, for `//proto/common/v1:ts` and `//proto/greet/v1:ts` |
| F6 OpenAPI bad `$ref` | `│ error loading swagger spec in schema/items.yaml : failed to load OpenAPI specification: map key "Itm" not found` |
| F7 schema changed, resolver not | `✗ //svc:bin build failed` `│ graph/resolver.go:13:59: … queryResolver does not implement … (wrong type for method Hello) have Hello(context.Context, string) … want Hello(context.Context, string, *bool)` — path is module-relative; SPEC should prefix the module dir |

### 3.5 Materialisation conflicts (`tmp/out/conflicts.txt`, git copy of the tree)

```
✗ bob gen: 2 file(s) in generated directories are not bob's to overwrite; those directories were left untouched
  • svc/gen/proto/greet/v1/greet.pb.go was edited by hand (generated by //proto/greet/v1:go)
  • svc/gen/proto/greet/v1/helpers.go is not a file bob generated, but it is inside svc/gen/proto/greet/v1/, which bob owns for //proto/greet/v1:go
  hint: generated code is rebuilt from its inputs (.proto, .graphqls, OpenAPI spec, bob.go).
        Change the input instead. To discard the edits run `bob gen --force`
        (bob moves the edited files to .bob/gen/backup/<time>/ first).
```
`--force` moved both files to `.bob/gen/backup/20261020T214731.015532508Z/…` and rewrote. A
git-tracked file in an owned dir is refused even with `--force`:
`✗ svc/gen/rest/items.gen.go is tracked by git, but svc/gen/rest/ is generated by //schema:openapi_go` /
`hint: run git rm -r --cached svc/gen/rest and commit; generated code must not be committed`.
Removing billing from bob.go removed `svc/gen/proto/billing/` (files, `.gitignore`, empty parents).
Deleting the manifest and rerunning adopts identical files without rewriting.

### 3.6 Caching and speed (`measure-results.md`, indicative, busy host)

| Measurement | [8c/15G] | [2c/4G] | Rebuilt |
|---|---|---|---|
| gen no-op | 0.22 s / 32 MB | 0.29 s / 31 MB | nothing |
| gen after `billing.proto` change | 0.92 s | 1.16 s | `//proto/billing/v1:go` only |
| gen after `common.proto` change | 1.85 s | 2.59 s | common go+ts, greet go+ts (importers) |
| gen after `schema.graphqls` change | 21.3 s ¹ | 47.3 s ¹ | gqlgen_go, graphql_ts (+ goproxy ¹) |
| build cold-ish ² (codegen + Go + TS) | 30.7 s / 397 MB | 65.6 s / 401 MB | see results file |
| build no-op | 0.39 s | 0.17 s | nothing |
| build after `billing.proto` change | 19.9 s | 47.7 s | billing:go, `//svc:bin` |
| build after `greet.proto` change | 20.4 s | 54.9 s | greet go+ts, `//svc:bin`, `//web:client` |
| build after whitespace-only `greet.proto` change | 20.0 s | 66.0 s | same 4 — output is identical, consumers rebuild anyway |
| same with `-cutoff` | **1.59 s** | **1.96 s** | greet go+ts only |
| status (compute drvs, compare manifest; no build) | 0.12 s | 0.19 s | — |
| `bob dev`: edit greet.proto → Go and TS files updated in tree | **1.44 s** | **1.67 s** | greet go+ts |
| materialise only: no-op / 11 files fresh | 3–9 ms / 0.23 s | — | (fsync-bound) |

¹ goproxy had been deleted by the "cold" clean step and was rebuilt here; gqlgen itself takes
a few seconds. ² `nix-store --delete` keeps paths still referenced, so "cold" rebuilt only part
of the graph (the rebuilt list is in the results file). Rerun `measure.sh` on a quiet machine.

Readings: unchanged inputs are a no-op (no Nix build is started: one `nix-store
--check-validity`); a `.proto` change rebuilds only codegen that includes it and its
consumers; the remaining cost (~20 s / ~50 s) is the per-module Go build (`//svc:bin`), the
same finding as S0a — per-package Go builds (S2) fix it. Peak RSS stays ≤ 402 MB.

**Early cutoff** (`-cutoff`): after building codegen, bob imports each output by content
(`nix-store --add` of a copy, 0.34 s for 8 outputs) and computes consumers against that path.
A byte-identical output then leaves consumers untouched: 1.6 s instead of 20 s. Cost: a
two-phase evaluation (consumers' derivations exist only after codegen is built), which
`--dry-run`, remote planning and `bob status` must handle.

## 4. Answer

Yes. Each generator is one derivation with tools from the pinned nixpkgs; all work offline,
reproducibly, and end to end. bob owns one directory per codegen package, records what it
wrote in `.bob/gen/manifest.json`, rejects edited or foreign files, writes atomically,
gitignores each owned directory with its own `.gitignore`, and writes on `bob gen`,
`bob build` (closure) and `bob dev` (watch, ~1.5 s). gopls and tsserver see the code.

## 5. SPEC input

### 5.1 API [proposal]

Package handles are memoised constructors (R-FW-011); generated outputs are typed so a TS
output cannot be passed to a Go module (R-FW-010).

```go
// proto/common/v1/bob.go
func Lib(b *bob.B) *proto.Library { return proto.Library(b, proto.LibraryOptions{}) } // *.proto of this dir

// proto/greet/v1/bob.go
func Lib(b *bob.B) *proto.Library {
	return proto.Library(b, proto.LibraryOptions{Deps: []*proto.Library{commonv1.Lib(b)}})
}

// svc/bob.go
func Module(b *bob.B) *golang.Module {
	return golang.NewModule(b, golang.ModuleOptions{Path: "example.com/svc", Go: "1.26"})
}
func Server(b *bob.B) *golang.Binary {
	m := Module(b)
	return m.Binary("./cmd/server", golang.BinaryOptions{Uses: []golang.Generated{
		connect.Go(m, greetv1.Lib(b), connect.GoOptions{}),                // -> svc/gen/proto/greet/v1 (+ common/v1)
		graphql.Gqlgen(m, schema.Schema(b), graphql.GqlgenOptions{}),      // -> svc/gen/graph{,/model}
		openapi.Go(m, openapi.Spec(b, "schema/items.yaml"), openapi.GoOptions{Package: "rest", Server: openapi.StdHTTP}),
	}})
}

// web/bob.go
func Client(b *bob.B) *ts.Package {
	p := ts.NewProject(b, ts.ProjectOptions{})
	return p.Build(ts.BuildOptions{Uses: []ts.Generated{
		connect.TS(p, greetv1.Lib(b)),                                      // -> web/gen/proto/greet/v1 (+ common/v1)
		graphql.TS(p, schema.Schema(b), graphql.TSOptions{Documents: b.Glob("schema/*.graphql")}), // -> web/gen/graphql
	}})
}
```

- `proto.Library`: one proto package = one directory; import root = the nearest ancestor that
  bob.go marks with `proto.Root(b, "proto")` (spike: fixed `proto/`). Deps are other Libraries.
- `connect.Go(m, lib)` creates the Go packages of `lib` and of every library it imports, in the
  same module (memoised per module+library). Options: `Dir` (override `gen/proto/<dir>`), `Simple bool`
  (connect-go simple API). It adds `google.golang.org/protobuf` and `connectrpc.com/connect` to
  the module at the generator versions.
- `connect.TS(p, lib)`: protoc-gen-es `target=ts, import_extension=js`; adds
  `@bufbuild/protobuf` at the generator version; `@connectrpc/connect(-node|-web)` are normal deps.
- `graphql.Schema(b, files...)`; `graphql.Gqlgen` options: `Models map[string]string` (GraphQL type
  → Go type), `Autobind []string`. Never generates resolvers. Adds `github.com/99designs/gqlgen`
  (exact version) and its gqlparser.
- `graphql.TS` options: `Documents`, `Scalars map[string]string`. Tool = bob-shipped lock.
- `openapi.Go` options: `Package`, `Server` (`StdHTTP` in v1; others later), `Models bool`
  (default true), `Client bool`. `openapi.TS` (R-GEN-003 TS client) not built in S6; proposal:
  openapi-typescript as a second bob-shipped tool lock.
- `proto.Lint(lib)` and `proto.Breaking(lib, against)` as check packages (R-API-003); `against`
  is the library at a git base revision. Shown to work as plain buf commands only.

### 5.2 Derivation rules [proposal]

- Generator configs are generated inside the derivation; inputs = only the files the
  generator reads (proto files of the library and its imports; schema files; spec + go.mod;
  for gqlgen also go.sum and the module proxy). Sources are laid out at repo-relative paths so
  errors need no translation.
- Output = exactly the owned directory's content; must contain only regular files and no
  `.gitignore` (checked).
- Pre-check (before Nix): generator vs runtime version (rules above). Error text as F4.

### 5.3 Materialisation [proposal]

Owned directory defaults: Go `<module>/gen/proto/<proto dir>`, `<module>/gen/graph`,
`<module>/gen/<package>`; TS `<project>/gen/proto/<proto dir>`, `<project>/gen/graphql`.
Owned dirs never nest or overlap (error: `✗ //p:b generates into gen/b/, which is inside gen/
owned by //p:a` / `hint: generated directories may not be nested`; duplicate: `✗ //p:a and //p:b
both generate into gen/` / `hint: give one of them another output directory`). Paths must be
clean, relative, not inside `.git` or `.bob`.

Manifest `.bob/gen/manifest.json`:
```json
{
  "version": 1,
  "roots": {
    "svc/gen/proto/greet/v1": {
      "label": "//proto/greet/v1:go",
      "source": "/nix/store/…-proto-greet-v1-go",
      "files": {
        "greet.pb.go": {"sha256": "…", "size": 5669, "mtime": 1792532482085748268},
        "greetv1connect/greet.connect.go": {"sha256": "…", "size": 4210, "exec": false, "mtime": …}
      },
      "pendingSource": "", "pending": null
    }
  }
}
```
`mtime`/`size` only enable the no-hash fast path; ownership is decided by `sha256`. The
manifest is hostile input: size cap 64 MiB, all paths re-validated.

Per owned dir, each file on disk is classified:

| On disk | Manifest/pending | New content | Action |
|---|---|---|---|
| hash = new | any | present | keep (fix mode if needed) |
| hash = manifest or pending | known | present / absent | overwrite / delete |
| hash ≠ manifest | known | — | conflict **edited** |
| not in manifest | — | — | conflict **foreign** (unless hash = new: adopt) |
| `.bob-tmp-` temp file | — | — | delete (interrupted write) |
| git-tracked | — | — | conflict **tracked** (not overridable) |
| missing | known | present | rewrite (not a conflict) |

Write protocol: flock `.bob/gen/lock` → write manifest with `pending` = new hashes → per file:
temp in same dir, write, fsync, chmod 0444 (0555 if executable), rename, fsync dir → delete
stale files and empty dirs → write `.gitignore` → manifest final (atomic; skipped if
unchanged). A root with conflicts is skipped entirely; other roots proceed; exit status 1.
`--force` renames conflicting files to `.bob/gen/backup/<UTC time>/<path>` first.

When: `bob gen [patterns]` (build codegen, write, prune roots no longer defined),
`bob gen --check` (no build, no write; exit 1 unless all fresh), `bob build` (write codegen in
the targets' closure, never prune defined roots, print `note: N generated directories are out
of date but not needed by this build … run bob gen`), `bob dev` (gen on entry, then on every
change of a codegen input or bob.go; conflicts printed, watching continues). Remote workers
and pipeline runs never materialise. `bob status` lists `fresh | stale | missing | edited |
orphan` per owned dir.

`.gitignore`: each owned dir gets `# Generated by bob. Do not edit files in this directory:
change the inputs and run \`bob gen\`.` + `*`; `.bob/.gitignore` = `*`. bob never edits user
`.gitignore` files. Source import of any package excludes owned dirs (R-FW-021).

### 5.4 Budgets [proposal]

On [2c/4G]: `bob gen` no-op ≤ 0.5 s; one-proto change → files in tree ≤ 3 s (`bob dev`);
materialise no-op ≤ 20 ms; `bob status` ≤ 0.5 s.

## 6. Decisions, risks, open items

### Agent decisions
- One owned directory per codegen package, per-dir `.gitignore` with `*` — keeps user files untouched (BOOTSTRAP §2.1).
- Conflicts block only the affected directory and are never overwritten; `--force` backs up, never deletes (§2.1).
- Generated files are read-only (0444) — editors warn before an edit (§2.3).
- bob writes generator configs and Go import paths itself — no buf/gqlgen/oapi config in user repos (§2.2, §2.3).
- gqlgen `skip_validation` + `skip_mod_tidy` — tidy would edit go.mod (R-FW-006); validation duplicates the Go build (§2.4).
- graphql-codegen via plugin API + bob-shipped tool lock, not the CLI — smaller tree, no config file (§2.8).
- `bob build` materialises codegen in its closure by default — the developer needs it for the editor anyway; cost 3–9 ms when fresh (§2.3).

### Owner decision needed: early cutoff for codegen outputs
- **a)** No cutoff: consumers depend on the codegen derivation. Simple; identical output still rebuilds consumers (20 s / 66 s measured).
- **b)** bob-level cutoff for codegen only: build codegen first, import outputs by content, compute consumers against them (1.6 s / 2.0 s measured; +0.34 s per build; two-phase evaluation).
- **c)** Nix content-addressed derivations (`ca-derivations`, experimental in Nix) — not tested.
- **Recommendation: b**, scoped to codegen packages, decided together with S2's Go granularity (the gain matters less once Go builds per package).

### Risks
- gqlgen needs the full Go module graph to generate; a gqlgen bump changes its runtime requirements.
- gopls must get bob's module proxy env (ADR 0002 Decision 5); tsserver needs node_modules (S4). Not tested through shims.
- Generator ↔ runtime coupling depends on nixpkgs versions; a nixpkgs bump can force runtime bumps.
- graphql-codegen's tool lock is maintained by bob releases (security updates).

### Still to verify
- LSP clients noticing regenerated files live; gopls via `.bob/bin` shim.
- `buf lint`/`buf breaking` as derivations; OpenAPI TS client; multiple Go modules consuming one proto library.
- Quiet-machine rerun of `measure.sh` (all timings depend on it; the "cold" rows also need a better clean step).
