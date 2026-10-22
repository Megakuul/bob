# §04 Outputs and platforms

> Source of SPEC.md: `/root/bob/SPEC.md` is generated from spec/01…06 by concatenation (command in the SPEC.md preamble). Edit this file, then regenerate.

Scope: cross-compilation rules, toolchain selection, variant projection per package kind, OCI
images, AppImages, tarballs, closures as outputs, releases as built objects, the consumer side of
`bob install` (profile model), and output size/time budgets. ADRs: 0009 (cross part), 0010.

## Interfaces

- **§01 provides** `bob.P`, `bob.Define`, `bob.Platform` (`LinuxAMD64`, `LinuxARM64`, `Host()`),
  `bob.Variant` and its label grammar (`//pkg:name@<platform>+<token>…`), `bob.Package`
  (anything with store outputs: `Outputs() []bob.Output`), `bob.Executable` (a `bob.Package`
  with `MainProgram() string`, a path relative to its output), the derivation layer
  (`drv.Derivation`, structured attributes, `Name`), `bob-builder` subcommands, the per-package
  fixup step (§01 §1.13: strip; references are prevented with prefix maps such as
  `--remap-path-prefix`/`-trimpath` instead of rewritten, and a forbidden reference fails with
  BOB-E-9003), and the embedded Nix config.
  **Required from §01:** the derivation layer supports `__structuredAttrs` with
  `unsafeDiscardReferences` (R-OUT-011). No experimental feature is needed: `discard-references`
  is stable in Nix 2.34 (enabling it only warns `unknown experimental feature`; §01 R-NIX-024).
- **§01 provides** `nixpkgs.Lib(p, attr)` / `nixpkgs.Tool(p, attr)` typing (this section
  defines the semantics, R-FW-032) and the nixpkgs index lookup (`cached on cache.nixos.org?`).
- **§02 provides** CLI syntax for `--platform`, `--variant`, `bob install|upgrade|rollback|uninstall|list`,
  `bob exec`, JSON event output, the error template (`✗ <title>` + position + hint).
- **§03 provides** per-ecosystem builders that consume `Toolchain` (R-FW-033) and
  `Projection` (R-FW-038): `cc`, `golang`, `rust`, `npm`, `ocaml`.
- **§05 provides** the worker upload of a job's outputs into `staging/<buildID>/<jobID>/<attempt>/…` (§05.2.4; `COMPLETE.json` schema in §06 §6.6.4)
  (`COMPLETE.json` with `images[]`) and the release state machine.
- **§06 provides** promotion (verify, sign, publish), the OCI registry and Nix cache endpoints,
  the download proxy (D61), `ReleaseService` (`GetRelease`, `ListReleases`, `ResolveRelease`) and release
  visibility.
- **§04 provides** `oci.Image`, `appimage.Bundle`, `tarball.Tarball`, `release.Release`, the
  `bob.release/v1` manifest (R-OUT-023), the OCI layout in the store (R-OUT-011), the profile
  layout (R-INST-005) and the `Toolchain`/`Projection` types.

## 4.1 Platforms

#### R-PLAT-001 Supported systems
- **Statement:** bob builds for, runs on, and accepts as host and target exactly `linux-amd64`
  (Nix system `x86_64-linux`) and `linux-arm64` (`aarch64-linux`).
- **Acceptance:** Given `bob build //x:y@linux-riscv64`, then exit code 2 and stderr
  `✗ unknown platform "linux-riscv64" (known: linux-amd64, linux-arm64)`; no Nix process starts.
  Given the CLI started on a host whose `uname -m` is neither `x86_64` nor `aarch64`, then
  `✗ unsupported host architecture "<m>" (bob runs on linux-amd64 and linux-arm64)`, exit 2.
- **Source:** R-PLAT-001, D4.

#### R-PLAT-002 darwin stays possible
- **Statement:** `bob.Platform` is a string type with a closed v1 value set; no API exposes
  Linux-only types (e.g. ELF details) in public signatures except in `appimage`.
- **Acceptance:** (manual) review: adding `darwin-arm64` needs no change to a public signature
  outside `pkg/output/appimage`.
- **Source:** R-PLAT-002.

#### R-PLAT-004 Host builds, target compiles
- **Statement:** Every bob-generated derivation has `system` = the **host** Nix system; the
  target platform changes only the toolchain, flags and target libraries that are inputs. No
  emulated builder, binfmt, or remote builder of the other architecture is used.
- **Acceptance:** Given the polyglot fixture evaluated for `@linux-amd64` on an arm64 host,
  then every generated `.drv` has `system = "aarch64-linux"`, and `bob build` succeeds with
  `extra-platforms` unset. Output binaries are `ELF 64-bit LSB … x86-64` (`debug/elf` check:
  `Machine == EM_X86_64`).
- **Source:** ADR 0009 §1, S7 §4.1.

#### R-PLAT-005 v1 platform × ecosystem matrix
- **Statement:** v1 supports, for C, C++, Go (pure and cgo) and Rust (lib, bin, staticlib,
  cdylib, proc-macro, build script): native arm64, native amd64, arm64→amd64, amd64→arm64.
  npm/Node outputs are platform-independent JS plus a target `nodejs` (`nixpkgs.Lib`); OCaml is
  native-only in v1 (`@<other platform>` → error below).
- **Acceptance:** CI builds every fixture repo (BOOTSTRAP §6.4) for both platforms on a host of
  each architecture (4 combinations); cross outputs run under `qemu-<arch>` (R-FW-039) and print
  the golden line. OCaml cross request: `✗ //ml:tool: OCaml packages cannot be cross-compiled
  yet (requested linux-amd64 on a linux-arm64 host)` + hint `build it on a linux-amd64 worker
  with --remote`, exit 2.
- **Source:** R-FW-030, R-GO-001, R-RS-001, S7 §5.5. amd64→arm64 is unverified (S7 had no
  amd64 host) → Stage 3 verification task.

#### R-PLAT-006 Per-package platform restriction
- **Statement:** Any package option struct may set `Platforms []bob.Platform`; requesting a
  package (directly or as a dependency) for another platform is an evaluation error.
- **Acceptance:** Given `cc.Library(p, "foo", cc.LibraryOpts{Platforms: []bob.Platform{bob.LinuxARM64}})`
  and `bob build //app:app@linux-amd64` where app depends on foo, then exit 2 and
  `✗ //libs/foo:foo does not support linux-amd64 (it allows: linux-arm64)` with foo's `bob.go`
  position and the dependency chain `//app:app → //libs/foo:foo`.
- **Source:** S7 §5.1.

#### R-PLAT-007 glibc and rustc consistency pre-check
- **Statement:** Before generating derivations for a cross target, bob checks (from the nixpkgs
  index, no build) that (a) the cross glibc and the target system's native glibc have the same
  version when target libraries come from the native set, and (b) the host `rustc` and the
  target system's `rustc` report the same version string.
