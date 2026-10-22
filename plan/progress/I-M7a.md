# I-M7a progress (M7.1 pkg/cc, M7.2 compile_commands.json, M7.3 cc.External)

Owner of: pkg/cc, pkg/cc/gen, pkg/cc/bop, pkg/cc/internal/ccspec. Outside (small, reported):
pkg/cli/link.go (one blank import), cmd/bob-builder/main.go (merges ccbop.Kinds()),
pkg/builder/testrun.go + bspec (test-run format "tap", BuilderVersion 4 → 5; other units
also change pkg/builder paths.go/unpack.go under the same bump),
pkg/builder/testrun_tap_test.go (new), pkg/engine/store/builders.go and
pkg/engine/eval/framework.zip (regenerated).

## done (checkpoint 18:45)
- pkg/cc API: Linkable, Std (C11..CXX23), LibraryOpts, StaticLibrary, SharedLibrary, Library,
  BinaryOpts, NewBinary/Binary, NewTest/Test (see TEST PROBLEM 1), BuildSystem (CMake,
  Autotools, Meson, Make), ExternalOpts, External, ExternalLib. Kinds cc.library, cc.binary,
  cc.test (axes platform, profile, sanitizers), cc.external (platform, profile). Every target
  depends on nixpkgs.Tool gcc, binutils, patchelf. Evaluation-time checks (R-CC-002a): -o,
  -I/-isystem/--sysroot/-L with absolute paths, anything naming /nix/store, -fplugin, -Xclang,
  @file; Std values; Defines names; empty Srcs; LinkOpts on a static library; External URL,
  Strip, Build, Libs, Include.
- pkg/cc/bop: kinds cc-compile-link (pkg-config entries resolved in the builder from the deps'
  .pc files incl. nix-support/propagated-build-inputs, fallback -I/-L/-l; compiles in parallel,
  then ar / link; public headers copied to $out/include; gcc "file:line:col: error:" lines →
  structured diagnostics) and cc-external (copy source to /build/src; make / cmake+ninja /
  meson+ninja / autotools with bash as SHELL/CONFIG_SHELL). Both use the fixup (strip in
  release, shrink-rpath, forbidden: cc-wrapper, binutils, patchelf, the sources, bob-builder).
- pkg/cc/gen: Ecosystem "cc" + LockFreshness. Plan: one cc-compile-link per library, binary,
  test (drv name cc-<name>), test-run ccrun-<name> (format tap), ccx-<name> per External with
  its source ccx-src-<name>-<narhash8> as a PlanResult fetch; flags in the §03.1.3 order;
  link order = deps before what they need, static libs pull their deps, shared libs their
  public deps; LinkInfo filled for other ecosystems (Go cgo reads it: the cc.Lib dependency
  of pkg/golang cgo needs no change in pkg/golang). Lock: cc section {inputs, externals}
  (Resolve via the fetcher, keeps pins whose URL/Strip did not change unless --update names
  the label or name). Stale: compares inputs only (a missing entry with unchanged
  declarations is BOB-E-1009 at plan, R-CC-005a(a)). Native: compile_commands.json.
- Unit tests: pkg/cc (TestFlagProblem), pkg/cc/bop (pkg-config, fallback, splitArgs,
  requires, external steps, parallel), pkg/cc/gen (TestGenJobsDecode: strict decode into
  bop/bspec types + flag and link order, TestExternalNotPinned, TestNoSources,
  TestCompileCommands), pkg/builder TestTapCases.
- End-to-end in a private copy (/var/tmp/i-m7a/{sync.sh,rebuild.sh,repo}; patches listed in
  sync.sh): cc fixture (renamed constructors, loopback http allowed in the copy) and cpp-lib
  fixture: bob lock (cc section pinned), bob build //... (4 built), bob test //... (TAP case
  parsed: "ring keeps last 3"), app runs under env -i (inside a namespace mapping the bob
  store to /nix): "gcd=12 isqrt=31 crc32=d7183e61 sha256=fde5eea7a256ddf8 zstd=12->12".
  R-CC-005a (a) entry removed → exit 3 BOB-E-1009 naming the label, no package started;
  (b) tampered archive, fresh store → exit 4 BOB-E-3001.

- Golden candidates (owner approves): out/golden-candidates/tests/goldens/eco/cc/jobs/
  {mathx,pack,app,mathx_test}.json and tests/goldens/eco/cc/compile_commands.json (+ .meta),
  produced from the private-copy run (store paths include the bob-builder path, so the job
  goldens change with every builder change; compile_commands.json has $REPO for the dir).
- Shared tree: go test ./pkg/cc/... ./pkg/builder/... ./pkg/engine/store ./pkg (layering) and
  pkg/engine/eval TestFrameworkZip green; gofmt/vet/staticcheck clean for pkg/cc; ci stage
  builder green after go generate ./pkg/engine/store. Repo-wide fmt/vet/lint fail only on
  other units' WIP (pkg/golang/resolve, pkg/cli/dev_env.go, pkg/rust, tests/serverenv).
- Acceptance tests tests/eco/cc (private copy): all red on the 5-minute harness timeout of the
  first `bob lock` in a fresh bob home (cold lock measured 5m31s at load 20 on 8 cores, 64 s
  earlier on a quieter host); the steps they check were verified by hand (above).
  B-053 (C/C++ no-op build): 0.51-1.06 s, median 0.59 s [8c/15G, load ~20, shared host].

## next
- nothing in this chain beyond NEEDS / TEST PROBLEM; M7.4 (cross) unblocks R-CC-001a amd64.

