package attachproxy_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv/attachproxy"
)

// echo starts a backend that answers every connection with its tag, then echoes.
func backend(t *testing.T, tag string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = c.Write([]byte(tag))
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func hello(t *testing.T, addr string, n int) (net.Conn, string, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, "", err
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, n)
	_, err = io.ReadFull(c, b)
	return c, string(b), err
}

func call(t *testing.T, ts *httptest.Server, method, path string) []byte {
	req, _ := http.NewRequest(method, ts.URL+path, nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if method == "POST" && r.StatusCode != http.StatusNoContent {
		t.Fatalf("%s %s: %s %s", method, path, r.Status, b)
	}
	return b
}

func TestProxy(t *testing.T) {
	p, err := attachproxy.New([]string{backend(t, "A"), backend(t, "B")})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ts := httptest.NewServer(p.Handler())
	defer ts.Close()
	a := p.Addrs()

	c0, got, err := hello(t, a[0], 1)
	if err != nil || got != "A" {
		t.Fatalf("front 0: %q %v", got, err)
	}
	c1, got, err := hello(t, a[1], 1)
	if err != nil || got != "B" {
		t.Fatalf("front 1: %q %v", got, err)
	}
	_, _ = c0.Write([]byte("12345"))
	_, _ = c1.Write([]byte("123"))
	deadline := time.Now().Add(2 * time.Second)
	for p.Bytes() != 8 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	var bytes struct {
		N int64 `json:"to_orchestrator"`
	}
	_ = json.Unmarshal(call(t, ts, "GET", "/bytes"), &bytes)
	if bytes.N != 8 {
		t.Fatalf("bytes = %d, want 8", bytes.N)
	}
	var streams map[string]int
	_ = json.Unmarshal(call(t, ts, "GET", "/streams"), &streams)
	if streams["orchestrator-0"] != 1 || streams["orchestrator-1"] != 1 {
		t.Fatalf("streams = %v", streams)
	}

	// block one front: open connection reset, new refused, other front unaffected
	call(t, ts, "POST", "/block?orch=0")
	_ = c0.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c0.Read(make([]byte, 1)); err == nil {
		t.Fatal("blocked front kept the connection")
	}
	if _, _, err := hello(t, a[0], 1); err == nil {
		t.Fatal("blocked front accepted a connection")
	}
	if _, got, err := hello(t, a[1], 1); err != nil || got != "B" {
		t.Fatalf("front 1 while 0 blocked: %q %v", got, err)
	}
	call(t, ts, "POST", "/unblock")
	if _, got, err := hello(t, a[0], 1); err != nil || got != "A" {
		t.Fatalf("after unblock: %q %v", got, err)
	}

	// redirect front 0 to backend 1 and restore
	call(t, ts, "POST", "/redirect?orch=0&to=1")
	if _, got, err := hello(t, a[0], 1); err != nil || got != "B" {
		t.Fatalf("redirected: %q %v", got, err)
	}
	call(t, ts, "POST", "/redirect?orch=0&to=-1")
	if _, got, err := hello(t, a[0], 1); err != nil || got != "A" {
		t.Fatalf("restored: %q %v", got, err)
	}

	// block all
	call(t, ts, "POST", "/block")
	for i := range a {
		if _, _, err := hello(t, a[i], 1); err == nil {
			t.Fatalf("front %d accepted while all blocked", i)
		}
	}
	r, _ := http.Post(ts.URL+"/block?orch=x", "text/plain", nil)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad orch: %s", r.Status)
	}
}
