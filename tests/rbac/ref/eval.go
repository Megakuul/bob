package ref

import "strings"

// Permission is {method, project} of SPEC §6.4.1; method is "<pkg>.<Service>/<Method>",
// "<pkg>.<Service>/*" or "*"; project is a project id or "*".
type Permission struct{ Method, Project string }

// Binding grants every permission of a role on Project ("*" or a project id).
type Binding struct {
	Permissions []Permission // the role's permissions
	Project     string
}

// Effective returns the effective permissions of binding b (SPEC §6.4.1): project = rp.project
// if the binding project is "*", the binding project if rp.project is "*", rp.project if both
// are equal, otherwise none.
func Effective(b Binding) []Permission {
	var out []Permission
	for _, rp := range b.Permissions {
		switch {
		case b.Project == "*":
			out = append(out, Permission{rp.Method, rp.Project})
		case rp.Project == "*":
			out = append(out, Permission{rp.Method, b.Project})
		case rp.Project == b.Project:
			out = append(out, Permission{rp.Method, rp.Project})
		}
	}
	return out
}

// matches reports whether permission method pm covers method full ("pkg.Svc/Name").
func matches(pm, full string) bool {
	if pm == "*" || pm == full {
		return true
	}
	svc, _, _ := strings.Cut(full, "/")
	return pm == svc+"/*"
}

// Allowed reports whether some effective permission of bindings matches method and the call's
// project ("*" in the permission matches every call project; a call project "*" needs a "*"
// permission).
func Allowed(bindings []Binding, full, project string) bool {
	for _, b := range bindings {
		for _, p := range Effective(b) {
			if matches(p.Method, full) && (p.Project == "*" || p.Project == project) {
				return true
			}
		}
	}
	return false
}

// HasAny reports whether the principal has any effective permission on project (a project id
// or "*" permission), whatever the method. It decides PermissionDenied versus NotFound.
func HasAny(bindings []Binding, project string) bool {
	for _, b := range bindings {
		for _, p := range Effective(b) {
			if p.Project == "*" || p.Project == project {
				return true
			}
		}
	}
	return false
}

// Outcome is what a call must answer.
type Outcome int

// The outcomes of the reference evaluator.
const (
	// Allow means authorization passes. The handler may still answer NotFound or
	// InvalidArgument for a missing resource or an empty request; PermissionDenied and
	// Unauthenticated are never acceptable.
	Allow Outcome = iota
	Unauthenticated
	PermissionDenied
	NotFound
	// Refused is for a worker token on a non-worker method or a user on a worker method:
	// any non-OK answer.
	Refused
)

func (o Outcome) String() string {
	return [...]string{"Allow", "Unauthenticated", "PermissionDenied", "NotFound", "Refused"}[o]
}

// Credential classifies the caller for Expect.
type Credential int

// The credential classes of the matrix.
const (
	Anonymous Credential = iota
	User                 // user or service account with an API-scope credential
	Worker               // a bwt1 worker token
)

// Expect computes the outcome of calling m with project (the call's project id) as the
// principal described by cred and bindings. It is SPEC §6.4 evaluated, nothing more.
func Expect(m Method, cred Credential, bindings []Binding, project string) Outcome {
	if m.Kind == KindWorker {
		if cred == Worker {
			return Allow
		}
		return Refused
	}
	switch cred {
	case Worker:
		return Refused
	case Anonymous:
		if m.Kind == KindPublic {
			return Allow
		}
		return Unauthenticated
	}
	switch m.Kind {
	case KindPublic, KindAuthenticated, KindListFiltered:
		return Allow
	case KindGlobal:
		if Allowed(bindings, m.Full(), "*") {
			return Allow
		}
		return PermissionDenied
	case KindProject:
		if Allowed(bindings, m.Full(), project) {
			return Allow
		}
		if HasAny(bindings, project) {
			return PermissionDenied
		}
		return NotFound
	}
	return Refused
}