- **Acceptance:** Given a test index where `legacyPackages.x86_64-linux.glibc.version = "2.42"`
  and the cross glibc is `"2.43"`, then `✗ nixpkgs mismatch: target libraries for linux-amd64
  use glibc 2.42 but the cross toolchain uses glibc 2.43` with hint `set Libs: cc.CrossLibs on
  the packages that link them, or move the nixpkgs pin`. Same for rustc (`rustc 1.95.0` vs
  `…`), hint `the nixpkgs pin must provide the same rustc for both systems`.
- **Source:** S7 §5.4, §6 risks.

## 4.2 Toolchains and dependencies

#### R-FW-032 Typed nixpkgs dependencies
- **Statement:** nixpkgs dependencies are typed: `nixpkgs.Tool(p, attr)` (runs at build time:
  always taken from the host's package set) and `nixpkgs.Lib(p, attr)` (linked into or shipped
  with the output: taken from the target set per R-FW-036). A `Tool` is not a `cc.Linkable`;
  a `Lib` is not a `bob.Tool` (compile-time type error).
- **Acceptance:** Golden test: `//api:api@linux-amd64` on arm64 references
  `legacyPackages.aarch64-linux.protobuf` for `Tool("protobuf")` and
  `legacyPackages.x86_64-linux.openssl` for `Lib("openssl")`. A `bob.go` passing
  `nixpkgs.Tool(p,"openssl")` in `cc.LibraryOpts.Deps` fails `go build` (example program in the
  error-quality suite asserts the bob-translated compile error).
- **Source:** S7 §3.2 "untyped nixpkgs deps", STATUS findings (S7).

```go
package nixpkgs // pkg/nixpkgs

// Lib is a nixpkgs package used as a target library: linked into, or shipped with, an
// output. For a cross target it comes from the target system's package set (default) or
// the pkgsCross set (cc.CrossLibs). Lib values satisfy cc.Linkable and bob.Package.
func Lib(p *bob.P, attr string) *Package

// Tool is a nixpkgs package that only runs during a build (code generators, compilers,
// build-time utilities). It always comes from the host's package set and never enters an
// output's closure unless the build output references it.
func Tool(p *bob.P, attr string) *ToolPackage
```

Example:

```go
var Gen = bob.Define(func(p *bob.P) *bob.RunStep {
	return bob.Run(p, "schema", bob.RunOpts{
		Tools: []bob.Tool{nixpkgs.Tool(p, "protobuf")},
		Cmd:   []string{"protoc", "--descriptor_set_out=$out/schema.pb", "schema.proto"},
		Srcs:  bob.Glob("schema.proto"),
	})
})
```

#### R-FW-033 Toolchain selection
- **Statement:** For each (ecosystem, host, target) bob selects the toolchain by the table
  below; `Toolchain` values are computed once per (host, target, ecosystem, toolchain version)
  per evaluation and shared by all packages.
- **Acceptance:** Golden derivations for the fixture on both hosts × both targets contain the
  attributes, env vars and flags of the table. Two packages of the same kind and target share
  one toolchain derivation path (graph test).
- **Source:** ADR 0009 §2, S7 §5.4.

| Ecosystem | Native | Cross host→target |
|---|---|---|
| C/C++ | `stdenv.cc` of the host set; `gcc`, `g++`, `ar` | `pkgsCross.<gnu64 \| aarch64-multiplatform>.stdenv.cc`; commands `<triple>-gcc`, `<triple>-g++`, `<triple>-ar`; triple `x86_64-unknown-linux-gnu` / `aarch64-unknown-linux-gnu` |
| Go | host `go` | host `go`; env `GOOS=linux GOARCH=<amd64\|arm64> CC=<triple>-gcc CXX=<triple>-g++`; link `-extld=<triple>-gcc`; tools from `pkg/tool/linux_<hostarch>`; every cgo package derivation has `CC` set (cgo calls `$CC`) |
| Go std | one derivation per (go version, target, cgo on/off, Go projection) | same; compiled package by package by bob (no `go build std`, which rejects prefixed compilers for `-asan`/`-race`) |
| Rust | host `rustc` | host `rustc --target <triple> --sysroot <rust-std-<triple>> -C linker=<triple>-cc` |
| C/C++ target libs | host set `<attr>` | R-FW-036 |
| Node | `nodejs` (Lib) | `legacyPackages.<target-system>.nodejs` |

```go
package bob

// Toolchain describes the compilers an ecosystem uses for one (host, target) pair.
// Builders in pkg/<ecosystem> receive it from p.Toolchain; users never construct one.
type Toolchain struct {
	Host, Target Platform
	Triple       string        // "x86_64-unknown-linux-gnu"; empty for native
	CC           drv.Ref       // C compiler wrapper derivation
	Prefix       string        // "x86_64-unknown-linux-gnu-" for cross, "" for native
}

// Toolchain returns the C/C++ toolchain for the package's target. Example:
//   tc := p.Toolchain()          // inside a bob.Define body
//   _ = tc.Prefix + "gcc"
func (p *P) Toolchain() Toolchain
```

#### R-FW-034 Rust target std
- **Statement:** For a cross target, bob builds a derivation `rust-std-<triple>` whose output is
  exactly the files listed in `lib/rustlib/manifest-rust-std-<triple>` of
  `legacyPackages.<target-system>.rustc.unwrapped`, laid out as a sysroot. Upstream
  `rust-std` tarballs are never used.
- **Acceptance:** `//rdemo:rdemo@linux-amd64` on arm64 builds and under `qemu-x86_64` prints
  `rdemo target=x86_64-unknown-linux-gnu answer=42 bits=64 x86cfg=true`. A test asserts no
  derivation fetches from `static.rust-lang.org`.
- **Source:** S7 §3.1 (E0514 with upstream std).

#### R-FW-035 Host units
- **Statement:** Rust proc-macros, build-script compilations and their dependency crates are
  generated by a host-role generator, also for native builds: their derivation depends only on
  the host platform and the crate's features, never on target or profile (always opt-level 0).
  Build-script **runs** are per target and profile (`TARGET`, `CARGO_CFG_*`, `PROFILE`,
  `OPT_LEVEL`, `DEBUG`, `CARGO_FEATURE_*`).
- **Acceptance:** Graph test on one host: `rmac` (proc-macro) and `rdemo-build-script` have the
  same `.drv` path in `@linux-arm64`, `@linux-amd64`, `+debug`, `@linux-amd64+debug`.
- **Source:** ADR 0009 §3, S7 §3.3.

