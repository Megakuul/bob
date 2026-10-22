# I-B4 (Track B: M15.2 identity → M15.3 OAuth flows) — internal/auth, new internal/store files, internal/http auth routes, cmd/bob-server admin

## Done
- Read AGENTS, impl-brief, HARNESS, I-B1/I-B3 progress, SPEC §6.3, §05.4.2/§05.4.5, tests/auth.

## Next
- store records (internal/store/identity.go), internal/auth identity + services, OAuth/OIDC HTTP, admin create-user.

## DECISIONS

## NEEDS

## TEST PROBLEM

## Checkpoint (code written)
- internal/store/identity.go (records), internal/auth/{secret,password,identity,sessions,users,roles,sa,oidc,authsvc,http,bootstrap}.go,
  cmd/bob-server/auth.go (authParts, admin create-user), internal/http Routes.Patterns + NixCacheInfo,
  internal/server process.go (Routes, Bootstrap hook), clock_bobtest.go (stat-based re-read).
- Integrated I-B5: rbac.Authorizer for catalogue, CheckGrant, CheckRolePermissions, Scope, ValidProject, Match/ReplaceOIDC.
