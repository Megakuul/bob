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

## next
- corpus check by hand (/var/tmp/i-m5b/scripts/allcorpus.sh), cross-repo with the eval patch in
  the private copy /var/tmp/i-m5b/tree.

## DECISIONS
- D-M5b-1: go.mod roots rule: every build-list module that some workspace module declares or
  that provides packages to some workspace (bob.lock `packages`) is a requirement, at its
  selected version; Resolve iterates to that fixpoint, so Native derives go.mod from bob.lock
  alone and the go command selects exactly the locked build list.
- D-M5b-2: BOB_GOPROXY = where bob reads modules (mirror/internal proxy/file:// dir); bob.lock
  records it if http(s), else the default proxy (lock validation accepts only http(s)).
- D-M5b-3: resolution caches every .mod/.zip in $XDG_CACHE_HOME/bob/goproxy (GOPROXY layout,
  the directory §03.2.5 gives bob env), so a second bob lock makes no registry request
  (R-FW-062).

## NEEDS

## TEST PROBLEM
