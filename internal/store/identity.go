package store

import "time"

// Identity records of the control bucket (§05.4.2, §05.4.5; fields owned by §06.3). Secret
// values never appear in a record: tokens, refresh tokens, cookies, authorization and device
// codes and OIDC states are keys of the form <pk>/<hash64(value)> (R-AUTH-007), and passwords
// are argon2id PHC strings (R-AUTH-151).

// RoleBindingEntry is one `ro` entry of UserRecord / ServiceAccountRecord (§06's RoleBinding).
type RoleBindingEntry struct {
	ID      string    `cbor:"id,omitempty"`
	Role    string    `cbor:"r,omitempty"`
	Project string    `cbor:"p,omitempty"` // project id or "*"
	Source  string    `cbor:"s,omitempty"` // "manual" | "oidc"
	Created time.Time `cbor:"c,omitzero"`
}

// SessionRef is one `ss` entry of UserRecord: the public id of a session and its key.
type SessionRef struct {
	ID   string `cbor:"id,omitempty"`
	Hash string `cbor:"h,omitempty"` // key value of sess/<hash>
}

// UserRecord is `user/<userID>` (userID = "u" + 26-char id).
type UserRecord struct {
	ID           string             `pk:"user" cbor:"-"`
	ETag         string             `etag:"true" cbor:"-"`
	Login        string             `cbor:"n,omitempty"`  // local login, or the OIDC username claim
	DisplayName  string             `cbor:"dn,omitempty"` // control only (R-SEC-003)
	Email        string             `cbor:"em,omitempty"` // control only (R-SEC-003)
	Source       string             `cbor:"k,omitempty"`  // "local" | "oidc"
	Provider     string             `cbor:"op,omitempty"` // OIDC provider id
	Issuer       string             `cbor:"is,omitempty"` // OIDC issuer (with Subject: oidcsub key)
	Subject      string             `cbor:"sub,omitempty"`
	PasswordHash string             `cbor:"pw,omitempty"` // argon2id PHC string
	Bindings     []RoleBindingEntry `cbor:"ro,omitempty"`
	Sessions     []SessionRef       `cbor:"ss,omitempty"` // ≤ 100
	Disabled     bool               `cbor:"d,omitempty"`
	MustChange   bool               `cbor:"mc,omitempty"`
	Index        uint64             `cbor:"ix,omitempty"` // position in uidx + 1 (0 = not indexed)
	Created      time.Time          `cbor:"c,omitzero"`
	LastLogin    time.Time          `cbor:"ll,omitzero"`
}

// UserKey is the key model of user/<id>.
func UserKey(id string) *UserRecord { return &UserRecord{ID: id} }

// NameRecord maps a unique name to an id: `uname/<enc(login)>` and
// `oidcsub/<hash64(iss‖0‖sub)>` → userID.
type NameRecord struct {
	Key  string `pk:"uname" cbor:"-"`
	ETag string `etag:"true" cbor:"-"`
	ID   string `cbor:"id,omitempty"`
}

// OidcSubRecord is `oidcsub/<hash64(iss‖0‖sub)>` → userID.
type OidcSubRecord struct {
	Key  string `pk:"oidcsub" cbor:"-"`
	ETag string `etag:"true" cbor:"-"`
	ID   string `cbor:"id,omitempty"`
}

// UserNameKey is the key model of uname/<enc(login)>.
func UserNameKey(login string) *NameRecord {
	e, _ := Enc(login)
	return &NameRecord{Key: e}
}

// OidcSubKey is the key model of oidcsub/<hash64(iss‖0‖sub)>.
func OidcSubKey(issuer, subject string) *OidcSubRecord {
	h, _ := Hash64(issuer + "\x00" + subject)
	return &OidcSubRecord{Key: h}
}

// UserIndexMeta is `uidxm/all`: the number of entries of the users paged index.
type UserIndexMeta struct {
	Name  string `pk:"uidxm" cbor:"-"` // always "all"
	ETag  string `etag:"true" cbor:"-"`
	Count uint64 `cbor:"n,omitempty"`
}

// UserIndexMetaKey is the key model of uidxm/all.
func UserIndexMetaKey() *UserIndexMeta { return &UserIndexMeta{Name: "all"} }

