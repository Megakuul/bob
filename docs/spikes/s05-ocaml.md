# Spike S5 — OCaml / opam in bob

Status: **done (indicative numbers)**, 2026-10-21, host `vibe-engine` (`docs/environment.md`).
Code: `spikes/s05-ocaml/` (throwaway). ADR: `docs/adr/0007-ocaml-opam.md` (Proposed).
Numbers were taken while other spikes were running; rerun `spikes/s05-ocaml/measure.sh` and
`spikes/s05-ocaml/resolve/compare.sh` on a quiet machine for final values.

## 1. Question and requirements

How does bob build OCaml without opam and dune as drivers: where do opam packages come from,
how are they resolved and built, how is bob's own code compiled, how do C stubs, native
executables and editors (ocaml-lsp/merlin) work, and at what cost? Requirements: R-ML-001, the
§7 rules (bob replaces the native build system, generates files for tooling), R-FW-006,
R-NFR-001/008, ADR 0002 Decision 5 (`.bob/bin` shims).

## 2. What was built

| Part | Path | What it does |
|---|---|---|
| opam file parser | `opam/parse.go` | opam 2.0 grammar: fields, sections, strings/triple strings, idents, lists, groups, `{}` options, `& \| ! ?`, relops, env ops |
| filters + versions | `opam/eval.go` | Debian/opam version order (`~`), filter evaluation with undefined, `%{var}%`/`%{v?a:b}%`, dependency formulas → CNF of conjunctive alternatives, flag filters drop atoms like opam |
| resolver | `opam/resolve.go` | toolchain packages fixed to nixpkgs versions, `available`, `conflicts`, static pruning fixpoint, newest-first backtracking, `depopts` as build edges |
| generator | `ocamlgen/gen.go` | per opam package: source FOD + build derivation running the opam `build:`/`install:` commands, then `opam-installer`; conf-* → nixpkgs table |
| own code | `ocamlgen/local.go` | C library, OCaml library (direct: `ocamldep -sort` + `ocamlfind ocamlopt`, dune-style wrapping; or dune mode), native executable, reference scrubbing |
| driver | `cmd/bob-ml/` | `parseall`, `resolve`, `latest`, `lock`, `build`/`drvs`/`outs` (`-mode direct\|dune`), `ide` (`-style merlin\|dune`) |
| workspaces | `ws/demo`, `ws/web`, `ws/err` | stand-ins for `bob.go` (JSON) |
| checks | `resolve/compare.sh`, `resolve/coverage.py`, `ide-check.sh`, `lsp-check.py`, `measure.sh` | |

