package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// maxName is the longest branch, tag or repository name an event may carry (SPEC §6.9.1).
const maxName = 255

// ErrBaseURL is returned by every constructor for a base URL that is not https.
var ErrBaseURL = errors.New("forge: base URL must be https")

// CheckBaseURL returns the base URL without a trailing slash, or ErrBaseURL: credentials never
// travel over plain HTTP (S16-8).
func CheckBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%w: %q", ErrBaseURL, raw)
	}
	return strings.TrimSuffix(raw, "/"), nil
}

// HTTPClient is the client of every adapter: bounded, and it never follows a redirect to
// another scheme (a redirect to http would carry the credential in clear).
func HTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("forge: too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("forge: refusing a redirect away from https")
		}
		return nil
	}}
}

// ValidSHA reports a lowercase hex object id (SHA-1 or SHA-256 object format).
func ValidSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// IsZeroSHA reports the all-zero object id forges send for created or deleted refs.
func IsZeroSHA(s string) bool { return s != "" && strings.Trim(s, "0") == "" }

// SplitRef classifies a full ref: kind is "branch", "tag" or "" (anything else).
func SplitRef(ref string) (kind, name string) {
	if n, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return "branch", n
	}
	if n, ok := strings.CutPrefix(ref, "refs/tags/"); ok {
		return "tag", n
	}
	return "", ref
}

// ValidRefName reports a branch or tag name git accepts (git check-ref-format, plus no
// leading '-' so that a name can never read as an option): 1..255 bytes of valid UTF-8, no
// control characters, space, ~ ^ : ? * [ \, no "..", "@{", "//", no component starting with
// '.' or ending with ".lock", not ending with '/' or '.'.
func ValidRefName(n string) bool {
	if n == "" || len(n) > maxName || !utf8.ValidString(n) || n == "@" || strings.HasPrefix(n, "-") {
		return false
	}
	if strings.ContainsAny(n, " ~^:?*[\\\x7f") || strings.Contains(n, "..") || strings.Contains(n, "@{") ||
		strings.Contains(n, "//") || strings.HasSuffix(n, "/") || strings.HasSuffix(n, ".") || strings.HasPrefix(n, "/") {
		return false
	}
	for i := 0; i < len(n); i++ {
		if n[i] < 0x20 {
			return false
		}
	}
	for _, c := range strings.Split(n, "/") {
		if strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".lock") {
			return false
		}
	}
	return true
}

// Truncate shortens s to at most n runes (forge description limits).
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// VerifyHexHMAC checks hex(HMAC-SHA256(secret, body)) == sig in constant time, after removing
// prefix ("sha256=" for X-Hub-Signature-256). An empty secret never verifies.
func VerifyHexHMAC(secret Secret, body []byte, sig, prefix string) error {
	if secret.IsZero() {
		return fmt.Errorf("%w: no secret configured", ErrSignature)
	}
	if sig == "" {
		return fmt.Errorf("%w: header missing", ErrSignature)
	}
	hexSig, ok := strings.CutPrefix(sig, prefix)
	if !ok {
		return fmt.Errorf("%w: unexpected format", ErrSignature)
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return fmt.Errorf("%w: not hex", ErrSignature)
	}
	m := hmac.New(sha256.New, []byte(secret.Reveal()))
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return fmt.Errorf("%w: mismatch", ErrSignature)
	}
	return nil
}

// CheckSize refuses a body over MaxWebhookBody.
func CheckSize(body []byte) error {
	if len(body) > MaxWebhookBody {
		return ErrTooLarge
	}
	return nil
}

// checkCloneURL requires an https URL with a host and without credentials.
func checkCloneURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("clone URL is not an https URL without credentials: %q", raw)
	}
	return nil
}

