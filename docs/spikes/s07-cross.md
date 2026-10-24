# Spike S7 — Cross-compilation and build variants

Status: **done (indicative numbers)**, 2026-10-21, host `vibe-engine` (aarch64, busy with other
spikes). Code: `spikes/s07-cross/` (throwaway). ADR: `docs/adr/0009-platforms-and-variants.md`.

## 1. Question and requirements

Can bob cross-compile amd64↔arm64 for C, C++, Go (with and without cgo) and Rust (incl.
proc-macros and build scripts) with nixpkgs toolchains at the pin, and how do variants
(debug/release, Rust features, ASan/UBSan, Go `-race`/`-asan`) enter derivations and cache keys?
Requirements: R-PLAT-001/002, R-FW-030, R-FW-031, R-CC-002, R-GO-001, R-RS-001, R-RS-004,
R-NFR-001/006/008; D3, D4, D28.

Only aarch64→x86_64 can be tested here (no x86_64 host, no KVM, no binfmt). x86_64 outputs are
run with **qemu-user from nixpkgs, called directly as `qemu-x86_64 <binary>`, without root**.

## 2. What was built

`spikes/s07-cross/` = a copy of S2 (`spikes/s02-go`, backend B + per-package Go) extended with:

| File | What |
|---|---|
| `gen/target.go` | `Platform`, `Variant`, per-kind **projection** (`Proj`), toolchain selection (`CCAttr`, `CCPrefix`, `LibAttr`), name suffixes, C/Go flag mapping |
| `gen/ccrust.go` | C/C++ with cross prefix and variant flags; Rust per-crate with `--target`/`--sysroot`/`-C linker`, a separate **host-unit generator** (proc-macros, build-script compile + deps), per-target build-script **run**, `rust-std-<triple>` derivation |
| `gen/gopkg.go` | std per (target, cgo, variant); `GOARCH`/`CC` in every package derivation; `-N -l`, `-race`, `-asan`; `-extld=<prefix>gcc` |
| `cmd/bob-s07` | flags `-platform -profile -sanitize -race -features -libs`; `list` prints every derivation of a configuration |
| `fixture/rmac`, `fixture/rdemo` | new: proc-macro crate (uses `itoa` at expansion time) and a bin with `build.rs`, a feature `shout` and a target-dependent cfg |
| `matrix.sh` | derivation identity across 11 platform×variant configurations (no build) |
| `measure.sh` | cold substitution of the cross toolchains; cold builds per configuration, [8c/15G] and [2c/4G] |

## 3. Results

### 3.1 Toolchains at the pin (`nixos-26.05@cd4ea8d5`), cache.nixos.org

Checked with `nix path-info --store https://cache.nixos.org` and `nix build --dry-run`.

| Need (aarch64 host → x86_64 target) | nixpkgs attribute | Cached? | Cold cost [8c/15G] |
|---|---|---|---|
| C/C++ cross compiler | `pkgsCross.gnu64.stdenv.cc` (gcc 15.3.0, glibc 2.42) | **yes** | 113.8 MiB download; 6.4 s substitution into an empty store |
| qemu-user (test runner) | `qemu-user` 10.2.4 | yes | 35.2 MiB; 0.9 s |
| openssl for x86_64, native set | `legacyPackages.x86_64-linux.openssl` | **yes** | 14.8 MiB; 0.7 s |
| openssl for x86_64, cross set | `pkgsCross.gnu64.openssl` | **no** | **369 s** build from source (cores=4, busy host) |
| Go cross | host `go` 1.26.8 + `GOARCH=amd64` | yes (host go) | std build per variant, see 3.4 |
| Go via cross set | `pkgsCross.gnu64.buildPackages.go` | no | not needed |
| Rust std for x86_64 | `legacyPackages.x86_64-linux.rustc.unwrapped` (contains x86_64 std) | **yes** | 455.8 MiB closure; 6.5 s (fast network); the copied std is 121 MB |
| Rust via cross set | `pkgsCross.gnu64.buildPackages.rustc` | **no** | would build rustc+LLVM from source (hours; not tried) |
| Rust std from upstream | `static.rust-lang.org/.../rust-std-1.95.0-x86_64…tar.xz` | n/a | **unusable**: `E0514 found crate std compiled by an incompatible version of rustc` (nixpkgs rustc reports `(built from a source tarball)`) |