## DECISIONS
- D-M7a-1: constructors NewBinary/NewTest (types Binary/Test), like npm.NewApp/NewTest: Go
  cannot have a function and a type both named Binary (SPEC §03.1.1). TEST PROBLEM 1.
- D-M7a-2: ExternalOpts.Tools is []bob.Tool (SPEC: []*nixpkgs.Package, but nixpkgs.Tool
  returns *nixpkgs.ToolPackage; the SPEC's own example would not compile).
- D-M7a-3: compiles run in the source path with repo-relative file names and
  -ffile-prefix-map=<src>=. (SPEC "=<label dir>" would double the label dir, since the source
  tree keeps repo-relative paths); own includes -I<label dir>/<Hdrs>, and -I<label dir> when
  Private is set. compile_commands.json uses the same flags without the prefix map.
- D-M7a-4: nixpkgs libraries (and Externals without Libs) are linked through their pkg-config
  files, read by bob-builder (argv placeholders "@cflags:<i>", "@libs:<i>" naming
  args.pkgConfig[i]); bob cannot read .pc files at plan time (outputs not realised). Every -L
  store dir also gets -Wl,-rpath (programs run under env -i; fixup shrinks unused entries).
  For other ecosystems a bob library's LinkInfo carries the nixpkgs deps' lib dirs and
  PkgConfig dirs but no -l names (static C libraries with nixpkgs deps linked into cgo need
  them; follow-up when a fixture needs it).
- D-M7a-5: the test-run derivation is ccrun-<name> (SPEC: cc-<name>-run): the R-CC-002a test
  matches every drv named cc-<pkg>[-…] to one golden, so cc-mathx_test-run would collide with
  the test's compile job cc-mathx_test.
- D-M7a-6: test-run format "tap" in bob-builder: TAP when the log starts with "TAP version",
  else one case named after the label's name; SKIP → skip, TODO failures pass, "# ..." lines
  after a failure are its output.
- D-M7a-7: the External source store name is ccx-src-<name>-<first 8 hex of narHash>; bob lock
  resolves it as ccx-src-<name> (narHash unknown yet), so the first build downloads once more.
- D-M7a-8: External builds get bash, coreutils, findutils, diffutils, gnused, gnugrep, gawk,
  pkg-config plus the build system's tools (cmake+ninja, meson+ninja, gnumake+autoconf+
  automake+libtool+m4+perl, gnumake); make runs as `make -jN SHELL=<bash> PREFIX=$out install
  <Args>`; cmake gets -DCMAKE_INSTALL_LIBDIR=lib (no lib64).
- D-M7a-9: C/C++ for a platform other than the host is BOB-E-9008 not implemented (task M7.4:
  cross toolchains, CrossLibs).
- D-M7a-10: compile_commands.json leaves out Externals' include dirs (their output path is only
  known by planning, Native has no Builder/Inputs) and cgo packages' C files (follow-up).

## NEEDS
1. I-M4f (pkg/engine/build/native.go): pass the repository root to Native
   (`engine.PlanRequest{..., Root: root}` in nativeFiles; both callers have it). Without it
   compile_commands.json is not written (Native returns nothing when Root is ""). The private
   copy patches it (sync.sh).
2. drv owner (pkg/engine/drv Parse): decode the `__json` environment variable into
   Derivation.Structured (and drop it from Env), so a parsed structured-attrs derivation
   round-trips. tests/eco/cc jobSpec reads Structured["bobSpec"] / Env["bobSpec"]; with Parse
   as it is, every R-CC-002a job golden fails with "bobSpec is not JSON".
3. pkg/bob owner (ext.Errorf / evaluation.misuse): a definition error whose message starts with
   "<file>:<line>: " should carry that position (and the following "<label>: " as label), so
   ir.evalDiag strips it; today the error reads "bob.go: invalid build definition:
   libs/mathx/bob.go:12: //libs/mathx:mathx: flag …".
4. CLI/translate owner: R-CC-002a (and R-PLAT-006) expect the human line
   "✗ //libs/mathx:mathx: flag "-I/nix/store/x" is not allowed; use Deps to add include
   paths"; the CLI prints "error[BOB-E-1020]: …" blocks.
5. fetch/translate owner: R-CC-005a(b) reads message + label + excerpt of BOB-E-3001 and wants
   the URL, the pinned and the actual hash there; they are only in `cause`/args today.
6. M6 (dev env): .bob/bin/clangd shim (R-CC-003a runs it).
7. M7.4: cross toolchains for C/C++ (R-CC-001a's linux-amd64 half on arm64).

## TEST PROBLEM
1. tests/fixtures/repos/{cc/app, cc/libs/mathx, cpp-lib, rust/cmain, polyglot/libs/foo}/bob.go
   call cc.Binary(p, …) returning *cc.Binary and cc.Test(p, …) returning *cc.Test: Go does not
   allow a function and a type with the same name in one package, so these fixtures cannot
   compile with any pkg/cc (SPEC §03.1.1 has the same conflict). Implemented NewBinary/NewTest
   (D-M7a-1); fix: rename the calls in the five fixtures (and SPEC §03.1.1) to cc.NewBinary /
   cc.NewTest. The private copy applies exactly that rename.
2. tests/eco/cc helpers serve the zstd archive with httptest.NewServer (plain http) and run
   `bob lock`: the fetcher refuses plain http without a pinned hash (SPEC R-NIX-014, BOB-E-3002
   "uses plain http and the request has no pinned hash"). Fix options: the test serves https
   (httptest.NewTLSServer + a trust env the fetcher honours) or SPEC allows plain http to
   loopback addresses when the policy allows private addresses. The private copy allows
   http://127.0.0.1 to test the rest.
