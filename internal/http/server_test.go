package http

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandlerPendingReplaced: a pattern equal to a pending prefix ("/" for the embedded UI)
// replaces its 501, while the other pending prefixes and more specific patterns keep winning.
func TestHandlerPendingReplaced(t *testing.T) {
	named := func(n string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, n) })
	}
	h := Handler(Routes{Patterns: map[string]http.Handler{
		"/": named("ui"), "GET /oauth/consent": named("ui"), "POST /oauth/consent": named("auth"),
	}}, slog.New(slog.DiscardHandler))
	for _, c := range []struct {
		method, path, want string
		code               int
	}{
		{"GET", "/p/x/builds/y", "ui", 200},
		{"GET", "/oauth/consent", "ui", 200},
		{"POST", "/oauth/consent", "auth", 200},
		{"GET", "/oauth/other", "not implemented: /oauth/\n", 501},
		{"GET", "/nix/x.narinfo", "not implemented: /nix/\n", 501},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.code || w.Body.String() != c.want {
			t.Errorf("%s %s: %d %q, want %d %q", c.method, c.path, w.Code, w.Body.String(), c.code, c.want)
		}
	}
}
