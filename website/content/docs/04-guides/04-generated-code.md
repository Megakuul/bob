---
title: Generated code
section: Guides
order: 4
summary: Which files bob writes into your tree, how generated code is pinned and cached, and what to do when a generated file was edited.
---

bob writes two kinds of files into your working tree. This guide explains both, how to keep
them fresh, and how to recover when someone edited one.

## Two kinds of generated files

| Kind | Examples | Written by | In git? |
|---|---|---|---|
| Native tooling files | `go.mod`, `go.sum`, `Cargo.toml`, `Cargo.lock`, `package.json`, `tsconfig.json`, `compile_commands.json`, `.merlin` | `bob gen`, `bob build`, `bob lock`, `bob dev` | yes, commit them (`go.mod`/`go.sum` and the Cargo files make the tree readable for tools) |
| Materialised codegen | `gen/proto/…`, `gen/graph/…`, `gen/rest/…`, `web/gen/…`, `node_modules/` | `bob gen`, `bob build`, `bob dev` | no: each directory carries its own `.gitignore` with `*` |

Both are outputs of `bob.go`. Change `bob.go` (or the schema) and let bob rewrite them; never
edit them by hand.

## Code generators

The code generator packages (`pkg/proto`, `pkg/connect`, `pkg/graphql`, `pkg/openapi`) run
real tools inside the sandbox:

| Recipe | Tool | Runtime added for you |
|---|---|---|
| `connect.Go` | protoc-gen-go, protoc-gen-connect-go | `google.golang.org/protobuf`, `connectrpc.com/connect` |
| `connect.TS` | protoc-gen-es | `@bufbuild/protobuf` |
| `graphql.Gqlgen` | gqlgen | `github.com/99designs/gqlgen` (exact) |
| `graphql.TS` | graphql-codegen | — |
| `openapi.Go` | oapi-codegen | `github.com/oapi-codegen/runtime` |
| `openapi.TS` | openapi-typescript | — |

The generator versions come from your nixpkgs pin, and bob adds the runtime libraries at the
matching versions, so generated code and runtime always fit. If you pin a runtime yourself
with a version that does not match, `bob lock` stops with `BOB-E-1021` ("toolchain
mismatch") and tells you which requirement to remove.

Each recipe owns exactly one directory. Two recipes cannot write the same directory, and a
file of yours in an owned directory is refused (`BOB-E-1016`).

## GraphQL, OpenAPI and TypeScript clients

A schema is declared once and can feed several generators. This file declares a GraphQL
schema and an OpenAPI document, generates Go code for both into a module, and TypeScript code
into a Node program:

```go verify
//go:build bob

package api

import (
	"github.com/megakuul/bob/pkg/bob"
	"github.com/megakuul/bob/pkg/golang"
	"github.com/megakuul/bob/pkg/graphql"
	"github.com/megakuul/bob/pkg/npm"
	"github.com/megakuul/bob/pkg/openapi"
)

var Schema = bob.Define(func(p *bob.P) *graphql.Schema { return graphql.NewSchema(p, bob.Glob("*.graphqls")) })

var Items = bob.Define(func(p *bob.P) *openapi.Spec { return openapi.NewSpec(p, "items.yaml") })

var Module = bob.Define(func(p *bob.P) *golang.Module {
	return golang.NewModule(p, golang.ModuleOpts{
		Path: "example.com/api",
		Go:   "1.26",
		Generated: []golang.Generated{
			graphql.Gqlgen(Schema(p), graphql.GqlgenOpts{}),
			openapi.Go(Items(p), openapi.GoOpts{Package: "rest", Server: openapi.StdHTTP}),
		},
	})
})

var Client = bob.Define(func(p *bob.P) *npm.App {
	return npm.NewApp(p, npm.AppOpts{
		Name:  "client",
		Entry: "src/client.ts",
		Generated: []npm.Generated{
			graphql.TS(Schema(p), graphql.TSOpts{Documents: bob.Glob("src/**/*.graphql")}),
			openapi.TS(Items(p), openapi.TSOpts{}),
		},
	})
})
```

- `graphql.Gqlgen` writes the executable schema and models to `gen/graph`; the resolvers
  stay yours.
- `openapi.Go` writes models and, with `Server: openapi.StdHTTP`, a `net/http` handler
  interface to `gen/rest`.
- The TypeScript recipes write to `gen/graphql` and `gen/openapi` of the importer.

The repository fixture `tests/fixtures/repos/codegen` is a complete, tested project with a
connect, GraphQL and REST server and Go and TypeScript clients.

## Caching and early cutoff

Every generated tree is a package of its own: `//:gen-proto-greet-v1` for `gen/proto/greet/v1`
(the label is `gen-` plus the directory without `gen/`, with `/` replaced by `-`). bob builds
these packages first and adds their output to the store by content. Consumers depend on the
content, not on the generator run: when a schema change produces byte-identical code (a
comment, whitespace), nothing downstream rebuilds.

## Keep the tree fresh

`bob build` and `bob dev` rewrite generated files as needed. Right after a clone, or after
switching branches, `bob gen` writes them without building anything else, so your editor
resolves imports at once:

```bash
bob gen                  # everything
bob gen //svc/...        # only the generated files of these packages
```

`bob status` shows the state of every generated root and of the lock:

```text
fresh    gen/graph/               //:gen-graph
fresh    gen/proto/greet/v1/      //:gen-proto-greet-v1
missing  gen/rest/                //:gen-rest
         gen/rest does not exist
fresh    web/node_modules/        //web:client
fresh    bob.lock
```

| State | Meaning | Fix |
|---|---|---|
| `fresh` | matches what bob would write | — |
| `stale` | the inputs changed since bob wrote it | `bob gen` |
| `missing` | not written yet | `bob gen` |
| `edited` | changed after bob wrote it | see below |
| `orphan` | written by bob, but no recipe owns it any more | delete it |

## When a generated file was edited

For codegen directories, `bob status` shows `edited`, and `bob gen` refuses to overwrite the
change:

```text
error[BOB-E-1016]: gen/rest/items.gen.go conflicts with generated output of //:gen-rest: it was edited after bob wrote it
  hint: move or delete the file, or run bob gen --force (keeps a backup)
```

`bob gen --force` rewrites the directory and keeps a copy of what it replaced in
`.bob/gen/backup/<timestamp>/`. Move the change into its real source (the schema, or a
hand-written file outside the generated directory).

For native files, `bob check` reports a hand edit as `BOB-E-1011` ("generated file go.mod was
edited by hand"), and `bob check --fix` rewrites the file. `bob build` refuses to run with an
edited native file, so the build never uses a file that differs from `bob.go`.

> [!NOTE]
> `bob check` does not look at codegen directories yet; use `bob status` for them.
> `bob gen --check` (a dry run for CI) is not implemented yet and exits with `BOB-E-9008`.

## Git ignore rules

bob never edits your `.gitignore`. It keeps its own ignore entries in a marked block of
`.git/info/exclude` (`.bob/` and other paths bob owns) and puts a `.gitignore` with `*` into each
codegen directory. `bob check` reports a bob path that git does not ignore (`BOB-E-1013`);
`bob check --fix` adds it.

## Next steps

- [Code generation quickstart](../03-quickstart/06-codegen.md).
- [Dev environments](05-dev-environments.md): what `bob dev` prepares.
- [CLI reference: bob gen](../05-cli/01-commands.md#bob-gen).
