package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// ZeroSHA is what forges send as before/after for created/deleted refs (SHA-1 repos).
const ZeroSHA = "0000000000000000000000000000000000000000"

var shaRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// IsZeroSHA reports an all-zero object id (SHA-1 or SHA-256 object format).
func IsZeroSHA(s string) bool { return s != "" && strings.Trim(s, "0") == "" }

// ValidSHA reports a lowercase hex object id (SHA-1 or SHA-256 object format).
func ValidSHA(s string) bool { return shaRE.MatchString(s) }

// VerifyHexHMACSHA256 checks hex(HMAC-SHA256(secret, body)) == sig in constant time.
// prefix is stripped from sig first ("sha256=" for GitHub's X-Hub-Signature-256).
func VerifyHexHMACSHA256(secret []byte, body []byte, sig, prefix string) error {
	if len(secret) == 0 {
		return fmt.Errorf("%w: no secret configured", ErrSignature)
	}
	if sig == "" {
		return fmt.Errorf("%w: header missing", ErrSignature)
	}
	if prefix != "" {
		if !strings.HasPrefix(sig, prefix) {
			return fmt.Errorf("%w: unexpected format", ErrSignature)
		}
		sig = sig[len(prefix):]
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("%w: not hex", ErrSignature)
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return fmt.Errorf("%w: mismatch", ErrSignature)
	}
	return nil
}

// SplitRef classifies a full ref. kind is "branch", "tag" or "".
func SplitRef(ref string) (kind, name string) {
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return "branch", strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		return "tag", strings.TrimPrefix(ref, "refs/tags/")
	}
	return "", ref
}

// Truncate shortens s to at most n runes.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// CheckEvent validates invariants every adapter must guarantee before an Event leaves it.
// Hostile input (R-SEC-004): ids, SHAs and names are checked, not trusted.
func CheckEvent(e Event) error {
	bad := func(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrPayload}, a...)...) }
	if e.Type == EventIgnored || e.Type == EventPing {
		return nil
	}
	if e.Repo.ID <= 0 || e.Repo.FullName == "" {
		return bad("repository id/name missing")
	}
	if !strings.HasPrefix(e.Repo.CloneURL, "https://") {
		return bad("clone URL is not https: %q", e.Repo.CloneURL)
	}
	switch e.Type {
	case EventPush:
		p := e.Push
		if p == nil || p.Branch == "" || len(p.Branch) > 255 {
			return bad("push without branch")
		}
		if !p.Deleted && !ValidSHA(p.After) {
			return bad("push after SHA invalid: %q", p.After)
		}
	case EventTag:
		t := e.Tag
		if t == nil || t.Name == "" || len(t.Name) > 255 {
			return bad("tag without name")
		}
		if !t.Deleted && !ValidSHA(t.Commit) {
			return bad("tag commit SHA invalid: %q", t.Commit)
		}
	case EventPullRequest:
		pr := e.PullRequest
		if pr == nil || pr.Number <= 0 {
			return bad("pull request without number")
		}
		if !ValidSHA(pr.HeadSHA) {
			return bad("pull request head SHA invalid: %q", pr.HeadSHA)
		}
	default:
		return bad("unknown event type %q", e.Type)
	}
	return nil
}
