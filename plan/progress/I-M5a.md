# I-M5a progress (M5.1 common ecosystem rules, M5.2 pkg/golang + gen + bop)

Owner of: pkg/golang, pkg/golang/gen, pkg/golang/bop (+ pkg/golang/internal/gospec).
M5.1's framework (native.json, drift 1011, backups, stale-lock 1012 via engine.LockFreshness)
was built by I-M4f in pkg/engine/build/native.go; no pkg/engine/eco package exists (SPEC §03.0.1
puts the shared types in pkg/engine; a new pkg/engine/eco would also need a protected
layering-test row). The Go side of M5.1 is here: Native (go.mod/go.sum + Explain), Stale.

## done (checkpoint 17:20)
- pkg/golang API (evaluator side): NewModule/ModuleOpts/Require/Requirement/Generated,
  Module.Binary/BinaryOpts/CGo (CGoAuto, CGoOff), Module.Cgo/CgoOpts/Package,
  Module.Tests/TestOpts (Name, Tags, Services, Args, Timeout, CGo)/TestSet, Module.CArchive/
  CShared/CLib. Kinds go.module, go.binary, go.package, go.tests, go.test. Toolchain default
  nixpkgs.Tool(p, "go_1_<minor>"), cgo C compiler nixpkgs.Tool(p, "gcc").
  Labels: binary //<module dir>/<dir>:<name>; "..." test set //<bob.go dir>:<name|tests> with
  units //<pkg dir>:go_test (dirs with _test.go found by the evaluator); a one-package test
  set is the unit itself, //<pkg dir>:<name|go_test> (svc fixture //it:it, //it:never).
- pkg/golang/bop: bob-builder kinds go-std (go list -export -deps std), go-tools (cmd/pack),
  go-compile/go-cgo/go-link/go-testmain (compile with importcfg, embedcfg, asm+symabis, cgo
  sequence of cmd/go with path maps, pack, link with modinfo, -X, external link for cgo).
- pkg/golang/gen: engine.Ecosystem "go" + engine.LockFreshness. Plan: go/build in-process,
  own import resolution (workspace modules from the graph or from go.mod files on disk,
  bob.lock modules, generated packages, std), hybrid groups (Tarjan SCC merges module
  cycles), one source import per workspace package (PlanStore.ImportSources), std per
  (toolchain, target, cgo), cgo flags from Module.Cgo deps (LinkInfo or nixpkgs dev/lib +
  rpath), link, test binary with bob's _testmain.go (go command's template, Go 1.26), test-run
  job (format go, services, timeout). Native: go.mod (header, module, go X.Y.0, direct +
  indirect blocks) and go.sum from bob.lock, Explain for R-FW-061 lines. Lock: inputs +
  workspaces (requirements need M5.3: BOB-E-9008 unless the previous section resolves them).
  Stale: per module of the graph (partial graphs safe).
- Unit tests pkg/golang/gen/plan_test.go: plan of binary+test, granularity (main.go change →
  main pkg + link; library change → importers + link + its test), resolve error, every job
  decodes strictly into bop's/bspec's types.
- End-to-end in a private copy (/var/tmp/i-m5a/repo with the NEEDS below applied locally):
  a repo with module m/, binary (embed, asm package, cgo package with own C file + nixpkgs
  zlib via Module.Cgo), test units (internal, external test package, example, cgo, asm):
  bob lock, bob build //..., bob test //... all work; the binary runs under env -i
  (RUNPATH has zlib/glibc); a failing test shows the case and BOB-E-2005.

## next
- run tests/eco/golang synthetic tests in the copy; cutoff (R-GO-005) needs engine support
  (NEEDS); register bop kinds in cmd/bob-builder + regenerate builders (end of unit).