Backend: B (ADR 0001). `drv/`, `nixidx/`, `run/` are copies from `spikes/s03-rust` (B with
S3's tweaks); `drv.FixedOutputPath` was extended to sha512 (opam checksums are mostly sha512).
Builders are bash scripts generated in Go (as in B), no stdenv. opam-repository pinned at
`ocaml/opam-repository@af5ddb0addf6e75660ee9d7485b27425459d1d66` (4 MB tarball, 4 684
packages, 19 415 opam files).

`ws/demo`: app `demo` (cmdliner, yojson, lwt.unix, fmt, local lib `geom`); `geom` (fmt, 3
modules, C stub `geom_stubs.c` calling bob C library `cfoo`). Opam roots `cmdliner fmt yojson
lwt` → 11 opam packages, including topkg/ocamlbuild (non-dune builds), cmdliner (Makefile),
dune-configurator, cppo, ocplib-endian. `ws/web`: `cohttp-lwt-unix` (60 packages: ppxlib,
zarith with gmp, mirage-crypto with C, conf-gmp, conf-pkg-config) plus an executable using
zarith, uri and cohttp.

## 3. Results

### 3.1 Builds that work (verified by running the output under `env -i`)

| Target | Mode | Result |
|---|---|---|
| all 11 opam packages of `ws/demo` | opam commands in derivation | built; fmt has its optional `fmt.cli`/`fmt.tty` because cmdliner (depopt) is in the solution |
| `demo` native exe (C stub → bob C lib `cfoo`, lwt, yojson, cmdliner, fmt) | direct | prints `{"x":6,"y":3,"hash":189}` and `vec (6, 3)`; 189 = cfoo_mix(6,3) → stub works |
| same | dune | same output |
| all 60 opam packages of `cohttp-lwt-unix` | opam commands | built in 61 s [8c/15G] cold incl. downloads, first try after one fix (conf-* skip) |
| `web` exe (zarith + gmp, uri, cohttp) | direct | prints 2^100 and a URI; `NEEDED libgmp.so.10` resolves under `env -i` |

Runtime closure of `demo`: before scrubbing 17 paths / 693 MB (the exe embeds the OCaml
stdlib path; cc-wrapper `-L` adds gcc and glibc-dev); after scrubbing those hashes 5 paths /
55 MB (glibc, libgcc, libidn2, libunistring, demo).

### 3.2 Resolver vs opam (`resolve/compare.sh`)

Both with OCaml 5.4.1, dune 3.21.1, ocamlfind 1.9.8 fixed (opam: faked installs in an empty
switch, `opam install --dry-run`). opam 2.5.1 from nixpkgs.

| Roots | bob | opam | Result |
|---|---|---|---|
| cmdliner fmt yojson lwt | 11 | 11 | identical |
| ppx_deriving ppx_deriving_yojson logs | 11 | 11 | identical |
| cohttp-lwt-unix | 60 | 60 | identical |
| core core_unix | 70 | 70 | identical |
| irmin-fs | 36 | 36 | identical |
| eio_main | 21 | 21 | identical |
| dream | 109 | 90 | differs: bob takes newest caqti 2.3.2 / h2 0.13 (pulls more deps), opam older ones |
| ocaml-lsp-server | 32 | 30 | differs: bob takes lsp 1.27 + dune-rpc 3.21.1, opam lsp 1.25 + dune-rpc 3.20.2 |

- bob resolves in 0.01–0.15 s, opam in 7–10 s (opam loads the whole repository) [8c/15G].
- The differences are valid solutions with different criteria: bob is greedy newest-first per
  package, opam optimises globally (its default criteria also minimise the number of packages).
  bob's results were not built for `dream`/`ocaml-lsp-server`; validity is inferred from the
  resolver checks only.
- Without the dune pin, opam would upgrade dune to 3.24.2 (dune-configurator follows). bob must
  keep the toolchain fixed (it comes from nixpkgs), so it pins `dune` like `ocaml`.
- Plain depth-first backtracking did not finish on 5 of 8 graphs (> 200 000 steps). Two changes
  made all 8 finish in ≤ 478 steps: a **static pruning fixpoint** (a version is dead if it is
  unavailable or one of its dependency clauses has no live candidate — e.g. everything needing
  dune ≥ 3.22) and breadth-first goal order. A conflict-driven solver is the robust fix for
  production.
- Parser coverage: 19 405 of 19 415 opam files parse and normalise (1.5 s for all); the 10
  failures are exotic formulas (`js_of_ocaml-ocamlbuild` 3.x, `ocaml-option-32bit`).

### 3.3 nixpkgs `ocamlPackages` vs opam repository (`resolve/coverage.py`)

| Set | packages | in `ocamlPackages` (not broken) | same version |
|---|---|---|---|
| whole opam repository (latest versions) | 4 684 | 1 045 (22 %) | 588 (12 %) |
| demo graph | 11 | 7 | 7 |
| cohttp-lwt-unix | 60 | 47 | 31 |
| dream | 109 | 92 | 61 |
| ocaml-lsp-server | 32 | 28 | 19 |

nixpkgs has one version per package and 57 broken entries among its 1 196 derivations. Mapping
to nixpkgs would cover none of the four graphs completely and would not let the user pick
versions. Building from the opam repository covered every package the spike tried. **Decision:
opam repository + bob resolver; nixpkgs only for the toolchain and conf-* system libraries.**

### 3.4 Building opam packages: opam commands vs `dune build -p` vs bypassing dune

- 4 of the 11 demo packages do not use dune: topkg and fmt (`ocaml pkg/pkg.ml build`),
  ocamlbuild (Makefile), cmdliner (Makefile + `install:`). A `dune build -p` derivation alone
  would not build them. **Running the package's own opam `build:`/`install:` commands**
  (rendered in Go: variables substituted, filters applied, `jobs` = `$NIX_BUILD_CORES`) and then
  `opam-installer` on `<name>.install` built every package of both graphs (11 and 60 packages) with no
  per-package code. For dune packages that is exactly `dune build -p <name> -j N @install`, so
  bob drives dune per package reliably; dune's own build log appears in the derivation log.
- Needed environment (no stdenv, no setup hooks): `OCAMLPATH` = closure of dep outputs `/lib`
  plus findlib's `site-lib` (provides `bytes` etc.; without it ocplib-endian fails `Library
  "bytes" not found`), `OCAMLFIND_DESTDIR=$out/lib`, `OCAMLTOP_INCLUDE_PATH` = findlib's
  site-lib (topkg needs `#use "topfind"`), `CAML_LD_LIBRARY_PATH`, `bzip2`/`xz` **bin**
  outputs (tar needs them for `.tbz`), and for conf-* in the closure `PKG_CONFIG_PATH`,
  `C_INCLUDE_PATH`, `LIBRARY_PATH`.
- Output layout: opam's `prefix` = `$out`, `lib` = `$out/lib`, `%{_:lib}%` = `$out/lib/<name>`,
  `stublibs` = `$out/lib/stublibs`; `%{dep:lib}%` = that dep's output.
- Sources: `builtin:fetchurl`, flat hash = the opam file's `checksum` (sha512 preferred, sha256
  accepted, md5-only refused). No download at lock time is needed to get the hash.
