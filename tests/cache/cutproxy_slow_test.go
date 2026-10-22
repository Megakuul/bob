//go:build slow

package cache

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// cutProxy forwards to a bob orchestrator and cuts the first full NAR response after cutAt
// bytes (the connection is closed with the body incomplete). It records the start of the first
// ranged NAR request that follows.
type cutProxy struct {
	*httptest.Server
	cuts        atomic.Int32
	resumedFrom atomic.Int64
}

func newCutProxy(t testing.TB, upstream string, cutAt int64) *cutProxy {
	p := &cutProxy{}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, upstream+r.URL.RequestURI(), nil)
		for k, v := range r.Header {
			req.Header[k] = v
		}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		isNAR := strings.HasPrefix(r.URL.Path, "/nix/nar/")
		if rng := r.Header.Get("Range"); isNAR && strings.HasPrefix(rng, "bytes=") && p.cuts.Load() > 0 {
			from, _, _ := strings.Cut(strings.TrimPrefix(rng, "bytes="), "-")
			if n, err := strconv.ParseInt(from, 10, 64); err == nil {
				p.resumedFrom.CompareAndSwap(0, n)
			}
		}
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		if !isNAR || r.Header.Get("Range") != "" || p.cuts.Load() > 0 || resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(w, resp.Body)
			return
		}
		p.cuts.Add(1)
		_, _ = io.CopyN(w, resp.Body, cutAt)
		w.(http.Flusher).Flush()
		if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetLinger(0)
			}
			_ = c.Close()
		}
	}))
	t.Cleanup(p.Close)
	return p
}