Reverse direction (x86_64 host → aarch64), checked by evaluation only:
`legacyPackages.x86_64-linux.pkgsCross.aarch64-multiplatform.stdenv.cc` **and** `.openssl` are
both cached. Building there is untested.

Whole cold set for this fixture on a fresh store: about 620 MiB download, 2.0 GiB unpacked,
≈15 s on this host's network (`measure.sh subst`).

### 3.2 Cross builds verified under qemu-user

All outputs below are x86-64 ELF (`file`), built on aarch64, run with
`env -i qemu-x86_64 <bin>`.

| Build | Result |
|---|---|
| C with x86_64 openssl (hand test, cross cc-wrapper) | `hello 2d` (= sha256("x")[0]) |
| `//api:api@linux-amd64` (cgo + C++ libfoo with openssl + Rust staticlib rcore + protobuf), target libs **native** | `digest=81b637d8…ce9 numhex=3432 proto_bytes=116` ✔ |
| same, target libs **cross** (`pkgsCross.gnu64.openssl`) | same output ✔ |
| `//api:api@linux-amd64+debug` | same output ✔ |
| `//rdemo:rdemo@linux-amd64` (proc-macro, build script, itoa) | `rdemo target=x86_64-unknown-linux-gnu answer=42 bits=64 x86cfg=true` ✔ |
| `…+feature shout` / `…+debug` | upper-cased line / `… debug` suffix ✔ |
| `//api:api@linux-amd64+asan`, `+asan+ubsan`, `+race` | **build and link**; cannot run under qemu-user: `AddressSanitizer/SanitizerTool: CHECK failed: sanitizer_allocator_primary32.h:292` |
| Native arm64 api: release, debug, asan, asan+ubsan, race | all print the expected line ✔; `libasan.so.8`/`libubsan.so.1` in NEEDED, `__tsan_init` present for race |

Notes from the builds:

- The nixpkgs **cross cc-wrapper works without stdenv**, like the native one (gen-b §2): it adds
  RUNPATHs for `-L` dirs, the cross glibc and the cross gcc-lib, and sets the interpreter to the
  cross glibc's `ld-linux-x86-64.so.2`. qemu-user finds it in `/nix/store` directly.
- **Native target libs + cross toolchain = two glibcs** in the closure
  (`glibc-x86_64-unknown-linux-gnu-2.42-100` from the cross cc, `glibc-2.42-100` from the x86_64
  openssl). Same version, it runs, the interpreter wins. Closure: 113.8 MB (native libs) vs
  76.1 MB (cross libs).
- **cgo needs `CC` in the package derivation**: the `cgo` tool runs `$CC` itself (default `gcc`
  is not on PATH: `cgo: C compiler "gcc" not found`).
- **cmd/go's `-asan`/`-race` check rejects a prefixed cross gcc**: `go list -asan` fails with
  `-asan: C compiler is not gcc or clang`, because `x86_64-unknown-linux-gnu-gcc --version` does
  not start with `gcc` (cmd/go `work/init.go compilerVersion`). The spike uses a 3-line `gcc` shim
  in the std derivation. A bob that compiles std package-by-package (like workspace packages)
  avoids cmd/go entirely.
- **nixpkgs packaging gap**: the cross gcc-lib's `libasan.so.8` NEEDs `libstdc++.so.6` but its
  RUNPATH lists only glibc (the native one also lists gcc-lib). Cross ASan binaries fail with
  `libstdc++.so.6: cannot open shared object file` until bob adds the cross gcc-lib dir to the
  RUNPATH of sanitised binaries (or links `-static-libasan`). With `LD_LIBRARY_PATH` set, the
  binary reaches the qemu ASan CHECK failure above. Fix not implemented.
- **Untyped nixpkgs deps are ambiguous.** The IR lists `go`, `protobuf`, `protoc-gen-go` and
  `openssl` the same way. For cross, tools must come from the host set and libraries from the
  target set. The spike treats only `cc_library` nixpkgs deps as target libraries. The SPEC
  must type them (§5.3).

### 3.3 Derivation identity across configurations (`matrix.sh`, whole fixture graph)

