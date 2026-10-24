package resolve

import "strings"

// Semantic versions as Go modules use them (golang.org/x/mod/semver rules, re-implemented
// here because L2 packages import no third-party code): "v" major.minor.patch, an optional
// pre-release and an optional build suffix ("+incompatible").

type semver struct {
	major, minor, patch string
	pre                 string // without the leading "-"
	ok                  bool
}

func parseSemver(v string) semver {
	var s semver
	if !strings.HasPrefix(v, "v") {
		return s
	}
	v = v[1:]
	if i := strings.IndexByte(v, '+'); i >= 0 {
		if !validIdents(v[i+1:], false) {
			return s
		}
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		s.pre = v[i+1:]
		if !validIdents(s.pre, true) {
			return s
		}
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return s
	}
	for _, p := range parts {
		if !isNum(p) || len(p) > 1 && p[0] == '0' {
			return s
		}
	}
	s.major, s.minor, s.patch, s.ok = parts[0], parts[1], parts[2], true
	return s
}

func isNum(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validIdents checks dot-separated identifiers of [0-9A-Za-z-]; numeric pre-release
// identifiers have no leading zero.
func validIdents(s string, pre bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for i := 0; i < len(id); i++ {
			c := id[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
				return false
			}
		}
		if pre && isNum(id) && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

// ValidVersion reports whether v is a canonical module version ("v1.2.3", pre-releases,
// pseudo-versions and "+incompatible" included).
func ValidVersion(v string) bool {
	s := parseSemver(v)
	if !s.ok {
		return false
	}
	if i := strings.IndexByte(v, '+'); i >= 0 && v[i:] != "+incompatible" {
		return false
	}
	return true
}

// Compare orders module versions: -1, 0 or +1. Invalid versions sort before valid ones.
func Compare(a, b string) int {
	x, y := parseSemver(a), parseSemver(b)
	switch {
	case !x.ok && !y.ok:
		return 0
	case !x.ok:
		return -1
	case !y.ok:
		return 1
	}
	if c := numCompare(x.major, y.major); c != 0 {
		return c
	}
	if c := numCompare(x.minor, y.minor); c != 0 {
		return c
	}
	if c := numCompare(x.patch, y.patch); c != 0 {
		return c
	}
	return preCompare(x.pre, y.pre)
}

func numCompare(a, b string) int {
	switch {
	case len(a) != len(b):
		if len(a) < len(b) {
			return -1
		}
		return 1
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// preCompare orders pre-releases: none is highest; identifiers compare numerically when both
// are numbers, numbers before words, words bytewise; a shorter equal prefix is lower.
func preCompare(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, y := as[i], bs[i]
		if x == y {
			continue
		}
		xn, yn := isNum(x), isNum(y)
		switch {
		case xn && yn:
			return numCompare(x, y)
		case xn:
			return -1
		case yn:
			return 1
		case x < y:
			return -1
		default:
			return 1
		}
	}
	return numCompare(strings.Repeat("x", len(as)), strings.Repeat("x", len(bs)))
}

// IsPrerelease reports whether v is a pre-release (pseudo-versions included).
func IsPrerelease(v string) bool { return parseSemver(v).pre != "" }

// Go versions ("1.26", "1.26.0", "1.21rc1"): the language version "1.N" sorts before every
// release of it, pre-releases before "1.N.0".

type goVersion struct {
	major, minor, patch string
	kind                string // "", "beta", "rc"
	pre                 string // number after beta/rc
}

func parseGo(v string) (goVersion, bool) {
	var g goVersion
	maj, rest, ok := strings.Cut(v, ".")
	if !ok || !isNum(maj) {
		return g, false
	}
	g.major = maj
	min := rest
	if i := strings.IndexAny(rest, ".br"); i >= 0 {
		min, rest = rest[:i], rest[i:]
	} else {
		rest = ""
	}
	if !isNum(min) {
		return g, false
	}
	g.minor = min
	switch {
	case rest == "":
	case strings.HasPrefix(rest, "."):
		if !isNum(rest[1:]) {
			return g, false
		}
		g.patch = rest[1:]
	case strings.HasPrefix(rest, "beta"), strings.HasPrefix(rest, "rc"):
		k := "rc"
		if rest[0] == 'b' {
			k = "beta"
		}
		g.kind, g.pre = k, rest[len(k):]
		if !isNum(g.pre) {
			return g, false
		}
	default:
		return g, false
	}
	return g, true
}

// GoCompare orders Go versions; unparsable versions sort first.
func GoCompare(a, b string) int {
	x, xok := parseGo(a)
	y, yok := parseGo(b)
	switch {
	case !xok && !yok:
		return 0
	case !xok:
		return -1
	case !yok:
		return 1
	}
	if c := numCompare(x.major, y.major); c != 0 {
		return c
	}
	if c := numCompare(x.minor, y.minor); c != 0 {
		return c
	}
	rank := func(g goVersion) int {
		switch {
		case g.kind == "" && g.patch == "":
			return 0 // language version
		case g.kind == "beta":
			return 1
		case g.kind == "rc":
			return 2
		}
		return 3
	}
	if c := rank(x) - rank(y); c != 0 {
		if c < 0 {
			return -1
		}
		return 1
	}
	if x.kind != "" {
		return numCompare(x.pre, y.pre)
	}
	return numCompare(x.patch, y.patch)
}

// GoLang is the language version of a Go version: "1.26.3" → "1.26".
func GoLang(v string) string {
	g, ok := parseGo(v)
	if !ok {
		return v
	}
	return g.major + "." + g.minor
}
