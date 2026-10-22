// Package rbac is bob's authorization (SPEC §6.4, M15.4): role permissions evaluated per RPC
// and project, the built-in role admin, the permission catalogue read from the methods'
// bob.authz.v1.rule options, the escalation check of role bindings, OIDC claim → role
// mapping evaluation, and the resolution of build/job/release/repository ids to projects.
//
// Identity (internal/auth) stores roles, bindings, the admin index and the mappings and
// serves RoleService; it calls this package for every decision.
package rbac
