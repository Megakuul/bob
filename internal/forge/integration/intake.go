package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/store"
)

// HookPattern is the intake route (SPEC §6.2, §6.9.3).
const HookPattern = "POST /hooks/{integration}"

// Builds creates the build of an accepted delivery (the build controller, M17.2).
type Builds interface {
	// CreateFromEvent creates the BuildRecord (state QUEUED, trigger from ev) of a verified event
	// of the repository linked to project. It is idempotent on key: a repeated call with the same
	// key (a delivery that a crash left unfinished) returns the first call's build id.
	CreateFromEvent(ctx context.Context, project, key string, ev forge.Event) (buildID string, err error)
}

// Intake is the webhook intake: POST /hooks/<integration-id>.
type Intake struct {
	Registry *Registry
	Builds   Builds // nil until builds exist: linked events are answered 503 (the forge retries)
}

// intakeBudget keeps the answer inside the forges' delivery timeouts (GitHub 10 s, §6.9.3).
const intakeBudget = 9 * time.Second

// Results of a delivery (forge.v1.Delivery.result).
const (
	resultPending   = "pending"
	resultAccepted  = "accepted"
	resultNotLinked = "ignored: repository not linked"
	resultPayload   = "rejected: payload"
)

func reply(w http.ResponseWriter, code int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, text+"\n")
}

// ServeHTTP follows §6.9.3: unknown integration 404; body over MaxWebhookBody 413; signature
// (and GitLab timestamp) 401 "invalid signature" with the reason in the log only; hostile
// payload 400; repeated delivery 200 "duplicate"; unlinked repository and ignored events 202;
// otherwise the build is created and the answer is 202.
func (in *Intake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), intakeBudget)
	defer cancel()
	id := r.PathValue("integration")
	if _, err := in.Registry.Record(ctx, id); err != nil {
		if errors.Is(err, ErrUnknown) {
			reply(w, http.StatusNotFound, "unknown integration")
			return
		}
		in.Registry.log().Error("webhook intake: integration record", "integration", id, "err", err)
		reply(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, forge.MaxWebhookBody+1))
	if err != nil {
		reply(w, http.StatusBadRequest, "body unreadable")
		return
	}
	if len(body) > forge.MaxWebhookBody {
		reply(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}
	_, ev, err := in.Registry.ParseWebhook(ctx, id, r.Header, body)
	switch {
	case errors.Is(err, forge.ErrSignature) || errors.Is(err, forge.ErrStale):
		// Unauthenticated: logged without header values, nothing stored (no write amplification).
		in.Registry.log().Warn("webhook intake: delivery refused", "integration", id, "reason", err.Error())
		reply(w, http.StatusUnauthorized, "invalid signature")
		return
	case errors.Is(err, forge.ErrTooLarge):
		reply(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	case errors.Is(err, forge.ErrPayload):
		in.Registry.log().Warn("webhook intake: payload refused", "integration", id, "reason", err.Error())
		in.ring(ctx, id, RingEntry{ID: ev.DeliveryID, Event: string(ev.Type), Result: resultPayload})
		reply(w, http.StatusBadRequest, "invalid payload")
		return
	case errors.Is(err, ErrUnknown):
		reply(w, http.StatusNotFound, "unknown integration")
		return
	case err != nil:
		in.Registry.log().Error("webhook intake: adapter", "integration", id, "err", err)
		reply(w, http.StatusServiceUnavailable, "integration unavailable")
		return
	}
	code, text := in.deliver(ctx, id, ev, body)
	reply(w, code, text)
}

// deliver de-duplicates the verified delivery and acts on it.
func (in *Intake) deliver(ctx context.Context, id string, ev forge.Event, body []byte) (int, string) {
	log := in.Registry.log()
	did := ev.DeliveryID
	if did == "" {
		// Without a delivery id the body is the identity: a byte-identical replay is a duplicate.
		h := sha256.Sum256(body)
		did = "body-" + hex.EncodeToString(h[:])
	}
	key := DeliveryKey(id, did)
	rec := *key
	rec.Received, rec.Event, rec.Result = store.Set(in.Registry.now().UTC()), store.Set(string(ev.Type)), store.Set(resultPending)
	err := store.Create(ctx, in.Registry.Control, &rec)
	if errors.Is(err, store.ErrExists) {
		cur, gerr := store.Get(ctx, in.Registry.Control, DeliveryKey(id, did))
		if gerr != nil {
			log.Error("webhook intake: delivery record", "integration", id, "err", gerr)
			return http.StatusServiceUnavailable, "storage unavailable"
		}
		if cur.Result.Value() != resultPending {
			return http.StatusOK, "duplicate"
		}
		rec = *cur // unfinished: finish it (the build creation is idempotent on the key)
	} else if err != nil {
		log.Error("webhook intake: delivery record", "integration", id, "err", err)
		return http.StatusServiceUnavailable, "storage unavailable"
	}

	result, build := "", ""
	switch ev.Type {
	case forge.EventIgnored:
		result = "ignored: " + ev.IgnoredReason
	case forge.EventPing:
		result = "ignored: ping"
	default:
		link, err := in.Registry.Link(ctx, id, ev.Repo.ID)
		switch {
		case errors.Is(err, ErrNotLinked):
			result = resultNotLinked
		case err != nil:
			log.Error("webhook intake: repository link", "integration", id, "err", err)
			return http.StatusServiceUnavailable, "storage unavailable"
		case in.Builds == nil:
			return http.StatusServiceUnavailable, "builds are not available on this server yet"
		default:
			build, err = in.Builds.CreateFromEvent(ctx, link.Project.Value(), rec.Key.Value(), ev)
			if err != nil {
				log.Error("webhook intake: create build", "integration", id, "project", link.Project.Value(), "err", err)
				return http.StatusServiceUnavailable, "build not created, retry"
			}
			result = resultAccepted
		}
	}
	if _, err := store.CAS(ctx, in.Registry.Control, DeliveryKey(id, did), func(cur *DeliveryRecord) (*DeliveryRecord, error) {
		if cur == nil {
			cur = &rec
		}
		cur.Result, cur.Build = store.Set(result), store.Set(build)
		return cur, nil
	}); err != nil {
		// The build exists (idempotent on the key): a repeat finishes the record.
		log.Warn("webhook intake: delivery result not stored", "integration", id, "err", err)
	}
	in.ring(ctx, id, RingEntry{ID: did, Event: string(ev.Type), Result: result, Build: build})
	if result == resultAccepted {
		return http.StatusAccepted, resultAccepted
	}
	return http.StatusAccepted, result
}

// ring appends to fdelr/<id> (best effort, bounded to RingSize).
func (in *Intake) ring(ctx context.Context, id string, e RingEntry) {
	e.Received = in.Registry.now().UTC()
	if len(e.ID) > 255 {
		e.ID = e.ID[:255]
	}
	_, err := store.CAS(ctx, in.Registry.Control, &DeliveryRing{Forge: store.ID(id)}, func(cur *DeliveryRing) (*DeliveryRing, error) {
		if cur == nil {
			cur = &DeliveryRing{Forge: store.ID(id)}
		}
		cur.Entries = store.Set(append(cur.Entries.Value(), e))
		if n := len(cur.Entries.Value()); n > RingSize {
			cur.Entries = store.Set(append([]RingEntry(nil), cur.Entries.Value()[n-RingSize:]...))
		}
		return cur, nil
	})
	if err != nil {
		in.Registry.log().Warn("webhook intake: delivery ring not updated", "integration", id, "err", err)
	}
}
