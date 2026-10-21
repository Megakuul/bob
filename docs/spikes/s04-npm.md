# Spike S4 — TypeScript/npm: own resolver, isolated node_modules, scripts, Vite dev in the namespace

Status: **done (indicative numbers)**, 2026-10-21, host `vibe-engine`. Code: `spikes/s04-npm/`
(throwaway). ADR: `docs/adr/0006-npm.md` (Proposed). Numbers: `spikes/s04-npm/measure-results.md`,
rerun with `spikes/s04-npm/measure.sh 8c|2c [depchange]`. Other spikes were running, so all
timings are indicative.

## 1. Question and requirements

Can bob install and build TypeScript/npm projects with its own resolver (no npm/pnpm/yarn CLI),
one fixed-output fetch per tarball, an isolated layout, scripts denied by default, Node as the
runtime with Bun optional, and run Vite with HMR inside bob's namespace?
R-NPM-001…010, R-DEV-001/002/006, D15, D30–D34, D47, ADR 0001 (B), ADR 0002 Decision 5.

## 2. What was built

| Part | Path | What it does |
|---|---|---|
| node-semver | `npm/semver.go` (+ test) | versions, `^ ~ x * - ||`, partials, prerelease rule |
| registry client | `npm/registry.go` | abbreviated packuments, disk cache + ETag, 16 parallel fetches, 128 MB cap, name validation; per-version full document for `libc` |
| resolver | `npm/resolve.go` | phase 1: versions (BFS, latest-tag rule, dedupe, optional, platform packages kept, `npm:` aliases, bundled deps skipped, required peers auto-installed, workspaces, `extend`); phase 2: peer-aware instances `name@v(peer@v,…)` |
| stand-in for bob.go | `npm/pkgjson.go`, `ws/*/package.json` + `bob.json` | importers from package.json/workspaces; targets, allowlist, `extend`, libs |
| npm comparison | `npm/pkgjson.go` `Compare` | name@version sets and resolved edges (walks npm's hoisted tree like Node) |
| derivations | `gen/gen.go` | sha512 FOD per tarball; one drv per package instance (unpack + dep symlinks; cycles collapsed via Tarjan); importer `node_modules` drv (symlinks + `.bin` wrappers); hoisted copy (comparison/fallback); workspace lib; builders; quirks table; lifecycle runner |
| builder scripts | `builder/bins.mjs`, `builder/lifecycle.mjs` | `.bin` wrappers with absolute node/bash; npm's install-script order incl. implicit `node-gyp rebuild` |
| driver | `cmd/bob-npm` | `lock`, `compare`, `build`, `materialise`, `paths` |
| dev / editor probes | `dev/hmr-probe.mjs`, `dev/tsserver-probe.mjs`, `dev/mkshims.sh`, `dev/vite.bob.config.mjs` | HMR latency over Vite's websocket; tsserver over stdio; `.bob/bin` shims; Vite wrapper config |

Backend: copies of `spikes/s06-codegen/{drv,run,nixidx}` (B plus sha512 fixed outputs).
Toolchain: nixpkgs `nodejs` 24.21.0 (npm 11.19.0 used **only** as the comparison oracle),
`nodejs-slim` for runtime outputs, `bun` 1.3.13.

Graphs (`ws/`): **vite-react** (React 19, Vite 7, plugin-react, TS 5.9), **ts-lib** (zod, vitest,
ESLint 9 + typescript-eslint type-checked), **fastify** (fastify 5, @fastify/cors, pino-pretty,
esbuild), **native** (better-sqlite3 = node-gyp addon, esbuild, `lodash4: npm:lodash@^4`),
**mono** (workspaces: Svelte 5 lib `@demo/ui` with peer svelte + Vite app; root devDep `npm`
which has 143 bundled deps), **stress** (jest 29, webpack 5, eslint 8, babel, ts-jest), **bun-app**
(hono, zod), **peersplit** (synthetic peer conflict across two workspaces).

## 3. Results

### 3.1 Resolver vs `npm install --package-lock-only` (npm 11.19.0, same minute)

| Graph | packages npm / bob | same name@version | edges checked / different | npm time | bob cold [8c] |
|---|---|---|---|---|---|
| vite-react | 119 / 119 | 119 | 94 / 0 | 6.67 s | 2.52 s |
| ts-lib | 206 / 206 | 206 | 221 / 0 | 8.92 s | 2.79 s |
| fastify | 94 / 94 | 94 | 81 / 0 | 2.39 s | 1.45 s |
| native | 69 / 69 | 69 | 49 / 0 | 1.68 s | 1.27 s |
| mono | 88 / 88 (+143 bundled in npm) | 88 | 42 / 0 | 6.88 s | 2.15 s |
| stress | 411 / 411 | 411 | 804 / 0 | 9.59 s | 1.53 s |

- **No difference on six real graphs.** Edges = for every npm lock entry and dependency, the
  version Node would load from npm's tree vs bob's edge. npm times are one run with a fresh npm
  cache; not a careful benchmark.
- Platform packages: bob keeps all 26–53 os/cpu-specific optional packages (esbuild, rollup,
  fsevents) like npm; `libc` agrees with npm on all 14 entries that have it. The abbreviated
  registry document has no `libc` field (checked on `@rollup/rollup-linux-x64-gnu`), so bob
  fetches the small per-version document for packages with `os`/`cpu`.
- Bundled deps: npm lists the 143 bundled packages of `npm` with `inBundle`; bob does not resolve
  them (they ship in the tarball). Intended.
- **peersplit** (root: react 18 + use-sync-external-store; workspace b: react 19 + the same):
  npm's lock has **one** `use-sync-external-store` at the root, so from b it resolves react 18
  (wrong peer). bob produces `use-sync-external-store@1.7.0(react@18.3.1)` and `…(react@19.3.1)`.
  Same name@version set; bob is more correct here.
- Instance keys with peers: 0–36 per graph (e.g. `@sveltejs/vite-plugin-svelte@6.2.4(svelte@5.57.2,vite@7.3.7)`).
- Not verified: graphs where npm's placement order produces a different dedupe than bob's global
  "reuse a chosen version" rule (none appeared), `overrides`, git/file/URL specs (bob rejects
  them with an error), `engines`-based selection.

### 3.2 Builds (all with backend B, outputs run under `env -i`)

| Build | Result |
|---|---|
| vite-react: `tsc` (skipLibCheck **off**) + `vite build` | ✓ dist contains the marker; hoisted variant gives byte-identical dist |
| ts-lib: `tsc` lib (d.ts), `vitest run`, `eslint` (type-checked) | ✓ 1 test passed, 0 lint errors |
| fastify: `tsc` + esbuild bundle + `bin/server` wrapper | ✓ `GET /hello` → `{"hello":"bob","node":"v24.21.0"}`; closure 253 MiB (nodejs-slim + icu + glibc) |
| native: better-sqlite3 (allowed script, node-gyp) + esbuild + alias | ✓ `{"sqlite":{"v":"3.53.2","y":42},"esbuild":"const a = 1;…","lodash":"bobNpm"}` |
| native without allowlist | ✗ as expected at run time: `Could not locate the bindings file` (bindings.js); bob printed `note: better-sqlite3@12.11.1: install script denied` |
| mono: Svelte app + workspace lib `@demo/ui` (peer svelte from the consumer) | ✓ `vite build`, dist contains the lib component |
| bun-app: `bun build --target=bun` + `bun run` in the sandbox + `bin/app` | ✓ `{"hi":"bob","bun":"1.3.13"}`; closure 165.6 MiB / 11 paths |

Findings from the builds:

- **Phantom dependencies break the strict layout:** `@fastify/cors` types `import … from 'fastify'`
  but fastify is only its devDependency. tsc: `TS2307: Cannot find module 'fastify'` in a
  `/nix/store/…-npm--fastify-cors-11.3.1/…` file. Fixed by `extend: {"@fastify/cors": {"fastify":
  "*"}}` (optional peer that links the consumer's instance). npm's hoisted tree hides this.
- **esbuild bundles keep store paths** in comments and `__commonJS` keys (`// ../../nix/store/…-npm-cookie…`),
  which put 51 npm paths into the runtime closure. Rewriting the hash part of `/nix/store/<hash>-npm-`
  in the bundle dropped them (59 → 56 paths, only node/bash left). bob's bundle builder must do this.
- **Vite writes into `node_modules/`** (`.vite-temp` for the config, `.vite/deps`). Builders and the
  working tree therefore get `node_modules` as a writable directory of symlinks, not a symlink.
- **Sandbox has no `/usr/bin/env`:** `.bin` entries are wrappers `exec <node> <realpath> "$@"`.
- node-gyp needed `sed`/`grep` besides python3/make/cc; the first attempt failed with
  `sh: sed: command not found`. The default script toolchain now includes them.
- After a node-gyp build the package output referenced python3 and nodejs (Makefiles,
  config.gypi). Keeping only `build/Release/*.node` cut its closure to glibc + libgcc.
- better-sqlite3 compile: **127–133 s** [8c/15G, cores=4] (sqlite amalgamation). That is a
  one-time cost per version and cacheable.

### 3.3 Layout: isolated (per-package derivations) vs hoisted copy (vite-react, 66 installed packages)

| | isolated [8c] | hoisted [8c] | isolated [2c/4G] | hoisted [2c/4G] |
|---|---|---|---|---|
| cold: all package drvs + node_modules + tsc + vite build (tarballs present) | 13.27 s | 10.01 s | 15.63 s | 13.33 s |
| no-op build | 1.37 s | 0.47 s | 0.84 s | 0.87 s |
| store size of node_modules (npm paths only) | 61.0 MiB / 67 paths | 60.9 MiB / 1 path | same | same |
| add one dependency (clsx): node_modules rebuild | **0.81 s** (5 drvs change) | 2.31 s (whole tree) | **0.69 s** | 2.32 s |
| one source file changed (App.tsx) → tsc + vite build | 5.58 s | 5.74 s | 5.92 s | 7.22 s |
| cold including tarball downloads | 13.18 s | — | 18.02 s | — |
| `materialise` (node_modules only) cold / no-op | 4.23 s / 0.58 s | — | 4.23 s / 0.42 s | — |

- Same disk use; the isolated layout shares package paths between projects (typescript, vite,
  esbuild are one path each across vite-react, ts-lib and mono). Hoisted is faster cold (one
  derivation, no per-drv overhead) and slower on every dependency change.
- A peer instance is a separate derivation, so peer splits cost one extra unpack each.
- Resolution tolerance: Node, tsc (`moduleResolution` bundler and NodeNext, skipLibCheck off),
  Vite 7/Rollup (native optional `@rollup/rollup-linux-arm64-gnu`), esbuild (`@esbuild/linux-arm64`),
  vitest, ESLint + typescript-eslint, Svelte plugin and Bun all worked through realpath'd
  symlinks with **no** `preserveSymlinks`. Only failure: phantom dependencies (above).

### 3.4 Resolve time (registry cache cold / warm with ETag revalidation / offline cache)

| Graph | [8c/15G] | [2c/4G] |
|---|---|---|
| vite-react (119) | 2.52 / 1.99 / 0.33 s | 3.35 / 2.74 / 0.71 s |
| ts-lib (206) | 2.79 / 2.60 / 0.76 s | 4.74 / 3.44 / 1.88 s |
| stress (411) | 1.53 / 1.07 / 0.35 s | 2.13 / 1.58 / 0.94 s |
| others (69–94) | 1.3–2.2 / 1.2–1.7 / 0.09–0.16 s | 1.3–2.0 / 1.4–3.7 / 0.15–0.35 s |

Peak RSS 22–168 MB (ts-lib is the largest: `typescript` 8.7 MB and `@types/node` 2.3 MB
abbreviated documents). Revalidation is network-bound (one 304 per package); offline time is
JSON decoding of large documents. Cold downloads 1.3–72 MB.

### 3.5 `bob dev`: Vite + HMR inside bob's namespace (bobtest, `spikes/rootless/cmd/bobns`)

Setup: closure of node + vite-react `node_modules` copied into bobtest's own userns store
(`nix copy` from a file cache, 5.0 s); project with materialised `node_modules` (symlinks to
`/nix/store/…`, which exists only inside the namespace) in `/home/bobtest/s04-dev/`.
`dev.sh` = `bobns -- vite --config .bob/vite.bob.config.mjs --port 20431`.

| Check | Result |
|---|---|
| Vite starts inside the namespace as bobtest (uid 0 inside) | ✓ ready in 257–309 ms |
| pre-bundled deps (`node_modules/.vite/deps`), `/src/*.tsx`, `/@react-refresh` | ✓ HTTP 200 |
| store file through `/@fs/nix/store/…` with the user's config only | ✗ **403 Restricted** (outside `server.fs.allow`) |
| same with bob's wrapper config (adds `/nix/store` to `fs.allow`) | ✓ 200 |
| inotify across the namespace: file written from **outside**, plain write, 20 saves | ✓ save → `update` median **2.5 ms**, max 17.5 ms [8c] |
| same with editor-style atomic save (write tmp + rename) | ✓ median 2.4 ms, max 15.9 ms |
| same with the dev server under [2c/4G] | ✓ median 3.3 ms, max 29.1 ms |
| served module after update contains the new text | ✓ every time |

Latency is save → HMR message on the websocket (server side). Browser apply time was not measured.
Dependency change (R-NPM-010): relock 2.1–2.5 s + re-materialise 0.7–0.8 s, then restart Vite
(~0.3 s); the restart itself was not automated in the spike.

### 3.6 Editors through `.bob/bin` shims (bobtest, outside the namespace)

Shim = `bobns -state … -- sh -c 'PATH=<node>/bin:$PATH; exec "$0" "$@"' <project>/node_modules/.bin/<tool>`.

| Probe | Result |
|---|---|
| tsserver (vite-react `App.tsx`): open + semantic diagnostics | ✓ 0 diagnostics, 2.1 s to first diagnostics |
| quickinfo + definition on `useState` | ✓ `(alias) useState<number>(…)`, definition in `/nix/store/…-npm--types-react-19.3.0/…/index.d.ts`, 42 ms |
| tsserver (ts-lib, zod `z.object`) | ✓ 2.6 s first diagnostics, definition into zod's `.d.cts` |
| `.bob/bin/eslint src` (type-checked rules) | ✓ rc 0, 3.7 s |
| `.bob/bin/tsc` on a file with a type error | ✓ `TS2322` reported with the repo path |

Not verified: a real editor (VS Code uses `typescript.tsdk` = a directory it reads itself).
Definition results are `/nix/store/…` paths; on this host they exist outside the namespace only
because root's store happens to contain the same paths. On a host without `/nix` the editor
cannot open them.

## 4. Answer

- **Resolver:** bob's own Go resolver matches npm 11 exactly on six real graphs (69–411
  packages) and is 1.1–6x faster cold. Peer-aware instance keys fix npm's wrong-peer case.
- **Layout:** isolated by default, **one derivation per package instance** (cycles collapsed),
  symlinked; hoisted copy only as an explicit per-importer fallback. Measured: same disk, faster
  dependency changes (0.7–0.8 s vs 2.3 s), cross-project sharing. Everything tested tolerated it
  except packages with phantom dependencies, which need `npm.Extend`.
- **Scripts:** denied by default works (esbuild needs no script; fsevents is not installed on
  Linux). An allowlisted node-gyp addon builds offline in its package derivation with bob's
  toolchain. Quirks table: small Go table with a test per entry.
- **Builders:** tsc, Vite (React, Svelte, workspace lib), esbuild bundle, vitest, ESLint, Bun all
  work hermetically. Bundles need store-path scrubbing.
- **`bob dev`:** Vite with HMR inside the namespace works as non-root; inotify sees saves from
  outside within ~3 ms. bob needs a generated wrapper config for `server.fs.allow`.

## 5. SPEC input

### 5.1 API [proposal]

```go
package npm // github.com/megakuul/bob/pkg/npm

// Registry dependency; req is an npm range or dist-tag. Resolved into bob.lock by `bob lock`.
func Dep(name, req string, opts ...DepOpt) Dependency
func Alias(as, name, req string) Dependency     // "lodash4": npm:lodash@^4
func Optional() DepOpt
func Dev() DepOpt                               // tooling only; not in runtime closures

// Package-level fixes declared by the user (not quirks):
func AllowScripts(pkgs ...string) Option        // lifecycle allowlist (R-NPM-004), exact names
func Extend(pkg string, deps ...Dependency) Option // phantom deps: npm.Extend("@fastify/cors", npm.Peer("fastify"))
func Peer(name string) Dependency
func Hoisted() Option                           // per-importer fallback layout (D30)

type Runtime int
const ( Node Runtime = iota; Bun )

// Local packages (workspace members are just bob packages, R-NPM-006).
func Library(p *bob.P, o LibOpts) *Lib          // tsc → dist + d.ts; linked by Name into consumers
func App(p *bob.P, o AppOpts) *App              // Node/Bun program: bundle + bin wrapper
func Vite(p *bob.P, o ViteOpts) *Site           // `vite build`; dev server in `bob dev`
func Test(p *bob.P, o TestOpts) *Test           // vitest/node --test

type LibOpts struct {
	Name     string              // package name, e.g. "@demo/ui"
	Deps     []Dependency        // npm.Dep(...), other *Lib handles
	Peers    []Dependency        // linked from the consumer
	TSConfig TSConfig            // generates tsconfig.json (R-NPM-008)
	Options  []Option
}
type AppOpts struct {
	Name, Entry string           // "src/server.ts"
	Deps        []Dependency
	Runtime     Runtime          // npm.Node (default) | npm.Bun
	Bundle      bool             // default true (esbuild); false = ship node_modules
	Options     []Option
}
type ViteOpts struct {
	Deps   []Dependency
	Config string                // default "vite.config.ts" (user-owned, D33)
	Dev    DevServer             // Port (default 5173), Host
}
```

Example:

```go
func Web(p *bob.P) *npm.Site {
	return npm.Vite(p, npm.ViteOpts{
		Deps: []npm.Dependency{
			npm.Dep("react", "^19.1.0"), npm.Dep("react-dom", "^19.1.0"),
			npm.Dep("vite", "^7.1.0", npm.Dev()), npm.Dep("@vitejs/plugin-react", "^5", npm.Dev()),
			npm.Dep("typescript", "~5.9.2", npm.Dev()), ui.Lib(p),
		},
	})
}
```

### 5.2 bob.lock npm section [proposal]

```json
{"npm": {
  "importers": {
    "web": {"deps": {"react": "react@19.3.1", "@demo/ui": "link:packages/ui",
                     "vite": "vite@7.3.7"}, "dev": ["vite"]}
  },
  "packages": {
    "use-sync-external-store@1.7.0(react@19.3.1)": {
      "name": "use-sync-external-store", "version": "1.7.0",
      "integrity": "sha512-<base64>",
      "deps": {"react": "react@19.3.1"}, "peers": ["react"]},
    "@rollup/rollup-linux-arm64-gnu@4.64.12": {
      "name": "@rollup/rollup-linux-arm64-gnu", "version": "4.64.12", "integrity": "sha512-…",
      "optional": true, "os": ["linux"], "cpu": ["arm64"], "libc": ["glibc"]},
    "better-sqlite3@12.11.1": {"name": "better-sqlite3", "version": "12.11.1", "integrity": "sha512-…",
      "deps": {"bindings": "bindings@1.5.0", "prebuild-install": "prebuild-install@7.1.3"},
      "hasInstallScript": true}
  }}}
```

Rules: key = `name@version` plus `(peer@version,…)` sorted when the package has resolved peers;
`tarball` only when not the default registry URL; `bundled` lists names that come in the
tarball; no `resolved`-per-platform: platform filtering happens at build time. All registry
data is validated (name charset/length, 128 MB per document, integrity algorithm in
{sha512, sha256, sha1 legacy}).

### 5.3 Derivation layout [proposal]

| Derivation | Name | Content | Inputs |
|---|---|---|---|
| tarball | `npm-<name>-<v>.tgz` | `builtin:fetchurl`, flat, hash = lock integrity | none |
| package instance | `npm-<key>` | `$out/node_modules/<name>/` (tarball, `--strip-components=1`) + `$out/node_modules/<dep alias>` → dep instance dir; cycle: `$out/<i>/node_modules/<name>` per member | coreutils, gnutar, gzip; + script toolchain only if allowed |
| importer node_modules | `node_modules-<importer>` | symlinks of direct deps + `.bin/<name>` wrappers (`exec <node> <realpath>`) | instances, nodejs, builder scripts |
| workspace lib | `ws-<name>` | built lib at `node_modules/<name>` + its runtime deps; peers from the consumer | |
| build | `<target>` | copies sources, `cp -a` node_modules into a writable dir, runs the tool | node_modules, tools |
| hoisted (fallback) | `node_modules-<importer>-hoisted` | npm-style tree, all tarballs extracted in one drv | tarballs |

Platform filter at build time: `os`, `cpu`, `libc` lists with `!x` negation; a non-optional
package that does not match is an error.

### 5.4 Lifecycle scripts and quirks [proposal]

- Default: deny. bob prints once per lock: `note: better-sqlite3@12.11.1 has an install script;
  bob does not run it. If the package needs it: npm.AllowScripts("better-sqlite3")`.
- Allowed: run `preinstall`, `install` (or `node-gyp rebuild` if `binding.gyp` and no
  install/preinstall), `postinstall` with `bash -euo pipefail -c`, cwd = package dir, no network,
  `HOME=$TMPDIR`, PATH = deps' bins + node-gyp wrapper + nodejs, python3, gnumake, cc, sed,
  grep, coreutils; `npm_config_nodedir=<nodejs>`; then keep only `build/Release/*.node`.
- Runtime error translation: `Could not locate the bindings file` from a store package with a
  denied script → `✗ better-sqlite3 needs its install script (native addon). Add
  npm.AllowScripts("better-sqlite3") to <importer>`.
- Phantom dependency translation: `TS2307 Cannot find module 'X'` or Node
  `ERR_MODULE_NOT_FOUND` raised **inside** `/nix/store/…-npm-P-…` → `✗ P uses X without declaring
  it. Add npm.Extend("P", npm.Peer("X"))`.
- Quirks table format (Go, shipped with bob, reviewed):

```go
type Quirk struct {
	Package string            // exact npm name
	Range   string            // node-semver range the entry applies to
	Why     string            // required justification, shown with -v
	Skip    bool              // never run the script (it only downloads/validates what bob provides)
	Tools   []string          // extra nixpkgs attributes for the script
	Env     map[string]string // extra env, e.g. npm_config_build_from_source=true
	Test    string            // command run in the package dir after the build (required)
}
// Entries proven by the spike:
// {esbuild >=0.15, Skip: postinstall only re-checks the @esbuild/<platform> binary}
// {better-sqlite3 >=8, Env build_from_source, Test: open :memory: and select sqlite_version()}
```

Admission rule: an entry only if the package cannot work with deny-or-allow alone; max size
reviewed per release; each entry's Test runs in bob's CI.

### 5.5 Dev-server contract (`bob dev`) [proposal]

1. Resolve/lock if bob.go changed; build importer `node_modules`; materialise into the working
   tree as a real directory of symlinks (atomic rename; keep `.vite`, `.cache`); write
   `.bob/node_modules.path`.
2. Generate `.bob/vite.bob.config.mjs` that imports the user's `vite.config.*` (object or
   function) and merges `server.fs.allow: [searchForWorkspaceRoot(cwd), "/nix/store"]`.
