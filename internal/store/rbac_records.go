package store

import "time"

// RBAC records of SPEC §05.4.2/§05.4.5 (§06.4). Bindings themselves are the `ro` entries of
// UserRecord and ServiceAccountRecord (the truth); RoleMembers is the derived per-role list.

// RoleBinding is one `ro` entry of a UserRecord or ServiceAccountRecord: a role bound on a
// project ("*" = every project). ID is the binding id the API shows (rbac.BindingID).
type RoleBinding struct {
	ID      string    `cbor:"id,omitempty"`
	Role    string    `cbor:"r,omitempty"`
	Project string    `cbor:"p,omitempty"`
	Source  string    `cbor:"s,omitempty"` // manual | oidc
	Created time.Time `cbor:"cr,omitzero"`
}

// Binding sources.
const (
	BindingManual = "manual"
	BindingOIDC   = "oidc"
)

// RolePermission is one permission of a role: method "<pkg>.<Service>/<Method>",
// "<pkg>.<Service>/*" or "*"; project id or "*".
type RolePermission struct {
	Method  string `cbor:"m,omitempty"`
	Project string `cbor:"p,omitempty"`
}

// RoleRecord is `role/<roleID>`. The built-in role admin has no record (it is immutable).
type RoleRecord struct {
	ID          string           `pk:"role" cbor:"-"`
	ETag        string           `etag:"true" cbor:"-"`
	Description string           `cbor:"d,omitempty"`
	Permissions []RolePermission `cbor:"ps,omitempty"` // ≤ 2,000 (API: ≤ 500)
	Created     time.Time        `cbor:"cr,omitzero"`
}

// RoleIndex is `roles/all`: the ids of every stored role (≤ 1,000), sorted.
type RoleIndex struct {
	Name  string   `pk:"roles" cbor:"-"` // "all"
	ETag  string   `etag:"true" cbor:"-"`
	Roles []string `cbor:"r,omitempty"`
}

// RoleIndexKey is the key model of `roles/all`.
func RoleIndexKey() *RoleIndex { return &RoleIndex{Name: "all"} }

// RoleMember is one binding of a role in RoleMembers. Pending is set while the binding is
// being written to its principal (it is confirmed or removed right after).
type RoleMember struct {
	Principal string    `cbor:"pr,omitempty"`
	Project   string    `cbor:"p,omitempty"`
	Source    string    `cbor:"s,omitempty"`
	Created   time.Time `cbor:"cr,omitzero"`
	Pending   bool      `cbor:"pe,omitempty"`
}

// RoleMembers is `rmem/<roleID>`: binding id → member. Closed is set by DeleteRole on an
// unbound role: no binding can be added until the role is created again.
type RoleMembers struct {
	Role    string                `pk:"rmem" cbor:"-"`
	ETag    string                `etag:"true" cbor:"-"`
	Members map[string]RoleMember `cbor:"m,omitempty"`
	Closed  bool                  `cbor:"c,omitempty"`
}

// AdminIndex is `adminidx/all`: the enabled principals holding admin@*. Its size is the count
// of SPEC §6.4.1; a set (instead of a bare number) keeps every change idempotent.
type AdminIndex struct {
	Name       string   `pk:"adminidx" cbor:"-"` // "all"
	ETag       string   `etag:"true" cbor:"-"`
	Principals []string `cbor:"p,omitempty"` // sorted
}

// AdminIndexKey is the key model of `adminidx/all`.
func AdminIndexKey() *AdminIndex { return &AdminIndex{Name: "all"} }

// OidcMapping is one OIDC claim → role mapping.
type OidcMapping struct {
	ID       string `cbor:"id,omitempty"`
	Provider string `cbor:"pv,omitempty"`
	Claim    string `cbor:"c,omitempty"`
	Value    string `cbor:"v,omitempty"`
	Role     string `cbor:"r,omitempty"`
	Project  string `cbor:"p,omitempty"`
}

// OidcMappings is `oidcmap/all`: every mapping (≤ 500 per provider).
type OidcMappings struct {
	Name     string        `pk:"oidcmap" cbor:"-"` // "all"
	ETag     string        `etag:"true" cbor:"-"`
	Mappings []OidcMapping `cbor:"m,omitempty"`
}

// OidcMappingsKey is the key model of `oidcmap/all`.
func OidcMappingsKey() *OidcMappings { return &OidcMappings{Name: "all"} }
