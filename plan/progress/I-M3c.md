# I-M3c progress (M3.6 engine/lock, then M3.7 platform/variant model, then M4.3 nixpkgs + nixidx)

Owner of: pkg/engine/lock, pkg/bob/platform.go (+ related platform code in pkg/bob), pkg/nixpkgs,
pkg/engine/nixidx.

## done
- M3.6 pkg/engine/lock: lock.go (File, Parse/Read with BOB-E-1019 + line, conflict-marker
  detection, strict top-level scan with duplicate/unknown member rejection, Encode canonical,
  Write/WriteBytes atomic + only-on-change, Stale, SalvageNixpkgsRev for --rebuild), sections.go
  (§03.7 types GoSection…CCSection, ToolLock, NixpkgsFacts; DecodeSection/EncodeSection with
  grammar checks), validate.go (SRI/h1/semver/module path/npm/opam/crate grammars, bounds),
  canon.go (canonical JSON, no floats, Inputs = sha256 hex of canonical declarations),
  nixpkgs.go (ParseNixpkgs, ChannelRev via channels.nixos.org, PinNixpkgs, NixpkgsRequest
  name "source" strip 1). Own tests lock_test.go green.
  - Green: tests/core TestRFW004FileWriteIsAtomicAndStable, TestRFW065SectionRoundTrip (all but
    the cc seed, see TEST PROBLEM 1), FuzzLockSection (60 s local fuzz, 2.0M execs, no failure).
  - Red, need `bob lock`/`bob build` (pkg/cli, M4.6): TestRFW004LockTwiceIdentical,
    TestRFW004ReformattedLockIsRewritten, TestRFW004SchemaValidatesLocks (fixture part passes,
    then red at bob lock), TestRFW005*, TestRFW048UnparseableLock, TestRNIX005NixpkgsPin.
- M3.7 platform model: the configured-label / variant model was already built by I-M3a
  (TestRFW050ParseConfigured green). Added the per-kind platform restriction: ext.Restrict(n,
  platforms) (pkg/bob/ext), graph.Restrict hook, node.allowed, evaluation check
  checkPlatforms → BOB-E-0006 `<label> does not support <p> (it allows: <list>)` with
  definedAt and the shortest dependency chain (new optional `chain` field in the evaluator's
  `bob::` diag line). Unit tests pkg/bob/platform_test.go.
  - R-FW-030 tests need `bob build` and cc (TestRFW030PlatformRestriction needs pkg/cc calling
    ext.Restrict; TestRFW030CrossBuildKeepsHostSystem needs the cross build, M7.4).

- M4.3 pkg/nixpkgs + pkg/engine/nixidx:
  - pkg/nixpkgs: Tool (→ *ToolPackage: bob.Tool + Tree, no LinkInfo, host set, kind
    nixpkgs.tool Host), Lib/Pkg (→ *Package: Tool, Tree, Linkable; kinds nixpkgs.lib /
    nixpkgs.pkg, target set or "cross" with Cross()), options Outputs/Modules/Cross/
    AllowInsecure/AllowUnfree → IR "nix" object; Example tests (TestAPISurface no longer lists
    nixpkgs).
  - pkg/engine/nixidx: resolve.nix (54 lines, data only, `nix eval --json --file … --argstr …
    result`), Open/Lookup (batch eval, names list fetched with the first batch, failing batch
    bisected until the aborting attribute is isolated → 1017 with the abort message as Cause and
    Nix's text in Raw), cache `<home>/cache/nixpkgs/<rev>-<system>.json` keyed by (version, rev,
    system, allow settings), re-query when a cached drvPath is invalid (store.Invalid once per
    path per Index), Names, Modulo/RecordModulo (cached modulo hashes), Cached (HEAD
    <sub>/<hash>.narinfo per substituter from the store's nix.conf + netrc creds, remembered per
    run). precheck.go: Index.Check(needs) → 1001 (≤3 suggestions: ≤2 edits or shared prefix),
    1002, 1003, 1004, 1005 with label + definedAt; Suggest. Unit tests precheck_test.go (incl.
    Check against tests/nix/testdata/fakepkgs).
  - Green: TestRNIX027ResolveNixShape, TestRNIX027CachedAsksSubstituters; TestRNIX027IndexLookup
    passes every check except the raw-text leak check of 1017 (TEST PROBLEM 2).
  - Red, need `bob build`/`bob dev` (CLI, M4.6): TestRNIX027IndexBudgets, R-NIX-011 fault
    catalogue rows, TestRNIX002NixpkgsStaysUsable, TestRNIX013OnlyNixpkgsLeaks (its API grep
    part passes; red at `bob dev`).
  - Measured [8c/15G, aarch64, real nixpkgs cd4ea8d5, 15 attrs incl. protobuf, openssl, go,
    rustc, nodejs, gcc]: cold 2.33 s on a store that had never seen those drvs, 1.12 s when the
    drvs already existed; warm (Open + Lookup) 13–14 ms. Budget B-078 (polyglot): cold ≤ 2 s,
    warm ≤ 50 ms. Pre-check data on real nixpkgs: opensll missing → suggestions openslp,
    openssl, openal; aranym broken; bzip2_1_1 insecure; 27,258 top-level names.
  - Also changed: pkg/engine/ir Nix gets `modules` (omitempty) + ir.schema.json property.