3. Start `vite --config .bob/vite.bob.config.mjs` inside the namespace with the dev env; workspace
   libraries link to the **working tree** (not the store) so their edits hot-reload.
4. On a dependency change in bob.go: relock, rebuild `node_modules`, swap, restart Vite.
   Codegen outputs regenerate into the working tree and reach Vite by its watcher.
5. Generate `.bob/bin/{node,tsc,tsserver,eslint,vite,…}` shims for editors.

### 5.6 Budgets [proposal] (reference profile [2c/4G], tarballs and toolchain cached)

| Operation | Budget | Measured [2c/4G] |
|---|---|---|
| `bob lock`, ~200 packages, warm registry cache | ≤ 4 s | 3.4 s |
| `bob lock` offline / unchanged deps (skip revalidation) | ≤ 1 s | 0.15–0.94 s (1.9 s ts-lib) |
| materialise node_modules, no-op | ≤ 0.5 s | 0.42 s |
| materialise after adding one dependency | ≤ 1.5 s | 0.69 s |
| cold node_modules, ~70 packages | ≤ 6 s | 4.2 s |
| save → HMR update (server side) | ≤ 50 ms p95 | median 3.3 ms, max 29 ms |
| tsserver first diagnostics through a shim | ≤ 4 s | 2.1–2.6 s [8c] |

