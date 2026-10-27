package translate

import (
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/megakuul/bob/pkg/bob/diag"
)

// RealiseFailure explains a failed Nix command that bob runs outside a build (a plain
// `nix-store --realise` of a nixpkgs toolchain, e.g. the evaluator's Go) from its stderr. what
// names the step for the user ("nixpkgs:go"). Nix text never reaches the message: the cause
// class comes first (the network or CA problem that made Nix fall back to building, a full
// disk, the build sandbox or bob's store, a package that failed to build), and the raw stderr
// goes to Raw for -v. Found by Q4 (A471): such a failure was reported as BOB-E-9004 "bob bug"
// with an empty -v.
func RealiseFailure(what, stderr string) *diag.Error {
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	e := classifyRealise(what, lines)
	// -v shows the warnings (the cache problems come first in Nix's output) and the end.
	raw := lines
	if len(raw) > 40 {
		var warns []string
		for _, l := range raw[:len(raw)-30] {
			if strings.HasPrefix(l, "warning: ") && len(warns) < 10 {
				warns = append(warns, l)
			}
		}
		raw = append(warns, raw[len(raw)-30:]...)
	}
	e.Raw = raw
	return e
}

var (
	reWarnDL = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`unable to download '([^']+)': (.*?)(?:; retrying in \d+ ms.*)?$`)
	})
	reTrustAnchor = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`error adding trust anchors from file: (\S+)`) })
	reStorePath   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`/nix/store/[0-9a-z]{32}-([^/'"\s]*)`) })
	reBuilderFail = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(?:builder for|Cannot build) '/nix/store/[0-9a-z]{32}-([^']+)\.drv'`)
	})
)

func classifyRealise(what string, lines []string) *diag.Error {
	text := strings.Join(lines, "\n")
	// A cache Nix could not reach comes first: it is why Nix then tried to build from source.
	for _, l := range lines {
		m := reWarnDL().FindStringSubmatch(l)
		if m == nil {
			continue
		}
		host := m[1]
		if u, err := url.Parse(m[1]); err == nil && u.Host != "" {
			host = u.Scheme + "://" + u.Host
		}
		e := newErr(cCacheUnreachable, map[string]string{"cache": host, "reason": netReason(m[2])})
		switch {
		case reTrustAnchor().MatchString(m[2]):
			e.Cause = "the CA bundle " + reTrustAnchor().FindStringSubmatch(m[2])[1] +
				" cannot be read (missing, unreadable or a dangling link), so " + host + " cannot be verified"
		case strings.Contains(m[2], "SSL"), strings.Contains(m[2], "certificate"):
			e.Cause = "the TLS connection to " + host + " failed: check the CA bundle of this system and any proxy"
		case strings.Contains(m[2], "Could not resolve host"):
			e.Cause = "the name of " + host + " cannot be resolved: check the network and /etc/resolv.conf"
		default:
			e.Cause = "check the network connection to " + host
		}
		return e
	}
	switch {
	case strings.Contains(text, "No space left on device"):
		return newErr(cDiskFull, map[string]string{"what": what})
	case sqliteBusy(text):
		return newErr(cStoreBusy, nil)
	case strings.Contains(text, "while setting up the build environment"),
		strings.Contains(text, "unable to start build process"),
		strings.Contains(text, "setting up a private mount namespace"),
		strings.Contains(text, "sandbox"):
		e := newErr(cStoreAccess, map[string]string{"reason": "the isolated build environment for " + what + " cannot be set up"})
		e.Cause = missingHostPath(text)
		return e
	case strings.Contains(text, "Permission denied"), strings.Contains(text, "Read-only file system"):
		e := newErr(cStoreAccess, map[string]string{"reason": "permission denied while getting " + what})
		if p := rePermPath().FindStringSubmatch(text); p != nil {
			e.Cause = "no access to " + shortPath(p[1])
		}
		return e
	}
	if m := reBuilderFail().FindStringSubmatch(text); m != nil {
		return newErr(cUpstream, map[string]string{"attr": m[1], "users": what})
	}
	return newErr(cNixStopped, map[string]string{"reason": "getting " + what + " failed (run with -v for details)"})
}

// sqliteBusy is store.sqliteBusy's test on Nix's stderr (R-NIX-026).
func sqliteBusy(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "database is locked") || strings.Contains(l, "database is busy") ||
		strings.Contains(l, "sqlite database '") && strings.Contains(l, "' is busy")
}

// missingHostPath names the host file a failed build environment setup could not find, in
// bob's words ("" when Nix's message names none).
func missingHostPath(text string) string {
	m := reAttrPath().FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	if strings.HasPrefix(m[1], "/nix/store/") {
		return "a file of this system links into its own Nix store (" + shortPath(m[1]) +
			"), which bob's private store hides"
	}
	return m[1] + " is missing"
}

// shortPath shortens the store paths in p to their names.
func shortPath(p string) string { return reStorePath().ReplaceAllString(p, "$1") }

var (
	reAttrPath = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`getting attributes of path ['"]([^'"]+)['"]`) })
	rePermPath = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`['"]([^'"]+)['"]: Permission denied`) })
)
