package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/megakuul/bob/internal/api"
)

// Password hashing (SPEC §6.3.1, R-AUTH-151): argon2id m = 64 MiB, t = 3, p = 4, 16-byte salt,
// 32-byte key, PHC string; at most 2 computations at once per orchestrator, queueing at most
// 5 s.
const (
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 4
	argonSalt    = 16
	argonKey     = 32

	minPassword = 12
	maxPassword = 256

	hashSlots   = 2
	hashTimeout = 5 * time.Second
)

var phcPrefix = fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$", argon2.Version, argonMemory, argonTime, argonThreads)

// hasher bounds concurrent argon2id computations.
type hasher struct{ slots chan struct{} }

func newHasher() *hasher { return &hasher{slots: make(chan struct{}, hashSlots)} }

func (h *hasher) acquire(ctx context.Context) error {
	t := time.NewTimer(hashTimeout)
	defer t.Stop()
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-t.C:
		return api.Unavailable("the server is busy hashing passwords, try again")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *hasher) release() { <-h.slots }

// checkPasswordPolicy enforces the length rule (12–256 bytes, nothing else).
func checkPasswordPolicy(field string, pw Secret) error {
	if n := len(pw.reveal()); n < minPassword || n > maxPassword {
		return api.InvalidArgument(field, fmt.Sprintf("a password must have %d to %d bytes", minPassword, maxPassword))
	}
	return nil
}

// hash returns the PHC string of pw.
func (h *hasher) hash(ctx context.Context, pw Secret) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	salt := make([]byte, argonSalt)
	_, _ = rand.Read(salt)
	key := argon2.IDKey([]byte(pw.reveal()), salt, argonTime, argonMemory, argonThreads, argonKey)
	enc := base64.RawStdEncoding
	return phcPrefix + enc.EncodeToString(salt) + "$" + enc.EncodeToString(key), nil
}

// errBadHash is a stored hash bob cannot parse; the login fails like a wrong password.
var errBadHash = errors.New("stored password hash is not an argon2id PHC string")

// verify compares pw with a PHC string. phc "" (unknown login) still costs one hash, so the
// answer time does not reveal whether a login exists.
func (h *hasher) verify(ctx context.Context, phc string, pw Secret) (bool, error) {
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	var m, t uint32
	var p uint8
	salt, want := make([]byte, argonSalt), make([]byte, argonKey)
	ok := false
	if parts := strings.Split(phc, "$"); len(parts) == 6 && parts[1] == "argon2id" {
		_, err1 := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p)
		s, err2 := base64.RawStdEncoding.DecodeString(parts[4])
		k, err3 := base64.RawStdEncoding.DecodeString(parts[5])
		if err1 == nil && err2 == nil && err3 == nil && parts[2] == fmt.Sprintf("v=%d", argon2.Version) &&
			m > 0 && m <= 1<<20 && t > 0 && t <= 16 && p > 0 && len(k) > 0 && len(k) <= 64 {
			salt, want, ok = s, k, true
		}
	}
	if !ok {
		m, t, p = argonMemory, argonTime, argonThreads
	}
	got := argon2.IDKey([]byte(pw.reveal()), salt, t, m, p, uint32(len(want)))
	if !ok {
		if phc != "" {
			return false, errBadHash
		}
		return false, nil
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// Login throttle (SPEC §6.3.1): per login name 5 failures / 5 min, per client IP 30 / 5 min,
// in memory per orchestrator.
const (
	throttleWindow  = 5 * time.Minute
	throttlePerName = 5
	throttlePerIP   = 30
	throttleMaxKeys = 100_000
)

type throttle struct {
	mu       sync.Mutex
	failures map[string][]time.Time // "n:<login>" | "i:<ip>" → failure times in the window
}

func newThrottle() *throttle { return &throttle{failures: map[string][]time.Time{}} }

func (t *throttle) recent(key string, now time.Time) []time.Time {
	l := t.failures[key]
	i := 0
	for i < len(l) && now.Sub(l[i]) >= throttleWindow {
		i++
	}
	l = l[i:]
	if len(l) == 0 {
		delete(t.failures, key)
	} else {
		t.failures[key] = l
	}
	return l
}

// check returns ResourceExhausted when login or ip has used up its failures.
func (t *throttle) check(login, ip string, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var wait time.Duration
	for _, k := range []struct {
		key   string
		limit int
	}{{"n:" + login, throttlePerName}, {"i:" + ip, throttlePerIP}} {
		if l := t.recent(k.key, now); len(l) >= k.limit {
			wait = max(wait, l[len(l)-k.limit].Add(throttleWindow).Sub(now))
		}
	}
	if wait > 0 {
		return api.ResourceExhausted(fmt.Sprintf("too many login attempts, try again in %ds", int(wait.Seconds()+0.999)))
	}
	return nil
}

// fail records one failed attempt.
func (t *throttle) fail(login, ip string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.failures) > throttleMaxKeys {
		for k := range t.failures {
			t.recent(k, now)
		}
	}
	for _, k := range []string{"n:" + login, "i:" + ip} {
		t.failures[k] = append(t.recent(k, now), now)
	}
}
