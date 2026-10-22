# I-M6a progress (M6.1-M6.7: developer environment)

Owner of: pkg/cli/dev*.go (new), pkg/cli/devenv, pkg/engine/dev.go (new, DevPreparer types),
pkg/engine/build/dev.go (new, see D-M6a-1), pkg/golang/gen/dev.go (new, Go DevPreparer, see
D-M6a-2), small wiring edits in pkg/cli/cli.go (command table only), tools/pins.json.

## State
- 10:00 started; docs read; plan below.

## Plan
1. pkg/engine/dev.go: DevPreparer, DevPrep, DevPackage, DevServerSpec.
2. pkg/engine/build/dev.go: build.Dev(ctx, o, shell) → evaluate, lock stage, index, shell
   selection (1014/1020), ecosystem PrepareDev, resolved packages.
3. pkg/golang/gen/dev.go: Go toolchain + gopls, GOROOT/GOTOOLCHAIN/CGO_ENABLED.
4. pkg/cli/devenv: env.json, outside rules, shims, hook scripts, enter/leave scripts, allow
   store, eval stamps, LSP Rewrite/Pump, editor settings.
5. pkg/cli/dev*.go: dev, env, hook, allow, deny, exec, editor; completion additions.

## Checkpoint 18:20
Done (compiles, vet clean): pkg/engine/dev.go (DevPreparer/DevRequest/DevPrep/DevPackage/
DevServerSpec), pkg/engine/build/dev.go (build.Dev, HostPath), pkg/golang/gen/dev.go (Go
PrepareDev), pkg/engine/store/exec.go (StartIn, Inside), pkg/cli/devenv/{env,hook,allow,stamps,
editor,devenv(LSP Rewrite/Pump)}.go, pkg/cli/dev_{env,exec,shell,servers,editor}.go, completion
(--shell values, exec tools, bash ':' fix, fish empty token), cli.go table wiring only.
Green: TestRDEV010ProxyRewrite, TestRDEV010ProxyFraming. Manual: bob dev on devshell fixture
(R-DEV-001 cases), hook enter/fast/leave, exec/shims.
Next: devenv acceptance runs (scratch copy in out/_i-m6a with an acme-like fixture, removed at
the end), budgets, completion goldens candidates.

## DECISIONS
- D-M6a-1: no `pkg/engine/dev` package: pkg/layering_test.go (protected) fails for any pkg/
  package outside the R-ARCH-003 table ("is not in the R-ARCH-003 layer table"). The engine side
  is `pkg/engine/dev.go` (types, E2 pkg/engine) + `pkg/engine/build/dev.go` (build.Dev, E3,
  reuses the lock/index stages and the nixpkgs planner). If the coordinator wants a separate
  package, the layer table needs a row first (owner, protected test).
- D-M6a-2: engine.DevPreparer takes `DevRequest{PlanRequest, Shell, Servers}` (SPEC open item
  10 proposed `(ctx, PlanRequest, shell)`, which cannot tell an ecosystem which of its nodes are
  the shell's Servers) and returns DevPrep{Packages, Env, InsideEnv, ToolEnv, Tools, Files,
  Servers}: Env is exported outside when store-free, InsideEnv never (GOROOT, GOTOOLCHAIN).
  Go's PrepareDev is in pkg/golang/gen/dev.go (new file; "Go works today").
- D-M6a-3: Go in the dev shell: the root module's toolchain + nixpkgs gopls on PATH;
  CGO_ENABLED=0 (exported; acme's acceptance wants 0 although cmd/api uses cgo: the shell's go
  cannot link bob-built C libraries, cgo builds go through bob build/test), GOROOT and
  GOTOOLCHAIN=local inside only. GOPROXY is not set to off (SPEC example): third-party modules
  are not materialised for the shell yet, so go/gopls may download them.
- D-M6a-4: env.json is written after realising the shell's top-level packages, also by the
  hook: the tool set (= shims) is "every executable in bin/" (§02.7.2), which needs the
  outputs. §02.7.1 step 5 says the hook does not realise; listing bin/ without the outputs is
  not possible. Indirect GC roots: .bob/dev/gcroots/<shell>/root*.
- D-M6a-5: env.json carries two additions: `drvs` (closure path → derivation, so bob exec can
  realise a missing path without evaluating) and `packages` (name/version/path for the bob dev
  summary line). Non-default shells (`--shell ci`) use .bob/env.ci.json; .bob/env.json and the
  shims always belong to the default selection, so the hook and bob dev --shell x do not
  overwrite each other.
