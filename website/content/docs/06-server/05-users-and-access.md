---
title: Users and access
section: Server
order: 5
summary: Local users, OIDC login, service accounts and tokens, and role-based access per project.
---

This page shows who can log in to a bob server and what they may do: users (local or through
an OIDC provider), service accounts for automation, and roles bound per project.

Everything here is done in the web UI (Access page) or through the API. The API is
[connect](https://connectrpc.com) over HTTP: `POST /api/<package>.<Service>/<Method>` with a
JSON body. The examples use `curl` with an admin session.

## Log in to the API with curl

```bash
curl -si -X POST -H 'Content-Type: application/json' \
  -d '{"login":"admin","password":"correct-horse-battery"}' \
  http://127.0.0.1:18443/api/bob.auth.v1.AuthService/LoginPassword | grep -i set-cookie
```

```text
Set-Cookie: bob_session=bob_st_…; Path=/; Max-Age=604800; HttpOnly; Secure; SameSite=Strict
```

Requests authenticated with the session cookie must also send `Bob-Csrf: 1`. Scripts should
rather use a service-account token as `Authorization: Bearer <token>`.

## Users

- **Local users** have a password (12 to 256 bytes, stored with argon2id). Admins create them
  (`UserService/CreateUser`) and set passwords (`SetUserPassword`). Repeated failures are
  throttled.
- **OIDC users** log in through an identity provider. Configure a provider
  (`OidcService/CreateOidcProvider`: issuer URL, client id and secret, scopes, the claim used
  as user name). Its redirect URL is `<base URL>/auth/oidc/callback`. Mappings
  (`CreateOidcMapping`: claim, value, role, project) turn claims such as `groups: ["dev"]` into
  role bindings at every login. With `require_mapping` (the default), a login without any
  matching mapping is refused.

> [!NOTE]
> OIDC login is covered by the server test suite with a test identity provider; it was not
> tried against a real provider for this documentation.

## Roles and bindings

A role is a list of permissions; a permission is an API method (or `Service/*`, or `*`) on a
project (or `*`). A binding gives a user or service account a role on a project or on all
projects (`*`). The only built-in role is `admin` (everything, everywhere), and at least one
admin always remains.

Create a role that can build and read the cache, and bind it on one project:

```bash
H=(-H "Cookie: $SESSION" -H 'Bob-Csrf: 1' -H 'Content-Type: application/json')
curl -s -X POST "${H[@]}" http://127.0.0.1:18443/api/bob.access.v1.RoleService/CreateRole -d '{"role":{"id":"builder","description":"build and read projects","permissions":[
  {"method":"bob.build.v1.BuildService/*","project":"*"},
  {"method":"bob.cache.v1.CacheService/GetPath","project":"*"},
  {"method":"bob.project.v1.ProjectService/GetProject","project":"*"}]}}'
curl -s -X POST "${H[@]}" http://127.0.0.1:18443/api/bob.access.v1.RoleService/CreateRoleBinding \
  -d '{"principalId":"sa:ci","role":"builder","project":"hello-oci"}'
```

A role's `*` project is narrowed by the binding: `builder` bound on `hello-oci` grants its
methods on `hello-oci` only. You cannot grant permissions you do not have yourself. Downloads
use the same permissions: cache paths need `CacheService/GetPath`, image pulls
`RegistryService/GetRepository`, release assets `ReleaseService/GetRelease`.

## Service accounts and tokens

Service accounts are for CI and other automation. They get role bindings like users and any
number of tokens, each with a scope:

| Scope | Valid for |
|---|---|
| `api` | the API and all downloads |
| `download` | only the cache, the registry and release downloads |

```bash
curl -s -X POST "${H[@]}" http://127.0.0.1:18443/api/bob.access.v1.ServiceAccountService/CreateServiceAccount \
  -d '{"serviceAccount":{"id":"ci","description":"CI runner"}}'
curl -s -X POST "${H[@]}" http://127.0.0.1:18443/api/bob.access.v1.ServiceAccountService/CreateServiceAccountToken \
  -d '{"serviceAccountId":"ci","name":"runner-1","scope":"TOKEN_SCOPE_API","ttlDays":30}'
```

```text
{"token":{"id":"tzpcpj1d5mv93", "name":"runner-1", "scope":"TOKEN_SCOPE_API", "expiresAt":"…"}, "secret":"bob_sa_…"}
```

The secret is shown once. Use it from the CLI:

```text
$ echo "$SECRET" | bob login --token-stdin http://127.0.0.1:18443
logged in to 127.0.0.1:18443 as sa:ci (token valid 30 days)
$ bob whoami
sa:ci on 127.0.0.1:18443 (sa:ci)
  token scope: api
  projects: hello-oci
```

Or without storing it: `BOB_TOKEN=$SECRET BOB_SERVER=http://127.0.0.1:18443 bob build --remote`.
A call on a project without permission fails with `BOB-E-4010` and names the missing
permission; an invalid or expired token fails with `BOB-E-4008`.

## User tokens

`bob login` gives the CLI a short-lived access token (1 hour) and a refresh token; the CLI
refreshes by itself. Browser sessions last at most 7 days. Revoke a CLI login with
`bob logout`; users see and revoke their sessions on the Account page.

## Next steps

- [Projects and forges](06-projects-and-forges.md).
- [CI and remote builds](07-ci-and-remote-builds.md).
