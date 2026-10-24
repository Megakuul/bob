# I-M11a progress (M11.2 OCaml ecosystem: pkg/ocaml API, gen, bop)

Owner of: pkg/ocaml/** (resolve untouched). Outside (small, reported): pkg/cli/link.go (one
blank import), cmd/bob-builder/main.go (merges mlbop.Kinds()), pkg/builder/bspec/bspec.go
(BuilderVersion 6 → 7), pkg/engine/store/builders.go and pkg/engine/eval/framework.zip
(regenerated), pkg/engine/lock/sections.go (opam packages without a source, NEEDS 1, done by
me because nothing locks without it), spec/03-ecosystems.md + SPEC.md §03.5.1 and
tests/fixtures/repos/ocaml/test/bob.go (ocaml.Test → ocaml.NewTest, coordinator-approved).

## done (2026-10-22)
- pkg/ocaml (L2): Opam/(*OpamPkg).Lib, Conf, Library, Executable, NewTest, LibOpts/ExeOpts,
  kinds opam.library/opam.executable/opam.test (axis platform). Every node depends on the
  toolchain tools (mlspec.ToolAttrs: ocamlPackages.{ocaml,findlib,dune_3}, opam-installer, gcc,
  binutils, patchelf, coreutils, bash, gnutar, gzip, bzip2, xz, gnumake, gnused, gnugrep, gawk,
  findutils, diffutils, which, gnupatch, pkg-config, m4) and the shipped conf table's nixpkgs
  libs (gmp, libev, openssl, pkg-config); ocaml.Conf adds/replaces entries. Default Srcs =
  *.ml (+ *.mli when the dir has any: an empty glob is BOB-E-1025). Evaluation checks: names,
  Main, opam names, flags (-o/-I/-ppx/abs paths refused), nil deps.
- pkg/ocaml/gen (E2): Ecosystem "opam" (Lock, Stale, Plan, Native, PrepareDev).
  Lock: requests (name, formula) of all nodes → inputs; snapshot = BOB_OPAM_REPOSITORY
  (<url>/<commit>.tar.gz, tests' mirror) or bob.lock's commit or DefaultCommit af5ddb0… from
  GitHub; narHash via fetch.Resolve (Archive, strip 1); opam files extracted once to
  $XDG_CACHE_HOME/bob/opam-repository/<commit>; resolve.Solve (host arch, toolchain 5.4.1 /
  3.21.1 / 1.9.8); Locked → lock.OpamPackage; unmapped conf-* → BOB-E-1023; UnsatError →
  BOB-E-1015 (ecosystem opam, root = declaring label, reason = the chain, definedAt);
  ChecksumError → 1023; bad formula → 1020. `bob lock --update opam-repository` re-picks the
  snapshot. Stale: declared package missing or not matching its formula.
  Plan: per opam package `opam-<name>-<version>` (opam-build; commands rendered in Go from the
  pinned opam file, digest checked; env OCAMLPATH/OCAMLFIND_DESTDIR/CAML_LD_LIBRARY_PATH/
  OCAMLTOP_INCLUDE_PATH, conf env PKG_CONFIG_PATH/C_INCLUDE_PATH/LIBRARY_PATH; source
  `opam-src-<name>-<version>` flat fetch: BOB_OPAM_CACHE_URL mirror, origin, opam.ocaml.org
  cache); conf-* = empty output with the mapped libs as inputs; local library `ml-<name>`
  (ml-compile), executable `ml-<name>` (ml-link, scrub + fixup forbids ocaml/gcc/binutils/
  sources/builder, shrink RUNPATH), test + `mlrun-<name>` (test-run, tap).
  Native: <dir>/.merlin (S ., B ., S <local lib dirs>, PKG <findlib libs incl. local libs>,
  FLG -open <Lib>__), no store paths. PrepareDev: ocaml, findlib, dune, ocaml-lsp, merlin,
  dot-merlin-reader on PATH; OCAMLPATH inside (opam closure + local libs only when the engine
  gives a full plan context, NEEDS 3).
- pkg/ocaml/bop (builder): opam-build (unpack gz/bz2/zip via Ctx.Unpack, xz/zstd through the
  job's tools; single top dir = source root; commands with @@BOB_JOBS@@ → cores; scripts with
  #!/usr/bin/env x or a missing interpreter run through PATH; opam-installer on <name>.install),
  ml-compile (ocamldep -sort, alias module, -open Lib__, stubs → lib<name>_stubs.a, -a, META,
  install cmi/cmx/cmt/cmti/sources; Dune: true → generated dune + dune build -p + installer),
  ml-link (direct: ocamlfind -linkpkg, main module last; Dune: true → dune build ./main.exe;
  then scrub). OCaml/gcc errors → structured diagnostics with repo paths.
- Tests: pkg/ocaml/gen TestGenJobsDecode (strict decode into bop/bspec types, rendered
  commands, closure, fetch URLs), TestNotLocked (1012), TestOpamDigestChecked;
  pkg/ocaml/bop TestScrub, TestShebang, TestMeta. go test ./pkg/ocaml/... ./pkg/engine/lock
  ./pkg/engine/store ./pkg/builder/... ./pkg ./pkg/cli green; gofmt, vet, staticcheck,
  boblint clean on my packages; `tools/ci -only fmt,vet,lint,layering`: layering ok, fmt/vet/
  lint fail only on other units' WIP (internal/notify, internal/store, pkg/engine/eval
  addBobImports, pkg/engine/build eval.FrameworkSum).
- End-to-end in a private copy (/var/tmp/i-m11a, removed at the end) on the ocaml fixture,
  bob home with an empty store [8c/15G, shared host, disk 99 % full]:
  - bob lock: 11 opam packages (= S5 demo / corpus "demo"), repository af5ddb0, narHash.
  - bob build //app:demo: cold 4m26 s incl. toolchain downloads; bin/demo under env -i prints
    `vec (4, 3)` and `{"x":4,"y":3,"hash":127}`; closure = demo + glibc only (ocaml, gcc,
    glibc-dev scrubbed); one `opam-<name>-<version>.drv` per locked package.
  - bob test //test:geom_test: pass (TAP, 2 cases).
  - vec.ml edited: built //app:demo and //geom:geom only, counts.built = 2.
  - Dune: true: bin/demo built by dune works the same.
  - ocaml-lsp-server "= 1.26.0": exit 3, BOB-E-1015 "cannot resolve opam dependencies for
    //app:app: ocaml-lsp-server.1.26.0 needs dune-rpc >= "3.22.0": no available version
    matches (dune is fixed to 3.21.1 by the toolchain)", definedAt app/bob.go:11.
  - bob gen writes the .merlin files; bob env creates .bob/bin/{ocamllsp,ocamlmerlin,
    dot-merlin-reader,dune,ocamlfind,ocaml,ocamlc,ocamlopt,…}.

## next
- NEEDS 2/3 (engine), then rerun tests/eco/ocaml once tests/fixtures/registry exists.
- Not done: opam patches/extra-source (SPEC: deferred, BOB-E-9008 at plan), ocaml.Editor
  (DuneFiles) opt-in dune files, nixpkgs libs in Native get -I/-L/rpath but no -l names,
  corpus graphs other than demo were not built.

## DECISIONS
- D-M11a-1: constructor ocaml.NewTest (type Test), as cc/npm; SPEC + fixture renamed
  (coordinator instruction).
- D-M11a-2: kinds are opam.library/opam.executable/opam.test: the engine routes kinds by the
  ecosystem name, which is the lock section key "opam".
- D-M11a-3: lock-time toolchain versions are SPEC's constants (5.4.1/3.21.1/1.9.8):
  LockRequest.Nixpkgs is nil in bob lock (NEEDS 4); the planner refuses an ocaml tool whose
  store name is not ocaml-5.4.1 (1020), so a nixpkgs bump cannot silently mismatch the lock.
- D-M11a-4: opam commands are rendered at plan time (gen), not in the builder: the builder
  must not import the resolver (its store path would change with resolver changes). %{jobs}%
  is a marker the builder replaces, so derivations do not depend on the machine.
- D-M11a-5: `<pkg>:installed`/`version`/`lib` of opam variables see the package's build
  closure (depends + present depopts) and the toolchain, not the whole solution: deterministic
  and exactly what is on OCAMLPATH.
- D-M11a-6: scrub patterns ^ocaml-[0-9.]+$, ^gcc-wrapper-, ^gcc-[0-9.]+$, ^glibc-.*-dev$,
  ^binutils- (gcc-*-lib, the runtime libgcc_s, is kept); the fixup additionally forbids the
  ocaml, gcc and binutils tool paths, the sources and bob-builder.
- D-M11a-7: .merlin names local libraries as PKG (resolved through the dev shell's OCAMLPATH)
  plus S <their dir>, never store paths: it is a Native file (no build needed, no drift).
- D-M11a-8: the opam-repository snapshot for resolving is extracted (opam files only) into
  the user cache; the plan reads opam files from the fetcher-imported, narHash-pinned store
  path and checks each against bob.lock's sha256.
- D-M11a-9: opam sources get three URLs: BOB_OPAM_CACHE_URL/<alg>/<xx>/<hex> (if set), the
  opam file's URL, https://opam.ocaml.org/cache/<alg>/<xx>/<hex>.

## NEEDS
1. (done by me, please review) pkg/engine/lock OpamSection.validate: url and hash may both be
   empty (virtual packages like base-bytes, conf-*); otherwise bob.lock with the S5 demo graph
   is unreadable.
2. translate/CLI owner: R-ML-002 expects the human line `✗ cannot resolve opam packages:
   <reason>`; the 1015 template renders `error[BOB-E-1015]: cannot resolve opam dependencies
   for //app:app: <reason>`. Proposal: definitionLine case 1015 with args ecosystem == "opam"
   → "cannot resolve opam packages: " + reason (UnsatError.Error() is exactly that text).
3. engine (build.Dev, I-M6a/I-M4f): R-ML-001b needs the opam packages and local libraries
   built for the shell. Proposal: Dev plans the whole graph like bob build (derivations written
   to the store) and passes the full PlanRequest (Builder, Sources, Resolver, Inputs) in
   DevRequest; ocaml's PrepareDev then lists the opam/library derivations as DevPackages
   (realised by bob env) and sets OCAMLPATH. Without it OCAMLPATH holds findlib's site-lib only
   and the LSP cannot see Lwt_main / Geom.
4. build/lock.go: LockRequest.Nixpkgs is not filled (Rust's rustVersion hits the same).

## TEST PROBLEM
1. tests/eco/ocaml/corpus_test.go writeRepo writes the root bob.go as `package root` in a
   t.TempDir() (base name like "001"): SPEC R2 wants the directory name ("x001"), so every
   R-ML-002 test fails with BOB-E-1024 before bob lock runs (measured with a copy:
   "./bob.go: package root, but … bob.go must use package …"). Plus I-R-opam's oracle problems.
2. tests/eco/ocaml needs tests/fixtures/registry (index.json with the opam commit, blobs);
   absent, so the acceptance tests cannot run offline; verified by hand above.
