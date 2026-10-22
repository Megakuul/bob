# I-M10a progress (M10.2 protobuf/connect Go+TS, M10.5 cutoff + runtime pre-check, eval R7 nested module)

Owner of: pkg/proto (+gen, bop if any), pkg/connect (+gen). Shared codegen plumbing (also for
I-M10b's graphql/openapi): pkg/engine/codegen.go (new), the npm side of `npm.Generated`
(pkg/npm api + gen), the Go side of generated packages (pkg/golang/gen load/plan), the codegen
error rendering in pkg/engine/translate. Eval R7 fix: pkg/engine/eval/rules.go.

## coordination with I-M10b (graphql/openapi)
- I-M10a provides (in progress, see "done" when ready): `npm.Generated` as a struct shaped like
  `golang.Generated` (Kind, Dir, Deps, Spec, Require as npm deps), the label rule
  `//<consumer dir>:gen-<owned dir without leading "gen/", '/'→'-'>`, and `pkg/engine/codegen.go`
  helpers (consumer lookup, early-cutoff realise + content import, materialisation entry,
  BOB-E-1021 runtime pre-check text). Please consume rather than duplicate; do not edit those
  files without a note here.

## answers to I-M10b (22:55)
- Q1: no generic bop kind. connect uses bob-builder's core kind "run" (steps mkdir/copy/write/
  exec, see pkg/connect/gen/gen.go plan()) through engine.Jobs; no BuilderVersion bump. Sources
  are copied to /build/src at repo paths, buf runs there, the owned tree is copied to $out.
  engine.PlanCodegen refuses outputs with non-regular files or a .gitignore (then no cutoff).
  If you need a kind of your own, write it in your bop; nothing of mine depends on it.
- Q2: I have no build-list helper; a new pkg/golang/gen/codegen.go of yours is fine.
- Q3: fine.
- READY to use (in the tree now):
  - golang.Generated{Kind, Dir, Deps, Tools []string (nixpkgs attrs → nixpkgs.Tool deps after
    Deps), Spec, Require, At ext.Site (= ext.Here() in your constructor: definedAt of the gen
    package, the test wants the line of graphql.Gqlgen(...)), Also}; golang.GeneratorRuntime(path,
    attr, exact) = requirement at the pinned generator's version ("v"+nixpkgs version) resolved by
    the Go lock, which also does the R-GEN-006 pre-check (exact for gqlgen) → BOB-E-1021 with
    the SPEC text via engine.RuntimeMismatch. golang.GenLabel(dir) = label name.
  - npm.Generated{Kind, Dir, Deps, Tools, Spec, Runtime []npm.GenRuntime{Name, Generator},
    At, Also}; npm lock pins runtimes and pre-checks (range must admit the generator version).
    npm/gen copies each generated output into the importer's work dir at Dir.
  - pkg/engine/codegen.go: RegisterCodegenKind (call in your gen init: failures render as
    `✗ <label> code generation failed (defined at f:l)` + last 10 log lines without builder
    lines), ConsumerOf(g, id, kinds...), LabelDir, OwnedDir, PlanCodegen(ctx, req, Codegen{Node,
    Drv, Path, Out, Dir}) → (Resolved with Cutoff, Materialisation) (call it after your jobs are
    added; return the Materialisation in PlanResult.Materialise and implement
    engine.Materialiser.Materialises), CodegenOutput (consumers), Blocked.
  - engine.Resolved.Cutoff: Go and npm consumers depend on the content copy (inputs.add).
  - Packages built while planning are reported "built" (translate.Context.PreBuilt).

## done (checkpoint 23:10)
- pkg/proto (NewLibrary, Library.ImportRoot/Dir/Imports/Closure, Lint, Breaking; kinds
  proto.library (resolves to its imported .proto sources), proto.check (test-run unit running
  buf lint / buf breaking; Breaking baseline read with git at plan time and imported as a source
  path)); pkg/proto/gen ecosystem "proto"; example tests.
- pkg/connect (Go, TS; Also recipes for imported libraries; runtimes via
  golang.GeneratorRuntime / npm.GenRuntime); pkg/connect/gen ecosystem "connect": one bob-builder
  "run" job per recipe (sources at repo paths in /build/src, buf generate --config/--template
  JSON, protoc-gen-go M options from the module path (golang.ModuleAware) and Imports), early
  cutoff via engine.PlanCodegens (one RealiseNow + one ImportSources per wave), Materialiser.
- pkg/engine/codegen.go (shared), engine.Resolved.Cutoff; Go and npm consumers depend on it;
  Go generated trees with several packages (prefix match in load.go, resolve, wsdeps); Go blocked
  plan when codegen failed while planning; Go/npm lock resolve generator versions from the pin
  and pre-check explicit pins (BOB-E-1021, R-GEN-006), also from Stale (build.staleSections
  re-asks with the index when stale).
- pkg/bob: ext.Here / ext.NewAt (definedAt of a codegen package = the recipe call line).
- pkg/golang: Generated.Tools/At/Also, GeneratorRuntime, ModuleAware, GenLabel (label rule).
- pkg/npm: Generated struct + GenRuntime; npm/gen copies generated outputs into the work dir.
- translate: codegen failure head (`✗ <label> code generation failed (defined at f:l)`), 1021
  runtime head, PreBuilt packages reported built, a failed drv's repeated Nix message reported
  once, fallback excerpt without bob:: lines.
- eval R7: imports of a nested bob-generated Go module path (example.com/tools) accepted and
  rewritten in the evaluator overlay (same line); SPEC R-FW-002 R7 row (spec/01 + SPEC.md);
  tests pkg/engine/eval/nested_import_test.go (TestOwnDirNestedModule, TestNestedModuleImport).
- E2E on /var/tmp/i-m10a/repo (codegen fixture without graphql/openapi; private build copy
  /var/tmp/i-m10a/src via /var/tmp/i-m10a/sync.sh because I-M10b's WIP pkg/graphql/gen did not
  compile at 22:55): lock (protoc-gen-go 1.36.11 → google.golang.org/protobuf v1.36.11,
  connect v1.20.0, @bufbuild/protobuf 2.11.0), build //... , Go server + Go client + TS client
  (bob run) all answer; whitespace-only greet.proto change → counts.built 2, built labels
  //svc:gen-proto-greet-v1 //web:gen-proto-greet-v1 only; no-change build 1.0-1.15 s
  [8c/15G, load ~26, shared store]; syntax error → `✗ //svc:gen-proto-greet-v1 code generation
  failed (defined at svc/bob.go:18)` + `| proto/greet/v1/greet.proto:9:1:unexpected `}` after
  definition`; F5 (@bufbuild/protobuf 2.10.0) and a Go pin protobuf v1.36.0 → BOB-E-1021 exit 3 at
  lock and at build; bob gen materialises (conflicts 1016, --force backup); bob test lint +
  breaking pass/fail with buf's messages.

## next
- unit tests (proto/connect gen), golden candidates, CI stages, framework.zip, fixtures on
  polyglot/go evaluation.

## DECISIONS

## NEEDS

## TEST PROBLEM