- **Bypassing dune for bob's own code works:** `ocamldep -sort`, an alias module `geom__`
  (`module Vec = Geom__Vec`, `-no-alias-deps`), modules compiled `-open Geom__ -o
  geom__Vec`, `geom.ml` as the library interface, `ocamlfind ocamlopt -a` with `-cclib
  -lgeom_stubs -cclib -lcfoo -ccopt -L…`, a generated `META`. Consumers (bob exe, in-tree dune,
  merlin) see the same `Geom.Vec` names as with dune. ≈ 40 lines of generated shell.
- Bypassing dune for third-party packages is not proposed: their dune files use rules, ppx,
  `(select)`, configurator, cppo and menhir; reimplementing dune is out of scope.

### 3.5 Editors (merlin 5.6-504, ocaml-lsp-server 1.25.0 from nixpkgs)

`bob-ml ide` writes `.bob/bin/{ocamllsp,ocamlmerlin,dot-merlin-reader,dune,ocamlfind,ocaml,ocamlc,ocamlopt}`
shims (fixed store paths, `OCAMLPATH` = opam closure in the store) plus either style:

| Style | Files | Needs a build in the tree? | merlin `errors` on app/geom | type of `Geom.Vec.hash` (cross-library) | ocamllsp diagnostics + hover |
|---|---|---|---|---|---|
| dune | `dune-project`, `geom/dune` (incl. stub flags with store paths), `app/dune`, `geom.opam` | **yes**: before `dune build @check` merlin reports a config error and types are `'a -> 'b`; after it (0.2–0.8 s) all correct | none | `Geom.Vec.t -> int` | `[]`, `Geom.Vec.t -> int` |
| merlin | `geom/.merlin`, `app/.merlin` (`S .`, `B .`, `PKG …`, `B <bob output of geom>`, `FLG -open Geom__`) | **no** | none | `Geom.Vec.t -> int` | `[]`, `Geom.Vec.t -> int`, 0.30 s session |

