# Spike S3 — Rust: crates, build scripts, proc-macros, resolution, rust-analyzer

Status: **done (indicative numbers)**, 2026-10-20, host `vibe-engine` (see `docs/environment.md`).
Code: `spikes/s03-rust/` (throwaway, module `bobspike/s03`, on a copy of backend B's `drv/`,
`nixidx/`, `run/`). ADR: `docs/adr/0005-rust-crates.md` (Proposed).

Timings were taken while other spikes (among them a GitLab instance from S16) ran on the host.
They are indicative. `spikes/s03-rust/measure.sh` reruns them on a quiet machine.

## 1. Question and requirements

How does bob build Rust without Cargo as the build driver: per-crate derivations, build
scripts, proc-macros, crates.io resolution, rust-analyzer, linking with C and Go, and at what
cost? Requirements: R-RS-001…004, R-FW-004/006/020, R-CC-001, R-NFR-001/002/008.

## 2. What was built

| Part | Path | What it does |
|---|---|---|
| semver + requirements | `crates/semver.go` | Cargo requirement syntax (caret, tilde, `=`, ranges, wildcards, pre-release rule), compat classes |
| sparse index client | `crates/index.go` | `index.crates.io` with on-disk cache + ETag revalidation, parallel prefetch, size caps |
| resolver | `crates/resolve.go`, `features.go` | Cargo-lockfile-compatible resolution in Go (fixpoint over semver classes and features) |
| crate metadata | `crates/meta.go`, `nar.go` | downloads `.crate`, checks sha256, reads 4 facts from Cargo.toml, computes the NAR hash of the unpacked source |
| lock formats | `crates/lock.go` | bob.lock (JSON in the spike) and Cargo.lock v4 writer |
| build plan | `crates/plan.go`, `cfg.go` | resolver-2 feature unification per (crate, platform), host/target units, cfg() evaluation, `-C metadata` hash |
| derivations | `rustgen/gen.go` | source FOD, compile, build-script compile, build-script run; C library, C program, cgo binary |
| builder | `cmd/bob-builder/` + `bspec/` | static Go builder of every Rust derivation: `fetch-crate`, `rustc`, `build-run` (directive parser), `cc`, `go` |
| driver | `cmd/bob-rs/` | `lock`, `build`/`drvs`/`outs`, `cargo` (Cargo.toml, Cargo.lock, vendor dir), `ide` (rust-project.json) |
| workspaces | `ws/demo`, `ws/native`, `ws/chain`, `ws/resolve/*` | stand-ins for `bob.go` (JSON), see below |