28 derivations natively (10 of them downloads/unpacks), 29 for amd64 (+`rust-std`).
"New" = derivations not present in `arm64-release`.

| Configuration | new | What is new |
|---|---|---|
| arm64 debug | 13 | go-std + 7 Go groups, libfoo, all 4 Rust target crates, rdemo build-run |
| arm64 asan | 8 | go-std + 7 Go groups (`-asan`), libfoo — **Rust crates unchanged** |
| arm64 asan+ubsan | 8 | vs asan only **2**: libfoo and the cgo/link package (pure Go sees only `address`) |
| arm64 race | 7 | go-std + Go groups only (C, C++, Rust unchanged) |
| arm64 feature `//rdemo:rdemo=shout` | 2 | rdemo build-run and rdemo compile only |
| amd64 release | 14 | everything target-side; **shared with arm64**: downloads, `proto-greeting_go` (codegen), `go-tools` (pack), `itoa-host`, `rmac-host`, `rdemo-build-script` |
| amd64 cross libs vs native libs | 2 | libfoo and the cgo/link package |
| amd64 asan vs amd64 release | 8 | Go + libfoo, Rust target crates shared |
| amd64 feature `shout` | 2 vs amd64 release | rdemo only |

Over the 11 configurations: **99 distinct derivations instead of 313** (sum of per-config
counts). No two configurations produced the same derivation path for a package whose
projection differs (every row's "new" set matches the projection rules), so variants cannot
collide in the cache: the path is a hash of the inputs, and the projection is an input.

### 3.4 Build times (indicative; busy host; `measure.sh builds` reruns them)

All nixpkgs toolchains present, all bob outputs of the configuration deleted.

| `//api:api` configuration | [8c/15G] | [2c/4G] |
|---|---|---|
| arm64 release (native) | 29.0 s | 92.7 s |
| amd64 release (cross) | 28.7 s | 57.0 s |
| amd64 + asan | 31.3 s | 92.0 s |
| amd64 + debug | 28.8 s | 60.5 s |

Cold includes the Go std of the configuration, all Go groups, libfoo, rcore and its crates, and
(cross) the `rust-std` copy. Cross is not slower than native here; the spread at [2c/4G]
(57–93 s) is mostly noise from parallel spikes and must be rechecked on a quiet machine.

Warm-std rebuilds observed while developing: `+asan+ubsan` after `+asan`: 4 s (only libfoo +
cgo/link); feature flip on rdemo: < 5 s.

## 4. Answer

1. **Cross-compile on the host; every derivation keeps the host Nix system.** For
   aarch64→x86_64 at the pin: C/C++ cross gcc (cached), host Go with `GOARCH`, host rustc with
   `--target` and a target std taken from the target system's own nixpkgs rustc (cached). The
   full fixture (cgo + C++ + Rust + protobuf) cross-builds and prints the expected line under
   qemu-user. Proc-macros and build scripts are host units; build-script runs are per target.
2. **Target libraries:** default to the target system's own nixpkgs package (cached, no
   source build), at the cost of a second glibc in the closure; allow the `pkgsCross` set as an
   option (smaller closure, usually a source build: 369 s for openssl).
3. **Variants are a typed configuration projected per package kind.** Only the projection
   enters the derivation, so unaffected packages are shared automatically and variants never
   collide. Measured sharing: 99 vs 313 derivations over 11 configurations.
4. **Testing foreign-arch binaries:** qemu-user works rootless for normal binaries (Go, C++,
   Rust). Sanitiser binaries (ASan, TSan) do not run under qemu-user; they must run on a native
   worker of that architecture.

## 5. SPEC input

### 5.1 Platform and variant model [proposal]

```go
// package bob
type Platform string
const (
    LinuxAMD64 Platform = "linux-amd64"
    LinuxARM64 Platform = "linux-arm64"
)

type Profile string
const ( Release Profile = "release"; Debug Profile = "debug" )

type Sanitizer string
const ( ASan Sanitizer = "asan"; UBSan Sanitizer = "ubsan"; Race Sanitizer = "race" ) // race: Go only

// Variant is the configuration of one build request. The zero value = host platform, release.
type Variant struct {
    Platform  Platform
    Profile   Profile
    Sanitize  []Sanitizer
    Features  map[Label][]string // Rust crate features added on top of the crate's own
}

// In bob.go: named variants that pipelines and the CLI refer to.
var _ = bob.Variants(
    bob.NamedVariant("asan", bob.Sanitize(bob.ASan, bob.UBSan), bob.Profile(bob.Debug)),
    bob.NamedVariant("loud", rust.Feature(rdemo, "shout")),
)
// A package can restrict where it builds:
cc.Library("foo", cc.Platforms(bob.LinuxAMD64, bob.LinuxARM64))
```

