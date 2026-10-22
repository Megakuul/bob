package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/forge/forgejo"
	"github.com/megakuul/bob/internal/forge/github"
	"github.com/megakuul/bob/internal/forge/gitlab"
	forgev1 "github.com/megakuul/bob/internal/gen/bob/forge/v1"
	"github.com/megakuul/bob/internal/store"
)

// ErrUnknown is an integration id without a record.
var ErrUnknown = errors.New("forge: unknown integration")

// GitHubAPI is the base URL of a GitHub integration that names none.
const GitHubAPI = "https://api.github.com"

// Registry hands out the adapter of an integration, built from its record. Adapters are kept
// while the record's credentials and secrets stay the same, so their caches (status tokens,
// GitHub checkout handles) survive health updates. Safe for concurrent use.
type Registry struct {
	Control *store.Bucket
	Now     func() time.Time
	Log     *slog.Logger

	cache store.Cache[ForgeRecord]
	mu    sync.Mutex
	built map[string]built // integration id → adapters
}

type built struct {
	fingerprint string
	cur, prev   forge.Adapter // prev verifies with the previous webhook secret (nil: none)
	prevUntil   time.Time
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Registry) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Record reads forge/<id> (conditional GET against the local copy).
func (r *Registry) Record(ctx context.Context, id string) (*ForgeRecord, error) {
	if _, err := store.Segment(id); err != nil {
		return nil, ErrUnknown
	}
	rec, _, err := store.GetCached(ctx, r.Control, &r.cache, ForgeKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w %s", ErrUnknown, id)
	}
	return rec, err
}

