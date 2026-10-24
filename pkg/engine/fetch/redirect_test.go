package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRedirectSchemes is the regression test of review finding F4 (R-NIX-014): a redirect from
// https to plain http is refused unless the request has a pinned hash; https → https is
// followed; the chain is capped.
func TestRedirectSchemes(t *testing.T) {
	body := []byte("payload\n")
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(plain.Close)
	var tlsSrv *httptest.Server
	tlsSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-http":
			http.Redirect(w, r, plain.URL+"/f", http.StatusFound)
		case "/to-https":
			http.Redirect(w, r, tlsSrv.URL+"/f", http.StatusMovedPermanently)
		case "/to-ftp":
			http.Redirect(w, r, "ftp://example.com/f", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, tlsSrv.URL+"/loop", http.StatusFound)
		default:
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(tlsSrv.Close)
	newF := func(t *testing.T) *fetcher {
		c := newClient(Policy{})
		c.http.Transport.(*http.Transport).TLSClientConfig = tlsSrv.Client().Transport.(*http.Transport).TLSClientConfig
		return newWithStore(&memStore{home: t.TempDir(), nars: map[string][]byte{}}, c)
	}
	ctx := context.Background()

	for path, want := range map[string]string{"/to-http": plain.URL + "/f", "/to-ftp": "ftp://example.com/f", "/loop": "redirects"} {
		_, _, err := newF(t).Resolve(ctx, Request{URLs: []string{tlsSrv.URL + path}, Kind: File, Name: "p", Label: "demo"})
		if text := code(t, err, 3002); !strings.Contains(text, want) {
			t.Errorf("%s: 3002 lacks %q:\n%s", path, want, text)
		}
	}
	h, _, err := newF(t).Resolve(ctx, Request{URLs: []string{tlsSrv.URL + "/to-https"}, Kind: File, Name: "p", Label: "demo"})
	if err != nil || h != sriOf(body) {
		t.Fatalf("https → https: %q %v", h, err)
	}
	// With a pinned hash plain http is allowed (the hash authenticates the content).
	if _, err := newF(t).Ensure(ctx, Request{URLs: []string{tlsSrv.URL + "/to-http"}, Kind: File, Hash: sriOf(body), Name: "p", Label: "demo"}); err != nil {
		t.Fatalf("pinned https → http: %v", err)
	}
}
