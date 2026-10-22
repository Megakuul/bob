package resolve

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a semver 2.0 version as crates.io publishes it.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string // pre-release identifiers, empty for a release
	Build               string   // build metadata; ordered last, never part of compatibility
}

// ParseVersion parses a strict semver version ("1.2.3", "1.2.3-alpha.1", "2.1.2+zstd.1.5.7").
func ParseVersion(s string) (Version, error) {
	var v Version
	in := s
	if i := strings.IndexByte(s, '+'); i >= 0 {
		v.Build, s = s[i+1:], s[:i]
		if !validIdents(v.Build, false) {
			return Version{}, fmt.Errorf("invalid version %q: bad build metadata", in)
		}
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if !validIdents(s[i+1:], true) {
			return Version{}, fmt.Errorf("invalid version %q: bad pre-release", in)
		}
		v.Pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid version %q", in)
	}
	for i, p := range parts {
		n, ok := number(p)
		if !ok {
			return Version{}, fmt.Errorf("invalid version %q", in)
		}
		*[]*uint64{&v.Major, &v.Minor, &v.Patch}[i] = n
	}
	return v, nil
}

// number parses a semver numeric identifier (no sign, no leading zero).
func number(s string) (uint64, bool) {
	if s == "" || len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func validIdents(s string, pre bool) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		numeric := true
		for i := 0; i < len(id); i++ {
			c := id[i]
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-':
				numeric = false
			default:
				return false
			}
		}
		if pre && numeric && len(id) > 1 && id[0] == '0' {
			return false
		}
	}
	return true
}

func (v Version) String() string {
	s := strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10) + "." + strconv.FormatUint(v.Patch, 10)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

