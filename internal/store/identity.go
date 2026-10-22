package store

import (
	"time"

	"github.com/megakuul/dynamitedb"
)

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
	ID           dynamitedb.KeyField                      `pk:"user" cbor:"-"`
	ETag         dynamitedb.ETagField                     `etag:"true" cbor:"-"`
	Login        dynamitedb.DataField[string]             `cbor:"n,omitempty"`  // local login, or the OIDC username claim
	DisplayName  dynamitedb.DataField[string]             `cbor:"dn,omitempty"` // control only (R-SEC-003)
	Email        dynamitedb.DataField[string]             `cbor:"em,omitempty"` // control only (R-SEC-003)
	Source       dynamitedb.DataField[string]             `cbor:"k,omitempty"`  // "local" | "oidc"
	Provider     dynamitedb.DataField[string]             `cbor:"op,omitempty"` // OIDC provider id
	Issuer       dynamitedb.DataField[string]             `cbor:"is,omitempty"` // OIDC issuer (with Subject: oidcsub key)
	Subject      dynamitedb.DataField[string]             `cbor:"sub,omitempty"`
	PasswordHash dynamitedb.DataField[string]             `cbor:"pw,omitempty"` // argon2id PHC string
	Bindings     dynamitedb.DataField[[]RoleBindingEntry] `cbor:"ro,omitempty"`
	Sessions     dynamitedb.DataField[[]SessionRef]       `cbor:"ss,omitempty"` // ≤ 100
	Disabled     dynamitedb.DataField[bool]               `cbor:"d,omitempty"`
	MustChange   dynamitedb.DataField[bool]               `cbor:"mc,omitempty"`
	Index        dynamitedb.DataField[uint64]             `cbor:"ix,omitempty"` // position in uidx + 1 (0 = not indexed)
	Created      dynamitedb.DataField[time.Time]          `cbor:"c,omitempty"`
	LastLogin    dynamitedb.DataField[time.Time]          `cbor:"ll,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *UserRecord) Draft() *UserRecord {
	return &UserRecord{ID: r.ID,
		ETag:         r.ETag,
		Login:        Carry(r.Login),
		DisplayName:  Carry(r.DisplayName),
		Email:        Carry(r.Email),
		Source:       Carry(r.Source),
		Provider:     Carry(r.Provider),
		Issuer:       Carry(r.Issuer),
		Subject:      Carry(r.Subject),
		PasswordHash: Carry(r.PasswordHash),
		Bindings:     Carry(r.Bindings),
		Sessions:     Carry(r.Sessions),
		Disabled:     Carry(r.Disabled),
		MustChange:   Carry(r.MustChange),
		Index:        Carry(r.Index),
		Created:      Carry(r.Created),
		LastLogin:    Carry(r.LastLogin)}
}

// UserKey is the key model of user/<id>.
func UserKey(id string) *UserRecord { return &UserRecord{ID: dynamitedb.Key(id)} }

// NameRecord maps a unique name to an id: `uname/<enc(login)>` and
// `oidcsub/<hash64(iss‖0‖sub)>` → userID.
type NameRecord struct {
	Key  dynamitedb.KeyField          `pk:"uname" cbor:"-"`
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	ID   dynamitedb.DataField[string] `cbor:"id,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *NameRecord) Draft() *NameRecord {
	return &NameRecord{Key: r.Key,
		ETag: r.ETag,
		ID:   Carry(r.ID)}
}

