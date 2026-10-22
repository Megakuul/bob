# I-M4f progress (engine NEEDS of the CLI unit + CLI wiring)

Owner of: pkg/engine/build (not testrun), pkg/cli, cmd/bob; coordinated edits in pkg/engine
(engine.go: additive), pkg/engine/ir (Pos.Col), pkg/bob (column.go, env.go bobSite, eval.go
fields, irout.go). Not touched: pkg/golang, pkg/engine/eco (I-M5a).

## State (checkpoint 1, ~16:55)
- done: Package.Drv; build.CheckDeterminism; build.Check / build.Gen / generate (native.go);
  IgnoreFindings (ignore.go); lockStage: stale ecosystem sections (engine.LockFreshness) → 1012
  before any download, missing sections locked on demand; Run: drift check + (re)write of
  native files in the index stage (whole-repo runs only); Result.Generated.
- done: ir.Pos.Col (+ schema), pkg/bob column of the constructor call in the IR.
- next: CLI wiring (check, gen, build exclude block, graph col), tools/ci errors stage,
  framework.zip regeneration, checks.