A type error introduced in `app/main.ml` was reported by ocamllsp in both styles (`The constant
3 has type int but an expression was expected of type Geom.Vec.t`). With the merlin style, the
editor sees other local libraries through their last bob build (`B <store>`), so their
signatures lag until `bob dev` rebuilds them; inside a library's own directory merlin uses
the sources.

### 3.6 Numbers (`measure.sh`)

`ws/demo`, `demo` target: 24 derivations (10 fetch, 11 opam, 1 C, 1 lib, 1 exe). Cold = all
bob outputs deleted, fetched sources and the nixpkgs toolchain present.

| Measurement | direct [8c/15G] | dune [8c/15G] | direct [2c/4G] | dune [2c/4G] |
|---|---|---|---|---|
| cold (11 opam packages + C + lib + exe) | 17.0 s | 16.7 s | 26.7 s | 27.3 s |
| no-op | 0.33 s | 0.33 s | 0.31 s | 0.28 s |
| incremental, exe source changed | 1.42 s | 1.45 s | 1.35 s | 1.43 s |
| incremental, library source changed (lib + exe) | 1.98 s | 2.17 s | 1.77 s | 2.11 s |
| baseline: in-tree `dune build` of own code only, cold / lib change | 0.80 s / 0.13 s | | 0.81 s / 0.16 s | |

Other: `cohttp-lwt-unix` graph cold incl. downloads 61 s [8c/15G]; resolve ≤ 0.15 s; parsing
the whole repository 1.5 s; `bob-ml ide` 0.9 s with everything built.

- Incremental cost is dominated by per-derivation overhead (source import, `nix build`
  start, sandbox): ≈ 0.7 s per derivation over the compile time. In-tree dune is 10× faster
  for local edits because it has early cutoff and no sandbox. Direct mode is 0.1–0.3 s faster
  than dune mode per local derivation (no dune start-up/scan).
- No opam baseline (`opam install` of the same 11 packages) was measured: it needs a full
  opam switch with a compiler build. Unverified.

### 3.7 Error output (`ws/err`)