## 6. Decisions, risks, open items

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S4-1 | Resolve from abbreviated registry documents; fetch the per-version document only for packages with `os`/`cpu` (for `libc`). | Full documents are 2–5x larger (typescript 15.8 MB vs 8.7 MB); libc is the only missing field. |
| S4-2 | Peer-aware instance keys (pnpm-style). | npm's tree resolves the wrong peer in peersplit; the isolated layout can be correct for free. |
| S4-3 | One derivation per package instance; cycles collapse into one derivation. | Principle 4; dependency change 0.7 s vs 2.3 s; Nix forbids reference cycles. |
| S4-4 | Package derivations do not depend on node; `.bin` wrappers live in the importer derivation. | A node bump must not rebuild every package. |
| S4-5 | Builders and the working tree get node_modules as a writable directory of symlinks. | Vite writes `.vite-temp`/`.vite` under node_modules. |
| S4-6 | Phantom dependencies are fixed by user `npm.Extend` plus an error hint, not by quirks. | D32: quirks must not become a catch-all; the class is common (all @fastify plugins). |
| S4-7 | Allowed scripts always get the C toolchain (cc, make, python3, sed, grep). | R-NPM-004; the lock cannot tell which packages have `binding.gyp`. |
| S4-8 | Bundled outputs scrub `/nix/store/<hash>-npm-` from bundles. | 51 npm paths otherwise end up in the runtime closure via comments. |
| S4-9 | Runtime outputs use nixpkgs `nodejs-slim`; build tools use `nodejs`. | Smaller closure; npm's node-gyp is only needed at build time. |
| S4-10 | git/file/URL specs are rejected with an error in v1. | Not in R-NPM-001; registry only keeps the fetch model simple. |

