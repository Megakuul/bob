# I-C1 (M16.5 CLI server side: bob login/logout/whoami, cache setup, pre-check, --remote, cancel, log --build) — pkg/cli, cmd/bob

## Done
- Read AGENTS, impl-brief, HARNESS, decisions, I-M4e/I-M4f/I-B3/I-B4/I-B9/I-B11, SPEC §02 (login, R-DEV-004/005,
  R-NIX-029, R-CLI-003/005), §06 (6.3.2 OAuth, 6.6.5, 6.9.7), tests/cli, tests/devenv, tests/server/srvtest,
  tests/chaos, tests/nix R-NIX-029, tests/errors faults 76/77.
- Layering finding: cmd/bob and pkg/ may not depend on internal/ even transitively (TestLayering runs
  `go list -deps ./cmd/bob`), and the glue layer may import no third-party module. So the server client
  cannot use internal/gen (connect-go stubs), and neither cmd/bob/remote nor internal/clientside is
  possible. The client is `pkg/cli/remote` (glue, std library only): Connect protocol by hand (unary
  POST/GET JSON, server-stream envelopes), OAuth flows, upload, and `remote.Executor`, which implements
  the existing `engine.RemoteExecutor` (SPEC R-FW-009). zstd comes from an E1 package: new file
  `pkg/engine/srcs/snapshot.go` (`srcs.Snapshot`, deterministic tar.zst; klauspost is E1-allowed).
- pkg/engine/engine.go: RemoteRequest.Test (additive).
- pkg/cli: creds.go (credentials.json 0600 under flock, temp+rename; config.json read-modify-write keeping
  unknown keys; server resolution; substituters), server.go (refresh < 60 s, WhoAmI pre-check, API error →
  BOB-E mapping of §02.3, nix.conf input sanitising), cmd_login.go (login: PKCE with $BROWSER/xdg-open,
  device flow, --token-stdin; logout with one best-effort revoke; whoami), cmd_remote.go (build/test
  --remote, Ctrl-C detach + second Ctrl-C cancel, cancel, log --build [-f] [--attempt]).
  openStore(check): Enter → pre-check (inside the namespace: once per command) → store.Open with
  bob's caches (config caches + logged-in servers) → build.Options.Caches. The outer (pre-namespace)
  process no longer prints its own 8001/run.finish after the child (duplicate JSON lines on Ctrl-C).
- Tests: pkg/cli/remote/remote_test.go (snapshot content/determinism/unsafe symlink, MatchLabel, error
  classes, executor end to end against a fake server incl. a broken watch stream and the second upload
  answered without body, PKCE S256).

## DECISIONS
- D-IC1-1 Server client in pkg/cli/remote with the std library (no connect-go / generated stubs):
  R-ARCH-002/003 forbid internal/ and third-party modules in glue. Messages mirrored as JSON structs.
- D-IC1-2 netrc password = the stored access token (bob_at_ / service token), not bob_dt_ as §6.6.5 says:
  tests/devenv R-DEV-004 requires `password <credentials accessToken>`; the cache accepts both scopes
  (I-B9 HTTPPrincipal). Consequence: after a refresh the netrc changes (written by the next store.Open).
- D-IC1-3 Several public keys of one server go into one Substituter.PublicKey joined by spaces (nix.conf
  trusted-public-keys is a space list). Keys are validated (`name:base64`) and the substituter URL must
  be http(s) without spaces, so a hostile WhoAmI cannot inject nix.conf settings.
- D-IC1-4 credentials.json entries carry an extra `nix` object (substituter + keys from the last WhoAmI),
  so every command writes the same nix.conf without a network call. All store-opening commands pass the
  logged-in servers; only build/test/run/--remote do the WhoAmI pre-check (R-NIX-029). Not passing the
  servers elsewhere would flip nix.conf between commands while a build runs.
- D-IC1-5 Project of `--remote`: BOB_SERVER_PROJECT, else the slug of the repository root's directory
  name. SPEC §02.6 sends project_id but never says where it comes from (SPEC gap; a RepoOpts field or a
  config entry would be cleaner — owner/SPEC).
- D-IC1-6 `--remote` without --platform sends the client's host platform (the outputs are downloaded
  for local use), not the server default.
