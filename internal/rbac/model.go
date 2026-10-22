package rbac

import (
	"strings"

	"github.com/megakuul/bob/internal/store"
)

// Permission is {method, project} of SPEC §6.4.1. Method is "<pkg>.<Service>/<Method>",
// "<pkg>.<Service>/*" or "*"; Project is a project id or "*".
type Permission = store.Permission

// Any is the wildcard of methods and projects.
const Any = "*"

// Admin is the id of the only built-in role. It grants {*, *} and cannot be changed.
const Admin = "admin"

// adminPermissions are the permissions of the built-in role admin.
func adminPermissions() []Permission { return []Permission{{Method: Any, Project: Any}} }

// Effective returns the effective permissions of a binding on bindProject of a role with the
// permissions perms (SPEC §6.4.1): for each role permission rp, project = rp.project if the
// binding is on "*", the binding's project if rp.project is "*", rp.project if both are
// equal, otherwise none.
func Effective(perms []Permission, bindProject string) []Permission {
	out := make([]Permission, 0, len(perms))
	for _, rp := range perms {
		switch {
		case bindProject == Any:
			out = append(out, rp)
		case rp.Project == Any:
			out = append(out, Permission{Method: rp.Method, Project: bindProject})
		case rp.Project == bindProject:
			out = append(out, rp)
		}
	}
	return out
}

// MethodMatches reports whether permission method pm covers the full method name
// ("<pkg>.<Service>/<Method>"): exactly, by its service wildcard, or by "*".
func MethodMatches(pm, method string) bool {
	if pm == Any || pm == method {
		return true
	}
	svc, _, ok := strings.Cut(method, "/")
	return ok && pm == svc+"/*"
}

// projectMatches reports whether permission project pp covers the call's project. A call on
// project "*" (a global method, or a binding on every project) needs a "*" permission.
func projectMatches(pp, project string) bool { return pp == Any || pp == project }

// Allows reports whether some permission of eff allows method on project.
func Allows(eff []Permission, method, project string) bool {
	for _, p := range eff {
		if MethodMatches(p.Method, method) && projectMatches(p.Project, project) {
			return true
		}
	}
	return false
}

// Touches reports whether some permission of eff is on project, whatever its method. It
// decides PermissionDenied (some permission on the project) versus NotFound (none).
func Touches(eff []Permission, project string) bool {
	for _, p := range eff {
		if projectMatches(p.Project, project) {
			return true
		}
	}
	return false
}
