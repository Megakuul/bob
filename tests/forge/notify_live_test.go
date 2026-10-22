//go:build live

package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/harness"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv/regenv"
)

// TestPIPE006OutgoingWebhooks: a receiver answering 500 twice and then 200 sees three deliveries
// with the same Bob-Delivery and valid signatures; last_result shows `200 at <time>`.
func TestPIPE006OutgoingWebhooks(t *testing.T) {
	parity.Covers(t, "R-PIPE-006")
	cert, certPEM := regenv.Loopback(t)
	type got struct {
		h    http.Header
		body []byte
		at   time.Time
	}
	var mu sync.Mutex
	var deliveries []got
	recv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		deliveries = append(deliveries, got{h: r.Header.Clone(), body: b, at: time.Now()})
		n := len(deliveries)
		mu.Unlock()
		if n <= 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	}))
	recv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	recv.StartTLS()
	defer recv.Close()

	e := regenv.UpCA(t, 1, certPEM)
	if e == nil {
		return
	}
	e.Project("acme", "demo")
	var created struct {
		Webhook struct{ ID string }
		Secret  string
	}
	e.MustCall(e.Admin, "bob.notify.v1.NotifyService/CreateNotifyWebhook", map[string]any{"webhook": map[string]any{
		"url": recv.URL, "events": []string{"build.finished"}, "projects": []string{"acme"}, "enabled": true}}, &created)
	if created.Secret == "" {
		t.Fatalf("CreateNotifyWebhook returned no secret")
	}
	dir := harness.Fixture(t, "acme")
	sha := e.Push(dir, "demo", "main")
	var build struct{ Build regenv.Build }
	e.MustCall(e.Admin, "bob.build.v1.BuildService/CreateBuild", map[string]any{
		"projectId": "acme", "pipeline": "ci", "source": map[string]any{"git": map[string]any{"ref": "refs/heads/main", "sha": sha}}}, &build)

	waitFor(t, "three deliveries", 5*time.Minute, func() bool { mu.Lock(); defer mu.Unlock(); return len(deliveries) >= 3 })
	mu.Lock()
	ds := append([]got(nil), deliveries...)
	mu.Unlock()
	if len(ds) != 3 {
		t.Fatalf("%d deliveries, want exactly 3 (500, 500, 200)", len(ds))
	}
	id := ds[0].h.Get("Bob-Delivery")
	sig := regexp.MustCompile(`^t=(\d+),v1=([0-9a-f]{64})$`)
	for i, d := range ds {
		if d.h.Get("Bob-Delivery") != id || id == "" || d.h.Get("Bob-Event") != "build.finished" || d.h.Get("Content-Type") != "application/json" {
			t.Errorf("delivery %d: headers %v, want the same Bob-Delivery %q, Bob-Event build.finished, JSON", i+1, d.h, id)
		}
		m := sig.FindStringSubmatch(d.h.Get("Bob-Signature"))
		if m == nil {
			t.Errorf("delivery %d: Bob-Signature %q does not match t=<unix>,v1=<hex>", i+1, d.h.Get("Bob-Signature"))
			continue
		}
		mac := hmac.New(sha256.New, []byte(created.Secret))
		mac.Write([]byte(m[1] + "."))
		mac.Write(d.body)
		if hex.EncodeToString(mac.Sum(nil)) != m[2] {
			t.Errorf("delivery %d: invalid Bob-Signature", i+1)
		}
		if ts, _ := strconv.ParseInt(m[1], 10, 64); time.Since(time.Unix(ts, 0)) > 10*time.Minute {
			t.Errorf("delivery %d: signature timestamp %s is stale", i+1, m[1])
		}
	}
	var payload struct {
		Event   string
		Project string
		Build   struct{ ID string }
	}
	if err := json.Unmarshal(ds[2].body, &payload); err != nil || payload.Event != "build.finished" || payload.Project != "acme" || payload.Build.ID != build.Build.ID {
		t.Errorf("payload %s (%v), want build.finished of build %s in project acme", ds[2].body, err, build.Build.ID)
	}
	if gap := ds[1].at.Sub(ds[0].at); gap < 9*time.Second {
		t.Errorf("second attempt %v after the first, want the 10 s backoff", gap)
	}
	var wh struct{ Webhook struct{ LastResult string } }
	waitFor(t, "last_result", 30*time.Second, func() bool {
		e.MustCall(e.Admin, "bob.notify.v1.NotifyService/GetNotifyWebhook", map[string]any{"id": created.Webhook.ID}, &wh)
		return strings.HasPrefix(wh.Webhook.LastResult, "200 at ")
	})
	e.AssertNoList()
}