## next
- nothing in this chain; waiting for review/commit.

## files
- pkg/engine/lock/{lock.go,sections.go,validate.go,canon.go,nixpkgs.go,lock_test.go}
- pkg/nixpkgs/{nixpkgs.go,example_test.go}; pkg/engine/nixidx/{nixidx.go,precheck.go,resolve.nix,
  precheck_test.go}; pkg/engine/ir/{ir.go,ir.schema.json} (Nix.Modules)
- pkg/bob/{eval.go (chain, checkPlatforms), node.go (allowed, restrict), platform_test.go},
  pkg/bob/ext/ext.go (Restrict), pkg/bob/internal/graph/graph.go (Restrict hook)

## DECISIONS
- D-M3c-1 `sources` has no `inputs` member (an `inputs` string inside the id→entry map would
  break the map's schema); each entry is its own declaration (url, kind, strip, name), so
  freshness is decided per entry. `nixpkgs.inputs` = sha256 hex of RepoOpts.Nixpkgs (optional).
- D-M3c-2 Section `inputs` is 64 lowercase hex (R-FW-004 statement, test seeds); the
  `sha256-…` in §03 examples is not accepted. `lock.Inputs(decl)` computes it.
- D-M3c-3 Empty `proxy` (go), `index` (rust), `registry` (npm) mean the default (the R-FW-004
  unit test writes a go section with only modules+inputs and reads it back).
- D-M3c-4 `nixpkgsFacts` is kept in File.Sections (the SPEC File type has no field for it) and
  validated as system → attr → {version}.
- D-M3c-5 BOB-E-1019 carries DefinedAt bob.lock:<line>; reason for conflict markers is
  `merge conflict marker <<<<<<<`; JSON errors use the decoder's text and the line of the bad
  byte. Unknown top-level members, duplicates, floats, trailing data, lockVersion != 1 are 1019.
- D-M3c-6 PinNixpkgs keeps an existing pin of the same channel unless --update; an existing
  narHash for the same rev+url is reused without download. nixpkgs store name "source" (as the
  tests/nix harness uses).
- D-M3c-7 Platform restriction is ext.Restrict (an explicit call by the kind constructor with
  its opts.Platforms), not reflection over option structs. The check runs on nodes reachable
  from the requested targets after evaluation; a restricted node's dependencies are not
  checked further (they are not built for it). Unknown platform names in the list are a
  definition error of the constructor.

- D-M3c-8 R-FW-032 vs §1.7: SPEC §1.7 says Tool returns *Package implementing Linkable, but
  R-FW-032 (and tests/core typecheck/nixtool) require a Tool not to be a cc.Linkable. Tool
  returns *nixpkgs.ToolPackage (no LinkInfo). "A Lib is not a bob.Tool" cannot hold while
  bob.Node carries the tool() marker (D-M3a-4); left for M7.4 (no test asserts it).
- D-M3c-9 The Modules option needs a place in the IR: `nix.modules` (optional string array) was
  added to ir.Nix and ir.schema.json; SPEC §1.6 row `nodes[].nix` should list it.
- D-M3c-10 resolve.nix takes every argument as a string (--argstr; lists/booleans as JSON,
  decoded with builtins.fromJSON) so attribute names never become Nix syntax; it returns its
  data under the attribute `result` because `nix eval --file` auto-calls the file's function
  only when an attribute path is given (measured with Nix 2.34.8: without one, "cannot convert
  a function to JSON").
- D-M3c-11 Index entries depend on AllowUnfree/PermittedInsecure (nixpkgs refuses drvPath
  otherwise), so the cache records a hash of them and drops entries when it changes; failed
  evaluations (1017) are not cached. resolve.nix's evalRefused is a boolean; Entry.EvalRefused
  becomes the reason (broken/insecure/unfree/unavailable/refused by nixpkgs).
- D-M3c-12 Cached: substituters and netrc credentials are read from the nix.conf/netrc the
  store writes (store has no accessor); an unreachable substituter counts as "not cached" and is
  not remembered; stops at the first hit.
- D-M3c-14 A cached entry is revalidated by an lstat of <home>/nix/store/<drv> instead of
  store.Invalid: the nix-store process made a warm lookup ~190 ms (budget 50 ms); the stat makes
  it ~13 ms.
- D-M3c-13 Config.PermittedInsecure is passed to nixpkgs as permittedInsecurePackages (package
  names); per-package AllowInsecure (knownVulnerabilities entries) is checked in Go by Check.

## NEEDS
- store (I-M2b, optional): `Substituters()` accessor instead of nixidx parsing nix.conf.
- CLI / engine: link pkg/nixpkgs into bob (kinds nixpkgs.tool/lib/pkg must be known to ir.Parse),
  build Needs from IR nodes with `nix` and call Index.Check before planning (R-NIX-011), emit the
  `index` stage event.
- pkg/bob/diag (coordinator): ruling G10 says row 1019's hint becomes `resolve the merge
  conflict, or run bob lock --rebuild to resolve everything again from bob.go`; codes.go still
  has `resolve the merge conflict, or run bob lock` and the protected golden
  tests/goldens/diag/codes.golden has the old text too, so changing codes.go alone makes
  TestCodeTableGolden red. Needs codes.go + an owner-approved golden update together.
  TestRFW048UnparseableLock asserts the new hint.
- pkg/engine/eval (M3.4) / CLI: map the new `chain` field of `bob::` diag lines into the
  error event and render `//app:app → //libs/foo:foo` in human output (R-PLAT-006).
- Kind packages with `Platforms []bob.Platform` options (pkg/cc, others): wrap their ext.New
  result in `ext.Restrict(node, opts.Platforms)`.
- CLI `bob lock` (M4.6): lock.Read (missing → fs.ErrNotExist), lock.PinNixpkgs, File.Stale per
  section, File.Encode + compare with the bytes on disk for `note: bob.lock was reformatted`,
  `--rebuild` → lock.SalvageNixpkgsRev on the raw bytes.

## TEST PROBLEM
2. tests/nix/nixidx_test.go TestRNIX027IndexLookup runs leakcheck over diagOf(err), which
   (tests/nix/fetch_test.go:102) concatenates de.Raw. SPEC R-NIX-027 says the 1017 error keeps
   "the raw text under -v" and R-NIX-010 says raw Nix text appears with -v "in nix: lines (or in
   raw[])", i.e. Raw is exactly where Nix text belongs. The implementation puts Nix's message in
   Raw, so the check fails with "raw error line"/"while evaluating" findings; everything else in
   the test passes. Fix: leak-check de.Error()+Cause+Args only (not Raw) in that test.
1. tests/core/lock_test.go sectionSeeds["cc"] is not valid JSON: `{"externals":{"//third_party/
   zstd:zstd":{…}}},"inputs":"000…"}` closes the top-level object after "externals" (three `}`)
   and then has `,"inputs":…}` (Python json: "Extra data: line 1 column 202"). The SPEC §03.7.6
   example has the same extra brace. TestRFW065SectionRoundTrip/cc cannot pass for any decoder
   (the test's own compactJSON of the seed fails). Fix: `…zstd-1.5.7.tar.gz"}},"inputs":…`
   (two braces). The invalid/cc/* subtests pass only because the seed is broken.