### 5.2 Label syntax [proposal]

```
//api:api                         host platform, release
//api:api@linux-amd64             cross (or native on an amd64 host)
//api:api@linux-amd64+debug       profile
//api:api@linux-arm64+debug+asan  sanitisers / named variants, any order, canonical order on output
//api:api@+asan                   host platform, variant only
```

- Grammar: `label ["@" [platform] {"+" token}]`, token = `release|debug|asan|ubsan|race|<named variant>`.
- Canonical form (logs, UI, cache listings): platform always printed, tokens sorted
  `profile, sanitisers (asan, ubsan, race), named variants (alphabetical)`; `release` omitted.
- CLI: `bob build //api:api --platform linux-amd64 --variant debug,asan` is equivalent.
- Errors: `✗ unknown platform "linux-x86" (known: linux-amd64, linux-arm64)`;
  `✗ //rcore:rcore does not support variant "asan" for Rust; it is built without it` is a
  **note**, not an error (projection drops it).

### 5.3 Projection rules (what enters each derivation) [proposal]

| Package kind | Platform | Profile | ASan/UBSan | Go race | Rust features |
|---|---|---|---|---|---|
| download / unpack (FOD) | – | – | – | – | – |
| codegen (protoc etc., host tool) | – | – | – | – | – |
| C/C++ object/library | ✔ | ✔ (`-O2` / `-O0 -g`) | ✔ (`-fsanitize=… -fno-omit-frame-pointer`) | – | – |
| Go std | ✔ | ✔ (`-N -l`) | ASan only (`-asan`) | ✔ | – |
| Go package (pure) | ✔ | ✔ | ASan only | ✔ | – |
| Go cgo package / link | ✔ | ✔ | ✔ (all sanitiser runtimes of linked C deps) | ✔ | – |
| Rust target crate | ✔ | ✔ (opt 2 / opt 0 + debuginfo + debug-assertions) | – (stable rustc) | – | own crate only |
| Rust host unit (proc-macro, build-script compile, their deps) | host only | – (always opt 0) | – | – | own crate only |
| Rust build-script run | ✔ (`TARGET`, `CARGO_CFG_*`) | ✔ (`PROFILE`, `OPT_LEVEL`, `DEBUG`) | – | – | ✔ (`CARGO_FEATURE_*`) |

- A derivation's name is `<label>[-host][-<platform if cross>][-<projection tag>]`, e.g.
  `libfoo-foo-linux-amd64-address`, so store paths stay readable.
- Rust `-C metadata` includes the projection tag.
- nixpkgs dependencies are typed: `nixpkgs.Tool("protobuf")` (always host set) vs
  `nixpkgs.Lib("openssl")` (target set). Untyped lists cannot be cross-compiled correctly.

### 5.4 Toolchain selection rules [proposal]

| Ecosystem | native | cross host→target |
|---|---|---|
| C/C++ | `stdenv.cc` | `pkgsCross.<gnu64 \| aarch64-multiplatform>.stdenv.cc`, commands `<triple>-gcc/g++/ar` |
| C/C++ target libs | `<attr>` | default `legacyPackages.<target-system>.<attr>`; option `pkgsCross.<x>.<attr>` |
| Go | host `go` | host `go`, `GOOS=linux GOARCH=<arch>`, `CC=<triple>-gcc`, `-extld=<triple>-gcc`, tools from `pkg/tool/linux_<hostarch>` |
| Rust | host `rustc` | host `rustc --target <triple> --sysroot <rust-std-<triple>> -C linker=<triple>-cc`; `rust-std-<triple>` = files of `manifest-rust-std-<triple>` copied from `legacyPackages.<target-system>.rustc.unwrapped` |

Pre-check (Go, before any derivation): the target system's rustc must report the **same
version string** as the host rustc (same nixpkgs pin guarantees it; a mismatch gives E0514).

