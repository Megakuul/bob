# I-B3 (Track B: bob-server process frame → M15.1) — cmd/bob-server, internal/server, internal/api, internal/http, proto/

## Done
- tools/go.mod: tool directives `google.golang.org/protobuf/cmd/protoc-gen-go` (v1.36.12) and
  `connectrpc.com/connect/cmd/protoc-gen-connect-go` (v1.21.1) (protected file: reported, D68).
- proto/buf.gen.yaml: local plugins only (`go tool protoc-gen-go`, `go tool protoc-gen-connect-go`,
  `protoc-gen-es` 2.16.0 on PATH). Generated internal/gen (Go + connect) and regenerated
  web/src/lib/gen (only the embedded file descriptors changed: managed-mode go_package option).
  Regeneration in a scratch copy (GOWORK=/root/bob/go.work, protoc-gen-es on PATH) is byte-identical.
- go.mod: connectrpc.com/connect v1.21.1, google.golang.org/protobuf v1.36.12 (still `// indirect`
  until `go mod tidy`). ATTRIBUTION.md rows for both.
- internal/api: rule table from descriptors (New refuses any method without a valid rule:
  missing option, unservable kind, PROJECT field path not a string, WORKER outside
  bob.worker.v1, download scope off AUTHENTICATED); one `guard` interceptor (unary + streaming):
  recover → authenticate per kind → authorize (GLOBAL, PROJECT with dotted project_field and
  Resolver; streaming checks the first received message) → error model (exactly one
  ErrorDetail; Unimplemented = "not implemented: <method>"; anything else Internal
  "internal error (ref <id>)", details only in the log). Interfaces Authenticator (+Chain,
  StaticToken), Authorizer (+AdminBindings), Resolver, WorkerAuthenticator (shape of
  orchestrator.Registrar.Authenticate). Built-in GetServerInfo, WhoAmI, ListPermissionCatalog.
  CRUD conventions: PageSize (50/500), scoped opaque page tokens, CheckETag (Aborted), CheckMask.
  Cookie sessions need `Bob-Csrf: 1`, cookie-only Connect GET refused, stale cookie ignored on
  PUBLIC methods, `download` tokens only where allow_download_scope.
- internal/http: endpoint map (/api/; other §6.2 routes answer 501 "not implemented: <route>"
  until their milestones), admin handler (/healthz, /readyz), Recover middleware,
  Listen (bind at start) / Serve (graceful shutdown with grace).
- internal/server: Config from env, Clock (bobtest only: BOB_TEST_CLOCK_OFFSET +
  BOB_TEST_CLOCK_FILE), Component/Run supervisor (panic/error → restart with 1–30 s backoff;
  Critical listeners end the process), Open (StorageConfigFromEnv + OpenStorage, worker root
  check BOB-E-7009), Orchestrator, Worker, OrchestratorParts/WorkerParts.
- cmd/bob-server: `orchestrator` / `worker` (/ `admin` = not implemented), SIGINT/SIGTERM →
  graceful stop, exit 0; refusals print "✗ <msg>\nhint: <hint>" and exit 78; ConfigError exit
  78; diag errors use diag.ExitCode. parts.go is the single wiring point for I-B2 and later
  milestones (controllers, worker loop, services, identity, RBAC, worker auth).
- Tests: internal/api/api_test.go (error model table, panic/plain error → Internal, WhoAmI
  POST == GET, cookie rules, streaming project check, rule refusal with a synthetic
  descriptor, conventions), internal/server/server_test.go (supervisor, config).
- Green: TestRSEC102OrchestratorRefusesReadableControlBucket, TestRSEC102PositivePathAndRefusal,
  TestRFLEET008WorkerRefusesNonRoot, TestRSTORE102RefusesBackendThatIgnoresIfNoneMatch
  (orchestrator half; worker half red until the M13.7 isolation check, D-IB3-7), TestRAPI002Resources,
  TestRAPI003BufBreaking, TestRAPI001/buf-lint, TestRAUTH006/server-permission-catalog.
