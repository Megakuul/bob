# I-M10b progress (M10.3 GraphQL for Go and TS, M10.4 OpenAPI for Go and TS)

Owner of: pkg/graphql/** (API, gen, bop), pkg/openapi/** (API, gen, bop).
Coordinates with I-M10a (pkg/proto, pkg/connect, shared codegen early cutoff).

## coordination with I-M10a (started 22:30)
- I reuse I-M10a's shared codegen helpers (cutoff = RealiseNow + content-addressed copy,
  "code generation failed" translation, runtime pre-check) once they exist; until then I write
  mine behind small functions so they can be swapped.
- Shared touch points both units need (whoever lands first, the other reuses):
  1. pkg/npm: `npm.Generated` is an interface with an unexported method, so codegen packages
     cannot implement it; needs a concrete struct like golang.Generated (Kind, Dir, Deps, Spec,
     Deps on npm packages at generator versions) and npm/gen support (materialise + include in
     the importer's tree/tsconfig).
  2. pkg/golang/gen: generated packages are read from their planned output (exists, load.go).

- 22:40 read I-M10a.md: I-M10a owns npm.Generated (struct), the label rule
  `gen-<dir without leading gen/>`, pkg/engine/codegen.go (consumer lookup, cutoff realise +
  content import, materialisation entry, BOB-E-1021 text), Go generated packages, translator
  codegen rendering. I consume those and do not edit them.
- Questions/proposals to I-M10a (please answer in I-M10a.md):
  Q1. Job kind: SPEC §03.6.2 says job `codegen`. Do you write one generic bob-builder kind
      `codegen` (args: input trees copied to repo-relative paths, files to write (configs),
      argv/env/PATH, the output dir to collect, regular-files-only + no .gitignore check, the
      generator's stderr as the excerpt)? If yes I use it for gqlgen, oapi-codegen and the
      node drivers and add no bop of my own. If you do not, I write it in pkg/graphql/bop and
      you may reuse it.
  Q2. gqlgen needs the consuming Go module's build list (go.mod/go.sum + module trees of the
      gqlgen runtime closure) inside its derivation: I plan an exported helper in a NEW file
      pkg/golang/gen/codegen.go (module trees of a go.module's build list, as store paths) and
      a vendor/ layout in the job. Tell me if you already have one.
  Q3. Tool locks (graphql-codegen, openapi-typescript): I add a NEW file pkg/npm/gen/tool.go
      (exported: install a lock.ToolLock as a node_modules derivation with the §03.4 installer).

- 22:55 I-M10a answered (I-M10a.md): core kind "run", PlanCodegen/ConsumerOf/OwnedDir/
  RegisterCodegenKind, golang.GeneratorRuntime, npm.Generated{Tools, At, Runtime}. Used as is.

- 23:20 PROBLEM for both of us (to I-M10a): `bob build //svc:gen-graph` (and your
  //svc:gen-proto-greet-v1 syntax-error test) evaluates only the target's closure, so the
  consumer go.module / npm importer is NOT in req.Graph and engine.ConsumerOf returns nil
  ("no Go module realises it", BOB-E-9004). Proposal (your files golang.go, npm api.go,
  engine/codegen.go): when the consumer realises a recipe it adds the consumer facts to the
  codegen node's spec: marshal g.Spec to a JSON object and add key "consumer":
  {"kind":"go.module"|"npm.app"…, "label":"//svc:module", "dir":"gen/graph" (consumer-relative),
  "owned":"svc/gen/graph" (repo-relative), "path":"example.com/svc", "go":"1.26"} (path/go only
  for Go), plus `engine.CodegenConsumerOf(n) (CodegenConsumer, error)` reading it. Generators
  decode their own fields non-strictly, so nothing else changes. I switch to it as soon as it
  exists (my code reads consumer facts in one place per package). Reply in I-M10a.md please.

- 23:50 RESOLVED on my side (no I-M10a change needed): my Go specs implement
  golang.ModuleAware (module path) and carry their Dir; the owned dir is label dir + Dir; the go
  directive comes from bob.lock's workspace entry. So a gen package plans the same derivation
  with or without its consumer in the graph (unit test TestPlanGoStandalone).
- 23:30 to I-M10a (fixed by them since, D-M10a-6): engine.CodegenOutput trusts os.Stat, but a codegen build that failed in
  RealiseNow leaves its output path on disk (measured: empty dir + `<path>.lock` in
  BOB_HOME/nix/store after each failed `nix build` while planning, recreated after rm). The Go
  planner then reads the empty dir: "BOB-E-1015 ... no buildable Go source files in
  .../svc-gen-graph" instead of the gqlgen message. Proposal: when req.Store is a
  PlanRealiser, a Resolved without Cutoff means "failed while planning" → not ok (Blocked);
  only without a realiser fall back to a validity check (st.Invalid), not Stat.

## done (checkpoint 23:15)
- pkg/openapi: NewSpec (kind openapi.spec, label //<dir>:openapi-<file base>, the document is
  the node's source), Go/GoOpts/Server (StdHTTP), TS/TSOpts; internal/oaspec payloads.
- pkg/graphql: NewSchema (graphql.schema, label //<dir>:graphql), Gqlgen/GqlgenOpts (runtime =
  golang.GeneratorRuntime(gqlgen, exact) + gqlparser >= v2.5.33), TS/TSOpts (documents are a
  graphql.documents node with the Documents sources); internal/gqlspec.
- pkg/openapi/gen, pkg/graphql/gen: ecosystems "openapi", "graphql" (no lock, no native,
  Materialises), codegen kinds registered; jobs are bob-builder "run" jobs through
  engine.Jobs, finished with engine.PlanCodegen (cutoff + materialisation).
  - oapi-codegen: spec at its repo path, go.mod (module path + Go of the consumer), config,
    output straight into $out/<spec base>.gen.go.
  - gqlgen: /build/src is a module root (go.mod of the consumer's path, vendor/ = symlinks to
    the build list's module trees + modules.txt, -mod=vendor, GOPROXY=off), schema at repo
    paths, gqlgen.yml (JSON) with skip_mod_tidy/skip_validation, owned dir at its
    module-relative path (import paths right), copied to $out.
  - TS: tool locks (embedded toollock/*.json, refreshed by `go test -run TestToolLock
    -update-tool-lock`), installed by npm/gen ToolModules (new pkg/npm/gen/tool.go), bob-owned
    driver.mjs (plugin/library API). graphql.ts → gen/graphql/graphql.ts, openapi.ts →
    gen/openapi/index.d.ts.
- Shared (small, outside ownership): engine.PlanRequest.Locks (all bob.lock sections) +
  build/plan.go passes it; pkg/golang/gen/codegen.go (GeneratorModuleOf: build list module
  trees + package lists for gqlgen's vendor/); pkg/npm/gen/tool.go (ToolModules, Tool.Prepare,
  ResolveToolLock); pkg/cli/link.go blank imports.
- E2E (private repo /var/tmp/i-m10b/repo, codegen fixture minus proto/TS): bob lock + bob build
  of the server: GET /rest/items/42 → {"id":"42","name":"item 42"}, POST /graphql →
  {"data":{"hello":{"greeting":"hello gql","length":9}}}; svc/gen/{graph,rest} materialised.

- pkg/builder/unpack.go: a repeated regular-file entry is accepted, the last one wins (as tar
  and npm do). Found with openapi-typescript's tool lock: agent-base 7.1.4 (a dependency of
  @redocly/openapi-core) ships package/dist/index.js and package/./dist/index.js; bob refused
  it ("duplicate entry"). Symlink/dir replacements stay refused; a hard link to itself is a
  no-op (copying would truncate). Test pkg/builder/unpack_duplicate_test.go. BuilderVersion
  11 → 12, builders.go regenerated (go generate ./pkg/engine/store).
- Unit tests green: pkg/openapi/gen (plan Go, consumer, determinism, tool lock),
  pkg/graphql/gen (gqlgen plan incl. vendor layout/modules.txt/go.mod/gqlgen.yml, not locked,
  tool lock, package name), pkg/builder TestUnpack*.
- E2E TS: //web:gen-graphql builds (graphql-codegen tool lock + driver) in the private repo.

- E2E TS (private repo): //web:gen-graphql and //web:gen-openapi build; `bob run //web:client`
  against the Go server prints "ts graphql: hello ts-gql length=12" and "ts rest: 7 item 7";
  `bob gen` + `bob env` + `.bob/bin/tsc --noEmit -p .` in web/ exits 0 (and a wrong field type
  gives TS2322 against the generated paths type).
- Errors (full build of server + client, schema with `type Other { ff: Nope }`):
  `✗ //svc:gen-graph code generation failed (defined at svc/bob.go:18)` +
  `| failed to load schema: schema/schema.graphqls:9:18: Undefined type Nope.` and
  `✗ //web:gen-graphql ...` + `| Unknown type: "Nope".`; bad $ref: `| error loading swagger
  spec in schema/items.yaml` + `| : failed to load OpenAPI specification: map key "Itm" not
  found`; openapi.TS: `schema/items.yaml: Can't resolve $ref at #/paths/...` (driver prints
  the thrown message once with the document path).
- pkg/npm/gen/dev.go: when no importer has a direct typescript dependency (the codegen
  fixture's web App is compiled by esbuild), PrepareDev adds the pinned nixpkgs typescript
  (tsc, tsserver shims). Without it R-GEN-002a/003a's `.bob/bin/tsc` does not exist.

## next
- fixture acceptance tests (TestRGEN002a*, TestRGEN003a*) with a private recording mirror
  (/var/tmp/i-m10b/registry, copy of the shared snapshot + what the codegen fixture loads).
