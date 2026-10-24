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

- Unit tests: pkg/connect/gen/gen_test.go (job decodes strictly into bspec.RunArgs/ExecArgs, M
  options, cutoff + materialisation, failure fallback, TS plugin, module required),
  pkg/proto/gen/gen_test.go (library sources, lint = test-run unit), pkg/engine/translate/
  codegen_test.go (codegen head, repeated Nix failure once, 1021 head), eval nested-module tests,
  pkg/proto + pkg/bob/ext example tests. polyglot and go fixtures (with the tools/tar overlay of
  tests/outputs) evaluate (`bob graph`) with the private build.
- Golden candidates: out/golden-candidates/gen/{proto-syntax-error,runtime-mismatch-f5}.txt
  (made on the scratch repo, line numbers set to the fixture's svc/bob.go:22, web/bob.go:26).
- framework.zip regenerated (23:25; includes I-M10b's WIP API packages present at that time).
- No pkg/builder change: BuilderVersion stays 11.
- Checks: gofmt clean, vet + staticcheck clean on touched packages; tools/ci fmt + layering ok;
  vet fails only on out/uicheck (R-ARCH-001, not mine), lint only on internal/worker/zz_debug.go
  (other unit). go test ./pkg/...: only budget tests under load ~26-38 fail (srcs
  TestImportManyTiming 41 s, store TestBuilderChain100Overhead 16 s; not touched).

## next
- nothing in this chain beyond NEEDS.

## not run (and why)
- tests/eco/gen acceptance: the shared tree's cmd/bob does not compile now (pkg/cli
  cmd_install_profile.go: e.defaultBinDir undefined, another unit's WIP), the codegen fixture
  needs I-M10b's gqlgen/openapi, and the offline registry mirror is empty (I-M9b NEEDS 1). The
  same flows were run by hand on the scratch repo (see above).

## DECISIONS
- D-M10a-1 proto checks (Lint, Breaking) are test units (bob-builder test-run): `bob test` and
  pipelines run them, `bob build //...` leaves them out (R-GEN-005 counts exactly the 2 codegen
  rebuilds although the fixture declares //proto/greet/v1:lint). Labels //<dir>:lint|breaking;
  library label //<dir>:proto.
- D-M10a-2 Breaking reads the baseline .proto files with `git ls-tree`/`git cat-file` at plan
  time (revision validated, `--end-of-options`, ≤ 2000 files / 64 MiB) and imports them as a
  source path; buf 1.73 cannot apply --path to the against input, so imported libraries are
  excluded with --exclude-path instead.
- D-M10a-3 codegen jobs use the core "run" kind (no new bop kind, no BuilderVersion bump); buf.yaml
  and buf.gen.yaml are passed as --config/--template JSON data.
- D-M10a-4 connect.Go/TS realise the imported libraries as their own recipes (default dirs,
  labels gen-proto-common-v1 …), as S6 did; label = "gen-" + owned dir without leading "gen/"
  (SPEC example; text amended). The package is defined at the recipe call (ext.Here/NewAt).
- D-M10a-5 generator runtime versions come from the pin at lock time (placeholder
  "nixpkgs:<attr>" resolved by the Go/npm lock with the nixpkgs index); explicit pins are checked
  there (Go: protobuf/connect at least the generator version, gqlgen exact via
  GeneratorRuntime(…, true); npm: the range must admit the generator version). The mismatch
  uses BOB-E-1021's existing template with the SPEC sentence as reason and a `✗ <label>: <reason>`
  head; the hint stays the code's ("align the versions named above"): a new hint with arguments
  would change codes.lock (append-only) and the protected codes golden.
- D-M10a-6 a consumer reads generated files only from the content-addressed copy; a failed build
  can leave an empty, invalid output directory, so without a copy the Go consumer plans a blocked
  derivation that depends on the failing codegen (the build reports the generator's error).
- D-M10a-7 bob.go may import a bob.go package of a bob-generated nested Go module by that
  module's path; the evaluator compiles it as the root module's package of that directory (import
  literal rewritten in an overlay copy, same line).

## NEEDS
1. I-M10b: TestRGEN002aUndefinedTypeInSchema expects `│ …` excerpts; bob prints `|` outside a
   terminal (§02.5) — same correction as item 40 of plan/test-corrections.md.
2. coordinator: TestAPISurface already failed before this unit (pkg/bob/ext New/NewIn both return
   bob.Node, R-FW-014); ext.NewAt adds a third. Needs a specSharedResults row for pkg/bob/ext
   (owner/test correction) or a different extension API.
3. M5.6: bob's own proto/ generation moves into bob only with self-hosting (not done, as asked).
4. bob dev: regeneration on .proto edits (R-FW-067 budget) and gopls/tsserver checks through the
   shims were not verified here (I-M6a / dev owner).

## TEST PROBLEM (corrections applied, owner-approved evidence-based class; test-corrections 38-40)
- proto.Library func/type clash → proto.NewLibrary (fixtures + SPEC).
- TestAPISurface: pkg/proto *Check shared by Lint and Breaking (SPEC API).
- TestRGEN001aProtoSyntaxError: buf's real message for the injection, `|` excerpt prefix.
