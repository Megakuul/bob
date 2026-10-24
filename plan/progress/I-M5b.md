# I-M5b progress (M5.3 Go resolution, M5.4 go.mod/go.sum, M5.5 cross-repo, R-GO-005 hook)

Owner of: pkg/golang/** (new pkg/golang/resolve); go-section glue in pkg/engine/build (lock/native).

## done (checkpoint 18:05)
- pkg/golang/resolve (L2, std + diag only): GOPROXY client (http/https/file, size caps,
  retries, local cache dir in GOPROXY layout = bob's goproxy dir), semver + Go version order,
  go.mod parser (module/go/require, major-version suffix rule), h1 (dirhash Hash1/HashZip),
  module zip checks + Extract (same tree as bob's fetcher), cmd/go module graph with pruning
  (readModGraph semantics), package loading of "all" (any-tag rule, workspace tests, deps'
  non-test imports, cross-workspace imports, generated-code runtimes), root fixpoint, toolchain
  check, pins and --update. Tests: synthetic graphs compared with the real go command
  (go list -m all, go build, go vet offline), toolchain/not-found errors, update+pins, semver.
- pkg/golang/gen: Lock uses resolve (no more BOB-E-9008), tree hash = NAR of the extracted zip,
  Stale accepts selected ≥ declared; go.mod requirements = resolve.Roots at selected versions;
  go.sum h1 for package modules + /go.mod for every locked version of a build-list path;
  planner picks third-party versions from the closure's main module build list; module trees
  fall back to the local zip (file:// proxy or bob's goproxy dir) when the fetcher cannot get
  them.
- engine.LockRequest.Root (additive); build/lock.go: Root passed, per-module go change lines
  (`~ go golang.org/x/net v0.1.0 → v0.2.0`), sections compared by content (re-lock printed a
  spurious change before).
- Manual: scratch repo with uuid: bob lock (second run 0.14 s, no network), bob build, bob gen;
  go list -m all / build / vet with GOPROXY=bob's goproxy dir OK.

- Root go.mod (NEEDS 9/12 of I-M5a): golang/gen.RootGoMod + BobImports (bob.Import calls of
  every bob.go, string literals, highest version wins): header, module = IR module, go =
  framework language (1.26.0, test keeps it equal to bob's go.mod), require framework at the
  IR's bob version + imports; committed; Explain adopts a go.mod from `go mod init` / an older
  bob (only an added requirement or another module line is drift). Wired in
  build/native.go (Check and generate) whatever ecosystems the graph uses; skipped when a Go
  module of the repo generates go.mod at the root.
- R-GO-005: planner hook: BOB_GO_CUTOFF=1 → BOB-E-9008 naming what is missing (optional
  PlanStore extension `RealiseNow` = realise-during-planning, and the -linkobj split in
  go-compile). Not implemented beyond that (see NEEDS).
- gen tests: lock_test.go (Lock/Native/Stale with a file proxy, the go command checks the
  generated go.mod offline, second lock without proxy), root_test.go.

- Cross-repo (R-FW-022) verified in the private copy /var/tmp/i-m5b/tree with the eval patch
  (NEEDS 1): app with bob.Import served from a file:// GOPROXY: bob lock, bob build
  //use:consumer builds example.com/dep//libs/bar:lib, bob graph shows the label, bob gen
  writes go.mod requiring example.com/dep v1.0.0 + the framework, the next evaluation works
  with it, bob check: no problems.
- Lock --update scenario of TestRCLI010Lock emulated by hand (go fixture without api/lib,
  file:// proxy): plain re-lock "no changes", --update golang.org/x/net lists exactly x/net and
  x/text v0.1.0 → v0.2.0, next plain lock keeps the pins, bob build writes tools/go.mod with
  the moved version. (The test itself stops earlier on the acme fixture: exit -1, npm part.)
- Skeleton adoption: a go.mod with no requirements (bob's PLACEHOLDER fixtures, go mod init)
  is adopted instead of drift (missing requirements are not a user edit); added/changed ones
  still are.

- R-GO-002b by hand on the real corpus (scratch repo per module like tests/eco/golang
  corpusRepo, real proxy.golang.org, script /var/tmp/i-m5b/scripts/corpus.sh): bob lock + bob
  gen, then `go list -m all` (GOFLAGS=-mod=readonly GOSUMDB=off GOTOOLCHAIN=local, GOPROXY =
  bob's goproxy dir) equals bob's build list and go build/vet ./... pass offline for: go-cmp
  (1 module), x/text (4), testify (6), uuid (1), cobra (7), zap (8), protobuf (4), grpc (26),
  prometheus (37), client-go (77), bob's own requirements (51). Found and fixed on the way:
  gopkg.in .v1 pseudo-version rule, go line raised to deps' patch level (lake needs 1.26.1).
  [8c/15G shared host, disk nearly full: one Nix ENOSPC on retry, not a resolver issue]
- go.mod go line = declared X.Y.0 raised to the highest go line of a selected module of the
  same language version (the go command wants go.mod updated otherwise); bob.lock keeps the
  declared one.

## next
- nothing in this chain beyond NEEDS / TEST PROBLEM.

## DECISIONS
- D-M5b-1: go.mod roots rule: every build-list module that some workspace module declares or
  that provides packages to some workspace (bob.lock `packages`) is a requirement, at its
  selected version; Resolve iterates to that fixpoint, so Native derives go.mod from bob.lock
  alone and the go command selects exactly the locked build list.
- D-M5b-2: BOB_GOPROXY = where bob reads modules (mirror/internal proxy/file:// dir); bob.lock
  records it if http(s), else the default proxy (lock validation accepts only http(s)).
- D-M5b-4: bob lock --update <module> moves a root (declared or package-providing module) to
  the proxy's latest release; requirements move with it by MVS; other names are ignored by
  the Go section. `bob lock --update` without names (Update = []) resolves from the
  declarations without the previous pins. A plain lock keeps pins of roots (max of declared,
  selected, pinned).
- D-M5b-5: the root go.mod (bob.go module) is generated by golang/gen.RootGoMod through the
  native glue for every graph with a module line, also without Go nodes (cross-repo app):
  go = framework language version, requirements = framework at the IR's bob version +
  bob.Import modules. A go.mod without requirements (go mod init, placeholders) is adopted.
- D-M5b-6: a dependency's go.mod requirement must match the path's major version (x/mod
  MatchPathMajor incl. the gopkg.in .v1 pseudo-version exception), else BOB-E-1015.
- D-M5b-7: packages of a workspace module are read with every build tag set and unset except
  "ignore" and "bob" (cmd/go's tidy rule; bob.go files are build definitions); its tests count,
  dependencies' tests do not. Generated packages (no source at lock time): every package of
  their runtime modules counts, and imports those cannot resolve are ignored.
- D-M5b-8: Go modules of dependency bob repositories (labels not starting with "//") are not
  resolved by the consumer's lock in v1 (the cross-repo example has none).
- D-M5b-3: resolution caches every .mod/.zip in $XDG_CACHE_HOME/bob/goproxy (GOPROXY layout,
  the directory §03.2.5 gives bob env), so a second bob lock makes no registry request
  (R-FW-062).

## NEEDS
1. eval owner (pkg/engine/eval, ~60 lines, patch ready and verified:
   /var/tmp/i-m5b/patches/eval-imports.go → pkg/engine/eval/imports.go, plus
   /var/tmp/i-m5b/patches/eval-imports.patch = one line `addBobImports(&rootMod, own)` in
   compile()): the evaluator must treat bob.Import calls of the bob.go files as requirements of
   the root go.mod before compiling (the generated root go.mod only exists after the first
   evaluation). Without it R-FW-022 cannot evaluate a fresh consumer.
2. eval owner: export the framework module's go.sum lines (eval's fw.sumLines) so the root
   go.sum can be generated (E2 cannot import E3; the native glue in build could pass them). Until
   then the root go.mod requires github.com/megakuul/bob v0.0.0-dev without go.sum lines, and
   gopls/`go list` in the root module need M6 (bob env serving the framework in bob's goproxy
   dir = gen.ProxyDir()).
3. M6 / pkg/cli (bob env): GOPROXY=file://<gen.ProxyDir()> (bob lock fills it with every .mod,
   .zip, .info and list it read), GOFLAGS=-mod=readonly, GOSUMDB=off, GOTOOLCHAIN=local. On a
   fresh clone the directory must be filled from bob.lock (zips can be written from the store
   trees; .mod of packages:false modules need the proxy once) — a helper is not written yet.
   R-GO-002b and R-GO-003a acceptance tests need bob env.
4. I-M4f / diag owner: BOB-E-1008's template ("{label} needs Go {want} but nixpkgs {rev}
   provides Go {have}") cannot carry SPEC §03.2.4's text "github.com/foo/bar@v1.2.0 needs go
   1.27.0, but example.com/api uses go 1.26 (set Go in api/bob.go)" (TestRGO004a checks message
   + hint). bob puts the SPEC text in Cause. Needs a 1008 variant (codes.go + codes golden).
5. engine (R-GO-005): PlanStore extension `RealiseNow(ctx, map[drvPath]*drv.Derivation)
   (map[drvPath]map[output]hostPath, error)` (write the derivations, build them, return
   outputs) — gen checks for it with BOB_GO_CUTOFF=1. Go side still to do then: go-compile
   `-linkobj` split (bop; builder version bump), planner level by level (realise a package,
   add its export data by content, plan importers against that path, link against linkobjs).
6. I-M4f (R-FW-061 Go template): the go drift message of the SPEC ("✗ api/go.mod or go.sum was
   changed outside bob" + Go hint) is still the generic 1011 block (I-M4f NEEDS).

## TEST PROBLEM
1. tests/eco/golang/resolve_test.go TestRGO002bBuildListEqualsGoCommand, fixture-go entry
   (and R-GO-003a `go build ./...` in tools): example.com/tools imports example.com/lib (another
   workspace module). The go command can only find lib through a `replace` line or a go.work
   (SPEC: no replace), and then `go list -m all` prints "example.com/lib v0.0.0 => ../lib" or a
   second main module line ("example.com/lib" alone), which the test rejects ("unexpected
   line"); serving lib from bob's proxy as a version would make go.sum change with every lib
   edit and the module cache refuse the changed zip. Measured with go1.26.9 (go.work: lines
   "example.com/lib" and "example.com/tools"; replace without require: "module example.com/lib
   provides package … and is replaced but not required"). bob resolves the cross-module import
   itself (lib's declarations join tools' roots). Needs an owner/SPEC decision (go.work
   generated for modules that import other workspace modules, and the test accepting several
   main modules), or a fixture without the cross-module import (also TEST PROBLEM 1 of I-M5a).
