// Package npm is the S4 spike's own npm resolver: node-semver ranges, registry client,
// graph resolution with peers/optional/platform/aliases/bundled/workspaces, and bob.lock.
package npm

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a semver 2.0 version (build metadata dropped, as node-semver does for ordering).
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Raw                 string
}

func ParseVersion(s string) (Version, error) {
	raw := s
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "=v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, pre = s[:i], s[i+1:]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid version %q", raw)
	}
	var n [3]uint64
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 10, 64)
		if err != nil || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("invalid version %q", raw)
		}
		n[i] = v
	}
	v := Version{Major: n[0], Minor: n[1], Patch: n[2], Raw: raw}
	if pre != "" {
		v.Pre = strings.Split(pre, ".")
	}
	return v, nil
}

func cmpIdent(a, b string) int {
	ai, ea := strconv.ParseUint(a, 10, 64)
	bi, eb := strconv.ParseUint(b, 10, 64)
	switch {
	case ea == nil && eb == nil:
		return cmpU(ai, bi)
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpU(a, b uint64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (a Version) Compare(b Version) int {
	if c := cmpU(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpU(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpU(a.Patch, b.Patch); c != 0 {
		return c
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := cmpIdent(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	return cmpU(uint64(len(a.Pre)), uint64(len(b.Pre)))
}

func (a Version) sameTuple(b Version) bool {
	return a.Major == b.Major && a.Minor == b.Minor && a.Patch == b.Patch
}

type comparator struct {
	op string // "<", "<=", ">", ">=", "="
	v  Version
}

func (c comparator) test(v Version) bool {
	r := v.Compare(c.v)
	switch c.op {
	case "<":
		return r < 0
	case "<=":
		return r <= 0
	case ">":
		return r > 0
	case ">=":
		return r >= 0
	}
	return r == 0
}

// Range is a node-semver range: OR of comparator sets.
type Range struct {
	sets [][]comparator
	Raw  string
}

// partial parses "1", "1.2", "1.x", "*", "1.2.3-beta" into components; -1 = wildcard.
func partial(s string) (n [3]int64, pre string, err error) {
	s = strings.TrimLeft(strings.TrimSpace(s), "=v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, pre = s[:i], s[i+1:]
	}
	n = [3]int64{-1, -1, -1}
	if s == "" {
		return n, pre, nil
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return n, "", fmt.Errorf("bad version %q", s)
	}
	for i, p := range parts {
		if p == "x" || p == "X" || p == "*" {
			break
		}
		v, e := strconv.ParseInt(p, 10, 64)
		if e != nil || v < 0 {
			return n, "", fmt.Errorf("bad version %q", s)
		}
		n[i] = v
	}
	return n, pre, nil
}

func mk(a, b, c int64, pre string) Version {
	v := Version{Major: uint64(a), Minor: uint64(b), Patch: uint64(c)}
	if pre != "" {
		v.Pre = strings.Split(pre, ".")
	}
	return v
}

func z(x int64) int64 {
	if x < 0 {
		return 0
	}
	return x
}

// lowerUpper converts a partial into [>=lo, <hi) bounds (hi nil = unbounded).
func expandX(n [3]int64, pre string) []comparator {
	switch {
	case n[0] < 0:
		return []comparator{{">=", mk(0, 0, 0, "")}}
	case n[1] < 0:
		return []comparator{{">=", mk(n[0], 0, 0, "")}, {"<", mk(n[0]+1, 0, 0, "0")}}
	case n[2] < 0:
		return []comparator{{">=", mk(n[0], n[1], 0, "")}, {"<", mk(n[0], n[1]+1, 0, "0")}}
	}
	return []comparator{{"=", mk(n[0], n[1], n[2], pre)}}
}

func parseComparator(tok string) ([]comparator, error) {
	switch {
	case strings.HasPrefix(tok, "^"):
		n, pre, err := partial(tok[1:])
		if err != nil {
			return nil, err
		}
		lo := mk(z(n[0]), z(n[1]), z(n[2]), pre)
		var hi Version
		switch {
		case n[0] < 0:
			return []comparator{{">=", mk(0, 0, 0, "")}}, nil
		case n[0] > 0 || n[1] < 0:
			hi = mk(n[0]+1, 0, 0, "0")
		case n[1] > 0 || n[2] < 0:
			hi = mk(0, n[1]+1, 0, "0")
		default:
			hi = mk(0, 0, n[2]+1, "0")
		}
		return []comparator{{">=", lo}, {"<", hi}}, nil
	case strings.HasPrefix(tok, "~"):
		t := strings.TrimPrefix(tok[1:], ">")
		n, pre, err := partial(t)
		if err != nil {
			return nil, err
		}
		if n[0] < 0 {
			return []comparator{{">=", mk(0, 0, 0, "")}}, nil
		}
		lo := mk(n[0], z(n[1]), z(n[2]), pre)
		hi := mk(n[0], z(n[1])+1, 0, "0")
		if n[1] < 0 {
			hi = mk(n[0]+1, 0, 0, "0")
		}
		return []comparator{{">=", lo}, {"<", hi}}, nil
	}
	op := ""
	for _, o := range []string{">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(tok, o) {
			op, tok = o, tok[len(o):]
			break
		}
	}
	n, pre, err := partial(tok)
	if err != nil {
		return nil, err
	}
	full := n[2] >= 0
	switch op {
	case "", "=":
		return expandX(n, pre), nil
	case ">=":
		return []comparator{{">=", mk(z(n[0]), z(n[1]), z(n[2]), pre)}}, nil
	case "<":
		return []comparator{{"<", mk(z(n[0]), z(n[1]), z(n[2]), pre)}}, nil
	case ">":
		if full {
			return []comparator{{">", mk(n[0], n[1], n[2], pre)}}, nil
		}
		if n[0] < 0 {
			return []comparator{{"<", mk(0, 0, 0, "0")}}, nil
		}
		if n[1] < 0 {
			return []comparator{{">=", mk(n[0]+1, 0, 0, "")}}, nil
		}
		return []comparator{{">=", mk(n[0], n[1]+1, 0, "")}}, nil
	case "<=":
		if full {
			return []comparator{{"<=", mk(n[0], n[1], n[2], pre)}}, nil
		}
		if n[0] < 0 {
			return []comparator{{">=", mk(0, 0, 0, "")}}, nil
		}
		if n[1] < 0 {
			return []comparator{{"<", mk(n[0]+1, 0, 0, "0")}}, nil
		}
		return []comparator{{"<", mk(n[0], n[1]+1, 0, "0")}}, nil
	}
	return nil, fmt.Errorf("bad comparator %q", tok)
}

func ParseRange(s string) (Range, error) {
	r := Range{Raw: s}
	for _, alt := range strings.Split(s, "||") {
		alt = strings.TrimSpace(alt)
		var set []comparator
		if strings.Contains(alt, " - ") {
			ab := strings.SplitN(alt, " - ", 2)
			lo, plo, err := partial(ab[0])
			if err != nil {
				return r, err
			}
			hi, phi, err := partial(ab[1])
			if err != nil {
				return r, err
			}
			set = append(set, comparator{">=", mk(z(lo[0]), z(lo[1]), z(lo[2]), plo)})
			switch {
			case hi[0] < 0:
			case hi[1] < 0:
				set = append(set, comparator{"<", mk(hi[0]+1, 0, 0, "0")})
			case hi[2] < 0:
				set = append(set, comparator{"<", mk(hi[0], hi[1]+1, 0, "0")})
			default:
				set = append(set, comparator{"<=", mk(hi[0], hi[1], hi[2], phi)})
			}
		} else {
			// glue operators to their operand: ">= 1.2" -> ">=1.2"
			f := strings.Fields(alt)
			var toks []string
			for i := 0; i < len(f); i++ {
				t := f[i]
				if strings.Trim(t, "<>=~^") == "" && i+1 < len(f) {
					t += f[i+1]
					i++
				}
				toks = append(toks, t)
			}
			if len(toks) == 0 {
				toks = []string{"*"}
			}
			for _, t := range toks {
				cs, err := parseComparator(t)
				if err != nil {
					return r, fmt.Errorf("range %q: %w", s, err)
				}
				set = append(set, cs...)
			}
		}
		r.sets = append(r.sets, set)
	}
	return r, nil
}

// Match implements node-semver satisfies() including the prerelease rule.
func (r Range) Match(v Version) bool {
	for _, set := range r.sets {
		ok := true
		for _, c := range set {
			if !c.test(v) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if len(v.Pre) == 0 {
			return true
		}
		for _, c := range set {
			if len(c.v.Pre) > 0 && c.v.sameTuple(v) && !(c.v.Pre[0] == "0" && len(c.v.Pre) == 1 && c.op == "<") {
				return true
			}
		}
	}
	return false
}
