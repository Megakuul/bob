// Package http runs the server listeners, the endpoint map and the middleware (SPEC §6.2).
package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

// Routes are the handlers of the main listener. A nil handler is not mounted.
type Routes struct {
	API http.Handler // /api/
	// Patterns are further handlers by http.ServeMux pattern ("POST /oauth/token",
	// "GET /nix/nix-cache-info", …). A more specific pattern wins over the pending prefixes
	// below; later milestones add /nix/, /v2/, /oci/token, /r/, /upload/, /hooks/ and the
	// embedded UI (§6.2).
	Patterns map[string]http.Handler
}

// pending are the §6.2 routes whose milestones have not landed: they answer 501 with the
// "not implemented:" text instead of a misleading 404.
var pending = []string{"/api/", "/nix/", "/v2/", "/oci/token", "/r/", "/upload/", "/hooks/", "/oauth/", "/auth/", "/"}

// Handler is the endpoint map of the main listener (server.listen).
func Handler(r Routes, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	if r.API != nil {
		mux.Handle("/api/", r.API)
	}
	for p, h := range r.Patterns {
		mux.Handle(p, h)
	}
	for _, p := range pending {
		if p == "/api/" && r.API != nil {
			continue
		}
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not implemented: "+p, http.StatusNotImplemented)
		})
	}
	return Recover(mux, log)
}

// NixCacheInfo answers GET /nix/nix-cache-info (SPEC §6.6.1) until the binary cache
// milestone serves all of /nix/: anonymous or a valid token → the cache info, an invalid or
// expired token → 401. authn returns an error for a presented credential it refuses.
func NixCacheInfo(authn func(*http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := authn(r); err != nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="bob"`)
			http.Error(w, "unauthorized: token invalid or expired (run 'bob login')", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/x-nix-cache-info")
		_, _ = w.Write([]byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n"))
	})
}

// AdminHandler serves /healthz (process alive) and /readyz (ready() == nil) on the admin
// listener (server.admin_listen). /metrics joins with the metrics milestone.
func AdminHandler(ready func() error, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if err := ready(); err != nil {
			http.Error(w, "not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return Recover(mux, log)
}

// Recover turns a panic in h into a 500 with a reference id; the stack goes to the log only.
// http.ErrAbortHandler keeps its meaning (abort the response silently).
func Recover(h http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
				log.Debug("handler aborted", "path", r.URL.Path)
				return
			}
			b := make([]byte, 6)
			_, _ = rand.Read(b)
			ref := hex.EncodeToString(b)
			log.Error("recovered panic in HTTP handler", "ref", ref, "path", r.URL.Path, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			http.Error(w, "internal error (ref "+ref+")", http.StatusInternalServerError)
		}()
		h.ServeHTTP(w, r)
	})
}

// Server is one HTTP listener with bounded timeouts.
type Server struct {
	ln  net.Listener
	srv *http.Server
}

// Listen binds addr. Binding happens before the process reports itself started, so a busy
// port is a start-up error, not a later failure.
func Listen(addr string, h http.Handler, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	return New(ln, h, log), nil
}

// New serves h on an already bound listener. Connections that arrive before Serve wait in
// the listen backlog instead of being refused.
func New(ln net.Listener, h http.Handler, log *slog.Logger) *Server {
	// HTTP/1.1 plus cleartext HTTP/2 (h2c): the bidirectional Attach stream needs HTTP/2, and
	// a plain http:// listener (devstack, a TLS-terminating proxy in front) has no ALPN.
	protos := new(http.Protocols)
	protos.SetHTTP1(true)
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	return &Server{ln: ln, srv: &http.Server{
		Protocols:         protos,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}}
}

// Addr is the bound address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Serve serves until ctx is done, then shuts down gracefully: open requests get up to grace
// to finish, after which the remaining connections are closed. Streams (Attach, WatchBuild)
// see their contexts cancelled.
func (s *Server) Serve(ctx context.Context, grace time.Duration) error {
	s.srv.BaseContext = func(net.Listener) context.Context { return ctx }
	done := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		defer cancel()
		err := s.srv.Shutdown(sctx)
		if err != nil {
			err = errors.Join(err, s.srv.Close())
		}
		done <- err
	})
	err := s.srv.Serve(s.ln)
	if !errors.Is(err, http.ErrServerClosed) {
		stop()
		_ = s.srv.Close()
		return err
	}
	return <-done
}
