# I-M9b progress (npm NEEDS outside pkg/npm: working-tree node_modules, runtime translations, lock glue)

Owner of: pkg/npm/**, pkg/engine/materialise (new), pkg/engine/translate, pkg/engine/build/lock.go.

## done
1. **Shared manifest-based materialisation** `pkg/engine/materialise` (new, E1; SPEC §03.6.6,
   R-FW-066/067): `.bob/gen/manifest.json` (SPEC types; 64 MiB cap, invalid entries dropped so
   their files count as foreign), flock `.bob/gen/lock`, per-file classification by sha256
   (keep / overwrite / delete / edited / foreign / tracked / `.bob-tmp-*` / missing),
   size+mtime fast path, pending manifest before writing, temp+fsync+chmod 0444/0555+rename,
   stale files and empty dirs removed, `.gitignore` per owned dir, final manifest only when
   changed; a root with conflicts is skipped (BOB-E-1016 per file), others proceed; `Force`
   moves edited/foreign files to `.bob/gen/backup/<UTC>/<path>`; git-tracked files refused even
   with Force; `Prune` removes roots nothing produces (files, .gitignore, empty parents);
   nested/shared/invalid owned dirs → BOB-E-1020; `.bob/.gitignore` = `*`. Store outputs are
   mirrored from their host path: symlinks to /nix/store are rewritten to `<home>/nix/store`
   (works inside and outside the namespace), relative links leaving the output become
   absolute; `Keep` names (.vite, .cache) are never touched; `Record` writes the store path.
   Tests: `materialise_test.go` (R-FW-066 a, b, c (git), d, e, f-style interrupted write,
   symlink tree + Keep + Record, nested roots).
2. **Engine hook**: `engine.Materialisation` + `engine.Materialiser` (new file
   pkg/engine/materialisation.go), `PlanResult.Materialise` (engine.go, one field). Build glue
   `pkg/engine/build/materialise.go` (new): after realise, bob build/test/run materialise every
   planned entry whose output is valid (conflicts = warnings); `CmdGen` (bob gen) builds the
   entries of the requested packages and materialises them (conflicts = errors, exit 3), prunes
   for the whole repo when every output built; `Options.NoMaterialise` (pipeline runs) and
   `Untrusted` (workers) skip it. `build.Gen` runs this after the native files (and only
   prunes when no ecosystem materialises).
3. **npm**: `nodeModules` records `<dir>/node_modules` (host system only, Keep .vite/.cache,
   Record `<dir>/.bob/node_modules.path`); `Materialises` = repo has importers; `PrepareDev`
   adds the direct deps' commands as in-tree tools (`<dir>/node_modules/.bin/<cmd>`, first
   importer by dir wins) so `bob env` makes shims (tsc, vite, vitest, esbuild).
4. **Translator**: `translate/npm.go`: `Could not locate the bindings file` (package from the
   first " → " path, not `bindings` itself) → BOB-E-2013 (script install, reason "native
   addon"); `ERR_MODULE_NOT_FOUND` / `Cannot find package 'X' imported from <store file>`,
   CJS `Cannot find module 'X'` + `Require stack`, `error TS2307` in a store package file →
   BOB-E-2014 (bare specifiers only; user sources are left alone). In build logs only for npm
   packages (classifyLog), for bob run via `translate.Program(label, stderrTail)`; the human
   block's head is the SPEC `✗` sentence (definitionLine). Excerpts have store paths
   shortened. Also fixed `newErr`: only the selected variant's template args are filled (a new
   1008 variant from another unit made the replay catalogue fail with `<missing>`).
5. **bob run** (pkg/cli/cmd_build.go, small): the program's stderr is teed into a 64 KiB tail;
   on a non-zero exit `translate.Program` adds the error block / --json error event; exit
   status stays the program's.
6. **build.Lock** passes `LockRequest.Nixpkgs` (lazily opened index of the pin; also the
   on-demand locking of bob build/test), so npm's engines check (and Rust's rust-version
   fallback, A84) see the pinned toolchain. npm: `nodeVersion` = the importers' nodejs
   attribute's version (24.21.0 measured); part of the section's `inputs` when known.
   npm lock changes are reported per package (`- npm left-pad 1.3.0`) like Go's.
7. **Resolver keep-pins**: `resolve.Options.Prefer` (name → previously locked versions): a
   range dep that admits a previous pin (engines OK) keeps the highest such pin, exact
   versions and dist-tags are picked as npm does. gen Lock passes the previous section's
   versions minus `--update` names (`--update` alone or `--update npm` = none) when the
   registry URL is unchanged. Test `TestPreferKeepsPreviousPins`.

Verified end to end [aarch64, 8c/15G, load ~30, live registry, BOB_HOME=/var/tmp/i-m9b/home]
on a private copy of the npm fixture (npm.NewApp/NewTest edits, see TEST PROBLEM 1):
bob lock (nodejs 24.21.0 looked up); bob gen materialised native/, site/, ts-lib/node_modules
(symlinks into `<home>/nix/store`, .bin wrappers 0555, `.bob/node_modules.path`); host node
outside the namespace: `import('lodash4')` ok, `import('bindings')` ERR_MODULE_NOT_FOUND; bob gen
again: no change, exit 0; bob run //native:native prints the sqlite/esbuild/lodash JSON; with
AllowScripts("esbuild") only: bob run exits 1, stderr ends with
`✗ better-sqlite3 needs its install script (native addon). Add npm.AllowScripts("better-sqlite3") to //native:native`
+ hint, --json has BOB-E-2013, node_modules/better-sqlite3 switched to the script-less
instance; bob env --format json → .bob/bin/{tsc,vite,vitest,esbuild,…}; `../.bob/bin/tsc
--noEmit -p .` in ts-lib exits 0; adding left-pad + bob lock: only `left-pad@1.3.0` added;
foreign `ts-lib/node_modules/foo/index.js` → bob gen exit 3 BOB-E-1016, `--force` → backup
under .bob/gen/backup/<UTC>/ts-lib/node_modules/foo and rewrite.

Checks: gofmt clean; go vet + GOWORK=off go vet clean for pkg/engine/{build,materialise,
translate}, pkg/engine, pkg/npm/..., pkg/cli; `tools/ci -only fmt,vet,lint,layering`: fmt, lint,
layering ok, vet fails only in internal/orchestrator (another unit's WIP: "could not import
maps"). go test ./pkg/ ./pkg/engine/... ./pkg/npm/... ./pkg/cli/...: all ok except
pkg/engine/srcs TestImportManyTiming (cold import 15.5 s vs 5 s budget at load ~30; not touched).

## files
- new: pkg/engine/materialise/{manifest,materialise,materialise_test}.go,
  pkg/engine/materialisation.go, pkg/engine/build/materialise.go,
  pkg/engine/translate/{npm,npm_test}.go
- pkg/npm/gen/{eco,plan,dev}.go, pkg/npm/resolve/{resolve,arborist,resolve_test}.go
- pkg/engine/translate/{classify,render,errors}.go, pkg/engine/build/lock.go

## files outside ownership (small, additive; reported)
- pkg/engine/engine.go: `PlanResult.Materialise`.
- pkg/engine/build/build.go (`CmdGen`, `Options.NoMaterialise`, `Options.Force`), plan.go
  (planner.materialise collected in planEcosystem), pipeline.go (materialise after realise;
  targetLabels nil for CmdGen), realise.go (CmdGen installables; genContext), native.go (Gen
  calls genMaterialise with the original patterns).
- pkg/cli/cmd_build.go: bob run stderr tail + translate.Program.
- pkg/layering_test.go e1Pkgs + spec/01-core.md and SPEC.md R-ARCH-003 table: E1 gains
  `pkg/engine/materialise` (R-ARCH-001: new packages are added to the table in the same change).
- No pkg/builder change (BuilderVersion stays), no pkg/bob change (framework.zip unchanged;
  TestFramework ok).

## DECISIONS
- D-M9b-1 pkg/engine/materialise is E1 (engine service next to srcs, importable by build and
  ecosystems); added to the R-ARCH-003 table (layering test + SPEC) instead of hiding it in a
  subpackage.
- D-M9b-2 node_modules uses the shared manifest protocol (atomic per entry: temp + rename)
  instead of one directory rename (§03.4.3 "swapped in by atomic rename"): one ownership model
  for every working-tree output, foreign trees (npm install) are never replaced silently
  (1016; --force keeps a backup), .vite/.cache kept via Keep. ManifestFile gains `link` for
  symlink entries (sha256 of "symlink:<target>").
- D-M9b-3 Symlinks in the working tree point to `<home>/nix/store/...` (host path), not
  /nix/store: they resolve outside bob's namespace (host node, editors) and inside (home is
  bound there too); `.bob/node_modules.path` records the /nix/store path (SPEC). Transitive
  links inside store packages stay /nix/store (namespace only: bob exec, shims).
- D-M9b-4 Materialisation conflicts are warnings in bob build/test/run (the build succeeded)
  and errors (exit 3) in bob gen. A hoisted node_modules is mirrored as files (symlinks would
  break sibling resolution through realpath).
- D-M9b-5 bob gen builds the working-tree outputs of the requested packages (patterns) and
  prunes only for the whole repository when every output built; repos without a materialising
  ecosystem only prune (no planning cost).
- D-M9b-6 2013/2014 render with the SPEC `✗` sentence as the block head and keep the `hint:`
  line (fault entries require a hint); bob run keeps the program's exit status.
- D-M9b-7 Keep-pins applies to range dependencies only (exact and dist-tag specs pick as npm);
  the node version joins the npm section's `inputs` when known (omitted otherwise, so existing
  locks keep their hash).
- D-M9b-8 PrepareDev Tools: the commands of direct dependencies of every importer, first
  importer by directory wins a name.

## NEEDS
1. **Registry snapshot (owner, `-refresh-registry`)**: no refresh command exists yet.
   `tools/regmirror` can only record crates (`-record-crates`); `tests/fixtures/registry` holds
   only README.md, so every npm acceptance test (they all start the mirror) fails at
   resolution. Needed in tools/regmirror (not this unit's): a `-record-npm` mode that, on a
   miss, fetches `https://registry.npmjs.org/<name>` with
   `Accept: application/vnd.npm.install-v1+json`, rewrites every `dist.tarball` to
   `@REGISTRY@/<name>/-/<file>.tgz`, stores `npm/packuments/<name>.json` (scoped "/" → "%2f"),
   per-version docs `npm/versions/<name>/<version>.json` on request (os/cpu packages), and
   tarballs into the blob cache `npm/<name>/-/<file>.tgz`; plus `index.json`
   (`{"v":1,"captured":…,"npm":{"snapshot":…},"opam":{"commit":…}}`). Recording through the
   tests captures exactly what npm (oracle) and bob load, including optional peers. Owner command
   once it exists:
   `go run ./tools/regmirror/cmd/regmirror -manifests tests/fixtures/registry -blobs ~/.cache/bob-test/registry-blobs -record-npm -listen 127.0.0.1:4873 &`
   then `BOB_TEST_REGISTRY_URL=http://127.0.0.1:4873 go test ./tests/eco/npm/... ./tests/errors/... -count=1 -timeout 6h`
   (then the opam/crates parts). Size estimate [measured on I-R-npm's warm metadata cache:
   420 abbreviated packuments = 113 MB]: the 10 graphs + fixtures load ~1,000–1,500 names →
   ~0.3–0.5 GB of packuments to commit (owner decision: repository size) + ~0.3 GB tarballs in
   the blob cache; < 2 GB, but not run here (no record mode; committing manifests needs the owner).
2. Pipeline runs (`bob pipeline run`, M5.6/M16) must set `build.Options.NoMaterialise`.
3. Codegen (M10) can use the same hook: return `PlanResult.Materialise` entries (Dir =
   owned dir, no Keep/Record) and implement `engine.Materialiser`; bob gen --check, bob status
   and the S6 conflict golden text are not done here.
4. I-M6a: in-tree tools from npm now exist (shims for tsc/vite/vitest); `bob env` must run after
   `bob gen` or a build (node_modules is not materialised by bob env itself).

## TEST PROBLEM
1. A123 says I-TC moved the npm fixtures to npm.NewApp/NewTest, but they still call
   `npm.App(p,…)` / `npm.Test(p,…)`: tests/fixtures/repos/npm/native/bob.go:14,
   tests/fixtures/repos/npm/ts-lib/bob.go:24, tests/fixtures/repos/codegen/web/bob.go:16,
   tests/eco/npm/repos_test.go:34, build_test.go:151, graphs_test.go:110. They do not compile
   against pkg/npm (function NewApp, type App). Not edited (protected).
2. Still open from I-M9a TEST PROBLEM 2 (A127 owner item): root package name rule R2 vs the
   fixtures' root `package npm` / `package root`; the private copy evaluated with `package npm`
   because its directory was named npm.