#### R-FW-036 Source of target C/C++ libraries
- **Statement:** `nixpkgs.Lib` for a cross target defaults to `legacyPackages.<target-system>.<attr>`
  (`cc.NativeLibs`); a package may set `Libs: cc.CrossLibs` to use `pkgsCross.<x>.<attr>`
  instead. Before building, bob reports target libraries that are not on cache.nixos.org.
- **Acceptance:** Default: `//api:api@linux-amd64` closure contains both
  `glibc-x86_64-unknown-linux-gnu-2.42-*` and `glibc-2.42-*` and runs. With `cc.CrossLibs`:
  only the cross glibc. Given an uncached Lib, then before any build the CLI prints
  `note: openssl for linux-amd64 is not in the binary cache and will be built from source (several minutes)`
  (JSON event `{"type":"note","code":"uncached-target-lib","attr":"openssl","platform":"linux-amd64"}`).
- **Source:** A19, S7 §3.1 (pkgsCross openssl 369 s), ADR 0009 §2.

#### R-FW-037 Cross sanitiser runtime path
- **Statement:** For sanitised C/C++/cgo binaries on a cross target, bob adds the cross
  `gcc-lib` `lib` directory to the binary's RUNPATH (link flag `-Wl,-rpath,<gcc-lib>/lib`).
- **Acceptance:** `readelf -d` of `//api:api@linux-amd64+asan` lists the cross gcc-lib dir in
  `RUNPATH`; on an amd64 worker the binary starts without `LD_LIBRARY_PATH` and prints the
  golden line.
- **Source:** S7 §3.2 (nixpkgs packaging gap), ADR 0009 open item.

## 4.3 Variants

#### R-FW-031 Build variants
- **Statement:** A variant is a typed configuration (profile, sanitisers, Rust features, named
  variants) defined in §01; it changes a package's identity only through the package's
  **projection** (R-FW-038) and propagates to dependencies as the projection table says.
- **Acceptance:** `matrix` test over the fixture's 11 configurations of S7 §3.3: the number of
  distinct derivations equals the golden (S7: 99, ±0 after the fixture is frozen), and no two
  configurations with different projections of one package yield the same path.
- **Source:** R-FW-031, D28, ADR 0009 §4.

#### R-FW-038 Projection per package kind
- **Statement:** Each package kind sees only the variant fields of the table; only the
  projection enters its derivation (env, args, `-C metadata`, name). Dropped fields produce a
  **note**, never an error.
- **Acceptance:** Golden table test: for each row, flipping a field marked `–` leaves the
  `.drv` path unchanged; flipping a `✔` field changes it. Requesting `+asan` for a Rust-only
  target prints `note: //rcore:rcore is built without asan (not supported for Rust in v1)`.
- **Source:** S7 §5.3.

| Package kind | Platform | Profile | ASan/UBSan | Go race | Rust features |
|---|---|---|---|---|---|
| fetch / unpack (bob-imported source paths, FOD-identical; §01 R-NIX-008) | – | – | – | – | – |
| codegen (host tool) | – | – | – | – | – |
| `nixpkgs.Tool` | – | – | – | – | – |
| `nixpkgs.Lib` | ✔ | – | – | – | – |
| C/C++ object/library/binary | ✔ | ✔ `-O2` / `-O0 -g` | ✔ `-fsanitize=<list> -fno-omit-frame-pointer` | – | – |
| Go std, Go package (pure) | ✔ | ✔ (`-N -l`) | ASan only (`-asan`) | ✔ | – |
| Go cgo package, Go link | ✔ | ✔ | ✔ (all sanitiser runtimes of linked C deps) | ✔ | – |
| Rust target crate | ✔ | ✔ opt 2 / opt 0 + debuginfo + debug-assertions | – | – | own crate |
| Rust host unit | host | – (opt 0) | – | – | own crate |
| Rust build-script run | ✔ | ✔ | – | – | ✔ |
| npm/TS compile, bundle | – | ✔ (`mode=development` / `production`) | – | – | – |
| npm app runtime (`nodejs` Lib) | ✔ | – | – | – | – |
| OCaml | host | ✔ (`-g` / `-O3` with flambda if available, else `-g` off) | – | – | – |
| OCI image, AppImage, tarball, release | union of their roots' projections (they are rebuilt only if a root changes) | | | | |

- Derivation name: `<dir-label>-<name>[-host][-<platform if cross>][-<tag>]`, tag = projection
  tokens joined by `-` in canonical order (`dbg`, `address`, `undefined`, `race`, `f-<hash8>`
  for features), e.g. `libs-foo-foo-linux-amd64-address`. Length ≤ 200 bytes (Nix limit 211);
  longer names are cut and end in `-<hash8>`.

#### R-FW-039 Foreign-architecture and sanitiser tests
- **Statement:** A test package for platform T runs on a worker of T; with none available
  (local build or no such worker), bob runs it under `qemu-<arch>` (nixpkgs `qemu-user`,
  `nixpkgs.Tool`) unless the test has a sanitiser or `race` projection; those report
  `not run: needs a native linux-amd64 machine` (status `skipped`, exit 0 unless
  `--require-all-tests`, then exit 1).
- **Acceptance:** On arm64 locally, `bob test //api:test@linux-amd64` passes via qemu
  (JSON event `{"type":"test","runner":"qemu-x86_64",…}`); `…@linux-amd64+asan` → status
  `skipped` with the text above. On the server, the scheduler (§05) assigns T-tests to T-workers
  when any is registered.
- **Source:** A18, S7 §3.2.

#### R-PLAT-008 Cross build budgets **[proposal]**
- **Statement / Acceptance:**
  - Cold cross toolchain (C cross cc + rust target std) substitution for one target:
    ≤ 700 MiB download, ≤ 15 s [8c/15G] (quiet: cc wrapper 2.2 s, rustc 6.6 s, qemu-user 1.0 s).
  - Cold build of fixture `//api:api` with toolchains present: native ≤ 35 s [8c/15G] /
    ≤ 75 s [2c/4G] (quiet 25.2 / 56.9 s); `@linux-amd64` ≤ 35 / 75 s (24.7 / 55.5 s);
    `+asan` ≤ 35 / 85 s (26.6 / 63.8 s); `+debug` ≤ 30 / 60 s (21.6 / 45.4 s).
  - Adding a variant to a built configuration rebuilds only projection-affected derivations
    (graph assertion) and the Go std per new (platform, Go projection) ≤ 60 s [2c/4G].
- **Source:** `docs/spikes/measure/quiet/s07-cross.log`; budgets = quiet × 1.3, rounded up.

## 4.4 Outputs in general

#### R-OUT-001 Closure as output
- **Statement:** Every `bob.Package` is a closure output: `bob build` makes its outputs
  available in bob's store, `bob install` installs `release.Closure` artifacts, and the worker
  uploads closures to the cache via §05/§06.
