package rbac

import (
	"context"
	"encoding/base32"
	"errors"
	"regexp"
	"strings"

	"github.com/megakuul/bob/internal/store"
)

// Principals is the identity side's store of principals (UserRecord, ServiceAccountRecord;
// identity, M15.2). The role bindings of a principal are the `ro` entries of its record: RBAC
// changes them only through UpdateBindings, which must be a CAS on that record.
type Principals interface {
	// Bindings returns the bindings of principal ("user:<id>" | "sa:<id>") and whether it is
	// enabled. A principal that does not exist is ErrNoPrincipal.
	Bindings(ctx context.Context, principal string) (bindings []store.RoleBinding, enabled bool, err error)
	// UpdateBindings replaces the bindings of principal with fn's result under CAS on its
	// record (fn may run more than once). fn returning store.ErrNoChange ends without a write
	// and UpdateBindings returns nil; any other error of fn is returned as is. A principal
	// that does not exist is ErrNoPrincipal.
	UpdateBindings(ctx context.Context, principal string, fn func(cur []store.RoleBinding, enabled bool) ([]store.RoleBinding, error)) error
}

// ErrNoPrincipal is returned by Principals for a principal that does not exist.
var ErrNoPrincipal = errors.New("principal not found")

// principalPattern is "user:u<26-char id>" or "sa:<slug>" (SPEC §6.1).
var principalPattern = regexp.MustCompile(`^(user:u[0-9a-z]{26}|sa:[a-z][a-z0-9-]{1,38}[a-z0-9])$`)

// ValidPrincipal reports whether id is a syntactically valid principal id.
func ValidPrincipal(id string) bool { return principalPattern.MatchString(id) }

// bindingIDs encodes binding ids: lower-case base32 without padding.
var bindingIDs = base32.StdEncoding.WithPadding(base32.NoPadding)

// BindingID is the id of the binding of role on project to principal. It is derived from the
// three values, so a principal holds a role on a project at most once (whatever the source)
// and DeleteRoleBinding finds the principal without an index.
func BindingID(principal, role, project string) string {
	return "b" + strings.ToLower(bindingIDs.EncodeToString([]byte(principal+"\x00"+role+"\x00"+project)))
}

// ParseBindingID returns the principal, role and project of a binding id.
func ParseBindingID(id string) (principal, role, project string, ok bool) {
	rest, found := strings.CutPrefix(id, "b")
	if !found || len(id) > 400 {
		return "", "", "", false
	}
	raw, err := bindingIDs.DecodeString(strings.ToUpper(rest))
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 || !ValidPrincipal(parts[0]) || !(parts[1] == Admin || ValidRoleID(parts[1])) || !ValidProject(parts[2]) {
		return "", "", "", false
	}
	if BindingID(parts[0], parts[1], parts[2]) != id { // canonical spelling only
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
