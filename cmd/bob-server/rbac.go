package main

import (
	"github.com/megakuul/bob/internal/rbac"
	"github.com/megakuul/bob/internal/server"
)

// rbacParts sets RBAC (M15.4) in parts: the authorizer over the roles of the control bucket
// and the resolver of build/job/release/repository ids. Identity (internal/auth) must use
// the same parts.Authz for its LIST_FILTERED filtering and escalation checks.
func rbacParts(env *server.Env, parts *server.OrchestratorParts) {
	if env.Storage == nil || env.Storage.Control == nil {
		return // api.New keeps the admin-only authorizer; Open never lets this happen
	}
	authz, err := rbac.New(env.Storage.Control)
	if err != nil {
		// The rule table is invalid: api.New refuses to start with the same error.
		env.Log.Error("rbac not available", "err", err.Error())
		return
	}
	parts.Authz = authz
	parts.Resolve = &rbac.Resolver{Control: env.Storage.Control, Fleet: env.Storage.Fleet}
}
