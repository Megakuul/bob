// Package notify sends bob's outgoing webhooks (SPEC §6.9.8, R-PIPE-006): NotifyService
// manages the receivers, Publish queues an event for every matching receiver in a durable
// outbox, and the notify controller delivers it, signed, with retries at 0, 10 s, 1 min,
// 10 min and 1 h after the event.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/megakuul/bob/internal/orchestrator"
	"github.com/megakuul/bob/internal/store"
)

// Events a receiver can subscribe to.
const (
	BuildFinished    = "build.finished"
	ReleasePublished = "release.published"
)

// HookRecord is nhook/<id> (NotifyWebhookRecord, §05.4.5). The secret lives only in the
// control bucket; SPEC wants it sealed with an orchestrator data key that is not defined yet
// (D-IB4-6), so it is stored as generated until that key exists.
type HookRecord struct {
	ID   string `pk:"nhook" cbor:"-"`
	ETag string `etag:"true" cbor:"-"`

	URL        string    `cbor:"u,omitempty"`
	Secret     string    `cbor:"s,omitempty"`
	Events     []string  `cbor:"e,omitempty"`
	Projects   []string  `cbor:"p,omitempty"` // empty = all
	Enabled    bool      `cbor:"on,omitempty"`
	LastResult string    `cbor:"lr,omitempty"`
	Created    time.Time `cbor:"c,omitzero"`
}

// HookKey is the key model of nhook/<id>.
func HookKey(id string) *HookRecord { return &HookRecord{ID: id} }

// HookIndex is nhooks/all (≤ MaxHooks ids).
type HookIndex struct {
	Name string   `pk:"nhooks" cbor:"-"`
	ETag string   `etag:"true" cbor:"-"`
	IDs  []string `cbor:"ids,omitempty"`
}

// HookIndexKey is the key model of nhooks/all.
func HookIndexKey() *HookIndex { return &HookIndex{Name: "all"} }

// MaxHooks bounds the receivers (ListNotifyWebhooks: ≤ 50).
const MaxHooks = 50

// Pending is one queued delivery in the outbox nout/<sh2(delivery id)>. The body is fixed at
// publish time, so every attempt sends the same bytes with the same Bob-Delivery.
type Pending struct {
	Hook    string    `cbor:"h,omitempty"`
	Event   string    `cbor:"e,omitempty"`
	Body    []byte    `cbor:"b,omitempty"`
	Attempt int       `cbor:"a,omitempty"` // attempts made
	Created time.Time `cbor:"c,omitzero"`
}

// outboxShards is the shard count of the outbox (each shard ≤ store.MaxShardEntries).
const outboxShards = 4

// MaxBody bounds one event body (a Build in proto JSON is a few KiB; jobs are capped at 1,024).
const MaxBody = 256 << 10

// Schedule is when attempts happen, measured from the event (§6.9.8).
var Schedule = []time.Duration{0, 10 * time.Second, time.Minute, 10 * time.Minute, time.Hour}

// Timeout bounds one delivery attempt.
const Timeout = 10 * time.Second

// ControllerID is the lease name of the notify controller (ctl/notify).
const ControllerID = "notify"

// Notifier queues and delivers outgoing webhooks. Safe for concurrent use.
type Notifier struct {
	Control *store.Bucket
	Now     func() time.Time
	Log     *slog.Logger
	// HTTP sends the deliveries (nil: a client with Timeout that follows no redirects).
	HTTP *http.Client

	outbox *store.ShardSet[Pending]
	hooks  store.Cache[HookRecord]
	wake   chan struct{}
}

