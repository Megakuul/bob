package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	notifyv1 "github.com/megakuul/bob/internal/gen/bob/notify/v1"
	"github.com/megakuul/bob/tests/store/env"
)

// TestDeliveryRetriesAndSignatures is R-PIPE-006 against RustFS with a stepped clock: a receiver
// answering 500 twice and then 200 gets three deliveries with one Bob-Delivery and valid
// signatures, the second 10 s and the third 1 min after the event; last_result is "200 at …".
func TestDeliveryRetriesAndSignatures(t *testing.T) {
	if testing.Short() {
		t.Skip("starts RustFS")
	}
	be := env.Start(t)
	control := be.Open(t, "control")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	type got struct {
		h    http.Header
		body []byte
	}
	var mu sync.Mutex
	var ds []got
	recv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		ds = append(ds, got{r.Header.Clone(), b})
		n := len(ds)
		mu.Unlock()
		if n <= 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	defer recv.Close()

	t0 := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	var clock struct {
		sync.Mutex
		now time.Time
	}
	clock.now = t0
	now := func() time.Time { clock.Lock(); defer clock.Unlock(); return clock.now }
	set := func(d time.Duration) time.Time {
		clock.Lock()
		defer clock.Unlock()
		clock.now = t0.Add(d)
		return clock.now
	}

	n := New(control, now, nil)
	n.HTTP = recv.Client()
	svc := Service{N: n}
	if _, err := svc.CreateNotifyWebhook(ctx, connect.NewRequest(&notifyv1.CreateNotifyWebhookRequest{Webhook: &notifyv1.NotifyWebhook{
		Url: "http://plain.example/x", Events: []string{BuildFinished}}})); err == nil {
		t.Errorf("an http receiver was accepted")
	}
	created, err := svc.CreateNotifyWebhook(ctx, connect.NewRequest(&notifyv1.CreateNotifyWebhookRequest{Webhook: &notifyv1.NotifyWebhook{
		Url: recv.URL, Events: []string{BuildFinished}, Projects: []string{"acme"}, Enabled: true}}))
	if err != nil {
		t.Fatal(err)
	}
	secret, id := created.Msg.GetSecret(), created.Msg.GetWebhook().GetId()

	build, _ := structpb.NewStruct(map[string]any{"id": "01build", "number": 7})
	if err := n.BuildFinished(ctx, "other", build); err != nil {
		t.Fatal(err)
	}
	if err := n.BuildFinished(ctx, "acme", build); err != nil {
		t.Fatal(err)
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(ds) }
	for _, step := range []struct {
		at   time.Duration
		want int
	}{{0, 1}, {5 * time.Second, 1}, {10 * time.Second, 2}, {30 * time.Second, 2}, {time.Minute, 3}, {2 * time.Hour, 3}} {
		if err := n.Pass(ctx, set(step.at)); err != nil {
			t.Fatalf("pass at +%v: %v", step.at, err)
		}
		if c := count(); c != step.want {
			t.Fatalf("after the pass at +%v: %d deliveries, want %d", step.at, c, step.want)
		}
	}
	sig := regexp.MustCompile(`^t=(\d+),v1=([0-9a-f]{64})$`)
	first := ds[0].h.Get("Bob-Delivery")
	for i, d := range ds {
		if d.h.Get("Bob-Delivery") != first || first == "" || d.h.Get("Bob-Event") != BuildFinished || d.h.Get("Content-Type") != "application/json" {
			t.Errorf("delivery %d headers %v", i+1, d.h)
		}
		m := sig.FindStringSubmatch(d.h.Get("Bob-Signature"))
		if m == nil {
			t.Fatalf("delivery %d: Bob-Signature %q", i+1, d.h.Get("Bob-Signature"))
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(m[1] + "."))
		mac.Write(d.body)
		if hex.EncodeToString(mac.Sum(nil)) != m[2] {
			t.Errorf("delivery %d: invalid signature", i+1)
		}
	}
	var payload struct {
		Event, Project string
		Build          struct{ ID string }
	}
	if err := json.Unmarshal(ds[2].body, &payload); err != nil || payload.Event != BuildFinished || payload.Project != "acme" || payload.Build.ID != "01build" {
		t.Errorf("payload %s: %v", ds[2].body, err)
	}
	wh, err := svc.GetNotifyWebhook(ctx, connect.NewRequest(&notifyv1.GetNotifyWebhookRequest{Id: id}))
	if err != nil || !strings.HasPrefix(wh.Msg.GetWebhook().GetLastResult(), "200 at ") {
		t.Errorf("last_result = %q, %v", wh.Msg.GetWebhook().GetLastResult(), err)
	}
	if left, err := n.outbox.All(ctx); err != nil || len(left) != 0 {
		t.Errorf("outbox after success: %v, %v", left, err)
	}
}
