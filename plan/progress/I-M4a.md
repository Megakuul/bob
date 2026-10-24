# I-M4a progress (M4.1 bob-builder + bspec v1, then M4.4 fixup contract / per-drv overhead)

## done
- M4.1 `bob-builder` + bspec job v1 (R-NIX-015..018)
  - pkg/builder: builder.go (Main, Kind registry passed explicitly — `builder.Core()` map that
    cmd/bob-builder extends with pkg/<eco>/bop kinds; load/validate job from
    $NIX_ATTRS_JSON_FILE; exit codes 0/1/2/3/4; setPhase/`bob::` diag lines; last line names the
    failed step and argv[0]), paths.go (confinement: lexical root check + no existing symlink from
    root to parent + O_NOFOLLOW final; copy with size-bounded reads, copy-into-itself refused),
    run.go (ops mkdir/write/copy/symlink/exec/check-outputs, all steps decoded and checked
    before the first runs; exec env exactly per R-NIX-016, argv[0] looked up only in the declared
    path), unpack.go (tar/gz/bz2/zip with the fetcher's safety rules), fixup.go (R-NIX-019).
  - cmd/bob-builder/main.go: stdlib + pkg/builder only (builder bytes stay stable).
  - store: builder.go (BuilderPath, HostSystem, Store.Builder: imports the embedded builder,
    9007 on mismatch, 7003 when built without it), builders.go (generated table),
    builderemb_{arm64,amd64}.go + builderbin/<arch>/.gitignore (embedded gz, gitignored),
    generator cmd/bob-builder/genbuilders (`go generate ./pkg/engine/store`).
  - Green: TestRNIX015BuilderReproducible, TestRNIX015EmbeddedTableMatchesBuilder,
    TestRNIX016RunOps, TestRNIX016UnpackAndCopyTree, TestRNIX016InvalidJobs,
    TestRNIX016MiBJobBuilds, TestRNIX017LogsAndExitCodes; own pkg/builder tests (fuzz 3 seeds x
    150 jobs, symlink escapes, unsafe archives, scanner) and store tests (NAR of the embedded
    tree, table vs embedded binary, import, real-Nix run, real-Nix failures exit 3/4 with the
    output invalid).
- M4.4 fixup contract (builder part) and per-derivation overhead (R-NIX-019, R-FW-020)
  - Green: TestRNIX019FixupStripsAndKeepsDebug, TestRNIX019FixupShrinksRunpath,
    TestRNIX019ReferenceCheck; store TestBuilderChain100Overhead.
  - Measured [8c/15G Neoverse-N1, shared host, load 2–4, tmpfs /tmp]: builder alone 6 ms/run
    (100 runs 0.62 s). Real Nix chain of 100 `run` drvs (each copies the previous output):
    1.7–1.9 s (17–19 ms/drv) without substituter lookups; 13.3–14.0 s (133–140 ms/drv) with
    the default nix.conf, because Nix asks cache.nixos.org for every bob output before building
    it (sandbox=false or fsync-metadata=false change nothing; `substituters =` or
    `allowSubstitutes = false` on the drv → 17–19 ms/drv). See NEEDS.
- ATTRIBUTION.md: Nix row Where/Source and busybox Source (rows from I-M2b NEEDS); the table got a
  Source column. TestRNIX020Attribution green.

## still red (not in my packages)
- TestRNIX017BuilderExitCodesInRealRuns, TestRNIX018SuccessIsStructural,
  TestRNIX019DisallowedReferenceFault, TestRNIX019PolyglotClosure,
  TestRNIX019FileMacroDoesNotReferenceSources, tests/core TestRFW020ChainOf100Steps: CLI-driven
  (`bob build` is a stub in pkg/cli / pkg/engine/build).
- TestRNIX016FuzzedJobsStayContained: TEST PROBLEM below.

## files
- pkg/builder/{doc,builder,paths,run,unpack,fixup}.go, builder_test.go
- cmd/bob-builder/main.go, cmd/bob-builder/genbuilders/main.go
- pkg/engine/store/{builder,builders,builderemb_arm64,builderemb_amd64}.go, builder_test.go,
  builderbin/{arm64,amd64}/.gitignore; store.go (stub removed only)
- ATTRIBUTION.md (Nix/busybox rows only)

## DECISIONS
- D-M4a-1: kinds are passed explicitly (`builder.Main(args, kinds, …)`, `builder.Core()`), no
  global registry; cmd/bob-builder merges pkg/<eco>/bop kinds into the map.
- D-M4a-2: cmd/bob-builder and pkg/builder import only the standard library and bspec (no diag,
  no engine packages): every byte of the builder is an input of every bob drv.
- D-M4a-3: jobs name outputs with Nix's hash placeholder (`/` + nixbase32(sha256("nix-output:"
  + name))). Verified on the embedded Nix 2.34.9: placeholders inside `bobSpec` are rewritten to
  the output paths in $NIX_ATTRS_JSON_FILE (store TestBuilderRunsInNix).
