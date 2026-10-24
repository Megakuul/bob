# I-R3: reviewer report for M5-M18, pass 2 (what pass 1 did not cover)

Role: reviewer (AGENTS.md §7), fresh context, read-only on code and tests. Date: 2026-10-24.
Scope: the "Not covered" list of `docs/review/m5-m18-reviewer.md` (I-R2). That covers
internal/rbac escalation rules; internal/auth (OIDC, local users, service accounts, token
minting and revocation, CLI OAuth flows); forge webhook intake (signatures, replay,
repository-to-project mapping, SSRF); internal/isolation; cmd/bob-builder and the
pkg/builder/inputs.go read-only input lock; the unpackers (pkg/engine/fetch/archive.go,
pkg/builder/unpack.go) and pkg/engine/materialise; pkg/cli (credentials, netrc, login, install);
pkg/output/* and pkg/release; and command or argument injection through strings that bob.go
supplies to the ecosystem generators.

Method: I read the code paths and checked them against SPEC. Findings marked **verified** were
confirmed with throw-away tests in a private copy (`/var/tmp/I-R3/repo`, deleted afterwards). The
input-lock tests ran real builds with bob's embedded Nix 2.34.9 on this host (arm64). Findings
marked "by inspection" were not run. The only repository files I wrote are this report and
plan/progress/I-R3.md.

## Findings, ranked

### G1 (high, authz, by inspection). Several GLOBAL methods grant privileges with no R-AUTH-152-style check, so "RoleService/*", "OidcService/*", "UserService/*" or "ServiceAccountService/*" on `*` are each equivalent to admin

- **Where:**
  - `internal/auth/oidc.go:590` `CreateOidcMapping`: checks only that the role exists. It never
    calls `rbac.CheckGrant`.
  - `internal/auth/users.go:403` `SetUserPassword`: works on any local user, admins included.
  - `internal/auth/sa.go:261` `CreateServiceAccountToken`: works on any service account,
    admin-bound ones included.
  - `internal/auth/roles.go:188` `CreateRole`: no escalation check. `DeleteRole` (`:355`) leaves
    OIDC mappings that name the role in place, and decides "still bound" from the best-effort
    `rmem/<role>` list.
- **SPEC:** R-AUTH-152 (stated only for `CreateRoleBinding`), R-AUTH-002, R-SEC-004, §6.4.1, and
  AGENTS §2.5 ("security boundaries are structural").
- **Failure scenarios:**
  1. A principal with role `{bob.access.v1.OidcService/*, *}` (meant for "manage login
     providers") creates the mapping `{provider, claim: "sub", value: <own subject>, role: admin,
     project: *}`. At the next login it holds `admin@*`. It can also add a provider whose issuer
     it controls and map any claim.
  2. A principal with `{UserService/*, *}` calls `SetUserPassword` on an admin user and logs in as
     that admin. With `{ServiceAccountService/*, *}` it mints an API token for an admin service
     account instead.
  3. A principal with `{RoleService/CreateRole, DeleteRole, *}` but without `UpdateRole`'s
     coverage: while nobody is logged in under an OIDC-mapped role `dev`, it deletes `dev` (rmem
     is empty) and recreates it as `[{*, *}]`. The mapping still names `dev`, so every user in
     that group, the attacker included if they are in it, gets `*` on the mapped project at next
     login. This bypasses `CheckRolePermissions`, which only `UpdateRole` runs. A failed
     `addMember` (logged only) creates the same gap for manual bindings.
- **Fix (proposed):**
  - `CreateOidcMapping`: run `CheckGrant(caller, role, project)`, the same check as
    `CreateRoleBinding`.
  - `SetUserPassword`, `CreateServiceAccountToken`, `UpdateUser{disabled:false}` and similar:
    refuse when the target principal's effective permissions are not covered by the caller's
    (`Catalog.Covers`).
  - `CreateRole`: run `CheckRolePermissions` for every permission when any binding or OIDC mapping
    already names the id. Better still, have `DeleteRole` also refuse while OIDC mappings name the
    role (or delete those mappings).
  - Alternatively, if the owner accepts "these GLOBAL services are admin-only", say so in SPEC
    §6.4.1 and the UI. That needs an owner decision.

### G2 (medium, auth, by inspection). OIDC `require_mapping` defaults to false, not true

- **Where:** `internal/auth/oidc.go:428` (`store.Set(in.GetRequireMapping())`). The proto field is
  a plain `bool require_mapping = 9; // default true` (`proto/bob/access/v1/access.proto:372`), so
  an omitted field reads as `false`. Neither the UI (`web/src/routes/(app)/access/Oidc.svelte`) nor
  the CLI sets it.
- **SPEC:** §6.3.2 ("`require_mapping = true` (default)"), R-AUTH-004.
- **Failure scenario:** an admin adds a provider with a public issuer (Google, GitHub, a shared
  Keycloak realm) and does not touch the flag. Then anyone with an account at that issuer can log
  in. That creates an unbounded number of `UserRecord`s. Each new user can call every AUTHENTICATED
  method: `WhoAmI`, `ListRoles`, `GetRole`, `ListPermissionCatalog`,
  `AnalyticsService/GetOverview`. A wrong default here is a fail-open.
- **Fix:** make the field `optional bool` (presence) and store `true` when it is absent, or
  invert it to `allow_unmapped`. Add a test: a provider created without the field refuses an
  unmapped login.

### G3 (medium, webhook replay, by inspection). GitHub and Forgejo deliveries can be replayed with a new, unsigned delivery id

- **Where:** `internal/forge/integration/intake.go:107` (de-duplication key = `ev.DeliveryID`);
  `internal/forge/github/github.go:181` and `internal/forge/forgejo/forgejo.go:168` (the id comes
  from `X-GitHub-Delivery` / `X-Forgejo-Delivery`, which the HMAC does not cover). The GitLab path
  is fine: `Webhook-Id` is signed and the timestamp window is ±5 min. GitLab's opt-in plaintext
  token mode has the same issue as GitHub and Forgejo.
- **SPEC:** §6.9.3 step 3, R-PIPE-008 ("replayed delivery id → 200 and still one build").
- **Failure scenario:** anyone who obtains one signed delivery replays the same body and signature
  with a fresh `X-GitHub-Delivery` header. Sources include a TLS-terminating proxy log, the forge's
  "recent deliveries" page that repository admins can see, or a debugging dump. Each replay is
  accepted and creates a build. That costs worker time (DoS). Replaying an old `push` to the
  default branch rebuilds an old commit. If the project's pipelines release or deploy on push,
  the old version is published again. No time window limits this.
- **Fix:** for adapters without a signed id, also de-duplicate on SHA-256(body). Create both
  `ForgeDeliveryRecord(integration, id)` and `(integration, "body-"+sha)` create-only, and treat
  either one existing as a duplicate. Optionally refuse push events whose `after` is not the
  branch's current head (forge API call). Add a fixture test: the same body with a new delivery id
  gives 200 `duplicate`.

### G4 (medium, client integrity, verified). A registry's release version is written unescaped into the install shim, so a hostile registry runs shell code despite pinned keys

- **Where:** `pkg/cli/cmd_install_profile.go:387` `shimText`. `en.Version` (from
  `ResolveRelease`, never validated on the client) is placed raw in a `#` comment line of a
  `#!/bin/sh` script.
- **SPEC:** §02.8 step 5 (shim), O-02-7 (registry key pinning), AGENTS §5.4 (hostile input).
- **Failure scenario:** the registry's signing keys are pinned (TOFU or `--key`), so only store
  paths signed by those keys install. The version string is not signed. A registry server that is
  compromised or malicious (or a MITM on an `http://` registry, see G9) answers with
  `version = "1.0\n<command>\n#"`. `bob install` writes a shim whose second line is `<command>`,
  and it runs as the user every time the installed tool is called.
  Test `TestIR3ShimVersionInjection`: the generated shim ran `touch <marker>`.
- **Fix:** validate the version on the client with the server's `ValidVersion` syntax
  (`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`, `internal/build/jobspec/match.go:102`) before using it.
  Apply the same check to `Ref` and to every server-supplied string that reaches the shim or the
  generation files. Add a regression test.

### G5 (low-medium, auth, by inspection). OIDC login CSRF: the `state` is not bound to the browser that started the login

- **Where:** `internal/auth/oidc.go:133` `oidcStart` / `:170` `oidcCallback`. The state record is
  keyed only by SHA-256(state). No cookie ties it to the browser that started the flow.
- **SPEC:** §6.3.2 (OIDC with state and nonce). Not explicitly required, but standard RP practice
  (OIDC Core §3.1.2.1, RFC 6749 §10.12).
- **Failure scenario:** the attacker starts a login, signs in at the IdP with their own account,
  stops before the callback, and sends the victim the `/auth/oidc/callback?code&state` link. The
  victim's browser receives a `bob_session` of the attacker's account. Anything the victim then
  uploads or creates (sources, a `bob login` device approval on `/device`, a CLI consent) is done
  as the attacker. The nonce does not help, because the attacker obtained the matching ID token.
- **Fix:** set a short-lived `HttpOnly; Secure; SameSite=Lax` cookie with a random value in
  `oidcStart`, store its hash in the state record, and compare in the callback.

### G6 (low, network policy, by inspection). Fork pull requests from anyone build automatically

- **Where:** `internal/build/event.go:34` (fork PRs get `Trigger.Fork` and are otherwise built
  like any PR); no approval gate exists anywhere.
- **SPEC:** R-FLEET-006 ("Fork PR: fetches `refs/pull/N/head` … with the base-repo
  credential"). There is no SPEC rule about who may trigger builds.
- **Failure scenario:** on a public repository linked to a project, any GitHub user opens a PR. A
  worker evaluates the fork's `bob.go` (sandboxed) and builds it (sandboxed), and promotion
  publishes the outputs into the project's cache namespace under the orchestrator's signature.
  The sandboxes hold. But this is the input F1 of pass 1 needs: an untrusted author gets job
  credentials (the project's download token, a checkout token for the base repository) on a
  worker. It also costs worker time.
- **Fix:** owner decision. Options: a per-project setting "build fork PRs: never / after
  approval / always" (default after approval), or promote fork-PR outputs with a separate
  visibility that no release closure may use.

### G7 (low, client credential routing, by inspection). The server chooses which host receives the CLI's API token through netrc

- **Where:** `pkg/cli/creds.go:248`. `ent.Nix.Substituter` (from the server's `WhoAmI`) becomes
  both the substituter URL and the netrc machine, with the API-scope access token as password. By
  contrast, `cacheURL` (`pkg/cli/cmd_install.go:149`) does refuse other hosts ("The token is
  never sent to another host").
- **SPEC:** R-DEV-004 / §6.6.5 (`machine <server>`).
- **Failure scenario:** a server, or anyone who can change its `WhoAmI.nix.substituter` setting,
  points the substituter at another host. Nix then sends the user's `bob_at_` token there, and
  that token is valid for the whole API, not just downloads. If the host is another bob server
  the user is logged in to, the netrc gets two `machine` lines for one host and the wrong token
  can be sent.
- **Fix:** accept `Nix.Substituter` only when its scheme and host equal the server's (as
  `cacheURL` does), otherwise use `<server>/nix`. Consider writing the session's `bob_dt_`
  download token (already returned by `/oauth/token` and currently discarded) instead of the API
  token. SPEC allows both.

### G8 (low, isolation hardening, by inspection)

- The job container (`internal/isolation/container.go`) has no network namespace and its root
  keeps all capabilities. This matches R-FLEET-008: builds get Nix's own net ns, and evalbox has
  `CLONE_NEWNET`. The consequence is that every non-Nix process bob starts inside the container
  (the `job-run` engine, `git`, Go module downloads in planning) has host networking, including
  loopback and IMDS. Keep that list short. A `CLONE_NEWNET` with only a veth/slirp to S3, the
  orchestrator and the caches would make the "no IMDS" acceptance case hold for the whole
  container, not just builds.
- The child's whole configuration, `Env` included, is passed on its command line
  (`exec.Command("/proc/self/exe", ChildArg, string(cj))`). Today `jobEnv()` holds no secrets.
  Pass the config on a pipe so a later secret in `Env` cannot appear in `/proc/<pid>/cmdline`
  during setup.
- `pkg/builder/inputs.go` takes the store directory from `$NIX_STORE`. A derivation env that
  overrides it (Nix copies the drv env after setting `NIX_STORE`) makes the lock find no mounts,
  and the job then runs unlocked. Today no bob.go-supplied string reaches the drv env:
  `bob.Run`'s `Env` goes through the JSON spec, not the drv env. Harden by deriving the store
  directory from `/proc/self/exe`'s store path, or by failing closed when no mount is found but
  `/proc/self/exe` lives in a store path.

### G9 (low, client transport, by inspection)

- `normServer` (`pkg/cli/creds.go`) accepts `http://` for `bob login` and `bob registry add`.
  `registryAdd` (`pkg/cli/cmd_install_registry.go:162`) pins keys by TOFU from
  `GetServerInfo`. Over plain http a network attacker supplies the keys, and with them every
  installed binary. Refuse http for non-loopback hosts unless `--key` is given (registry) or
  `--insecure` is set (login).

## Checked and found sound (no finding)

- **Read-only input lock (A370), verified.** Two throw-away Nix builds against the real embedded
  Nix 2.34.9:
  1. A flat-file store path as input: chmod and write give EROFS. Nix bind-mounts it, it does not
     hard-link it.
  2. Job code creating a nested user and mount namespace: `MS_REMOUNT|MS_BIND` without
     `MS_RDONLY` gives EPERM (the kernel locks the flag), `umount` gives EINVAL, and writes give
     EROFS.

  `nix-store --verify-path` stayed clean. The outer stage is non-dumpable before release, the
  locked stage checks it has zero effective capabilities, and the existing
  `TestBuilderInputsReadOnlyInNix` passes. Only G8's `$NIX_STORE` point remains.
- **RBAC core** (`internal/rbac`): `Effective`, `Allows`, `Covers` and `CheckGrant` follow §6.4.1.
  `Covers` expands wildcards to catalogue methods and needs `*` to grant `*`. `UpdateRole` checks
  the permissions it adds against the role's own project field, which bounds every binding's
  effective project. The admin index CAS keeps the last admin.
- **Tokens and sessions** (`internal/auth/identity.go`, `sessions.go`, `http.go`): tokens are
  stored as SHA-256 only. SA tokens are re-checked against the SA record's list. Access and
  download tokens die with their session. Download-scope SA tokens are refused on the API. PKCE
  uses S256 only, codes are single-use (deleted before checks), and `redirect_uri` is
  loopback-only and must match exactly. The device flow handles `slow_down` and single use. The
  consent and device POSTs check `Origin` on top of SameSite=Strict, and the OIDC `next` is
  confined to paths.
- **Webhook signatures:** constant-time HMAC, an empty secret never verifies, size checks come
  before parsing, nothing is stored for unsigned deliveries, and `CheckEvent` validates
  SHAs, refs and clone URLs before any git use. Repositories map to projects by repo id within the
  integration (R-PIPE-002).
- **Unpackers:**
  - `pkg/engine/fetch/archive.go`: two passes, nothing written before validation, `..` and
    absolute names refused, no writes through symlinks, chain-aware `linkInside`, hard links as
    copies, entry count and size caps, zip size lies caught.
  - `pkg/builder/unpack.go`: one pass, same rules, final link check; runs inside the Nix sandbox.
  - `pkg/engine/materialise`: no symlink traversal in `mkdirs`, temp file plus rename, sha256
    re-checked on copy, git-tracked files never touched.
- **CLI install:** narinfo `StorePath`/`URL`/`References` validated, ed25519 signature checked
  against pinned keys before download, NAR size and hash checked, then Nix verifies again with
  only the pinned keys and cache.nixos.org's. Bin names in shims are restricted to `safeName`.
  `scripts/fetch-nix.sh` pins the download sha256, the NAR hash and the binary hash.
- **Release manifest** (`pkg/release/bop`): written inside the derivation from built outputs.
  Server-side release versions are validated (`jobspec.ValidVersion`).
- **Reproducible tar** (`pkg/builder/reprotar.go`): fixed mtime, uid/gid 0, lexical order with
  parents first, no names. A non-ASCII name produces a PAX `path` record (deterministic, and SPEC
  R-OUT-010 says "format chosen by archive/tar"). Only the code comment "never PAX records" is
  wrong.
- **Generators:** bob.go strings that become argv go through validation regexps (Go module paths
  and versions, crate, feature, opam, findlib and define names) and never through a shell on the
  host. `git` in proto `Breaking` uses `--end-of-options`. Module downloads validate path and
  version first. npm lifecycle scripts run only for allowlisted packages and inside the sandbox.
  The one place bob.go strings reach a host shell is `bob dev` hooks (`pkg/cli/dev_shell.go:61`).
  That is by design for an explicit `bob dev` (like `nix develop`'s shellHook), but it does leave
  the evaluator sandbox. Consider gating it on the R-DEV-009 allow list like the shell hook is.

## Not covered (still open)

internal/ui; the AppImage runtime (`pkg/output/appimage/gen/runtime`: squashfs reader and launcher);
OCI layer generation beyond the tar writer; every ecosystem `bop` builder in depth (they run inside
the sandbox, so I only checked for host-side injection); the `internal/worker` job runner beyond
the isolation call.

## Proposed fixes, in order

1. G1: run escalation checks on `CreateOidcMapping`, on credential-setting methods aimed at more
   privileged principals, and on `CreateRole` when mappings or bindings name the id. Or have the
   owner declare these services admin-only.
2. G2: `require_mapping` presence-aware with default true, plus a test.
3. G4: validate server-supplied version and ref strings in the CLI before writing shims, plus a
   regression test.
4. G3: de-duplicate deliveries on the body hash for adapters without a signed id, plus a fixture
   test.
5. G5: bind the OIDC state to a browser cookie.
6. G7, G9: pin the substituter host to the server, consider the `bob_dt_` token for netrc, refuse
   http TOFU.
7. G6: owner decision on fork-PR builds.
8. G8: hardening (container netns, config over a pipe, store directory from the executable path).

## Owner-relevant items

1. **G1:** decide whether GLOBAL access-management services count as admin-equivalent (document
   it) or get escalation checks (code).
2. **G6:** fork-PR build policy. Without one, pass 1's F1 (a compromised worker poisons other
   projects) is reachable by any GitHub user who can open a PR, provided they also escape the
   build sandbox.
