package main

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/megakuul/bob/internal/forge/integration"
	"github.com/megakuul/bob/internal/server"
)

// TestWebhookURLFallback: webhook URLs (and the worker endpoint) use server.base_url, else the
// listen address. I-B22: without base_url the hook URL was "/hooks/<id>" and Forgejo refused it
// ("create hook: Invalid url"), so tag pushes triggered nothing.
func TestWebhookURLFallback(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cfg  server.Config
		want string
	}{
		{server.Config{BaseURL: "https://bob.example/", Listen: ":8080"}, "https://bob.example/hooks/i1"},
		{server.Config{Listen: "10.0.0.5:8080"}, "http://10.0.0.5:8080/hooks/i1"},
		{server.Config{Listen: ":8080"}, "http://" + host + ":8080/hooks/i1"},
		{server.Config{Listen: "0.0.0.0:9000"}, "http://" + host + ":9000/hooks/i1"},
		{server.Config{Listen: "[::1]:8080"}, "http://[::1]:8080/hooks/i1"},
	} {
		got := integration.HookURL(endpoint(tc.cfg), "i1")
		if got != tc.want {
			t.Errorf("%+v: hook URL %q, want %q", tc.cfg, got, tc.want)
		}
		if u, err := url.Parse(got); err != nil || !u.IsAbs() || u.Host == "" {
			t.Errorf("%+v: hook URL %q is not an absolute URL a forge accepts", tc.cfg, got)
		}
	}
}

// TestWebhookWiringUsesEndpoint: both places that create forge webhooks (the forge service's
// repository links and project creation) and the build controller (commit status target URLs,
// Build.web_url) take their base from endpoint, not from the raw base_url, which is "" on a
// stack without one.
func TestWebhookWiringUsesEndpoint(t *testing.T) {
	for _, f := range []string{"parts_forge.go", "parts_project.go", "parts_build.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "BaseURL: endpoint(env.Config)") {
			t.Errorf("%s does not give webhook URLs the endpoint base", f)
		}
	}
}