// OidcSubRecord is `oidcsub/<hash64(iss‖0‖sub)>` → userID.
type OidcSubRecord struct {
	Key  dynamitedb.KeyField          `pk:"oidcsub" cbor:"-"`
	ETag dynamitedb.ETagField         `etag:"true" cbor:"-"`
	ID   dynamitedb.DataField[string] `cbor:"id,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *OidcSubRecord) Draft() *OidcSubRecord {
	return &OidcSubRecord{Key: r.Key,
		ETag: r.ETag,
		ID:   Carry(r.ID)}
}

// UserNameKey is the key model of uname/<enc(login)>.
func UserNameKey(login string) *NameRecord {
	e, _ := Enc(login)
	return &NameRecord{Key: dynamitedb.Key(e)}
}

// OidcSubKey is the key model of oidcsub/<hash64(iss‖0‖sub)>.
func OidcSubKey(issuer, subject string) *OidcSubRecord {
	h, _ := Hash64(issuer + "\x00" + subject)
	return &OidcSubRecord{Key: dynamitedb.Key(h)}
}

// UserIndexMeta is `uidxm/all`: the number of entries of the users paged index.
type UserIndexMeta struct {
	Name  dynamitedb.KeyField          `pk:"uidxm" cbor:"-"` // always "all"
	ETag  dynamitedb.ETagField         `etag:"true" cbor:"-"`
	Count dynamitedb.DataField[uint64] `cbor:"n,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *UserIndexMeta) Draft() *UserIndexMeta {
	return &UserIndexMeta{Name: r.Name,
		ETag:  r.ETag,
		Count: Carry(r.Count)}
}

// UserIndexMetaKey is the key model of uidxm/all.
func UserIndexMetaKey() *UserIndexMeta { return &UserIndexMeta{Name: dynamitedb.Key("all")} }

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
	return NewPagedIndex(control, UserIndexP, func(k uint64, e []UserIndexEntry) *UserIndexPage {
		n, _ := Num10(k)
		return &UserIndexPage{Name: dynamitedb.Key("all"), Page: dynamitedb.Key(n), Entries: setNZ(e)}
	}, func(p *UserIndexPage) []UserIndexEntry { return p.Entries.Value() })
}

// UserIndexPage is one page `uidx/all/p/<num10>` of the users index.
type UserIndexPage struct {
	Name    dynamitedb.KeyField                    `pk:"uidx" cbor:"-"`
	Page    dynamitedb.KeyField                    `sk:"p" cbor:"-"`
	ETag    dynamitedb.ETagField                   `etag:"true" cbor:"-"`
	Entries dynamitedb.DataField[[]UserIndexEntry] `cbor:"e,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *UserIndexPage) Draft() *UserIndexPage {
	return &UserIndexPage{Name: r.Name, Page: r.Page, ETag: r.ETag, Entries: Carry(r.Entries)}
}

// Permission is one role permission.
type Permission struct {
	Method  string `cbor:"m,omitempty"`
	Project string `cbor:"p,omitempty"`
}

// RoleRecord is `role/<roleID>`. The built-in admin role is not stored.
type RoleRecord struct {
	ID          dynamitedb.KeyField                `pk:"role" cbor:"-"`
	ETag        dynamitedb.ETagField               `etag:"true" cbor:"-"`
	Description dynamitedb.DataField[string]       `cbor:"ds,omitempty"`
	Permissions dynamitedb.DataField[[]Permission] `cbor:"pm,omitempty"` // ≤ 2,000 (API: ≤ 500)
	Created     dynamitedb.DataField[time.Time]    `cbor:"c,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *RoleRecord) Draft() *RoleRecord {
	return &RoleRecord{ID: r.ID,
		ETag:        r.ETag,
		Description: Carry(r.Description),
		Permissions: Carry(r.Permissions),
		Created:     Carry(r.Created)}
}

// RoleKey is the key model of role/<id>.
func RoleKey(id string) *RoleRecord { return &RoleRecord{ID: dynamitedb.Key(id)} }

// IDList is a single index document holding ids: `roles/all`, `saidx/all`, `oidcps/all`.
type IDList struct {
	Name dynamitedb.KeyField            `pk:"roles" cbor:"-"`
	ETag dynamitedb.ETagField           `etag:"true" cbor:"-"`
	IDs  dynamitedb.DataField[[]string] `cbor:"ids,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *IDList) Draft() *IDList {
	return &IDList{Name: r.Name,
		ETag: r.ETag,
		IDs:  Carry(r.IDs)}
}

// SAIndex is `saidx/all` (≤ 1,000 ids).
type SAIndex struct {
	Name dynamitedb.KeyField            `pk:"saidx" cbor:"-"`
	ETag dynamitedb.ETagField           `etag:"true" cbor:"-"`
	IDs  dynamitedb.DataField[[]string] `cbor:"ids,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *SAIndex) Draft() *SAIndex {
	return &SAIndex{Name: r.Name,
		ETag: r.ETag,
		IDs:  Carry(r.IDs)}
}