// UserIndexEntry is one entry of the users paged index `uidx/all/p/<num10>` (P = 200). A
// deleted user leaves an empty entry.
type UserIndexEntry struct {
	ID       string `cbor:"id,omitempty"`
	Login    string `cbor:"n,omitempty"`
	Source   string `cbor:"k,omitempty"`
	Disabled bool   `cbor:"d,omitempty"`
}

// UserIndexP is the page size of uidx.
const UserIndexP = 200

// UserIndex returns the users paged index.
func UserIndex(control *Bucket) *PagedIndex[UserIndexEntry] {
	return NewPagedIndex[UserIndexEntry](control, "uidx", "all", UserIndexP)
}

// Permission is one role permission.
type Permission struct {
	Method  string `cbor:"m,omitempty"`
	Project string `cbor:"p,omitempty"`
}

// RoleRecord is `role/<roleID>`. The built-in admin role is not stored.
type RoleRecord struct {
	ID          string       `pk:"role" cbor:"-"`
	ETag        string       `etag:"true" cbor:"-"`
	Description string       `cbor:"ds,omitempty"`
	Permissions []Permission `cbor:"pm,omitempty"` // ≤ 2,000 (API: ≤ 500)
	Created     time.Time    `cbor:"c,omitzero"`
}

// RoleKey is the key model of role/<id>.
func RoleKey(id string) *RoleRecord { return &RoleRecord{ID: id} }

// IDList is a single index document holding ids: `roles/all`, `saidx/all`, `oidcps/all`.
type IDList struct {
	Name string   `pk:"roles" cbor:"-"`
	ETag string   `etag:"true" cbor:"-"`
	IDs  []string `cbor:"ids,omitempty"`
}

// SAIndex is `saidx/all` (≤ 1,000 ids).
type SAIndex struct {
	Name string   `pk:"saidx" cbor:"-"`
	ETag string   `etag:"true" cbor:"-"`
	IDs  []string `cbor:"ids,omitempty"`
}

// OidcProviderIndex is `oidcps/all` (≤ 50 ids).
type OidcProviderIndex struct {
	Name string   `pk:"oidcps" cbor:"-"`
	ETag string   `etag:"true" cbor:"-"`
	IDs  []string `cbor:"ids,omitempty"`
}

// RolesIndexKey, SAIndexKey and OidcProviderIndexKey are the key models of the index documents.
func RolesIndexKey() *IDList                   { return &IDList{Name: "all"} }
func SAIndexKey() *SAIndex                     { return &SAIndex{Name: "all"} }
func OidcProviderIndexKey() *OidcProviderIndex { return &OidcProviderIndex{Name: "all"} }

// RoleMembers is `rmem/<roleID>`: the principals with at least one binding of the role.
type RoleMembers struct {
	Role       string   `pk:"rmem" cbor:"-"`
	ETag       string   `etag:"true" cbor:"-"`
	Principals []string `cbor:"m,omitempty"` // "user:u…" | "sa:ci"
}

// RoleMembersKey is the key model of rmem/<role>.
func RoleMembersKey(role string) *RoleMembers { return &RoleMembers{Role: role} }

// AdminIndex is `adminidx/all`: the enabled principals holding an admin@* binding. Its length
// is the admin count of §6.4.1; the set form keeps every update idempotent.
type AdminIndex struct {
	Name       string   `pk:"adminidx" cbor:"-"` // always "all"
	ETag       string   `etag:"true" cbor:"-"`
	Principals []string `cbor:"m,omitempty"`
}

// AdminIndexKey is the key model of adminidx/all.
func AdminIndexKey() *AdminIndex { return &AdminIndex{Name: "all"} }

// SATokenEntry is one `tk` entry of ServiceAccountRecord (no secret, only its hash key).
type SATokenEntry struct {
	ID       string    `cbor:"id,omitempty"`
	Name     string    `cbor:"n,omitempty"`
	Scope    string    `cbor:"sc,omitempty"` // "api" | "download"
	Hash     string    `cbor:"h,omitempty"`  // key value of tok/<hash>
	Expires  time.Time `cbor:"x,omitzero"`   // zero = no expiry
	Created  time.Time `cbor:"c,omitzero"`
	LastUsed time.Time `cbor:"lu,omitzero"` // day precision
}