- D-IC1-7 Detach (Ctrl-C) exits 130 after 2 s; SIGINTs within 250 ms of the first count once (the outer
  bob forwards the terminal's SIGINT to the namespace child, which also gets it from the terminal).
- D-IC1-8 `bob whoami [<url>]` added (not in the §02.1 table; requested for agent use; JSON `result`).
- D-IC1-9 A failed remote build: BOB-E-4012 whose excerpt lists the worker's errors; --json also emits
  each worker error as its own `error` event; the exit status counts 4012 plus the worker codes.
- D-IC1-10 bob logout revokes only user logins (refresh token, ends the CLI session); a service-account
  token belongs to its account and stays valid.
- D-IC1-11 GetLog/CancelBuild/CreateBuild are POST; only WhoAmI uses Connect GET (R-NIX-029 text).

## Verified against the real server (devstack, 18:58)
- `bob login --token-stdin <api_url>` with the admin token, `bob whoami --json`, credentials 0600, config
  defaultServer + registry default. `bob build --remote --json //...` → BOB-E-4012 "not implemented:
  /upload/" (NEEDS 1). Through a local proxy that stored the upload in bob-artifacts/src/<p>/<sha>.tar.zst
  (and a project created with CreateProject): CreateBuild accepted the snapshot, remote.submitted,
  WatchBuild stream → remote.state pending → queued → running; SIGTERM/Ctrl-C detached with
  `bob cancel <id>`; `bob cancel` → cancelled; second cancel → "already finished"; `bob log --build`.
- Fake WhoAmI server: 401 → BOB-E-4008 (exit 4, one WhoAmI hit, no Nix), 403 → 4010, unreachable →
  warning[BOB-E-4009] and the build continues.

## Tests
- Green: tests/cli TestRCLI003DeviceFlow, TestRCLI003PKCE, TestRCLI003TokenStdin, TestRCLI001*;
  tests/cli TestRCLI003Refresh/refresh-401; tests/devenv TestRDEV004CacheSetupAfterLogin; pkg/cli,
  pkg/cli/remote, pkg TestLayering. The `--remote` half of TestRCLI005NoServer passes.
- Still red, not this unit: TestRCLI003Refresh/refresh-ok and TestRCLI005NoServer (the acme fixture's
  local build/check/graph fail or time out: unregistered oci/appimage/release kinds), tests/nix
  TestRNIX029 (cpp-lib does not evaluate: cc API), server e2e (NEEDS below, TEST PROBLEM).
- pkg/engine/srcs TestImportManyTiming failed on budget (1m54 vs 5 s) under host load; snapshot.go does
  not touch Import.

## NEEDS
1. internal/http (server): `PUT /upload/source/sha256/<hex>` (SPEC §6.9.7) is not implemented (the
   /upload/ prefix answers 501). Every real `bob build --remote` stops there, so tests/server,
   tests/chaos, R-DEV-005, R-CLI-008 stay red until it exists (buildctl.checkUpload expects
   `src/<project>/<sha>.tar.zst`; the CLI sends Bearer, Bob-Project, application/zstd,
   Expect: 100-continue, exact Content-Length).
2. internal/auth: WhoAmI.nix (substituter + public_keys) is empty on the real server (I-B9 NEEDS to
   I-B4 still open): without it bob writes no substituter and `--remote` cannot download its outputs.
3. Server: CreateBuild answers NotFound "project not found" unless the project exists, but tests/server
   (srvtest), tests/chaos and tests/cli R-CLI-008 submit right after `bob login` without creating a
   project (see TEST PROBLEM 2).
4. Server: build ids are lower-case ULIDs ("01m5jfnx6ww3f50h3pv28fj5g3"); SPEC §02.6 shows `b-01JB7Q8V3K`
   and tests/devenv R-DEV-005 waits for `b-[0-9A-Z]{10,}` in bob's output.
5. SPEC: where the project id of `--remote` comes from (D-IC1-5); §6.6.5 netrc token kind (D-IC1-2).

## TEST PROBLEM
1. tests/devenv/login_test.go `rpc()` posts to `<api>/<method>` without the `/api/` prefix (same bug as
   test-corrections item 12(b)/(e)): TestRDEV005 fails at its harness step
   "RoleService/CreateRole: 501 not implemented: /" before bob runs; `buildState` has the same bug.
2. tests/server/srvtest, tests/chaos and tests/cli logs_test submit `bob build --remote` in a fresh
   repository with no project on the server; with the ProjectService in place CreateBuild answers
   "project not found". Either the tests create the project (named after the repo directory, D-IC1-5,
   or via BOB_SERVER_PROJECT) or the SPEC defines an implicit project for REMOTE builds (owner).
