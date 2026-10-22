//go:build bobtest

package fetch

import (
	"context"
	"testing"
)

// Test builds let unpinned plain http reach loopback addresses (A108); other hosts stay refused.
func TestLoopbackHTTPAllowedInTestBuilds(t *testing.T) {
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
