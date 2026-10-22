# I-M12a progress (M12.1 closures/bundles/pruning/licences, M12.2 OCI, M12.4 tarballs, M12.5 releases, M12.6 budgets)

Owner of: pkg/output/oci (+gen, bop, internal/ocispec), pkg/output/tarball (+gen, bop,
internal/tarspec), pkg/release (+gen, bop, internal/relspec, schema). Not pkg/output/appimage
(I-M12b).

## coordination with I-M12b (AppImage)
- Uses I-M12b's nixidx Entry.Pname/Entry.Licenses (indexVersion 2). BuilderVersion: 9 = appimage,
  10 = these kinds, 11 = my later builder edits (regenerate builders.go after the last change).
- Offered to I-M12b: `engine.PlanCloser` (closure while planning), `engine.Licences`,
  `engine.Prune`/`CheckPruned`, `ext.Elsewhere` (they use Elsewhere for Bundle.Platform).

## done (checkpoint 22:25)
- Evaluator (pkg/bob nested.go, ext.go additions, graph hooks): `ext.Elsewhere(p, platform)` runs
  the running bob.Define function once more for another platform in a *nested* configuration
  (own memo, never a target, so its own result is never a root); `ext.Nested`, `ext.Record`,
  `ext.Recorded` carry the nested run's packages to the outer constructor. Unit tests
  pkg/bob/nested_test.go (both platforms' programs in one image node, targets not shadowed).
- Engine (additive): `engine.PlanCloser`/`PlanOutput`/`ErrClosureBuild` (planstore.go),
  `PlanRequest.Version`; pkg/engine/outputs.go: ClosureOf, StoreName, Licences (nixpkgs meta
  via the index by store-name pname; bob paths name = package, "unknown"), SPDXExpression,
  OwnLicences, PrimaryOutputs, PruneGlob/Prune/CheckPruned (DT_NEEDED via RUNPATH → BOB-E-2015),
  Jobs (no-fixup bob-builder jobs). pkg/engine/build: closure.go (planStore.Closure: RealiseNow for
  bob drvs, `nix build drv^out` for nixpkgs ones, one `nix path-info --json --json-format 1 -r`),
  release.go (DevVersion, CheckVersion → BOB-E-1020, note "no version given; using 0.0.0-dev"),
  Options.Version → planner → PlanRequest.Version. CLI: `bob build --version V` (validated before
  the store opens; usage line unchanged because tests/cli/help_test.go pins it).
- Kinds: oci.image, tarball.tarball, release.release (ext.Register in the API packages; gen
  packages registered as ecosystems "oci", "tarball", "release"; blank imports in
  pkg/cli/link.go; bop kinds oci-layer, oci-image, tarball, release in cmd/bob-builder).
- OCI (R-OUT-002/009/010/011/012): per platform closure of Program+Extra (+ host cacert/tzdata,
  "out" outputs, shared by all platforms), popularity layering with MaxLayers, one derivation
  per layer `oci-layer-<hash8>-<name>` (shared layer `oci-layer-<sha8>-shared`), inputs = its
  store paths only, unsafeDiscardReferences.out; image derivation inputs = layers only; layer
  blobs are *relative* symlinks (resolve under bob's home too); customisation layer, config
  defaults, licences label; release images get tag, ref.name annotation, version label and
  their own derivation with the same layers. Prune → per-layer skip lists; DT_NEEDED check.
- Tarballs (R-OUT-004): Closure (nix/store/…, bob/roots, bob/licences.json) and Static (bin/<name>
  + bob/licences.json); Static of a program with references → BOB-E-1022 at plan time (exit 3).
- Releases (R-OUT-022/023): closures for each of Platforms (ext.Elsewhere), images re-planned
  with the version, AppImage/tarball files hashed by the builder, versioned file names in the
  manifest only, release.json keys sorted, artifacts sorted by name then platform.
- pkg/builder: reprotar.go (reproducible tar/gzip, GNU long names instead of PAX), args.go
  (DecodeArgs). BuilderVersion 11 (10 was committed at 21:58 with an older builder; the CI
  builder stage passes with 11), builders.go and framework.zip regenerated 22:21.
- Unit tests green: pkg/bob (nested), pkg/engine (outputs), pkg/builder (reprotar),
  pkg/output/oci/{gen,bop}, pkg/output/tarball/gen, pkg/release/bop, pkg (layering).
  gofmt/vet/staticcheck/boblint clean on touched packages; `tools/ci -only layering` ok; the
  repo-wide fmt/vet/lint stages fail only on tools/regmirror (another unit's WIP).
- End-to-end on a scratch repo /var/tmp/i-m12a/repo (Go program + nixpkgs coreutils, arm64 host,
  load 8-20): static and closure tarballs; images for arm64 and amd64+arm64 (amd64 cross-built);
  image closure = image + layers (nix path-info -r), layer references empty; `crane validate
  --remote` PASS for linux/amd64 and linux/arm64; rootless podman (bobtest) pull + run prints the
  program's line, `id` → uid=65534(app) gid=65534(app), `TZ=Europe/Zurich date -d @0` → Thu Jan  1
  01:00:00 CET 1970; skopeo copy --all keeps the index digest; second build no-op (counts.built
  0); one source change → exactly one new layer derivation; MaxLayers 4 → 4 layers; prune of
  libacl → `✗ //bad:pruned: prune pattern "…libacl.so*" removes libacl.so.1, which coreutils
  needs` exit 1; static tarball of coreutils → BOB-E-1022 listing acl, attr, glibc, gmp; release
  1.0.0 vs 1.0.1: identical closure paths and tarball bytes, only index/tags/file names differ;
  no --version → note + 0.0.0-dev; invalid version → `✗ --version: invalid release version …`
  exit 3. Fresh bob home (second store) → the same image store path and an identical index.json
  (R-OUT-010). `bob build --dry-run` builds nothing for outputs: Closure refuses in a dry run and
  the generators plan placeholders.
- M12.6: tools/outbench (std only; not protected) measures B-086/B-087 and prints BUDGET lines.
  Scratch repo, image = Go program + coreutils + curl, amd64+arm64 [8c/15G shared, load 12-18,
  NOT the budget fixture, cold includes substituting curl's closure and the amd64 cross build]:
  OCI cold 24.6 s (55 drvs), incremental 3.9 s (5 drvs), new compressed bytes 4.7 %, arm64 image
  28.5 MB in 29 layers. Not registered in tools/internal/budgets (NEEDS 1).

## next
- nothing in this chain beyond NEEDS (polyglot acceptance runs once M10 lands).

## DECISIONS
- D-M12a-1: multi-platform programs come from `ext.Elsewhere` (nested evaluation of the running
  constructor); no SPEC mechanism existed for "Program and Extra are built for each platform".
- D-M12a-2: closures are read at plan time (engine.PlanCloser): OCI needs one derivation per
  store path; a failed program build while planning plans a placeholder image/tarball that
  depends on the program (the build stage reports the program's failure).
- D-M12a-3: layer inputs are the contained store paths as inputSrcs (not the roots' drvs), so a
  layer derivation is keyed only by its paths, the skip list (prune) and the format version.
- D-M12a-4: cacert/tzdata are the host's nixpkgs packages ("out") for every platform (data only),
  so images of all platforms share those layers; they are closure members, not roots.
- D-M12a-5: prune applies to nixpkgs paths only (SPEC "files of nixpkgs paths"); the skip list
  is per layer (only entries of its paths); R-OUT-007 check runs at plan time on the realised
  closure (ELF DT_NEEDED resolved through RUNPATH/RPATH, $ORIGIN).
- D-M12a-6: tar entries are written in lexical order of names with "/" after directories (parents
  first); names/targets over 100 bytes use the GNU long-name form (archive/tar would pick PAX,
  which R-OUT-010/tests forbid).
- D-M12a-7: layer blob symlinks are relative (`../../../<layer>/layer.tar.gz`): absolute
  /nix/store targets dangle on the host (bob's store lives under its home), and the acceptance
  tests read the layout through host paths. Nix still records the reference (hash in target).
- D-M12a-8: release `project` is "" (no project setting exists in bob.RepoOpts yet); repo =
  <project>/<release> (one image) or <project>/<release>/<image>; tags [version]; image config
  label org.opencontainers.image.version and index.json ref.name annotation.
- D-M12a-9: versioned file name = <artifact name>-<version>-<rest of the file name> (fallback:
  before the extension); the package output keeps the unversioned file.
- D-M12a-10: invalid --version is BOB-E-1020 with label "--version", option `release version
  "<v>"`, checked in the CLI before the store opens and again in build.Run.
- D-M12a-11: org.opencontainers.image.licenses = SPDX ids of the roots joined with " AND ",
  "unknown" → NOASSERTION (dropped when other ids exist).
- D-M12a-12: a closure artifact's entrypoints = {<name>: MainProgram}; without a main program
  the builder lists bin/* of the output.

## NEEDS
1. M10 (pkg/proto): the polyglot fixture does not evaluate (`undefined: proto.Lint` in
   proto/greeting/v1/bob.go), so every tests/outputs test on polyglot (R-OUT-001/002/006/007/008/
   009/010/011/012/022/023) cannot run yet; verified on the scratch repo instead (above). The
   R-OUT-024/025 bench rows (B-086, B-087) are not registered in tools/internal/budgets (a
   registered bench that cannot build its fixture would fail the fast budget stage).
2. eval owner: TestROUT004Tarballs (go fixture overlay tools/tar/bob.go importing
   example.com/tools) fails with BOB-E-1024 R7: rules.go ownDir only maps the root module, the
   nested golang module example.com/tools (bob-generated go.mod) is not a bob.go package for R7
   (same family as I-M5a NEEDS 3, nested go.mod).
3. CLI/server: `bob build --remote --version` does not carry the version (engine.RemoteRequest
   has no field); M16.
4. SPEC: R-OUT-009 acceptance (customisation layer changes too, test-corrections 35); §4.11 /
   R-OUT-023 invalid-version text with the 1020 label (36/37); R-OUT-002 mechanism note for
   ext.Elsewhere; §04 Interfaces: engine.PlanCloser.
5. R-INST-005 (profile semantics) needs `bob install`/`bob registry` (M16.6, notYet in the CLI);
   not implemented here.

## TEST PROBLEM (corrections applied, owner-approved evidence-based class; plan/test-corrections.md 35-37)
1. TestROUT009: customisation layer always changes with the root (licences.json) → count leaves
   the last layer out.
2. TestROUT011: skopeo needs --insecure-policy on hosts without /etc/containers/policy.json.
3. TestROUT023: `✗ invalid release version "<v>"` impossible with BOB-E-1020's template → expects
   `✗ --version: invalid release version "<v>"`.
4. (I-M12b applied) checkLicences glibc LGPL-2.0-or-later (nixpkgs meta) — confirmed by my run:
   glibc 2.42-100 → ["LGPL-2.0-or-later"].
