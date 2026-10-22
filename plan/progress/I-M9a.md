# I-M9a progress (M9.2-M9.5: npm ecosystem)

Owner of: pkg/npm/** (resolve untouched), web/bob.go. Small reported edits outside (below).

## done
- pkg/npm API (evaluator side): Dep/Alias/Peer/Dev/Optional, AllowScripts/Extend/Hoisted,
  Library, NewApp, Vite, NewTest (runners Vitest, NodeTest, SvelteCheck, ESLint), TSConfig
  defaults per constructor, kinds npm.lib/app/site/test (pkg/npm/internal/npmspec). Importers
  depend on nixpkgs nodejs + bash; apps on nodejs-slim + esbuild (Node) or bun (Bun); importers
  with AllowScripts on python3, gnumake, gcc, gnused, gnugrep, coreutils (+ quirk Tools).
- Quirks table (esbuild >=0.15 Skip; better-sqlite3 >=8 npm_config_build_from_source=true) with
  a Test each; CheckQuirks → BOB-E-9006 naming the entry.
- pkg/npm/gen (engine.Ecosystem "npm" + LockFreshness + DevPreparer):
  - Lock: resolve.Input from the IR (libs as links, Extend merged), resolve.Resolve against
    $BOB_NPM_REGISTRY or the public registry (ETag cache $XDG_CACHE_HOME/bob/npm-meta, 24 h);
    *resolve.Error → BOB-E-1015; section re-encoded through lock.EncodeSection; inputs =
    lock.Inputs(declarations + registry); unchanged inputs and no --update keep the section.
    Once-per-lock notes for install scripts not run (not for packages that never install on
    Linux, not for quirk-skipped ones).
  - Stale: per importer of the graph (missing record, unlocked alias, version outside the
    declared range, Dev change, alias no longer declared) → BOB-E-1012 with the bob.go position.
  - Native: package.json (SPEC key order, exact locked versions, npm:<name>@<v> for aliases,
    file:<rel> for libraries, sorted dependency objects = resolver order) and tsconfig.json
    (header comment, extends, sorted compilerOptions, include/exclude; SvelteKit inherits
    include from .svelte-kit/tsconfig.json); drift Explain lines per key.
  - Plan: tarball fetches (FOD-identical flat files, hash = integrity), one npm-unpack
    derivation per package instance (`npm-<key>`), dependency cycles in one `npm-scc-<hash8>`,
    platform filtering of os/cpu/libc (optional packages dropped, required ones an error),
    allowed install scripts in the instance derivation (`…-script`), importer
    `node_modules-<dir>` (symlinks + .bin wrappers) or `node_modules-<dir>-hoisted` (npm-style
    tree from the tarballs, breadth-first top-or-nest placement), library `npm-lib-<name>`
    (tsc; $out/node_modules/<name>/{package.json,dist} + runtime links), Vite site (svelte-kit
    sync for Kit; dist/ or build/ → $out), app (esbuild --bundle [--packages=external] or bun
    build; store refs of npm drvs scrubbed; bin/<name> launcher: node-slim shebang + import(),
    Bun: bash exec), test (workspace drv + core test-run: vitest run, node --test tap,
    svelte-check --fail-on-warnings, eslint).
- pkg/npm/bop: kinds npm-unpack (unpack, links, lifecycle scripts offline with bash -euo
  pipefail, node-gyp wrapper, HOME=$TMPDIR, npm_config_nodedir, then only build/Release/*.node
  kept and only addons the package's JS names), npm-node-modules (isolated/hoisted, .bin),
  npm-build (mkdir/copy/write/symlink/exec/scrub).
- web/bob.go: //web:web (SvelteKit, deps of web/package.json at their exact versions, TSConfig
  Extra as web/tsconfig.json) and //web:svelte-check. Inert until M5.6 makes the repository a
  bob repository (no root bob.lock yet); tools/ci stage "web" stays the bootstrap path.
- Unit tests: pkg/npm/gen (strict decode of every job into bop/bspec types, layout: instance
  drvs, cycle group, darwin package dropped, scripts only where allowed, determinism; hoisted;
  native files + drift; stale), pkg/npm/bop (unpack/links/escapes, scripts + prune, failing
  script, node_modules isolated/hoisted, build steps + scrub).
- Verified end to end (private copies, live registry, BOB_HOME=/var/tmp/i-m9a/home1, aarch64,
  shared busy host): npm fixture (with the TEST PROBLEM edits): lock; //ts-lib:ts-lib (dist +
  d.ts); //site:site (Svelte + workspace lib); bob test //ts-lib:test PASS (vitest);
  //native:native builds better-sqlite3 offline (~2-3 min) and bob run prints
  {"sqlite":{"v":"3.53.2","y":42},…,"lodash":"bobNpm"}; only build/Release/better_sqlite3.node
  kept, the addon references only glibc and gcc-lib. fastify (esbuild bundle): bob run prints
  "fastify ok", output references only nodejs-slim. hono (Bun): "bun ok object", references bun
  + bash. Vite+React isolated and npm.Hoisted(): identical site output (diff -r), one
  node_modules-web-hoisted drv. SvelteKit adapter-static: index.html, no .svelte-kit in the tree.
  Denied better-sqlite3: run fails with node's "Could not locate the bindings file".

## files outside ownership (small, additive; reported)
- pkg/bob/internal/graph/graph.go, pkg/bob/node.go, pkg/bob/ext/ext.go: ext.CallerDir (the
  label default "directory name" needs the directory before the node exists).
- pkg/engine/engine.go: LockRequest.Note; pkg/engine/build/lock.go: LockResult.Notes and the
  note parameter of lockEcosystems; pkg/cli/cmd_lock.go prints "note: …"; pkg/cli/link.go
  blank import of pkg/npm/gen.
- pkg/builder: Ctx.Unpack, Ctx.Copy exported (the unpack kind / copy op, unchanged behaviour);
  bspec.BuilderVersion 6; cmd/bob-builder/main.go adds npmbop.Kinds(); builders.go regenerated;
  pkg/engine/eval/framework.zip regenerated (contains every unit's current pkg/ L0-L3 sources).

## DECISIONS
- D-M9a-1 App/Test naming: functions NewApp/NewTest, types App/Test (A35 kept; Go forbids a
  function and a type of one name; same pattern as golang.NewModule). See TEST PROBLEM 1.
- D-M9a-2 Default sources: Library bob.Glob("src/**") (SPEC); App and Vite the importer
  directory without node_modules, dist, build, .svelte-kit, .vite, package.json,
  tsconfig.json, package-lock.json (the SPEC list for Vite fails when public/ is absent:
  every include pattern must match, BOB-E-1025).
- D-M9a-3 Label name default: directory name via ext.CallerDir; root importer: "app"/"site".
- D-M9a-4 Stale is per importer (versions must satisfy declared ranges); the section's inputs
  hash only short-cuts bob lock. Ranges moving outside the lock are found; npm.Extend changes are
  not (they re-resolve at the next bob lock).
- D-M9a-5 Allowed scripts keep only the build/Release/*.node files the package's JavaScript names
  (all of them when it names none): better-sqlite3 also builds test_extension.node.
- D-M9a-6 Package-instance and node_modules jobs skip the binary fixup (prebuilt binaries stay
  byte-exact); script builds still reference only glibc/libgcc (measured).
- D-M9a-7 Hoisted layout: npm-style tree, each package at the top when the name is free, else
  nested below the package that needs it (breadth-first; keeps every earlier resolution valid);
  less hoisting than npm, same resolution semantics. Library peers resolve to the library's own
  locked peers (a consumer with another version gets two copies).
- D-M9a-8 App launcher: Node: `#!<nodejs-slim>/bin/node` + import(<bundle>) (no shell in the
  closure); Bun: bash script exec bun <bundle>. Bundle=false keeps packages external
  (esbuild --packages=external) and links runtime deps into $out/node_modules.
- D-M9a-9 Test runners SvelteCheck (//<dir>:svelte-check) and ESLint (//<dir>:eslint) added to
  npm.Runner (G24 left the ESLint/check entry point to this milestone).
- D-M9a-10 Lock without the nixpkgs index (LockRequest.Nixpkgs is nil): NodeVersion "" (engines
  check off).

## NEEDS
1. Working-tree node_modules (SPEC §03.4.3 last bullet, R-NPM-003a layout test, shims of
   R-NPM-008a): no engine hook lets an ecosystem swap a built node_modules into the tree and write
   .bob/node_modules.path after bob build/bob gen. Proposal: optional engine.Materializer
   (Materialize(ctx, req, resolved outputs) after realise) or the M9.6 bob dev contract; npm
   would materialise each importer's node_modules-<dir> output.
2. bob run / translator (R-NPM-004a): "Could not locate the bindings file" from a store package
   whose script was denied → BOB-E-2013 with "✗ better-sqlite3 needs its install script (native
   addon). Add npm.AllowScripts(\"better-sqlite3\") to //native:native" (the program's stderr is
   not translated today); and TS2307/ERR_MODULE_NOT_FOUND inside a store npm package → 2014.
3. build.Lock should pass LockRequest.Nixpkgs (pinned nodejs version for the resolver's engines
   check, I-R-npm DECISIONS 8).
4. bob exec / bob env shims (M6): TestRNPM005aQuirkTestsPassInFixture and
   TestRNPM008aTscShimAndDrift need them; npm's PrepareDev only puts nodejs on PATH.
5. Resolver: no "keep previous pins" input; when inputs change, bob lock re-resolves everything
   (versions of unrelated packages may move). Needs a preferred-versions option in resolve.
6. M5.6: root bob.go/bob.lock so web/bob.go is built by bob and //web:svelte-check runs in the
   ci pipeline; then tools/ci stage "web" can call bob instead of npm ci.
7. Registry snapshot manifests (tests/fixtures/registry/npm, owner -refresh-registry) for every
   npm acceptance test (they all start the mirror).

## TEST PROBLEM
1. tests/fixtures/repos/npm/native/bob.go, ts-lib/bob.go, tests/fixtures/repos/codegen/web/bob.go,
   tests/eco/npm/build_test.go (bun), repos_test.go (fastify), graphs_test.go (writeGraph) use
   both `*npm.App` / `*npm.Test` (type) and `npm.App(p, …)` / `npm.Test(p, …)` (call): Go
   forbids a function and a type with one name. With A35: calls become npm.NewApp / npm.NewTest.
2. Rule R2 (BOB-E-1024, SPEC 1.2): the npm fixture's root bob.go is `package npm` (module
   example.com/npmfix → must be npmfix); newRepo writes `package root` (→ npmextra); writeGraph
   uses packages g0, g1 … in directories web, lib, api (→ web, lib, api) and `package root` for
   the root. Every npm acceptance test fails evaluation before reaching npm.
3. A86 (known): npmResolution importer edges (plan/test-corrections.md item 10).
