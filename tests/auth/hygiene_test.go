package auth

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/megakuul/bob/tests/auth/authenv"
	"github.com/megakuul/bob/tests/parity"
)

var (
	argon2Hash = regexp.MustCompile(`\$argon2id\$[A-Za-z0-9$=,+/]+`)
	throttle   = regexp.MustCompile(`^too many login attempts, try again in \d+s$`)
)

// TestRAUTH151CredentialHygiene covers cases 1–3 of R-AUTH-151 (case 4, the log scan over the
// whole suite, is TestRAUTH151LogsStaySecretFree in zz_logs_test.go).
func TestRAUTH151CredentialHygiene(t *testing.T) {
	parity.Covers(t, "R-AUTH-151")

	t.Run("argon2id-hashes", func(t *testing.T) {
		s := authenv.Start(t, 1)
		for _, l := range []string{"hash-a", "hash-b"} {
			s.NewUser(t, l, authenv.Password)
		}
		hashes := s.ObjectMatches(t, "bob-control", argon2Hash)
		if len(hashes) < 2 {
			t.Fatalf("found %d argon2id hashes in the control bucket for 2 local users (+ the bootstrap admin)", len(hashes))
		}
		for _, h := range hashes {
			if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
				t.Errorf("stored password hash %q does not start with $argon2id$v=19$m=65536,t=3,p=4$", h)
			}
			if parts := strings.Split(h, "$"); len(parts) != 6 { // "", argon2id, v, params, salt, key
				t.Errorf("stored password hash %q is not a PHC string with salt and key", h)
			}
		}
		if hits, _ := s.ObjectsContaining(t, "bob-control", authenv.Password); len(hits) > 0 {
			t.Errorf("control bucket objects contain a plaintext password: %v", hits)
		}
		// a password of 11 bytes is refused, 12 is accepted (SPEC §6.3.1)
		short := s.Admin().Call(t, authenv.UserSvc, "CreateUser", map[string]any{
			"user": map[string]any{"login": "hash-short"}, "initialPassword": "elevenbytes"})
		if short.Code != "invalid_argument" {
			t.Errorf("11-byte password: want InvalidArgument, got %s", short)
		}
	})

	t.Run("login-throttle-per-name", func(t *testing.T) {
		s := authenv.Start(t, 1)
		s.NewUser(t, "throttled", authenv.Password)
		for i := 1; i <= 5; i++ {
			_, r := s.Login(t, "throttled", fmt.Sprintf("wrong-password-%d", i))
			if r.Code != "unauthenticated" || r.Message != "invalid login or password" {
				t.Fatalf("failure %d: want Unauthenticated 'invalid login or password', got %s", i, r)
			}
		}
		_, r := s.Login(t, "throttled", "wrong-password-6")
		if r.Code != "resource_exhausted" || r.Detail != "BOB-API-4001" || !throttle.MatchString(r.Message) {
			t.Errorf("6th wrong password: want ResourceExhausted 'too many login attempts, try again in <n>s', got %s", r)
		}
		if r.HTTP != 429 {
			t.Errorf("6th wrong password: HTTP %d, want 429", r.HTTP)
		}
		if _, ok := s.Login(t, "throttled", authenv.Password); ok.OK() {
			t.Errorf("the correct password was accepted while the login name is throttled")
		}
		// an unknown login fails with the same message as a wrong password
		if _, r := s.Login(t, "no-such-user", authenv.Password); r.Code != "unauthenticated" || r.Message != "invalid login or password" {
			t.Errorf("unknown login: want the same answer as a wrong password, got %s", r)
		}
	})

	t.Run("login-throttle-per-ip", func(t *testing.T) {
		s := authenv.Start(t, 1)
		for i := 1; i <= 30; i++ {
			_, r := s.Login(t, fmt.Sprintf("ghost-%02d", i), "whatever-password-1")
			if r.Code != "unauthenticated" {
				t.Fatalf("failure %d from one IP: want Unauthenticated, got %s", i, r)
			}
		}
		_, r := s.Login(t, "ghost-31", "whatever-password-1")
		if r.Code != "resource_exhausted" || !throttle.MatchString(r.Message) {
			t.Errorf("31st failure from one IP within 5 min: want ResourceExhausted, got %s", r)
		}
	})

	t.Run("csrf", func(t *testing.T) {
		s := authenv.Shared(t)
		s.NewAdmin(t, "csrf-admin", authenv.Password)
		c := s.MustLogin(t, "csrf-admin", authenv.Password)
		body := map[string]any{"project": map[string]any{"id": "csrf-probe"}}
		c.NoCSRF = true
		r := c.Call(t, "bob.project.v1.ProjectService", "CreateProject", body)
		if r.Code != "unauthenticated" || r.Detail != "BOB-API-2001" {
			t.Errorf("cookie-only POST without Bob-Csrf: want Unauthenticated, got %s", r)
		}
		c.NoCSRF = false
		if r := c.Call(t, "bob.project.v1.ProjectService", "CreateProject", body); r.Code == "unauthenticated" || r.Code == "permission_denied" {
			t.Errorf("cookie with Bob-Csrf: 1 as admin must be authorized (the handler may refuse the empty project), got %s", r)
		}
		// Connect GET with only a cookie is refused
		code, _, raw := (&authenv.Client{Server: s, Cookie: c.Cookie, NoCSRF: true}).Get(t,
			"/api/bob.auth.v1.AuthService/WhoAmI?connect=v1&encoding=json&message=%7B%7D", nil)
		if code != 401 {
			t.Errorf("Connect GET with only a cookie: HTTP %d %.200s, want 401", code, raw)
		}
		// the server sends no CORS headers at all
		code, h, _ := s.Anon().Get(t, "/api/bob.auth.v1.AuthService/GetServerInfo", map[string]string{"Origin": "https://evil.example"})
		_ = code
		for k := range h {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("the API answered with the CORS header %s", k)
			}
		}
	})
}
