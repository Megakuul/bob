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
	ID   string `pk:"forge" cbor:"-"`
	ETag string `etag:"true" cbor:"-"`

	Kind        string    `cbor:"k,omitempty"` // github | forgejo | gitlab
	DisplayName string    `cbor:"n,omitempty"`
	BaseURL     string    `cbor:"u,omitempty"`
	Created     time.Time `cbor:"c,omitzero"`

	AppID          int64  `cbor:"ga,omitempty"` // github
	InstallationID int64  `cbor:"gi,omitempty"`
	PrivateKeyPEM  string `cbor:"gk,omitempty"`
	BotLogin       string `cbor:"fl,omitempty"` // forgejo (D62)
	BotPassword    string `cbor:"fp,omitempty"`
	AccessToken    string `cbor:"lt,omitempty"` // gitlab
	AllowPlaintext bool   `cbor:"lp,omitempty"`

	// Secret verifies deliveries; PrevSecret is still accepted until PrevUntil (24 h after a
	// rotation, §6.9.3).
	Secret     string    `cbor:"s,omitempty"`
	PrevSecret string    `cbor:"ps,omitempty"`
	PrevUntil  time.Time `cbor:"pu,omitzero"`

	Health    int32  `cbor:"h,omitempty"` // forgev1.Health
	HealthMsg string `cbor:"hm,omitempty"`

	// Repos are the linked repositories: forge repository id → project id. It is the index of
	// the repo/ links of this integration (DeleteForgeIntegration is refused while it is not
	// empty; RotateWebhookSecret re-ensures their hooks). Written before the link record and
	// removed after it (R-SRV-101), so it may name a link that a crash left unwritten.
	Repos map[string]string `cbor:"r,omitempty"`
}

// ForgeKey is the key model of forge/<id>.
func ForgeKey(id string) *ForgeRecord { return &ForgeRecord{ID: id} }

// ForgeIndex is forges/all: the integration ids (≤ 50).
type ForgeIndex struct {
	Name string   `pk:"forges" cbor:"-"`
	ETag string   `etag:"true" cbor:"-"`
	IDs  []string `cbor:"ids,omitempty"`
}

// ForgeIndexKey is the key model of forges/all.
func ForgeIndexKey() *ForgeIndex { return &ForgeIndex{Name: "all"} }

// MaxIntegrations bounds forges/all (ListForgeIntegrations: ≤ 50).
const MaxIntegrations = 50

// MaxLinkedRepos bounds ForgeRecord.Repos.
const MaxLinkedRepos = 5000

// RepoLinkRecord is repo/<forgeID>.<enc(forge repo id)>: the project a repository is linked to
// (R-PIPE-002), with the repository's name and clone URL as of linking (the checkout path
// needs them without a forge round trip). SPEC writes the key repo/<forgeID>/<enc(id)>; the
// store's keys have two or four segments, so the forge id and the encoded repo id share one.
type RepoLinkRecord struct {
	Key  string `pk:"repo" cbor:"-"`
	ETag string `etag:"true" cbor:"-"`

	Project  string    `cbor:"p,omitempty"`
	FullName string    `cbor:"n,omitempty"`
	CloneURL string    `cbor:"u,omitempty"`
	Created  time.Time `cbor:"c,omitzero"`
}

// RepoLinkKey is the key model of the link of repository repoID of integration forgeID.
func RepoLinkKey(forgeID string, repoID int64) *RepoLinkRecord {
	e, _ := store.Enc(strconv.FormatInt(repoID, 10))
	return &RepoLinkRecord{Key: forgeID + "." + e}
}

// DeliveryRecord is fdel/<forgeID>.<sha256(delivery id)[:40]>: the de-duplication record of one
// delivery (create-only; kept 30 days by a lifecycle rule). Result "pending" means the delivery
// was seen but not finished (a crash or an error after the create): a repeat finishes it.
type DeliveryRecord struct {
	Key  string `pk:"fdel" cbor:"-"`
	ETag string `etag:"true" cbor:"-"`

	Received time.Time `cbor:"r,omitzero"`
	Event    string    `cbor:"e,omitempty"`
	Result   string    `cbor:"s,omitempty"`
	Build    string    `cbor:"b,omitempty"`
}

// DeliveryKey is the key model of a delivery's record.
func DeliveryKey(forgeID, deliveryID string) *DeliveryRecord {
	return &DeliveryRecord{Key: forgeID + "." + deliveryHash(deliveryID)}
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
	Forge   string      `pk:"fdelr" cbor:"-"`
	ETag    string      `etag:"true" cbor:"-"`
	Entries []RingEntry `cbor:"d,omitempty"`
}

// RingSize is the length of the delivery ring (§05.4.5).
const RingSize = 200