- **Acceptance:** `bob build //cmd/api:api --json` emits `{"type":"result","label":"//cmd/api:api","outputs":{"out":"/nix/store/…"}}`;
  `bob build --remote` of the same label then has the path substitutable from the server
  (`nix path-info --store <server>` in the harness).
- **Source:** R-OUT-001, R-NIX-003.

#### R-OUT-006 Bundles are build targets
- **Statement:** `oci.Image`, `appimage.Bundle` and `tarball.Tarball` are `bob.Package`s whose
  outputs are store paths built by `bob-builder` subcommands (`oci-layer`, `oci-image`,
  `appimage`, `tarball`) in ordinary derivations; they cache, upload and promote like any
  package. The builders never run Docker, a daemon, or network access.
- **Acceptance:** `bob build //deploy:image` twice → second run is a no-op (no derivation built).
  Building in the sandbox with network disabled succeeds.
- **Source:** ADR 0010, S8 §5.1.

#### R-OUT-007 Per-output pruning
- **Statement:** Bundles may remove files of nixpkgs paths from **their copy** of the closure
  through `Prune` globs (matched against `/nix/store/<hash>-<name>/<path>`; `*` does not cross
  `/`, `**` does). nixpkgs store paths themselves are never modified. A pruned file that the
  bundle's ELF files need (`DT_NEEDED` resolution) is a build error.
- **Acceptance:** AppImage of fixture `api` with `appimage.DefaultPrune` ≤ 12 MB and runs.
  Given `Prune: []string{"/nix/store/*-openssl-*/lib/libssl.so*"}`, then
  `✗ //deploy:bundle: prune pattern "/nix/store/*-openssl-*/lib/libssl.so*" removes libssl.so.3, which api needs`.
- **Source:** S8 §3.1, §5.4, S8-7.

#### R-OUT-008 Licence manifest
- **Statement:** Every bundle contains `licences.json` (AppImage: `.bob/licences.json`; OCI:
  config label `org.opencontainers.image.licenses` = SPDX expression of the roots, plus a file
  `/.bob/licences.json` in the customisation layer; tarball: `bob/licences.json`):
  `[{"path":"<store path>","name":"<pname>","version":"…","licenses":["<SPDX id>",…]}]` from
  nixpkgs `meta.license` (bob paths: the `License` field of their package, default `"unknown"`).
- **Acceptance:** The fixture AppImage's `licences.json` lists glibc with its nixpkgs licence
  (`LGPL-2.0-or-later` at the pin: meta.license is lgpl2Plus; upstream COPYING.LIB says 2.1, so
  `LGPL-2.1-or-later` is accepted too) and openssl with `Apache-2.0`; entries sorted by path.
- **Source:** S8 §5.8, D16, D52.

## 4.5 OCI images

#### R-OUT-002 OCI images from closures
- **Statement:** `oci.Image` produces an OCI image layout (`oci-layout`, `index.json`,
  `blobs/sha256/…`) with one image index over the requested platforms (default amd64 +
  arm64, cross-built per R-PLAT-004), written by bob's own Go code (stdlib `archive/tar`,
  `compress/gzip`, `encoding/json`; no go-containerregistry, no Docker).
- **Acceptance:** `crane validate --remote` of the pushed fixture index → PASS for each platform;
  index media type `application/vnd.oci.image.index.v1+json`, platforms
  `[{linux,amd64},{linux,arm64}]` sorted amd64 first; rootless `podman run` (bobtest) of the
  host-platform image prints the golden line; `id` → `uid=65534(app) gid=65534(app)`.
- **Source:** R-OUT-002, D3, ADR 0010 §1, S8 §3.2.

```go
package oci // pkg/output/oci

// Image is an OCI image built from closures, without Docker. Each platform gets one image
// manifest; the result is an image index. Program and Extra are built for each platform.
type Image struct{ /* unexported */ }

// ImageOpts configures an image. Zero values give the defaults in the field comments.
type ImageOpts struct {
	Program    bob.Executable      // closure root; Entrypoint defaults to its MainProgram
	Extra      []bob.Package       // more roots (e.g. a debug shell); each gets its own layer
	Entrypoint []string            // default: []string{<Program out>/<MainProgram>}
	Cmd        []string
	Env        map[string]string   // merged over the defaults PATH, SSL_CERT_FILE, TZDIR
	User       string              // default "65534:65534"; "0:0" must be written explicitly
	WorkDir    string              // default "/"
	Ports      []int               // ExposedPorts "<n>/tcp"
	Labels     map[string]string   // org.opencontainers.image.* from a release are added
	NoCACerts  bool                // default false: nss-cacert + /etc/ssl/certs symlinks
	NoTZData   bool                // default false: tzdata + /usr/share/zoneinfo symlink
	Platforms  []bob.Platform      // default {LinuxAMD64, LinuxARM64}
	MaxLayers  int                 // default 100, allowed 2..125
	Prune      []string            // R-OUT-007; default none
}

// NewImage declares an image. Example:
//
//	var Image = bob.Define(func(p *bob.P) *oci.Image {
//		return oci.NewImage(p, "api", oci.ImageOpts{
//			Program: api.Binary(p),
//			Ports:   []int{8080},
//		})
//	})
func NewImage(p *bob.P, name string, opts ImageOpts) *Image
```

#### R-OUT-009 Layer algorithm
- **Statement:** Per platform: (1) closure of roots (Program + Extra); (2) popularity(p) =
  number of closure paths whose closure contains p; (3) non-root paths sorted by
  (popularity desc, path asc); with `L = MaxLayers − 1 − |roots|`, the first `L−1` get one
  layer each, the rest share one layer; (4) one layer per root in root order; (5) the
  customisation layer (R-OUT-012) last.
- **Acceptance:** Fixture `api` v1 → 14 layers; v2 (one string changed in `api`) differs from
  v1 in exactly one layer blob and the config/manifest; new compressed bytes ≤ 16 % of the image
  (S8: 3.78 / 25.4 MB). With `MaxLayers: 4`: 4 layers, the root alone in layer 3.
- **Source:** S8 §3.2, §5.2, S8-2.

#### R-OUT-010 Reproducibility
- **Statement:** Image bytes depend only on the closure contents and options. Tar: lexical
  order, parents first, each layer includes `nix/` and `nix/store/` dirs (0555, uid 0),
  `mtime` = 1 s since epoch, uid/gid 0, no uname/gname, permissions from the store, symlinks as
  symlinks, hard links as regular files, no xattrs, format chosen by `archive/tar`. Gzip:
  `compress/gzip` `DefaultCompression`, no name, `ModTime` zero. Config: no `created`;
  `history[i].created` = `1970-01-01T00:00:01Z`; JSON from structs (`encoding/json`, fixed
  field order). Layer identity = `diff_id`; promoted blobs are reused by `diff_id` and never
  recompressed.
