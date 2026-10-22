# I-M7b progress (M7.4 platforms and toolchains, M7.5 variants and projections, M7.6 budgets)

Owner of: pkg/bob platform files (platform.go, toolchain.go), engine toolchain code
(pkg/engine/toolchain.go, see D-M7b-1), pkg/engine/build platform code (platform.go + small
hooks), pkg/cc/gen cross parts, pkg/golang cross/variant parts (gen, bop toolchain args).
Not pkg/rust, pkg/npm.

## done (checkpoint 19:30)
- pkg/bob/toolchain.go: bob.Toolchain {Host, Target, Triple, Prefix, CC, Bintools, CCLib,
  Emulator, EmulatorCmd}, p.Toolchain(), bob.ToolchainFor (R-FW-033 C/C++ row), Platform
  methods NixSystem/GOARCH/Triple/CrossSet. Unit test toolchain_test.go.
- pkg/engine/toolchain.go: HostPlatform/HostSystem, NodePlatform, CToolchain, GoFor (Go row),
  RustFor (Rust row, hook for I-M8a), NixpkgsSource (R-FW-032/036 set selection), CrossRefused
  (OCaml native-only, R-PLAT-005), Versions/TestVersions (BOB_TEST_NIXPKGS_INDEX)/CheckVersions
  (R-PLAT-007). Unit test toolchain_test.go.
- pkg/engine/build: every bob derivation has the host system (systemOf, R-PLAT-004; 1010 gone);
  nixpkgs entries per (system, attr) with one index per system (platform.go indexes.check);
  pre-check before planning; uncached target-lib note (R-FW-036b); variant-dropped note
  (R-FW-038); cross refusals; foreign sanitised/race test units skipped with
  "not run: needs a native <p> machine" (R-FW-039), Options.RequireAllTests → exit 1;
  TestResult.Runner from the unit's argv (qemu-<arch>).
- pkg/cli/cmd_build.go: note events (json {"event":"note","type":"note",code,…}; human
  "note: …" on stderr), test.result runner/reason, SKIP line, --require-all-tests wired.
- pkg/cc: tools from p.Toolchain() (cross cc-wrapper + bintools, gcc-lib for sanitised cross),
  qemu-user dep for cross tests, LibraryOpts/BinaryOpts.Libs (cc.NativeLibs / cc.CrossLibs).
  pkg/nixpkgs: (*Package).CrossSet(p) (CrossLibs re-declares a Lib from pkgsCross).
- pkg/cc/gen: host system, prefixed gcc/g++/ar/strip/objcopy, names cc-<name>[-<platform>][-dbg]
  [-address][-undefined], test-run under qemu, R-FW-037 rpath, External cross (CC/CXX/AR/…,
  cmake system/compilers, autotools --host; Meson cross = 9008). Unit test cross_test.go.
- pkg/golang: go.binary/go.test axes platform+profile+sanitizers+race; cgo C compiler from the
  toolchain; qemu dep for cross tests. pkg/golang/gen: host system, GoTarget env (GOOS GOARCH CC
  CXX) + -extld in every cross job, variant.go (-N -l, -race, -asan, C sanitisers on cgo/link,
  name tags), cgo forced on for race/asan. pkg/golang/bop: Toolchain.Env/GCFlags/LDFlags/CFlags,
  go-std with -race/-asan/-gcflags, compiler shim for cross -asan. BuilderVersion 7 (shared with
  the OCaml unit's bump), builders.go + framework.zip regenerated.
- Verified: cc fixture `bob build --platform linux-amd64 //app:app` on arm64: x86-64 ELF,
  interpreter cross glibc; runs under qemu-x86_64: "gcd=12 isqrt=31 crc32=d7183e61
  sha256=fde5eea7a256ddf8 zstd=12->21" (External zstd via make cross, openssl/zlib from
  legacyPackages.x86_64-linux). Cold cross build 4m41s at load ~30 [8c/15G shared].

## next
- go fixture cross build; cc cross test under qemu; budgets/closures (M7.6).

