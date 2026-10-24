//go:build bobtest

package fetch

import (
	"context"
	"testing"
)

// Test builds with BOB_TEST_HTTP_LOOPBACK=1 let unpinned plain http reach loopback addresses
// (A108, A109); other hosts stay refused.
func TestLoopbackHTTPAllowedInTestBuilds(t *testing.T) {
	t.Setenv("BOB_TEST_HTTP_LOOPBACK", "1")
	e := newEnv(t)
	body := []byte("payload\n")
	u := e.serve("/f", 200, body)
	h, _, err := e.f.Resolve(context.Background(), Request{URLs: []string{u}, Kind: File, Name: "p", Label: "demo"})
	if err != nil || h != sriOf(body) {
		t.Fatalf("loopback http: %q %v", h, err)
	}
	_, _, err = e.f.Resolve(context.Background(), Request{URLs: []string{"http://example.com/f"}, Kind: File, Name: "p", Label: "demo"})
	code(t, err, 3002)
}

// Without the env var even a bobtest build refuses plain http to loopback.
func TestLoopbackHTTPRefusedWithoutEnv(t *testing.T) {
	t.Setenv("BOB_TEST_HTTP_LOOPBACK", "")
	e := newEnv(t)
	u := e.serve("/f", 200, []byte("payload\n"))
	_, _, err := e.f.Resolve(context.Background(), Request{URLs: []string{u}, Kind: File, Name: "p", Label: "demo"})
	code(t, err, 3002)
	if e.hits.Load() != 0 {
		t.Errorf("the refused download contacted the server")
	}
}