- **Acceptance:** Build the fixture image twice into fresh stores (different machines in the
  nightly suite, `[8c/15G]` and `[2c/4G]`) → identical index digest. Golden: index digest of
  the frozen fixture.
- **Source:** S8 §5.3, R-NFR-006.

#### R-OUT-011 Image layout in the store
- **Statement:** Each layer is its own derivation `oci-layer-<storehash8>-<name>`
  (`bob-builder oci-layer`), keyed by the store paths it contains, the prune set and the layer
  format version `bob-oci-layer-v1`; its output is `layer.tar.gz` + `layer.json`
  (`{"diffID","digest","size","paths":[…]}`) and it sets `unsafeDiscardReferences.out = true`
  (structured attrs), so a layer never pulls the paths it contains into its closure. The image
  derivation's output is an OCI layout whose layer blobs are symlinks to the layer outputs;
  config, manifests and index are regular files.
- **Acceptance:** `nix-store -q --references <layer out>` is empty; the image output's closure
  = image + its layers only (no glibc). `skopeo copy --all oci:<out>:<tag> docker://…` succeeds
  (follows symlinks). Changing only `api` rebuilds exactly one layer derivation plus the image
  derivation. Verified (integration, 2026-10-21) on Nix 2.34.8 and the embedded 2.34.9 with
  `experimental-features = nix-command` only (no `discard-references`):
  `unsafeDiscardReferences.out = true` produced an output containing an input's path with no
  registered reference; the control without it registered the reference.
- **Source:** S8 §5.2 (layer cache key), verification below.

#### R-OUT-012 Customisation layer and defaults
- **Statement:** The last layer contains: `/tmp` (1777), `/etc/passwd`
  (`root:x:0:0::/root:/sbin/nologin` and `app:x:65534:65534::/tmp:/sbin/nologin`),
  `/etc/group` (`root:x:0:`, `app:x:65534:`), `/etc/nsswitch.conf` (`hosts: files dns`),
  unless disabled: `/etc/ssl/certs/ca-certificates.crt` and `/etc/ssl/certs/ca-bundle.crt`
  symlinks to nss-cacert, `/usr/share/zoneinfo` → tzdata; `/.bob/licences.json`. Config env
  defaults: `PATH=<roots' bin dirs joined>:/usr/bin:/bin`, `SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt`,
  `TZDIR=/usr/share/zoneinfo`.
- **Acceptance:** In rootless podman: HTTPS GET to the harness's TLS server (CA from nss-cacert
  test chain) returns 200; `TZ=Europe/Zurich date -d @0` → `Thu Jan  1 01:00:00 CET 1970`.
- **Source:** S8 §3.2.

#### R-OUT-013 Images in releases and the registry
- **Statement:** When a build includes an `oci.Image`, the worker uploads its blobs (layers,
  configs, manifests, index) into `<staging>oci/blobs/sha256/<hex>` (`<staging>` = `staging/<buildID>/<jobID>/<attempt>/`) and lists the
  image in `COMPLETE.json.images[]` as `{"repo","tags","index":"sha256:…","mediaType","blobs":[…]}`;
  promotion and tags are §06. v1 layers are gzip only (`application/vnd.oci.image.layer.v1.tar+gzip`).
- **Acceptance:** Harness: release pipeline of the fixture → `podman pull <server>/acme/api:<version>`
  (service-account token) and `crane manifest` returns the same index digest as the local build.
- **Source:** A17, S12 §5.1, R-REG-003.

## 4.6 AppImages

#### R-OUT-003 AppImage
- **Statement:** `appimage.Bundle` produces one `<name>-<arch>.AppImage` (`arch` = `x86_64` |
  `aarch64`) that runs on the hosts of R-OUT-018 without Nix, FUSE, or unprivileged user
  namespaces, bundling ld.so, glibc, all libraries and data of the closure.
- **Acceptance:** Distro matrix (R-OUT-018) green for the fixture `api` and `curl` bundles.
- **Source:** R-OUT-003, ADR 0010 §2.

```go
package appimage // pkg/output/appimage

// Bundle is a self-contained AppImage for one platform.
type Bundle struct{ /* unexported */ }

type BundleOpts struct {
	Program      bob.Executable            // main entry
	Entries      map[string]bob.Executable // extra entries, chosen by BOB_ENTRY or argv[0]
	Env          map[string]string         // "${STORE}" expands to the bundled store at run time
	NoCACerts    bool                      // default false: nss-cacert, SSL_CERT_FILE set
	Locales      []string                  // default {"C.UTF-8"}; others add a locale-archive
	NoIconv      bool                      // true prunes glibc lib/gconv (iconv only builtin charsets)
	HostGraphics bool                      // append host GPU library dirs (R-OUT-020)
	Desktop      *Desktop                  // nil → Terminal=true entry, built-in placeholder icon
	Platform     bob.Platform              // default host
	Prune        []string                  // default DefaultPrune; nil-vs-empty: []string{} = no pruning
	Mode         Mode                      // AutoMode (default), UsernsOnly
}

type Desktop struct {
	Name       string     // Name= in the .desktop file
	Icon       bob.Source // SVG or PNG, 256x256
	Categories []string
	Terminal   bool
}

// Mode restricts the launcher. UsernsOnly is required for programs that exec other
// store paths or are shebang scripts (R-OUT-019).
type Mode int

const (
	AutoMode Mode = iota
	UsernsOnly
)

// DefaultPrune is the prune set used when Prune is nil.
var DefaultPrune = []string{
	"/nix/store/*-glibc-*/share/i18n/**",
	"/nix/store/*-glibc-*/share/locale/**",
	"/nix/store/*-gcc-*-lib/lib/lib?san.so*",
	"/nix/store/*-gcc-*-lib/lib/libhwasan.so*",
}

// NewBundle declares an AppImage. Example:
//
//	var Bundle = bob.Define(func(p *bob.P) *appimage.Bundle {
//		return appimage.NewBundle(p, "api", appimage.BundleOpts{
//			Program:  api.Binary(p),
//			Platform: bob.LinuxAMD64,
//		})
//	})
func NewBundle(p *bob.P, name string, opts BundleOpts) *Bundle
```

#### R-OUT-015 AppImage file format and runtime
- **Statement:** File = bob runtime ELF (static Go, `CGO_ENABLED=0`, for the bundle's
  platform) + squashfs image appended at the ELF's end. Bytes 8–10 = `AI\x02`. The squashfs is
  written by nixpkgs `mksquashfs` (`nixpkgs.Tool`) inside the `appimage` derivation with
  `-all-root -no-xattrs -comp zstd -Xcompression-level 19 -b 1M` and `SOURCE_DATE_EPOCH=1`
  (bob-builder's environment; mksquashfs refuses `-all-time 1 -mkfs-time 1` next to it, the
  effect is the same: mkfs and inode times 1).
  AppDir: `AppRun` (= the runtime binary, launcher mode), `<name>.desktop`, `<name>.svg|png`,
  `.DirIcon`, `.bob/launch.json`, `.bob/licences.json`, `nix/store/…`. The runtime never uses
  FUSE in v1: it extracts once to `$XDG_CACHE_HOME/bob/appimage/<sha256 of file>/` (fallback
  `~/.cache`, then `$TMPDIR/bob-appimage-<uid>/`) via a temp dir + `rename`, verifies a
  `.complete` marker, and then acts as launcher. Flags: `--appimage-extract [<glob>]` (to
  `./squashfs-root`), `--appimage-offset`, `--appimage-version`, `--appimage-help`.
