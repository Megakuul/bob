package auth

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/parity"
	"github.com/megakuul/bob/tests/serverenv"
	"github.com/megakuul/bob/tests/ui"
)

// apiPath is the request path rule of R-API-004, verbatim from SPEC.
var apiPath = regexp.MustCompile(`^/(api/bob\.[a-z]+\.v1\.[A-Za-z]+/[A-Za-z]+|auth/oidc/(start|callback)|oauth/(authorize|consent)|_app/.*|[a-z0-9/_.-]*)$`)

// chromiumBin returns the pinned Chromium (BOB_CHROMIUM when tools/ci already realised it).
func chromiumBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("BOB_CHROMIUM"); p != "" {
		return p
	}
	pins, err := serverenv.LoadPins()
	if err != nil {
		t.Fatalf("harness error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nix", "--extra-experimental-features", "nix-command flakes",
		"build", "--no-link", "--print-out-paths", pins.Nixpkgs+"#chromium")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || len(strings.Fields(string(out))) == 0 {
		t.Fatalf("harness error: nix build chromium: %v: %s", err, stderr.String())
	}
	f := strings.Fields(string(out))
	return filepath.Join(f[len(f)-1], "bin", "chromium")
}

// TestRAPI004UIUsesOnlyPublicAPI loads every UI page in a browser against the real server with
// request interception: every request path matches the SPEC pattern and no non-static request
// leaves /api/, /auth/ and /oauth/.
func TestRAPI004UIUsesOnlyPublicAPI(t *testing.T) {
	parity.Covers(t, "R-API-004")
	s := authenv.Shared(t)
	s.NewAdmin(t, "ui-api004", authenv.Password)
	session := s.MustLogin(t, "ui-api004", authenv.Password)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromiumBin(t)), chromedp.Flag("disable-gpu", true), chromedp.Flag("ignore-certificate-errors", true))
	if os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()
	root, cancelRoot := chromedp.NewContext(alloc)
	defer cancelRoot()
	if err := chromedp.Do(root); err != nil {
		t.Fatalf("harness error: start chromium: %v", err)
	}

	type req struct{ url, typ, page string }
	var (
		mu   sync.Mutex
		reqs []req
	)
	base, _ := url.Parse(s.URL)
	paths := []string{"/login"}
	for _, p := range ui.Pages {
		paths = append(paths, p.Path)
	}
	for _, path := range paths {
		ctx, cancel := chromedp.NewContext(root)
		tctx, cancelT := context.WithTimeout(ctx, 45*time.Second)
		sent := chromedp.Events(tctx, network.RequestWillBeSent)
		sockets := chromedp.Events(tctx, network.WebSocketCreated)
		go func(page string) {
			for e, err := range sent {
				if err != nil {
					return
				}
				mu.Lock()
				reqs = append(reqs, req{e.Request.URL, string(e.Type), page})
				mu.Unlock()
			}
		}(path)
		go func(page string) {
			for e, err := range sockets {
				if err != nil {
					return
				}
				mu.Lock()
				reqs = append(reqs, req{e.URL, "WebSocket", page})
				mu.Unlock()
			}
		}(path)
		err := chromedp.Do(tctx,
			chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
				if _, err := chromedp.Call(ctx, network.Enable, network.EnableParams{}); err != nil {
					return err
				}
				if path == "/login" {
					return nil
				}
				_, err := chromedp.Call(ctx, network.SetCookie, network.SetCookieParams{Name: "bob_session", Value: session.Cookie, URL: s.URL})
				return err
			}),
			chromedp.Navigate(s.URL+path),
			chromedp.Sleep(2*time.Second), // the client router fetches after hydration
		)
		cancelT()
		cancel()
		if err != nil {
			t.Fatalf("harness error: load %s: %v", path, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	apis := 0
	for _, r := range reqs {
		u, err := url.Parse(r.url)
		if err != nil || strings.HasPrefix(r.url, "data:") || strings.HasPrefix(r.url, "blob:") || strings.HasPrefix(r.url, "about:") {
			continue
		}
		if u.Host != base.Host {
			t.Errorf("%s: request to another origin: %s", r.page, r.url)
			continue
		}
		if !apiPath.MatchString(u.Path) {
			t.Errorf("%s: request path %s does not match the R-API-004 pattern", r.page, u.Path)
		}
		static := r.typ == "Document" || r.typ == "Script" || r.typ == "Stylesheet" || r.typ == "Image" || r.typ == "Font" || r.typ == "Manifest" || r.typ == "Other" && strings.HasPrefix(u.Path, "/_app/")
		if !static && !(strings.HasPrefix(u.Path, "/api/") || strings.HasPrefix(u.Path, "/auth/") || strings.HasPrefix(u.Path, "/oauth/")) {
			t.Errorf("%s: non-static request (%s) outside /api/, /auth/, /oauth/: %s", r.page, r.typ, u.Path)
		}
		if strings.HasPrefix(u.Path, "/api/") {
			apis++
		}
	}
	if apis == 0 {
		t.Errorf("no page issued a single /api/ request: the UI is not talking to the server")
	}
}
