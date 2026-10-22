# I-M12a progress (M12.1 closures/bundles/pruning/licences, M12.2 OCI, M12.4 tarballs, M12.5 releases, M12.6 budgets)

Owner of: pkg/output/oci (+gen, bop), pkg/output/tarball (+gen, bop), pkg/release (+gen, bop,
schema). Not pkg/output/appimage (I-M12b).

## coordination with I-M12b (AppImage)
- I add (engine, additive): `engine.PlanCloser` (optional extension of PlanRequest.Store, like
  PlanRealiser): `Closure(ctx, outs []engine.PlanOutput) (map[string][]string, error)` makes the
  given outputs valid (bob derivations planned so far are built with RealiseNow, nixpkgs ones
  substituted/built) and returns path → references for their whole closure. OCI needs it at
  plan time (one layer derivation per store path, R-OUT-011); tarballs use it for the Static
  check (BOB-E-1022, exit 3) and R-OUT-007 prune checks (BOB-E-2015) run at plan time on the
  realised closure. AppImage may use it too (licence table for every closure path, prune check).
- I use your nixidx `Entry.Pname`/`Entry.Licenses` and `builder.WriteLicences` when they land;
  I will not edit pkg/engine/nixidx or those builder helpers.
- Evaluator: images/releases need their programs for several platforms (ImageOpts.Platforms,
  ReleaseOpts.Platforms). I add to pkg/bob + pkg/bob/ext: `ext.Elsewhere(p, platform) *bob.P`
  (evaluates the running constructor again for another platform in a separate "nested"
  configuration that is never a root and returns it), `ext.Nested(p) bool`, `ext.Record/Recorded`
  (values a nested constructor leaves for the outer one). In a nested configuration
  appimage.NewBundle may simply build its node as usual (unreachable nodes are not in the IR).
- BuilderVersion: we both bump it; whoever lands second re-checks the number (current 8).

## done
(starting 21:35)
- 22:20 BuilderVersion 9 → 10 (I-M12b took 9 for appimage; my kinds oci-layer/oci-image/tarball/
  release + pkg/builder reprotar.go/args.go). Whoever regenerates builders.go last runs
  `go generate ./pkg/engine/store` after both.