// OidcProviderIndex is `oidcps/all` (≤ 50 ids).
type OidcProviderIndex struct {
	Name dynamitedb.KeyField            `pk:"oidcps" cbor:"-"`
	ETag dynamitedb.ETagField           `etag:"true" cbor:"-"`
	IDs  dynamitedb.DataField[[]string] `cbor:"ids,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *OidcProviderIndex) Draft() *OidcProviderIndex {
	return &OidcProviderIndex{Name: r.Name,
		ETag: r.ETag,
		IDs:  Carry(r.IDs)}
}

// RolesIndexKey, SAIndexKey and OidcProviderIndexKey are the key models of the index documents.
func RolesIndexKey() *IDList { return &IDList{Name: dynamitedb.Key("all")} }
func SAIndexKey() *SAIndex   { return &SAIndex{Name: dynamitedb.Key("all")} }
func OidcProviderIndexKey() *OidcProviderIndex {
	return &OidcProviderIndex{Name: dynamitedb.Key("all")}
}

// RoleMembers is `rmem/<roleID>`: the principals with at least one binding of the role.
type RoleMembers struct {
	Role       dynamitedb.KeyField            `pk:"rmem" cbor:"-"`
	ETag       dynamitedb.ETagField           `etag:"true" cbor:"-"`
	Principals dynamitedb.DataField[[]string] `cbor:"m,omitempty"` // "user:u…" | "sa:ci"
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *RoleMembers) Draft() *RoleMembers {
	return &RoleMembers{Role: r.Role,
		ETag:       r.ETag,
		Principals: Carry(r.Principals)}
}

// RoleMembersKey is the key model of rmem/<role>.
func RoleMembersKey(role string) *RoleMembers { return &RoleMembers{Role: dynamitedb.Key(role)} }

// AdminIndex is `adminidx/all`: the enabled principals holding an admin@* binding. Its length
// is the admin count of §6.4.1; the set form keeps every update idempotent.
type AdminIndex struct {
	Name       dynamitedb.KeyField            `pk:"adminidx" cbor:"-"` // always "all"
	ETag       dynamitedb.ETagField           `etag:"true" cbor:"-"`
	Principals dynamitedb.DataField[[]string] `cbor:"m,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *AdminIndex) Draft() *AdminIndex {
	return &AdminIndex{Name: r.Name,
		ETag:       r.ETag,
		Principals: Carry(r.Principals)}
}