- **Acceptance:** Byte-identical AppImage from two fresh builds (golden sha256 of the frozen
  fixture). `file` reports an ELF; `--appimage-offset` prints the squashfs offset and
  `unsquashfs -o <offset>` lists `AppRun`. First run extracts (≤ 0.5 s for the 10.8 MB fixture,
  [8c/15G]); second run starts ≤ 0.1 s; two concurrent first runs both succeed and leave one
  cache dir. The bundle contains no LGPL/GPL runtime code (licence check on the runtime binary's
  Go module graph).
- **Source:** A15, A16, S8 §3.3, §5.5. FUSE mount is **ROADMAP** (it needs a setuid
  `fusermount3`, absent on most targets; S8 §3.3).

`launch.json` schema (all paths are `/nix/store/…` paths inside the bundle):

```json
{
  "schema": "bob.appimage.launch/v1",
  "entry": "/nix/store/<h>-api/bin/api",
  "entries": { "curl": "/nix/store/<h>-curl-8.x/bin/curl" },
  "interp": "/nix/store/<h>-glibc-2.42/lib/ld-linux-aarch64.so.1",
  "libDirs": ["/nix/store/<h>-openssl-3.6.5/lib", "/nix/store/<h>-glibc-2.42/lib"],
  "env": ["SSL_CERT_FILE=${STORE}/<h>-nss-cacert/etc/ssl/certs/ca-bundle.crt",
          "GCONV_PATH=${STORE}/<h>-glibc-2.42/lib/gconv"],
  "mode": "auto",
  "hostGraphics": false
}
```

#### R-OUT-016 Launcher modes
- **Statement:** `BOB_APPIMAGE_MODE=auto|userns|ldso` (default from `launch.json.mode`).
  `userns`: new user + mount namespace mapping the caller's uid/gid, tmpfs root, bind every host
  top-level entry except `nix`, bind the bundled store at `/nix/store`, chroot, restore cwd,
  drop ambient caps, exec entry. `ldso`: exec bundled `interp --library-path <libDirs>[:<host LD_LIBRARY_PATH>] <entry>`
  with `${STORE}` = the extracted store. `auto` = userns, on any namespace setup error ldso.
  `BOB_APPIMAGE_DEBUG=1` prints the choice to stderr. Exit codes: 127 launcher error (message
  `bobrun: <text>`: `user namespace: <err>`, `unknown entry "<n>"`, `read launch.json: <err>`),
  child's code otherwise, 128+signal when the child is killed; SIGINT/TERM/HUP/QUIT forwarded.
- **Acceptance:** Matrix rows run in both modes; `BOB_APPIMAGE_MODE=userns` under
  `bwrap --disable-userns` → exit 127 with `bobrun: user namespace:`; `kill -TERM` of the
  AppImage process → child receives TERM, exit 143.
- **Source:** S8 §3.4, §5.5, S8-4.

#### R-OUT-017 Data paths
- **Statement:** The builder sets in `launch.json.env`: `SSL_CERT_FILE` (unless `NoCACerts`),
  `GCONV_PATH` (glibc in closure, unless `NoIconv`), `LOCALE_ARCHIVE` (when `Locales` contains
  anything other than `C.UTF-8`; built with nixpkgs `glibcLocales` restricted to the list),
  `TZDIR` when tzdata is in the closure. User `Env` entries override them.
- **Acceptance:** On every matrix distro: HTTPS 200, 0 locale warnings with
  `LANG=en_US.UTF-8` and `Locales: {"C.UTF-8","en_US.UTF-8"}`, `iconv` ä→ISO-8859-15 = `e4`.
- **Source:** S8 §3.5.

#### R-OUT-018 Supported hosts for AppImages
- **Statement:** Supported: glibc distros Ubuntu ≥ 22.04, Debian ≥ 12, Fedora ≥ 43 and musl
  Alpine ≥ 3.22, amd64 and arm64, kernel ≥ the bundled glibc's minimum (3.10). Ubuntu ≥ 23.10
  with the AppArmor userns restriction is served by ldso mode.
- **Acceptance:** Nightly harness runs the fixture bundles in unprivileged `bwrap` as `bobtest`
  on the 5 pinned rootfs images (digests of S8 §3.5) × {userns, nouserns} × {arm64 native,
  amd64 native worker}: `api` golden line, HTTPS 200, locale, `date`, `iconv`, all pass.
  Real Ubuntu 24.04 host with `kernel.apparmor_restrict_unprivileged_userns=1`: (manual) once
  per release.
- **Source:** S8 §3.5, §5.6.

#### R-OUT-019 Build-time fidelity warnings
- **Statement:** In `AutoMode`, the `appimage` builder warns when an entry is not an ELF
  executable (shebang script) or the closure contains `makeWrapper`-style wrappers (ELF or
  shell scripts that exec another `/nix/store` path): these fail in ldso mode.
- **Acceptance:** Bundle with a shell-script entry → `warning: //deploy:tools: entry "deploy" is
  a script; it cannot run on hosts without user namespaces. Set Mode: appimage.UsernsOnly to
  make this explicit.`; with `UsernsOnly` no warning, and in ldso-only hosts the launcher exits
  127 `bobrun: this bundle needs user namespaces`.
- **Source:** S8 §6 risks, ADR 0010 Consequences.

#### R-OUT-020 Host graphics (best effort)
- **Statement:** With `HostGraphics: true`, the launcher appends host library dirs
  (`/run/opengl-driver/lib`, `/usr/lib/<multiarch>`, `/usr/lib64`, `/usr/lib`, existing ones
  only) after the bundled ones (ldso: `--library-path`; userns: `LD_LIBRARY_PATH`). Bundles
  always carry the pin's recent glibc, never an older one.
- **Acceptance:** Fedora 43 rootfs with `mesa-vulkan-drivers`: bundled `vulkaninfo` with
  `HostGraphics: true` reports `llvmpipe` in both modes. Real GPUs: (manual).
- **Source:** S8 §3.6, S8-5.

## 4.7 Tarballs