### 5.5 Supported in v1 [proposal]

| | native arm64 | native amd64 | arm64→amd64 | amd64→arm64 |
|---|---|---|---|---|
| C/C++ | ✔ verified | (same code) | ✔ verified (qemu) | toolchain cached, untested |
| Go pure / cgo | ✔ | (same code) | ✔ verified (qemu): cgo api, pure-Go 20-package synthetic (static binary, same output `total 400` as native) | untested |
| Rust lib/staticlib/bin, proc-macro, build script | ✔ | (same code) | ✔ verified (qemu) | untested |
| debug/release, Rust features | ✔ | | ✔ | |
| ASan/UBSan C/C++ + cgo, Go `-asan`, Go `-race` | ✔ verified (runs) | | builds; **runs only on a native amd64 worker** | |
| Rust sanitisers, MSan, TSan for C/C++ | ✗ not in v1 | | | |

### 5.6 Budgets [proposal] (reference [2c/4G], to confirm on a quiet machine)

- Cold cross toolchain for one target, substituted: ≤ 700 MiB download, ≤ 60 s on a 100 Mbit/s
  link. A target library not cached on cache.nixos.org is reported before the build:
  `note: openssl for linux-amd64 is not in the binary cache and will be built from source (several minutes)`.
- Adding one variant to an already-built configuration: rebuild only the projection-affected
  packages; Go std per new (platform, Go-variant) ≤ 60 s [2c/4G].
- Evaluation: a variant adds no evaluation cost beyond computing the extra derivations
  (same as a graph of that size, gen-b budgets).

## 6. Decisions, risks, open items

### Agent decisions

- Target libraries default to the target system's native nixpkgs set: cached beats a smaller
  closure (principle 4, R-NFR-008: no 6-minute openssl build on small machines).
- Host units are generated by a separate host-role generator also for native builds: one
  derivation per host unit for all targets (principle 4).
- Rust target std is copied from the target system's nixpkgs rustc, not fetched from upstream:
  upstream std is incompatible (measured E0514).
- Sanitisers are not propagated to Rust crates (stable rustc cannot); Go packages get only ASan
  (Go has no UBSan mode); the cgo/link step gets all C sanitiser runtimes.
- qemu-user is used only for verification in the spike; bob does not run tests of foreign
  architectures under emulation in v1 (sanitiser runtimes fail there).

### Owner decision needed

**O-S7-1: Tests of the non-host architecture.**
- a) Run them only on workers of that architecture; locally `bob test @linux-amd64` on an arm64
  machine builds and reports "not run: needs a linux-amd64 worker".
- b) Additionally run them under qemu-user when no worker exists (works for ordinary binaries,
  not for sanitiser builds; adds qemu-user as a test dependency).
- c) Refuse to build tests for a non-host platform locally.

Recommendation: **b**, with sanitiser variants always marked "native worker only".

**O-S7-2: Where target C/C++ libraries come from by default.**
- a) Target system's native nixpkgs set (cached; second glibc in the closure, +38 MB here).
- b) `pkgsCross` set (clean closure; often built from source: 369 s for openssl here).
- c) Per-package choice with a) as default.

Recommendation: **c** (default a).

### Risks

- The two-glibc closure is fine while both have the same version (same pin). A future
  nixpkgs where the cross glibc and the native glibc diverge would break at run time; bob
  should pre-check that both glibc versions match.
- nixpkgs cross gcc-lib RUNPATH gap for `libasan` (3.2) needs a bob-side fix.
- cmd/go is only used to build std; its `-asan` check needs a shim for prefixed compilers.
  Building std package-by-package removes that dependency.
- The native fixture binary has a 366 MB closure because it references the full native
  `gcc-15.3.0` (cross: 76 MB). A fixup/reference-scan step is needed (also in gen-b §7).
- amd64→arm64 cross is unverified (no x86_64 host).

### Still to verify

- amd64 host: amd64→arm64 cross and native amd64 builds of the same graph; check that host
  units and codegen get the same derivations there as their native counterparts.
- Sanitiser binaries for amd64 on a real amd64 machine.
- Reproducibility (R-NFR-006) of cross outputs built on two hosts.
- Quiet-machine rerun of `measure.sh`.
