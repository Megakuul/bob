package authenv

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// These tests check the harness itself against tiny fakes; they need no server.

func TestReplyParsing(t *testing.T) {
	detail := []byte{0x0a, 12}
	detail = append(detail, "BOB-API-3001"...)
	val := base64.RawStdEncoding.EncodeToString(detail)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/p.S/Err", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":"not_found","message":"build not found","details":[{"type":"bob.common.v1.ErrorDetail","value":"` + val + `"}]}`))
	})
	mux.HandleFunc("/api/p.S/Debug", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"code":"invalid_argument","message":"m","details":[{"type":"x","value":"AAAA","debug":{"code":"BOB-API-1001","field":"role.id"}}]}`))
	})
	mux.HandleFunc("/api/p.S/Ok", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Connect-Protocol-Version") != "1" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`{"login":"alice"}`))
	})
	mux.HandleFunc("/api/p.S/Stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/connect+json")
		end := []byte(`{"error":{"code":"permission_denied","message":"nope"}}`)
		env := make([]byte, 5, 5+len(end))
		env[0] = 2
		binary.BigEndian.PutUint32(env[1:], uint32(len(end)))
		_, _ = w.Write(append(env, end...))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := &Client{Server: &Server{URL: srv.URL}, Token: "tok"}

	if r, err := c.Do("p.S", "Err", map[string]any{}, false); err != nil || r.Code != "not_found" || r.Detail != "BOB-API-3001" || r.Details != 1 || r.HTTP != 404 {
		t.Errorf("Err: %v %v", r, err)
	}
	if r, _ := c.Do("p.S", "Debug", map[string]any{}, false); r.Detail != "BOB-API-1001" || r.Field != "role.id" {
		t.Errorf("Debug: %v field %q", r, r.Field)
	}
	if r, _ := c.Do("p.S", "Ok", map[string]any{}, false); !r.OK() || Str(r.Body, "login") != "alice" {
		t.Errorf("Ok: %v", r)
	}
	if r, _ := c.Do("p.S", "Stream", map[string]any{}, true); r.Code != "permission_denied" || r.Message != "nope" {
		t.Errorf("Stream: %v", r)
	}
	if r, _ := (&Client{Server: &Server{URL: srv.URL}}).Do("p.S", "Ok", nil, false); r.OK() {
		t.Errorf("anonymous call reached the authorized handler")
	}
}

func TestSessionHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header; _, _ = w.Write([]byte("{}")) }))
	defer srv.Close()
	c := &Client{Server: &Server{URL: srv.URL}, Cookie: "bob_st_x"}
	if _, err := c.Do("p.S", "M", nil, false); err != nil {
		t.Fatal(err)
	}
	if got.Get("Bob-Csrf") != "1" || !strings.Contains(got.Get("Cookie"), "bob_session=bob_st_x") {
		t.Errorf("cookie client headers: %v", got)
	}
	c.NoCSRF = true
	_, _ = c.Do("p.S", "M", nil, false)
	if got.Get("Bob-Csrf") != "" {
		t.Errorf("NoCSRF still sends the header")
	}
}

// TestIDPFlow plays relying party against bobtest-idp: discovery, authorize with PKCE, code
// exchange, ID token signature via JWKS, nonce and claims, wrong nonce, code reuse.
func TestIDPFlow(t *testing.T) {
	p := TheIDP(t)
	pem, err := os.ReadFile(p.CertFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	var disc map[string]any
	resp, err := hc.Get(p.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("discovery over TLS with the harness CA: %v", err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&disc)
	resp.Body.Close()
	if disc["issuer"] != p.URL {
		t.Fatalf("issuer %v", disc["issuer"])
	}

	login := func(wrongNonce bool) (idToken, nonce string) {
		p.Login("sub-1", map[string]any{"groups": []string{"dev"}})
		p.WrongNonce(wrongNonce)
		defer p.WrongNonce(false)
		verifier, challenge := PKCE()
		nonce = "n-123"
		q := url.Values{"client_id": {p.ClientID}, "response_type": {"code"}, "redirect_uri": {"http://127.0.0.1:1/cb"},
			"state": {"st"}, "nonce": {nonce}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
		resp, err := hc.Get(p.URL + "/authorize?" + q.Encode())
		if err != nil || resp.StatusCode != 302 {
			t.Fatalf("authorize: %v %v", resp, err)
		}
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if loc.Query().Get("state") != "st" || loc.Query().Get("code") == "" {
			t.Fatalf("redirect %v", loc)
		}
		form := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "code_verifier": {verifier},
			"client_id": {p.ClientID}, "client_secret": {p.ClientSecret}, "redirect_uri": {"http://127.0.0.1:1/cb"}}
		resp, err = hc.PostForm(p.URL+"/token", form)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("token: %v %v", resp, err)
		}
		var tok map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&tok)
		resp.Body.Close()
		again, _ := hc.PostForm(p.URL+"/token", form)
		if again.StatusCode != 400 {
			t.Errorf("code reuse answered %d, want 400", again.StatusCode)
		}
		return tok["id_token"].(string), nonce
	}

	idt, nonce := login(false)
	parts := strings.Split(idt, ".")
	if len(parts) != 3 {
		t.Fatalf("id_token %q", idt)
	}
	var jwks struct {
		Keys []struct{ N, E string }
	}
	resp, _ = hc.Get(p.URL + "/jwks")
	_ = json.NewDecoder(resp.Body).Decode(&jwks)
	resp.Body.Close()
	n, _ := base64.RawURLEncoding.DecodeString(jwks.Keys[0].N)
	e, _ := base64.RawURLEncoding.DecodeString(jwks.Keys[0].E)
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("ID token signature does not verify with the JWKS: %v", err)
	}
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(cb, &claims)
	if claims["nonce"] != nonce || claims["iss"] != p.URL || claims["aud"] != p.ClientID || claims["sub"] != "sub-1" {
		t.Errorf("claims %v", claims)
	}
	if g, _ := claims["groups"].([]any); len(g) != 1 || g[0] != "dev" {
		t.Errorf("groups %v", claims["groups"])
	}
	if !p.SawPKCE() {
		t.Errorf("PKCE verification not recorded")
	}
	idt, nonce = login(true)
	cb, _ = base64.RawURLEncoding.DecodeString(strings.Split(idt, ".")[1])
	_ = json.Unmarshal(cb, &claims)
	if claims["nonce"] == nonce || claims["nonce"] == nil {
		t.Errorf("WrongNonce kept the nonce: %v", claims["nonce"])
	}
}

func TestBrowserCookies(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "bob_session", Value: "v", Secure: true, HttpOnly: true, Path: "/"})
		http.Redirect(w, r, "/where", http.StatusFound)
	})
	mux.HandleFunc("/where", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("bob_session")
		if err != nil {
			http.Error(w, "no cookie", 400)
			return
		}
		_, _ = w.Write([]byte(c.Value))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	b := NewBrowser()
	res := b.Get(t, srv.URL+"/set")
	if res.Status != 200 || res.Body != "v" || len(b.Chain) != 2 || b.Cookie(srv.URL, "bob_session") == nil {
		t.Errorf("browser: %+v chain %v", res, b.Chain)
	}
}
