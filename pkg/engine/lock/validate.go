package lock

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Everything in bob.lock is hostile input (SPEC §03.7, AGENTS 5.4): these checks bound and
// validate every name, version, URL and hash before use.

const (
	maxLockBytes = 64 << 20 // one bob.lock
	maxName      = 512      // names, paths, versions, keys
	maxURL       = 4096
)

var (
	hex40Re    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64Re    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	goSemverRe = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	semverRe   = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	goVerRe    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))?((rc|beta)[1-9][0-9]*)?$`)
	goElemRe   = regexp.MustCompile(`^[A-Za-z0-9._~+-]+$`)
	crateRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	featureRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_+./:-]{0,127}$`)
	npmNameRe  = regexp.MustCompile(`^(@[a-z0-9-~][a-z0-9-._~]*/)?[a-z0-9-~][a-z0-9-._~]*$`)
	opamNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_+-]*$`)
	opamVerRe  = regexp.MustCompile(`^[A-Za-z0-9_+.~-]+$`)
	npmWordRe  = regexp.MustCompile(`^!?[A-Za-z0-9_.-]{1,64}$`)
	storeName  = regexp.MustCompile(`^[A-Za-z0-9+_?=-][A-Za-z0-9+._?=-]*$`)
	channelRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// sriSizes are the digest sizes of the SRI algorithms bob.lock may hold.
var sriSizes = map[string]int{"sha1": 20, "sha256": 32, "sha512": 64}

// checkSRI validates an SRI hash with one of algos ("sha256", …).
func checkSRI(what, s string, algos ...string) error {
	algo, b64, ok := strings.Cut(s, "-")
	size, known := sriSizes[algo]
	if !ok || !known || !contains(algos, algo) {
		return fmt.Errorf("%s %q is not an SRI hash (%s-<base64>)", what, clip(s), strings.Join(algos, "|"))
	}
	d, err := base64.StdEncoding.Strict().DecodeString(b64)
	if err != nil || len(d) != size {
		return fmt.Errorf("%s %q is not a valid %s SRI hash", what, clip(s), algo)
	}
	return nil
}

// checkH1 validates a Go "h1:" hash (sha256, base64).
func checkH1(what, s string) error {
	b64, ok := strings.CutPrefix(s, "h1:")
	if d, err := base64.StdEncoding.Strict().DecodeString(b64); !ok || err != nil || len(d) != 32 {
		return fmt.Errorf("%s %q is not an h1: hash", what, clip(s))
	}
	return nil
}

// checkInputs validates a section's "inputs" (sha256 hex, R-FW-004).
func checkInputs(s string) error {
	if !hex64Re.MatchString(s) {
		return fmt.Errorf("inputs %q is not a sha256 hex digest", clip(s))
	}
	return nil
}

// checkHTTPURL validates an http(s) URL; prefix is an allowed scheme prefix ("sparse+").
func checkHTTPURL(what, s, prefix string) error {
	raw, ok := strings.CutPrefix(s, prefix)
	if !ok || len(s) > maxURL || !utf8.ValidString(s) {
		return fmt.Errorf("%s %q is not a valid URL", what, clip(s))
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return fmt.Errorf("%s %q is not an http(s) URL", what, clip(s))
	}
	return nil
}

// checkGoPath validates a Go module path (cmd/go's rules, simplified: no escaping of the
// repository, a dotted first element, safe characters only).
func checkGoPath(p string) error {
	if p == "" || len(p) > maxName {
		return fmt.Errorf("module path %q has an invalid length", clip(p))
	}
	elems := strings.Split(p, "/")
	if !strings.Contains(elems[0], ".") || strings.HasPrefix(elems[0], "-") {
		return fmt.Errorf("module path %q does not start with a domain name", clip(p))
	}
	for _, e := range elems {
		if e == "" || e == "." || e == ".." || !goElemRe.MatchString(e) || strings.HasPrefix(e, ".") || strings.HasSuffix(e, ".") {
			return fmt.Errorf("module path %q has an invalid element %q", clip(p), clip(e))
		}
	}
	return nil
}

// checkRelDir validates a clean, repo-relative directory ("web", "packages/ui", ".").
func checkRelDir(what, d string) error {
	if d == "" || len(d) > maxName || !utf8.ValidString(d) || strings.ContainsAny(d, "\\\x00") ||
		path.IsAbs(d) || path.Clean(d) != d || d == ".." || strings.HasPrefix(d, "../") {
		return fmt.Errorf("%s %q is not a clean repository-relative path", what, clip(d))
	}
	return nil
}

// checkText validates a free-form map key or value (instance keys, targets): bounded UTF-8.
func checkText(what, s string) error {
	if s == "" || len(s) > maxName || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\n\r") {
		return fmt.Errorf("%s %q is invalid", what, clip(s))
	}
	return nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// clip shortens hostile strings for error messages.
func clip(s string) string {
	if len(s) > 80 {
		return strings.ToValidUTF8(s[:80], "") + "…"
	}
	return s
}
