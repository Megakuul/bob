// Command cachesrv is the orchestrator side of S12: Nix binary cache + OCI pull registry
// served from the artifacts bucket, plus promotion (verify, sign, publish) of staged builds.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"bobspike/s12/nixfmt"
	"bobspike/s12/store"
)

// Principal is an authenticated caller (user token, service account or anonymous).
type Principal struct {
	Name     string   `json:"name"`
	Projects []string `json:"projects"` // "*" = all
	Anon     bool     `json:"-"`
}

func (p *Principal) canRead(scopes []string) bool {
	for _, s := range scopes {
		if s == store.ScopePublic {
			return true
		}
		if p.Anon {
			continue
		}
		for _, pr := range p.Projects {
			if pr == "*" || store.ProjectScope(pr) == s {
				return true
			}
		}
	}
	return false
}

func (p *Principal) all() bool {
	for _, pr := range p.Projects {
		if pr == "*" {
			return true
		}
	}
	return false
}

type tokenEntry struct {
	SHA256   string   `json:"sha256"` // hex sha256 of the secret; secrets are never stored (R-SEC-005)
	Name     string   `json:"name"`
	Projects []string `json:"projects"`
}

type config struct {
	Tokens []tokenEntry `json:"tokens"`
}

type server struct {
	st        *store.Store
	key       *nixfmt.SecretKey
	tokens    map[string]tokenEntry
	ociSecret []byte
	publicURL string
	negTTL    int
	faultCut  atomic.Int64 // test: cut the next NAR response after N bytes
	stats     struct{ narinfo, nar, narBytes atomic.Int64 }
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:21220", "listen")
	endpoint := flag.String("s3", "http://127.0.0.1:21203", "S3 endpoint")
	ak := flag.String("ak", "orchkey", "")
	sk := flag.String("sk", "orchsecret0123456789", "")
	bucket := flag.String("bucket", "artifacts", "")
	keyFile := flag.String("key", "", "Nix secret key file (name:base64)")
	cfgFile := flag.String("config", "", "tokens JSON")
	pub := flag.String("public-url", "http://127.0.0.1:21220", "")
	flag.Parse()
	kb, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	key, err := nixfmt.ParseSecretKey(string(kb))
	if err != nil {
		return err
	}
	s := &server{st: store.New(*endpoint, *ak, *sk, *bucket), key: key, tokens: map[string]tokenEntry{}, publicURL: *pub}
	if *cfgFile != "" {
		b, err := os.ReadFile(*cfgFile)
		if err != nil {
			return err
		}
		var c config
		if err := json.Unmarshal(b, &c); err != nil {
			return err
		}
		for _, t := range c.Tokens {
			s.tokens[t.SHA256] = t
		}
	}
	s.ociSecret = make([]byte, 32)
	if v := os.Getenv("S12_OCI_SECRET"); v != "" { // HA: all orchestrators share it (control bucket)
		s.ociSecret = []byte(v)
	} else {
		rand.Read(s.ociSecret)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/nix/", s.nixHandler)
	mux.HandleFunc("/v2/", s.ociHandler)
	mux.HandleFunc("/oci/token", s.ociToken)
	mux.HandleFunc("/api/whoami", s.whoami)
	mux.HandleFunc("/internal/promote", s.promoteHandler)
	mux.HandleFunc("/internal/fault", func(w http.ResponseWriter, r *http.Request) {
		var n int64
		fmt.Sscan(r.URL.Query().Get("cut"), &n)
		s.faultCut.Store(n)
	})
	mux.HandleFunc("/internal/stats", func(w http.ResponseWriter, r *http.Request) {
		var ru syscall.Rusage
		syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
		fmt.Fprintf(w, "narinfo=%d nar=%d narBytes=%d maxrssMiB=%d\n", s.stats.narinfo.Load(), s.stats.nar.Load(), s.stats.narBytes.Load(), ru.Maxrss/1024)
	})
	log.Printf("listening on %s, signing key %s", *addr, key.Public())
	srv := &http.Server{Addr: *addr, Handler: logMW(mux), ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

type statusW struct {
	http.ResponseWriter
	code int
	n    int64
}

func (w *statusW) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusW) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.n += int64(n)
	return n, err
}
func (w *statusW) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logMW(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		sw := &statusW{ResponseWriter: w, code: 200}
		h.ServeHTTP(sw, r)
		auth := "none"
		if a := r.Header.Get("Authorization"); a != "" {
			auth, _, _ = strings.Cut(a, " ")
		}
		log.Printf("%s %s auth=%s range=%q ua=%q -> %d %dB %s", r.Method, r.URL.Path, auth,
			r.Header.Get("Range"), firstWord(r.UserAgent()), sw.code, sw.n, time.Since(t).Round(time.Microsecond))
	})
}