#### R-OUT-004 Tarballs
- **Statement:** `tarball.Tarball` produces `<name>-<platform>.tar.gz` in one of two kinds:
  `Closure` (all closure paths under `nix/store/…`, plus `bob/roots` listing roots and
  `bob/licences.json`) or `Static` (one statically linked executable at `bin/<name>`;
  evaluation error if the program's closure has more than its own path). Same tar/gzip rules as
  R-OUT-010.
- **Acceptance:** Static tarball of a pure-Go fixture binary contains exactly `bin/<name>` and
  `bob/licences.json`; a cgo binary as `Static` → `✗ //cmd/api:api is not a static binary (it
  needs glibc-2.42, openssl-3.6.5); use tarball.Closure`. Two builds byte-identical.
- **Source:** R-OUT-004, D43.

```go
package tarball // pkg/output/tarball

type Kind int

const (
	Closure Kind = iota // whole closure under nix/store
	Static              // a single static executable
)

type TarballOpts struct {
	Program  bob.Executable
	Kind     Kind
	Platform bob.Platform // default host
}

// New declares a tarball. Example:
//
//	var Tar = bob.Define(func(p *bob.P) *tarball.Tarball {
//		return tarball.New(p, "cli", tarball.TarballOpts{Program: cli.Binary(p), Kind: tarball.Static})
//	})
func New(p *bob.P, name string, opts TarballOpts) *Tarball
```

## 4.8 Releases as built objects

#### R-OUT-022 Release definition
- **Statement:** A release is declared in `bob.go` with `release.New` and lists its artifacts;
  building `//path:release` (with a version) builds every artifact and a release manifest
  (R-OUT-023). The version enters **only** the manifest, OCI tags, the OCI label
  `org.opencontainers.image.version` and file names, never the artifacts' package derivations,
  so an unchanged program is byte-identical across versions.
- **Acceptance:** Building the fixture release as `1.0.0` and `1.0.1` without source changes:
  identical closure store paths, identical AppImage bytes, identical OCI layer blobs; only the
  manifest, image config/manifests/index differ. `bob build //rel:api` without `--version` uses
  version `0.0.0-dev` and prints `note: no version given; using 0.0.0-dev`.
- **Source:** R-REG-004, R-FW-003, R-FW-040, principle 4.

```go
package release // pkg/release

// Release is a versioned set of artifacts. Publishing (immutability, visibility, promotion)
// happens on the server (§06); this type only describes and builds it.
type Release struct{ /* unexported */ }

type ReleaseOpts struct {
	Public    bool          // installable and downloadable without authentication
	Closures  []bob.Executable // installable with bob install; built for each of Platforms
	Images    []*oci.Image
	AppImages []*appimage.Bundle
	Tarballs  []*tarball.Tarball
	Platforms []bob.Platform // default {LinuxAMD64, LinuxARM64}, applies to Closures
	Notes     bob.Source     // optional Markdown file shown in the UI
}

// New declares a release. The release name is the label's name; artifact names must be
// unique within it. Example:
//
//	var API = bob.Define(func(p *bob.P) *release.Release {
//		return release.New(p, "api", release.ReleaseOpts{
//			Public:    true,
//			Closures:  []bob.Executable{api.Binary(p)},
//			Images:    []*oci.Image{deploy.Image(p)},
//			AppImages: []*appimage.Bundle{deploy.Bundle(p)},
//		})
//	})
func New(p *bob.P, name string, opts ReleaseOpts) *Release
```

#### R-OUT-023 Release manifest `bob.release/v1`
- **Statement:** The release derivation's output is `release.json` (schema below; keys sorted;
  arrays sorted by `name`, then `platform`), consumed by §06 (publish) and `bob install`.
- **Acceptance:** Golden `release.json` of the fixture; JSON-schema test (the schema file lives
  in `pkg/release/schema/release-v1.json`); `version` matches
  `^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$` (else evaluation error
  `✗ invalid release version "<v>"`).
- **Source:** R-REG-004, R-INST-001.

| Field | Type | Meaning |
|---|---|---|
| `schema` | `"bob.release/v1"` | format version |
| `project` | string | server project id from `bob.go` module config (§01); `""` when built without a server |
| `release` | string | release name (label name) |
| `label` | string | `//rel:api` |
| `version` | string | given version |
| `public` | bool | visibility request; the server enforces it |
| `artifacts[]` | object | one per artifact and platform |
| `artifacts[].name` | string | artifact name (package name) |
| `artifacts[].kind` | `closure` \| `oci` \| `appimage` \| `tarball` | |
| `artifacts[].platform` | `linux-amd64` \| `linux-arm64` \| `multi` | `multi` only for `oci` |
| `artifacts[].storePath` | string | the artifact's output path |
| `artifacts[].entrypoints` | map name→relative path | `closure` only: executables installed by `bob install` (default `{<name>: <MainProgram>}`; all of `bin/*` when `MainProgram` is empty) |
| `artifacts[].file` | string | `appimage`/`tarball`: file name, e.g. `api-1.0.1-x86_64.AppImage` |
| `artifacts[].sha256`, `size` | string, int | `appimage`/`tarball`: of the file |
| `artifacts[].repo`, `tags`, `index` | string, []string, string | `oci`: repo, `[<version>]`, index digest |

```json
{
  "artifacts": [
    {"entrypoints": {"api": "bin/api"}, "kind": "closure", "name": "api", "platform": "linux-amd64",
     "storePath": "/nix/store/biszb9qipkwb6z81yh4745icllxq2kzk-cmd-api-api-linux-amd64"},
    {"file": "api-1.0.1-x86_64.AppImage", "kind": "appimage", "name": "bundle", "platform": "linux-amd64",
     "sha256": "0e5a4e01…", "size": 10848776, "storePath": "/nix/store/…-bundle-linux-amd64"},
    {"index": "sha256:40e73051…", "kind": "oci", "name": "image", "platform": "multi",
     "repo": "acme/api", "storePath": "/nix/store/…-image", "tags": ["1.0.1"]}
  ],
  "label": "//rel:api", "project": "acme", "public": true, "release": "api",
  "schema": "bob.release/v1", "version": "1.0.1"
}
```

## 4.9 Installing releases (consumer side)

CLI syntax and output samples are §02; the server endpoints are §06. This section fixes the
semantics and the on-disk profile.

R-INST-001…004 (install, upgrade, rollback, uninstall, list; no system Nix; authentication;
several registries) are specified once, in §02 §02.8, which also owns the file layout. This
section fixes what an installable release is (§4.8: the `closure` artifacts and entry binaries
of the `bob.release/v1` manifest) and the profile semantics below.

#### R-INST-005 Profile semantics
- **Statement:** Installed software forms profile generations in the layout of §02.8 Files
  (`$XDG_DATA_HOME/bob/install/`): `generations/<n>.json` is create-only (temp file +
  `rename`), `current` is a symlink replaced atomically, and every store path of each of the
  last 5 generations has a GC root (`<home>/nix/var/nix/gcroots/bob-install/<n>`); older
  generations lose their roots. Shims are `0755` POSIX sh files in `binDir` (default
  `~/.local/bin`) with the text of §02.8 step 5 (`bob exec --installed <name> -- <bin>`).
