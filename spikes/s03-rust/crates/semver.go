// Package crates is spike S3's crates.io support: semver, sparse index client, resolver,
// crate metadata and lock formats. Written from the Cargo reference (semver/resolver/features
// chapters), no code taken from cargo.
package crates

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a semver 2.0 version (build metadata kept for printing, ignored for ordering).
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Build               string
}

func ParseVersion(s string) (Version, error) {
	var v Version
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '+'); i >= 0 {
		v.Build, s = s[i+1:], s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.Pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("invalid version %q", s)
	}
	var err error
	nums := []*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if *nums[i], err = strconv.ParseUint(p, 10, 64); err != nil {
			return v, fmt.Errorf("invalid version %q", s)
		}
	}
	return v, nil
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
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

// Compare orders versions per semver 2.0 §11.
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
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		a, b := v.Pre[i], o.Pre[i]
		an, aerr := strconv.ParseUint(a, 10, 64)
		bn, berr := strconv.ParseUint(b, 10, 64)
		switch {
		case aerr == nil && berr == nil:
			if c := cmpU(an, bn); c != 0 {
				return c
			}
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(a, b); c != 0 {
				return c
			}
		}
	}
	return cmpU(uint64(len(v.Pre)), uint64(len(o.Pre)))
}

// Compat is the semver-compatibility class cargo uses: two versions in the same class cannot
// both be in one graph. 1.x.y -> "1", 0.3.y -> "0.3", 0.0.4 -> "0.0.4".
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
	opGT
	opGE
	opLT
	opLE
	opTilde
	opCaret
	opWild
)

type comparator struct {
	op         op
	maj        uint64
	min, patch *uint64
	pre        []string
}

// Req is a cargo version requirement: comparators joined by ',' (all must match).
type Req struct {
	raw  string
	cmps []comparator
}

func (r Req) String() string { return r.raw }

func ParseReq(s string) (Req, error) {
	r := Req{raw: s}
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return r, nil
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		c := comparator{op: opCaret}
		for _, p := range []struct {
			s string
			o op
		}{{">=", opGE}, {"<=", opLE}, {">", opGT}, {"<", opLT}, {"=", opExact}, {"~", opTilde}, {"^", opCaret}} {
			if strings.HasPrefix(part, p.s) {
				c.op, part = p.o, strings.TrimSpace(part[len(p.s):])
				break
			}
		}
		if part == "*" {
			continue
		}
		if i := strings.IndexByte(part, '+'); i >= 0 {
			part = part[:i]
		}
		if i := strings.IndexByte(part, '-'); i >= 0 {
			c.pre = strings.Split(part[i+1:], ".")
			part = part[:i]
		}
		nums := strings.Split(part, ".")
		if len(nums) > 3 {
			return r, fmt.Errorf("invalid requirement %q", s)
		}
		wild := false
		for i, n := range nums {
			if n == "*" || n == "x" || n == "X" {
				wild = true
				break
			}
			u, err := strconv.ParseUint(n, 10, 64)
			if err != nil {
				return r, fmt.Errorf("invalid requirement %q", s)
			}
			switch i {
			case 0:
				c.maj = u
			case 1:
				c.min = &u
			case 2:
				c.patch = &u
			}
		}
		if wild && c.op == opCaret {
			c.op = opWild
		}
		r.cmps = append(r.cmps, c)
	}
	return r, nil
}

func z(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

func (c comparator) lower() Version {
	return Version{Major: c.maj, Minor: z(c.min), Patch: z(c.patch), Pre: c.pre}
}

func (c comparator) matches(v Version) bool {
	lo := c.lower()
	switch c.op {
	case opExact, opWild:
		if v.Major != c.maj || c.min != nil && v.Minor != *c.min || c.patch != nil && v.Patch != *c.patch {
			return false
		}
		return c.op == opWild || c.patch == nil || equalPre(v.Pre, c.pre)
	case opGT:
		if c.patch == nil { // >1.2 means >=1.3.0
			if c.min == nil {
				return v.Major > c.maj
			}
			return v.Major > c.maj || v.Major == c.maj && v.Minor > *c.min
		}
		return v.Compare(lo) > 0
	case opGE:
		return v.Compare(lo) >= 0
	case opLT:
		return v.Compare(lo) < 0
	case opLE:
		if c.patch == nil {
			if c.min == nil {
				return v.Major <= c.maj
			}
			return v.Major < c.maj || v.Major == c.maj && v.Minor <= *c.min
		}
		return v.Compare(lo) <= 0
	case opTilde:
		if v.Compare(lo) < 0 || v.Major != c.maj {
			return false
		}
		return c.min == nil || v.Minor == *c.min
	case opCaret:
		if v.Compare(lo) < 0 {
			return false
		}
		switch {
		case c.maj > 0 || c.min == nil:
			return v.Major == c.maj
		case *c.min > 0 || c.patch == nil:
			return v.Major == 0 && v.Minor == *c.min
		default:
			return v.Major == 0 && v.Minor == 0 && v.Patch == *c.patch
		}
	}
	return false
}

func equalPre(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Matches reports whether v satisfies r. Pre-release versions only match when a comparator
// names the same major.minor.patch with a pre-release (cargo/semver rule).
func (r Req) Matches(v Version) bool {
	if len(v.Pre) > 0 {
		ok := false
		for _, c := range r.cmps {
			if len(c.pre) > 0 && c.maj == v.Major && z(c.min) == v.Minor && z(c.patch) == v.Patch {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	for _, c := range r.cmps {
		if !c.matches(v) {
			return false
		}
	}
	return true
}