- Manual: devstack up → orchestrator and worker running; WhoAmI with the admin token answers;
  SIGTERM → "orchestrator stopped", exit 0.

## Still red (not this unit / environment)
- TestRAPI001/generated-trees-committed: TEST PROBLEM below.
- TestRAPI001/whoami-same-everywhere: POST == GET passes; connect-es part needs
  `@connectrpc/connect-node` in web/node_modules (NEEDS I-UI).
- TestRAPI150ErrorModel: needs the devstack seed (projects p1/p2 + builds) and identity
  (CreateUser, LoginPassword: M15.2), RBAC roles (M15.4), BuildService.GetBuild + Resolver.
- TestRAPI151ListsAreGetOnly: needs CreateUser (M15.2) and every list RPC implemented over
  §05 index documents (each feature milestone); the framework issues no S3 request itself.

## DECISIONS
- D-IB3-1 connect-go v1.21.1 (`connectrpc.com/connect`), not v2.0.1: v1 is the version the
  S17 spike measured, is supported indefinitely, and its protocol is what connect-es 2.2.0
  (web/) speaks; v2 is a different API with no spike evidence.
- D-IB3-2 buf.gen.yaml uses local plugins (Go tools of tools/go.mod; protoc-gen-es from PATH)
  instead of remote BSR plugins: offline, pinned, no BSR account. Running it needs the
  workspace (`go tool` resolves through go.work) and protoc-gen-es 2.16.0 on PATH.
- D-IB3-3 BOB_ADMIN_TOKEN (HARNESS §13.2 contract) is honoured in every build as a bootstrap
  API credential, principal `sa:bootstrap`, binding admin@*, scope api; at least 32 chars; a
  warning is logged while it is set. SPEC §6.3.2 names `bob-server admin create-user` as the
  only way to the first admin: this env credential is a product addition the devstack/auth
  suites rely on (they build bob-server without -tags bobtest). **Owner item** (keep, or gate
  behind -tags bobtest and have devstack build with it).
- D-IB3-4 Listen address: `server.listen` = BOB_SERVER_LISTEN (SPEC mapping), with BOB_LISTEN
  (harness contract) as alias; default :8443. Admin listener 127.0.0.1:9090 by default; when the
  default address is busy (several orchestrators on one host) it is skipped with a warning; an
  explicitly set BOB_SERVER_ADMIN_LISTEN that cannot bind is a start error.
- D-IB3-5 BOB_SERVER_NAME is the process/orchestrator id (as devstack sets it); GetServerInfo
  reports it as server_name until settings (server name, base URL) exist. base_url from
  BOB_SERVER_BASE_URL (optional).