func cmpU(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// cmpIdents orders dot-separated identifier lists: numeric identifiers numerically and before
// alphanumeric ones, alphanumeric ones in ASCII order, a shorter list first when it is a prefix.
func cmpIdents(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		an, aok := number(a[i])
		bn, bok := number(b[i])
		switch {
		case aok && bok:
			if c := cmpU(an, bn); c != 0 {
				return c
			}
		case aok:
			return -1
		case bok:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	return cmpU(uint64(len(a)), uint64(len(b)))
}

// cmpPre orders pre-releases; a release (no pre-release) is greater than any pre-release.
func cmpPre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	return cmpIdents(a, b)
}

// Compare orders versions by semver precedence, build metadata last (as Cargo sorts them).
func (v Version) Compare(o Version) int {
	if c := cmpU(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpU(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpU(v.Patch, o.Patch); c != 0 {
		return c
	}
	if c := cmpPre(v.Pre, o.Pre); c != 0 {
		return c
	}
	switch {
	case v.Build == o.Build:
		return 0
	case v.Build == "":
		return -1
	case o.Build == "":
		return 1
	}
	return cmpIdents(strings.Split(v.Build, "."), strings.Split(o.Build, "."))
}

// Compat is the semver-compatibility class: one graph holds at most one version per class
// and crate. 1.x.y -> "1", 0.3.y -> "0.3", 0.0.4 -> "0.0.4".
func (v Version) Compat() string {
	switch {
	case v.Major > 0:
		return strconv.FormatUint(v.Major, 10)
	case v.Minor > 0:
		return "0." + strconv.FormatUint(v.Minor, 10)
	}
	return "0.0." + strconv.FormatUint(v.Patch, 10)
}

type op int

const (
	opExact op = iota
	opGreater
	opGreaterEq
	opLess
	opLessEq
	opTilde
	opCaret
	opWildcard
)

// comparator is one term of a requirement; nil minor/patch were left out ("1", "1.2").
type comparator struct {
	op           op
	major        uint64
	minor, patch *uint64
	pre          []string
}

// Req is a Cargo version requirement: comma-separated comparators that must all match.
type Req struct {
	raw  string
	cmps []comparator
}

func (r Req) String() string { return r.raw }

// ParseReq parses a Cargo requirement ("1", "^0.13", "=1.38.0", ">=1.2, <1.5", "~1.2", "1.*", "*").
func ParseReq(s string) (Req, error) {
	r := Req{raw: s}
	t := strings.TrimSpace(s)
	if t == "" {
		return r, fmt.Errorf("empty version requirement")
	}
	if t == "*" {
		return r, nil
	}
	for _, part := range strings.Split(t, ",") {
		c, err := parseComparator(strings.TrimSpace(part))
		if err != nil {
			return r, fmt.Errorf("invalid version requirement %q: %w", s, err)
		}
		r.cmps = append(r.cmps, c)
	}
	return r, nil
}

func parseComparator(part string) (comparator, error) {
	c := comparator{op: opCaret}
	explicit := false
	for _, p := range []struct {
		s string
		o op
	}{{">=", opGreaterEq}, {"<=", opLessEq}, {">", opGreater}, {"<", opLess}, {"=", opExact}, {"~", opTilde}, {"^", opCaret}} {
		if strings.HasPrefix(part, p.s) {
			c.op, part, explicit = p.o, strings.TrimSpace(part[len(p.s):]), true
			break
		}
	}
	if part == "" {
		return c, fmt.Errorf("missing version")
	}
	if i := strings.IndexByte(part, '+'); i >= 0 {
		part = part[:i] // build metadata is ignored in requirements
	}
	if i := strings.IndexByte(part, '-'); i >= 0 {
		if !validIdents(part[i+1:], true) {
			return c, fmt.Errorf("bad pre-release")
		}
		c.pre = strings.Split(part[i+1:], ".")
		part = part[:i]
	}
	nums := strings.Split(part, ".")
	if len(nums) > 3 {
		return c, fmt.Errorf("too many version components")
	}
	wild := false
	for i, n := range nums {
		if n == "*" || n == "x" || n == "X" {
			if i == 0 {
				return c, fmt.Errorf("wildcard major version in a comparator list")
			}
			wild = true
			continue
		}
		if wild {
			return c, fmt.Errorf("version component after a wildcard")
		}
		u, ok := number(n)
		if !ok {
			return c, fmt.Errorf("bad version component %q", n)
		}
		switch i {
		case 0:
			c.major = u
		case 1:
			c.minor = &u
		case 2:
			c.patch = &u
		}
	}
	if wild {
		if explicit && c.op != opExact {
			return c, fmt.Errorf("wildcard after an operator")
		}
		c.op = opWildcard
	}
	if len(c.pre) > 0 && c.patch == nil {
		return c, fmt.Errorf("pre-release without a patch version")
	}
	return c, nil
}

// Matches reports whether v satisfies r with the semver crate's rules Cargo uses: a
// pre-release only matches when some comparator names the same major.minor.patch with a
// pre-release.
func (r Req) Matches(v Version) bool {
	for _, c := range r.cmps {
		if !c.matches(v) {
			return false
		}
	}
	if len(v.Pre) == 0 {
		return true
	}
	for _, c := range r.cmps {
		if len(c.pre) > 0 && c.major == v.Major && c.minor != nil && *c.minor == v.Minor && c.patch != nil && *c.patch == v.Patch {
			return true
		}
	}
	return false
}

func (c comparator) matches(v Version) bool {
	switch c.op {
	case opExact, opWildcard:
		return c.exact(v)
	case opGreater:
		return c.greater(v)
	case opGreaterEq:
		return c.exact(v) || c.greater(v)
	case opLess:
		return c.less(v)
	case opLessEq:
		return c.exact(v) || c.less(v)
	case opTilde:
		if v.Major != c.major || c.minor != nil && v.Minor != *c.minor {
			return false
		}
		if c.patch != nil && v.Patch != *c.patch {
			return v.Patch > *c.patch
		}
		return cmpPre(v.Pre, c.pre) >= 0
	case opCaret:
		return c.caret(v)
	}
	return false
}

func (c comparator) exact(v Version) bool {
	if v.Major != c.major || c.minor != nil && v.Minor != *c.minor || c.patch != nil && v.Patch != *c.patch {
		return false
	}
	return cmpPre(v.Pre, c.pre) == 0
}

func (c comparator) greater(v Version) bool {
	if v.Major != c.major {
		return v.Major > c.major
	}
	if c.minor == nil {
		return false
	}
	if v.Minor != *c.minor {
		return v.Minor > *c.minor
	}
	if c.patch == nil {
		return false
	}
	if v.Patch != *c.patch {
		return v.Patch > *c.patch
	}
	return cmpPre(v.Pre, c.pre) > 0
}

func (c comparator) less(v Version) bool {
	if v.Major != c.major {
		return v.Major < c.major
	}
	if c.minor == nil {
		return false
	}
	if v.Minor != *c.minor {
		return v.Minor < *c.minor
	}
	if c.patch == nil {
		return false
	}
	if v.Patch != *c.patch {
		return v.Patch < *c.patch
	}
	return cmpPre(v.Pre, c.pre) < 0
}

func (c comparator) caret(v Version) bool {
	if v.Major != c.major {
		return false
	}
	if c.minor == nil {
		return true
	}
	minor := *c.minor
	if c.patch == nil {
		if c.major > 0 {
			return v.Minor >= minor
		}
		return v.Minor == minor
	}
	patch := *c.patch
	switch {
	case c.major > 0:
		if v.Minor != minor {
			return v.Minor > minor
		}
		if v.Patch != patch {
			return v.Patch > patch
		}
	case minor > 0:
		if v.Minor != minor {
			return false
		}
		if v.Patch != patch {
			return v.Patch > patch
		}
	default:
		if v.Minor != minor || v.Patch != patch {
			return false
		}
	}
	return cmpPre(v.Pre, c.pre) >= 0
}
