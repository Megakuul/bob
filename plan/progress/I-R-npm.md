# I-R-npm progress (M9.1 pure npm resolver, early start per A78)

## done
- `pkg/npm/resolve` (pure library, L2; imports only std + `pkg/bob/diag`):
  - `semver.go` node-semver port (loose ranges, prerelease rule, engines check), `collate.go`
    (npm's `localeCompare('en')` for ASCII), `spec.go` (npm-package-arg subset; `ValidName`),
    `manifest.go` (JS-ordered JSON objects, npm's manifest normalisation, bin normalisation),
    `registry.go` (abbreviated packuments, per-version docs for libc, ETag disk cache + MaxAge,
    128 MiB cap incl. Content-Length, singleflight, 16 parallel, prefetch),
    `tree.go`/`arborist.go`/`place.go` (port of npm 11.19.0 Arborist buildIdealTree: deps queue,
    virtual roots, peer sets, PlaceDep/CanPlaceDep, dedupe/prune, ERESOLVE),
    `instances.go` (peer-correct instances on npm's tree, canonical merge of equal instances),
    `validate.go` (hostile-input checks), `resolve.go` (API: `Resolve(ctx, Input, Options)` →
    `Section` with the exact JSON of `lock.NpmSection`; `*Error` → `Diag()` BOB-E-1015).
  - Tests (offline, green, race-clean): `semver_test.go` (node-semver + ICU goldens in
    `testdata/`, generated from the pinned nodejs 24.21.0), `resolve_test.go` (fake registry:
    peer split, hoist/nest/duplicate instances, alias/tag/exact/latest/deprecated/engines,
    optional+platform+libc+bundled+bins, workspace lib + link + Extend, hostile docs
    (oversize/badname/md4/tarball/json, also for optional deps), invalid declarations, ERESOLVE,
    ETag cache 200/304/MaxAge, determinism, integrity forms, names).
  - `oracle_test.go` (opt-in): `BOB_NPM_ORACLE=<nodejs-24 bin> BOB_NPM_SNAPSHOT=<dir> go test
    ./pkg/npm/resolve -run TestOracle` runs pinned npm 11.19.0 (`npm install --package-lock-only`)
    and bob against one registry state (caching proxy over registry.npmjs.org serving abbreviated
    docs like tools/regmirror) and compares exactly as tests/eco/npm/resolve_test.go does.
- Evidence (2026-10-22, aarch64, live registry snapshot):
  - **All 10 SPEC graphs equal npm**: name@version sets and every edge, also with importers'
    devDependencies/peerDependencies counted; peersplit gives
    `use-sync-external-store@1.7.0(react@18.3.1)` / `…(react@19.3.1)`. Packages: vite-react 119,
    ts-lib 206, fastify 91, native 69, mono 89, stress 487, sveltekit 99, vite-svelte 87,
    bun-app 2, peersplit 5. Output byte-identical across runs.
  - Extra graphs (BOB_NPM_ORACLE_EXTRA=1): next 413, storybook 308, airbnb 294, cra 1216, vue 200,
    oldies 570, ws-conflict 18, rn 566, docusaurus 1151, mui 84, typeorm 209, karma 213,
    electron 380, eslint9 250, cdk 26: equal. angular 817, nest 547, gatsby 1301, nuxt 703,
    vuecli 833: equal name@version sets; differ only in peer edges where npm's hoisted copy
    resolves a peer its dependent does not see (DECISIONS 2). eresolve / eresolve-hooks: both npm
    and bob fail (ERESOLVE / E-RESOLVE).
  - Resolve time with warm local docs: ts-lib 1.5 s, stress 1.6 s, vite-react 1.0 s (JSON decoding
    of large packuments dominates).
- `go vet`, `GOWORK=off go vet`, gofmt, staticcheck, boblint (-nopanic -noexit -nolist -safego,
  -importboundary) clean for pkg/npm/resolve; TestLayering green. The full `tools/ci -only
  fmt,vet,lint,layering` currently fails on other units' WIP (internal/orchestrator,
  tests/store/fleet), not on this package.

## next
- nothing in this unit; M9 integration (see NEEDS).

## files
- pkg/npm/resolve/{resolve,arborist,place,tree,instances,registry,manifest,spec,semver,collate,validate}.go
- pkg/npm/resolve/{resolve_test,semver_test,oracle_test}.go, pkg/npm/resolve/testdata/{node-semver,icu-en}.json

## DECISIONS
1. Phase 1 ports npm 11.19.0's Arborist (buildIdealTree for a fresh `npm install
   --package-lock-only`, hoisted, no force/legacy-peer-deps/overrides) instead of the S4 spike's
   simplified BFS: equality with npm then holds by construction (placement, dedupe, nesting,
   peer sets, ERESOLVE), shown on 25 graphs. Reason: AGENTS §2.1/§2.9 (correctness, measured).
2. Peers resolve in the dependent's context (pnpm semantics, SPEC "pnpm-style") for every
   dependent, not only importers; all non-peer edges are npm's tree edges. If the dependent's copy
   does not satisfy the peer range but npm's does, npm's is used. Measured effect: none on the 10
   SPEC graphs; on angular/nest/gatsby/nuxt/vuecli some loaders get the webpack (acorn, ajv,
   vite) their dependent uses, where npm's hoisted copy loads a second webpack. Reason: §2.1
   (correct builds) over byte-equality with npm's known peer bug.
3. Instance keys: `name@version` + `(peer@version,…)` sorted by peer name (SPEC). Structurally
   equal instances are merged; when different instances would share a key (the same
   name@version at two npm tree positions with different dependencies, or peer-of-peer
   differences), each gets `_<8 hex>` of a canonical structural hash (deterministic,
   order-independent). Reason: the lock must keep npm's per-copy edges; SPEC has no rule.
4. Importer records list Dev() deps in `deps` and `dev` (SPEC example), and a library's Peers in
   `deps` and `dev` (installed for the library's own tsc/test build; consumers provide them at
   run time).
5. Importers are modelled as npm workspaces of a synthetic root package ("." is the root package
   when declared), with dependency objects sorted by name; nameless importers get link name
   `@bob-importer/<dir>`. bob's generated package.json files (M9.4) should write the same
   sorted order so that npm and bob agree on a user's machine too.
6. Hostile input: documents capped at 128 MiB (body and Content-Length); names validated (npm
   validForOldPackages, ≤ 214, no "."/".." segments) for declarations, dependencies and
   bundles; integrity = strongest of sha512/sha256/sha1 (anything else rejected, legacy hex
   shasum → sha1 SRI); tarball must be an http(s) URL; bins normalised; invalid JSON rejected.
   These are fatal (E-RESOLVE) even for optional dependencies; a 404 or no matching version of
   an optional dependency is dropped like npm does.
7. Not emulated (rare; recorded): opening tarballs with bundleDependencies (npm inspects them
   in lock-only mode; bob does not resolve bundled deps), npm-shrinkwrap.json inside
   dependencies (warning), overrides, a registry package depending on a workspace name;
   git/file/URL/workspace: specs are rejected (SPEC §03.4.1).
8. engines.node is applied like npm-pick-manifest with Options.NodeVersion (pass the pinned
   nodejs version; "" disables it).
9. npm.Extend: extra Deps are added to the package's manifest before resolution (they place like
   real dependencies); Extend peers are optional peers resolved in phase 2 only.
10. A Registry keeps documents in memory for its lifetime: use one per `bob lock` run. MaxAge
    implements "skip revalidation when caches are fresh (≤ 24 h)".
11. The code is a translation of npm's ISC-licensed algorithms (arborist, node-semver with its
    regular expressions, npm-package-arg, npm-pick-manifest, npm-normalize-package-bin), done
    under the cleanroom exception (D42): ISC is AGPL-compatible; needs the ATTRIBUTION row and
    decisions.md entry below (NEEDS).

## NEEDS
- Coordinator: ATTRIBUTION.md "Code taken under the cleanroom exception (D42)" rows, e.g.
  `| pkg/npm/resolve (semver.go, arborist.go, place.go, tree.go, manifest.go, spec.go) | translation of @npmcli/arborist 9 (npm 11.19.0), node-semver 7 (incl. its regular expressions), npm-package-arg, npm-pick-manifest, npm-normalize-package-bin | ISC (npm, Inc. and contributors) | keep the ISC notice | I-R-npm DECISIONS 11 |`
  and `collate.go`: ICU/CLDR root collation order for ASCII (Unicode licence, data only); plus a
  decisions.md row for DECISIONS 11.
- M9 integration (`bob lock`, pkg/npm/gen or the lock pipeline): build `resolve.Input` from the
  npm importers in bob.go (Lib → Name + Peers; npm.Alias → Dep{Alias, Name}; *Lib dependency →
  Dep{Link: dir}; Extend), `Options{Registry: &resolve.Registry{URL: $BOB_NPM_REGISTRY or default,
  CacheDir: $XDG_CACHE_HOME/bob/npm-meta, MaxAge: 24h unless --refresh}, NodeVersion: <pinned
  nodejs>, Root: <label/repo>}`; `resolve.Section` has the JSON of `lock.NpmSection` (convert
  field by field or via JSON); `*resolve.Error` → `e.Diag()` (BOB-E-1015, exit 3); print
  `Result.Warnings`; packages with `hasInstallScript` feed the R-NPM-004 note.
- Registry fixtures (owner, `-refresh-registry`): the snapshot must cover the abbreviated
  packuments of every package npm *loads*, which includes optional peers of loaded packages
  (npm fetches them to check peer-set conflicts, e.g. vite's sass/less/stylus/terser/…), and
  per-version documents for packages with os/cpu.

## TEST PROBLEM
- tests/eco/npm/resolve_test.go `npmResolution` adds importer edges only from `dependencies`
  and `optionalDependencies` of the importer's package-lock entry, while `bobResolution` adds
  every `deps` entry of bob.lock's importer, and SPEC §03.4.2 puts Dev() deps in `deps` (listed
  in `dev`). So every graph with a Dev() dependency (8 of the 10: all but bun-app and
  peersplit) reports "edge bob has and npm lacks: importer:<dir>|<dev dep>|…" although the
  resolution equals npm's. Evidence: oracle_test.go compares importer edges both ways; counting
  the lock entry's `devDependencies` (and `peerDependencies` for the library importer
  packages/ui of mono, see DECISIONS 4) gives zero differences on all 10 graphs. Suggested fix:
  read `devDependencies` and `peerDependencies` into npmLock and include them for importers.
