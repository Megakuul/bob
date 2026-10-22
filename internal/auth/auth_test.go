package auth

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/megakuul/bob/internal/gen/bob/auth/v1"
)

// TestLoginPasswordEmptyBody: a missing login or password is InvalidArgument (A107), decided
// before any store access.
func TestLoginPasswordEmptyBody(t *testing.T) {
	for _, m := range []*authv1.LoginPasswordRequest{{}, {Login: "a"}, {Password: "x"}} {
		_, err := authService{}.LoginPassword(context.Background(), connect.NewRequest(m))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%+v: got %v, want InvalidArgument", m, err)
		}
	}
}

func TestValidRedirect(t *testing.T) {
	for _, c := range []struct {
		uri string
		ok  bool
	}{
		{"http://127.0.0.1:4000/callback", true},
		{"http://127.0.0.1/callback", true},
		{"http://[::1]:4000/callback", true},
		{"http://evil.example/callback", false},
		{"http://localhost:4000/callback", false},
		{"http://127.0.0.1:4000/other", false},
		{"https://127.0.0.1:4000/callback", false},
		{"http://127.0.0.1.evil.example/callback", false},
		{"http://user@127.0.0.1:80/callback", false},
		{"http://127.0.0.1:4000/callback?x=1", false},
	} {
		if got := validRedirect(c.uri); got != c.ok {
			t.Errorf("validRedirect(%q) = %v, want %v", c.uri, got, c.ok)
		}
	}
}

func TestUserCode(t *testing.T) {
	for range 100 {
		c := newUserCode()
		if normalizeUserCode(c) != c {
			t.Fatalf("user code %q does not normalise to itself", c)
		}
	}
	if got := normalizeUserCode("bcdf ghjk"); got != "BCDF-GHJK" {
		t.Errorf("normalizeUserCode = %q", got)
	}
	if got := normalizeUserCode("ABCD-EFGH"); got != "" {
		t.Errorf("vowels accepted: %q", got)
	}
}

func TestPasswordHash(t *testing.T) {
	h := newHasher()
	phc, err := h.hash(context.Background(), Secret("correct-horse-battery-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=65536,t=3,p=4$") || len(strings.Split(phc, "$")) != 6 {
		t.Fatalf("PHC string %q", phc)
	}
	for pw, want := range map[string]bool{"correct-horse-battery-1": true, "correct-horse-battery-2": false} {
		ok, err := h.verify(context.Background(), phc, Secret(pw))
		if err != nil || ok != want {
			t.Errorf("verify(%q) = %v, %v; want %v", pw, ok, err, want)
		}
	}
	if ok, err := h.verify(context.Background(), "", Secret("x")); ok || err != nil {
		t.Errorf("unknown login: %v %v", ok, err)
	}
}

func TestThrottle(t *testing.T) {
	th, now := newThrottle(), time.Now()
	for i := range 5 {
		if err := th.check("a", "1.1.1.1", now); err != nil {
			t.Fatalf("attempt %d throttled: %v", i+1, err)
		}
		th.fail("a", "1.1.1.1", now)
	}
	if err := th.check("a", "1.1.1.1", now); err == nil || !strings.Contains(err.Error(), "too many login attempts, try again in 300s") {
		t.Errorf("6th attempt: %v", err)
	}
	if err := th.check("a", "1.1.1.1", now.Add(5*time.Minute)); err != nil {
		t.Errorf("after the window: %v", err)
	}
	for i := range 30 {
		th.fail(fmt.Sprint("n", i), "2.2.2.2", now)
	}
	if err := th.check("fresh", "2.2.2.2", now); err == nil {
		t.Errorf("31st attempt from one IP not throttled")
	}
}

func TestSecretRedacts(t *testing.T) {
	s := Secret("bob_at_secret")
	var b strings.Builder
	slog.New(slog.NewTextHandler(&b, nil)).Info("x", "tok", s)
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%#v %v %s", s, s, s), b.String()} {
		if strings.Contains(out, "bob_at_secret") {
			t.Errorf("secret printed: %s", out)
		}
	}
}

func TestIDsAndTokens(t *testing.T) {
	id := newID(time.Now())
	if len(id) != 26 || !userRE.MatchString("u"+id) {
		t.Errorf("id %q", id)
	}
	if tok := newToken(PrefixAccess).reveal(); len(tok) != len(PrefixAccess)+52 {
		t.Errorf("token %q", tok)
	}
	for _, n := range []string{"/", "/p/x?y=1"} {
		if safeNext(n) != n {
			t.Errorf("safeNext(%q)", n)
		}
	}
	for _, n := range []string{"//evil.example", "https://evil.example", "/\\evil"} {
		if safeNext(n) != "/" {
			t.Errorf("safeNext(%q) kept", n)
		}
	}
}