- D-M6a-6: hook fast path (R-DEV-007): BOB_PROJECT == root, sha256(env.json) == BOB_ENV_HASH,
  and the stamps file `<state>/devenv/eval-<16 hex>.json` (size/mtime/inode of every bob.go,
  go.mod, go.sum, bob.lock, every directory a bob.go could appear in, the eval index's inputs,
  and allow.json) all match by stat. It opens only env.json and the stamps file (under bob's
  state, name contains "eval"); allow.json is stat'ed, not opened. bob allow/deny rewrite
  allow.json, so the next prompt leaves the fast path and re-checks the trust list.
- D-M6a-7: leave restores BOB_ENV_RESTORE's values except PATH, which keeps the user's own
  changes and only loses <root>/.bob/bin. BOB_DENIED_SEEN is base64url JSON of the roots.
- D-M6a-8: `bob allow` also loads the environment (evaluate + realise + env.json), so the
  next prompt only activates it; an evaluation error is reported, the root stays allowed.
- D-M6a-9: bob exec does not re-execute bob in the namespace: the shim's bob reads env.json
  outside and starts the tool through the namespace child, which execs the tool in place
  (store.StartIn, new file pkg/engine/store/exec.go). In --lsp mode the shim's bob pumps the
  proxy and the server runs in the namespace (rule 1's "one bob process" holds; the bob is
  the outer one). Closure check = lstat of the host store paths; a missing one is realised
  from env.json's drvs.
- D-M6a-10: a repository without DevShell gets an implicit "default" shell (ecosystem tools
  only); `--shell x` naming no shell is 1014.
- D-M6a-11: `bob dev` reuses env.json when its stamps are fresh (R-DEV-006 no-op budget);
  otherwise it evaluates again. While the shell runs, a 2 s poll of the same stamps replaces
  inotify: a change recomputes the environment quietly, restarts changed servers and prints
  "dev environment changed …" once when `inside` differs.
- D-M6a-12: completion: bash splits COMP_LINE itself (COMP_WORDS breaks labels at ':'),
  fish passes the current token as "$(commandline -ct)" (fish 4 drops empty command
  substitutions), zsh drops empty candidates; `--shell` values come from .bob/shells (written
  at every dev evaluation), `bob exec -- <tool>` from .bob/bin.
- D-M6a-13: tools/pins.json gains "bash": "bashInteractive" (tests/devenv pinned("bash", …)
  looks the name up first; zsh, fish, strace, bashInteractive were already listed).

## NEEDS
1. Start-up cost of every bob command (hook ≤ 10 ms p95, shim ≤ 15 ms p95, completion ≤ 50 ms):
   package init compiles regexps eagerly. GODEBUG=inittrace=1 on `bob __complete` [8c/15G,
   load ~25]: ~6 ms of ~10 ms are init: pkg/engine/translate 1.6 ms, pkg/npm 1.3 ms,
   pkg/engine/nixidx 1.1 ms, pkg/golang 0.9 ms, pkg/bob 0.8 ms, pkg/engine/lock 0.65 ms,
   pkg/npm/resolve 0.45 ms. Owners: make package-level regexps lazy (sync.OnceValue) or
   simpler string checks. The hook/shim code paths of this unit open/exec nothing else.
2. Cold first command in a fresh HOME (tests/devenv uses a fresh HOME per test, 4 min per
   bob run, 2 min per shell prompt): measured `bob dev -- true` cold 322 s [8c/15G, load ~20],
   of which evaluation (evaluator Go from nixpkgs: nixpkgs fetch+import, go realise, evaluator
   seed + compile) is 312 s; build.Dev's own part (lock, index, gopls lookup, realise) ~10 s.
   Every devenv acceptance test's first bob call times out on this host for that reason
   (TestRDEV001DevShell's first case; the rest of it passes). Engine/eval owners: cold-path
   cost (nixpkgs source import, seed build).
3. tests/devenv uses acme everywhere (hook, trust, cost, shims, exec, signal, LSP, editor):
   acme does not evaluate yet (deploy/bob.go: oci.image, appimage.bundle, release kinds are
   M12; cc/rust/npm APIs are other units). Those tests stay red until then. Verified instead on
   a scratch copy (out/_i-m6a, deleted at the end) with an acme-like Go-only fixture.
4. Ecosystem owners (cc, rust, npm, ocaml): implement engine.DevPreparer (pkg/engine/dev.go):
   toolchains + LSP servers (clangd from clang-tools, rust-analyzer, typescript-language-server)
   as DevPrep.Packages, CC/CXX/PKG_CONFIG_PATH/RUSTC as InsideEnv, node_modules/.bin as Tools,
   materialised files as Files, npm.Vite's dev server as Servers (DevServerSpec: Name
   "<dir>-vite", Cmd ["vite","--config",".bob/vite.bob.config.mjs"], Ready "http", Port).
   R-DEV-010 clangd/rust-analyzer/typescript and R-DEV-002 (M9.6) depend on it.