`ws/demo/ws.json` is the main graph: a binary `demo` using serde (derive), serde_json, regex,
anyhow, thiserror, clap (derive), tokio (rt, macros, time), log, zstd (C code through
zstd-sys + `cc`), libc, a local proc-macro `mymacro` (syn/quote/proc-macro2) and a local lib
`corelib` with a build script (cfg, rustc-env, generated file in OUT_DIR) and features
(`dep:hex` optional). The same workspace has `rcore` (staticlib, the fixture's), `rdyn`
(cdylib), `usec` (Rust bin linking a bob C library), a C program `cmain` linking `rdyn`, and a
cgo Go binary `gapi` linking `rcore`.

## 3. Results

### 3.1 Builds that work (all verified by running the output, under `env -i`)

| Target | Units / drvs | Output check |
|---|---|---|
| `demo` (49 units: 45 crates.io + 3 local + 1 host copy) | 120 drvs: 45 fetch, 49 compile, 13 build-script compile, 13 build-script run | prints JSON round-trip, regex match, zstd 650→29 bytes, `Point has 2 fields` (local derive), `corelib=7 fast build.rs target=aarch64-unknown-linux-gnu` (build-script cfg, env, OUT_DIR file), clap `--help` |
| web graph `ws/resolve/web` (tokio full, axum, reqwest+rustls+**ring**, tracing-subscriber, clap) | 129 units, 285 drvs | builds and runs (empty main); ring's cc+asm build script works |
| `ws/native` (openssl 0.10 → openssl-sys via **pkg-config + nixpkgs openssl**) | 18 units, 45 drvs | `OpenSSL 3.6.5 … sha256(bob)=81b637d8…` (same digest as the fixture); openssl's build script gets `DEP_OPENSSL_VERSION_NUMBER` from openssl-sys and emits `ossl300…ossl350` cfgs |
| `usec`: Rust bin + bob C static lib | 2 drvs | `cfoo_mul(6,7)=42` |
| `cmain`: C program + Rust cdylib | 2 drvs | `rdyn_add(40,2)=42`; RUNPATH has the rdyn store path (cc-wrapper) |
| `gapi`: cgo binary + Rust staticlib (fixture rcore + itoa + hex) | 6 drvs | `rcore_num_hex(1234)=31323334` |

Closure of `demo`: 60.9 MiB, only glibc, libgcc, gcc-lib, libidn2, libunistring. No crate
source and no build path is referenced (`--remap-path-prefix`, `-C strip=debuginfo`). B's
fixture `api` had 75.9 MiB because it referenced the itoa source.

### 3.2 Resolver vs Cargo (`ws/resolve/compare.sh`)

bob's Go resolver against `cargo generate-lockfile` (cargo 1.95.0) on the same generated
Cargo.toml, live crates.io, index cache warm. Times [8c/15G], network-bound.

| Workspace | Packages in Cargo.lock | Cargo.lock bob vs cargo | bob | cargo |
|---|---|---|---|---|
| serde_json | 12 | identical | 0.15 s | 0.19 s |
| tokio (full) | 25 | identical | 0.24 s | 0.26 s |
| web (tokio, axum, reqwest/rustls, tracing, serde, clap, anyhow) | 173 | identical | 0.93 s | 0.98 s |
| big (+ sqlx postgres/sqlite, tonic, prost, opentelemetry, chrono, uuid, rand, image) | 393 | identical | 1.91 s | 2.17 s |
| demo | 56 | identical | 0.41 s | 0.71 s |

Offline (index files cached, no HTTP): web 0.67 s / 88 MB, big 0.99 s / 123 MB peak RSS.
`lock` with metadata (downloads every .crate once, reads Cargo.toml, computes NAR hash): big
graph 3.2 s total (1.2 s metadata).

Two findings came from diffing against Cargo, not from documentation:

- **Weak features lock the dependency.** Cargo's lockfile resolver treats `dep?/feat` like
  `dep/feat` (tracing-core `valuable?/std` puts `valuable` into Cargo.lock; reqwest
  `quinn?/ring` puts quinn in). The build-time feature resolver does not. bob's `Expand` has
  a `lockMode` switch for exactly this.
- **Prefetch only active edges.** Prefetching the index files of all declared dependencies
  (incl. optional and dev) cost 4× more requests and made `big` 15 s instead of 1.9 s.

### 3.3 Build-script contract (implemented in `cmd/bob-builder`)

The build-script run gets: `OUT_DIR` (= `$out/out-dir`), `CARGO_MANIFEST_DIR` (the unpacked
source in the store), `CARGO_MANIFEST_PATH`, `CARGO_MANIFEST_LINKS`, `CARGO_PKG_*`,
`CARGO_FEATURE_<F>`, `CARGO_CFG_<KEY>` (from bob's cfg table; release: no
`CARGO_CFG_DEBUG_ASSERTIONS`), `TARGET`, `HOST`, `PROFILE=release`, `OPT_LEVEL`, `DEBUG=false`,
`NUM_JOBS` (= `NIX_BUILD_CORES`), `RUSTC`, `RUSTDOC`, `CC=cc`, `CXX=c++`, `AR=ar`,
`CARGO_ENCODED_RUSTFLAGS=""`, `HOME=$TMPDIR`, `DEP_<LINKS>_<KEY>` for direct dependencies
with `links`, and for crates declared with native packages `PKG_CONFIG_PATH` + their `bin/`.
PATH: rustc, gcc-wrapper, binutils-wrapper, coreutils. cwd = source dir.

Directives parsed (old `cargo:` and new `cargo::` syntax): `rustc-cfg`, `rustc-check-cfg`,
`rustc-env`, `rustc-link-lib`, `rustc-link-search`, `rustc-flags` (-l/-L only),
`rustc-link-arg`, `-arg-bins`, `-arg-cdylib`/`cdylib-link-arg`, `warning`, `error` (fails
the derivation), `metadata` and old-style `cargo:KEY=VALUE` (links metadata).
`rerun-if-*` are ignored (Nix rebuilds on input change). Per-target link args of tests,
examples, benches and named bins are ignored for registry crates. Unknown new-syntax
directives are recorded as `unsupported`.

The consumer side: the crate's own compile reads `directives.json` (cfgs, env, `-l`, `-L`).
Every linking unit (bin, cdylib, staticlib, proc-macro) adds `-L` of all transitive
dependencies' build-script outputs. Seen in practice: zstd-sys and ring emit
`static=` libs into OUT_DIR (bundled into the rlib by rustc); openssl-sys emits a dylib `-L`
into nixpkgs openssl, which the cc-wrapper turns into RUNPATH.

**The source must be a stable store path.** zstd-sys exports
`include=/nix/store/…-zstd-sys-…/zstd/lib` as links metadata. If the source were unpacked into
`$TMPDIR` of the run, dependents would get a dangling path. So every registry crate has one
unpacked source derivation, which the build-script run, the compile and rust-analyzer share.

### 3.4 Derivation layout and fetching

- **Source:** a recursive fixed-output derivation per crate; `bob-builder fetch-crate`
  downloads `static.crates.io/crates/<n>/<n>-<v>.crate` (TLS via nixpkgs `cacert`), checks the
  `.crate` sha256 from bob.lock, unpacks without the top directory. Nix then checks the NAR hash
  bob computed at lock time in Go. bob's Go NAR hash matched `nix-prefetch-url --unpack` on 3
  crates and on every one of the 128+45+17 crates the builds fetched (a mismatch would have
  failed the FOD).
- `builtin:fetchurl` with `unpack = "1"` does **not** work for crates: it expects a NAR
  archive (`error: bad archive: input doesn't look like a Nix archive`, tried).
- **Compile:** `rustc --crate-name N --edition E --crate-type T root --emit=link
  -C opt-level=3|0 -C metadata=H -C extra-filename=-H (libs) -C embed-bitcode=no (libs)
  -C strip=debuginfo --cfg feature="…" --extern name=<store>/lib/lib<n>-H.rlib|.so
  -L dependency=<each transitive dep>/lib --cap-lints allow (registry) --remap-path-prefix`.
  Host units (proc-macros, build scripts and their dependencies) use opt-level 0, like Cargo's
  release `build-override`.
- `-C metadata` = first 16 hex of sha256(name, version, source checksum or "local",
  host/target, features, toolchain id, dependencies' hashes). It disambiguates two versions of
  one crate (demo has syn 2 and syn 3, getrandom 0.2 and 0.4 in other graphs).
- **Cross (prepares S7, unverified by building):** the plan for x86_64 from an aarch64 host
  splits the demo graph into 17 host and 32 target units; 2 crates are needed on both sides
  (`crates/crates_test.go: TestCrossPlan`). No proc-macro is planned for the target. Native
  builds give host and target units the same derivation when flags agree; they differ here
  only in opt-level.

### 3.5 rust-analyzer (rust-analyzer 2026-06-01 from nixpkgs, `analysis-stats`)

| Mode | What bob generates | Result on `demo` |
|---|---|---|
| **rust-project.json** (`bob-rs ide`) | crate graph from the plan; registry sources = store paths; local roots = working tree; cfgs incl. build-script cfgs; `OUT_DIR` + `rustc-env`; `proc_macro_dylib_path` = store dylibs; `sysroot` = nixpkgs rustc, `sysroot_src` = `rustPlatform.rustLibSrc` | 49 crates; 670 exprs, ??ty 5 (0 %), ?ty 22 (3 %); with `--disable-proc-macros` only 391 exprs → derives (clap, serde, local `Describe`) are expanded by RA's proc-macro-srv from the store dylibs. Load 2.5 s, total 12 s, ~0.9 GB [8c/15G] |
| **Cargo.toml + Cargo.lock + `.bob/vendor`** (`bob-rs cargo`) | workspace + one manifest per crate, Cargo.lock from bob.lock, `.cargo/config.toml` replacing crates-io with a directory source (symlinks into store sources + `.cargo-checksum.json` with `"files":{}`) | `cargo metadata --locked --offline`: 0.34 s, Cargo.lock unchanged. RA cargo mode loads; ??ty 6 (1 %); RA runs `cargo check` itself for build scripts (3.9 s, 5.9 MB `target/`) |

The vendor dir needs **all** locked crates, including Windows-only ones (6 of 53 in demo), or
cargo refuses to resolve offline. bob already downloads them at lock time.

rust-project.json is the better fit: it is exactly bob's graph, it needs no Cargo binary, no
second build in `target/`, and proc-macros come from the cache. Generated Cargo files are still
useful for other cargo-based tools (R-RS-003, R-FW-006 drift check).

### 3.6 Numbers (`measure.sh`)

| Measurement | [8c/15G] | [2c/4G] |
|---|---|---|
| demo cold | 65.43 s / 492 MB | 147.91 s / 482 MB |
| demo no-op | 1.52 s / 35 MB | 0.55 s / 36 MB |
| demo incr-leaf | 4.01 s / 264 MB | 4.80 s / 265 MB |
| demo incr-lib | 3.99 s / 270 MB | 5.64 s / 268 MB |
| cargo cold | 71.26 s / 350 MB | 123.88 s / 348 MB |
| chain30 cold (max-jobs 1) | 4.77 s / 132 MB | 3.91 s / 132 MB |
| plan+drvs demo (no build) | 0.46 s / 35 MB | |
| lock big (392 crates, offline index) | 2.45 s / 116 MB | |

Run 2026-10-20 22:21 UTC on the busy host (S16's GitLab was running). Profiles: [8c/15G]
max-jobs 8 / cores 8, cargo `-j8`; [2c/4G] cgroup 2 CPUs / 4 GiB, max-jobs 2 / cores 2, cargo
`-j2`. "cold" keeps fetched sources and toolchains; cargo cold uses the same vendored sources
and an empty `target/`. All numbers depend on host load; rerun on a quiet machine.

Reading the numbers:

- **Cold demo (49 units, 120 drvs): bob 65 s vs cargo 71 s at [8c/15G]; 148 s vs 124 s at
  [2c/4G].** On 8 cores parallel derivations make up for missing pipelining; on 2 cores the
  per-derivation cost and missing pipelining show (+19 %).
- **Per-derivation cost:** the 30-crate chain at max-jobs 1 takes 3.9–4.8 s, i.e. **0.13–0.16 s
  per derivation including a trivial rustc run** (rustc alone for such a crate is a few tens of
  ms [unverified]). That is in line with B's ~0.1 s.
- **Incremental:** a change in the binary's own source rebuilds 1 derivation (4.0 s / 4.8 s);
  a change in `corelib` (lib with build script) rebuilds its build-script compile, run,
  compile and `demo` (4.0 s / 5.6 s). Cargo's incremental numbers were not measured.
- **No-op:** 0.55–1.5 s (plan, nixpkgs index, source import, drv import, `nix build` no-op).
- **Evaluation:** plan + 120 derivations into the store 0.46 s / 35 MB. Peak RSS of any process
  stayed under 500 MB, far below the 4 GiB budget.

Earlier single runs on the busy host: demo cold 108.6 s (max-jobs 2, cores 4, incl. 45
downloads); web graph (129 units) cold 3 min 0 s (max-jobs 4, cores 4); IDE preparation for demo
(sources + 13 build-script runs + 4 proc-macros) 76 s cold.

### 3.7 Error output

A type error in a local crate (`tmp/fault`) is reported as:

```
✗ //c3:c3 failed
  │ error[E0308]: mismatched types
  │  --> c3-0.1.0/src/lib.rs:2:21
  │ 2 | pub fn g() -> u32 { "x" }
  │   |               ---   ^^^ expected `u32`, found `&str`
```

The path is the remapped `/rust/<name>-<version>` prefix. bob should remap local crates to
their repository path instead (`c3/src/lib.rs`), so editors can jump to it. Not done in the spike.

## 4. Answer

- **Per-crate derivations calling rustc directly work for real crates.io graphs**, including
  build scripts (13 in demo, ring, zstd-sys, openssl-sys/openssl with links metadata),
  proc-macros (crates.io and local) and all crate types (lib/rlib, proc-macro, bin, staticlib,
  cdylib). Cargo is not run at build time.
- **bob resolves crates itself in Go.** A ~900-line resolver (semver, sparse index, features)
  produced byte-identical Cargo.lock files to cargo on four graphs up to 393 packages, at the
  same speed. A Go resolver is feasible; it needs a regression corpus against cargo.
- **Build scripts run in their own derivation** under a fixed contract (§3.3/§5.3), executed by
  a static Go `bob-builder`; their outputs are read at build time by dependents.
- **rust-analyzer works best with a generated rust-project.json**; generated Cargo.toml/
  Cargo.lock + vendor dir also work for cargo-based tools.
- **Cost:** cold builds are on par with `cargo build --release` on 8 cores (65 s vs 71 s for 49
  crates) and 19 % slower on 2 cores (148 s vs 124 s); per derivation ≈ 0.15 s; incremental
  leaf change 4–5 s; no-op ≤ 1.5 s. Cached builds need no rustc at all.

## 5. SPEC input

### 5.1 API surface [proposal]

```go
package rust // github.com/megakuul/bob/pkg/rust

// Toolchain from nixpkgs (R-RS-004); default = nixpkgs rustc of the pinned revision.
func Toolchain(p *bob.P, opts ToolchainOpts) *Rust

// crates.io dependency; versions are Cargo requirement strings, resolved into bob.lock.
func Crate(name, req string, opts ...DepOpt) Dep
func Features(fs ...string) DepOpt   // enable features of the dependency
func NoDefaultFeatures() DepOpt
func Rename(codeName string) DepOpt  // `use codeName::…` when the crate name differs

// Local crates (one constructor per kind, R-FW-014). All return typed handles.
func Library(p *bob.P, o LibOpts) *Lib            // rlib; usable as Dep
func ProcMacro(p *bob.P, o LibOpts) *Lib          // host-compiled; usable as Dep
func Binary(p *bob.P, o BinOpts) *Bin
func StaticLib(p *bob.P, o LibOpts) *CLib         // implements cc.Linkable (C/cgo consumers)
func CDylib(p *bob.P, o LibOpts) *CLib            // implements cc.Linkable
func Test(p *bob.P, o TestOpts) *Test             // R-FW-024

type LibOpts struct {
	Name     string            // crate name, default: directory name
	Edition  Edition           // rust.Edition2021 …
	Root     string            // default src/lib.rs
	Deps     []Dep             // rust.Crate(...), *Lib handles
	BuildDeps []Dep            // build-dependencies (host)
	Features map[string][]string // this crate's feature table
	Enable   []string          // features enabled when built as a root
	BuildScript string         // "build.rs" or ""; runs in its own derivation
	Native   []cc.Linkable     // bob C/C++ libraries or nixpkgs packages to link
	Headers  string            // StaticLib/CDylib: include dir exported to C
}

// Native inputs for a crates.io crate's build script (pkg-config, system libraries):
func Uses(crate string, pkgs ...nixpkgs.Package) bob.Option // rust.Uses("openssl-sys", nixpkgs.Pkg("openssl"))
```

Example (`bob.go` next to the crate):

```go
func Demo(p *bob.P) *rust.Bin {
	return rust.Binary(p, rust.BinOpts{
		Edition: rust.Edition2021,
		Deps: []rust.Dep{
			rust.Crate("serde", "1", rust.Features("derive")),
			rust.Crate("tokio", "1", rust.Features("rt", "macros", "time")),
			rust.Crate("zstd", "0.13"),
			corelib.Lib(p), mymacro.Lib(p),
		},
	})
}
```

### 5.2 Derivation layout per crate unit [proposal]

| Derivation | Name | Builder job | Inputs | Output |
|---|---|---|---|---|
| source (registry) | `<name>-<version>` | `fetch-crate`, recursive FOD, hash = bob.lock `narHash` | bob-builder, cacert | unpacked crate |
| build-script compile | `<name>-<version>[-host]-build-script` | `rustc` crate-type bin, opt 0 | source, build-deps (host units) | `bin/build_script_build` |
| build-script run | `<name>-<version>[-host]-build-run` | `build-run` | source, script, `links` deps' runs, declared nixpkgs packages | `out-dir/`, `output`, `directives.json` |
| compile | `<name>-<version>[-host]` | `rustc` | source, deps' compile outputs, own run, transitive runs (linking types) | `lib/lib<n>-<H>.rlib`, `.so` (proc-macro), `lib/lib<n>.a`/`.so` + `include/` (staticlib/cdylib), `bin/<n>` |

Units are keyed by (crate, version, host|target, features). Toolchain inputs: nixpkgs `rustc`,
`stdenv.cc`, `stdenv.cc.bintools`, `coreutils`. The derivation env holds one JSON job
(`bobSpec`); bob-builder has no other inputs. A staticlib consumer should also get rustc's
`--print native-static-libs` list (not needed with glibc 2.42 in the spike; not implemented).

### 5.3 bob.lock entry for a crates.io crate [proposal]

```json
{"name": "zstd-sys", "version": "2.1.2+zstd.1.5.7",
 "checksum": "sha256-<base64 of the .crate sha256 = index cksum>",
 "narHash": "sha256-uQTfBw5nXEUrOTW0UY9fbKzVnYOU5x1IwZwZnw2J9D0=",
 "links": "zstd",
 "deps": [{"name": "cc", "crate": "cc@1.2.x", "kind": "build", "features": ["parallel"]},
          {"name": "pkg-config", "crate": "pkg-config@0.3.x", "kind": "build", "optional": true}],
 "features": {"default": ["legacy", "arrays", "zdict_builder"], "pkg-config": ["dep:pkg-config"]},
 "meta": {"edition": "2018", "build": "build.rs"}}
```

`deps[].crate` is the resolved `name@version`; optional deps that no feature of the workspace
can enable are omitted. `meta` keys: `edition`, `libName` (if ≠ name with `-`→`_`), `libPath`
(if ≠ `src/lib.rs`), `procMacro`, `build`. Size: 54 KB for 53 crates, 244 KB for 172 crates,
413 KB for 392 crates as indented JSON (65 KB gzip). A one-line-per-crate encoding would roughly
halve it [unverified].

### 5.4 Resolution and features [proposal]

- `bob lock`: resolve like Cargo's **lockfile** resolver: all features of local crates, all
  platforms, dev-dependencies of local crates, weak features count, one version per semver
  class (`1.x`, `0.y`, `0.0.z`), highest non-yanked match, prefer versions whose
  `rust_version` ≤ toolchain, at most one crate per `links` value. Then download each crate
  once, verify, record `narHash` and `meta`.
- Evaluation (every `bob build`): resolver-2 feature unification per (crate, platform), build
  deps and proc-macros on the host, cfg() filtering from bob's per-triple tables (a test
  compares them with `rustc --print cfg` of the pinned toolchain), dev-deps only for tests.
- Errors in bob terms, e.g. `✗ cannot resolve crates: app@0.1.0 depends on tokio 1: no version
  of tokio satisfies all of: ^1.40, =1.38.0`; `✗ serde_json@1.0.154 is not in bob.lock (run bob
  lock)`; `✗ crates.io:foo@1.2.3: download does not match bob.lock`.

### 5.5 Budgets [proposal]

| Budget | [8c/15G] | [2c/4G] | Basis |
|---|---|---|---|
| `bob lock` resolve, 400-crate graph, warm index cache | ≤ 3 s | ≤ 5 s | 1.9 s live, 1.0 s offline |
| plan + derivations for a 50-crate graph (no build) | ≤ 1 s | ≤ 2 s | §3.6 |
| no-op build, 50-crate graph | ≤ 1 s | ≤ 1.5 s | §3.6 |
| incremental, leaf crate change | ≤ rustc time + 2 s | ≤ rustc time + 3 s | §3.6 |
| per-derivation cost (trivial crate, incl. rustc) | ≤ 0.25 s | ≤ 0.3 s | §3.6 chain: 0.13–0.16 s |
| cold 50-crate graph vs `cargo build --release` | ≤ 1.1× | ≤ 1.3× | §3.6: 0.92× / 1.19× |
| resolver result | identical to `cargo generate-lockfile` on a ≥ 10-workspace corpus | | §3.2 |

## 6. Decisions, risks, open items

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S3-1 | Resolve in Go, never run Cargo. | Matched Cargo exactly on 4 graphs; bob already needs its own npm resolver; no Cargo binary needed (R-NIX-020). |
| S3-2 | Lock stores dependency edges, feature tables and 4 Cargo.toml facts per crate. | Evaluation must work offline and without sources, so a warm cache needs no downloads (R-DEV-006). |
| S3-3 | Features are resolved at evaluation, not locked. | Features are bob.go inputs and variants (R-FW-031); lock keeps pins only. |
| S3-4 | One unpacked source FOD per crate, hash = NAR hash computed by bob. | Build-script metadata points into sources; rust-analyzer uses the same paths; `builtin:fetchurl` cannot unpack tarballs. |
| S3-5 | Build-script compile and run are separate derivations. | Cross needs host compile + target run; directives become cacheable data. Costs 2 drvs per build script (13 of 49 units in demo). |
| S3-6 | A static Go bob-builder runs every Rust job (no bash). | Directive parsing and quoting are testable Go; matches ADR 0001's production plan. |
| S3-7 | Host units use opt-level 0. | Same as Cargo's release build-override; proc-macro-heavy graphs compile faster. |
| S3-8 | rust-analyzer gets rust-project.json; Cargo files are generated too. | rust-project.json is bob's real graph and needs no cargo; Cargo files satisfy R-RS-003 for other tools. |
| S3-9 | Registry crates get `--cap-lints allow`; local crates get `--check-cfg` for their features. | Same as Cargo. |

### Owner decisions needed

**O-S3-1: What may bob.lock contain for Rust crates?** R-FW-004 says "only resolved pins and
hashes".
- **a)** Store edges, feature tables and 4 Cargo.toml facts in bob.lock (spike format; 400
  crates ≈ 400 KB JSON). Evaluation is offline and needs no sources. **Recommended.**
- **b)** bob.lock holds only name/version/checksum/narHash; the rest goes into a second
  generated, committed file (`bob.lock.d/crates.json`). Same behaviour, smaller main lock, two
  files to keep consistent.
