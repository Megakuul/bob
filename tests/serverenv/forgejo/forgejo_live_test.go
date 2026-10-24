//go:build live

package forgejo_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/forgejo"
)

func TestForgejoLiveStartsSeeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	in, err := forgejo.Start(ctx, forgejo.Options{Dir: t.TempDir(), Seed: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("forgejo up at %s in %v", in.URL, time.Since(start))
	defer func() {
		if err := in.Stop(); err != nil {
			t.Error(err)
		}
	}()
	var v struct{ Version string }
	if err := in.API(ctx, http.MethodGet, "/api/v1/version", forgejo.Auth{}, nil, &v); err != nil || v.Version == "" {
		t.Fatalf("version: %q %v", v.Version, err)
	}
	if err := in.API(ctx, http.MethodGet, "/api/v1/repos/alice/demo", forgejo.Auth{Token: in.Seeded.StatusToken}, nil, nil); err != nil {
		t.Errorf("bobbot on alice/demo: %v", err)
	}
	err = in.API(ctx, http.MethodGet, "/api/v1/repos/alice/demo", forgejo.Auth{User: "mallory", Pass: forgejo.UserPass("mallory")}, nil, nil)
	var se *forgejo.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Errorf("mallory on private alice/demo: %v, want 404", err)
	}
}