// AdminIndexKey is the key model of adminidx/all.
func AdminIndexKey() *AdminIndex { return &AdminIndex{Name: dynamitedb.Key("all")} }

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
	ID          dynamitedb.KeyField                      `pk:"sa" cbor:"-"`
	ETag        dynamitedb.ETagField                     `etag:"true" cbor:"-"`
	Description dynamitedb.DataField[string]             `cbor:"ds,omitempty"`
	Disabled    dynamitedb.DataField[bool]               `cbor:"d,omitempty"`
	Bindings    dynamitedb.DataField[[]RoleBindingEntry] `cbor:"ro,omitempty"`
	Tokens      dynamitedb.DataField[[]SATokenEntry]     `cbor:"tk,omitempty"` // ≤ 20
	Created     dynamitedb.DataField[time.Time]          `cbor:"c,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ServiceAccountRecord) Draft() *ServiceAccountRecord {
	return &ServiceAccountRecord{ID: r.ID,
		ETag:        r.ETag,
		Description: Carry(r.Description),
		Disabled:    Carry(r.Disabled),
		Bindings:    Carry(r.Bindings),
		Tokens:      Carry(r.Tokens),
		Created:     Carry(r.Created)}
}

// ServiceAccountKey is the key model of sa/<id>.
func ServiceAccountKey(id string) *ServiceAccountRecord {
	return &ServiceAccountRecord{ID: dynamitedb.Key(id)}
}

// TokenRecord is `tok/<hash64(token)>` for bob_at_, bob_dt_ and bob_sa_ tokens.
type TokenRecord struct {
	Hash    dynamitedb.KeyField             `pk:"tok" cbor:"-"`
	ETag    dynamitedb.ETagField            `etag:"true" cbor:"-"`
	Owner   dynamitedb.DataField[string]    `cbor:"o,omitempty"`  // "user:u…" | "sa:ci"
	Kind    dynamitedb.DataField[string]    `cbor:"k,omitempty"`  // "at" | "dt" | "sa"
	Scope   dynamitedb.DataField[string]    `cbor:"sc,omitempty"` // "api" | "download"
	Session dynamitedb.DataField[string]    `cbor:"se,omitempty"` // key value of the owning sess/<hash> (at, dt)
	TokenID dynamitedb.DataField[string]    `cbor:"id,omitempty"` // SA token id
	Expires dynamitedb.DataField[time.Time] `cbor:"x,omitempty"`  // zero = no expiry
	Created dynamitedb.DataField[time.Time] `cbor:"c,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *TokenRecord) Draft() *TokenRecord {
	return &TokenRecord{Hash: r.Hash,
		ETag:    r.ETag,
		Owner:   Carry(r.Owner),
		Kind:    Carry(r.Kind),
		Scope:   Carry(r.Scope),
		Session: Carry(r.Session),
		TokenID: Carry(r.TokenID),
		Expires: Carry(r.Expires),
		Created: Carry(r.Created)}
}

// TokenKey is the key model of tok/<hash64(token)>.
func TokenKey(token string) *TokenRecord {
	h, _ := Hash64(token)
	return &TokenRecord{Hash: dynamitedb.Key(h)}
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
	Hash      dynamitedb.KeyField              `pk:"sess" cbor:"-"`
	ETag      dynamitedb.ETagField             `etag:"true" cbor:"-"`
	ID        dynamitedb.DataField[string]     `cbor:"id,omitempty"` // public id (ListMySessions)
	User      dynamitedb.DataField[string]     `cbor:"u,omitempty"`
	Kind      dynamitedb.DataField[string]     `cbor:"k,omitempty"` // "cli" | "browser"
	UserAgent dynamitedb.DataField[string]     `cbor:"ua,omitempty"`
	Created   dynamitedb.DataField[time.Time]  `cbor:"c,omitempty"`
	LastUsed  dynamitedb.DataField[time.Time]  `cbor:"lu,omitempty"`
	IdleUntil dynamitedb.DataField[time.Time]  `cbor:"ie,omitempty"`
	MaxUntil  dynamitedb.DataField[time.Time]  `cbor:"me,omitempty"`
	Download  dynamitedb.DataField[string]     `cbor:"dt,omitempty"` // hash of the session's bob_dt_ token
	Access    dynamitedb.DataField[[]TokenRef] `cbor:"at,omitempty"` // live bob_at_ tokens of a CLI session
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *SessionRecord) Draft() *SessionRecord {
	return &SessionRecord{Hash: r.Hash,
		ETag:      r.ETag,
		ID:        Carry(r.ID),
		User:      Carry(r.User),
		Kind:      Carry(r.Kind),
		UserAgent: Carry(r.UserAgent),
		Created:   Carry(r.Created),
		LastUsed:  Carry(r.LastUsed),
		IdleUntil: Carry(r.IdleUntil),
		MaxUntil:  Carry(r.MaxUntil),
		Download:  Carry(r.Download),
		Access:    Carry(r.Access)}
}

// SessionKey is the key model of sess/<hash64(token)>.
func SessionKey(token string) *SessionRecord {
	h, _ := Hash64(token)
	return &SessionRecord{Hash: dynamitedb.Key(h)}
}