func firstWord(s string) string { f, _, _ := strings.Cut(s, " "); return f }

var errBadCreds = errors.New("invalid credentials")

// authenticate accepts Basic (any user, password = token; what netrc produces), Bearer with a
// raw bob token, or Bearer with an OCI token issued by /oci/token. No header = anonymous.
func (s *server) authenticate(r *http.Request) (*Principal, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return &Principal{Name: "anonymous", Anon: true}, nil
	}
	kind, cred, _ := strings.Cut(h, " ")
	var secret string
	switch strings.ToLower(kind) {
	case "basic":
		raw, err := base64.StdEncoding.DecodeString(cred)
		if err != nil {
			return nil, errBadCreds
		}
		_, pw, ok := strings.Cut(string(raw), ":")
		if !ok {
			return nil, errBadCreds
		}
		secret = pw
	case "bearer":
		if p, ok := s.verifyOCIToken(cred); ok {
			return p, nil
		}
		secret = cred
	default:
		return nil, errBadCreds
	}
	sum := sha256.Sum256([]byte(secret))
	t, ok := s.tokens[hex.EncodeToString(sum[:])]
	if !ok {
		return nil, errBadCreds
	}
	return &Principal{Name: t.Name, Projects: t.Projects}, nil
}

// whoami is what the bob CLI calls before running Nix (Nix hides 401s, S0b).
func (s *server) whoami(w http.ResponseWriter, r *http.Request) {
	p, err := s.authenticate(r)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, `{"error":"unauthenticated","message":"token is invalid or expired; run 'bob login'"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"name": p.Name, "anonymous": p.Anon, "projects": p.Projects,
		"nix": map[string]any{"substituter": s.publicURL + "/nix", "publicKey": s.key.Public().String()}})
}

// --- OCI bearer tokens: HMAC-signed claims, opaque to clients ------------------------------

type ociClaims struct {
	Sub    string              `json:"sub"`
	Exp    int64               `json:"exp"`
	Access map[string][]string `json:"acc"` // repo -> actions
	Projs  []string            `json:"prj,omitempty"`
}

func (s *server) signOCIToken(c ociClaims) string {
	b, _ := json.Marshal(c)
	p := base64.RawURLEncoding.EncodeToString(b)
	m := hmac.New(sha256.New, s.ociSecret)
	m.Write([]byte("bob-oci-v1." + p))
	return "bob-oci-v1." + p + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *server) verifyOCIToken(t string) (*Principal, bool) {
	rest, ok := strings.CutPrefix(t, "bob-oci-v1.")
	if !ok {
		return nil, false
	}
	p, sig, ok := strings.Cut(rest, ".")
	if !ok {
		return nil, false
	}
	m := hmac.New(sha256.New, s.ociSecret)
	m.Write([]byte("bob-oci-v1." + p))
	want := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return nil, false
	}
	var c ociClaims
	if json.Unmarshal(b, &c) != nil || time.Now().Unix() > c.Exp {
		return nil, false
	}
	return &Principal{Name: c.Sub, Projects: c.Projs, Anon: c.Sub == "anonymous"}, true
}

func ctxT(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 30*time.Second)
}
