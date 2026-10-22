package rbac

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/megakuul/bob/internal/api"
	authzv1 "github.com/megakuul/bob/internal/gen/bob/authz/v1"
)

// Catalog is the permission catalogue: every RPC that a role permission can name, read from
// the methods' bob.authz.v1.rule options (worker methods are not part of RBAC).
type Catalog struct {
	methods  []string            // sorted full method names
	kinds    map[string]string   // method → kind name (PROJECT, GLOBAL, …)
	services map[string][]string // "<pkg>.<Service>" → its methods
}

// NewCatalog builds the catalogue from a rule table (api.AllRules).
func NewCatalog(rules api.Rules) *Catalog {
	c := &Catalog{kinds: map[string]string{}, services: map[string][]string{}}
	for _, r := range rules {
		if r.Kind == authzv1.Kind_KIND_WORKER {
			continue
		}
		c.methods = append(c.methods, r.Method)
		c.kinds[r.Method] = r.KindName()
		svc, _, _ := strings.Cut(r.Method, "/")
		c.services[svc] = append(c.services[svc], r.Method)
	}
	slices.Sort(c.methods)
	for _, ms := range c.services {
		slices.Sort(ms)
	}
	return c
}

// Methods returns every method of the catalogue, sorted.
func (c *Catalog) Methods() []string { return slices.Clone(c.methods) }

// Kind is the rule kind of method ("" if it is not in the catalogue).
func (c *Catalog) Kind(method string) string { return c.kinds[method] }

// Expand returns the catalogue methods a permission method names.
func (c *Catalog) Expand(pm string) []string {
	if pm == Any {
		return c.methods
	}
	if svc, ok := strings.CutSuffix(pm, "/*"); ok {
		return c.services[svc]
	}
	if _, ok := c.kinds[pm]; ok {
		return []string{pm}
	}
	return nil
}

// projectPattern is what a permission or binding may name as a project besides "*": a
// project id (slug) — checked by syntax only, a binding may precede its project.
var projectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// rolePattern is the slug rule of SPEC §6.1.
var rolePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)

// ValidProject reports whether p is "*" or a syntactically valid project id.
func ValidProject(p string) bool { return p == Any || projectPattern.MatchString(p) }

// ValidRoleID reports whether id is a valid role id (slug).
func ValidRoleID(id string) bool { return rolePattern.MatchString(id) }

// MaxPermissions bounds the permissions of a role (SPEC §6.5.5 Role.permissions).
const MaxPermissions = 500

// CheckPermissions validates the permissions of a role: each names "*", a service of the
// catalogue with "/*", or a method of the catalogue, and "*" or a project id. Duplicates are
// dropped; the result is sorted.
func (c *Catalog) CheckPermissions(ps []Permission) ([]Permission, error) {
	if len(ps) > MaxPermissions {
		return nil, api.InvalidArgument("role.permissions", fmt.Sprintf("a role has at most %d permissions", MaxPermissions))
	}
	out := make([]Permission, 0, len(ps))
	for i, p := range ps {
		field := fmt.Sprintf("role.permissions[%d]", i)
		if len(c.Expand(p.Method)) == 0 {
			return nil, api.InvalidArgument(field+".method", fmt.Sprintf("unknown method %q (use \"*\", \"<package>.<Service>/*\" or a method of the permission catalog)", p.Method))
		}
		if !ValidProject(p.Project) {
			return nil, api.InvalidArgument(field+".project", fmt.Sprintf("invalid project %q (a project id or \"*\")", p.Project))
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b Permission) int {
		if a.Method != b.Method {
			return strings.Compare(a.Method, b.Method)
		}
		return strings.Compare(a.Project, b.Project)
	})
	return out, nil
}