// CheckEvent validates the invariants every normalized event satisfies before it leaves an
// adapter (SPEC §6.9.1): repo id > 0 and a name, https clone URL, SHAs of 40 or 64 lowercase
// hex digits, ref names git accepts and ≤ 255 bytes, PR number > 0. Webhook content is hostile
// input even after its signature verified (R-PIPE-008): a failure is ErrPayload.
func CheckEvent(e Event) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrPayload, fmt.Sprintf(format, a...))
	}
	switch e.Type {
	case EventIgnored:
		if e.IgnoredReason == "" {
			return bad("ignored event without a reason")
		}
		return nil
	case EventPing:
		return nil
	case EventPush, EventTag, EventPullRequest:
	default:
		return bad("unknown event type %q", e.Type)
	}
	if e.Repo.ID <= 0 || e.Repo.FullName == "" || len(e.Repo.FullName) > maxName {
		return bad("repository id or name missing")
	}
	if err := checkCloneURL(e.Repo.CloneURL); err != nil {
		return bad("%v", err)
	}
	n := 0
	for _, set := range []bool{e.Push != nil, e.Tag != nil, e.PullRequest != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return bad("%s event must carry exactly one of push, tag, pull request", e.Type)
	}
	switch e.Type {
	case EventPush:
		p := e.Push
		if p == nil || !ValidRefName(p.Branch) {
			return bad("push branch name invalid")
		}
		if p.Before != "" && !ValidSHA(p.Before) {
			return bad("push before is not a commit id")
		}
		if !ValidSHA(p.After) || p.Deleted != IsZeroSHA(p.After) {
			return bad("push after is not a commit id")
		}
	case EventTag:
		t := e.Tag
		if t == nil || !ValidRefName(t.Name) {
			return bad("tag name invalid")
		}
		if !t.Deleted && (!ValidSHA(t.Commit) || IsZeroSHA(t.Commit)) {
			return bad("tag commit is not a commit id")
		}
		if t.Deleted && t.Commit != "" {
			return bad("deleted tag with a commit")
		}
	case EventPullRequest:
		pr := e.PullRequest
		if pr == nil || pr.Number <= 0 {
			return bad("pull request without number")
		}
		switch pr.Action {
		case PROpened, PRSynchronized, PRReopened, PRClosed:
		default:
			return bad("pull request action %q", pr.Action)
		}
		if !ValidSHA(pr.HeadSHA) || IsZeroSHA(pr.HeadSHA) {
			return bad("pull request head is not a commit id")
		}
		if !ValidRefName(pr.BaseBranch) || (pr.HeadBranch != "" && !ValidRefName(pr.HeadBranch)) {
			return bad("pull request branch name invalid")
		}
		if len(pr.Title) > 64<<10 || len(pr.HeadRepo) > maxName {
			return bad("pull request title or head repository too long")
		}
		if want1, want2 := fmt.Sprintf("refs/pull/%d/head", pr.Number), fmt.Sprintf("refs/merge-requests/%d/head", pr.Number); pr.FetchRef != want1 && pr.FetchRef != want2 {
			return bad("pull request fetch ref %q", pr.FetchRef)
		}
	}
	if len(e.Sender) > maxName || len(e.DeliveryID) > maxName {
		return bad("sender or delivery id too long")
	}
	return nil
}

// HTTPError maps a forge API answer to the sentinel errors: 401/403 ErrAuth, 404 ErrNotFound.
func HTTPError(op string, code int, err error) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: %w (HTTP %d): %v", op, ErrAuth, code, err)
	case http.StatusNotFound:
		return fmt.Errorf("%s: %w (HTTP %d): %v", op, ErrNotFound, code, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// Description is s.Description fitted to a forge limit of n runes. A cancelled status says so
// in the text, since only GitLab has a cancelled state (SPEC §6.9.2).
func Description(s Status, n int) string {
	d := s.Description
	if s.State == StateCancelled && !strings.Contains(strings.ToLower(d), "cancel") {
		d = strings.TrimSuffix("cancelled: "+d, ": ")
	}
	return Truncate(d, n)
}
