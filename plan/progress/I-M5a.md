# I-M5a progress (M5.1 common ecosystem rules, M5.2 pkg/golang + gen + bop)

Owner of: pkg/golang, pkg/golang/gen, pkg/golang/bop, the common ecosystem rules (pkg/engine,
SPEC §03.0.1 names it as the place; pkg/engine/eco would need a protected layering-test row).

## done
- (in progress)

## next
- pkg/golang API, pkg/engine common rules, bop kinds, gen Plan/Native/Lock

## files (outside ownership, small, reported)
- pkg/bob/internal/graph/graph.go, pkg/bob/node.go, pkg/bob/ext/ext.go: ext.NewIn (label dir
  other than the bob.go's: Go binaries //<module dir>/<dir>:<name>, test units), ext.Errorf.

## DECISIONS

## NEEDS
- I-M4f (pkg/engine/build plan.go, ~20 lines): fill the new `engine.PlanRequest.Root` (o.Root)
  and `PlanRequest.Store` (new `engine.PlanStore`, pkg/engine/planstore.go): HostPath =
  hostPath(st, p); ImportSources = srcs.Import(ctx, st, trees); Ensure = the run's fetcher
  Ensure. pkg/golang/gen imports one source path per Go workspace package while planning and
  reads third-party module trees (two phases, S2-2). Without Store, Go planning fails with an
  internal error naming this.
- I-M4f (pkg/cli/link.go): blank-import `github.com/megakuul/bob/pkg/golang/gen` (registers
  ecosystem "go"; go.* kinds known to ir.Parse).
- Note for I-M4f: M5.1's framework (native.json, drift, backups, LockFreshness) is yours in
  pkg/engine/build/native.go; I do not create pkg/engine/eco. pkg/golang/gen implements
  Native (go.mod/go.sum with Explain) and engine.LockFreshness.

## TEST PROBLEM