### Owner decision needed

**O-S4-1: Vite's `server.fs.allow` in `bob dev`.** Without it, files served from the store are
403. vite.config.ts is user-owned (D33).
- a) bob generates a wrapper config that imports the user's config and adds `/nix/store`
  (verified). Users see `.bob/vite.bob.config.mjs` in Vite's logs.
- b) Require users to add `server.fs.allow` themselves; bob checks and errors with the exact line.
- c) Materialise dependency files into the working tree (copy), so no store paths are served;
  costs 61 MiB and ~seconds per project and change.
- **Recommendation: a.** No user action, measured working, reversible.

**O-S4-2: Editors outside the namespace on hosts without `/nix`.** Shimmed tsserver/ESLint work,
but definitions point to `/nix/store/…`, which the editor cannot open there.
- a) Accept: recommend starting the editor from `bob dev` (inside the namespace) for full
  navigation; shims give diagnostics/completion everywhere.
- b) Materialise dependency **type** packages (`@types/*`, `.d.ts` of direct deps) as copies in
  the working tree for tooling.
- c) bob serves a FUSE/bind view of the store at a host path (needs extra privileges; conflicts with D11).
- **Recommendation: a** for v1, revisit with S1/S2 findings for gopls (same issue).

### Open risks

- Registry revalidation is one HTTP request per package (warm lock ≈ cold). A TTL or a "deps
  unchanged" short-cut is needed to meet the offline budget everywhere.
- JSON decoding of big documents dominates offline time on [2c/4G] (1.9 s for ts-lib). Caching a
  trimmed per-package form would help (not tried).
- Phantom dependencies will appear in more packages than the one found; bob's error
  translation is the main defence.
- node-gyp builds are slow (127 s for better-sqlite3) and pull a large toolchain; acceptable once
  per version with a cache.
- Dedupe differences against npm in graphs where npm's placement order matters were not observed
  but are possible; npm compatibility is not a requirement, correctness of the lock is.
- The spike stand-in reads package.json; R-NPM-008 (generate package.json/tsconfig from bob.go)
  is untested here.

### Still to verify

- Real editors (VS Code `typescript.tsdk`, ESLint extension) through shims; `-hide-nix` host.
- Automatic Vite restart on dependency change; codegen outputs reaching Vite (S6 path).
- SvelteKit build (`vite build` with adapter-static) and its `.svelte-kit` writes.
- x86_64 platform selection (lock is cross-platform; only aarch64 built).
- Quiet-machine rerun of `measure.sh 8c`, `measure.sh 2c`, and `… depchange`.
