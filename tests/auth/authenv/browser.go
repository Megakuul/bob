package authenv

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Browser is a scripted browser: it follows redirects itself, keeps cookies per host:port
// (including Secure ones, which Go's cookie jar would not send over the stack's plain HTTP),
// and records the chain of locations it went through.
type Browser struct {
	cookies map[string]map[string]*http.Cookie // host:port → name → cookie
	http    *http.Client
	Chain   []string // every URL requested, in order
	Header  http.Header
}

// NewBrowser returns an empty browser.
func NewBrowser() *Browser {
	return &Browser{cookies: map[string]map[string]*http.Cookie{}, http: HTTPClient(), Header: http.Header{}}
}

// Cookie returns the cookie name for the host of rawURL, or nil.
func (b *Browser) Cookie(rawURL, name string) *http.Cookie {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return b.cookies[u.Host][name]
}

// Result is the final answer of a navigation.
type Result struct {
	Status   int
	URL      string // final URL
	Location string // Location header of the final response (when it is a redirect that was not followed)
	Header   http.Header
	Body     string
	Cookies  []*http.Cookie // Set-Cookie of the final response
}

// Do requests rawURL with method and optional form body, follows redirects until a non-redirect
// answer or until stop(location) is true (the redirect is then returned unfollowed).
func (b *Browser) Do(t testing.TB, method, rawURL string, form url.Values, stop func(loc string) bool) *Result {
	t.Helper()
	for range 12 {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("browser: %v", err)
		}
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			cancel()
			t.Fatalf("browser: %v", err)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for k, v := range b.Header {
			req.Header[k] = v
		}
		for _, c := range b.cookies[u.Host] {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
		b.Chain = append(b.Chain, rawURL)
		resp, err := b.http.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("harness error: browser %s %s: %v", method, rawURL, err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		cancel()
		for _, c := range resp.Cookies() {
			if b.cookies[u.Host] == nil {
				b.cookies[u.Host] = map[string]*http.Cookie{}
			}
			if c.MaxAge < 0 || c.Value == "" {
				delete(b.cookies[u.Host], c.Name)
			} else {
				b.cookies[u.Host][c.Name] = c
			}
		}
		res := &Result{Status: resp.StatusCode, URL: rawURL, Header: resp.Header, Body: string(data), Cookies: resp.Cookies()}
		loc := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode > 399 || loc == "" {
			return res
		}
		next, err := u.Parse(loc)
		if err != nil {
			t.Fatalf("browser: bad Location %q: %v", loc, err)
		}
		res.Location = next.String()
		if stop != nil && stop(res.Location) {
			return res
		}
		rawURL = next.String()
		if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusSeeOther {
			method, form = http.MethodGet, nil
		}
	}
	t.Fatalf("browser: redirect loop from %s: %s", rawURL, fmt.Sprint(b.Chain))
	return nil
}

// Get navigates to rawURL.
func (b *Browser) Get(t testing.TB, rawURL string) *Result {
	t.Helper()
	return b.Do(t, http.MethodGet, rawURL, nil, nil)
}