// ServiceAccountRecord is `sa/<saID>`.
type ServiceAccountRecord struct {
	ID          string             `pk:"sa" cbor:"-"`
	ETag        string             `etag:"true" cbor:"-"`
	Description string             `cbor:"ds,omitempty"`
	Disabled    bool               `cbor:"d,omitempty"`
	Bindings    []RoleBindingEntry `cbor:"ro,omitempty"`
	Tokens      []SATokenEntry     `cbor:"tk,omitempty"` // ≤ 20
	Created     time.Time          `cbor:"c,omitzero"`
}

// ServiceAccountKey is the key model of sa/<id>.
func ServiceAccountKey(id string) *ServiceAccountRecord { return &ServiceAccountRecord{ID: id} }

// TokenRecord is `tok/<hash64(token)>` for bob_at_, bob_dt_ and bob_sa_ tokens.
type TokenRecord struct {
	Hash    string    `pk:"tok" cbor:"-"`
	ETag    string    `etag:"true" cbor:"-"`
	Owner   string    `cbor:"o,omitempty"`  // "user:u…" | "sa:ci"
	Kind    string    `cbor:"k,omitempty"`  // "at" | "dt" | "sa"
	Scope   string    `cbor:"sc,omitempty"` // "api" | "download"
	Session string    `cbor:"se,omitempty"` // key value of the owning sess/<hash> (at, dt)
	TokenID string    `cbor:"id,omitempty"` // SA token id
	Expires time.Time `cbor:"x,omitzero"`   // zero = no expiry
	Created time.Time `cbor:"c,omitzero"`
}

// TokenKey is the key model of tok/<hash64(token)>.
func TokenKey(token string) *TokenRecord {
	h, _ := Hash64(token)
	return &TokenRecord{Hash: h}
}

// TokenRef is an access token issued under a session (hash and expiry), so a refresh can
// delete expired ones and a revoked session can take its tokens with it.
type TokenRef struct {
	Hash    string    `cbor:"h,omitempty"`
	Expires time.Time `cbor:"x,omitzero"`
}

// SessionRecord is `sess/<hash64(token)>` for a CLI refresh token (bob_rt_) or a browser
// session cookie (bob_st_).
type SessionRecord struct {
	Hash      string     `pk:"sess" cbor:"-"`
	ETag      string     `etag:"true" cbor:"-"`
	ID        string     `cbor:"id,omitempty"` // public id (ListMySessions)
	User      string     `cbor:"u,omitempty"`
	Kind      string     `cbor:"k,omitempty"` // "cli" | "browser"
	UserAgent string     `cbor:"ua,omitempty"`
	Created   time.Time  `cbor:"c,omitzero"`
	LastUsed  time.Time  `cbor:"lu,omitzero"`
	IdleUntil time.Time  `cbor:"ie,omitzero"`
	MaxUntil  time.Time  `cbor:"me,omitzero"`
	Download  string     `cbor:"dt,omitempty"` // hash of the session's bob_dt_ token
	Access    []TokenRef `cbor:"at,omitempty"` // live bob_at_ tokens of a CLI session
}

// SessionKey is the key model of sess/<hash64(token)>.
func SessionKey(token string) *SessionRecord {
	h, _ := Hash64(token)
	return &SessionRecord{Hash: h}
}

// OAuthCodeRecord is `oac/<hash64(code)>`: single use (deleted with If-Match), 60 s.
type OAuthCodeRecord struct {
	Hash      string    `pk:"oac" cbor:"-"`
	ETag      string    `etag:"true" cbor:"-"`
	User      string    `cbor:"u,omitempty"`
	Client    string    `cbor:"cl,omitempty"`
	Redirect  string    `cbor:"ru,omitempty"`
	Challenge string    `cbor:"cc,omitempty"` // S256 PKCE challenge
	UserAgent string    `cbor:"ua,omitempty"`
	Expires   time.Time `cbor:"x,omitzero"`
}

// OAuthCodeKey is the key model of oac/<hash64(code)>.
func OAuthCodeKey(code string) *OAuthCodeRecord {
	h, _ := Hash64(code)
	return &OAuthCodeRecord{Hash: h}
}

