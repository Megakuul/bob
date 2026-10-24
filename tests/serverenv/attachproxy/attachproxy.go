// Package attachproxy is the TCP front of the orchestrators in the devstack
// (HARNESS.md §13.2). Workers reach each orchestrator through its own front, so a
// test can cut the Attach path without touching S3, count the bytes workers send,
// count the open streams per orchestrator and point one front at another
// orchestrator's backend (a changed TLS key, R-SRV-102).
//
// Control API (served by Proxy.Handler, its URL is devstack.json attach_proxy_url):
//
//	POST /block[?orch=i]      refuse new connections and reset open ones (all fronts without orch); 204
//	POST /unblock[?orch=i]    accept again; 204
//	GET  /bytes               {"to_orchestrator": n}  bytes workers sent, all fronts, since start
//	GET  /streams             {"orchestrator-N": n}   open connections per front
//	POST /redirect?orch=i&to=j  front i forwards to backend j; to<0 restores; 204
//
// A stream is one TCP connection: the Connect Attach stream is one long-lived HTTP/2
// connection per worker and orchestrator.
package attachproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

// Proxy is a set of TCP fronts plus the control handler.
type Proxy struct {
	fronts  []*front
	backend []string
	bytes   atomic.Int64
	wg      sync.WaitGroup
}

type front struct {
	name string
	ln   net.Listener

	mu      sync.Mutex
	target  int // index into Proxy.backend
	blocked bool
	conns   map[net.Conn]struct{}
}

// New starts one front per backend address (host:port), on free loopback ports.
// Front i is named "orchestrator-i".
func New(backends []string) (*Proxy, error) {
	p := &Proxy{backend: append([]string{}, backends...)}
	for i := range backends {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = p.Close()
			return nil, fmt.Errorf("attachproxy: %w", err)
		}
		f := &front{name: "orchestrator-" + strconv.Itoa(i), ln: ln, target: i, conns: map[net.Conn]struct{}{}}
		p.fronts = append(p.fronts, f)
		p.wg.Add(1)
		go p.accept(f)
	}
	return p, nil
}

// Addrs returns the front addresses (host:port), index i fronts orchestrator i.
func (p *Proxy) Addrs() []string {
	var a []string
	for _, f := range p.fronts {
		a = append(a, f.ln.Addr().String())
	}
	return a
}

// Close stops all fronts and resets open connections.
func (p *Proxy) Close() error {
	for _, f := range p.fronts {
		_ = f.ln.Close()
		f.setBlocked(true)
	}
	p.wg.Wait()
	return nil
}

func (f *front) setBlocked(b bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocked = b
	if b {
		for c := range f.conns {
			reset(c)
		}
	}
}

func reset(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = c.Close()
}

func (p *Proxy) accept(f *front) {
	defer p.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.serve(f, c)
		}()
	}
}

func (p *Proxy) serve(f *front, c net.Conn) {
	f.mu.Lock()
	if f.blocked {
		f.mu.Unlock()
		reset(c)
		return
	}
	target := p.backend[f.target]
	f.mu.Unlock()
	b, err := net.Dial("tcp", target)
	if err != nil {
		reset(c)
		return
	}
	f.mu.Lock()
	if f.blocked {
		f.mu.Unlock()
		reset(c)
		reset(b)
		return
	}
	f.conns[c], f.conns[b] = struct{}{}, struct{}{}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.conns, c)
		delete(f.conns, b)
		f.mu.Unlock()
		_ = c.Close()
		_ = b.Close()
	}()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(counter{b, &p.bytes}, c)
		if tc, ok := b.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, b)
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

type counter struct {
	w io.Writer
	n *atomic.Int64
}

func (c counter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n.Add(int64(n))
	return n, err
}

// Bytes returns the bytes sent towards the orchestrators so far.
func (p *Proxy) Bytes() int64 { return p.bytes.Load() }

// Streams returns the open connections per front name. Each connection is
// counted once (client side).
func (p *Proxy) Streams() map[string]int {
	m := map[string]int{}
	for _, f := range p.fronts {
		f.mu.Lock()
		m[f.name] = len(f.conns) / 2
		f.mu.Unlock()
	}
	return m
}

// Block refuses new connections and resets open ones; orch < 0 means all fronts.
func (p *Proxy) Block(orch int, block bool) error {
	for i, f := range p.fronts {
		if orch < 0 || orch == i {
			f.setBlocked(block)
		}
	}
	if orch >= len(p.fronts) {
		return fmt.Errorf("attachproxy: no orchestrator %d", orch)
	}
	return nil
}

// Redirect makes front from forward to the backend of orchestrator to; to < 0 restores.
func (p *Proxy) Redirect(from, to int) error {
	if from < 0 || from >= len(p.fronts) || to >= len(p.backend) {
		return fmt.Errorf("attachproxy: bad redirect %d -> %d", from, to)
	}
	if to < 0 {
		to = from
	}
	f := p.fronts[from]
	f.mu.Lock()
	f.target = to
	for c := range f.conns { // established streams move too: they must reconnect
		reset(c)
	}
	f.mu.Unlock()
	return nil
}

// Handler is the control API.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	orch := func(r *http.Request, key string, def int) (int, error) {
		s := r.URL.Query().Get(key)
		if s == "" {
			return def, nil
		}
		return strconv.Atoi(s)
	}
	bad := func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadRequest) }
	for _, verb := range []string{"block", "unblock"} {
		mux.HandleFunc("POST /"+verb, func(w http.ResponseWriter, r *http.Request) {
			i, err := orch(r, "orch", -1)
			if err == nil {
				err = p.Block(i, verb == "block")
			}
			if err != nil {
				bad(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("GET /bytes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"to_orchestrator": p.Bytes()})
	})
	mux.HandleFunc("GET /streams", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(p.Streams())
	})
	mux.HandleFunc("POST /redirect", func(w http.ResponseWriter, r *http.Request) {
		from, err := orch(r, "orch", -1)
		to, err2 := orch(r, "to", -1)
		if err == nil {
			err = err2
		}
		if err == nil {
			err = p.Redirect(from, to)
		}
		if err != nil {
			bad(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
