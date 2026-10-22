// Package integration runs bob's forge integrations on an orchestrator (SPEC §6.9): the
// integration records and their adapters, the webhook intake (POST /hooks/<id>, §6.9.3),
// ForgeService, repository links (§05.4.5) and the forge side of checkout credentials (§6.9.6).
package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/megakuul/bob/internal/store"
)

// ForgeRecord is forge/<forgeID>, one forge integration. Credentials and webhook secrets live
// only in the control bucket, which workers cannot read (R-SEC-102). SPEC wants them sealed with
// an orchestrator data key that is not defined yet (D-IB4-6); they are stored as given until it
// exists.
type ForgeRecord struct {
	ID   store.KeyField  `pk:"forge" cbor:"-"`
	ETag store.ETagField `etag:"true" cbor:"-"`

	Kind        store.DataField[string]    `cbor:"k,omitempty"` // github | forgejo | gitlab
	DisplayName store.DataField[string]    `cbor:"n,omitempty"`
	BaseURL     store.DataField[string]    `cbor:"u,omitempty"`
	Created     store.DataField[time.Time] `cbor:"c,omitempty"`

	AppID          store.DataField[int64]  `cbor:"ga,omitempty"` // github
	InstallationID store.DataField[int64]  `cbor:"gi,omitempty"`
	PrivateKeyPEM  store.DataField[string] `cbor:"gk,omitempty"`
	BotLogin       store.DataField[string] `cbor:"fl,omitempty"` // forgejo (D62)
	BotPassword    store.DataField[string] `cbor:"fp,omitempty"`
	AccessToken    store.DataField[string] `cbor:"lt,omitempty"` // gitlab
	AllowPlaintext store.DataField[bool]   `cbor:"lp,omitempty"`

	// Secret verifies deliveries; PrevSecret is still accepted until PrevUntil (24 h after a
	// rotation, §6.9.3).
	Secret     store.DataField[string]    `cbor:"s,omitempty"`
	PrevSecret store.DataField[string]    `cbor:"ps,omitempty"`
	PrevUntil  store.DataField[time.Time] `cbor:"pu,omitempty"`

	Health    store.DataField[int32]  `cbor:"h,omitempty"` // forgev1.Health
	HealthMsg store.DataField[string] `cbor:"hm,omitempty"`

	// Repos are the linked repositories: forge repository id → project id. It is the index of
	// the repo/ links of this integration (DeleteForgeIntegration is refused while it is not
	// empty; RotateWebhookSecret re-ensures their hooks). Written before the link record and
	// removed after it (R-SRV-101), so it may name a link that a crash left unwritten.
	Repos store.DataField[map[string]string] `cbor:"r,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ForgeRecord) Draft() *ForgeRecord {
	return &ForgeRecord{ID: r.ID,
		ETag:           r.ETag,
		Kind:           store.Carry(r.Kind),
		DisplayName:    store.Carry(r.DisplayName),
		BaseURL:        store.Carry(r.BaseURL),
		Created:        store.Carry(r.Created),
		AppID:          store.Carry(r.AppID),
		InstallationID: store.Carry(r.InstallationID),
		PrivateKeyPEM:  store.Carry(r.PrivateKeyPEM),
		BotLogin:       store.Carry(r.BotLogin),
		BotPassword:    store.Carry(r.BotPassword),
		AccessToken:    store.Carry(r.AccessToken),
		AllowPlaintext: store.Carry(r.AllowPlaintext),
		Secret:         store.Carry(r.Secret),
		PrevSecret:     store.Carry(r.PrevSecret),
		PrevUntil:      store.Carry(r.PrevUntil),
		Health:         store.Carry(r.Health),
		HealthMsg:      store.Carry(r.HealthMsg),
		Repos:          store.Carry(r.Repos)}
}

// ForgeKey is the key model of forge/<id>.
func ForgeKey(id string) *ForgeRecord { return &ForgeRecord{ID: store.ID(id)} }

// ForgeIndex is forges/all: the integration ids (≤ 50).
type ForgeIndex struct {
	Name store.KeyField            `pk:"forges" cbor:"-"`
	ETag store.ETagField           `etag:"true" cbor:"-"`
	IDs  store.DataField[[]string] `cbor:"ids,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *ForgeIndex) Draft() *ForgeIndex {
	return &ForgeIndex{Name: r.Name,
		ETag: r.ETag,
		IDs:  store.Carry(r.IDs)}
}