- **c)** bob.lock minimal; bob fetches index entries and crate manifests at evaluation time
  (cached). Needs network on fresh machines and CI workers before any cache hit; not
  recommended.

**O-S3-2: Who provides `cargo` for tooling?**
- **a)** bob generates rust-project.json for rust-analyzer and also generated Cargo files +
  vendor dir; `cargo` itself is only offered in `bob dev` shells for developers who want it.
  **Recommended.**
- **b)** Cargo files only; rust-analyzer runs in cargo mode (needs cargo in every dev shell,
  builds build scripts a second time in `target/`).
- **c)** rust-project.json only; drop generated Cargo files (would need R-RS-003 reworded).

### Risks

- **bob-builder is an input of every Rust derivation.** Reformatting its source changed its
  binary (line tables) and rebuilt all 120 demo derivations. bob-builder needs its own
  version, rarely changed, and pinned independently of the `bob` CLI.
- **Cargo semantics drift.** Lockfile rules (weak features, MSRV fallback, a resolver v3/v4)
  change with Cargo releases. Mitigation: corpus test against `cargo generate-lockfile` in CI.
  Not tested: yanked versions already in a lock, `[patch]`, git dependencies, alternative
  registries, `links` conflicts in the wild, crates with `build = false` + `build.rs`.
- **Build scripts that assume Cargo** (call `cargo`, read `CARGO_TARGET_DIR`, write outside
  OUT_DIR, download things) will fail in the sandbox. None of the 13+14+5 build scripts tried
  did. `-sys` crates need a `rust.Uses` declaration; a quirks table like npm's (R-NPM-005) may
  be needed for common ones (openssl-sys, libz-sys, …).
- **No pipelining, no early cutoff:** cold builds on 2 cores are 19 % slower than cargo (§3.6);
  a change in a widely used local crate rebuilds all dependents.
- **cfg tables are hard-coded** per target triple; they must match the toolchain (test).

### Still to verify

- Cross-compilation build (planned split exists; S7 builds it), and `--target` + linker setup.
- `rust.Test` derivations (`--test` harness, dev-dependencies) — planned, not built.
- `native-static-libs` propagation for staticlib consumers on non-glibc-2.34+ targets.
- Remapping local crate paths to repository paths in diagnostics (§3.7).
- Quiet-machine numbers: rerun `measure.sh`.
- Large crates with `include!` of files outside the crate, workspace inheritance in published
  manifests (published crates are normalised, so expected fine).
