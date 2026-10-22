# I-B4 (Track B: M15.2 identity → M15.3 OAuth flows) — internal/auth, new internal/store files, internal/http auth routes, cmd/bob-server admin

## Done
- Read AGENTS, impl-brief, HARNESS, I-B1/I-B3 progress, SPEC §6.3, §05.4.2/§05.4.5, tests/auth.

## Next
- Done (M15.2 + M15.3). Hand back.

## Done (M15.2, M15.3)
- internal/store/identity.go: UserRecord, NameRecord (uname), OidcSubRecord, UserIndexMeta/UserIndex (uidx P=200),
  RoleRecord, roles/saidx/oidcps index docs, RoleMembers (rmem), AdminIndex (adminidx/all, set of principals),
  ServiceAccountRecord (+tk), TokenRecord (tok/<sha256>), SessionRecord (sess/<sha256>), OAuthCodeRecord,
  DeviceCodeRecord + DeviceUserRecord, OidcStateRecord, OidcProviderRecord, OidcMappings. Key builders hash
  every secret (R-AUTH-007).
- internal/auth: Identity (api.Authenticator for bob_at_/bob_dt_/bob_sa_ and bob_st_ cookies; 10 s/2 s caches,
  local invalidation), argon2id hasher (2 slots, 5 s queue), throttle (5/name, 30/IP per 5 min), Secret type
  (redacts in String/GoString/JSON/slog), sessions (browser 12h/7d, cli 7d/30d, at 1h, dt = session),
  AuthService (LoginPassword cookie, Logout, ChangePassword, ListMySessions, RevokeMySession, WhoAmI with
  projects from rbac effective permissions, GetServerInfo with OIDC login options), UserService,
  ServiceAccountService (secret once, ≤20 tokens, last-used day), OidcService, RoleService (storage;
  decisions from internal/rbac: Catalog, CheckGrant, CheckRolePermissions, Scope, ValidProject, Match,
  ReplaceOIDC), OIDC RP (go-oidc + x/oauth2, PKCE, nonce, state record), OAuth endpoints
  (/oauth/authorize, POST /oauth/consent, /oauth/token code+PKCE / refresh (not rotated) / device,
  /oauth/device/code, /oauth/revoke, POST /device), fallback GET /oauth/consent + GET /device pages
  while the UI is not served, Bootstrap (BOB_ADMIN_TOKEN → real user "bootstrap-admin" with admin@*), CreateLocalUser.
- cmd/bob-server/parts_auth.go: authParts (after rbacParts in parts.go), `bob-server admin create-user --login <n> [--admin]`
  (password from stdin; main.go calls it). internal/http: Routes.Patterns + NixCacheInfo (GET /nix/nix-cache-info
  with the §6.6.1 401 until M16.1). internal/server/process.go: OrchestratorParts.Routes, .Bootstrap (static
  sa:bootstrap token only without identity). clock_bobtest.go: clock file re-read on mtime/size change
  (was every 200 ms: SetClock followed at once by a request saw the old offset).