// Adapter returns the record and the adapter of integration id.
func (r *Registry) Adapter(ctx context.Context, id string) (*ForgeRecord, forge.Adapter, error) {
	rec, b, err := r.adapters(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return rec, b.cur, nil
}

func (r *Registry) adapters(ctx context.Context, id string) (*ForgeRecord, built, error) {
	rec, err := r.Record(ctx, id)
	if err != nil {
		return nil, built{}, err
	}
	fp := fingerprint(rec)
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.built[id]; ok && b.fingerprint == fp {
		return rec, b, nil
	}
	b := built{fingerprint: fp, prevUntil: rec.PrevUntil.Value()}
	if b.cur, err = NewAdapter(ConfigOf(rec), rec.Secret.Value()); err != nil {
		return nil, built{}, err
	}
	if rec.PrevSecret.Value() != "" {
		if b.prev, err = NewAdapter(ConfigOf(rec), rec.PrevSecret.Value()); err != nil {
			return nil, built{}, err
		}
	}
	if r.built == nil {
		r.built = map[string]built{}
	}
	r.built[id] = b
	return rec, b, nil
}

// ParseWebhook verifies and normalizes a delivery for integration id; during a rotation the
// previous secret is accepted until PrevUntil.
func (r *Registry) ParseWebhook(ctx context.Context, id string, h http.Header, body []byte) (*ForgeRecord, forge.Event, error) {
	rec, b, err := r.adapters(ctx, id)
	if err != nil {
		return nil, forge.Event{}, err
	}
	now := r.now()
	ev, err := b.cur.ParseWebhook(h, body, now)
	if errors.Is(err, forge.ErrSignature) && b.prev != nil && now.Before(b.prevUntil) {
		ev, err = b.prev.ParseWebhook(h, body, now)
	}
	return rec, ev, err
}

// fingerprint identifies what an adapter is built from.
func fingerprint(rec *ForgeRecord) string {
	h := sha256.New()
	for _, s := range []string{rec.Kind.Value(), rec.BaseURL.Value(), fmt.Sprint(rec.AppID.Value(), rec.InstallationID.Value(), rec.AllowPlaintext.Value()),
		rec.PrivateKeyPEM.Value(), rec.BotLogin.Value(), rec.BotPassword.Value(), rec.AccessToken.Value(), rec.Secret.Value(), rec.PrevSecret.Value(), rec.PrevUntil.Value().String()} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NewAdapter builds the adapter of rec with webhook secret secret.
func NewAdapter(c Config, secret string) (forge.Adapter, error) {
	switch forge.Kind(c.Kind) {
	case forge.GitHub:
		base := c.BaseURL
		if base == "" {
			base = GitHubAPI
		}
		return github.New(github.Config{BaseURL: base, AppID: c.AppID, InstallationID: c.InstallationID,
			PrivateKeyPEM: forge.NewSecret(c.PrivateKeyPEM), WebhookSecret: forge.NewSecret(secret)})
	case forge.Forgejo:
		return forgejo.New(forgejo.Config{BaseURL: c.BaseURL, BotLogin: c.BotLogin,
			BotPassword: forge.NewSecret(c.BotPassword), WebhookSecret: forge.NewSecret(secret)})
	case forge.GitLab:
		return gitlab.New(gitlab.Config{BaseURL: c.BaseURL, AccessToken: forge.NewSecret(c.AccessToken),
			SigningToken: forge.NewSecret(secret), AllowPlaintextSecret: c.AllowPlaintext})
	}
	return nil, fmt.Errorf("forge: unknown kind %q", c.Kind)
}

// Config is the adapter configuration of an integration: the kind, display name, base URL and
// credential fields of a ForgeRecord as plain values (API handlers edit and validate it before
// it is written).
type Config struct {
	Kind, DisplayName, BaseURL                        string
	AppID, InstallationID                             int64
	PrivateKeyPEM, BotLogin, BotPassword, AccessToken string
	AllowPlaintext                                    bool
}

// ConfigOf reads the configuration of a fetched record.
func ConfigOf(rec *ForgeRecord) Config {
	return Config{Kind: rec.Kind.Value(), DisplayName: rec.DisplayName.Value(), BaseURL: rec.BaseURL.Value(),
		AppID: rec.AppID.Value(), InstallationID: rec.InstallationID.Value(), PrivateKeyPEM: rec.PrivateKeyPEM.Value(),
		BotLogin: rec.BotLogin.Value(), BotPassword: rec.BotPassword.Value(), AccessToken: rec.AccessToken.Value(),
		AllowPlaintext: rec.AllowPlaintext.Value()}
}

// apply writes c into rec (update operators).
func (c Config) apply(rec *ForgeRecord) {
	rec.Kind, rec.DisplayName, rec.BaseURL = store.Set(c.Kind), store.Set(c.DisplayName), store.Set(c.BaseURL)
	rec.AppID, rec.InstallationID, rec.PrivateKeyPEM = store.Set(c.AppID), store.Set(c.InstallationID), store.Set(c.PrivateKeyPEM)
	rec.BotLogin, rec.BotPassword, rec.AccessToken = store.Set(c.BotLogin), store.Set(c.BotPassword), store.Set(c.AccessToken)
	rec.AllowPlaintext = store.Set(c.AllowPlaintext)
}

// NewSecret generates a webhook secret: 32 random bytes, hex; GitLab signing tokens are
// whsec_<base64> (S16 §5.4).
func NewSecret(kind forge.Kind) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	if kind == forge.GitLab {
		return "whsec_" + base64.StdEncoding.EncodeToString(b), nil
	}
	return hex.EncodeToString(b), nil
}

// Report records the outcome of an adapter call in the integration's health: ErrAuth sets
// AUTH_FAILED with the message of SPEC §6.9.4, a success clears it. Best effort.
func (r *Registry) Report(ctx context.Context, id string, callErr error) {
	health, msg := forgev1.Health_HEALTH_OK, ""
	switch {
	case errors.Is(callErr, forge.ErrAuth):
		health = forgev1.Health_HEALTH_AUTH_FAILED
		msg = fmt.Sprintf("forge: integration %s: forge rejected bob's credentials (HTTP 401); re-enter the credentials in Settings → Forges", id)
	case callErr != nil:
		return
	}
	cur, err := r.Record(ctx, id)
	if err != nil || (cur.Health.Value() == int32(health) && cur.HealthMsg.Value() == msg) {
		return
	}
	_, err = store.CAS(ctx, r.Control, ForgeKey(id), func(cur *ForgeRecord) (*ForgeRecord, error) {
		if cur == nil || (cur.Health.Value() == int32(health) && cur.HealthMsg.Value() == msg) {
			return nil, store.ErrNoChange
		}
		cur.Health, cur.HealthMsg = store.Set(int32(health)), store.Set(msg)
		return cur, nil
	})
	if err != nil {
		r.log().Warn("forge: integration health not updated", "integration", id, "err", err)
	}
}