- D-M4a-4: confinement: a write is invalid (exit 2) when its path is outside the outputs/build
  dir lexically or passes through an existing symlink (root included); the last component is
  opened with O_NOFOLLOW (exit 1 if it is a symlink). Copy into itself is invalid. Copy reads at
  most the size seen at stat time (pseudo files such as /proc stay bounded). Exec cwd may not be
  a symlink.
- D-M4a-5: write: mode = Mode&0777 (0 → 0644) | 0600 (the builder and the fixup must be able to
  read/rewrite what it wrote; Nix canonicalises the bits to 0444/0555 anyway). Copies normalise
  to 0644/0755.
- D-M4a-6: builder `unpack` supports tar, tar.gz, tar.bz2 and zip (stdlib); xz/zstd → exit 2
  (bob's fetcher unpacks those in Go before the build; the builder layer may not import the
  compression modules).
- D-M4a-7: fixup: strip only under bin/ libexec/ lib/; ELF ET_EXEC and PIE → --strip-all,
  `*.so*`, ET_REL and `!<arch>` → --strip-debug; debug info goes to
  `<debug>/lib/debug/<output>/<rel>.debug` (+ .gnu_debuglink); static libraries are not split.
  RUNPATH: entries outside the store dir are dropped except `$ORIGIN`-relative ones, then
  `patchelf --shrink-rpath`; applied to every ELF of every output. The reference scan covers
  every output except the fixup's own debug output (debug info may name sources), with Nix's
  32-char window algorithm over contents, names and symlink targets.
- D-M4a-8: the debug output is created by the fixup; `check-outputs` (during the steps) does not
  require it, the final output check does.
- D-M4a-9: embedded builder binary is gitignored like the Nix binary (D-M2b-5): a bob built
  without `go generate ./pkg/engine/store` compiles and fails at Store.Builder with BOB-E-7003.
  The generator computes the path with nar.Hash + drv.MakeStorePath (srcs.Path imports store,
  which would need the table being generated).

## NEEDS
- pkg/engine/build (pipeline owner): per-drv overhead is dominated by Nix's substituter lookups
  (above: 135 ms vs 18 ms per drv). Bob drv outputs never exist on cache.nixos.org; options:
  (a) set `allowSubstitutes = false` (drv.Job.ExtraAttribute) on bob drvs after bob has checked
  its own caches in one batch (Index.Cached / narinfo HEADs) and substituted the hits itself, or
  (b) realise nixpkgs paths first, then bob drvs with `--option substituters "<bob caches>"`.
  Without one of them TestRFW020ChainOf100Steps (≤ 12 s) cannot pass on this host. Possibly an
  architect/SPEC note (R-NIX-016 lists the drv attributes).
- pkg/engine/build: R-NIX-018: success = `nix build` exit 0 AND Store.Invalid(outputs) empty;
  the builder never relies on log text. Get the builder path with `Store.Builder(ctx)` (once per
  command) and list it in drv.Job.InputSrcs; outputs are referenced with the hash placeholder
  (D-M4a-3); `Fixup.Forbidden` and `Job.Disallowed` must carry the same set.
- Layering (pkg/layering_test.go owner): E2 generators may not import pkg/builder/bspec, so they
  cannot use the job types; either allow e2 → bspec or generators build the job JSON by hand.
- drv (I-M2a): a `drv.Placeholder(output string) string` helper (Nix hash placeholder) for the
  generators.
- CI / harness (tools/ci, tests/harness): run `go generate ./pkg/engine/store` (and
  scripts/fetch-nix.sh) before `go build ./cmd/bob`, and add the R-NIX-015 check "builder source
  changed without a BuilderVersion bump" (e.g. builders.go diff vs bspec.BuilderVersion change).
- ATTRIBUTION.md: bob-builder is bob's own AGPL code embedded in `bob`; it has no row (I was
  only allowed the I-M2b rows). Coordinator may add one if "every embedded binary" is meant
  literally.

## TEST PROBLEM
- tests/nix TestRNIX016FuzzedJobsStayContained cannot pass for any implementation: for the
  "bogus" op it builds `json.RawMessage(`{"path":"` + path() + `"}`)` by string concatenation;
  the tokens `a\..\b` and `\x00` make that invalid JSON, so `raw()` (builder_test.go:61) fails
  with "json: error calling MarshalJSON for type json.RawMessage: invalid character '.' in string
  escape code" at the first such job (seed 16 hits it deterministically, after 0.16 s). Fix would
  be `raw(t, map[string]string{"path": path()})`. Every job before that point stayed contained.
  The same property is covered by pkg/builder TestFuzzStaysContained (3 seeds x 150 jobs,
  exit spread 0/1/2/4 checked).
- Observed while running CI: pkg/engine/lock/lock_test.go does not compile (another unit's work
  in progress), so `go run ./tools/ci -only lint,layering` currently fails there; it passed for
  my packages before that file changed.