## files (outside ownership, small, reported)
- pkg/bob/internal/graph/graph.go, pkg/bob/node.go, pkg/bob/ext/ext.go: ext.NewIn (label dir
  other than the bob.go's: Go binaries //<module dir>/<dir>:<name>, test units), ext.Errorf
  (definition errors of ecosystem constructors). Framework zip must be regenerated.
- pkg/engine/engine.go: PlanRequest.Root, PlanRequest.Store (additive);
  pkg/engine/planstore.go (new): engine.PlanStore interface.

## DECISIONS
- D-M5a-1: M5.1 framework lives in pkg/engine/build (I-M4f); no pkg/engine/eco.
- D-M5a-2: test unit labels: see "done" (one-package sets are the unit; svc fixture needs
  three sets of one package with distinct names).
- D-M5a-3: a "..." test set lists packages with _test.go files at evaluation (the evaluator
  reads the module's directories); the eval key does not cover them, so a new test package
  appears at the next evaluation that misses the key (eval owner may add the dir list to the key).
- D-M5a-4: Module.Cgo declarations reach binaries/tests through the module object (declared
  before them in bob.go order) and at plan time through the graph (go.package nodes of the same
  module and dir). A package importing "C" without a declaration uses libc and its own C files.
- D-M5a-5: a workspace package imported from another workspace module is compiled with the
  importing closure's toolchain and its own module's -lang: Go cannot mix export data of two
  toolchains (measured: go1.26.9 compile refuses a go1.25.10 archive, "object is [go1.25.10 …]
  expected [go1.26.9 …]"). See TEST PROBLEM 1.
- D-M5a-6: CGoAuto = cgo on iff a non-std package of the closure has cgo files; otherwise std and
  everything build with CGO_ENABLED=0 (static binary, no C compiler input).
- D-M5a-7: Go jobs skip the fixup (NoFixup): archives must stay byte-exact; binaries come out of
  the linker with -buildid= and trimmed paths; C objects use -ffile-prefix-map so no source
  store path reaches debug info.
- D-M5a-8: compile flags in the job are an argv list ("flags": ["-p", ip, "-lang=go1.26"]) so the
  job names the package as the compiler sees it (tests grep `-p <path>` in bobSpec).
- D-M5a-9: errors: import without module → BOB-E-1015 (Go, root label, hint golang.Require);
  invalid declarations at plan time (pkg-config, C++ files in Go packages, CGoOff + cgo,
  duplicate module) → BOB-E-1020; toolchain version ≠ ModuleOpts.Go → BOB-E-1008.
- D-M5a-10: test binaries run with -test.v=true -test.paniconexit0 -test.timeout=<unit
  timeout>s, then TestOpts.Args, then `bob test --` args; cwd is the build dir (see NEEDS 5).

## NEEDS
1. I-M4f (pkg/engine/build plan.go, ~20 lines): fill `engine.PlanRequest.Root` (o.Root) and
   `PlanRequest.Store` (engine.PlanStore): HostPath = hostPath(st, p); ImportSources =
   srcs.Import(ctx, st, trees); Ensure = the run's fetcher Ensure. Exactly what the private
   copy does: /var/tmp/i-m5a/sync.sh. Without it Go planning fails with an internal error
   naming PlanRequest.Store.
2. I-M4f (pkg/cli/link.go): blank-import `github.com/megakuul/bob/pkg/golang/gen`.
3. eval owner (pkg/engine/eval scan.go + gen.go, ~30 lines; patch ready:
   /var/tmp/i-m5a/patches/eval-nested-gomod.patch, verified): the scan stops at every
   directory with its own go.mod (R-FW-002), but golang.NewModule's bob.go sits next to the
   go.mod bob generates for that module (go fixture api/, lib/, tools/; synthetic syn/). After
   the first build writes syn/go.mod, `bob graph` says "//... matches no package". Fix: a go.mod
   whose first line is bob's header ("// Code generated by bob from bob.go. DO NOT EDIT.") does
   not end the scan, and the evaluator overlay deletes it (and its go.sum) so go build sees the
   directory as part of the root module (verified: overlay "" on sub/go.mod makes
   `go build ./sub` work from the root module). SPEC R-FW-002 scan text needs the same
   exception (owner/SPEC).
4. translator / I-M4f: R-GO-002a counts `run.finish counts.built` as derivations ("exactly 2
   Go derivations rebuild (package + link)"); the translator counts packages (labels), so a
   binary's package + link rebuild shows built:1. counts.built must count bob derivations Nix
   built (or the Go tests cannot pass).
5. builder (I-M4c's test-run): tests that read testdata/ need the package directory as cwd;
   test-run's Cwd must be writable (build dir). A `copy` of a source tree into the build dir
   before the test (e.g. TestRunArgs.Copy {from,to}) would let Go set cwd to the package copy.
6. engine (codegen + R-GO-005 cutoff): both need "build some derivations during planning, add
   their output (or part of it) to the store by content, plan dependents against that path"
   (§03.6.3). Proposal: optional `PlanStore.Realise(ctx, drvs)`; the Go planner then plans
   level by level in cutoff mode. Until then BOB_GO_CUTOFF=1 is not implemented and generated
   Go packages must already be built when Go plans (they are read from their output path).
7. coordinator: cmd/bob-builder must merge `gobop.Kinds()` (pkg/golang/bop) into
   builder.Core(); then `go generate ./pkg/engine/store` and BuilderVersion bump (builder
   behaviour changed). Done in the private copy; I do it in the shared tree at the end of
   this unit unless told otherwise.
8. M5.3 (Go resolution in bob lock, MVS over GOPROXY .mod files) is needed by every fixture
   with golang.Require (go, acme, polyglot, codegen); Lock returns BOB-E-9008 for them now.
9. go.mod of a module whose directory holds bob.go files should also require the framework
   (§03.2.5 "build-definition requirements §01 contributes"); needs the bob version (eval) in
   the Native request — M5.4.

## TEST PROBLEM
1. tests/eco/golang/toolchain_test.go TestRGO004aToolchainPerModule: with lib at Go 1.25 and
   tools at 1.26, tools/cmd/stamp imports example.com/lib/fastadd and lib/strutil, so those
   packages must be compiled with the 1.26 toolchain inside stamp's closure (Go refuses to
   import an archive of another toolchain, evidence D-M5a-5); the test then finds a compile job
   of example.com/lib/fastadd naming go-1.26.x and fails. SPEC R-GO-004a "each package is
   compiled with its module's toolchain" cannot hold for packages imported across modules with
   different Go versions. Fix options: the test only checks jobs of lib's own closures
   (lib's test units), or the fixture's tools module does not import lib.
