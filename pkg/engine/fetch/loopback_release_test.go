//go:build !bobtest

package fetch

import (
	"context"
	"strings"
	"testing"
)

// Release builds refuse unpinned plain http, loopback included (BOB-E-3002, A108).
func TestLoopbackHTTPRefusedInRelease(t *testing.T) {
	e := newEnv(t)
	u := e.serve("/f", 200, []byte("payload\n"))
	for _, url := range []string{u, strings.Replace(u, "127.0.0.1", "localhost", 1)} {
		_, _, err := e.f.Resolve(context.Background(), Request{URLs: []string{url}, Kind: File, Name: "p", Label: "demo"})
		if text := code(t, err, 3002); !strings.Contains(text, "plain http") {
			t.Errorf("%s: %s", url, text)
		}
	}
	if e.hits.Load() != 0 {
		t.Errorf("the refused download contacted the server")
	}
}
