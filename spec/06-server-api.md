# §06 — Server API, auth, cache/registry, releases, pipelines, UI

> Source of SPEC.md: `/root/bob/SPEC.md` is generated from spec/01…06 by concatenation (command in the SPEC.md preamble). Edit this file, then regenerate.

Status: draft for the coordinator merge (Phase 2.3, 2026-10-21).
Scope: `proto/` (complete), RBAC per RPC, identity and tokens, HTTP endpoints (Nix cache, OCI,
release downloads, webhooks, OAuth), promotion, releases, forge adapters, web UI.
ADRs updated: 0014, 0018.

## 6.0 Interfaces

Provided by other sections (names as used here; the coordinator reconciles):

| From | Name | Used for |
|---|---|---|
| §05 | `ProjectRecord`, `RepoLinkRecord` (forge id + forge repo id → project id, §05.4.5) | project CRUD, webhook routing |
| §05 | `BuildRecord` (state machine §05.6.2, trigger, source `src`, pipelines `pi`, reported statuses `rp`, retry-of `rt`) | BuildService, statuses, promotion trigger |
| §05 | `JobRecord` (owner = worker id, epoch, kind, state, staging prefix, promotion `pm`), job state machine §05.6.1 | JobService, `GetCheckoutCredential`, promotion per job |
| §05 | `WorkerRecord` (state §05.6.3, platform, labels, caps), `OrchRecord` (endpoint, pin, heartbeat; public keys in `FleetConfig.k`) | FleetService |
| §05 | `UserRecord`, `RoleRecord`, role bindings (`ro` in `UserRecord`/`ServiceAccountRecord`), `ServiceAccountRecord`, `TokenRecord` (key = SHA-256 of token), `SessionRecord`, `OAuthCodeRecord`, `DeviceCodeRecord`, `OidcProviderRecord`, `OidcMappingRecord`, `AdminIndex` (count of admin bindings, CAS) (§05.4.2, §05.4.5) | auth, RBAC |
| §05 | `ForgeRecord` (credentials, sealed), `ForgeDeliveryRecord` (dedupe, create-only), `CheckoutRecord` (entry of `ftok/<sh2>`: job, epoch, handle, expiry, forge-expires; no token value) | forges |
| §05 | `ReleaseRecord` (state machine §05.6.4), release indexes `relv`/`rpage`/`ridx`; concurrency rule over `bbr` (§05.6.2) | releases, concurrency |
| §05 | `SettingRecord`, `SigningKeyRecord` (control, orchestrator only), `OciHmacKey`, `NotifyWebhookRecord` (§05.4.5) | settings |
| §05 | index documents / sequence slots for every list RPC (D60: GET only, no LIST/`Query`) and the page-token encoding over them | all `List*` RPCs |
| §05 | artifacts bucket layout (`staging/<buildID>/<jobID>/<attempt>/…`, `nix/…`, `oci/…`, `rel/<projectID>/<num10>/…`, `src/…`) and policies; lifecycle rules | cache, registry, uploads |
| §05 | lake tables `log_line`, `package_run`; worker in-memory live buffer; merge rule (ADR 0019) | LogService, AnalyticsService |
| §05 | wake-up semantics (D54), live-log pull (D55) and drain over the worker's outbound `WorkerControlService/Attach` stream, worker token `bwt1` + `Bob-PoP` (ADR 0013), `proto/bob/worker/v1/worker.proto` (§05.9.1) | LogService, FleetService, checkout |
| §01/§02 | pipeline definition (triggers, release outputs) in the evaluated graph IR; trigger matcher `pipeline.Match(event)` in `pkg/` | pipeline selection |
| §02 | CLI error codes (BOB-E-4001 cache credentials, 4008 login invalid, 4009 server unreachable, 4010 denied; BOB-API → BOB-E mapping in §02.3) and the `bob login`/`--remote` client side | auth flows, remote builds |
| §04 | release artifact kinds (closure, OCI index, AppImage, tarball) as build outputs | releases |

Provided by this section to others: all `proto/` packages except `bob/worker/v1` (§05.9.1) (§6.5), the authz option `bob.authz.v1.rule`,
HTTP endpoint map (§6.2), token formats (§6.3), `forge.Adapter` (§6.9), visibility document
format (§6.6), promotion protocol (§6.6.4), UI tokens (§6.10).

## 6.1 Conventions

- Module layout: `proto/buf.yaml` (one module), packages `bob.<area>.v1`, files
  `proto/bob/<area>/v1/<area>.proto`. Generated Go in `internal/gen/bob/<area>/v1`
  (`protoc-gen-go`, `protoc-gen-connect-go`), TypeScript in `web/src/lib/gen`
  (`@bufbuild/protoc-gen-es` v2). `pkg/` never imports generated server packages (R-ARCH-002); the
  `bob` CLI imports only `bob.auth.v1`, `bob.build.v1`, `bob.log.v1`, `bob.release.v1`.
- connectrpc handlers are mounted under `/api/` (e.g. `POST /api/bob.build.v1.BuildService/GetBuild`).
  Protocols: Connect (JSON and binary) and gRPC. Methods marked `idempotency_level = NO_SIDE_EFFECTS`
  also accept Connect GET.
- Ids: build, job, delivery ids are 26-char time-sortable ids (48-bit ms + 80 random bits,
  Crockford base32, lower case). Slugs (project, role, service account, release name, image name,
  integration, OIDC provider, webhook): `^[a-z][a-z0-9-]{1,38}[a-z0-9]$`. Users: `u` + 26-char id;
  local login names `^[a-z][a-z0-9._-]{1,62}$`.