- **Acceptance:** Golden `generations/1.json` and shim text (§02.8); an existing non-bob file at
  the shim path → `BOB-E-0004` (`~/.local/bin/api exists and was not created by bob; remove it or
  use --as`), nothing changes; after 7 installs exactly generations 3–7 have GC roots.
- **Source:** R-INST-001, R-NFR-007; aligned with §02.8 at integration.

## 4.10 Output budgets **[proposal]**

#### R-OUT-024 Output build-time budgets
- **Statement / Acceptance** (fixture `api`, toolchains and closure present; quiet:
  `docs/spikes/measure/quiet/s08-bundles.log` for [8c/15G], S8 §3.7 for [2c/4G]; budget =
  measurement × 1.5 rounded):
  - OCI index cold (per-path layers): ≤ 3 s [8c/15G] (1.91 s), ≤ 4 s [2c/4G] (2.3 s).
  - OCI incremental (one root changed): ≤ 1 s [8c/15G] (0.40 s), ≤ 1 s [2c/4G] (0.57 s).
  - AppImage `api` with default prune: ≤ 5 s [8c/15G] (3.00 s), ≤ 18 s [2c/4G] (11.6 s,
    busy host). 100 MiB closure ≤ 30 s [2c/4G] at zstd-19; if the budget fails, the default
    level becomes 15 (decision recorded, not a test change).
  - AppImage start: first run (extract) ≤ 0.5 s, cached ≤ 0.1 s [8c/15G].
- **Source:** S8 §3.7, §5.7. The per-layer derivation design (R-OUT-011) adds one derivation
  per store path; re-measure in Stage 3 and keep the budgets.

#### R-OUT-025 Output size budgets
- **Statement / Acceptance:**
  - Fixture `api` AppImage, default prune: ≤ 12 MB (measured 10.85 MB); without prune ≤ 23 MB
    (21.2 MB).
  - Fixture `api` OCI arm64 image compressed: ≤ 26 MB (25.4 MB); v1→v2 new bytes ≤ 16 %.
  - Native `api` closure after fixup ≤ 74 MiB, ≤ 12 paths (§01 R-NIX-030 is the single source:
    75.9 MiB measured without fixup, S8 §3.1; strip of `api` −2.5 MiB and dropping `itoa-src`
    give 73.4 MiB) and must not contain the full `gcc-*` path (only `gcc-*-lib`); cross
    `@linux-amd64` with default libs ≤ 112 MiB (113.8 MB = 108.5 MiB, S7). Aligned at integration.
  - The bob AppImage runtime binary ≤ 3 MiB per platform (launcher prototype 2.3 MiB).
- **Source:** S8 §3.1, §3.2; S7 §3.2; STATUS (S7 366 MB closure finding).

## 4.11 Error messages (this section)

| Condition | Text (stderr, after `✗ `) | Code (§02) | Exit |
|---|---|---|---|
| unknown platform | `unknown platform "<p>" (known: linux-amd64, linux-arm64)` | 0001 | 2 |
| unsupported host architecture | `unsupported host architecture "<m>" (…)` | 0008 | 2 |
| package restricted | `<label> does not support <p> (it allows: <list>)` + chain | 0006 | 2 |
| unknown variant token | `unknown variant "<t>" (known: release, debug, asan, ubsan, race, <named…>)` | 0001 | 2 |
| conflicting profiles | `variants "release" and "debug" conflict: both set the profile` | 0007 | 2 |
| `race` with `asan` on Go | `variants "race" and "asan" conflict: Go cannot combine them (in //…)` | 0007 | 2 |
| pruned needed library | R-OUT-007 | 2015 | 1 |
| non-static tarball | R-OUT-004 | 1022 | 3 |
| OCI `User` root implicit | n/a (default is 65534; `"0:0"` allowed) | – | – |
| `MaxLayers` out of range | `//…: invalid MaxLayers: must be between 2 and 125 (got <n>)` | 1020 | 3 |
| too many roots for MaxLayers | `//…: invalid MaxLayers: <n> roots need MaxLayers ≥ <n+2>` | 1020 | 3 |
| nothing installable in a release | §02 R-INST-002 | 0009 | 2 |

Exit statuses follow §02.2 (aligned at integration: option errors in `bob.go` are definition
errors, exit 3).

## Open points

- **[open] O-04-1 Version stamping into binaries.** Recommendation: `golang.BinaryOpts.Stamp`
  (`-X` ldflags from `release.Version(p)`) affecting only the Go link derivation of the release
  build, so the cache is shared up to the link step; C/Rust: a generated `version.h` /
  `env!("BOB_VERSION")` only for the final binary. Not in v1 unless pipelines need it.
- **[open] O-04-2 AppImage runtime delivery.** Recommendation: bob embeds prebuilt static runtime
  binaries for both platforms (built by bob's own release, R-ARCH-005) and adds them with
  `nix store add` (content-addressed, no compile on first bundle). Alternative: build from
  embedded Go source in a derivation (+~10–20 s first bundle, no binary blobs in bob).
- **[open] O-04-3 squashfs reader in the runtime.** The runtime needs a squashfs reader with zstd.
  Recommendation: own minimal reader (read-only, ~600 lines) + `github.com/klauspost/compress/zstd`
  (BSD-3). A third-party Go squashfs library needs a licence and correctness check first.
- **[open] O-04-4 Fleet-wide popularity for layer order.** Recommendation: no (closure-local
  popularity in v1, deterministic per image); revisit with registry dedupe measurements.
- **[open] O-04-5 OCaml cross.** Recommendation: native only in v1 (R-PLAT-005), cross on the
  roadmap.

## Escalations

None. (No owner decision conflicts with this section. D61 only affects how §06 serves release
files; D56/D53 affect where foreign-arch tests run, which §05 schedules per R-FW-039.)

## Verifications run while drafting

- `unsafeDiscardReferences` (R-OUT-011): `__structuredAttrs = true`, Nix 2.34.8 (drafting, with
  the feature flag) and re-run at integration on 2.34.8 and 2.34.9 **without** any
  `discard-references` flag (same result; the flag only produces `warning: unknown experimental
  feature 'discard-references'`; scratch `/root/scratch-specint/dr/`). Output containing an input's store path: with
  `unsafeDiscardReferences.out = true` → `nix-store -q --references` empty; without → the input
  is registered. Scratch: `/root/scratch-spec04/t.nix`, `b.sh`.
- Budgets read from `docs/spikes/measure/quiet/s07-cross.log` and `s08-bundles.log`.
