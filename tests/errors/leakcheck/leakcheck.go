// Package leakcheck finds raw Nix text in output bob shows to a user without -v (AGENTS.md §5.5,
// R-NIX-010, HARNESS.md §5.2 check 3). Every suite runs it over the stdout and stderr it
// captures from bob.
package leakcheck

import (
	"fmt"
	"regexp"
	"strings"
)

// patterns are the shared leak patterns of HARNESS.md §5.2 and SPEC R-NIX-010.
var patterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"store path", regexp.MustCompile(`/nix/store/[0-9a-z]{32}`)},
	{"derivation file", regexp.MustCompile(`\.drv\b`)},
	{"derivation quote", regexp.MustCompile(`derivation '`)},
	{"builder for", regexp.MustCompile(`builder for '`)},
	{"Cannot build", regexp.MustCompile(`Cannot build`)},
	{"Reason:", regexp.MustCompile(`Reason:`)},
	{"substituter", regexp.MustCompile(`substituter`)},
	{"@nix", regexp.MustCompile(`@nix `)},
	{"while evaluating", regexp.MustCompile(`while evaluating`)},
	{"Nix source position", regexp.MustCompile(`-source/\S*\.nix:`)},
	// A raw "error:" line; bob's own lines are "error[BOB-E-NNNN]:".
	{"raw error line", regexp.MustCompile(`(?m)^\s*error:`)},
	{"Nix option", optionRE(nixOptions)},
}

// nixOptions are Nix settings that the S15 fault catalogue makes Nix name in its messages. The
// embedded Nix's full option list replaces this list once bob embeds Nix (HARNESS.md §5.2).
var nixOptions = []string{
	"max-jobs", "max-silent-time", "max-build-log-size", "stalled-download-timeout",
	"trusted-public-keys", "require-sigs", "post-build-hook", "build-users-group",
	"experimental-features", "allowed-references", "disallowed-references", "__noChroot",
	"sandbox-paths", "connect-timeout", "download-attempts",
}

func optionRE(opts []string) *regexp.Regexp {
	q := make([]string, len(opts))
	for i, o := range opts {
		q[i] = regexp.QuoteMeta(o)
	}
	// An option counts when it stands alone ("'max-jobs'", "max-jobs = 0"), not inside a word.
	return regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(?:` + strings.Join(q, "|") + `)(?:[^A-Za-z0-9_-]|$)`)
}

// Leak is one finding.
type Leak struct {
	Pattern string
	Line    string
}

func (l Leak) String() string { return fmt.Sprintf("%s: %q", l.Pattern, l.Line) }

// Check returns every line of out that matches a shared pattern or contains one of the extra
// literal texts (a fault's own raw Nix messages).
func Check(out []byte, extra ...string) []Leak {
	var leaks []Leak
	for _, line := range strings.Split(string(out), "\n") {
		for _, p := range patterns {
			if p.re.MatchString(line) {
				leaks = append(leaks, Leak{p.name, line})
			}
		}
		for _, x := range extra {
			if x != "" && strings.Contains(line, x) {
				leaks = append(leaks, Leak{"raw Nix message", line})
			}
		}
	}
	return leaks
}
