package forgefix_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/serverenv/forgefix"
)

// Every fixture parses, and every recorded delivery still verifies against
// its recorded secret, which proves the bytes are exactly as delivered.
func TestFixturesIntact(t *testing.T) {
	forgejoSecret, err := forgefix.Secret("forgejo/SECRET.test")
	if err != nil {
		t.Fatal(err)
	}
	glToken, err := forgefix.Secret("gitlab/live/SIGNING_TOKEN.test")
	if err != nil {
		t.Fatal(err)
	}
	glKey, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(glToken, "whsec_"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sub    string
		min    int
		verify func(t *testing.T, d forgefix.Delivery)
	}{
		{"forgejo", 12, func(t *testing.T, d forgefix.Delivery) {
			m := hmac.New(sha256.New, []byte(forgejoSecret))
			m.Write(d.Body)
			if got := d.Headers.Get("X-Forgejo-Signature"); got != hex.EncodeToString(m.Sum(nil)) {
				t.Errorf("signature mismatch")
			}
		}},
		{"forgejo/raw", 15, nil},
		{"gitlab/live", 8, func(t *testing.T, d forgefix.Delivery) {
			m := hmac.New(sha256.New, glKey)
			m.Write([]byte(d.Headers.Get("Webhook-Id") + "." + d.Headers.Get("Webhook-Timestamp") + "."))
			m.Write(d.Body)
			if got := d.Headers.Get("Webhook-Signature"); got != "v1,"+base64.StdEncoding.EncodeToString(m.Sum(nil)) {
				t.Errorf("standard-webhooks signature mismatch")
			}
		}},
		{"gitlab/derived", 5, nil},
		{"gitlab/sdk-testdata", 4, nil},
		{"github/webhooks", 10, nil},
		{"github/api", 3, nil},
	} {
		t.Run(tc.sub, func(t *testing.T) {
			ds, err := forgefix.Load(tc.sub)
			if err != nil {
				t.Fatal(err)
			}
			if len(ds) < tc.min {
				t.Fatalf("%d deliveries, want ≥ %d", len(ds), tc.min)
			}
			for _, d := range ds {
				if !json.Valid(d.Body) {
					t.Errorf("%s: invalid JSON", d.Name)
				}
				if d.Headers != nil {
					if cl := d.Headers.Get("Content-Length"); cl != "" && cl != strconv.Itoa(len(d.Body)) {
						t.Errorf("%s: Content-Length %s, body %d bytes", d.Name, cl, len(d.Body))
					}
				}
				if tc.verify != nil {
					t.Run(d.Name, func(t *testing.T) { tc.verify(t, d) })
				}
			}
		})
	}
}

func TestReplayRequest(t *testing.T) {
	ds, err := forgefix.Load("forgejo")
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	var event string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		event = r.Header.Get("X-Forgejo-Event")
	}))
	defer srv.Close()
	req, err := ds[0].Request(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if string(got) != string(ds[0].Body) || event != ds[0].Headers.Get("X-Forgejo-Event") {
		t.Fatalf("replay changed the delivery")
	}
}