## DECISIONS
- D-M7b-1: engine toolchain code lives in package engine (pkg/engine/toolchain.go), not a new
  pkg/engine/toolchain: TestLayering (pkg/layering_test.go, a test) rejects packages missing
  from its layer table; package engine is E2 already.
- D-M7b-2: bob.Toolchain.CC is the nixpkgs attribute (host set) of the compiler wrapper, not a
  drv.Ref (SPEC §04.2): the evaluator computes no derivations; ecosystems take it as
  nixpkgs.Tool(p, tc.CC), so all packages of one kind and target share one derivation.
- D-M7b-3: C/C++ derivation names follow §03.1 (G14): cc-<name>[-<platform if cross>][-tag];
  tags dbg/address/undefined. Go: go-<ip>[-<platform>][-tag], go-std-<v>-linux-<arch>[-cgo][-tag],
  go-link-<name>[...], tags dbg/address/undefined/race.
- D-M7b-4: target nixpkgs libraries for a cross target are looked up in an index opened for the
  target system (legacyPackages.<sys>.<attr>); Cross() and cc.CrossLibs use
  pkgsCross.<set>.<attr> of the host index.
- D-M7b-5: cc.CrossLibs is applied at evaluation: the cc constructor replaces each nixpkgs.Lib
  dep by the same Lib from the cross set ((*nixpkgs.Package).CrossSet); natively it changes
  nothing.
- D-M7b-6: R-PLAT-007 needs no code for its messages in §02.3; uses BOB-E-1021 (label
  "nixpkgs", reason = SPEC text, hint in Cause); OCaml cross refusal uses 1010 with the SPEC
  text in Cause (exit 3, SPEC says 2). See NEEDS 1.
- D-M7b-7: race and asan force cgo on (cmd/go: both require cgo); CGoOff + race/asan →
  BOB-E-1020; race+asan together → 1020.
- D-M7b-8: cross `go list -asan` (std) runs with a CC shim script that strips the triple prefix
  from `--version` (cmd/go compilerVersion requires output starting with "gcc").
- D-M7b-9: cross sanitised/race test units are not built at all (skipped, R-FW-039);
  others run as `<qemu-user>/bin/qemu-<arch> <binary> …` inside the test-run job.

## NEEDS
1. coordinator/SPEC §02.3 (+ codes.lock, owner-approved tests/goldens/diag/codes.golden): codes
   for "nixpkgs mismatch: {what}" (R-PLAT-007, two hints) and "{label}: {eco} packages cannot be
   cross-compiled yet (requested {platform} on a {host} host)" (usage, exit 2, R-PLAT-005).
2. SPEC §02.4 + schema/events-v1.json (protected): a "note" event (type, code, message, attr,
   platform, label, field), test.result status "skipped" and fields "runner"/"reason".
3. I-M8a (Rust): use engine.RustFor(host, target) / p.Toolchain() for --target, sysroot
   (rust-std-<triple> from legacyPackages.<target>.rustc.unwrapped), linker <triple>-cc, host
   units with RustFor(host, host); qemu-user dep for cross rust tests (p.Toolchain().Emulator).
4. OCaml unit: nothing (engine refuses cross OCaml nodes before planning).

## TEST PROBLEM
1. tests/platforms/platform_test.go restrictedAppGo calls `cc.Binary(p, …)` returning
   *cc.Binary: cannot compile (D-M7a-1, cc.NewBinary); R-PLAT-006 test cannot pass as written.
2. tests/platforms TestRPLAT004 checks every .drv in the store for the host system, including
   nixpkgs derivations of target libraries (legacyPackages.x86_64-linux.openssl has system
   x86_64-linux by definition, and R-FW-032's test requires exactly that). Ruling G11 fixed
   this for R-FW-030 only; R-PLAT-004 should check bob-builder derivations (or exclude
   nixpkgs target libraries).
3. tests/core TestRFW038DerivationNames wants `libs-foo-foo-<platform>-address` and
   TestRFW033 looks up `libs-mathx-mathx`/`libs-pack-pack`; ruling G14 makes §03 names win for
   ecosystem packages (cc-<name>…, which tests/eco/cc requires). Both cannot hold.