// OAuthCodeRecord is `oac/<hash64(code)>`: single use (deleted with If-Match), 60 s.
type OAuthCodeRecord struct {
	Hash      dynamitedb.KeyField             `pk:"oac" cbor:"-"`
	ETag      dynamitedb.ETagField            `etag:"true" cbor:"-"`
	User      dynamitedb.DataField[string]    `cbor:"u,omitempty"`
	Client    dynamitedb.DataField[string]    `cbor:"cl,omitempty"`
	Redirect  dynamitedb.DataField[string]    `cbor:"ru,omitempty"`
	Challenge dynamitedb.DataField[string]    `cbor:"cc,omitempty"` // S256 PKCE challenge
	UserAgent dynamitedb.DataField[string]    `cbor:"ua,omitempty"`
	Expires   dynamitedb.DataField[time.Time] `cbor:"x,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *OAuthCodeRecord) Draft() *OAuthCodeRecord {
	return &OAuthCodeRecord{Hash: r.Hash,
		ETag:      r.ETag,
		User:      Carry(r.User),
		Client:    Carry(r.Client),
		Redirect:  Carry(r.Redirect),
		Challenge: Carry(r.Challenge),
		UserAgent: Carry(r.UserAgent),
		Expires:   Carry(r.Expires)}
}

// OAuthCodeKey is the key model of oac/<hash64(code)>.
func OAuthCodeKey(code string) *OAuthCodeRecord {
	h, _ := Hash64(code)
	return &OAuthCodeRecord{Hash: dynamitedb.Key(h)}
}

// DeviceCodeRecord is `devc/<hash64(device_code)>` (RFC 8628), 600 s.
type DeviceCodeRecord struct {
	Hash      dynamitedb.KeyField             `pk:"devc" cbor:"-"`
	ETag      dynamitedb.ETagField            `etag:"true" cbor:"-"`
	UserCode  dynamitedb.DataField[string]    `cbor:"uc,omitempty"`
	State     dynamitedb.DataField[string]    `cbor:"st,omitempty"` // "pending" | "approved" | "denied" | "used"
	User      dynamitedb.DataField[string]    `cbor:"u,omitempty"`
	Client    dynamitedb.DataField[string]    `cbor:"cl,omitempty"`
	UserAgent dynamitedb.DataField[string]    `cbor:"ua,omitempty"`
	Expires   dynamitedb.DataField[time.Time] `cbor:"x,omitempty"`
	LastPoll  dynamitedb.DataField[time.Time] `cbor:"lp,omitempty"`
	Interval  dynamitedb.DataField[int]       `cbor:"iv,omitempty"` // seconds; raised by slow_down
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *DeviceCodeRecord) Draft() *DeviceCodeRecord {
	return &DeviceCodeRecord{Hash: r.Hash,
		ETag:      r.ETag,
		UserCode:  Carry(r.UserCode),
		State:     Carry(r.State),
		User:      Carry(r.User),
		Client:    Carry(r.Client),
		UserAgent: Carry(r.UserAgent),
		Expires:   Carry(r.Expires),
		LastPoll:  Carry(r.LastPoll),
		Interval:  Carry(r.Interval)}
}

// DeviceCodeKey is the key model of devc/<hash64(device_code)>.
func DeviceCodeKey(deviceCode string) *DeviceCodeRecord {
	h, _ := Hash64(deviceCode)
	return &DeviceCodeRecord{Hash: dynamitedb.Key(h)}
}

// DeviceUserRecord is `devu/<user_code>` → hash of the device code.
type DeviceUserRecord struct {
	Code    dynamitedb.KeyField             `pk:"devu" cbor:"-"` // "ABCD-EFGH"
	ETag    dynamitedb.ETagField            `etag:"true" cbor:"-"`
	Hash    dynamitedb.DataField[string]    `cbor:"h,omitempty"`
	Expires dynamitedb.DataField[time.Time] `cbor:"x,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *DeviceUserRecord) Draft() *DeviceUserRecord {
	return &DeviceUserRecord{Code: r.Code,
		ETag:    r.ETag,
		Hash:    Carry(r.Hash),
		Expires: Carry(r.Expires)}
}

// DeviceUserKey is the key model of devu/<user_code>.
func DeviceUserKey(userCode string) *DeviceUserRecord {
	return &DeviceUserRecord{Code: dynamitedb.Key(userCode)}
}