```
✗ //demo:demo build failed
  File "main.ml", line 5, characters 85-86:
  5 |   let json = `Assoc [ … ("hash", `Int (Geom.Vec.hash 3)) ] in
  Error: The constant 3 has type int but an expression was expected of type
✗ //geom:geom build failed
  geom_stubs.c:3:52: error: implicit declaration of function 'cfoo_mixx'; did you mean 'cfoo_mix'?
```

Paths are relative to the copied source dir (`main.ml`, not `app/main.ml`); production must map
them back to repo paths and strip OSC-8 hyperlinks from gcc output. Not done in the spike.

## 4. Answer

- **Resolve from the opam repository with bob's own Go resolver**, toolchain (ocaml, dune,
  ocamlfind) fixed to nixpkgs versions. It agreed with opam on 6 of 8 graphs and is 50× faster;
  nixpkgs `ocamlPackages` covers too little (22 % of opam, none of the test graphs fully).
- **One derivation per opam package, running the package's own opam build/install commands**,
  then `opam-installer`. This drives dune (`dune build -p`) for dune packages and also handles
  topkg/Makefile/ocamlbuild packages. Every package of both built graphs (11 and 60 packages) built.
- **bob's own code: compile directly** (`ocamldep -sort` + `ocamlfind ocamlopt`) with
  dune-compatible wrapping, one derivation per library/executable; keep a dune mode (generated
  dune file run inside the derivation) as fallback. Both were verified.
- **C stubs and bob C libraries link** into native executables that run under `env -i`; build
  references must be scrubbed from executables (693 → 55 MB closure).
- **Editors: generated `.merlin` + `.bob/bin` shims** work with no build in the tree; generated
  dune files also work but need an in-tree `dune build @check` first.
- Cost: cold 17 s [8c/15G] / 27 s [2c/4G] for the 11-package demo; no-op ≈ 0.3 s; local edits
  1.4–2.2 s.

## 5. SPEC input

### 5.1 API surface [proposal]

```go
package ocaml // github.com/megakuul/bob/pkg/ocaml

// Toolchain from nixpkgs (default: ocamlPackages of the pinned revision: OCaml 5.4.1,
// dune 3.21.1, findlib 1.9.8). Other versions = another nixpkgs ocaml-ng set (deferred).
func Toolchain(p *bob.P, opts ToolchainOpts) *OCaml

// An opam package; req is an opam version formula ("", ">= 5.0", "= 6.1.2"). Resolved into
// bob.lock by `bob lock` against the pinned opam repository.
func Opam(name, req string) *OpamPkg
func (o *OpamPkg) Lib(findlib string) Dep  // a findlib library of that package: lwt.Lib("lwt.unix")
// An opam package is also a Dep for its findlib library of the same name.

func Library(p *bob.P, o LibOpts) *Lib     // usable as Dep (not a cc.Linkable in v1)
func Executable(p *bob.P, o ExeOpts) *Exe  // native code
func Test(p *bob.P, o TestOpts) *Test      // an executable run in its own derivation (R-FW-024)

type LibOpts struct {
	Name    string          // default: directory name; module namespace = capitalised name
	Srcs    []string        // default: *.ml, *.mli in the directory
	Deps    []Dep           // ocaml.Opam(...), (*OpamPkg).Lib(...), *Lib
	Stubs   []string        // C stub files (foo_stubs.c)
	Native  []cc.Linkable   // bob C/C++ libraries or nixpkgs packages linked into stubs
	Wrapped *bool           // default true (dune semantics)
	Flags   []string        // extra ocamlopt flags
	Dune    bool            // build this library with a generated dune file (fallback mode)
}
type ExeOpts struct {
	Name string
	Main string  // module, default "main"
	Deps []Dep
}

// System packages behind opam conf-* names (shipped table; per-project additions):
func Conf(opamName string, pkgs ...nixpkgs.Package) bob.Option // ocaml.Conf("conf-libev", nixpkgs.Pkg("libev"))
```

Example:

```go
func Demo(p *bob.P) *ocaml.Exe {
	lwt := ocaml.Opam("lwt", ">= 5.7")
	return ocaml.Executable(p, ocaml.ExeOpts{
		Deps: []ocaml.Dep{ocaml.Opam("cmdliner", ""), ocaml.Opam("yojson", ""), lwt.Lib("lwt.unix"),
			ocaml.Opam("fmt", ""), geom.Lib(p)},
	})
}
```

### 5.2 Derivation layout [proposal]

| Derivation | Name | Builder job | Inputs | Output |
|---|---|---|---|---|
| source | `<file name of url>` | `builtin:fetchurl`, flat sha512/sha256 from bob.lock | – | archive |
| opam package | `opam-<name>-<version>` | unpack; env (§3.4); opam `build:` + `install:` rendered by bob; `opam-installer --prefix=$out --libdir=$out/lib --stubsdir=$out/lib/stublibs <name>.install` if present | source, toolchain, closure of dep outputs, conf-mapped nixpkgs | `lib/<findlib>/…`, `bin/`, `lib/stublibs/` |
| conf-* | `opam-conf-<x>-<v>` | none (empty) | mapped nixpkgs packages | empty; consumers get PKG_CONFIG_PATH/C_INCLUDE_PATH/LIBRARY_PATH |
| local library | `ml-<name>` | direct: ocamldep -sort, alias module, ocamlopt per module, stubs + `ar`, `-a`, META | own source, closure, bob C libs | `lib/<name>/{*.cmi,*.cmx,*.cmt*,<name>.cmxa,<name>.a,lib<name>_stubs.a,META}` |
| executable | `ml-<name>` | `ocamlfind ocamlopt -linkpkg`, then scrub build-only store refs (ocaml, gcc, glibc-dev, binutils) | own source, closure | `bin/<name>` |

Toolchain inputs: `ocamlPackages.{ocaml,findlib,dune_3}`, `opam-installer`, `stdenv.cc`,
`stdenv.cc.bintools`, `coreutils`, `bash`, `gnutar`, `gzip`, `bzip2.bin`, `xz.bin`,
`gnumake`, `gnused`, `gnugrep`, `gawk`, `findutils`, `diffutils`, `which`, `gnupatch`,
`pkg-config`. Production: replace the bash script with the static `bob-builder` (as in S3) and
make reference scrubbing an explicit, tested step.

### 5.3 bob.lock entry [proposal]

```json
{"opamRepository": "github:ocaml/opam-repository@af5ddb0addf6e75660ee9d7485b27425459d1d66",
 "packages": {
  "fmt": {"name": "fmt", "version": "0.11.0",
          "url": "https://erratique.ch/software/fmt/releases/fmt-0.11.0.tbz",
          "hash": "sha512-P0AVX8anMVIC5BBYWWQwfWNBbIAB/SQ2Z+2djRoCtn3uyssl6cL+tAnFN7vft4F9kRaN5N3WQ1Mv9R1sHGlqSg==",
          "opam": "02bb57a07d169eef9bde16e0a50a00423a4f756d589f7d1fed0d4d3aa367d11c",
          "deps": ["cmdliner", "ocamlbuild", "topkg"]}}}
```

`deps` = depends (all flags as evaluated) + depopts present in the solution, minus provided
toolchain packages. The opam repository snapshot itself is a fixed-output input (tarball,
NAR-hashed); evaluation reads only the opam files named in bob.lock, verified by `opam`.
Size: 11 packages 3.8 KB, 60 packages 22.9 KB (indented JSON).

### 5.4 Resolution [proposal]

- Global variables: `os=linux`, `arch=arm64|x86_64`, `os-family/os-distribution=nixos`,
  `opam-version=2.5.1`, `ocaml:native=true`; flags `build=post=true`,
  `with-test=with-doc=dev=with-dev-setup=false` (`with-test=true` only for packages under test
  in a later version).
- Provided (fixed, never chosen from the repository): `ocaml`, `ocaml-base-compiler`,
  `ocaml-compiler`, `ocaml-config`, `ocaml-options-vanilla`, `base-{unix,threads,bigarray,
  domains,nnp,effects}`, `dune`, `ocamlfind` at the nixpkgs versions.
- Algorithm: prune fixpoint, then newest-first search. **Production should use a conflict-driven
  solver** (or PubGrub-style) and a regression corpus against `opam install --dry-run`.
- Errors: `✗ cannot resolve opam packages: ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0":
  no available version matches (dune is fixed to 3.21.1 by the toolchain)`;
  `✗ opam:foo.1.0 has no sha256/sha512 checksum; bob cannot pin it`;
  `✗ opam:conf-foo.1: no nixpkgs mapping for this system package (add it to bob's conf table)`;
  `✗ opam package lwt is not in bob.lock (run bob lock)`.

### 5.5 Tooling files [proposal]

- Default: `<dir>/.merlin` per local library/executable (`S .`, `B .`, `PKG <findlib>…`,
  `B <store>/lib/<locallib>` for local deps, `FLG -open <Lib>__` inside wrapped libraries),
  `.bob/bin/{ocamllsp,ocamlmerlin,dot-merlin-reader,dune,ocamlfind,ocaml,ocamlc,ocamlopt}`.
  All generated files start with `# generated by bob from bob.go — do not edit` and are
  checked for drift (R-FW-006).
- Optional (`ocaml.Editor(ocaml.DuneFiles)`): `dune-project` (`(lang dune 3.21)` must be the
  first line — a leading comment is rejected by dune), `dune` per directory, `<lib>.opam`; `bob
  dev` then runs `.bob/bin/dune build @check` in the tree after changes.

### 5.6 Budgets [proposal]

| Budget | [8c/15G] | [2c/4G] | Basis |
|---|---|---|---|
| resolve, ≤ 110-package graph | ≤ 0.5 s | ≤ 1 s | 0.15 s |
| no-op build, demo graph | ≤ 0.5 s | ≤ 0.6 s | 0.33 / 0.31 s |
| incremental, executable source change | ≤ compile + 1.5 s | ≤ compile + 1.5 s | 1.4 s |
| incremental, library change (lib + exe) | ≤ compile + 2 s | ≤ compile + 2 s | 2.0 / 1.8 s |
| cold, 11 opam packages + local code | ≤ 25 s | ≤ 40 s | 17 / 27 s |
| `bob dev` tooling files, everything built | ≤ 1.5 s | ≤ 2 s | 0.9 s |
| resolver result | = `opam install --dry-run` on ≥ 70 % of a 20-graph corpus; always a valid solution | | 6/8 |

### 5.7 v1 scope [proposal]

Support: native libraries/executables/tests, opam packages from the pinned repository,
conf-* table, C stubs linking bob C libraries and nixpkgs packages, `.merlin` + shims,
Linux aarch64/x86_64.
Defer: bytecode and js_of_ocaml/melange outputs, ppx in local code in direct mode (use the
dune mode), menhir/ocamllex in direct mode (dune mode), `patches`/`extra-source` in opam files,
OCaml version choice other than nixpkgs' default, cross-compilation, OCaml libraries as
`cc.Linkable` for C consumers, odoc.

## 6. Decisions, risks, open items

### Agent decisions

- Bash builders instead of the Go `bob-builder`: the question was the build model, not the
  builder; S3 already settled the builder.
- opam repository pinned as a tarball snapshot in the spike (`tmp/repo`), not yet as a FOD
  input: the read path is the same.
- Greedy newest-first resolver with pruning instead of a SAT solver: enough to answer
  feasibility; flagged as a production risk.
- `dune` fixed to the nixpkgs version during resolution: bob cannot build dune from opam
  without bootstrapping it, and nixpkgs already provides it.
- Direct compilation for local code by default: it is what bob controls fully and is slightly
  faster; dune mode kept because it already works and covers ppx/menhir.
- `.merlin` as the default editor file: the only style that needs no in-tree build.
- conf-* packages run no checks: bob provides the system library from nixpkgs instead.

### Owner decisions needed

1. **Where opam packages come from.**
   (a) opam repository + bob resolver (this spike);
   (b) nixpkgs `ocamlPackages` only;
   (c) (a), with nixpkgs used for a package when the versions match.
   Recommendation: **(a)**. (b) covers 22 % and has one version per package; (c) adds a second
   code path for little gain (same-version overlap is 12 %).
2. **Who builds bob's own OCaml code.**
   (a) bob directly (ocamldep + ocamlfind), dune mode per library as opt-in fallback;
   (b) always generated dune files run inside the derivation;
   (c) in-tree dune only, bob just packages the result.
   Recommendation: **(a)**. It matches §7 (bob replaces dune) and was verified; (b) is the
   simplest to maintain and could become the default if ppx/menhir support in direct mode
   proves costly; (c) breaks hermetic per-package caching.

### Risks

- Resolver quality: plain backtracking blew up on 5 of 8 graphs before pruning; real-world
  graphs with tight conflicts may still need a proper solver.
- opam build commands may access the network or the system (sandbox catches it, but such
  packages then fail); `patches`/`extra-source` are unimplemented.
- Reference scrubbing is pattern-based (`ocaml-*`, `gcc-*`, `glibc-*-dev`, `binutils-*`);
  scrubbing a path the program needs at runtime would break it. Needs a test per output.
- With `.merlin`, editors see other local libraries only as of their last bob build.
- The repository snapshot is 171 MB unpacked (4 MB compressed); bob reads only needed opam
  files, but must fetch and unpack it once per pin.

### Still to verify

- Tests (`ocaml.Test`) were not built; ppx in local code in dune mode was not tried.
- `dream` and `ocaml-lsp-server` graphs as resolved by bob were not built.
- x86_64 and cross-compilation not tried.
- opam baseline timings (`opam install` of the same packages) not measured.
- All numbers indicative (shared host); rerun `measure.sh` and `resolve/compare.sh`.