- D-IB3-6 Worker without a worker loop (I-B2 not wired yet) runs its start-up checks and then
  exits with BOB-E-9008 "bob-server worker loop is not implemented yet" (exit 70): an idle
  worker that looks healthy turned red e2e tests into real failures ("worker did not become
  active"). parts.go switches the loop on. The stacks detect the exit only if it happens within
  their 300 ms start wait (measured well inside it on this host).
- D-IB3-7 Worker host checks run first, before configuration and storage (tests/store/fleet
  TestRFLEET008* start the worker with no storage env): root (D56) → *server.HostRefusal
  "✗ the worker must run as root; builds themselves never run as root" / "hint: run it as a
  systemd system service or a privileged container", exit 7; then the isolation check hook
  `workerHostCheck` in cmd/bob-server/parts.go (BOB-E-7010, M13.7), a BOB-E-9008 stub for now.
  Consequence: `bob-server worker` is red (9008) for every test until M13.7 replaces the stub,
  including the worker half of TestRSTORE102RefusesBackendThatIgnoresIfNoneMatch.
- D-IB3-13 The orchestrator binds server.listen right after reading its configuration, before
  the storage checks, and serves only after them: clients connecting during start-up wait in the
  listen backlog instead of getting "connection refused" (seen once under load in tests/auth).
  A busy port is reported only after the storage checks (refusals win; TestRSEC102Positive
  PathAndRefusal runs a second orchestrator on the default :8443).
- D-IB3-8 Unimplemented RPCs answer "not implemented: <pkg.Service/Method>" with ErrorDetail
  BOB-API-9001; not-yet-built §6.2 routes answer 501 "not implemented: <route>". Both carry the
  harness's red marker so suites that now reach a running server stay red, not failing.
- D-IB3-9 PUBLIC methods ignore an invalid/expired cookie (anonymous) but refuse an invalid
  bearer token (Unauthenticated), so a stale browser cookie never blocks the login page.
- D-IB3-10 GLOBAL denial is PermissionDenied "permission denied: <method>" (no project, so no
  existence to hide); PROJECT without any permission is NotFound "<build|job|release|
  repository|project> not found", identical to a missing resource.
- D-IB3-11 ListPermissionCatalog descriptions are empty: generated Go descriptors carry no
  comments. A later step can embed a buf image with source info if the role editor needs them.
- D-IB3-12 Page tokens are "p1." + base64url("<list scope>:<position>"), bound to the list
  they came from; max 512 bytes.

## NEEDS
- I-B2 (internal/orchestrator, internal/worker): plug in through cmd/bob-server/parts.go:
  `OrchestratorParts{Controllers: []server.Component{...}, Workers: registrar, Services:
  api.Services{WorkerControl: ...}}`, `WorkerParts{Loop: ...}`; Env gives Config, Storage,
  Now (bobtest clock), Log, Getenv (for fleet.* settings). Components: Run(ctx) until ctx done;
  errors/panics are restarted with backoff. internal/fleet, internal/orchestrator files
  currently fail gofmt (theirs).
- I-UI (web/): add `@connectrpc/connect-node` 2.2.0 (devDependency) for the R-API-001 connect-es
  check; web/src/lib/gen is now written by `proto/buf.gen.yaml` (managed mode) — regenerate
  with it, not with a separate template, or TestRAPI001/generated-trees-committed diverges.
- Coordinator: `go mod tidy` (connect, protobuf, aws, cbor become direct).
- tools/ci gen stage: `buf generate` + compare (TODO M-proto in stage_gen.go) needs
  protoc-gen-es on PATH (pin it, e.g. via tools/pins.json nodejs + npm package).
- Whoever owns `out/uicheck`: it breaks `vet` (R-ARCH-001 path rule).

- Environment: /tmp (7.6 GB tmpfs) hit 100 % during this unit's runs: tests/harness leaves a
  53 MB `/tmp/bob-harness-bin-*` per test process (580 of them from all agents), authenv leaves
  `/tmp/bob-auth-devstack-*`. I removed such dirs older than 60 min (2026-10-22). The harness
  should build into one cached dir or clean up (protected tests: owner/coordinator item).

## TEST PROBLEM
- tests/server/srvtest/srvtest.go `Env.Call` posts to `<api_url>/<service>/<method>` without
  the `/api/` prefix that SPEC §6.1/§6.2 and tests/auth/authenv use (`<api_url>/api/...`);
  against the real server those calls get the 501 placeholder of "/". Fix belongs to srvtest.
- srvtest.Start: `bob login` answering BOB-E-9008 (CLI stub) is reported with t.Fatalf, not
  harness.Red, so once the server runs these tests fail as real instead of red.
- tests/auth/api_test.go TestRAPI001ConnectAndBuf/generated-trees-committed runs
  `go tool buf generate` with cmd.Dir = t.TempDir()/proto, outside any module: the go command
  answers `go: no such tool "buf"` (harness error) whatever buf.gen.yaml says. With
  GOWORK=/root/bob/go.work in the environment (and protoc-gen-es on PATH) the same steps give
  output byte-identical to the committed trees (checked 2026-10-22). Fix belongs to the test
  (set GOWORK to the repo's go.work, or run buf with --template from the repo).
