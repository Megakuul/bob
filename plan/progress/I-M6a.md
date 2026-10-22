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
