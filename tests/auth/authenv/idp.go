package authenv

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/megakuul/bob/tests/serverenv"
)

// IDP is bobtest-idp: an in-process OIDC provider (authorization code flow, optional PKCE,
// RS256 ID tokens, discovery, JWKS, userinfo) served over TLS with a throw-away certificate.
// The stack trusts the certificate through SSL_CERT_FILE, which Start/Shared export before the
// server processes start.
type IDP struct {
	URL          string // issuer, https://127.0.0.1:<port>
	ClientID     string
	ClientSecret string
	CertFile     string

	key *rsa.PrivateKey
	ln  net.Listener
	srv *http.Server

	mu         sync.Mutex
	sub        string
	claims     map[string]any
	wrongNonce bool
	codes      map[string]*grant
	authorizes int
	sawPKCE    bool
}

type grant struct {
	sub       string
	claims    map[string]any
	nonce     string
	challenge string
	redirect  string
	used      bool
}

var (
	idpOnce sync.Once
	idp     *IDP
	idpErr  error
)

// TheIDP returns the process-wide bobtest-idp, starting it on first use.
func TheIDP(t testing.TB) *IDP {
	t.Helper()
	idpOnce.Do(func() { idp, idpErr = newIDP() })
	if idpErr != nil {
		t.Fatalf("harness error: bobtest-idp: %v", idpErr)
	}
	return idp
}

func newIDP() (*IDP, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	ck, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bobtest-idp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &ck.PublicKey, ck)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "bobtest-idp-")
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		return nil, err
	}
	port, err := serverenv.FreePort()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	p := &IDP{
		URL: "https://" + ln.Addr().String(), ClientID: "bob-server", ClientSecret: "idp-client-secret-0123456789",
		CertFile: certFile, key: key, ln: ln, codes: map[string]*grant{}, sub: "idp-user-1",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("/jwks", p.jwks)
	mux.HandleFunc("/authorize", p.authorize)
	mux.HandleFunc("/token", p.token)
	mux.HandleFunc("/userinfo", p.userinfo)
	p.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: ck}}}}
	go func() { _ = p.srv.ServeTLS(ln, "", "") }()
	return p, nil
}

// Login sets the identity the provider authenticates next (and until changed): the subject and
// the extra claims of the ID token, e.g. {"groups": []string{"dev"}}.
func (p *IDP) Login(sub string, claims map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sub, p.claims = sub, claims
}

// WrongNonce makes the provider put a nonce into the ID token that is not the one of the
// authorization request.
func (p *IDP) WrongNonce(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wrongNonce = on
}

// Authorizations is the number of authorization requests seen.
func (p *IDP) Authorizations() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authorizes
}

// SawPKCE reports whether a code exchange with a verified PKCE verifier happened.
func (p *IDP) SawPKCE() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sawPKCE
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (p *IDP) discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token",
		"jwks_uri": p.URL + "/jwks", "userinfo_endpoint": p.URL + "/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
	})
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (p *IDP) jwks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
		"n": b64(p.key.N.Bytes()), "e": b64(big.NewInt(int64(p.key.E)).Bytes()),
	}}})
}

func random() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return b64(b)
}

func (p *IDP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.ClientID || q.Get("response_type") != "code" || q.Get("redirect_uri") == "" {
		http.Error(w, "bobtest-idp: bad authorization request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.authorizes++
	code := random()
	g := &grant{sub: p.sub, claims: p.claims, nonce: q.Get("nonce"), redirect: q.Get("redirect_uri")}
	if q.Get("code_challenge_method") == "S256" {
		g.challenge = q.Get("code_challenge")
	}
	if p.wrongNonce {
		g.nonce = "wrong-nonce-" + random()
	}
	p.codes[code] = g
	p.mu.Unlock()
	u, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bobtest-idp: bad redirect_uri", http.StatusBadRequest)
		return
	}
	rq := u.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	u.RawQuery = rq.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (p *IDP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.ClientID || secret != p.ClientSecret {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	g := p.codes[r.PostForm.Get("code")]
	if g == nil || g.used || r.PostForm.Get("grant_type") != "authorization_code" {
		p.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	g.used = true
	if g.challenge != "" {
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if b64(sum[:]) != g.challenge {
			p.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]string{"error": "invalid_grant", "error_description": "PKCE verifier mismatch"})
			return
		}
		p.sawPKCE = true
	}
	p.mu.Unlock()
	now := time.Now()
	claims := map[string]any{"preferred_username": g.sub, "email": g.sub + "@bobtest.invalid", "name": "Test " + g.sub}
	for k, v := range g.claims {
		claims[k] = v
	}
	claims["iss"], claims["sub"], claims["aud"] = p.URL, g.sub, p.ClientID
	claims["iat"], claims["exp"] = now.Unix(), now.Add(10*time.Minute).Unix()
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	writeJSON(w, map[string]any{
		"access_token": "idp-at-" + random(), "token_type": "Bearer", "expires_in": 600,
		"id_token": p.sign(claims),
	})
}

func (p *IDP) userinfo(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	claims := map[string]any{"sub": p.sub, "preferred_username": p.sub}
	for k, v := range p.claims {
		claims[k] = v
	}
	writeJSON(w, claims)
}

func (p *IDP) sign(claims map[string]any) string {
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	cb, _ := json.Marshal(claims)
	in := b64(hb) + "." + b64(cb)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		return fmt.Sprintf("sign-error-%v", err)
	}
	return in + "." + b64(sig)
}

// RegisterProvider creates the provider on the server (admin API) with id and returns the
// provider id. require_mapping is as given.
func (p *IDP) RegisterProvider(t testing.TB, s *Server, id string, requireMapping bool) {
	t.Helper()
	s.Admin().Call(t, OidcSvc, "CreateOidcProvider", map[string]any{"provider": map[string]any{
		"id": id, "displayName": "bobtest-idp " + id, "issuerUrl": p.URL, "clientId": p.ClientID,
		"clientSecret": Secret(p.ClientSecret), "requireMapping": requireMapping,
	}}).Must(t, "CreateOidcProvider "+id)
}

// Map creates a claim → role mapping and returns its id.
func (s *Server) Map(t testing.TB, provider, claim, value, role, project string) string {
	t.Helper()
	r := s.Admin().Call(t, OidcSvc, "CreateOidcMapping", map[string]any{"mapping": map[string]any{
		"providerId": provider, "claim": claim, "value": value, "role": role, "project": project,
	}}).Must(t, "CreateOidcMapping")
	return Str(r.Body, "mapping.id")
}
