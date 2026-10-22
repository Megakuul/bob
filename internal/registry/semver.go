package registry

import (
	"strconv"
	"strings"
)

// Version is a semantic version 2.0 (release versions, OCI release tags, "latest").
type Version struct {
	Major, Minor, Patch uint64
	Pre                 string // pre-release part without "-" ("" = a release)
}

// ParseVersion parses "1.4.2", "1.6.0-rc.1", "1.0.0+build.5" (build metadata is ignored).
func ParseVersion(s string) (Version, bool) {
	if len(s) == 0 || len(s) > 128 {
		return Version{}, false
	}
	s, _, _ = strings.Cut(s, "+")
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var n [3]uint64
	for i, p := range parts {
		v, ok := numeric(p)
		if !ok {
			return Version{}, false
		}
		n[i] = v
	}
	if hasPre {
		if pre == "" {
			return Version{}, false
		}
		for _, id := range strings.Split(pre, ".") {
			if id == "" || strings.Trim(id, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-") != "" {
				return Version{}, false
			}
			if _, isNum := numeric(id); !isNum && strings.Trim(id, "0123456789") == "" {
				return Version{}, false // numeric identifier with a leading zero
			}
		}
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2], Pre: pre}, true
}

// numeric parses a semver numeric identifier (no leading zero).
func numeric(s string) (uint64, bool) {
	if s == "" || len(s) > 1 && s[0] == '0' || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// Compare returns -1, 0 or +1 (semver 2.0 precedence).
func (v Version) Compare(o Version) int {
	for _, d := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		switch {
		case d[0] < d[1]:
			return -1
		case d[0] > d[1]:
			return 1
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	a, b := strings.Split(v.Pre, "."), strings.Split(o.Pre, ".")
	for i := 0; i < len(a) && i < len(b); i++ {
		x, xn := numeric(a[i])
		y, yn := numeric(b[i])
		switch {
		case xn && yn && x != y:
			if x < y {
				return -1
			}
			return 1
		case xn && !yn:
			return -1
		case !xn && yn:
			return 1
		case !xn && !yn && a[i] != b[i]:
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// String is the canonical form.
func (v Version) String() string {
	s := strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10) + "." + strconv.FormatUint(v.Patch, 10)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Matches reports whether version satisfies a ResolveRelease constraint (§6.5.11): "" = any
// release, "1" / "1.4" = that major / minor line, a full version = exactly it. Pre-releases
// match only exactly.
func Matches(constraint string, v Version) bool {
	if constraint == "" {
		return v.Pre == ""
	}
	if c, ok := ParseVersion(constraint); ok {
		return c.Compare(v) == 0
	}
	if v.Pre != "" {
		return false
	}
	parts := strings.Split(constraint, ".")
	if len(parts) > 2 {
		return false
	}
	want := []uint64{v.Major, v.Minor}
	for i, p := range parts {
		n, ok := numeric(p)
		if !ok || n != want[i] {
			return false
		}
	}
	return true
}

// ValidConstraint reports whether c is a constraint Matches understands.
func ValidConstraint(c string) bool {
	if c == "" {
		return true
	}
	if _, ok := ParseVersion(c); ok {
		return true
	}
	parts := strings.Split(c, ".")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if _, ok := numeric(p); !ok {
			return false
		}
	}
	return true
}
