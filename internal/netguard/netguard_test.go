package netguard

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// TestRefusesInternalAddresses (I-R2 F8): loopback, private, link-local (IMDS), shared and
// unspecified addresses are refused unless allowed by host name, address or range.
func TestRefusesInternalAddresses(t *testing.T) {
	def, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	allow, err := New([]string{"forge.corp.example", "10.0.0.0/8", "fd00::1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		host, ip    string
		def, allows bool
	}{
		{"x", "203.0.114.7", true, true},
		{"x", "2a00:1450::1", true, true},
		{"x", "169.254.169.254", false, false},
		{"x", "10.1.2.3", false, true},
		{"x", "172.16.0.1", false, false},
		{"x", "192.168.1.1", false, false},
		{"x", "100.64.0.1", false, false},
		{"x", "0.0.0.0", false, false},
		{"x", "::ffff:10.1.2.3", false, true},
		{"x", "64:ff9b::a01:203", false, true},
		{"x", "fe80::1", false, false},
		{"x", "fd00::1", false, true},
		{"x", "fd00::2", false, false},
		{"forge.corp.example", "192.168.7.7", false, true},
		{"x", "127.0.0.1", testLoopback(), testLoopback()},
	} {
		ip := netip.MustParseAddr(c.ip)
		if got := def.Allowed(c.host, ip); got != c.def {
			t.Errorf("default policy, %s (%s): allowed %v, want %v", c.host, c.ip, got, c.def)
		}
		if got := allow.Allowed(c.host, ip); got != c.allows {
			t.Errorf("allowing policy, %s (%s): allowed %v, want %v", c.host, c.ip, got, c.allows)
		}
	}
	if _, err := New([]string{"http://x"}); err == nil {
		t.Errorf("a URL was accepted as an allow entry")
	}
}

// TestClientRefusesLoopbackUntilAllowed: a request to a loopback receiver never connects with
// the default policy (release builds), and goes through once the address is allowed.
func TestClientRefusesLoopbackUntilAllowed(t *testing.T) {
	if testLoopback() {
		t.Skip("bobtest builds allow loopback")
	}
	hits := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	def, _ := New(nil)
	_, err := def.Client(srv.Client()).Get(srv.URL)
	if !errors.Is(err, ErrRefused) || hits != 0 {
		t.Fatalf("default policy: %v (%d hits), want ErrRefused and no connection", err, hits)
	}
	host, _, _ := net.SplitHostPort(srv.Listener.Addr().String())
	allow, _ := New([]string{host})
	resp, err := allow.Client(srv.Client()).Get(srv.URL)
	if err != nil {
		t.Fatalf("allowed loopback: %v", err)
	}
	resp.Body.Close()
	if hits != 1 {
		t.Errorf("%d hits, want 1", hits)
	}
	// a name that resolves to loopback is refused too (checked after resolution)
	d := def.DialContext(&net.Dialer{})
	if _, err := d(context.Background(), "tcp", "localhost:1"); !errors.Is(err, ErrRefused) {
		t.Errorf("localhost: %v, want ErrRefused", err)
	}
}