// DeviceCodeRecord is `devc/<hash64(device_code)>` (RFC 8628), 600 s.
type DeviceCodeRecord struct {
	Hash      string    `pk:"devc" cbor:"-"`
	ETag      string    `etag:"true" cbor:"-"`
	UserCode  string    `cbor:"uc,omitempty"`
	State     string    `cbor:"st,omitempty"` // "pending" | "approved" | "denied" | "used"
	User      string    `cbor:"u,omitempty"`
	Client    string    `cbor:"cl,omitempty"`
	UserAgent string    `cbor:"ua,omitempty"`
	Expires   time.Time `cbor:"x,omitzero"`
	LastPoll  time.Time `cbor:"lp,omitzero"`
	Interval  int       `cbor:"iv,omitempty"` // seconds; raised by slow_down
}

// DeviceCodeKey is the key model of devc/<hash64(device_code)>.
func DeviceCodeKey(deviceCode string) *DeviceCodeRecord {
	h, _ := Hash64(deviceCode)
	return &DeviceCodeRecord{Hash: h}
}

// DeviceUserRecord is `devu/<user_code>` → hash of the device code.
type DeviceUserRecord struct {
	Code    string    `pk:"devu" cbor:"-"` // "ABCD-EFGH"
	ETag    string    `etag:"true" cbor:"-"`
	Hash    string    `cbor:"h,omitempty"`
	Expires time.Time `cbor:"x,omitzero"`
}

// DeviceUserKey is the key model of devu/<user_code>.
func DeviceUserKey(userCode string) *DeviceUserRecord { return &DeviceUserRecord{Code: userCode} }

// OidcStateRecord is `oidcst/<hash64(state)>`: one OIDC login in flight, 10 min.
type OidcStateRecord struct {
	Hash     string    `pk:"oidcst" cbor:"-"`
	ETag     string    `etag:"true" cbor:"-"`
	Provider string    `cbor:"p,omitempty"`
	Nonce    string    `cbor:"no,omitempty"`
	Verifier string    `cbor:"v,omitempty"` // PKCE verifier towards the provider
	Next     string    `cbor:"nx,omitempty"`
	Redirect string    `cbor:"ru,omitempty"` // our callback URL used in the request
	Expires  time.Time `cbor:"x,omitzero"`
}

// OidcStateKey is the key model of oidcst/<hash64(state)>.
func OidcStateKey(state string) *OidcStateRecord {
	h, _ := Hash64(state)
	return &OidcStateRecord{Hash: h}
}

// OidcProviderRecord is `oidcp/<providerID>`.
type OidcProviderRecord struct {
	ID             string    `pk:"oidcp" cbor:"-"`
	ETag           string    `etag:"true" cbor:"-"`
	DisplayName    string    `cbor:"dn,omitempty"`
	Issuer         string    `cbor:"is,omitempty"`
	ClientID       string    `cbor:"ci,omitempty"`
	ClientSecret   string    `cbor:"cs,omitempty"`
	Scopes         []string  `cbor:"sc,omitempty"`
	UsernameClaim  string    `cbor:"uc,omitempty"`
	RequireMapping bool      `cbor:"rm,omitempty"`
	Created        time.Time `cbor:"c,omitzero"`
}

// OidcProviderKey is the key model of oidcp/<id>.
func OidcProviderKey(id string) *OidcProviderRecord { return &OidcProviderRecord{ID: id} }

// OidcMappingEntry is one claim → role mapping.
type OidcMappingEntry struct {
	ID       string `cbor:"id,omitempty"`
	Provider string `cbor:"pv,omitempty"`
	Claim    string `cbor:"cl,omitempty"`
	Value    string `cbor:"v,omitempty"`
	Role     string `cbor:"r,omitempty"`
	Project  string `cbor:"p,omitempty"`
}

// OidcMappings is `oidcmap/all` (≤ 500 mappings per provider).
type OidcMappings struct {
	Name     string             `pk:"oidcmap" cbor:"-"` // always "all"
	ETag     string             `etag:"true" cbor:"-"`
	Mappings []OidcMappingEntry `cbor:"m,omitempty"`
}

// OidcMappingsKey is the key model of oidcmap/all.
func OidcMappingsKey() *OidcMappings { return &OidcMappings{Name: "all"} }