// ForgeIndexKey is the key model of forges/all.
func ForgeIndexKey() *ForgeIndex { return &ForgeIndex{Name: store.ID("all")} }

// MaxIntegrations bounds forges/all (ListForgeIntegrations: ≤ 50).
const MaxIntegrations = 50

// MaxLinkedRepos bounds ForgeRecord.Repos.
const MaxLinkedRepos = 5000

// RepoLinkRecord is repo/<forgeID>.<enc(forge repo id)>: the project a repository is linked to
// (R-PIPE-002), with the repository's name and clone URL as of linking (the checkout path
// needs them without a forge round trip). SPEC writes the key repo/<forgeID>/<enc(id)>; the
// store's keys have two or four segments, so the forge id and the encoded repo id share one.
type RepoLinkRecord struct {
	Key  store.KeyField  `pk:"repo" cbor:"-"`
	ETag store.ETagField `etag:"true" cbor:"-"`

	Project  store.DataField[string]    `cbor:"p,omitempty"`
	FullName store.DataField[string]    `cbor:"n,omitempty"`
	CloneURL store.DataField[string]    `cbor:"u,omitempty"`
	Created  store.DataField[time.Time] `cbor:"c,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *RepoLinkRecord) Draft() *RepoLinkRecord {
	return &RepoLinkRecord{Key: r.Key,
		ETag:     r.ETag,
		Project:  store.Carry(r.Project),
		FullName: store.Carry(r.FullName),
		CloneURL: store.Carry(r.CloneURL),
		Created:  store.Carry(r.Created)}
}

// RepoLinkKey is the key model of the link of repository repoID of integration forgeID.
func RepoLinkKey(forgeID string, repoID int64) *RepoLinkRecord {
	e, _ := store.Enc(strconv.FormatInt(repoID, 10))
	return &RepoLinkRecord{Key: store.ID(forgeID + "." + e)}
}

// DeliveryRecord is fdel/<forgeID>.<sha256(delivery id)[:40]>: the de-duplication record of one
// delivery (create-only; kept 30 days by a lifecycle rule). Result "pending" means the delivery
// was seen but not finished (a crash or an error after the create): a repeat finishes it.
type DeliveryRecord struct {
	Key  store.KeyField  `pk:"fdel" cbor:"-"`
	ETag store.ETagField `etag:"true" cbor:"-"`

	Received store.DataField[time.Time] `cbor:"r,omitempty"`
	Event    store.DataField[string]    `cbor:"e,omitempty"`
	Result   store.DataField[string]    `cbor:"s,omitempty"`
	Build    store.DataField[string]    `cbor:"b,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *DeliveryRecord) Draft() *DeliveryRecord {
	return &DeliveryRecord{Key: r.Key,
		ETag:     r.ETag,
		Received: store.Carry(r.Received),
		Event:    store.Carry(r.Event),
		Result:   store.Carry(r.Result),
		Build:    store.Carry(r.Build)}
}

// DeliveryKey is the key model of a delivery's record.
func DeliveryKey(forgeID, deliveryID string) *DeliveryRecord {
	return &DeliveryRecord{Key: store.ID(forgeID + "." + deliveryHash(deliveryID))}
}

func deliveryHash(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:])[:40]
}

// RingEntry is one delivery in fdelr/<forgeID>.
type RingEntry struct {
	ID       string    `cbor:"i,omitempty"`
	Event    string    `cbor:"e,omitempty"`
	Result   string    `cbor:"s,omitempty"`
	Build    string    `cbor:"b,omitempty"`
	Received time.Time `cbor:"r,omitzero"`
}

// DeliveryRing is fdelr/<forgeID>: the last RingSize deliveries, newest last (best effort).
type DeliveryRing struct {
	Forge   store.KeyField               `pk:"fdelr" cbor:"-"`
	ETag    store.ETagField              `etag:"true" cbor:"-"`
	Entries store.DataField[[]RingEntry] `cbor:"d,omitempty"`
}

// Draft returns a fresh update value with r's keys, ETag and data, for a CAS write.
func (r *DeliveryRing) Draft() *DeliveryRing {
	return &DeliveryRing{Forge: r.Forge,
		ETag:    r.ETag,
		Entries: store.Carry(r.Entries)}
}

// RingSize is the length of the delivery ring (§05.4.5).
const RingSize = 200
