# I-M9b progress (npm NEEDS outside pkg/npm: working-tree node_modules, runtime translations, lock glue)

Owner of: pkg/npm/**, pkg/engine/materialise (new), pkg/engine/translate, pkg/engine/build/lock.go.

## Checkpoint 20:20
Done (compiles, unit tests green): materialise package, engine.Materialisation hook, npm
node_modules materialisation, build glue (bob build/test/run + bob gen), translator 2013/2014 +
bob run, LockRequest.Nixpkgs, resolver Prefer + gen Lock. Next: ci -only fmt,vet,lint,layering;
end-to-end on the npm fixture with the live registry; registry refresh notes.