// OidcStateRecord is `oidcst/<hash64(state)>`: one OIDC login in flight, 10 min.
type OidcStateRecord struct {
	Hash     dynamitedb.KeyField             `pk:"oidcst" cbor:"-"`
	ETag     dynamitedb.ETagField            `etag:"true" cbor:"-"`
	Provider dynamitedb.DataField[string]    `cbor:"p,omitempty"`
	Nonce    dynamitedb.DataField[string]    `cbor:"no,omitempty"`
	Verifier dynamitedb.DataField[string]    `cbor:"v,omitempty"` // PKCE verifier towards the provider
	Next     dynamitedb.DataField[string]    `cbor:"nx,omitempty"`
	Redirect dynamitedb.DataField[string]    `cbor:"ru,omitempty"` // our callback URL used in the request
	Expires  dynamitedb.DataField[time.Time] `cbor:"x,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *OidcStateRecord) Draft() *OidcStateRecord {
	return &OidcStateRecord{Hash: r.Hash,
		ETag:     r.ETag,
		Provider: Carry(r.Provider),
		Nonce:    Carry(r.Nonce),
		Verifier: Carry(r.Verifier),
		Next:     Carry(r.Next),
		Redirect: Carry(r.Redirect),
		Expires:  Carry(r.Expires)}
}

// OidcStateKey is the key model of oidcst/<hash64(state)>.
func OidcStateKey(state string) *OidcStateRecord {
	h, _ := Hash64(state)
	return &OidcStateRecord{Hash: dynamitedb.Key(h)}
}

// OidcProviderRecord is `oidcp/<providerID>`.
type OidcProviderRecord struct {
	ID             dynamitedb.KeyField             `pk:"oidcp" cbor:"-"`
	ETag           dynamitedb.ETagField            `etag:"true" cbor:"-"`
	DisplayName    dynamitedb.DataField[string]    `cbor:"dn,omitempty"`
	Issuer         dynamitedb.DataField[string]    `cbor:"is,omitempty"`
	ClientID       dynamitedb.DataField[string]    `cbor:"ci,omitempty"`
	ClientSecret   dynamitedb.DataField[string]    `cbor:"cs,omitempty"`
	Scopes         dynamitedb.DataField[[]string]  `cbor:"sc,omitempty"`
	UsernameClaim  dynamitedb.DataField[string]    `cbor:"uc,omitempty"`
	RequireMapping dynamitedb.DataField[bool]      `cbor:"rm,omitempty"`
	Created        dynamitedb.DataField[time.Time] `cbor:"c,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *OidcProviderRecord) Draft() *OidcProviderRecord {
	return &OidcProviderRecord{ID: r.ID,
		ETag:           r.ETag,
		DisplayName:    Carry(r.DisplayName),
		Issuer:         Carry(r.Issuer),
		ClientID:       Carry(r.ClientID),
		ClientSecret:   Carry(r.ClientSecret),
		Scopes:         Carry(r.Scopes),
		UsernameClaim:  Carry(r.UsernameClaim),
		RequireMapping: Carry(r.RequireMapping),
		Created:        Carry(r.Created)}
}

// OidcProviderKey is the key model of oidcp/<id>.
func OidcProviderKey(id string) *OidcProviderRecord {
	return &OidcProviderRecord{ID: dynamitedb.Key(id)}
}

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
	Name     dynamitedb.KeyField                      `pk:"oidcmap" cbor:"-"` // always "all"
	ETag     dynamitedb.ETagField                     `etag:"true" cbor:"-"`
	Mappings dynamitedb.DataField[[]OidcMappingEntry] `cbor:"m,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *OidcMappings) Draft() *OidcMappings {
	return &OidcMappings{Name: r.Name,
		ETag:     r.ETag,
		Mappings: Carry(r.Mappings)}
}

// OidcMappingsKey is the key model of oidcmap/all.
func OidcMappingsKey() *OidcMappings { return &OidcMappings{Name: dynamitedb.Key("all")} }
