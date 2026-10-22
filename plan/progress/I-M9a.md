# I-M9a progress (M9.2-M9.5: npm ecosystem)

Owner of: pkg/npm/** (resolve: fixes only), tools/regmirror npm routes. Small reported edits
outside (see "files outside ownership").

## done (checkpoint 1)
- pkg/npm API: Dep/Alias/Peer/Dev/Optional, AllowScripts/Extend/Hoisted, Library, NewApp, Vite,
  NewTest, TSConfig defaults, kinds npm.lib/app/site/test (pkg/npm/internal/npmspec), quirks
  table (esbuild skip, better-sqlite3 build_from_source) + CheckQuirks (BOB-E-9006).
- pkg/npm/gen: Lock (resolver integration, inputs shortcut, install-script notes), Stale
  (per-importer), Native (package.json/tsconfig.json), Plan (tarball fetches, per-instance
  npm-unpack drvs, cycle groups, allowed scripts, isolated/hoisted node_modules, tsc lib,
  vite/SvelteKit site, esbuild/Bun app with launcher, vitest/node --test test-run).
- pkg/npm/bop: npm-unpack, npm-node-modules, npm-build.
- Verified in a private copy (/var/tmp/i-m9a/fx, fixture with NewApp/NewTest + root package
  npmfix): lock, //ts-lib:ts-lib (dist + d.ts), //site:site, bob test //ts-lib:test (vitest pass),
  //native:native (better-sqlite3 built offline, program prints sqlite/esbuild/lodash).

## next
- fastify/hono/react/kit private repos, hoisted, closure checks; unit tests; web/ + ci.

## files outside ownership (small, additive)
- pkg/bob/internal/graph/graph.go, pkg/bob/node.go, pkg/bob/ext/ext.go: ext.CallerDir (label
  default "directory name" before the node exists).
- pkg/engine/engine.go: LockRequest.Note; pkg/engine/build/lock.go: LockResult.Notes,
  lockEcosystems note param; pkg/cli/cmd_lock.go prints notes; pkg/cli/link.go blank import.
- pkg/builder: Ctx.Unpack, Ctx.Copy exported (unpack/copy of kind run); bspec.BuilderVersion 6;
  cmd/bob-builder/main.go adds npmbop.Kinds(); pkg/engine/store/builders.go regenerated.

## DECISIONS
(see final report)

## NEEDS
(see final report)

## TEST PROBLEM
(see final report)