- tools/devstack: exports BOB_TEST_CLOCK_FILE=<dir>/clock (authenv set it only after starting the devstack,
  so a stack used the previous stack's file or none).
- go.mod: go-oidc v3.17.0, go-jose v4.1.3, x/oauth2 v0.36.0, x/crypto v0.58.0 (indirect until tidy);
  ATTRIBUTION rows added (Apache-2.0 / BSD-3).
- Tests green: TestRAUTH001LocalAndOIDC, TestRAUTH004MappingsReevaluated, TestRAUTH007ServerIssuedTokens,
  TestRAUTH150OAuthFlows, TestRAUTH151CredentialHygiene, TestRAUTH151LogsStaySecretFree; tests/rbac
  TestRAUTH003AdminInvariants, TestRAUTH152NoEscalation; internal/auth unit tests.
- Still red/failing (not this unit): TestRAUTH005 only on /oci/token (501, registry M16.3); TestRAPI001
  (buf/connect-node, known TEST PROBLEM), TestRAPI150/RBACMatrix/RAUTH002 (seed, handlers), TestRAPI151
  (list RPCs of other services), TestRAPI004 (UI not served by internal/ui), TestRAUTH006 build/job ids
  "is required" before NotFound (api layer).

## DECISIONS
- D-IB4-1 SA ids: 1–40 chars `^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`: SPEC §6.1 slug needs ≥ 3, but SPEC's own
  examples and R-AUTH-005 use SA "ci". SPEC text should follow.
- D-IB4-2 A service-account token of scope download is refused on every /api/ method ("token not valid for
  the API"), also on WhoAmI (allow_download_scope applies to the CLI's bob_dt_, R-REG-154): R-AUTH-005.
- D-IB4-3 Updates without an etag are unconditional (still one CAS); a given etag must match (Aborted).
  tests/rbac R-AUTH-003 sends UpdateUser without etag; Delete* likewise.
- D-IB4-4 AdminIndex is `adminidx/all` holding the set of admin principals (len = count): idempotent
  add/remove; removal is CASed first (refused for the last admin) and undone if the principal write fails,
  so a crash can only undercount (the safe direction). R-STORE-104 needs a pk value, hence `/all`.
- D-IB4-5 Bootstrap: BOB_ADMIN_TOKEN (bobtest builds only) is the credential of a real local user
  "bootstrap-admin" bound to admin@* (created once, random password); the token is never stored.
- D-IB4-6 OIDC client secrets are stored unsealed in the control bucket (orchestrator-only, R-SEC-102):
  SPEC names an "orchestrator data key" nowhere defined. Seal when the key exists (NEEDS).
- D-IB4-7 require_mapping: proto3 bool cannot carry "default true"; the stored value is what the request
  says (false when unset). An OIDC login that would remove the last admin's oidc admin@* binding keeps it.
- D-IB4-8 Consent/device approvals: POST /oauth/consent and POST /device accept decision allow|approve|deny
  (UI uses approve, harness allow); Origin, when present, must be this server; cookie SameSite=Strict.
- D-IB4-9 Base URL for verification_uri / OIDC redirect: server.base_url, else scheme+Host of the request.
- D-IB4-10 Client IP for the throttle is the connection peer (server.trusted_proxies not read yet).
- D-IB4-11 Access tokens are listed in their CLI session (expired ones deleted at the next refresh;
  all deleted with the session); user `ss` keeps ≤ 100 sessions (oldest evicted and deleted).

## NEEDS
- Registry (M16.3): GET /oci/token must accept Basic with any bob token: use `Identity.HTTPPrincipal(r)`
  (Basic password or Bearer; api and download scopes; nil = anonymous). Last red part of TestRAUTH005.
- Cache (M16.1): replace parts.Routes["GET /nix/nix-cache-info"] (parts_auth.go) with the real /nix/ handler,
  authenticating with Identity.HTTPPrincipal.
- UI (internal/ui): when the UI is served, drop the fallback GET /oauth/consent and GET /device pages
  (auth.FallbackPatterns in parts_auth.go); the UI's /login must honour ?next=.
- CLI (M16.5 `bob login`): endpoints and JSON as SPEC §6.3.2; device flow verification_uri_complete is sent too.
- Coordinator: go mod tidy (go-oidc, go-jose, oauth2, crypto become direct/indirect correctly).
- I-B6 (parts.go owner): keep `authParts(env, &parts)` right after `rbacParts` (it was dropped once).
- Sealing of OIDC client secrets once an orchestrator data key exists (D-IB4-6).
- GC: orphaned tok/sess records of crashed writes and expired oac/devc/devu/oidcst records (no LIST allowed
  outside sweep) — a sweep or lifecycle rule.

## TEST PROBLEM

## Checkpoint (code written)
- internal/store/identity.go (records), internal/auth/{secret,password,identity,sessions,users,roles,sa,oidc,authsvc,http,bootstrap}.go,
  cmd/bob-server/auth.go (authParts, admin create-user), internal/http Routes.Patterns + NixCacheInfo,
  internal/server process.go (Routes, Bootstrap hook), clock_bobtest.go (stat-based re-read).
- Integrated I-B5: rbac.Authorizer for catalogue, CheckGrant, CheckRolePermissions, Scope, ValidProject, Match/ReplaceOIDC.