5. **eval (pkg/engine/eval/scan.go), blocking R-DEV-003's nested-project case and every
   nested bob repo**: the scan must stop at a directory with its own bob.lock (a nested root,
   §1.2). Today it stops only at a go.mod without bob's header; since bob now writes a
   bob-headed go.mod into every bob repo root (requires github.com/megakuul/bob), a nested
   project's go.mod no longer stops the parent's scan, and the parent fails with "bob.Repo is
   allowed only in the root bob.go --> sub/bob.go:9" (reproduced: acme-like repo + nested
   sub/ with bob.lock, `bob allow` in sub (= bob gen there), then any evaluation of the parent).

## TEST PROBLEM
1. tests/devenv/shim_test.go TestRDEV008ExecAndShims (and SPEC R-DEV-008 acceptance "`sh -c
   '.bob/bin/go run ./x; echo $?'` with os.Exit(5) prints 5"): `go run` itself exits 1 when the
   program fails (it prints "exit status 5"); measured with the pinned go 1.26.8 inside
   `bob dev` without any shim: `go run ./x; echo $?` → "exit status 5", 1; Go source
   cmd/go/internal/base/base.go sets exit status 1. The shim passes the tool's status through
   (exit 7 of `sh -c 'exit 7'` via bob dev, 143 after SIGTERM). Fix: build the program and run
   the binary through the shim (`.bob/bin/go build -o x5 ./x && ./x5`), or expect 1.
2. tests/devenv/shim_test.go findTool panics ("index out of range [0] with length 0") on any
   process whose /proc/<pid>/cmdline is empty — every kernel thread and zombie — via
   `strings.Fields(c + " ")[0]`; TestRDEV008Signal can never pass on Linux. Fix: skip empty
   cmdlines. With that one-line fix in a scratch copy, see results below.
3. TestRDEV010LSPLocations' typescript case uses `bob dev --no-servers -- true` and `bob gen`
   in vite-react: npm (other unit). clangd/rust-analyzer cases need cc/rust DevPreparers.
6. eval rules (other unit, in progress at 20:30): a root bob.go `package acme` in a directory
   named al2 with no other Go files in it fails BOB-E-1024 "the Go code in this directory is
   package al2" — the package seems derived from the directory name when the directory has no
   Go code. Real repos are rarely cloned into a directory named like their package.

## Coordinator items (20:20) — done
- build.Dev plans the whole graph like bob build (sources import, planner with builder,
  fetches, drvs imported into the store, drv cache recorded) and passes the full PlanRequest
  (Builder, Sources, Resolver, Inputs = every planned node) to PrepareDev (R-ML-001b,
  R-RS-003a). DevPrep gains `Drvs` (extra derivations bob imports) and `Realise` (derivations
  realised by the CLI before tools run, GC-rooted under .bob/dev/gcroots/<shell>-prep).
- Go: GOPROXY=file://<gen.ProxyDir()>, GOFLAGS=-mod=readonly, GOSUMDB=off inside (shims,
  bob dev; not exported outside, so the user's other Go work is untouched); the directory is
  filled from bob.lock's go section on every dev evaluation (.mod for every module, .zip for
  packages:true ones; missing files are fetched once from the proxy bob reads, cached ones
  cost a stat) (I-M5b NEEDS 3). gopls keeps GOFLAGS=-tags=bob (tests/devenv checks it exactly).
- clangd shim (R-CC-003a), npm tsc shim/.bob/node_modules.path: provided by the cc and npm
  DevPreparers (Packages → bin/ shims, Tools → in-tree shims); nothing cc/npm-specific in the
  CLI.
