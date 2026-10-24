# I-B5 (Track B: M15.4 RBAC) — internal/rbac, wiring in cmd/bob-server, hooks in internal/api

## Done
- Read AGENTS, impl-brief, HARNESS §8.3, I-B1/I-B3 progress, SPEC §6.3.2/§6.4/§6.5.19/§05.4.2,
  tests/rbac (+ ref), tests/auth/authenv.
- internal/rbac:
  - model.go: Permission (= store.Permission), Effective (SPEC §6.4.1 rule), MethodMatches
    (exact / service wildcard / "*"), Allows, Touches; built-in admin = {*, *}.
  - catalog.go: Catalog from the rule table (api.AllRules, worker methods excluded): Methods,
    Entries (ListPermissionCatalog form), Kind, Expand, CheckPermissions (method must name the
    catalogue, project "*" or `^[a-z0-9][a-z0-9-]{0,39}$`, dedupe + sort), ValidProject,
    ValidRoleID.
  - escalation.go: Catalog.Covers (wanted permissions expanded through the catalogue).
  - authorizer.go: Roles (role/<id> via store.GetCached, conditional GET on every check, admin
    built in), RoleSource interface, Authorizer (api.Authorizer: Allowed / Denied (some
    permission on the project, or project "*") / NoAccess), Effective, Scope (LIST_FILTERED
    projects), CheckGrant (R-AUTH-152: "binding would grant permissions you do not have"),
    CheckRolePermissions (UpdateRole additions), rbac.New(control) (catalogue from api.AllRules).
  - oidc.go: Match (claim equals / array contains, per provider, dedupe), ReplaceOIDC (pure:
    oidc bindings replaced by the matched set, manual kept, kept ids stay), SourceManual/OIDC.
  - resolve.go: Resolver (api.Resolver): build → BuildRecord.pr; job → fleet JobRecord spec
    project (or Resolver.Job hook); release "<project>.<num10>" + GET rel/<p>/n/<num>;
    repository "<project>/<image>" by prefix; anything else NotFound "<what> not found".
- internal/api/rbac.go (additive): `api.Forbidden(msg)` (PermissionDenied with own message),
  `api.AllRules()` (rule table of every served service; test proves it equals api.New's).
- cmd/bob-server/rbac.go + 3-line change in parts.go: parts.Authz = rbac.New(control),
  parts.Resolve = &rbac.Resolver{Control, Fleet}.
- Tests (internal/rbac): TestAccessTable (R-AUTH-002 rows + NotFound/PermissionDenied split +
  global), TestAccessAgainstReference (400 random role sets × every PROJECT/GLOBAL method of
  proto/ vs tests/rbac/ref.Expect), TestCheckGrant (all R-AUTH-152 cases), TestCatalog,
  TestOIDC (R-AUTH-004 scenario), TestScope, TestResolver (RustFS; incl. deleted role stops
  granting on the next call). All green. gofmt, GOWORK=off vet, staticcheck, boblint
  (nopanic/noexit/nolist/safego/importboundary) clean on internal/rbac, internal/api,
  cmd/bob-server. `tools/ci -only fmt,vet,lint,layering` fails only on internal/auth (I-B4
  mid-edit: undefined plainError/httpFail/providers).
- tests/rbac: TestRAUTH006 proto / fixture / SPEC summary / server-permission-catalog pass;
  everything else is blocked on identity (CreateUser, LoginPassword, RoleService CRUD answer
  501 until I-B4 is wired) and on the devstack seed.

## DECISIONS
- D-IB5-1 Split with I-B4: when this unit started, I-B4 had already written every RBAC record
  (store/identity.go: RoleRecord, Permission, RoleBindingEntry, RoleMembers, AdminIndex as a
  principal set at `adminidx/all`, OidcMappings) and RoleService (role CRUD, bindings, admin
  invariant, rmem, ListRoleBindings) in internal/auth. To avoid two implementations,
  internal/rbac holds the decisions only (evaluation, catalogue, escalation, claim matching,
  id resolution) and no records: internal/store/rbac_records.go was not kept (it would have
  duplicated identity.go). I-B4 calls rbac (NEEDS).
- D-IB5-2 A PROJECT call on project "*" (e.g. CreateRoleBinding @*) without permission is
  Denied → PermissionDenied, not NotFound: "*" names no resource whose existence could leak;
  R-AUTH-152 expects PermissionDenied for "any binding @*".
- D-IB5-3 Escalation coverage expands wanted method patterns through the catalogue: a wanted
  `Svc/*` or `*` is covered when every catalogue method it names is allowed by the caller on
  the wanted project; a wanted project "*" needs held "*" permissions. Unknown methods are
  never covered.
- D-IB5-4 Role permissions are read with a conditional GET on every check (1 request, 304
  when unchanged): role edits/deletes take effect on the next call on every orchestrator
  (the RBAC matrix re-creates roles with the same id within seconds).
- D-IB5-5 Resolver: finalised jobs have no job → build index in SPEC §05 yet; job ids
  resolve through the fleet JobRecord while it exists, else NotFound. `Resolver.Job` is the
  hook for the builds milestone. Repository names resolve by prefix (the registry handler
  answers NotFound for a missing image, same as no permission).
- D-IB5-6 (beyond SPEC, principle 5) CheckRolePermissions: permissions added to a role by
  UpdateRole must be held by the caller on "*", since the role may already be bound @*;
  otherwise a holder of {RoleService/UpdateRole, *} could escalate through an existing
  binding. Offered to I-B4; owner may overturn.
- D-IB5-7 Project ids in permissions/bindings are checked by syntax only
  (`^[a-z0-9][a-z0-9-]{0,39}$`), not existence: the suites bind on p1/p2 (shorter than the
  3-char slug rule) and a binding may precede its project.

## NEEDS (I-B4, internal/auth + cmd/bob-server/parts.go)
- Wiring: after `rbacParts(env, &parts)` pass `parts.Authz` as `auth.Config.Authz`; the
  RoleService catalogue func = `parts.Authz.(*rbac.Authorizer).Catalog().Entries`.
- CreateRoleBinding: before the write call
  `authz.CheckGrant(ctx, api.PrincipalFrom(ctx), role, project)` (R-AUTH-152; returns NotFound
  "role not found" for an unknown role and PermissionDenied "binding would grant permissions
  you do not have"). UpdateRole (optional, D-IB5-6): `authz.CheckRolePermissions(ctx, caller,
  added)`.
- Project validation in roles.go uses slugRE (min 3 chars): "p1"/"p2" of the suites are
  refused. Use `rbac.ValidProject` for permission and binding projects.
- OIDC login: `matched := rbac.Match(mappings, providerID, claims)`; refuse when
  `len(matched) == 0 && require_mapping`; inside updateBindings use
  `rbac.ReplaceOIDC(ro, matched, bindingID, now)`.
- R-AUTH-003: the bootstrap admin of the test stack must be a real user ("user:u…") with one
  admin@* binding visible in ListRoleBindings{role: admin} and counted in adminidx; today
  BOB_ADMIN_TOKEN is the StaticToken `sa:bootstrap` (internal/server/process.go, I-B3).
- WhoAmI.projects should come from effective permissions (`Authorizer.Effective`, "*" if any
  {*, *}), not binding projects (builtin.go of I-B3 uses binding projects).

## TEST PROBLEM
- (none)