// New builds a notifier; it performs no I/O.
func New(control *store.Bucket, now func() time.Time, log *slog.Logger) *Notifier {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Notifier{Control: control, Now: now, Log: log, outbox: store.NewShardSet[Pending](control, "nout", outboxShards),
		wake: make(chan struct{}, 1),
		HTTP: &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// BuildFinished publishes build.finished: {event, build (proto JSON), project}.
func (n *Notifier) BuildFinished(ctx context.Context, project string, build proto.Message) error {
	b, err := protojson.Marshal(build)
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	body, err := json.Marshal(struct {
		Event   string          `json:"event"`
		Build   json.RawMessage `json:"build"`
		Project string          `json:"project"`
	}{BuildFinished, b, project})
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return n.Publish(ctx, BuildFinished, project, body)
}

// ReleasePublished publishes release.published: {event, release (proto JSON)}.
func (n *Notifier) ReleasePublished(ctx context.Context, project string, release proto.Message) error {
	b, err := protojson.Marshal(release)
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	body, err := json.Marshal(struct {
		Event   string          `json:"event"`
		Release json.RawMessage `json:"release"`
	}{ReleasePublished, b})
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return n.Publish(ctx, ReleasePublished, project, body)
}

func (h *HookRecord) wants(event, project string) bool {
	return h.Enabled && slices.Contains(h.Events, event) && (len(h.Projects) == 0 || slices.Contains(h.Projects, project))
}

// Publish queues body for every enabled receiver of event and project, then wakes the
// controller. A receiver that is removed before delivery gets nothing.
func (n *Notifier) Publish(ctx context.Context, event, project string, body []byte) error {
	if len(body) > MaxBody {
		return fmt.Errorf("notify: %s body of %d bytes exceeds %d", event, len(body), MaxBody)
	}
	idx, err := store.Get(ctx, n.Control, HookIndexKey())
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range idx.IDs {
		h, _, err := store.GetCached(ctx, n.Control, &n.hooks, HookKey(id))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !h.wants(event, project) {
			continue
		}
		did, err := deliveryID()
		if err != nil {
			return err
		}
		if err := n.outbox.Add(ctx, did, Pending{Hook: id, Event: event, Body: body, Created: n.Now().UTC()}); err != nil {
			errs = append(errs, fmt.Errorf("notify: queue %s for %s: %w", event, id, err))
		}
	}
	select {
	case n.wake <- struct{}{}:
	default:
	}
	return errors.Join(errs...)
}

func deliveryID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "d" + hex.EncodeToString(b), nil
}

// Sign is the Bob-Signature value of body at t: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>.
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// Controller is the notify controller: one leader delivers the due outbox entries every 2 s and
// at once after a local Publish.
func (n *Notifier) Controller(fleet *store.Bucket, owner string, skew time.Duration) *orchestrator.Controller {
	return &orchestrator.Controller{ID: ControllerID, Fleet: fleet, Owner: owner, Interval: 2 * time.Second, Skew: skew,
		Now: n.Now, Wake: n.wake, Log: n.Log, Pass: func(ctx context.Context, r *orchestrator.Run) error { return n.Pass(ctx, r.Now) }}
}

// Pass delivers every outbox entry whose next attempt is due at now.
func (n *Notifier) Pass(ctx context.Context, now time.Time) error {
	all, err := n.outbox.All(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for did, p := range all {
		if p.Attempt >= len(Schedule) {
			errs = append(errs, n.outbox.Remove(ctx, did))
			continue
		}
		if now.Before(p.Created.Add(Schedule[p.Attempt])) {
			continue
		}
		if err := n.attempt(ctx, did, p, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// attempt makes one delivery attempt and records its outcome: removed on 2xx or after the last
// attempt, otherwise the attempt count advances. last_result shows every outcome.
func (n *Notifier) attempt(ctx context.Context, did string, p Pending, now time.Time) error {
	h, _, err := store.GetCached(ctx, n.Control, &n.hooks, HookKey(p.Hook))
	if errors.Is(err, store.ErrNotFound) {
		return n.outbox.Remove(ctx, did) // receiver deleted
	}
	if err != nil {
		return err
	}
	if !h.Enabled {
		return n.outbox.Remove(ctx, did)
	}
	code, sendErr := n.send(ctx, h, did, p)
	result := fmt.Sprintf("%d at %s", code, now.UTC().Format(time.RFC3339))
	if sendErr != nil {
		result = "error: " + sendErr.Error()
	}
	var errs []error
	if sendErr == nil || p.Attempt+1 >= len(Schedule) {
		errs = append(errs, n.outbox.Remove(ctx, did))
	} else {
		p.Attempt++
		errs = append(errs, n.outbox.Add(ctx, did, p))
	}
	if _, err := store.CAS(ctx, n.Control, HookKey(p.Hook), func(cur *HookRecord) (*HookRecord, error) {
		if cur == nil {
			return nil, store.ErrNoChange
		}
		cur.LastResult = result
		return cur, nil
	}); err != nil {
		errs = append(errs, err)
	}
	if sendErr != nil {
		n.Log.Info("notify: delivery failed", "hook", p.Hook, "delivery", did, "attempt", p.Attempt+1, "err", sendErr.Error())
	}
	return errors.Join(errs...)
}

// send POSTs the delivery; only a 2xx answer is a success.
func (n *Notifier) send(ctx context.Context, h *HookRecord, did string, p Pending) (int, error) {
	if u, err := url.Parse(h.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		return 0, errors.New("receiver URL is not https")
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(p.Body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bob")
	req.Header.Set("Bob-Event", p.Event)
	req.Header.Set("Bob-Delivery", did)
	req.Header.Set("Bob-Signature", Sign(h.Secret, n.Now(), p.Body))
	resp, err := n.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST failed: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}
