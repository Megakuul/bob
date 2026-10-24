package forge_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"testing"
	"time"

	"bobspike/s16/forge"
	"bobspike/s16/forge/forgejo"
	"bobspike/s16/forge/github"
	"bobspike/s16/forge/gitlab"
)

// Contract every adapter must meet, independent of the forge: it implements forge.Adapter,
// reports its Kind, and rejects an unsigned delivery with ErrSignature before parsing.
func TestAdapterContract(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	fj, err := forgejo.New(forgejo.Config{BaseURL: "https://f", WebhookSecret: forge.NewSecret("s")})
	if err != nil {
		t.Fatal(err)
	}
	gh, err := github.New(github.Config{AppID: 1, PrivateKeyPEM: forge.NewSecret(keyPEM), WebhookSecret: forge.NewSecret("s")})
	if err != nil {
		t.Fatal(err)
	}
	glb, err := gitlab.New(gitlab.Config{BaseURL: "https://g", SigningToken: forge.NewSecret("whsec_c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0MTI=")})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		a    forge.Adapter
		kind forge.Kind
		evH  string
		ev   string
	}{{fj, forge.Forgejo, "X-Forgejo-Event", "push"}, {gh, forge.GitHub, "X-GitHub-Event", "push"}, {glb, forge.GitLab, "X-Gitlab-Event", "Push Hook"}} {
		if c.a.Kind() != c.kind {
			t.Errorf("kind %s", c.a.Kind())
		}
		h := http.Header{}
		h.Set(c.evH, c.ev)
		_, err := c.a.ParseWebhook(h, []byte(`{"ref":"refs/heads/main"}`), time.Now())
		if !errors.Is(err, forge.ErrSignature) {
			t.Errorf("%s unsigned: %v", c.kind, err)
		}
		_, err = c.a.ParseWebhook(h, make([]byte, forge.MaxWebhookBody+1), time.Now())
		if !errors.Is(err, forge.ErrTooLarge) {
			t.Errorf("%s oversize: %v", c.kind, err)
		}
	}
}

func TestSecretNeverPrints(t *testing.T) {
	s := forge.NewSecret("hunter2")
	co := forge.Checkout{Token: s}
	for _, out := range []string{s.String(), co.Token.GoString()} {
		if out == "hunter2" {
			t.Fatal("secret printed")
		}
	}
	b, _ := s.MarshalJSON()
	if string(b) != `"[redacted]"` {
		t.Fatal(string(b))
	}
}