- Lists: `page_size` (default 50, max 500), `page_token` (opaque, from §05's index encoding),
  response `next_page_token` (empty = end). Filters are fields of the request; free-text filter
  `query` matches name/id prefixes only (no scans, R-STORE-004).
- Updates: full resource + `google.protobuf.FieldMask update_mask` + `etag` (optimistic
  concurrency; mismatch → `Aborted`). Every resource message carries `etag`.
- Errors: connect codes as below, each with one `bob.common.v1.ErrorDetail`.

| Code | When | `ErrorDetail.code` |
|---|---|---|
| InvalidArgument | validation failure (field named) | `BOB-API-1001` |
| Unauthenticated | missing/invalid/expired credential | `BOB-API-2001` |
| PermissionDenied | principal has some permission on the project but not this method | `BOB-API-2002` |
| NotFound | missing, **or** principal has no permission at all on the project (no existence leak) | `BOB-API-3001` |
| AlreadyExists | create on an existing id | `BOB-API-3002` |
| FailedPrecondition | state forbids the action (e.g. retry a running build, delete last admin) | `BOB-API-3003` |
| Aborted | etag mismatch, or CAS lost 5 times | `BOB-API-3004` |
| ResourceExhausted | rate limit, stream limit | `BOB-API-4001` |
| Unavailable | storage unavailable / timeouts | `BOB-API-5001` |
| Internal | bug; message `internal error (ref <id>)`, details only in the server log | `BOB-API-9001` |

#### R-API-001 connectrpc + buf
- **Statement:** The API is the protobuf schema in §6.4, served with connectrpc; Go server and TS
  client are generated by `buf generate`.
- **Acceptance:** Given a clean checkout, when `buf lint` and `buf generate` run, then both exit 0
  and the generated trees equal the committed ones (`git diff --exit-code`). A connect-go client
  and a connect-es client each call `bob.auth.v1.AuthService/WhoAmI` against a test server and get
  the same JSON-decoded response.
- **Source:** R-API-001, D5.

#### R-API-002 CRUD resources
- **Statement:** Every resource of R-API-002 has the RPCs listed in §6.4 (Get/List/Create/Update/
  Delete where the resource is user-managed; read-only where it is system-produced).
- **Acceptance:** A table-driven test enumerates resources {projects, builds, jobs, workers,
  releases, images, cache entries, users, roles, permissions, service accounts, OIDC mappings,
  settings} and asserts the methods named in the §6.4 coverage table exist in the descriptors.
- **Source:** R-API-002.

#### R-API-003 lint and breaking checks
- **Statement:** CI runs `buf lint` (STANDARD rules) and `buf breaking --against` the main branch.
- **Acceptance:** A fixture change that renumbers a field fails `buf breaking`; the unchanged tree passes.
- **Source:** R-API-003.

#### R-API-004 UI uses only the public API
- **Statement:** The UI calls only `/api/*` connect endpoints and the documented auth endpoints of
  §6.2; there is no UI-only server route.
- **Acceptance:** Browser e2e: with request interception over all UI pages, every request path
  matches `^/(api/bob\.[a-z]+\.v1\.[A-Za-z]+/[A-Za-z]+|auth/oidc/(start|callback)|oauth/(authorize|consent)|_app/.*|[a-z0-9/_.-]*)$`
  and no non-static request goes outside `/api/`, `/auth/`, `/oauth/`.
- **Source:** R-API-004, R-UI-001.

#### R-API-150 Error model (new)
- **Statement:** Every error has a connect code and one `ErrorDetail` per the table above; NotFound
  is used instead of PermissionDenied when the principal has no permission on the project.
- **Acceptance:** Given user `u1` without any binding on project `p2`, when `u1` calls
  `GetBuild` for an existing build of `p2` and for a non-existent build id, then both answers are
  NotFound with identical messages `build not found`. Given `u1` with only `GetBuild` on `p2`,
  `CancelBuild` returns PermissionDenied `permission denied: bob.build.v1.BuildService/CancelBuild on project p2`.
- **Source:** R-AUTH-002, R-SEC-004.

#### R-API-151 List RPCs are GET-only (new)
- **Statement:** No RPC handler issues an S3 LIST or dynamitedb `Query` on control/fleet; lists
  read §05 index documents by known key.
- **Acceptance:** With an S3 proxy that counts operations, running the full API conformance suite
  issues 0 `ListObjects*` requests against control and fleet buckets.
- **Source:** D60, R-STORE-004.

## 6.2 HTTP endpoint map (orchestrator)

One listener (`server.listen`, default `:8443`, TLS or behind a TLS proxy with
`server.trusted_proxies`). A second listener `server.admin_listen` (default `127.0.0.1:9090`)
serves `/metrics`, `/healthz`, `/readyz` only.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| POST/GET | `/api/<package>.<Service>/<Method>` | per RPC (§6.5) | connectrpc API |
| GET | `/nix/nix-cache-info` | none; invalid token → 401 | Nix cache info |
| GET/HEAD | `/nix/<hash>.narinfo`, `/nix/nar/<hash>.nar.zst`, `/nix/<hash>.ls` | download token or none + visibility (§6.6) | Nix binary cache |
| GET | `/v2/`, `/v2/<repo>/manifests/<ref>`, `/v2/<repo>/blobs/<digest>`, `/v2/<repo>/tags/list`, `/v2/<repo>/referrers/<digest>` | OCI Bearer (§6.7) | OCI pull |
| GET | `/oci/token` | Basic (service account token or download/API token) or none | OCI token endpoint |
| GET/HEAD | `/r/<project>/<release>/<version>/<file>` | none if public, else Bearer or Basic | release asset download (§6.8) |
| GET | `/r/<project>/<release>/latest/<file>` | same | 302 to the highest version's URL |
| PUT | `/upload/source/sha256/<hex>` | Bearer API token; header `Bob-Project` | source snapshot for `--remote` (§6.9.7) |
| POST | `/hooks/<integration-id>` | webhook signature (§6.9.3) | forge webhooks |
| GET | `/oauth/authorize` | browser session (else redirect to `/login`) | OAuth code + PKCE (§6.3) |
| POST | `/oauth/token`, `/oauth/device/code`, `/oauth/revoke` | client `bob-cli` (public client) | token endpoint, device flow |
| GET | `/auth/oidc/start`, `/auth/oidc/callback` | — | OIDC relying party |
| GET | `/*` | — | embedded UI; unknown paths → `200.html` (SPA fallback); `Cache-Control: no-cache` for HTML, `immutable` for `/_app/immutable/*` |

All download endpoints stream from S3 through the orchestrator (D61); there are no presigned URLs
and no redirects to the storage backend.

## 6.3 Identity, tokens and login

### 6.3.1 Principals and credentials

| Principal | Credential | Format | TTL | Stored |
|---|---|---|---|---|
| user (local or OIDC) | API access token | `bob_at_` + base32(32 random bytes) | 1 h | `TokenRecord` key SHA-256(token) |
| user | refresh token (CLI) | `bob_rt_` + base32(32 B) | session: idle 7 d, max 30 d (settings) | `SessionRecord` |
| user | browser session cookie `bob_session` | `bob_st_` + base32(32 B) | idle 12 h, max 7 d | `SessionRecord` |
| user / service account | download token (netrc, OCI Basic, release Basic) | `bob_dt_` + base32(32 B) | = its session, or SA token expiry | `TokenRecord` with `scope=download` |
| service account | SA token | `bob_sa_` + base32(32 B) | default 90 d, max 1 y, or none if admin sets `no_expiry` | `TokenRecord` |
| OCI client | OCI bearer | `bob-oci-v1.<b64url(json)>.<b64url(HMAC-SHA256)>` (ADR 0014) | 15 min | stateless; HMAC key `OciHmacKey` |
| worker | worker token | `bwt1.` (ADR 0013) + `Bob-PoP` header | 1 h | stateless; worker record check |

- Token scopes: `api` (connect API + download endpoints) and `download` (only GET/HEAD on `/nix/*`,
  `/oci/token`, `/r/*`). A `download` token on `/api/` → Unauthenticated `token not valid for the API`.
  SA tokens carry a scope chosen at creation (default `download`).
- Lookup: `TokenRecord` GET by SHA-256(token); orchestrators cache positive results ≤ 10 s and
  negative results ≤ 2 s. Revocation (delete record) is effective ≤ 10 s.
- Passwords: argon2id (`golang.org/x/crypto/argon2`), m = 64 MiB, t = 3, p = 4, 16-byte salt,
  32-byte key, PHC string; at most 2 concurrent hash computations per orchestrator (queue, 5 s
  timeout → Unavailable). Length 12–256 bytes, no other composition rules.
- Login throttle: per login name 5 failures / 5 min, per client IP 30 / 5 min (per orchestrator,
  in memory) → ResourceExhausted `too many login attempts, try again in <n>s`. Failure message is
  always `invalid login or password`.
- Secrets never logged: `Secret` types redact in `String`, `GoString`, `MarshalJSON`, `slog.LogValuer`.

### 6.3.2 Flows

**UI login.** `/login` page offers local password (`AuthService/LoginPassword`) and each OIDC
provider (`/auth/oidc/start?provider=<id>&next=<path>`). Success sets `bob_session`
(`HttpOnly; Secure; SameSite=Strict; Path=/`). Cookie-authenticated `/api/` requests must send
`Bob-Csrf: 1` (forces a CORS preflight; the server sends no CORS headers), else Unauthenticated.
Connect GET with only a cookie is refused.

**CLI login (`bob login <url>`), authorization code + PKCE (RFC 7636, RFC 8252 loopback).**
```
CLI: listen 127.0.0.1:<random>; open browser
  GET /oauth/authorize?response_type=code&client_id=bob-cli&redirect_uri=http://127.0.0.1:<port>/callback
      &code_challenge=<S256>&code_challenge_method=S256&state=<rand>
server: no browser session → 302 /login?next=<authorize URL>; then consent page /oauth/consent
        ("Allow the bob CLI on <hostname from User-Agent> to act as <user>?") → 302 redirect_uri?code&state
CLI: POST /oauth/token grant_type=authorization_code&code&code_verifier&redirect_uri&client_id=bob-cli
server → {"access_token":"bob_at_…","token_type":"Bearer","expires_in":3600,
          "refresh_token":"bob_rt_…","bob_download_token":"bob_dt_…","bob_user":"u…"}
```
Only `redirect_uri` with host `127.0.0.1` or `[::1]`, path `/callback`, any port, is accepted.
Codes are single-use (`OAuthCodeRecord` deleted with If-Match), 60 s TTL.

**Device flow (RFC 8628, `bob login --device`).** `POST /oauth/device/code client_id=bob-cli` →
`{device_code, user_code: "ABCD-EFGH" (8 chars from BCDFGHJKLMNPQRSTVWXZ), verification_uri:
"<base>/device", expires_in: 600, interval: 5}`; the user enters the code on `/device` (needs a
browser session); polling answers `authorization_pending`, `slow_down`, `expired_token`,
`access_denied` per RFC 8628.

**Refresh.** `grant_type=refresh_token` returns a new access token; the refresh token is not
rotated (no CAS race between orchestrators); the session's idle timer is extended.

**OIDC (server is a relying party).** `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2`
(Apache-2.0/BSD-3). Code flow with PKCE, `state` and `nonce` stored in a 10 min record keyed by
SHA-256(state). After ID-token verification (issuer, audience, expiry, nonce): user = `UserRecord`
keyed by SHA-256(issuer ‖ 0 ‖ sub); email/name stored in control only (R-SEC-003). Mappings are
evaluated at each login: a mapping `{provider, claim, value, role, project}` matches when the claim
equals `value` (string) or contains it (array of strings). The user's `source=oidc` bindings are
replaced by the matched set; manual bindings are kept. If the provider has
`require_mapping = true` (default) and nothing matches, login fails with
`your account has no access to this bob server (no role mapping matched)`.

**Bootstrap.** `bob-server admin create-user --login <name> --admin` (reads the password from
stdin, uses the orchestrator's control credentials) creates a local user bound to `admin`. It is
the only way to create the first admin.

#### R-AUTH-001 OIDC and local users
- **Statement:** Users log in with a local password or through any configured OIDC provider; the
  server then issues its own tokens.
- **Acceptance:** Harness with a test OIDC provider (`bobtest-idp`, in-process): (1) local user
  `alice` with correct password → session cookie, `WhoAmI` returns `alice`; wrong password →
  Unauthenticated `invalid login or password`. (2) OIDC user with claim `groups:["dev"]` and a
  mapping `groups=dev → role dev on p1` → login works, `WhoAmI.bindings` contains `dev@p1`.
  (3) ID token with a wrong nonce → `/auth/oidc/callback` answers 400 and no session exists.
- **Source:** R-AUTH-001, D14, D19.

#### R-AUTH-004 OIDC claim → role mappings
- **Statement:** Mappings are re-evaluated at every OIDC login and replace only OIDC-sourced bindings.
- **Acceptance:** Given user with OIDC binding `dev@p1` and manual binding `viewer@*`, when the IdP
  drops `dev` from `groups` and the user logs in again, then bindings are exactly `viewer@*`.
  With `require_mapping=true` and no match, login fails with the message above.
- **Source:** R-AUTH-004, D14.

#### R-AUTH-005 Service accounts
- **Statement:** Service accounts have role bindings like users and any number of tokens with a
  scope (`api` or `download`) and expiry.
- **Acceptance:** Create SA `ci`, token scope `download` → token works for `/oci/token` Basic and
  `/nix/*` netrc, and gets Unauthenticated on `/api/`. Revoke the token → within 10 s all three
  endpoints answer 401/Unauthenticated. `CreateServiceAccountToken` returns the secret once;
  `ListServiceAccountTokens` returns only id, name, scope, expiry, last-used day.
- **Source:** R-AUTH-005, D19.

#### R-AUTH-007 Server-issued tokens
- **Statement:** The server issues and validates the token kinds of §6.3.1; tokens are stored only
  as SHA-256.
- **Acceptance:** After a CLI login, the control bucket contains no object whose bytes include the
  plaintext access, refresh or download token (grep of all objects). An expired access token gives
  Unauthenticated `token expired`; a refresh then succeeds.
- **Source:** R-AUTH-007, R-SEC-005.

#### R-AUTH-150 OAuth flows for the CLI (new)
- **Statement:** The server implements authorization code + PKCE with loopback redirect and the
  device flow for client `bob-cli`, as specified in §6.3.2.
- **Acceptance:** Scripted client: PKCE flow with a wrong `code_verifier` → `invalid_grant`; reused
  code → `invalid_grant`; `redirect_uri=http://evil.example/callback` → 400 without redirect;
  device flow before approval → `authorization_pending`, polling faster than 5 s → `slow_down`,
  after approval → tokens.
- **Source:** R-CLI-003, R-AUTH-007.

#### R-AUTH-151 Credential hygiene (new)
- **Statement:** argon2id parameters, throttles and CSRF rule as in §6.3.1/§6.3.2.
- **Acceptance:** (1) stored password hashes start with `$argon2id$v=19$m=65536,t=3,p=4$`.
  (2) 6 wrong passwords for one login within 5 min → the 6th gets ResourceExhausted.
  (3) cookie-only POST to `/api/bob.project.v1.ProjectService/CreateProject` without `Bob-Csrf`
  → Unauthenticated. (4) server log after the full auth suite contains none of the test passwords
  or tokens.
- **Source:** R-SEC-005, R-SEC-004.

## 6.4 RBAC

### 6.4.1 Model

```
Permission  = { method: "<package>.<Service>/<Method>" | "<package>.<Service>/*" | "*",
                project: "<project-id>" | "*" }
Role        = { id, description, permissions: [Permission] }        // built-in: admin = [{*, *}]
RoleBinding = { principal: "user:<id>" | "sa:<id>", role, project: "<project-id>" | "*", source: manual | oidc }
```

Effective permission of a binding for each role permission `rp`:
`project = rp.project` if `binding.project == "*"`; `binding.project` if `rp.project == "*"`;
`rp.project` if both are equal; otherwise none. A call is allowed iff some effective permission
matches the method (exact, service wildcard, or `*`) and the call's project (exact or `*`).

Each RPC declares its authorization rule with the method option `bob.authz.v1.rule` (§6.4.2 proto):

| Kind | Check |
|---|---|
| `PROJECT` | permission on the project named by `project_field` in the request (a top-level string field, or a dotted path to a nested one such as `project.id`; for ids like `build_id`, the handler resolves the build's project first; unknown id → NotFound) |
| `GLOBAL` | permission with project `*` |
| `LIST_FILTERED` | authenticated; results contain only projects where the principal has the method; an explicit `project_id` filter without permission → NotFound |
| `AUTHENTICATED` | any valid API principal (own data only) |
| `PUBLIC` | no credential needed; handler applies resource visibility, then `PROJECT` for private resources |
| `WORKER` | `bwt1` token + valid `Bob-PoP` (ADR 0013); not part of RBAC; only on `bob.worker.v1.WorkerControlService` (§05.9) |

Built-in `admin`: cannot be updated or deleted (FailedPrecondition `role admin is built in`).
`AdminIndex` counts `admin@*` bindings of enabled principals; any write that would make it 0
(remove binding, disable/delete user, delete SA) fails with FailedPrecondition
`at least one admin must remain`. The check and the binding write are CAS on `AdminIndex`.

HTTP downloads reuse RPC permissions: Nix cache paths → `bob.cache.v1.CacheService/GetPath`, OCI
pulls → `bob.registry.v1.RegistryService/GetRepository`, release assets →
`bob.release.v1.ReleaseService/GetRelease`, source upload → `bob.build.v1.BuildService/CreateBuild`,
each on the owning project.

#### R-AUTH-002 RPC × project permissions
- **Statement:** Authorization is evaluated exactly per §6.4.1.
- **Acceptance:** Table test over (role permission, binding project, call project, method) with
  ≥ 30 rows, including: role `{BuildService/*, *}` bound `@p1` allows `CancelBuild` on p1, denies
  on p2; role `{BuildService/GetBuild, p1}` bound `@p2` grants nothing; `{*, *}` bound `@*` allows all.
- **Source:** R-AUTH-002, D14′.

#### R-AUTH-003 Only built-in role is admin
- **Statement:** A fresh server has exactly one role, `admin`; it is immutable and at least one admin remains.
- **Acceptance:** Fresh server: `ListRoles` returns `[admin]`. `UpdateRole(admin)` and
  `DeleteRole(admin)` → FailedPrecondition. With one admin, `RemoveRoleBinding` of that binding,
  `UpdateUser{disabled:true}` and `DeleteUser` → FailedPrecondition `at least one admin must remain`;
  two orchestrators removing the two last admin bindings concurrently leave exactly one.
- **Source:** R-AUTH-003, D39.

#### R-AUTH-006 Every RPC has a rule
- **Statement:** Every method in every service carries `bob.authz.v1.rule` with a kind other than
  `UNSPECIFIED`, and `PROJECT` rules name an existing string field; the interceptor refuses
  methods without a rule.
- **Acceptance:** Go test over `protoregistry.GlobalFiles` fails on a missing rule (proved by a
  fixture service without options). For each `PROJECT`/`GLOBAL` method, a generated test calls it
  with a principal that has no bindings and asserts NotFound or PermissionDenied (never OK).
- **Source:** R-AUTH-006.

#### R-AUTH-152 No privilege escalation through bindings (new)
- **Statement:** `CreateRoleBinding` succeeds only if every effective permission of the new
  binding is also an effective permission of the caller.
- **Acceptance:** Caller with role `{RoleService/*, *}` bound `@p1` and `{BuildService/GetBuild, *}`
  bound `@p1`: binding `viewer(GetBuild)@p1` to another user → OK; binding `admin@p1` →
  PermissionDenied `binding would grant permissions you do not have`; any binding `@*` → PermissionDenied.
- **Source:** R-AUTH-002, R-SEC-004.

## 6.5 `proto/` (complete draft)

### 6.5.1 Module files

```yaml
# proto/buf.yaml
version: v2
modules:
  - path: .
lint:
  use: [STANDARD]
breaking:
  use: [FILE]
```

```yaml
# proto/buf.gen.yaml
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: github.com/megakuul/bob/internal/gen
plugins:
  - remote: buf.build/protocolbuffers/go
    out: ../internal/gen
    opt: paths=source_relative
  - remote: buf.build/connectrpc/go
    out: ../internal/gen
    opt: paths=source_relative
  - remote: buf.build/bufbuild/es
    out: ../web/src/lib/gen
    opt: target=ts
```

Plugins are pinned in the repo's `bob.go` toolchain (local plugins from nixpkgs in CI; remote
names above only identify them). `buf breaking` runs against `main`.

Shorthand used below: every `rpc` carries `option (bob.authz.v1.rule) = {...}`. Read-only RPCs
also set `option idempotency_level = NO_SIDE_EFFECTS;` (written as `// GET` to save space; the
real file contains the option). All files start with:

```proto
syntax = "proto3";
import "google/protobuf/timestamp.proto";
import "google/protobuf/field_mask.proto";
import "bob/authz/v1/authz.proto";
import "bob/common/v1/common.proto";
```
(imports a file does not use are left out in the real file; `buf lint` checks this).

Verified 2026-10-21 (re-run at integration): the 15 files below plus §05.9.1's
`bob/worker/v1/worker.proto`, expanded mechanically (syntax line, needed imports, `// GET`
markers kept as comments; script `/root/scratch-specint/extract.py`), pass `buf build` and
`buf lint` (STANDARD) with buf 1.73.0; 98 RPCs, 98 `rule` options.

### 6.5.2 `bob/authz/v1/authz.proto`

```proto
syntax = "proto3";
package bob.authz.v1;
import "google/protobuf/descriptor.proto";

enum Kind {
  KIND_UNSPECIFIED = 0;
  KIND_PROJECT = 1;        // permission on the request's project (see project_field, resolve)
  KIND_GLOBAL = 2;         // permission with project "*"
  KIND_LIST_FILTERED = 3;  // authenticated; results filtered to permitted projects
  KIND_AUTHENTICATED = 4;  // any valid API principal
  KIND_PUBLIC = 5;         // no credential; handler applies visibility, then PROJECT for private data
  KIND_WORKER = 6;         // bwt1 worker token + Bob-PoP (bob.worker.v1 only)
  reserved 7;              // was KIND_ORCHESTRATOR: workers serve no RPCs (outbound Attach, §05.9)
  reserved "KIND_ORCHESTRATOR";
}

enum Resolve {
  RESOLVE_UNSPECIFIED = 0; // project_field holds a project id
  RESOLVE_BUILD = 1;       // project_field holds a build id
  RESOLVE_JOB = 2;         // job id
  RESOLVE_RELEASE = 3;     // release id
  RESOLVE_REPOSITORY = 4;  // OCI repository name "<project>/<image>"
}

message Rule {
  Kind kind = 1;
  string project_field = 2;        // KIND_PROJECT only; string field of the request, or a dotted path to a nested string field ("project.id")
  Resolve resolve = 3;
  bool allow_download_scope = 4;   // KIND_AUTHENTICATED: also accept bob_dt_ tokens
}

extend google.protobuf.MethodOptions {
  Rule rule = 50001;
}
```

### 6.5.3 `bob/common/v1/common.proto`

```proto
package bob.common.v1;

message ErrorDetail {
  string code = 1;   // BOB-API-NNNN (§6.1)
  string field = 2;  // request field path for InvalidArgument
  string hint = 3;
}

enum Visibility {
  VISIBILITY_UNSPECIFIED = 0;
  VISIBILITY_PRIVATE = 1;
  VISIBILITY_PUBLIC = 2;
}

message Principal {
  string id = 1;            // "user:u01j…" | "sa:ci"
  string display_name = 2;
}

message RoleBindingRef {
  string role = 1;
  string project = 2;       // project id or "*"
}

// A bob label and where it is defined (R-FW-016).
message PackageRef {
  string label = 1;         // //svc/api:server
  string platform = 2;      // linux/amd64 | linux/arm64
  string variant = 3;       // "" = default
  string defined_at = 4;    // svc/api/bob.go:17:9
}
```

### 6.5.4 `bob/auth/v1/auth.proto`

```proto
package bob.auth.v1;

service AuthService {
  rpc GetServerInfo(GetServerInfoRequest) returns (GetServerInfoResponse) {          // GET
    option (bob.authz.v1.rule) = {kind: KIND_PUBLIC};
  }
  rpc WhoAmI(WhoAmIRequest) returns (WhoAmIResponse) {                               // GET
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED, allow_download_scope: true};
  }
  // Sets the bob_session cookie. Local users only.
  rpc LoginPassword(LoginPasswordRequest) returns (LoginPasswordResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_PUBLIC};
  }
  // Ends the calling session (cookie or refresh token); clears the cookie.
  rpc Logout(LogoutRequest) returns (LogoutResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED};
  }
  rpc ChangePassword(ChangePasswordRequest) returns (ChangePasswordResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED};
  }
  rpc ListMySessions(ListMySessionsRequest) returns (ListMySessionsResponse) {       // GET
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED};
  }
  rpc RevokeMySession(RevokeMySessionRequest) returns (RevokeMySessionResponse) {
    option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED};
  }
}

message NixInfo {
  string substituter = 1;            // https://bob.example/nix
  repeated string public_keys = 2;   // all ACTIVE and VERIFY_ONLY keys, "name:base64"
}
message LoginOption {
  string provider_id = 1;            // "" = local password
  string display_name = 2;
  string start_url = 3;              // /auth/oidc/start?provider=<id>
}
message GetServerInfoRequest {}
message GetServerInfoResponse {
  string server_name = 1;
  string base_url = 2;
  string version = 3;
  NixInfo nix = 4;
  repeated LoginOption login_options = 5;
  string oci_registry = 6;           // host[:port] for docker/podman
}
message WhoAmIRequest {}
message WhoAmIResponse {
  bob.common.v1.Principal principal = 1;
  string login = 2;
  repeated bob.common.v1.RoleBindingRef bindings = 3;
  repeated string projects = 4;      // projects with at least one permission ("*" if any {*, *})
  string token_scope = 5;            // api | download | session
  google.protobuf.Timestamp token_expires_at = 6;
  NixInfo nix = 7;
  bool must_change_password = 8;
}
message LoginPasswordRequest { string login = 1; string password = 2; }
message LoginPasswordResponse { bob.common.v1.Principal principal = 1; bool must_change_password = 2; }
message LogoutRequest {}
message LogoutResponse {}
message ChangePasswordRequest { string old_password = 1; string new_password = 2; }
message ChangePasswordResponse {}   // all other sessions of the user are revoked
message Session {
  string id = 1;
  string kind = 2;                   // cli | browser
  string user_agent = 3;
  google.protobuf.Timestamp created_at = 4;
  google.protobuf.Timestamp last_used_at = 5;   // day precision
  bool current = 6;
}
message ListMySessionsRequest {}
message ListMySessionsResponse { repeated Session sessions = 1; }
message RevokeMySessionRequest { string session_id = 1; }
message RevokeMySessionResponse {}
```

### 6.5.5 `bob/access/v1/access.proto`

```proto
package bob.access.v1;

service UserService {
  rpc CreateUser(CreateUserRequest) returns (CreateUserResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetUser(GetUserRequest) returns (GetUserResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }            // GET
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }      // GET
  rpc UpdateUser(UpdateUserRequest) returns (UpdateUserResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteUser(DeleteUserRequest) returns (DeleteUserResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc SetUserPassword(SetUserPasswordRequest) returns (SetUserPasswordResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

service RoleService {
  rpc CreateRole(CreateRoleRequest) returns (CreateRoleResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetRole(GetRoleRequest) returns (GetRoleResponse) { option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED}; }     // GET
  rpc ListRoles(ListRolesRequest) returns (ListRolesResponse) { option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED}; } // GET
  rpc UpdateRole(UpdateRoleRequest) returns (UpdateRoleResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteRole(DeleteRoleRequest) returns (DeleteRoleResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }    // refused while bound
  // Every RPC with its rule, generated from the descriptors (for the role editor and matrix).
  rpc ListPermissionCatalog(ListPermissionCatalogRequest) returns (ListPermissionCatalogResponse) { option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED}; } // GET
  // project "*" in the binding needs a {CreateRoleBinding, *} permission (KIND_PROJECT with value "*").
  rpc CreateRoleBinding(CreateRoleBindingRequest) returns (CreateRoleBindingResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project"}; }
  rpc DeleteRoleBinding(DeleteRoleBindingRequest) returns (DeleteRoleBindingResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project"}; }
  rpc ListRoleBindings(ListRoleBindingsRequest) returns (ListRoleBindingsResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; } // GET
}

service ServiceAccountService {
  rpc CreateServiceAccount(CreateServiceAccountRequest) returns (CreateServiceAccountResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetServiceAccount(GetServiceAccountRequest) returns (GetServiceAccountResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }     // GET
  rpc ListServiceAccounts(ListServiceAccountsRequest) returns (ListServiceAccountsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc UpdateServiceAccount(UpdateServiceAccountRequest) returns (UpdateServiceAccountResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteServiceAccount(DeleteServiceAccountRequest) returns (DeleteServiceAccountResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc CreateServiceAccountToken(CreateServiceAccountTokenRequest) returns (CreateServiceAccountTokenResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc ListServiceAccountTokens(ListServiceAccountTokensRequest) returns (ListServiceAccountTokensResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc RevokeServiceAccountToken(RevokeServiceAccountTokenRequest) returns (RevokeServiceAccountTokenResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

service OidcService {
  rpc CreateOidcProvider(CreateOidcProviderRequest) returns (CreateOidcProviderResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetOidcProvider(GetOidcProviderRequest) returns (GetOidcProviderResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }      // GET
  rpc ListOidcProviders(ListOidcProvidersRequest) returns (ListOidcProvidersResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc UpdateOidcProvider(UpdateOidcProviderRequest) returns (UpdateOidcProviderResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteOidcProvider(DeleteOidcProviderRequest) returns (DeleteOidcProviderResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc CreateOidcMapping(CreateOidcMappingRequest) returns (CreateOidcMappingResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc ListOidcMappings(ListOidcMappingsRequest) returns (ListOidcMappingsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }    // GET
  rpc DeleteOidcMapping(DeleteOidcMappingRequest) returns (DeleteOidcMappingResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

enum UserSource { USER_SOURCE_UNSPECIFIED = 0; USER_SOURCE_LOCAL = 1; USER_SOURCE_OIDC = 2; }
message User {
  string id = 1;                       // u + 26-char id (output only)
  string login = 2;                    // local: chosen; OIDC: username_claim value (display only)
  string display_name = 3;
  string email = 4;
  UserSource source = 5;               // output only
  string oidc_provider_id = 6;         // output only
  bool disabled = 7;
  bool must_change_password = 8;
  google.protobuf.Timestamp created_at = 9;
  google.protobuf.Timestamp last_login_at = 10;
  string etag = 11;
}
message CreateUserRequest { User user = 1; string initial_password = 2; }  // creates a LOCAL user
message CreateUserResponse { User user = 1; }
message GetUserRequest { string id = 1; }
message GetUserResponse { User user = 1; }
message ListUsersRequest { int32 page_size = 1; string page_token = 2; string query = 3; UserSource source = 4; bool include_disabled = 5; }
message ListUsersResponse { repeated User users = 1; string next_page_token = 2; }
message UpdateUserRequest { User user = 1; google.protobuf.FieldMask update_mask = 2; } // display_name, email (local), disabled, must_change_password
message UpdateUserResponse { User user = 1; }
message DeleteUserRequest { string id = 1; string etag = 2; }  // revokes sessions and tokens, deletes bindings
message DeleteUserResponse {}
message SetUserPasswordRequest { string id = 1; string new_password = 2; bool must_change = 3; }
message SetUserPasswordResponse {}

message Permission {
  string method = 1;    // "bob.build.v1.BuildService/CancelBuild" | "bob.build.v1.BuildService/*" | "*"
  string project = 2;   // project id | "*"
}
message Role {
  string id = 1;
  string description = 2;
  repeated Permission permissions = 3;   // ≤ 500
  bool built_in = 4;                     // output only; true only for admin
  string etag = 5;
}
message CreateRoleRequest { Role role = 1; }
message CreateRoleResponse { Role role = 1; }
message GetRoleRequest { string id = 1; }
message GetRoleResponse { Role role = 1; }
message ListRolesRequest { int32 page_size = 1; string page_token = 2; string query = 3; }
message ListRolesResponse { repeated Role roles = 1; string next_page_token = 2; }
message UpdateRoleRequest { Role role = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateRoleResponse { Role role = 1; }
message DeleteRoleRequest { string id = 1; string etag = 2; }
message DeleteRoleResponse {}
message CatalogEntry {
  string method = 1;          // full method name
  string kind = 2;            // PROJECT | GLOBAL | LIST_FILTERED | AUTHENTICATED | PUBLIC
  string description = 3;     // first line of the rpc comment
}
message ListPermissionCatalogRequest {}
message ListPermissionCatalogResponse { repeated CatalogEntry entries = 1; }  // excludes WORKER / ORCHESTRATOR
enum BindingSource { BINDING_SOURCE_UNSPECIFIED = 0; BINDING_SOURCE_MANUAL = 1; BINDING_SOURCE_OIDC = 2; }
message RoleBinding {
  string id = 1;              // output only
  bob.common.v1.Principal principal = 2;
  string role = 3;
  string project = 4;         // project id | "*"
  BindingSource source = 5;   // output only
  google.protobuf.Timestamp created_at = 6;
}
message CreateRoleBindingRequest { string principal_id = 1; string role = 2; string project = 3; }
message CreateRoleBindingResponse { RoleBinding binding = 1; }
message DeleteRoleBindingRequest { string id = 1; string project = 2; }    // project must equal the binding's
message DeleteRoleBindingResponse {}
message ListRoleBindingsRequest { int32 page_size = 1; string page_token = 2; string principal_id = 3; string role = 4; string project = 5; }
message ListRoleBindingsResponse { repeated RoleBinding bindings = 1; string next_page_token = 2; }

message ServiceAccount {
  string id = 1;              // slug
  string description = 2;
  bool disabled = 3;
  google.protobuf.Timestamp created_at = 4;
  string etag = 5;
}
enum TokenScope { TOKEN_SCOPE_UNSPECIFIED = 0; TOKEN_SCOPE_API = 1; TOKEN_SCOPE_DOWNLOAD = 2; }
message ServiceAccountToken {
  string id = 1;
  string name = 2;
  TokenScope scope = 3;
  google.protobuf.Timestamp expires_at = 4;    // unset = no expiry (needs no_expiry)
  google.protobuf.Timestamp last_used_on = 5;  // day precision
  google.protobuf.Timestamp created_at = 6;
}
message CreateServiceAccountRequest { ServiceAccount service_account = 1; }
message CreateServiceAccountResponse { ServiceAccount service_account = 1; }
message GetServiceAccountRequest { string id = 1; }
message GetServiceAccountResponse { ServiceAccount service_account = 1; }
message ListServiceAccountsRequest { int32 page_size = 1; string page_token = 2; string query = 3; }
message ListServiceAccountsResponse { repeated ServiceAccount service_accounts = 1; string next_page_token = 2; }
message UpdateServiceAccountRequest { ServiceAccount service_account = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateServiceAccountResponse { ServiceAccount service_account = 1; }
message DeleteServiceAccountRequest { string id = 1; string etag = 2; }
message DeleteServiceAccountResponse {}
message CreateServiceAccountTokenRequest { string service_account_id = 1; string name = 2; TokenScope scope = 3; int32 ttl_days = 4; bool no_expiry = 5; }
message CreateServiceAccountTokenResponse { ServiceAccountToken token = 1; string secret = 2; }   // secret returned once
message ListServiceAccountTokensRequest { string service_account_id = 1; }
message ListServiceAccountTokensResponse { repeated ServiceAccountToken tokens = 1; }             // ≤ 20 tokens per SA
message RevokeServiceAccountTokenRequest { string service_account_id = 1; string token_id = 2; }
message RevokeServiceAccountTokenResponse {}

message OidcProvider {
  string id = 1;
  string display_name = 2;
  string issuer_url = 3;            // https only
  string client_id = 4;
  string client_secret = 5;         // input only; never returned
  bool has_client_secret = 6;       // output only
  repeated string scopes = 7;       // default ["openid","profile","email"]
  string username_claim = 8;        // default "preferred_username"
  bool require_mapping = 9;         // default true
  string redirect_url = 10;         // output only: <base>/auth/oidc/callback
  string etag = 11;
}
message OidcMapping {
  string id = 1;
  string provider_id = 2;
  string claim = 3;                 // e.g. "groups"
  string value = 4;
  string role = 5;
  string project = 6;               // project id | "*"
}
message CreateOidcProviderRequest { OidcProvider provider = 1; }
message CreateOidcProviderResponse { OidcProvider provider = 1; }
message GetOidcProviderRequest { string id = 1; }
message GetOidcProviderResponse { OidcProvider provider = 1; }
message ListOidcProvidersRequest {}
message ListOidcProvidersResponse { repeated OidcProvider providers = 1; }   // ≤ 10
message UpdateOidcProviderRequest { OidcProvider provider = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateOidcProviderResponse { OidcProvider provider = 1; }
message DeleteOidcProviderRequest { string id = 1; string etag = 2; }
message DeleteOidcProviderResponse {}
message CreateOidcMappingRequest { OidcMapping mapping = 1; }
message CreateOidcMappingResponse { OidcMapping mapping = 1; }
message ListOidcMappingsRequest { string provider_id = 1; }
message ListOidcMappingsResponse { repeated OidcMapping mappings = 1; }      // ≤ 500 per provider
message DeleteOidcMappingRequest { string id = 1; }
message DeleteOidcMappingResponse {}
```

### 6.5.6 `bob/project/v1/project.proto`

```proto
package bob.project.v1;

service ProjectService {
  rpc CreateProject(CreateProjectRequest) returns (CreateProjectResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetProject(GetProjectRequest) returns (GetProjectResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id"}; }  // GET
  rpc ListProjects(ListProjectsRequest) returns (ListProjectsResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; }          // GET
  rpc UpdateProject(UpdateProjectRequest) returns (UpdateProjectResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project.id"}; }
  // Refused while non-terminal builds exist. Releases must be deleted first (R-STORE-007).
  rpc DeleteProject(DeleteProjectRequest) returns (DeleteProjectResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Pipelines from the newest evaluation of the default branch.
  rpc ListPipelines(ListPipelinesRequest) returns (ListPipelinesResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project_id"}; } // GET
}

enum LinkHealth { LINK_HEALTH_UNSPECIFIED = 0; LINK_HEALTH_OK = 1; LINK_HEALTH_REPO_NOT_FOUND = 2; LINK_HEALTH_AUTH_FAILED = 3; LINK_HEALTH_WEBHOOK_MISSING = 4; }
message Project {
  string id = 1;                          // slug; immutable; used in OCI repos and release URLs
  string display_name = 2;
  string description = 3;
  string forge_integration_id = 4;
  string repo_full_name = 5;              // "acme/api"
  int64 repo_id = 6;                      // output only (adapter LookupRepo)
  string default_branch = 7;              // output only
  string repo_web_url = 8;                // output only
  LinkHealth link_health = 9;             // output only
  google.protobuf.Timestamp created_at = 10;
  string etag = 11;
}
message CreateProjectRequest { Project project = 1; }     // links the repo, ensures the webhook
message CreateProjectResponse { Project project = 1; }
message GetProjectRequest { string id = 1; }
message GetProjectResponse { Project project = 1; }
message ListProjectsRequest { int32 page_size = 1; string page_token = 2; string query = 3; }
message ListProjectsResponse { repeated Project projects = 1; string next_page_token = 2; }
message UpdateProjectRequest { Project project = 1; google.protobuf.FieldMask update_mask = 2; } // display_name, description, forge_integration_id, repo_full_name
message UpdateProjectResponse { Project project = 1; }
message DeleteProjectRequest { string id = 1; string etag = 2; }
message DeleteProjectResponse {}
enum Concurrency { CONCURRENCY_UNSPECIFIED = 0; CONCURRENCY_SUPERSEDE = 1; CONCURRENCY_QUEUE = 2; CONCURRENCY_PARALLEL = 3; }
message Pipeline {
  string name = 1;
  repeated string triggers = 2;           // human form: "push main", "pull_request", "tag v*"
  Concurrency concurrency = 3;
  bool releases = 4;                      // produces releases
  bob.common.v1.PackageRef defined_at = 5;
  string last_build_id = 6;
  string last_state = 7;                  // BuildState name
}
message ListPipelinesRequest { string project_id = 1; }
message ListPipelinesResponse { repeated Pipeline pipelines = 1; string evaluated_sha = 2; }
```

### 6.5.7 `bob/build/v1/build.proto`

Enum values mirror §05's `BuildRecord`/`JobRecord` state machines; §05 is authoritative and the
coordinator aligns names.

```proto
package bob.build.v1;

service BuildService {
  rpc CreateBuild(CreateBuildRequest) returns (CreateBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project_id"}; }
  rpc GetBuild(GetBuildRequest) returns (GetBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }   // GET
  rpc ListBuilds(ListBuildsRequest) returns (ListBuildsResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; }                                    // GET
  rpc CancelBuild(CancelBuildRequest) returns (CancelBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }
  // New build (trigger RETRY, retry_of set, attempt+1) of the failed/cancelled pipelines; succeeded packages are cache hits.
  rpc RetryBuild(RetryBuildRequest) returns (RetryBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }
  // New build with the same trigger and source (trigger kind RERUN, rerun_of set).
  rpc RerunBuild(RerunBuildRequest) returns (RerunBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }
  rpc GetBuildGraph(GetBuildGraphRequest) returns (GetBuildGraphResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; } // GET
  // Server stream: current build first, then changes; ends when the build is terminal.
  rpc WatchBuild(WatchBuildRequest) returns (stream WatchBuildResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_BUILD}; }
}

service JobService {
  rpc GetJob(GetJobRequest) returns (GetJobResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_JOB}; }            // GET
  rpc ListJobs(ListJobsRequest) returns (ListJobsResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; } // GET
}

// States and phases are §05.6.2's (BuildRecord `st`, `ph`).
enum BuildState {
  BUILD_STATE_UNSPECIFIED = 0;
  BUILD_STATE_PENDING = 1;      // created, waiting for planning or for an older build (queue rule)
  BUILD_STATE_QUEUED = 2;       // first job enqueued, not yet claimed
  BUILD_STATE_RUNNING = 3;
  BUILD_STATE_SUCCEEDED = 4;
  BUILD_STATE_FAILED = 5;
  BUILD_STATE_CANCELLED = 6;
  BUILD_STATE_SUPERSEDED = 7;   // cancelled by a newer build (concurrency rule)
  BUILD_STATE_SKIPPED = 8;      // no pipeline matched the event
}
enum BuildPhase {
  BUILD_PHASE_UNSPECIFIED = 0;
  BUILD_PHASE_EVALUATING = 1;
  BUILD_PHASE_BUILDING = 2;
  BUILD_PHASE_PROMOTING = 3;
}
enum TriggerKind {
  TRIGGER_KIND_UNSPECIFIED = 0;
  TRIGGER_KIND_PUSH = 1;
  TRIGGER_KIND_TAG = 2;
  TRIGGER_KIND_PULL_REQUEST = 3;
  TRIGGER_KIND_API = 4;         // API or UI
  TRIGGER_KIND_REMOTE = 5;      // bob build/test --remote
  TRIGGER_KIND_RERUN = 6;
  TRIGGER_KIND_RETRY = 7;       // RetryBuild (§05: a retry is a new build with retry-of set)
}
message Trigger {
  TriggerKind kind = 1;
  string ref = 2;               // refs/heads/main, refs/tags/v1.4.0, refs/pull/7/head
  int64 pull_request = 3;
  bool from_fork = 4;
  string sender = 5;            // forge login (display only)
  string delivery_id = 6;
  bob.common.v1.Principal principal = 7;   // API/REMOTE/RERUN
  string rerun_of = 8;
  string superseded_by = 9;
  string retry_of = 10;
}
message GitSource {
  string ref = 1;
  string sha = 2;               // empty on create = resolved by the worker at checkout, then set
  string fetch_ref = 3;         // PRs: refs/pull/N/head | refs/merge-requests/N/head
}
message Source {
  oneof kind {
    GitSource git = 1;
    string upload_sha256 = 2;   // PUT /upload/source/sha256/<hex> first
  }
}
message PackageCounts {
  int32 total = 1; int32 cached = 2; int32 built = 3; int32 running = 4;
  int32 queued = 5; int32 failed = 6; int32 skipped = 7; int32 cancelled = 8;
}
message ErrorSummary {
  string code = 1;              // BOB-E-NNNN (§02 taxonomy)
  string message = 2;
  bob.common.v1.PackageRef package = 3;
  string hint = 4;
}
message PipelineRun {
  string name = 1;
  BuildState state = 2;
  string release_id = 3;        // set when the pipeline published a release
}
message Build {
  string id = 1;
  string project_id = 2;
  int64 number = 3;             // per-project sequence, shown as #N
  BuildState state = 4;
  Trigger trigger = 5;
  Source source = 6;
  repeated PipelineRun pipelines = 7;
  repeated string targets = 8;  // labels / patterns for API and REMOTE builds
  repeated string platforms = 9;
  repeated string variants = 10;
  int32 attempt = 11;           // 1 + number of retries in the retry-of chain
  PackageCounts counts = 12;
  repeated ErrorSummary errors = 13;   // ≤ 20, first failures
  google.protobuf.Timestamp created_at = 14;
  google.protobuf.Timestamp started_at = 15;
  google.protobuf.Timestamp finished_at = 16;
  string web_url = 17;          // <base>/p/<project>/builds/<id>
  string etag = 18;
  BuildPhase phase = 19;        // while RUNNING
}
message CreateBuildRequest {
  string project_id = 1;
  Source source = 2;
  string pipeline = 3;          // run one pipeline (ignores its triggers), or
  repeated string targets = 4;  // build/test these labels (REMOTE when sent by the CLI)
  repeated string platforms = 5;   // default: pipeline/package defaults
  repeated string variants = 6;
  bool test = 7;                // targets are tests (bob test --remote)
  string reason = 8;            // ≤ 200 bytes, shown in the UI
}
message CreateBuildResponse { Build build = 1; }
message GetBuildRequest { string id = 1; }
message GetBuildResponse { Build build = 1; }
message ListBuildsRequest {
  int32 page_size = 1; string page_token = 2;
  string project_id = 3; repeated BuildState states = 4; string ref = 5;
  string pipeline = 6; TriggerKind trigger_kind = 7;
  google.protobuf.Timestamp created_after = 8;
}
message ListBuildsResponse { repeated Build builds = 1; string next_page_token = 2; }   // newest first
message CancelBuildRequest { string id = 1; }
message CancelBuildResponse { Build build = 1; }
message RetryBuildRequest { string id = 1; }
message RetryBuildResponse { Build build = 1; }
message RerunBuildRequest { string id = 1; }
message RerunBuildResponse { Build build = 1; }

enum PackageState {
  PACKAGE_STATE_UNSPECIFIED = 0;
  PACKAGE_STATE_PENDING = 1;    // waiting for dependencies
  PACKAGE_STATE_QUEUED = 2;
  PACKAGE_STATE_BUILDING = 3;
  PACKAGE_STATE_CACHED = 4;     // substituted (bob cache or cache.nixos.org)
  PACKAGE_STATE_SUCCEEDED = 5;
  PACKAGE_STATE_FAILED = 6;
  PACKAGE_STATE_SKIPPED = 7;    // a dependency failed
  PACKAGE_STATE_CANCELLED = 8;
}
message PackageNode {
  bob.common.v1.PackageRef package = 1;
  string drv = 2;               // /nix/store/…drv (shown only in detail/-v views)
  PackageState state = 3;
  string job_id = 4;
  repeated int32 deps = 5;      // indexes into GetBuildGraphResponse.nodes
  google.protobuf.Timestamp started_at = 6;
  google.protobuf.Timestamp finished_at = 7;
  ErrorSummary error = 8;
  repeated string outputs = 9;  // store paths (after success)
}
message GetBuildGraphRequest { string build_id = 1; }
message GetBuildGraphResponse { repeated PackageNode nodes = 1; bool complete = 2; }  // complete=false before evaluation finished
message WatchBuildRequest { string id = 1; }
message WatchBuildResponse {
  Build build = 1;                       // always set
  repeated int32 changed_nodes = 2;      // indexes; client re-fetches GetBuildGraph when the length changes
  repeated PackageNode nodes = 3;        // the changed nodes
}

// Kinds and states are §05's (JobSpec.Kind, JobRecord `st`); promotion progress is Job.promotion.
enum JobKind { JOB_KIND_UNSPECIFIED = 0; JOB_KIND_EVALBUILD = 1; JOB_KIND_BATCH = 2; JOB_KIND_EVAL = 3; }
enum JobState {
  JOB_STATE_UNSPECIFIED = 0;
  JOB_STATE_QUEUED = 1;
  JOB_STATE_CLAIMED = 2;
  JOB_STATE_RUNNING = 3;
  JOB_STATE_SUCCEEDED = 4;
  JOB_STATE_FAILED = 5;
  JOB_STATE_CANCELLED = 6;
}
message Job {
  string id = 1;
  string build_id = 2;
  JobKind kind = 3;
  JobState state = 4;
  string platform = 5;
  string worker_id = 6;
  int32 attempt = 7;
  int64 epoch = 8;
  int32 drv_count = 9;
  string promotion = 10;        // "", "blocked: staging incomplete", "failed: artifact verification", "done"
  repeated string non_reproducible = 11;   // store paths (R-NFR-006)
  google.protobuf.Timestamp created_at = 12;
  google.protobuf.Timestamp claimed_at = 13;
  google.protobuf.Timestamp finished_at = 14;
  ErrorSummary error = 15;
}
message GetJobRequest { string id = 1; }
message GetJobResponse { Job job = 1; }
message ListJobsRequest { string build_id = 1; int32 page_size = 2; string page_token = 3; }
message ListJobsResponse { repeated Job jobs = 1; string next_page_token = 2; }
```

### 6.5.8 `bob/log/v1/log.proto`

Replaces the push-based `LiveLogService` and `PeerLogService` of ADR 0019 (D55): orchestrators
pull from workers over the worker-opened `Attach` stream (`LiveSubscribe`/`LiveChunk`, §05.9.1,
§05.10.3); there is no orchestrator↔orchestrator service. `LogLine`, `Cursor`, `Restart` and
`Final` carry the fields of §05.9.1's `LiveLine`/`LiveCursor`/`LiveRestart`/`LiveFinal`.

```proto
package bob.log.v1;

service LogService {
  // Persisted log of a build or one package, ordered by (package, seq).
  rpc GetLog(GetLogRequest) returns (GetLogResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; }   // GET
  // Server stream: persisted + live lines after the cursor, gap-free and duplicate-free (ADR 0019 merge).
  // While at least one follower exists, the orchestrator pulls the running workers' in-memory logs.
  rpc FollowLog(FollowLogRequest) returns (stream FollowLogResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; }
  rpc SearchLog(SearchLogRequest) returns (SearchLogResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "build_id", resolve: RESOLVE_BUILD}; } // GET
}

enum Stream { STREAM_UNSPECIFIED = 0; STREAM_STDOUT = 1; STREAM_STDERR = 2; STREAM_BOB = 3; }
message LogLine {
  string package = 1;          // label
  int64 seq = 2;               // per (build, package), from 1, continues across attempts
  int32 attempt = 3;
  Stream stream = 4;
  int64 ts_unix_nano = 5;
  string text = 6;             // UTF-8, ≤ 4 KiB; rendered inert by every client
}
message Cursor { int64 seq = 1; int32 attempt = 2; }
message Restart { string package = 1; int32 attempt = 2; int64 from_seq = 3; }
message Final { string package = 1; int64 last_seq = 2; int32 attempt = 3; }

message GetLogRequest { string build_id = 1; string package = 2; int64 after_seq = 3; int32 limit = 4; }  // limit default 10,000, max 100,000
message GetLogResponse { repeated LogLine lines = 1; bool truncated = 2; }
message FollowLogRequest { string build_id = 1; string package = 2; map<string, Cursor> cursor = 3; }
message FollowLogResponse {
  repeated LogLine lines = 1;
  repeated Restart restarts = 2;   // client drops its lines of that package with seq >= from_seq
  repeated Final end = 3;
  bool done = 4;
  bool delayed = 5;                // true while no live source is reachable (lake only)
}
message SearchLogRequest { string build_id = 1; string term = 2; string package = 3; }  // term 3..200 bytes, case-sensitive
message SearchLogResponse { repeated LogLine hits = 1; bool truncated = 2; }            // ≤ 1,000 hits
```

### 6.5.9 `bob/fleet/v1/fleet.proto`

```proto
package bob.fleet.v1;

service FleetService {
  rpc ListWorkers(ListWorkersRequest) returns (ListWorkersResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }      // GET
  rpc GetWorker(GetWorkerRequest) returns (GetWorkerResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }            // GET
  // Worker finishes current jobs and claims no new ones.
  rpc DrainWorker(DrainWorkerRequest) returns (DrainWorkerResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc UndrainWorker(UndrainWorkerRequest) returns (UndrainWorkerResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // ADR 0013 revocation: state=revoked, gen+1; effective ≤ 10 s; the process must restart with new keys.
  rpc RevokeWorker(RevokeWorkerRequest) returns (RevokeWorkerResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc ListOrchestrators(ListOrchestratorsRequest) returns (ListOrchestratorsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
}

enum WorkerState {
  WORKER_STATE_UNSPECIFIED = 0;
  WORKER_STATE_ACTIVE = 1;
  WORKER_STATE_DRAINING = 2;
  WORKER_STATE_STALE = 3;      // heartbeat older than fleet.gone_after (5 min, §05.6.3)
  WORKER_STATE_REVOKED = 4;
}
message Worker {
  string id = 1;
  WorkerState state = 2;
  string platform = 3;
  map<string, string> labels = 4;
  repeated string capabilities = 5;     // kvm, big-parallel, …
  string version = 6;
  int32 cpus = 7;
  int64 memory_bytes = 8;
  repeated string job_ids = 9;
  google.protobuf.Timestamp registered_at = 10;
  google.protobuf.Timestamp last_heartbeat = 11;
}
message Orchestrator {
  string id = 1;
  string endpoint = 2;
  string version = 3;
  google.protobuf.Timestamp started_at = 4;
  google.protobuf.Timestamp last_heartbeat = 5;
  repeated string leader_of = 6;        // controller leader leases held (ADR 0011)
}
message ListWorkersRequest { int32 page_size = 1; string page_token = 2; string platform = 3; repeated WorkerState states = 4; string query = 5; }
message ListWorkersResponse { repeated Worker workers = 1; string next_page_token = 2; }
message GetWorkerRequest { string id = 1; }
message GetWorkerResponse { Worker worker = 1; }
message DrainWorkerRequest { string id = 1; }
message DrainWorkerResponse { Worker worker = 1; }
message UndrainWorkerRequest { string id = 1; }
message UndrainWorkerResponse { Worker worker = 1; }
message RevokeWorkerRequest { string id = 1; string reason = 2; }
message RevokeWorkerResponse { Worker worker = 1; }
message ListOrchestratorsRequest {}
message ListOrchestratorsResponse { repeated Orchestrator orchestrators = 1; }
```

### 6.5.10 `bob/cache/v1/cache.proto`

```proto
package bob.cache.v1;

service CacheService {
  rpc GetCacheStats(GetCacheStatsRequest) returns (GetCacheStatsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }   // GET
  // Metadata of one served store path. Visible paths only (§6.6.2); otherwise NotFound.
  // Also the permission name that gates /nix/* downloads of private paths.
  rpc GetPath(GetPathRequest) returns (GetPathResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }                       // GET
  rpc ListTopPackages(ListTopPackagesRequest) returns (ListTopPackagesResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; } // GET
}

message GetCacheStatsRequest {}
message GetCacheStatsResponse {
  int64 nar_bytes = 1;            // compressed bytes under nix/nar/ (from the daily GC sweep)
  int64 paths = 2;
  double hit_rate_24h = 3;        // narinfo 200 / (200 + 404), all orchestrators
  double hit_rate_7d = 4;
  int32 retention_unreferenced_days = 5;
  google.protobuf.Timestamp measured_at = 6;
}
message GetPathRequest { string store_hash = 1; }   // 32-char Nix base32
message GetPathResponse {
  string store_path = 1;
  int64 nar_size = 2;
  int64 file_size = 3;
  repeated string references = 4;
  repeated string signatures = 5;
  repeated string projects = 6;   // only projects the caller may see
  bool public = 7;
  string build_id = 8;            // first publication
  google.protobuf.Timestamp promoted_at = 9;
}
message TopPackage { string project_id = 1; string label = 2; int64 hits_7d = 3; int64 nar_bytes = 4; }
message ListTopPackagesRequest { string project_id = 1; int32 limit = 2; }    // limit ≤ 100
message ListTopPackagesResponse { repeated TopPackage packages = 1; }
```

### 6.5.11 `bob/release/v1/release.proto`

```proto
package bob.release.v1;

service ReleaseService {
  rpc ListReleases(ListReleasesRequest) returns (ListReleasesResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }   // GET; public + permitted
  rpc GetRelease(GetReleaseRequest) returns (GetReleaseResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }         // GET
  // bob install: "<project>/<name>@<constraint>" + platform → release and closure roots.
  rpc ResolveRelease(ResolveReleaseRequest) returns (ResolveReleaseResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; } // GET
  rpc UpdateRelease(UpdateReleaseRequest) returns (UpdateReleaseResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "release.id", resolve: RESOLVE_RELEASE}; }
  // The only way a release is removed (R-STORE-007).
  rpc DeleteRelease(DeleteReleaseRequest) returns (DeleteReleaseResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "id", resolve: RESOLVE_RELEASE}; }
}

enum ReleaseState { RELEASE_STATE_UNSPECIFIED = 0; RELEASE_STATE_PENDING = 1; RELEASE_STATE_PUBLISHING = 2; RELEASE_STATE_PUBLISHED = 3; RELEASE_STATE_FAILED = 4; RELEASE_STATE_DELETED = 5; }  // §05.6.4
enum AssetKind { ASSET_KIND_UNSPECIFIED = 0; ASSET_KIND_APPIMAGE = 1; ASSET_KIND_TARBALL = 2; ASSET_KIND_FILE = 3; }
message PlatformClosure {
  string platform = 1;
  repeated string roots = 2;      // store paths installed by bob install
  int64 closure_nar_bytes = 3;    // bob-served part only
}
message Asset {
  string file = 1;                // ^[A-Za-z0-9._+-]{1,128}$
  AssetKind kind = 2;
  string platform = 3;
  int64 size = 4;
  string sha256 = 5;              // hex
  string url = 6;                 // <base>/r/<project>/<name>/<version>/<file>
}
message ImageRef { string repository = 1; string tag = 2; string digest = 3; }
message Release {
  string id = 1;
  string project_id = 2;
  string name = 3;                // release name from bob.go (slug)
  string version = 4;             // semver 2.0 without leading v
  bob.common.v1.Visibility visibility = 5;
  ReleaseState state = 6;
  string build_id = 7;
  string sha = 8;
  repeated PlatformClosure closures = 9;
  repeated Asset assets = 10;
  repeated ImageRef images = 11;
  string description = 12;        // ≤ 10 KiB, plain text
  google.protobuf.Timestamp created_at = 13;
  string etag = 14;
}
message ListReleasesRequest { int32 page_size = 1; string page_token = 2; string project_id = 3; string name = 4; }
message ListReleasesResponse { repeated Release releases = 1; string next_page_token = 2; }   // newest version first
message GetReleaseRequest { string id = 1; }
message GetReleaseResponse { Release release = 1; }
message ResolveReleaseRequest {
  string project_id = 1;
  string name = 2;
  string constraint = 3;          // "" = highest; "1.4" = highest 1.4.x; "1.4.2" exact (pre-releases only exact)
  string platform = 4;
}
message ResolveReleaseResponse { Release release = 1; PlatformClosure closure = 2; repeated string substituters = 3; repeated string public_keys = 4; }
message UpdateReleaseRequest { Release release = 1; google.protobuf.FieldMask update_mask = 2; }   // visibility, description only
message UpdateReleaseResponse { Release release = 1; }
message DeleteReleaseRequest { string id = 1; string etag = 2; }
message DeleteReleaseResponse {}
```

### 6.5.12 `bob/registry/v1/registry.proto`

```proto
package bob.registry.v1;

service RegistryService {
  rpc ListRepositories(ListRepositoriesRequest) returns (ListRepositoriesResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; } // GET; public + permitted
  // Also the permission name that gates /v2/ pulls of private manifests and blobs.
  rpc GetRepository(GetRepositoryRequest) returns (GetRepositoryResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }          // GET
  rpc ListTags(ListTagsRequest) returns (ListTagsResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; }                         // GET
}

message Repository {
  string name = 1;                // "<project>/<image>"
  string project_id = 2;
  bool has_public_tags = 3;
  int32 tag_count = 4;
  google.protobuf.Timestamp updated_at = 5;
  string pull_ref = 6;            // <registry host>/<name>
}
message Tag {
  string tag = 1;
  string digest = 2;
  string media_type = 3;          // OCI index or manifest
  repeated string platforms = 4;
  int64 size = 5;                 // sum of layer sizes, largest platform
  string release_id = 6;
  bool public = 7;
  google.protobuf.Timestamp created_at = 8;
}
message ListRepositoriesRequest { int32 page_size = 1; string page_token = 2; string project_id = 3; string query = 4; }
message ListRepositoriesResponse { repeated Repository repositories = 1; string next_page_token = 2; }
message GetRepositoryRequest { string name = 1; }
message GetRepositoryResponse { Repository repository = 1; }
message ListTagsRequest { string repository = 1; int32 page_size = 2; string page_token = 3; }
message ListTagsResponse { repeated Tag tags = 1; string next_page_token = 2; }   // visible tags only
```

### 6.5.13 `bob/settings/v1/settings.proto`

```proto
package bob.settings.v1;

service SettingsService {
  rpc GetSettings(GetSettingsRequest) returns (GetSettingsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }         // GET
  rpc UpdateSettings(UpdateSettingsRequest) returns (UpdateSettingsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc ListSigningKeys(ListSigningKeysRequest) returns (ListSigningKeysResponse) { option (bob.authz.v1.rule) = {kind: KIND_PUBLIC}; } // GET; public keys only
  // New ACTIVE key; the previous ACTIVE key becomes VERIFY_ONLY.
  rpc CreateSigningKey(CreateSigningKeyRequest) returns (CreateSigningKeyResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Re-signs every narinfo signed only by this key, then marks it RETIRED (removed from public_keys).
  rpc RetireSigningKey(RetireSigningKeyRequest) returns (RetireSigningKeyResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

message Retention {
  int32 logs_days = 1;                 // default 30
  int32 analytics_days = 2;            // default 400
  int32 builds_days = 3;               // build/job records, default 365 (= §05 `retention.builds`)
  int32 cache_unreferenced_days = 4;   // paths not reachable from a release or a build younger than this, default 90 (= §05 `retention.cache`)
  int32 staging_days = 5;              // lifecycle rule, default 7
}
message SessionPolicy {
  int32 cli_idle_days = 1;             // default 7
  int32 cli_max_days = 2;              // default 30
  int32 browser_idle_hours = 3;        // default 12
  int32 browser_max_days = 4;          // default 7
  int32 sa_token_max_days = 5;         // default 365
}
message Settings {
  string server_name = 1;              // default "bob"; Nix key name prefix
  string base_url = 2;                 // https://bob.example (status links, OCI realm)
  Retention retention = 3;
  SessionPolicy sessions = 4;
  bool live_log_enabled = 5;           // default true (D55 pull)
  int32 max_parallel_downloads = 6;    // per orchestrator, default 256
  string etag = 7;
}
message GetSettingsRequest {}
message GetSettingsResponse { Settings settings = 1; }
message UpdateSettingsRequest { Settings settings = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateSettingsResponse { Settings settings = 1; }
enum KeyState { KEY_STATE_UNSPECIFIED = 0; KEY_STATE_ACTIVE = 1; KEY_STATE_VERIFY_ONLY = 2; KEY_STATE_RETIRING = 3; KEY_STATE_RETIRED = 4; }
message SigningKey {
  string name = 1;                     // <server_name>-<n>
  string public_key = 2;               // "name:base64(32 bytes)"
  KeyState state = 3;
  google.protobuf.Timestamp created_at = 4;
}
message ListSigningKeysRequest {}
message ListSigningKeysResponse { repeated SigningKey keys = 1; }
message CreateSigningKeyRequest {}
message CreateSigningKeyResponse { SigningKey key = 1; }
message RetireSigningKeyRequest { string name = 1; }
message RetireSigningKeyResponse { SigningKey key = 1; }   // RETIRING until the re-sign sweep finishes
```

### 6.5.14 `bob/forge/v1/forge.proto`

```proto
package bob.forge.v1;

service ForgeService {
  // Returns the webhook secret once.
  rpc CreateForgeIntegration(CreateForgeIntegrationRequest) returns (CreateForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetForgeIntegration(GetForgeIntegrationRequest) returns (GetForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }       // GET
  rpc ListForgeIntegrations(ListForgeIntegrationsRequest) returns (ListForgeIntegrationsResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc UpdateForgeIntegration(UpdateForgeIntegrationRequest) returns (UpdateForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Refused while projects link to it.
  rpc DeleteForgeIntegration(DeleteForgeIntegrationRequest) returns (DeleteForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc RotateWebhookSecret(RotateWebhookSecretRequest) returns (RotateWebhookSecretResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Checks credentials (and optionally one repository) against the forge.
  rpc TestForgeIntegration(TestForgeIntegrationRequest) returns (TestForgeIntegrationResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  // Last 100 deliveries (ring document).
  rpc ListForgeDeliveries(ListForgeDeliveriesRequest) returns (ListForgeDeliveriesResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }       // GET
}

enum ForgeKind { FORGE_KIND_UNSPECIFIED = 0; FORGE_KIND_GITHUB = 1; FORGE_KIND_FORGEJO = 2; FORGE_KIND_GITLAB = 3; }
enum Health { HEALTH_UNSPECIFIED = 0; HEALTH_OK = 1; HEALTH_AUTH_FAILED = 2; HEALTH_UNREACHABLE = 3; }
message GitHubCredentials { int64 app_id = 1; int64 installation_id = 2; string private_key_pem = 3; /* input only */ }
message ForgejoCredentials { string bot_login = 1; string bot_password = 2; /* input only */ }
message GitLabCredentials { string access_token = 1; /* input only; project/group token, Maintainer, api */ bool allow_plaintext_secret = 2; /* GitLab < 19.0 */ }
message ForgeIntegration {
  string id = 1;
  ForgeKind kind = 2;
  string display_name = 3;
  string base_url = 4;              // https only (github: https://api.github.com)
  oneof credentials {
    GitHubCredentials github = 5;
    ForgejoCredentials forgejo = 6;
    GitLabCredentials gitlab = 7;
  }
  bool has_credentials = 8;         // output only
  string webhook_url = 9;           // output only: <base>/hooks/<id>
  Health health = 10;               // output only
  string health_message = 11;       // output only
  google.protobuf.Timestamp created_at = 12;
  string etag = 13;
}
message CreateForgeIntegrationRequest { ForgeIntegration integration = 1; }
message CreateForgeIntegrationResponse { ForgeIntegration integration = 1; string webhook_secret = 2; }
message GetForgeIntegrationRequest { string id = 1; }
message GetForgeIntegrationResponse { ForgeIntegration integration = 1; }
message ListForgeIntegrationsRequest {}
message ListForgeIntegrationsResponse { repeated ForgeIntegration integrations = 1; }   // ≤ 50
message UpdateForgeIntegrationRequest { ForgeIntegration integration = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateForgeIntegrationResponse { ForgeIntegration integration = 1; }
message DeleteForgeIntegrationRequest { string id = 1; string etag = 2; }
message DeleteForgeIntegrationResponse {}
message RotateWebhookSecretRequest { string id = 1; }
message RotateWebhookSecretResponse { string webhook_secret = 1; }   // old secret accepted for 24 h; per-repo hooks re-ensured
message TestForgeIntegrationRequest { string id = 1; string repo_full_name = 2; }
message TestForgeIntegrationResponse { Health health = 1; string message = 2; int64 repo_id = 3; }
message Delivery {
  string delivery_id = 1;
  string event = 2;                 // forge event name
  string result = 3;                // accepted | duplicate | ignored: <reason> | rejected: signature | rejected: payload
  string build_id = 4;
  google.protobuf.Timestamp received_at = 5;
}
message ListForgeDeliveriesRequest { string integration_id = 1; }
message ListForgeDeliveriesResponse { repeated Delivery deliveries = 1; }
```

### 6.5.15 `bob/notify/v1/notify.proto`

```proto
package bob.notify.v1;

service NotifyService {
  rpc CreateNotifyWebhook(CreateNotifyWebhookRequest) returns (CreateNotifyWebhookResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc GetNotifyWebhook(GetNotifyWebhookRequest) returns (GetNotifyWebhookResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }       // GET
  rpc ListNotifyWebhooks(ListNotifyWebhooksRequest) returns (ListNotifyWebhooksResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; } // GET
  rpc UpdateNotifyWebhook(UpdateNotifyWebhookRequest) returns (UpdateNotifyWebhookResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
  rpc DeleteNotifyWebhook(DeleteNotifyWebhookRequest) returns (DeleteNotifyWebhookResponse) { option (bob.authz.v1.rule) = {kind: KIND_GLOBAL}; }
}

message NotifyWebhook {
  string id = 1;
  string url = 2;                       // https only
  repeated string events = 3;           // build.finished | release.published
  repeated string projects = 4;         // empty = all
  bool enabled = 5;
  string last_result = 6;               // output only: "200 at <time>" | "error: <reason>"
  string etag = 7;
}
message CreateNotifyWebhookRequest { NotifyWebhook webhook = 1; }
message CreateNotifyWebhookResponse { NotifyWebhook webhook = 1; string secret = 2; }   // once
message GetNotifyWebhookRequest { string id = 1; }
message GetNotifyWebhookResponse { NotifyWebhook webhook = 1; }
message ListNotifyWebhooksRequest {}
message ListNotifyWebhooksResponse { repeated NotifyWebhook webhooks = 1; }             // ≤ 50
message UpdateNotifyWebhookRequest { NotifyWebhook webhook = 1; google.protobuf.FieldMask update_mask = 2; }
message UpdateNotifyWebhookResponse { NotifyWebhook webhook = 1; }
message DeleteNotifyWebhookRequest { string id = 1; }
message DeleteNotifyWebhookResponse {}
```

### 6.5.16 `bob/analytics/v1/analytics.proto`

```proto
package bob.analytics.v1;

service AnalyticsService {
  // Overview page; every list filtered to permitted projects; fleet numbers are aggregate counts.
  rpc GetOverview(GetOverviewRequest) returns (GetOverviewResponse) { option (bob.authz.v1.rule) = {kind: KIND_AUTHENTICATED}; }      // GET
  rpc PackageDurations(PackageDurationsRequest) returns (PackageDurationsResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project_id"}; } // GET
  rpc RecentFailures(RecentFailuresRequest) returns (RecentFailuresResponse) { option (bob.authz.v1.rule) = {kind: KIND_LIST_FILTERED}; } // GET
  rpc BuildHistory(BuildHistoryRequest) returns (BuildHistoryResponse) { option (bob.authz.v1.rule) = {kind: KIND_PROJECT, project_field: "project_id"}; } // GET
}

message PlatformFleet { string platform = 1; int32 active = 2; int32 busy = 3; int32 stale = 4; int32 queued_jobs = 5; }
message GetOverviewRequest {}
message GetOverviewResponse {
  repeated bob.build.v1.Build running = 1;        // ≤ 20
  repeated PlatformFleet fleet = 2;
  double cache_hit_rate_24h = 3;
  repeated bob.build.v1.Build recent_failures = 4; // ≤ 10
  int32 queue_depth = 5;
}
message PackageDuration { string label = 1; string day = 2; /* YYYY-MM-DD */ double avg_ms = 3; int64 count = 4; }
message PackageDurationsRequest { string project_id = 1; string label = 2; int32 days = 3; }   // days ≤ 400
message PackageDurationsResponse { repeated PackageDuration rows = 1; }
message RecentFailuresRequest { string project_id = 1; int32 limit = 2; }                   // ≤ 100
message RecentFailuresResponse { repeated bob.build.v1.Build builds = 1; }
message HistoryDay { string day = 1; int32 succeeded = 2; int32 failed = 3; int32 cancelled = 4; double p50_duration_s = 5; double p90_duration_s = 6; }
message BuildHistoryRequest { string project_id = 1; int32 days = 2; string pipeline = 3; }
message BuildHistoryResponse { repeated HistoryDay days = 1; }
```

### 6.5.17 `bob/worker/v1/worker.proto` (worker → orchestrator)

Specified completely in §05.9.1 (owner §05): `WorkerControlService` with `Attach` (the
long-lived stream each worker opens **outbound** to every orchestrator; it carries Wake (D54),
live-log subscriptions (D55), CancelJob and Drain) and `GetCheckoutCredential` (§6.9.6). Both
carry `rule = {kind: KIND_WORKER}`. Workers expose no port and serve no RPC; there is no
`bob.workeragent.v1` package (integration decision, closes E-06-1). Worker-side live buffer caps:
last 20,000 lines or 8 MiB per job, whichever is smaller; a `LiveChunk` is ≤ 4 MiB.

### 6.5.19 RPC coverage and rule summary

The `rule` option in the files above is the source of truth (R-AUTH-006). Summary:

| Kind | RPCs |
|---|---|
| PUBLIC | AuthService/GetServerInfo, LoginPassword; CacheService/GetPath; ReleaseService/ListReleases, GetRelease, ResolveRelease; RegistryService/*; SettingsService/ListSigningKeys |
| AUTHENTICATED | AuthService/WhoAmI (also download scope), Logout, ChangePassword, ListMySessions, RevokeMySession; RoleService/GetRole, ListRoles, ListPermissionCatalog; AnalyticsService/GetOverview |
| LIST_FILTERED | ProjectService/ListProjects; BuildService/ListBuilds; RoleService/ListRoleBindings; CacheService/ListTopPackages; AnalyticsService/RecentFailures |
| PROJECT | ProjectService/GetProject, UpdateProject, ListPipelines; BuildService/CreateBuild, GetBuild, CancelBuild, RetryBuild, RerunBuild, GetBuildGraph, WatchBuild; JobService/*; LogService/*; ReleaseService/UpdateRelease, DeleteRelease; RoleService/CreateRoleBinding, DeleteRoleBinding; AnalyticsService/PackageDurations, BuildHistory |
| GLOBAL | UserService/*; RoleService/CreateRole, UpdateRole, DeleteRole; ServiceAccountService/*; OidcService/*; ProjectService/CreateProject, DeleteProject; FleetService/*; CacheService/GetCacheStats; SettingsService/GetSettings, UpdateSettings, CreateSigningKey, RetireSigningKey; ForgeService/*; NotifyService/* |
| WORKER | WorkerControlService/Attach, GetCheckoutCredential (§05.9.1) |

PUBLIC methods on private data (private release, private repository, private path) apply the
PROJECT check with the permission named by the method itself, and answer NotFound when it fails.

Resource coverage (R-API-002): projects → ProjectService; builds → BuildService; jobs →
JobService; workers → FleetService; releases → ReleaseService; images → RegistryService; cache
entries → CacheService; users → UserService; roles and permissions → RoleService; service
accounts → ServiceAccountService; OIDC mappings → OidcService; settings → SettingsService.

## 6.6 Nix binary cache, visibility, download proxy, promotion

### 6.6.1 Endpoints

| Path | Answer |
|---|---|
| `GET /nix/nix-cache-info` | `StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n`; with an invalid/expired token → 401 |
| `GET/HEAD /nix/<hash>.narinfo` | 200 signed narinfo / 404 not visible or absent / 401 invalid token / 503 storage |
| `GET/HEAD /nix/nar/<hash>.nar.zst` | 200, 206 (one `Range`), 404, 416; same visibility as the narinfo |
| `GET /nix/<hash>.ls` | JSON listing v1; same visibility |

- `<hash>` must match `^[0-9a-df-np-sv-z]{32}$`, else 404 without S3 access.
- 401 carries `WWW-Authenticate: Basic realm="bob"` and body
  `unauthorized: token invalid or expired (run 'bob login')`.
- Credentials: netrc Basic with user `bob` and password = download token (or API/SA token). No
  `Authorization` header = anonymous.
- Headers: public narinfo `Cache-Control: public, max-age=300`; private narinfo
  `Cache-Control: private, max-age=300`; NARs `Cache-Control: public|private, max-age=31536000, immutable`.
- Narinfo field order: `StorePath, URL, Compression, FileHash, FileSize, NarHash, NarSize,
  References, Deriver, Sig, CA`; `URL: nar/<hash>.nar.zst`, `Compression: zstd`, hashes
  `sha256:<nix32>`, references as base names sorted. `Deriver` is omitted (it reveals source
  layout and Nix does not need it).

### 6.6.2 Visibility documents (D58)

One document per served object, artifacts bucket, written only by orchestrators:

| Key | Value | Writer | Reader | Access |
|---|---|---|---|---|
| `nix/vis/<storehash>.json` | `{"v":1,"projects":["<project>",…],"public":["<release-id>",…]}` | promotion (adds project), release publish/visibility change (adds/removes release id) | every `/nix/*` request | GET; create `If-None-Match: *`, update `If-Match` (≤ 5 CAS retries) |
| `oci/vis/sha256/<hex>.json` | same schema, for manifests, indexes, configs and layers | same | `/v2/*` | same |

Rule: an object is visible to a caller iff `public` is non-empty, or the caller has
`bob.cache.v1.CacheService/GetPath` (Nix) or `bob.registry.v1.RegistryService/GetRepository`
(OCI) on at least one project in `projects`. Orchestrators cache documents in an LRU (≤ 200,000
entries, 30 s TTL); so revoking public visibility takes effect ≤ 30 s (documented, accepted).
Absent document = not visible. `projects` is never shrunk except by GC; `public` lists ≤ 10,000
release ids (beyond that the release publish fails with `too many releases share <path>` **[open]**).

#### R-REG-001 Nix binary cache served by orchestrators
- **Statement:** Orchestrators serve §6.6.1 so stock Nix substitutes natively, with per-project
  privacy and anonymous access to public paths.
- **Acceptance:** Harness (RustFS, 2 orchestrators): (1) embedded Nix with bob's config and a
  download token for p1 substitutes a p1 closure; `nix store verify --trusted-public-keys <bob key>`
  exits 0. (2) Same closure without credentials → narinfo 404, Nix prints no bob path as copied.
  (3) After the closure is in a public release, anonymous substitution works. (4) A user of p2
  only → 404. (5) A wrong token → `nix-cache-info` 401.
- **Source:** R-REG-001, D58, ADR 0014, S12 §3.2.

#### R-REG-150 Visibility documents (new)
- **Statement:** Visibility is decided only from the §6.6.2 documents, one GET per object (cached).
- **Acceptance:** With an S3 operation counter: 1,000 narinfo requests for 10 distinct paths cause
  ≤ 20 GETs on `nix/vis/` and 0 LIST requests. Making a release private makes its exclusive
  paths 404 for anonymous callers within 31 s.
- **Source:** D58, D60, O-S12-1 (a).

### 6.6.3 Download proxy (D61)

- Every NAR, OCI blob and release asset byte goes S3 → orchestrator → client as a stream
  (`io.CopyBuffer`, 256 KiB buffer); no presigned URL, no redirect to the backend.
- `Range`: a single `bytes=a-b` / `a-` range is forwarded to S3 `GetObject Range`; multi-range →
  200 full body. S3 416 → 416.
- Limits: `max_parallel_downloads` (default 256) per orchestrator; beyond it 503 with
  `Retry-After: 1`. Client idle 60 s → connection closed.
- Integrity: the server does not re-hash on download; objects are immutable after promotion.

#### R-REG-151 Streaming download proxy (new)
- **Statement:** Downloads stream through the orchestrator with bounded memory and Range support.
- **Acceptance:** (1) 10 parallel downloads of a 1 GiB NAR: orchestrator RSS ≤ 64 MiB
  **[proposal]** (measured 19–33 MiB for 1–2 streams, S12 §3.4). (2) Connection cut at
  10,000,000 bytes: Nix resumes with `Range` and the path verifies. (3) No response of the server
  contains a `Location` header pointing to the S3 endpoint, and no URL with `X-Amz-Signature`.
- **Source:** D61, O-S12-2 (a), S12 §3.4.

### 6.6.4 Promotion protocol (per job, ADR 0016)

Trigger: the promotion controller sees a `JobRecord` in `succeeded` (the worker wrote
`<staging>/COMPLETE.json` and its signed result) with `pm` = "" (not yet promoted); all transitions are CAS on the
`JobRecord` (§05). Workers stage only paths that neither cache.nixos.org (HEAD narinfo) nor bob's
cache (HEAD own narinfo via the orchestrator with its download token) already has (R-REG-005).

Staging layout (worker writes; `<staging>` = `staging/<buildID>/<jobID>/<attempt>/`, §05.2.4):
`<staging>nar/<hash>.nar.zst`, `<staging>narinfo/<hash>.narinfo` (unsigned claim),
`<staging>oci/blobs/sha256/<hex>`, `<staging>assets/<file>`, `<staging>COMPLETE.json` last:
```json
{"v":1,"job":"01j…","build":"01j…","project":"api",
 "paths":["/nix/store/…-api-1.4.2"],
 "images":[{"repo":"api/server","tag":"1.4.2","manifest":"sha256:…","mediaType":"application/vnd.oci.image.index.v1+json","blobs":["sha256:…"]}],
 "assets":[{"file":"api-1.4.2-x86_64.AppImage","sha256":"…","size":31457280}],
 "skipped":["/nix/store/…-glibc-2.40"]}
```

Steps (orchestrator, idempotent, re-run until done):

1. GET `COMPLETE.json` (≤ 4 MiB, schema-validated). Missing → job promotion `blocked: staging
   incomplete`, job requeued per §05.
2. Parse every claim (≤ 4 MiB each; ≤ 100,000 paths). Topologically order paths (references in the set first).
3. Per level, 4 in parallel, for each path:
   a. narinfo already served → compare NarHash; differ → add to `non_reproducible` (keep the
      published one); add the project to the visibility document; done.
   b. GET the staged NAR, remember its ETag; stream: zstd decode → NAR structure check (magic,
      sorted unique names, no `/`, `.`, `..`, NUL, zero padding, nothing after the root; caps:
      NAR ≤ 64 GiB, ≤ 5,000,000 entries, names ≤ 255 B, symlink targets ≤ 4,095 B, depth ≤ 256)
      → NarHash, NarSize, FileHash, FileSize recomputed → **reference scan**: every 32-char
      substring matching a store hash of a path in this job's closure or in an already-served
      narinfo that the claim does not list → `failed: artifact verification` (refs are no longer
      blind worker claims).
   c. `CopyObject` to `nix/nar/<hash>.nar.zst` with `x-amz-copy-source-if-match: <ETag>`.
   d. PUT `nix/ls/<hash>.ls`; update `nix/vis/<hash>.json` (add project).
   e. Sign (ed25519, fingerprint `1;<path>;<NarHash>;<NarSize>;<refs full paths sorted, ","-joined>`,
      `Sig: <keyname>:<base64>`); PUT `nix/narinfo/<hash>.narinfo` with `If-None-Match: *`;
      412 = another orchestrator won = success.
4. Images: stream-verify each blob digest, copy to `oci/blobs/sha256/<hex>` (ETag guard) unless
   present, update `oci/vis/sha256/<hex>.json`, then update `oci/repos/<repo>/index.json` (tag
   last, CAS). Non-release builds are tagged with the build id itself (`b-<ULID>`, §05 R-STORE-104);
   release tags are written by §6.8.
5. Assets are verified (sha256, size) and stay in staging until a release copies them (§6.8);
   the release controller copies before the staging lifecycle expires (7 d) or the asset is lost
   and the release fails with `release asset expired; re-run the release pipeline`.
6. CAS `JobRecord.pm` → `done`. Staged objects of promoted paths are deleted by known key (§05 R-STORE-105); assets stay until a release copies them; the rest expires by the lifecycle rule.

Invariants: a served narinfo always has its NAR, `.ls`, visibility document and all in-set
references served; a crash leaves only orphans without narinfo (GC after 24 h).
Signing keys: `SigningKeyRecord` (`skey/<keyname>`, §05.4.5) in the control bucket only. Key name `<server_name>-<n>`.

#### R-REG-002 Signing only on orchestrators
- **Statement:** Only orchestrators hold signing keys; they sign only after their own verification.
- **Acceptance:** (1) A worker credential cannot read any `SigningKeyRecord` (S3 403). (2) Go
  signature equals `nix store sign` output byte for byte for 4 fixture paths. (3) Tamper case: a
  staged NAR replaced after upload → that path and dependents stay 404, job promotion
  `failed: artifact verification`, and the security event is logged with the worker id.
- **Source:** R-REG-002, S12 §3.1, §3.5.

#### R-REG-152 Crash-safe, concurrent promotion (new)
- **Statement:** Promotion follows §6.6.4 and is safe under orchestrator kill and concurrency.
- **Acceptance:** (1) Two orchestrators promote the same job: one reports `promoted N`, the other
  `alreadyThere N`; `nix store verify` OK. (2) SIGKILL at random points in 20 runs of a 1.1 GiB
  promotion: never a narinfo whose reference is 404; a re-run completes. (3) Same input-addressed
  path with different bytes → `non_reproducible` lists it and the first publication is unchanged.
  (4) A claim that omits a reference whose hash appears in the NAR → `failed: artifact verification`.
- **Source:** R-REG-002, R-SRV-004, R-NFR-006/007, S12 §3.5, risk 1.

#### R-REG-005 Only bob-built outputs
- **Statement:** The cache never serves or stores a path available from cache.nixos.org at
  staging time, and never proxies upstream caches.
- **Acceptance:** After a build whose closure contains `hello` from nixpkgs, `GET /nix/<hello-hash>.narinfo`
  → 404 and `COMPLETE.json.skipped` lists it; the server has no code path that fetches from
  cache.nixos.org (dependency check: no outbound HTTP client in `internal/cache`).
- **Source:** R-REG-005, D37.

#### R-REG-153 Signing key rotation (new)
- **Statement:** `CreateSigningKey` adds an ACTIVE key and keeps the old one VERIFY_ONLY;
  `RetireSigningKey` re-signs affected narinfos (maintenance LIST over `nix/narinfo/`, not a hot
  path) and then drops the key from `public_keys`.
- **Acceptance:** Rotate, promote a new path: it carries only the new signature; old paths still
  verify with the old key. Retire the old key: after the sweep every narinfo verifies with the new
  key alone; a narinfo rewrite uses `If-Match`.
- **Source:** R-REG-002, S12 §5.2.

### 6.6.5 Client configuration (written by the `bob` CLI; server side of R-DEV-004)

`WhoAmI.nix` / `GetServerInfo.nix` give the values. §02 writes:
```
substituters = https://cache.nixos.org https://<server>/nix
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY= <WhoAmI.nix.public_keys…>
netrc-file = <home>/etc/nix/netrc        # §01 R-NIX-024; machine <server> login bob password <token>, mode 0600
narinfo-cache-negative-ttl = 60
```
`<token>` is the login's access token (`bob_at_…`, or the service token of `bob login
--token-stdin`): the cache accepts it like a download token (§6.6 credentials), so one token
serves the API and the cache; a `bob_dt_` download token works in the netrc too.
The CLI calls `AuthService/WhoAmI` with that token before any Nix call that may hit the
server (Nix hides 401 behind a level-1 warning and then builds locally, STATUS finding S15), and
passes `--option narinfo-cache-negative-ttl 0` to the Nix call that fetches a remote build result
or a release.

#### R-REG-154 Download token works for WhoAmI and Nix, not the API (new)
- **Statement:** `bob_dt_` tokens are accepted by WhoAmI and download endpoints only.
- **Acceptance:** WhoAmI with a download token → `token_scope: "download"`; `ListProjects` with it
  → Unauthenticated `token not valid for the API`.
- **Source:** S12-2, R-AUTH-007.

## 6.7 OCI registry (pull only)

| Path | Answer |
|---|---|
| `GET /v2/` | no/invalid Bearer → 401 `WWW-Authenticate: Bearer realm="<base>/oci/token",service="bob"`; valid → 200 `{}` |
| `GET/HEAD /v2/<repo>/manifests/<tag or digest>` | manifest, `Docker-Content-Digest`, `Content-Type` = stored media type |
| `GET/HEAD /v2/<repo>/blobs/<digest>` | blob stream, single Range |
| `GET /v2/<repo>/tags/list?n=&last=` | `{name, tags}` visible tags only, from `oci/repos/<repo>/index.json` |
| `GET /v2/<repo>/referrers/<digest>` | empty OCI image index |
| any other method under `/v2/` | 405 `{"errors":[{"code":"UNSUPPORTED","message":"this registry is pull-only"}]}` |
| `GET /oci/token?service=bob&scope=repository:<repo>:pull[&scope=…]` | `{token, access_token, expires_in: 900, issued_at}` |

- Repository names `<project>/<image>`, distribution-spec regex, ≤ 255 bytes; tags
  `^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`.
- `oci/repos/<repo>/index.json`: `{"v":1,"tags":{"1.4.2":{"d":"sha256:…","r":"<release-id>","t":1761000000}}}`,
  CAS-written; `latest` points to the highest semver release tag and is the only tag that moves.
  Release tags are create-only within the document (an existing tag → release fails).
- Token endpoint: Basic with any API/download/SA token (user name ignored) or anonymous. Granted
  scopes = requested `pull` scopes on repositories with at least one object visible to the caller
  (§6.6.2); others are left out silently; `push`/`delete` never granted. Token =
  `bob-oci-v1.<b64url(json{sub, exp, acc:{"<repo>":["pull"]}})>.<b64url(HMAC-SHA256(OciHmacKey))>`.
  Invalid Basic credentials → 401 `{"errors":[{"code":"UNAUTHORIZED","message":"invalid credentials"}]}`.
- Per object, `/v2/` checks the token's repo scope **and** the object's visibility document.
  Missing and not-allowed give the same 401 `insufficient_scope` / 404 `MANIFEST_UNKNOWN`
  consistently (no existence leak). Blobs are content-addressed and shared across repositories;
  a blob is served when visible to the caller, regardless of which repository path asked.

#### R-REG-003 OCI pull registry
- **Statement:** Stock OCI clients pull bob-built images; only promotion writes.
- **Acceptance:** crane, skopeo, podman (rootful and rootless): anonymous pull of a public tag OK;
  private tag anonymous → `UNAUTHORIZED`; with SA download token (docker config) → OK; user of
  another project → same error as missing; `skopeo copy` push with valid creds → refused; HTTP
  `PUT /v2/x/manifests/y` → 405.
- **Source:** R-REG-003, D19, S12 §3.7.

## 6.8 Releases

- A release is produced by a pipeline whose definition has release outputs (§01/§04). After all
  jobs of the build are promoted, the release controller:
  1. Creates `ReleaseRecord` in `pending` (§05.6.4) create-only on (project, name, version) via `relv`; exists →
     pipeline fails `release <project>/<name>@<version> already exists; releases are immutable`.
  2. Moves the record to `publishing` and copies assets `<staging>assets/<file>` →
     `rel/<projectID>/<num10>/<file>` (sha256 verified, ETag guard).
  3. If visibility is public: adds the release id to `public` of every visibility document of the
     closure paths and image objects (bob-served part only).
  4. Writes image tags `<version>` and moves `latest` (CAS on the repo index).
  5. Updates `rpage`/`ridx` (CAS), sets `published`, sends `release.published` notifications.
     Release id = `<projectID>.<num10>` (§05.2.4).
- Visibility default comes from the release definition in `bob.go`; `UpdateRelease` changes it
  and the controller adds/removes the release id in the documents (idempotent sweep).
- `DeleteRelease` → `deleted` (§05.6.4): removes the release id from documents and tags,
  deletes `rel/<projectID>/<num10>/`; the record stays as a tombstone (`relv` kept, so the
  version is never reused). Its paths become ordinary cache paths subject to GC.
- Version: semver 2.0 from the tag (`v1.4.2` → `1.4.2`) or from the pipeline definition.

Release downloads: `GET /r/<project>/<name>/<version>/<file>` — public: anonymous; private:
Bearer (API token) or Basic (any token, user ignored) with `ReleaseService/GetRelease` on the
project; else 404. Headers: `Content-Type: application/octet-stream`,
`Content-Disposition: attachment; filename="<file>"`, `ETag: "<sha256>"`, `Cache-Control:
public|private, max-age=31536000, immutable`. `/latest/<file>` → 302 to the versioned path,
`Cache-Control: no-cache`.

#### R-REG-004 Releases
- **Statement:** Releases are immutable sets of artifacts, public or private, downloadable over
  HTTP and installable with `bob install`.
- **Acceptance:** Tag push `v1.4.2` on fixture project `acme` with release `api`: a release
  `acme/api@1.4.2` appears with closure roots per platform, an AppImage asset and an OCI tag.
  `curl -fsS <base>/r/acme/api/1.4.2/<file> | sha256sum` equals `Asset.sha256`. A second run of
  the same tag fails with the "already exists" message. `ResolveRelease(constraint "1.4")` returns
  1.4.2 when 1.4.0, 1.4.2 and 1.5.0 exist; `""` returns 1.5.0; `1.6.0-rc.1` only by exact match.
- **Source:** R-REG-004, D10.

Server side of **R-INST-003** (specified in §02 §02.8): `ResolveRelease` and the narinfo/NAR
endpoints of a public release answer anonymously; a private release answers NotFound without a
permitted token.

#### R-STORE-150 Releases are never collected (new)
- **Statement:** Cache GC never deletes an object whose visibility document lists a release, and
  never deletes `rel/` objects of a non-deleted release.
- **Acceptance:** With `cache_unreferenced_days = 0`, a GC run after 2 builds and 1 release
  deletes the unreleased build's paths and keeps every path and asset of the release; after
  `DeleteRelease` and another GC run they are gone.
- **Source:** R-STORE-007.

## 6.9 Pipelines and forges

### 6.9.1 Adapter interface (`internal/forge`)

Eight methods (ADR 0018 had six; `LookupRepo` and `EnsureWebhook` are added for project linking).

```go
// Package forge is bob's only view of GitHub, Forgejo and GitLab. One Adapter value exists per
// forge integration (one credential set); it is safe for concurrent use.
package forge

type Kind string

const (
	GitHub  Kind = "github"
	Forgejo Kind = "forgejo"
	GitLab  Kind = "gitlab"
)

// MaxWebhookBody is GitHub's documented payload limit; larger bodies are refused with ErrTooLarge.
const MaxWebhookBody = 25 << 20

var (
	ErrSignature   = errors.New("forge: webhook signature missing or invalid") // HTTP 401
	ErrPayload     = errors.New("forge: webhook payload malformed")            // HTTP 400
	ErrTooLarge    = errors.New("forge: webhook payload too large")            // HTTP 413
	ErrStale       = errors.New("forge: webhook timestamp outside tolerance")  // HTTP 401 (GitLab)
	ErrUnsupported = errors.New("forge: operation not supported by this forge")
	ErrAuth        = errors.New("forge: forge rejected bob's credentials")     // integration health → AUTH_FAILED
	ErrNotFound    = errors.New("forge: repository or object not found")
)

type Adapter interface {
	Kind() Kind
	// ParseWebhook verifies the signature on the raw body first, then normalizes. No I/O.
	ParseWebhook(h http.Header, body []byte, now time.Time) (Event, error)
	// SetStatus writes a commit status with context s.Context ("bob/<pipeline>").
	SetStatus(ctx context.Context, repo Repo, s Status) error
	// IssueCheckout mints a read-only credential for exactly req.Repo.
	IssueCheckout(ctx context.Context, req CheckoutRequest) (Checkout, error)
	// RevokeCheckout is idempotent; an already revoked or expired credential is success.
	RevokeCheckout(ctx context.Context, c Checkout) error
	// SweepCheckouts revokes bob-minted credentials created before cutoff. Returns 0, nil where
	// the forge expires credentials itself.
	SweepCheckouts(ctx context.Context, cutoff time.Time) (int, error)
	// LookupRepo resolves "owner/name" (GitLab: full path) to the forge's repository record.
	LookupRepo(ctx context.Context, fullName string) (Repo, error)
	// EnsureWebhook creates or updates bob's hook on repo (push, tag, PR/MR events) with url and
	// secret. GitHub App hooks are app-level: returns ErrUnsupported, which callers treat as OK.
	EnsureWebhook(ctx context.Context, repo Repo, url string, secret Secret) error
}

type Repo struct {
	Forge         Kind
	ID            int64
	FullName      string
	CloneURL      string // https only
	WebURL        string
	DefaultBranch string
	Private       bool
}

type State string // pending running success failure error cancelled

type Status struct {
	SHA, Context string
	State        State
	Description  string // truncated to the forge limit (GitHub 140 runes, others 255)
	TargetURL    string
}

type CheckoutRequest struct {
	Repo  Repo
	JobID string
	TTL   time.Duration // ≤ 1 h
}

type Checkout struct {
	CloneURL, Username string
	Token              Secret    // redacts in String/GoString/MarshalJSON/LogValue
	ExpiresAt          time.Time
	ForgeExpires       bool   // false only for Forgejo
	Handle             string // opaque; stored in CheckoutRecord for RevokeCheckout
}

// Secret is a string that never prints or marshals; Reveal returns the value.
type Secret struct{ v string }

func NewSecret(v string) Secret
func (s Secret) Reveal() string
```

Events (normalized, after `CheckEvent` invariants: repo id > 0, clone URL `https://`, SHAs
`^([0-9a-f]{40}|[0-9a-f]{64})$`, names ≤ 255 bytes, PR number > 0):

```go
type EventType string // push tag pull_request ping ignored

type Event struct {
	Forge         Kind
	DeliveryID    string
	Type          EventType
	Repo          Repo
	Sender        string
	Push          *Push        // exactly one of Push/Tag/PullRequest for push/tag/pull_request
	Tag           *Tag
	PullRequest   *PullRequest
	IgnoredReason string       // Type == "ignored"
}
type Push struct{ Branch, Before, After string; Created, Deleted bool }
type Tag struct{ Name, Commit string; Deleted bool } // Commit = peeled commit
type PullRequest struct {
	Number     int64
	Action     PRAction // opened synchronized reopened closed; others → Type ignored
	Title, HeadSHA, HeadBranch, HeadRepo, BaseBranch string
	FromFork, Merged bool
	FetchRef   string // refs/pull/N/head | refs/merge-requests/N/head (in the base repo)
}
type PRAction string
```

Constructors (each refuses a non-https base URL with `forge: base URL must be https`):

```go
// github.New builds a GitHub App adapter (google/go-github v88; App JWT RS256 via stdlib).
// Same shape in each package: func New(cfg Config) (forge.Adapter, error)
// github.Config{BaseURL, AppID, InstallationID, PrivateKeyPEM forge.Secret, WebhookSecret forge.Secret}
// forgejo.New: codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3 (A12); Config{BaseURL, BotLogin, BotPassword, WebhookSecret}
// gitlab.New: gitlab.com/gitlab-org/api/client-go; Config{BaseURL, AccessToken, SigningToken, AllowPlaintextSecret}
```
Example:
```go
a, err := forgejo.New(forgejo.Config{
	BaseURL: "https://git.acme.example", BotLogin: "bob-bot",
	BotPassword: forge.NewSecret(pw), WebhookSecret: forge.NewSecret(hookSecret),
})
if err != nil { return err }
repo, err := a.LookupRepo(ctx, "acme/api")
```

### 6.9.2 Per-forge mapping

| bob | GitHub | Forgejo | GitLab |
|---|---|---|---|
| Event header | `X-GitHub-Event` | `X-Forgejo-Event` (+`-Type`) | `X-Gitlab-Event` |
| Delivery id | `X-GitHub-Delivery` | `X-Forgejo-Delivery` | `webhook-id` |
| Signature | `X-Hub-Signature-256: sha256=<hex>` HMAC(body) | `X-Forgejo-Signature: <hex>` HMAC(body) | `webhook-signature: v1,<b64>` HMAC(`id.ts.body`), key = b64-decoded part after `whsec_` |
| Replay window | dedupe only | dedupe only | `webhook-timestamp` ±5 min + dedupe |
| push / tag | `push` `refs/heads/*` / `refs/tags/*` (commit `head_commit.id`) | same | `Push Hook` / `Tag Push Hook` (commit `checkout_sha`) |
| PR opened / new commits / reopened / closed | `opened`/`synchronize`/`reopened`/`closed` | `opened`/`synchronized`/`reopened`/`closed` | `open`/`update` with `oldrev`/`reopen`/`close`,`merge` |
| Fork | head.repo.id ≠ base.repo.id | head.repo_id ≠ base.repo_id | source_project_id ≠ target_project_id |
| Fetch ref | `refs/pull/N/head` | `refs/pull/N/head` | `refs/merge-requests/IID/head` |
| ignored | create, delete, release, others | create, delete, release, others | Release Hook, others |
| ping | `ping` | (test = push) | (test = push) |
| status pending / running | `pending` / `pending` | `pending` / `pending` | `pending` / `running` |
| success / failure / error / cancelled | `success`/`failure`/`error`/`error`+"cancelled" | `success`/`failure`/`error`/`warning` | `success`/`failed`/`failed`/`canceled` |
| bob credential | GitHub App (app id, key, installation id) | bot account password (D62); status token minted by bob (`write:repository`, repo-scoped) | project/group access token, Maintainer, `api` |
| Checkout credential | installation token, `repository_ids:[id]`, `contents: read`, 1 h fixed | PAT `read:repository`, `repositories:[repo]`, no expiry → revoke + sweep | deploy token `read_repository`, `expires_at` = job deadline |
| Clone user | `x-access-token` | bot login | `gitlab+deploy-token-N` |

### 6.9.3 Webhook intake (`POST /hooks/<integration-id>`)

1. Unknown integration → 404. Read ≤ `MaxWebhookBody`+1 bytes; more → 413.
2. `ParseWebhook` verifies on the raw bytes (constant-time); no secret configured → refuse. During
   a secret rotation the previous secret is accepted for 24 h. Failure → 401 `invalid signature`
   (the reason goes to the log, no header values).
3. Create `ForgeDeliveryRecord(integration, SHA-256(delivery id))` with `If-None-Match: *`;
   exists → 200 `duplicate`. Records live 30 days (> forge retry windows).
4. GET `RepoLinkRecord(integration, repo id)`; none → 202 `ignored: repository not linked`.
5. Create the `BuildRecord` (trigger from the event, state QUEUED) and respond 202 within 10 s.
   `ignored` events → 202 and only the delivery ring is updated.
6. Refs and SHAs reach git only as validated separate arguments, never through a shell.

#### R-PIPE-001 Forge integrations
- **Statement:** GitHub, Forgejo and GitLab adapters implement §6.9.1 with the mapping of §6.9.2.
- **Acceptance:** Contract test (every CI run): all three adapters pass the shared suite on the
  recorded fixtures (real Forgejo 16 and GitLab 19 deliveries, documented GitHub payloads): push,
  tag, PR opened/sync/fork, ignored kinds; unsigned and oversize bodies refused; GitLab ±5 min.
  Live Forgejo in the harness (every run) and live GitLab (slow suite, A13): hook → build →
  statuses → checkout → revoke round trip.
- **Source:** R-PIPE-001, R-PIPE-007, D48, A12, A13, S16.

#### R-PIPE-002 One repository per project
- **Statement:** A project links one repository via `RepoLinkRecord`; events of other repositories are ignored.
- **Acceptance:** A correctly signed delivery for repo id 2 on an integration whose only link is
  repo id 1 → 202, delivery result `ignored: repository not linked`, no build.
  `CreateProject` with a repo already linked to another project → AlreadyExists.
- **Source:** R-PIPE-002.

#### R-PIPE-008 Webhook security (new)
- **Statement:** Intake follows §6.9.3 exactly.
- **Acceptance:** Fixture tests: wrong signature → 401 `invalid signature`; replayed delivery id →
  200 and still one build; GitLab timestamp 6 min old → 401; body 25 MiB + 1 → 413; a push with
  `after = "--upload-pack=x"` → 400 (CheckEvent) and no git process started.
- **Source:** R-SEC-004, S16 §5.4.

### 6.9.4 From event to pipelines and statuses

- The build's first job is an `EVALBUILD` (ADR 0016). The worker evaluates `bob.go` at the commit,
  runs `pipeline.Match(trigger)` (§01) and reports the matched pipelines and their targets with the
  evaluation result. The orchestrator re-runs `pipeline.Match` on the reported pipeline
  definitions and the stored trigger and rejects the job result on mismatch (hostile input).
  No match → `SKIPPED` (no forge status beyond clearing `bob/evaluate`).
- API/UI `CreateBuild{pipeline}` runs that pipeline regardless of its triggers;
  `CreateBuild{targets}` runs no pipeline (REMOTE/API ad-hoc builds).
- Statuses, written by the status controller from `BuildRecord.reported` (idempotent, retried with
  backoff 5 s → 5 min, ≤ 24 h): `bob/evaluate` pending at QUEUED, success/failure after
  evaluation; then one `bob/<pipeline>` per matched pipeline: pending (queued), running,
  success/failure/error/cancelled. Description `#<number> <state>: <first error message>`
  (truncated); target URL = `Build.web_url`. Status credentials never leave the orchestrator.
- `ErrAuth` from any adapter call → integration health `AUTH_FAILED`, message
  `forge: integration <id>: forge rejected bob's credentials (HTTP 401); re-enter the credentials in Settings → Forges`.

#### R-PIPE-003 Pipelines run as defined and report back
- **Statement:** Matched pipelines run and report per-pipeline statuses with a link to the build.
- **Acceptance:** Live Forgejo: push to `main` with pipelines `ci` (push main) and `release`
  (tag v*) → only `bob/ci` appears, pending → running → success, target URL opens the build
  page (HTTP 200). A failing test → `failure` with description containing `BOB-E-2005`.
  A forged evaluation result that adds `release` → job result rejected, build FAILED with
  `evaluation result rejected: pipeline match mismatch` (this test needs the worker Attach client of
  Gate H G20: `tests/serverenv/workerclient`, built with worker registration).
- **Source:** R-PIPE-003, R-FW-040, R-SEC-004.

#### R-PIPE-004 Triggers by API, UI and --remote
- **Statement:** `CreateBuild` starts builds for a pipeline, for labels from a git ref, or from an uploaded source snapshot.
- **Acceptance:** `CreateBuild{project, git{sha}, pipeline:"ci"}` → build with trigger API;
  `bob build --remote //svc/api:server` (§02) → upload + build with trigger REMOTE; afterwards
  the CLI substitutes the result with negative TTL 0 (cache 200 on the narinfo).
- **Source:** R-PIPE-004, R-DEV-005.

### 6.9.5 Cancel, retry, rerun, concurrency (R-PIPE-005)

| Action | Allowed in | Effect |
|---|---|---|
| CancelBuild | PENDING, QUEUED, RUNNING | → CANCELLED (§05.6.2: queued jobs cancelled, running jobs get `cx` + CancelJob over Attach); statuses `cancelled`. On a terminal build → FailedPrecondition `build is already finished` |
| RetryBuild | FAILED, CANCELLED, SUPERSEDED | new build (trigger RETRY, `retry_of`, attempt+1) of the failed/cancelled pipelines; succeeded packages are cache hits |
| RerunBuild | any terminal state | new build, trigger RERUN, same source and pipelines |

Concurrency is §05.6.2's rule (project setting per ref, evaluated on `bbr`; default `queue`
for the default branch and tags, `supersede` for other branches and PRs, `parallel` optional).
`supersede`: a newer build for the same ref moves the older non-terminal build to SUPERSEDED,
`Trigger.superseded_by` set, forge status `cancelled` with description `superseded by #<n>`.
`queue`: the newer build stays PENDING until the older is terminal. Builds that run a release
pipeline are never superseded.

#### R-PIPE-005 Cancel, retry, re-run, concurrency
- **Statement:** As in the table and rules above.
- **Acceptance:** (1) Two pushes to a feature branch 1 s apart: the first build ends SUPERSEDED with
  `superseded_by` = second build, its forge status `cancelled`. (2) Two tag pushes with a `Queue`
  release pipeline run one after the other. (3) RetryBuild on a build with 1 failed of 10
  packages rebuilds only that package and its dependents (others CACHED/SUCCEEDED unchanged).
  (4) CancelBuild on a SUCCEEDED build → FailedPrecondition.
- **Source:** R-PIPE-005.

### 6.9.6 Checkout credentials (R-FLEET-006, D62)

```
worker holds lease (job J, epoch E) → WorkerControlService/GetCheckoutCredential(J, E)  (§05.9.1)
orchestrator: GET JobRecord; owner = caller wid and epoch = E, state CLAIMED|RUNNING, else PermissionDenied `permission_denied: worker holds no lease on job <id>`
  CheckoutRecord(J) in ftok/<sh2(J)> exists (repeat call, e.g. worker restart of the fetch) → RevokeCheckout(old handle) first; token values are never stored
  adapter.IssueCheckout({Repo, JobID: J, TTL: min(1h, job deadline − now)})
  CAS-add CheckoutRecord{J, epoch E, handle, expiresAt, forgeExpires} to ftok/<sh2(J)> (no token value)
  return sealed checkout (HPKE to the worker key) + download token
worker: git init; git -c "http.extraHeader=Authorization: Basic <b64(user:token)>" fetch --depth=1 <cloneURL> <sha|fetchRef>
        verify HEAD = expected SHA; credential never written to .git/config; discarded after fetch
job terminal or lease lost/requeued → orchestrator RevokeCheckout(handle) (idempotent), remove CheckoutRecord
forge-sweeper every 60 s (§05.11 controller) → revoke CheckoutRecords of terminal/missing jobs;
  every 5 min also SweepCheckouts(now − max job duration − 10 min) for tokens without a record
```
Forgejo (D62): bob stores the bot password (encrypted in the control bucket), mints
`bob-job-<unix-seconds>-<jobID>` tokens limited to the one repository with `read:repository`,
revokes at job end, sweeps by the timestamp in the name. The bot account must be a collaborator
only on linked repositories and cannot use 2FA (documented in the admin guide).

R-FLEET-006 is specified once, in §05 §05.9.2 (its acceptance includes the live-Forgejo
checks of this flow).

### 6.9.7 Source upload for `--remote`

`PUT /upload/source/sha256/<hex>` with `Authorization: Bearer bob_at_…`, `Bob-Project: <id>`,
`Content-Type: application/zstd`, body = zstd tar of the files §02 selects (tracked + untracked
not ignored). Caps: 512 MiB compressed **[proposal]**, ≤ 200,000 entries, regular files,
directories and symlinks only (no absolute or `..` targets; checked by the worker on unpack).
The server streams to `src/<project>/<hex>.tar.zst` with `If-None-Match: *` while hashing; hash
mismatch → 400 `upload does not match its sha256`, object deleted; first write → 201; existing → 200 without reading
the body beyond the headers (client sends `Expect: 100-continue`). Lifecycle 7 days.

#### R-PIPE-009 Remote source snapshots (new)
- **Statement:** Uploads are content-addressed, size-capped, project-scoped and permission-checked.
- **Acceptance:** Upload with a wrong hash → 400 and no object; without `CreateBuild` on the
  project → 404; same upload twice → second answers 200 in < 100 ms with ≤ 1 MiB body sent;
  a tar with `../x` → job fails `source snapshot contains an unsafe path: ../x`.
- **Source:** R-PIPE-004, R-DEV-005, R-SEC-004.

### 6.9.8 Outgoing notifications (R-PIPE-006)

Events `build.finished` `{event, build: Build JSON (proto JSON), project}` and
`release.published` `{event, release}`. POST with `Content-Type: application/json`,
`Bob-Event`, `Bob-Delivery: <id>`, `Bob-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>`.
Delivery from the notify controller: timeout 10 s, attempts at 0, 10 s, 1 min, 10 min, 1 h; 2xx = done.
https only.

#### R-PIPE-006 Outgoing webhooks
- **Statement:** Notifications are signed and retried as above.
- **Acceptance:** A test receiver returning 500 twice then 200 sees 3 deliveries with the same
  `Bob-Delivery`, valid signatures, and `last_result` shows `200 at …`.
- **Source:** R-PIPE-006, D43.

#### R-PIPE-007 Adapter testing
- **Statement:** As in ADR 0018 Decision 7 (fixtures every CI run, GitHub replay server, live
  Forgejo in the harness, live GitLab in the slow suite).
- **Acceptance:** CI job list contains `forge-fixtures`, `forge-github-replay`, `forge-forgejo-live`
  (fast suite) and `forge-gitlab-live` (slow suite); each passes on main.
- **Source:** R-PIPE-007, D48, A13.

## 6.10 Web UI

### 6.10.1 Stack and structure

- SvelteKit 2 + Svelte 5 runes, `@sveltejs/adapter-static` with `fallback: '200.html'`,
  `ssr = false`, `prerender = false` except `/login`. Tailwind CSS v4 (`@tailwindcss/vite`).
  Client: `@connectrpc/connect-web` + generated `@bufbuild/protobuf` v2 types. No other runtime
  dependencies except a small SVG graph layout written in bob (no component or chart library;
  charts are hand-written SVG). Banned: `clsx`, `cva`, `tailwind-merge`, `@apply` piles, any UI kit.
- Built by bob (R-ARCH-005) into `web/build`, embedded with `embed.FS` in `internal/ui`, served by
  the orchestrator (§6.2).
- Layout: fixed left nav (200 px; collapses to a top bar < 900 px), page header (title, breadcrumb,
  primary action right), content. Theme switch in the nav footer: Light / Dark / System;
  default **Light** regardless of OS; stored in `localStorage["bob.theme"]`; applied as
  `data-theme` on `<html>` before first paint (inline script in `app.html`).
- Live data: `WatchBuild` and `FollowLog` streams on the build page; lists refresh every 10 s
  while visible (Page Visibility API), never faster.
- Every list has a filter bar (text `query` + the request's enum filters) whose state is in the URL
  query string; every id (build, job, worker, release, digest, user, role, SA) renders as a link
  to its detail route. Ids, hashes, store paths, labels and logs use the mono font.
- Log view: virtualised list, ANSI colours mapped to tokens (no other escape handling; text is
  inserted as text nodes only), per-package filter, search via `SearchLog`, "follow" toggle.

### 6.10.2 Design tokens (Tailwind v4 theme, `web/src/app.css`)

```css
@import "tailwindcss";

@theme {
  --font-sans: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  --font-mono: ui-monospace, "SFMono-Regular", Menlo, Consolas, monospace;
  --text-xs: 11px;  --text-sm: 12px;  --text-base: 13px;  --text-lg: 15px;  --text-xl: 18px;
  --radius-xs: 2px;
  --spacing: 4px;

  --color-bg: #ffffff;        /* page */
  --color-surface: #f6f6f3;   /* nav, table header, code blocks */
  --color-line: #deded8;      /* 1px borders */
  --color-fg: #111110;        /* text */
  --color-muted: #5e5e58;     /* secondary text */
  --color-accent: #ffd400;    /* yellow: fills only (primary button, running, selection bar) */
  --color-on-accent: #111110; /* text on yellow */
  --color-focus: #111110;     /* 2px focus outline */
  --color-ok: #1e7b34;
  --color-fail: #b42318;
  --color-warn: #8a5a00;
  --color-idle: #77776f;      /* queued, cancelled, skipped */
}

[data-theme="dark"] {
  --color-bg: #0e0e0d;
  --color-surface: #171715;
  --color-line: #2c2c29;
  --color-fg: #edede8;
  --color-muted: #a3a39b;
  --color-accent: #ffd400;
  --color-on-accent: #111110;
  --color-focus: #ffd400;
  --color-ok: #4cc26b;
  --color-fail: #f2685c;
  --color-warn: #e0a526;
  --color-idle: #8c8c84;
}
```

Rules: yellow is never a text colour on the light theme (contrast); it is a fill with
`on-accent` text, a 2 px left bar for the active nav item and the selected table row, or the
running state. Status = small square-cornered badge (`rounded-xs`, 1px border in the state
colour, text in the state colour; running = yellow fill). Tables: row height 28 px, header in
`surface`, 1px `line` borders, numeric columns right-aligned with tabular figures. Only
`rounded-xs` or none; no shadows beyond none; no gradients, glass, blur, glow, emoji, hero cards.

Components (hand-written, `web/src/lib/ui/`): `Button` (primary/secondary/danger), `Input`,
`Select`, `Checkbox`, `Badge`, `StatusBadge`, `Table` (+ `Th`, `Td`), `FilterBar`, `Pagination`,
`Tabs`, `Dialog`, `Confirm`, `KeyValue`, `Mono` (copy button), `CodeBlock`, `LogView`, `Graph`,
`Waterfall`, `BarChart`, `LineChart`, `EmptyState` (one line of text, no illustration), `Toast`.

### 6.10.3 Pages and wireframes

Routes: `/` overview · `/projects` · `/p/<project>` (tabs builds, pipelines, releases, settings,
permissions) · `/p/<project>/builds/<id>` · `/fleet` · `/fleet/workers/<id>` · `/cache` ·
`/registry` (releases, images) · `/registry/<repo>` · `/p/<project>/releases/<name>/<version>` ·
`/access` (users, roles, matrix, service accounts, OIDC) · `/settings` (general, forges, signing
keys, retention, webhooks) · `/analytics` · `/login` · `/device` · `/oauth/consent` · `/account`.

Overview `/`
```
┌ bob ─────────┬──────────────────────────────────────────────────────────────────────┐
│ Overview  ▌  │ Overview                                                             │
│ Projects     │ ┌ Running 4 ─┬ Queue 7 ─────┬ Workers amd64 6/8 arm64 2/2 ┬ Cache 91% ┐ │
│ Fleet        │ └────────────┴──────────────┴─────────────────────────────┴──────────┘ │
│ Cache        │ Running builds                                          [filter    ] │
│ Registry     │ ID          Project  Pipeline  Ref        State     Progress  Started │
│ Access       │ 01jb…k2p    api      ci        main       RUNNING   41/120    2m      │
│ Settings     │ Recent failures                                                       │
│ Analytics    │ 01jb…7aa    web      ci        pr/88      FAILED    BOB-E-2005 …  14m │
│ ─────────    │                                                                       │
│ Light|Dark   │                                                                       │
└──────────────┴──────────────────────────────────────────────────────────────────────┘
```
Stat strip = four bordered cells with number + label (not cards with icons).

Project `/p/api` (tab Builds)
```
api  ·  acme/api (forgejo) · link OK                               [Run pipeline ▾]
[Builds] Pipelines  Releases  Settings  Permissions
filter: [text     ] state [any ▾] pipeline [any ▾] trigger [any ▾]
#    ID          Pipeline  Trigger        Ref/SHA          State      Dur    Created
412  01jb…k2p    ci        push alice     main  3f2a91c    RUNNING    2:11   2m
411  01jb…h0d    ci        push alice     main  77c0e1b    CANCELLED  0:40   9m  superseded by #412
                                                              ‹ prev  next ›
```

Build detail `/p/api/builds/<id>`
```
#412  api · ci · main 3f2a91c · push by alice           RUNNING   [Cancel] [Retry] [Re-run]
platforms linux/amd64 linux/arm64 · variant release · attempt 1 · 41/120 · cached 63
┌ Packages ───────────────── [table|graph] ┬ Log  //svc/api:server  [follow ●] [search  ] ┐
│ ● //svc/api:server   amd64 BUILDING 0:42 │ 0041 compiling internal/http …               │
│ ✓ //lib/db:db        amd64 CACHED        │ 0042 …                                       │
│ ✗ //lib/auth:test    amd64 FAILED   0:09 │                                              │
│   └ BOB-E-2005 tests failed  lib/auth/bob.go:12:3                                       │
├ Timing waterfall ───────────────────────────────────────────────────────────────────── ┤
│ //lib/db:db       ███                                                                    │
│ //svc/api:server      ██████████▒▒                                                       │
├ Artifacts ─────────────────────────────────────────────────────────────────────────────┤
│ /nix/store/…-api-server   1.2 MiB   [copy]    image api/server:b-01jb…k2p  sha256:…     │
└────────────────────────────────────────────────────────────────────────────────────────┘
```
(✓ ✗ ● here stand for the coloured StatusBadge text OK/FAIL/RUN; no emoji or icon fonts.)
Graph mode: layered left-to-right SVG (longest-path layering, ≤ 300 nodes; above that the toggle
is disabled with `graph has <n> packages; use the table`).

Fleet `/fleet`
```
Workers   filter [text] platform [any ▾] state [any ▾]
ID              Platform     State     Jobs  CPU  Mem     Heartbeat  Labels         Actions
w3k…p0q         linux/amd64  ACTIVE    1     8    15 GiB  4s         ssd            [Drain][Revoke]
Orchestrators
ID     Endpoint                 Version  Started   Heartbeat  Leader of
o-1    https://10.0.0.5:8443    0.1.0    3d        2s         promotion, gc
```

Cache `/cache`: stat strip (size, paths, hit rate 24 h / 7 d, retention days), hit-rate line
chart (30 days), top packages table (project, label, hits 7 d, size), path lookup input
(store hash → `GetPath` key/value view).

Registry `/registry`: tabs Releases (project, name, version, visibility, platforms, date) and
Images (repository, tags, public, updated); release detail shows `bob install acme/api@1.4.2`,
assets with size/sha256/download link, closure roots, image refs (`podman pull …`).

Access `/access`: tabs Users (login, source, roles, last login, disabled), Roles (id,
permissions count; editor = method picker from `ListPermissionCatalog` × project), Matrix
(rows principals, columns projects, cells role names; filter by project/role), Service accounts
(tokens table, "create token" dialog shows the secret once with a copy button), OIDC (providers,
mappings `claim = value → role @ project`).

Settings `/settings`: General (server name, base URL, session policy), Forges (integrations,
health, webhook URL, "rotate secret", recent deliveries), Signing keys (name, state, public key,
rotate/retire), Retention (days per kind), Webhooks (outgoing).

Analytics `/analytics`: project selector, build history bars (succeeded/failed per day),
p50/p90 duration line, slowest packages table (`PackageDurations`), recent failures.

Login `/login`
```
            bob
            ┌──────────────────────────┐
            │ Login       [          ] │
            │ Password    [          ] │
            │ [ Log in ]               │
            │ ──────── or ──────────── │
            │ [ Continue with Corp SSO ]│
            └──────────────────────────┘
```

#### R-UI-001 Static SvelteKit app served by Go
- **Statement:** The UI is a static build embedded in `bob-server`.
- **Acceptance:** `web/build` contains `200.html` and no server output; `svelte.config.js` uses
  `adapter-static`; `GET /p/x/builds/y` on a running server returns `200.html` with
  `Cache-Control: no-cache`; `/_app/immutable/*` returns `immutable`.
- **Source:** R-UI-001, D5.

#### R-UI-002 Pages
- **Statement:** All routes of §6.10.3 exist and show the listed data.
- **Acceptance:** Browser e2e against a seeded fixture server: each route renders without console
  errors and shows the seeded entity (by id text); build page shows graph table, live log lines
  appearing within the R-LOG budget, waterfall bars and artifacts.
- **Source:** R-UI-002.

#### R-UI-003 Visual style
- **Statement:** Tokens and rules of §6.10.2.
- **Acceptance:** (manual) code-steward review with the checklist: no gradients, no `rounded-*`
  other than `rounded-xs`/`rounded-none`, no shadow/blur/glow, no emoji, yellow only as fill.
  A helper script `web/scripts/style-check.sh` greps for these and is run by the steward (not a
  CI gate, D45).
- **Source:** R-UI-003, D21, D45.

#### R-UI-004 Light default, dark optional
- **Statement:** Light is the default even when the OS prefers dark; the user can choose Dark or System.
- **Acceptance:** e2e with `prefers-color-scheme: dark` and empty storage → `<html data-theme="light">`
  and computed body background `rgb(255, 255, 255)`; choose Dark → `#0e0e0d`, persists after reload.
- **Source:** R-UI-004, D35.

#### R-UI-005 Tailwind with hand-written components
- **Statement:** No component or class-composition libraries.
- **Acceptance:** `web/package.json` dependencies ⊆ {svelte, @sveltejs/kit, @sveltejs/adapter-static,
  @sveltejs/vite-plugin-svelte, vite, tailwindcss, @tailwindcss/vite, @connectrpc/connect,
  @connectrpc/connect-web, @bufbuild/protobuf, typescript, svelte-check, test tooling}; a test fails on any other.
- **Source:** R-UI-005, D21.

#### R-UI-006 Filterable lists, linked ids
- **Statement:** Every list has a filter; every id links to its detail page.
- **Acceptance:** e2e over all list pages: typing a seeded id prefix in the filter reduces rows to
  the matching ones and updates the URL query; every cell with class `id` is an `<a href>` whose
  target route renders the same id.
- **Source:** R-UI-006.

#### R-UI-007 First load budget (new)
- **Statement:** The UI loads fast on small machines.
- **Acceptance:** **[proposal]** initial JS ≤ 250 KiB gzip, CSS ≤ 30 KiB gzip; overview first
  contentful paint ≤ 1.0 s [8c/15G] and ≤ 2.0 s [2c/4G] against a local server (no measurement
  yet; derived from the bundle cap and API p99 below).
- **Source:** R-NFR-004, R-NFR-008.

## 6.11 Budgets [proposal]

| Budget | Value | Derived from |
|---|---|---|
| 100 MiB closure: worker upload to staging | ≤ 1.5 s [8c/15G]; ≤ 3 s [2c/4G] | quiet 0.64–0.76 s [8c/15G]; loaded 1.19–2.13 s [2c/4G] (S12 §3.8) |
| 100 MiB closure: promotion | ≤ 1.5 s [8c/15G]; ≤ 3 s [2c/4G] | quiet 0.49–0.54 s; loaded 0.89–2.27 s |
| 100 MiB closure: Nix substitution from bob, same LAN | ≤ 1.5 s [8c/15G]; ≤ 3 s [2c/4G] | quiet 0.61–0.67 s; loaded 1.11–1.84 s |
| narinfo latency (warm visibility cache) | p50 ≤ 5 ms, p99 ≤ 20 ms | 2–5 ms incl. a visibility HEAD (S12 §5.7) |
| Orchestrator RSS at 10 × 1 GiB parallel downloads | ≤ 64 MiB | 19–33 MiB for 1–2 |
| API unary p99 (Get*, warm caches, RustFS local) | ≤ 50 ms; List* ≤ 150 ms | S12 narinfo 2–5 ms + one dynamitedb GET per index slot (§05) |
| Webhook → BuildRecord created | p99 ≤ 500 ms after receipt | delivery latency is the forge's (Forgejo 2–3 s, S16 §3.2) |
| Checkout mint (Forgejo / GitLab) | ≤ 500 ms p99 | 153 / 145 ms median (S16 §3.2, loaded) |
| Live log line → UI (D55 pull over Attach, 250 ms chunks) | = §05 R-NFR-105: p99 ≤ 1 s; lake fallback p99 ≤ `log.flush_interval` + 10 s (40 s at the 30 s default) | S17 fan-out p99 223 ms; S10 lake p99 2.7 s at 5 s flush |
| Token revocation effective | ≤ 10 s (API), ≤ 30 s visibility | cache TTLs §6.3.1/§6.6.2 |

#### R-NFR-150 Cache throughput (new)
- **Statement:** The cache budgets above hold on the reference backend.
- **Acceptance:** `measure.sh`-style harness benchmark (RustFS, 5 runs) meets the upload,
  promotion and substitution rows in both profiles (median) and the narinfo latency row.
- **Source:** R-NFR-003, R-NFR-008, S12 quiet rerun.

## 6.12 Open points

- **[open] O-06-1** `public` list cap of 10,000 release ids per visibility document.
  Recommendation: keep the cap; if hit in practice, switch that document to a counter
  `{"public_count": n}` maintained by CAS.
- **[open] O-06-2** Source snapshot cap 512 MiB for `--remote`. Recommendation: keep, make it a
  setting; measure on real monorepos in Stage 3.
- ~~O-06-3~~ Live-log pull interval: closed at integration — the worker sends `LiveChunk`s every
  `live_log.chunk_interval` (250 ms, §05.11.1) over Attach while subscribed.
- **[open] O-06-4** GitHub not exercised live (S16). Recommendation: keep replay tests; add a
  manual live check against a test org before the first release (manual acceptance).
- **[open] O-06-5** Visibility LRU TTL 30 s (making a release private takes ≤ 30 s).
  Recommendation: accept and document.

## 6.13 Escalations

- **E-06-1 — closed at integration.** The inbound `WorkerAgentService` is replaced by §05's
  `WorkerControlService/Attach`, a stream the worker opens outbound to each orchestrator, which
  carries Wake (D54), live-log pull (D55), CancelJob and Drain. Workers need no inbound port;
  without Attach they still work by polling and lake-only logs (R-SRV-100).
