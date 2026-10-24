package worker

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestAttachClientDetectsDeadConnection: a streaming response whose connection goes silent
// (the path forwards nothing any more, but nobody sends FIN or RST) must fail on the client
// within the HTTP/2 health check (attachPingIdle + attachPingTimeout), so that attachLoop
// reconnects (A291). Without the health check the read blocks until TCP gives up.
func TestAttachClientDetectsDeadConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the HTTP/2 health check")
	}
	srvLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pr := new(http.Protocols)
	pr.SetUnencryptedHTTP2(true)
	srv := &http.Server{Protocols: pr, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // a stream that stays open, like Attach
	})}
	go func() { _ = srv.Serve(srvLn) }()
	defer srv.Close()

	// A TCP relay that can be frozen: it then neither forwards nor closes anything.
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	var mu sync.Mutex
	frozen := false
	var held []net.Conn
	defer func() {
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	}()
	relay := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			mu.Lock()
			f := frozen
			mu.Unlock()
			if f {
				continue // swallow: the peer sees silence, not an error
			}
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go func() {
		for {
			c, err := front.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", srvLn.Addr().String())
			if err != nil {
				_ = c.Close()
				return
			}
			mu.Lock()
			held = append(held, c, b)
			mu.Unlock()
			go relay(b, c)
			go relay(c, b)
		}
	}()

	base := "http://" + front.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := attachClient(base).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("attach client spoke %s, want HTTP/2", resp.Proto)
	}
	first := make([]byte, 6)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	frozen = true
	mu.Unlock()
	start := time.Now()
	_, err = io.Copy(io.Discard, resp.Body)
	took := time.Since(start)
	if ctx.Err() != nil {
		t.Fatalf("the read on a silent connection did not fail within %v (no dead-connection detection)", time.Minute)
	}
	if err == nil {
		t.Fatal("the read on a silent connection ended without an error")
	}
	if limit := attachPingIdle + attachPingTimeout + 5*time.Second; took > limit {
		t.Errorf("dead connection detected after %v, want within %v", took, limit)
	}
}
