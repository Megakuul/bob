package resolve

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// This file is a port of node-semver (ISC, used by npm 11) as far as npm's resolution needs it:
// version parsing, range parsing with the loose grammar, and Range.test with the prerelease rule.
// The regular expressions are node-semver's (internal/re.js); RE2 accepts all of them. Keeping
// them literal makes the port checkable line by line against the original.

const (
	maxVersionLength = 256
	maxSafeInteger   = 1<<53 - 1
)

var (
	reSrc = func() map[string]string {
		s := map[string]string{}
		s["NUMERICIDENTIFIER"] = `0|[1-9]\d*`
		s["NUMERICIDENTIFIERLOOSE"] = `\d+`
		s["NONNUMERICIDENTIFIER"] = `\d*[a-zA-Z-][a-zA-Z0-9-]*`
		s["MAINVERSION"] = `(` + s["NUMERICIDENTIFIER"] + `)\.(` + s["NUMERICIDENTIFIER"] + `)\.(` + s["NUMERICIDENTIFIER"] + `)`
		s["MAINVERSIONLOOSE"] = `(` + s["NUMERICIDENTIFIERLOOSE"] + `)\.(` + s["NUMERICIDENTIFIERLOOSE"] + `)\.(` + s["NUMERICIDENTIFIERLOOSE"] + `)`
		s["PRERELEASEIDENTIFIER"] = `(?:` + s["NONNUMERICIDENTIFIER"] + `|` + s["NUMERICIDENTIFIER"] + `)`
		s["PRERELEASEIDENTIFIERLOOSE"] = `(?:` + s["NONNUMERICIDENTIFIER"] + `|` + s["NUMERICIDENTIFIERLOOSE"] + `)`
		s["PRERELEASE"] = `(?:-(` + s["PRERELEASEIDENTIFIER"] + `(?:\.` + s["PRERELEASEIDENTIFIER"] + `)*))`
		s["PRERELEASELOOSE"] = `(?:-?(` + s["PRERELEASEIDENTIFIERLOOSE"] + `(?:\.` + s["PRERELEASEIDENTIFIERLOOSE"] + `)*))`
		s["BUILDIDENTIFIER"] = `[a-zA-Z0-9-]+`
		s["BUILD"] = `(?:\+(` + s["BUILDIDENTIFIER"] + `(?:\.` + s["BUILDIDENTIFIER"] + `)*))`
		s["FULLPLAIN"] = `v?` + s["MAINVERSION"] + s["PRERELEASE"] + `?` + s["BUILD"] + `?`
		s["LOOSEPLAIN"] = `[v=\s]*` + s["MAINVERSIONLOOSE"] + s["PRERELEASELOOSE"] + `?` + s["BUILD"] + `?`
		s["GTLT"] = `((?:<|>)?=?)`
		s["XRANGEIDENTIFIERLOOSE"] = s["NUMERICIDENTIFIERLOOSE"] + `|x|X|\*`
		s["XRANGEIDENTIFIER"] = s["NUMERICIDENTIFIER"] + `|x|X|\*`
		s["XRANGEPLAIN"] = `[v=\s]*(` + s["XRANGEIDENTIFIER"] + `)(?:\.(` + s["XRANGEIDENTIFIER"] + `)(?:\.(` + s["XRANGEIDENTIFIER"] + `)(?:` + s["PRERELEASE"] + `)?` + s["BUILD"] + `?)?)?`
		s["XRANGEPLAINLOOSE"] = `[v=\s]*(` + s["XRANGEIDENTIFIERLOOSE"] + `)(?:\.(` + s["XRANGEIDENTIFIERLOOSE"] + `)(?:\.(` + s["XRANGEIDENTIFIERLOOSE"] + `)(?:` + s["PRERELEASELOOSE"] + `)?` + s["BUILD"] + `?)?)?`
		s["LONETILDE"] = `(?:~>?)`
		s["LONECARET"] = `(?:\^)`
		return s
	}()

	reFull        = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^` + reSrc["FULLPLAIN"] + `$`) })
	reLoose       = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^` + reSrc["LOOSEPLAIN"] + `$`) })
	reXRangeLoose = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^` + reSrc["GTLT"] + `\s*` + reSrc["XRANGEPLAINLOOSE"] + `$`)
	})
	reTildeTrim  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(\s*)` + reSrc["LONETILDE"] + `\s+`) })
	reTildeLoose = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^` + reSrc["LONETILDE"] + reSrc["XRANGEPLAINLOOSE"] + `$`)
	})
	reCaretTrim  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(\s*)` + reSrc["LONECARET"] + `\s+`) })
	reCaretLoose = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^` + reSrc["LONECARET"] + reSrc["XRANGEPLAINLOOSE"] + `$`)
	})
	reComparatorLoose = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^` + reSrc["GTLT"] + `\s*(` + reSrc["LOOSEPLAIN"] + `)$|^$`)
	})
	reComparatorFull = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^` + reSrc["GTLT"] + `\s*(` + reSrc["FULLPLAIN"] + `)$|^$`)
	})
	reComparatorTrim = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`(\s*)` + reSrc["GTLT"] + `\s*(` + reSrc["LOOSEPLAIN"] + `|` + reSrc["XRANGEPLAIN"] + `)`)
	})
	reHyphenLoose = sync.OnceValue(func() *regexp.Regexp {
		return regexp.MustCompile(`^\s*(` + reSrc["XRANGEPLAINLOOSE"] + `)\s+-\s+(` + reSrc["XRANGEPLAINLOOSE"] + `)\s*$`)
	})
	reStar    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(<|>)?=?\s*\*`) })
	reGTE0    = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^\s*>=\s*0\.0\.0\s*$`) })
	reGTE0Pre = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^\s*>=\s*0\.0\.0-0\s*$`) })
	reBuild   = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(reSrc["BUILD"]) })
	reSpaces  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`\s+`) })
	reDigits  = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[0-9]+$`) })
)

// Version is a parsed semantic version (node-semver SemVer).
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string // prerelease identifiers; numeric ones compare numerically
	Build               []string
}

// String is node-semver's SemVer.version: major.minor.patch[-pre], without build metadata.
func (v *Version) String() string {
	s := strconv.FormatUint(v.Major, 10) + "." + strconv.FormatUint(v.Minor, 10) + "." + strconv.FormatUint(v.Patch, 10)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

var errVersion = errors.New("invalid version")

// ParseVersion parses s like `new SemVer(s, {loose})`.
func ParseVersion(s string, loose bool) (*Version, error) {
	s = strings.TrimSpace(s)
	if len(s) > maxVersionLength {
		return nil, fmt.Errorf("%w: longer than %d characters", errVersion, maxVersionLength)
	}
	re := reFull()
	if loose {
		re = reLoose()
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("%w: %q", errVersion, s)
	}
	v := &Version{}
	for i, p := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil || n > maxSafeInteger {
			return nil, fmt.Errorf("%w: component too large in %q", errVersion, s)
		}
		*p = n
	}
	if m[4] != "" {
		for _, id := range strings.Split(m[4], ".") {
			if reDigits().MatchString(id) {
				n, err := strconv.ParseUint(id, 10, 64)
				if err == nil && n <= maxSafeInteger {
					id = strconv.FormatUint(n, 10)
				}
			}
			v.Pre = append(v.Pre, id)
		}
	}
	if m[5] != "" {
		v.Build = strings.Split(m[5], ".")
	}
	return v, nil
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// numericID reports whether a prerelease identifier is numeric and its value.
func numericID(s string) (uint64, bool) {
	if !reDigits().MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > maxSafeInteger {
		return 0, false
	}
	return n, true
}

func compareIdentifiers(a, b string) int {
	an, aok := numericID(a)
	bn, bok := numericID(b)
	switch {
	case aok && bok:
		return cmpUint(an, bn)
	case aok:
		return -1
	case bok:
		return 1
	}
	return strings.Compare(a, b)
}

// Compare orders versions by precedence (build metadata is ignored).
func (v *Version) Compare(o *Version) int {
	if c := cmpUint(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpUint(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpUint(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch {
	case len(v.Pre) > 0 && len(o.Pre) == 0:
		return -1
	case len(v.Pre) == 0 && len(o.Pre) > 0:
		return 1
	}
	for i := 0; ; i++ {
		switch {
		case i >= len(v.Pre) && i >= len(o.Pre):
			return 0
		case i >= len(o.Pre):
			return 1
		case i >= len(v.Pre):
			return -1
		}
		if c := compareIdentifiers(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
}

// comparator is one node-semver Comparator; any is the empty comparator ("" matches everything).
type comparator struct {
	op  string // "", "<", "<=", ">", ">="
	v   *Version
	any bool
}

func (c comparator) value() string {
	if c.any {
		return ""
	}
	return c.op + c.v.String()
}

func (c comparator) isNull() bool { return c.value() == "<0.0.0-0" }

func (c comparator) test(v *Version) bool {
	if c.any {
		return true
	}
	r := v.Compare(c.v)
	switch c.op {
	case "":
		return r == 0
	case "<":
		return r < 0
	case "<=":
		return r <= 0
	case ">":
		return r > 0
	case ">=":
		return r >= 0
	}
	return false
}

func parseComparator(s string, loose bool) (comparator, error) {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	re := reComparatorFull()
	if loose {
		re = reComparatorLoose()
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		return comparator{}, fmt.Errorf("invalid comparator %q", s)
	}
	c := comparator{op: m[1]}
	if c.op == "=" {
		c.op = ""
	}
	if m[2] == "" {
		return comparator{any: true}, nil
	}
	v, err := ParseVersion(m[2], loose)
	if err != nil {
		return comparator{}, err
	}
	c.v = v
	return c, nil
}

// Range is a parsed node-semver range: a disjunction of comparator conjunctions.
type Range struct {
	set               [][]comparator
	includePrerelease bool
}

// rangeOpts mirrors node-semver's options object.
type rangeOpts struct{ loose, includePrerelease bool }

// ParseRange parses raw like `new Range(raw, {loose: true})`, the form npm uses for
// dependency specs.
func ParseRange(raw string) (*Range, error) { return parseRange(raw, rangeOpts{loose: true}) }

func parseRange(raw string, o rangeOpts) (*Range, error) {
	if len(raw) > 64<<10 {
		return nil, fmt.Errorf("invalid range: longer than 64 KiB")
	}
	raw = reSpaces().ReplaceAllString(strings.TrimSpace(raw), " ")
	r := &Range{includePrerelease: o.includePrerelease}
	for _, part := range strings.Split(raw, "||") {
		cs, err := parseRangePart(strings.TrimSpace(part), o)
		if err != nil {
			return nil, err
		}
		if len(cs) > 0 {
			r.set = append(r.set, cs)
		}
	}
	if len(r.set) == 0 {
		return nil, fmt.Errorf("invalid range %q", raw)
	}
	if len(r.set) > 1 {
		first := r.set[0]
		var kept [][]comparator
		for _, c := range r.set {
			if !c[0].isNull() {
				kept = append(kept, c)
			}
		}
		switch {
		case len(kept) == 0:
			r.set = [][]comparator{first}
		case len(kept) > 1:
			r.set = kept
			for _, c := range kept {
				if len(c) == 1 && c[0].any {
					r.set = [][]comparator{c}
					break
				}
			}
		default:
			r.set = kept
		}
	}
	return r, nil
}

// replaceAll is JS String.replace with a global regexp and a "$1…" replacement.
func replaceAll(re *regexp.Regexp, s, tmpl string) string {
	return re.ReplaceAllString(s, tmpl)
}

// replaceAnchored is JS String.replace with an anchored, non-global regexp and a function.
func replaceAnchored(re *regexp.Regexp, s string, f func(m []string) string) string {
	loc := re.FindStringSubmatchIndex(s)
	if loc == nil {
		return s
	}
	m := make([]string, len(loc)/2)
	for i := range m {
		if loc[2*i] >= 0 {
			m[i] = s[loc[2*i]:loc[2*i+1]]
		}
	}
	return s[:loc[0]] + f(m) + s[loc[1]:]
}

func parseRangePart(rng string, o rangeOpts) ([]comparator, error) {
	rng = reBuild().ReplaceAllString(rng, "")
	rng = replaceAnchored(reHyphenLoose(), rng, func(m []string) string { return hyphenReplace(m, o.includePrerelease) })
	rng = replaceAll(reComparatorTrim(), rng, "${1}${2}${3}")
	rng = replaceAll(reTildeTrim(), rng, "${1}~")
	rng = replaceAll(reCaretTrim(), rng, "${1}^")

	var comps []string
	for _, c := range strings.Split(rng, " ") {
		comps = append(comps, parseComparatorString(c, o))
	}
	var list []string
	for _, c := range reSpaces().Split(strings.Join(comps, " "), -1) {
		list = append(list, replaceGTE0(c, o))
	}
	if o.loose {
		var kept []string
		for _, c := range list {
			if reComparatorLoose().MatchString(c) {
				kept = append(kept, c)
			}
		}
		list = kept
	}
	var out []comparator
	seen := map[string]int{}
	for _, s := range list {
		c, err := parseComparator(s, o.loose)
		if err != nil {
			return nil, err
		}
		if c.isNull() {
			return []comparator{c}, nil
		}
		if i, ok := seen[c.value()]; ok {
			out[i] = c
			continue
		}
		seen[c.value()] = len(out)
		out = append(out, c)
	}
	if len(out) > 1 {
		if i, ok := seen[""]; ok {
			out = append(out[:i], out[i+1:]...)
		}
	}
	return out, nil
}

func parseComparatorString(comp string, o rangeOpts) string {
	comp = reBuild().ReplaceAllString(comp, "")
	comp = replaceEach(comp, func(c string) string { return replaceCaret(c, o) })
	comp = replaceEach(comp, func(c string) string { return replaceTilde(c, o) })
	comp = replaceEachRaw(comp, func(c string) string { return replaceXRange(c, o) })
	comp = reStarFirst(strings.TrimSpace(comp))
	return comp
}

func reStarFirst(s string) string {
	loc := reStar().FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + s[loc[1]:]
}

// replaceEach is node-semver's comp.trim().split(/\s+/).map(f).join(' ').
func replaceEach(comp string, f func(string) string) string {
	parts := reSpaces().Split(strings.TrimSpace(comp), -1)
	for i, p := range parts {
		parts[i] = f(p)
	}
	return strings.Join(parts, " ")
}

// replaceEachRaw is comp.split(/\s+/).map(f).join(' ') (no trim, as replaceXRanges).
func replaceEachRaw(comp string, f func(string) string) string {
	parts := reSpaces().Split(comp, -1)
	for i, p := range parts {
		parts[i] = f(p)
	}
	return strings.Join(parts, " ")
}

func isX(id string) bool { return id == "" || strings.ToLower(id) == "x" || id == "*" }

func plus1(s string) string {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return s + "1" // unreachable for matched digits; keeps the comparator invalid
	}
	return strconv.FormatUint(n+1, 10)
}

// num is JS unary plus on a matched numeric identifier (drops leading zeros).
func num(s string) string {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return s
	}
	return strconv.FormatUint(n, 10)
}

func replaceTilde(comp string, o rangeOpts) string {
	z := ""
	if o.includePrerelease {
		z = "-0"
	}
	return replaceAnchored(reTildeLoose(), comp, func(m []string) string {
		M, mi, p, pr := m[1], m[2], m[3], m[4]
		switch {
		case isX(M):
			return ""
		case isX(mi):
			return ">=" + M + ".0.0" + z + " <" + plus1(M) + ".0.0-0"
		case isX(p):
			return ">=" + M + "." + mi + ".0" + z + " <" + M + "." + plus1(mi) + ".0-0"
		case pr != "":
			return ">=" + M + "." + mi + "." + p + "-" + pr + " <" + M + "." + plus1(mi) + ".0-0"
		}
		return ">=" + M + "." + mi + "." + p + " <" + M + "." + plus1(mi) + ".0-0"
	})
}

func replaceCaret(comp string, o rangeOpts) string {
	z := ""
	if o.includePrerelease {
		z = "-0"
	}
	return replaceAnchored(reCaretLoose(), comp, func(m []string) string {
		M, mi, p, pr := m[1], m[2], m[3], m[4]
		switch {
		case isX(M):
			return ""
		case isX(mi):
			return ">=" + M + ".0.0" + z + " <" + plus1(M) + ".0.0-0"
		case isX(p):
			if M == "0" {
				return ">=" + M + "." + mi + ".0" + z + " <" + M + "." + plus1(mi) + ".0-0"
			}
			return ">=" + M + "." + mi + ".0" + z + " <" + plus1(M) + ".0.0-0"
		case pr != "":
			if M == "0" {
				if mi == "0" {
					return ">=" + M + "." + mi + "." + p + "-" + pr + " <" + M + "." + mi + "." + plus1(p) + "-0"
				}
				return ">=" + M + "." + mi + "." + p + "-" + pr + " <" + M + "." + plus1(mi) + ".0-0"
			}
			return ">=" + M + "." + mi + "." + p + "-" + pr + " <" + plus1(M) + ".0.0-0"
		}
		if M == "0" {
			if mi == "0" {
				return ">=" + M + "." + mi + "." + p + " <" + M + "." + mi + "." + plus1(p) + "-0"
			}
			return ">=" + M + "." + mi + "." + p + " <" + M + "." + plus1(mi) + ".0-0"
		}
		return ">=" + M + "." + mi + "." + p + " <" + plus1(M) + ".0.0-0"
	})
}

func replaceXRange(comp string, o rangeOpts) string {
	comp = strings.TrimSpace(comp)
	return replaceAnchored(reXRangeLoose(), comp, func(m []string) string {
		ret, gtlt, M, mi, p := m[0], m[1], m[2], m[3], m[4]
		if (isX(M) && !isX(mi)) || (isX(mi) && p != "" && !isX(p)) {
			return comp
		}
		xM := isX(M)
		xm := xM || isX(mi)
		xp := xm || isX(p)
		anyX := xp
		if gtlt == "=" && anyX {
			gtlt = ""
		}
		pr := ""
		if o.includePrerelease {
			pr = "-0"
		}
		switch {
		case xM:
			if gtlt == ">" || gtlt == "<" {
				ret = "<0.0.0-0"
			} else {
				ret = "*"
			}
		case gtlt != "" && anyX:
			if xm {
				mi = "0"
			}
			p = "0"
			switch gtlt {
			case ">":
				gtlt = ">="
				if xm {
					M = plus1(M)
					mi = "0"
				} else {
					mi = plus1(mi)
				}
				p = "0"
			case "<=":
				gtlt = "<"
				if xm {
					M = plus1(M)
				} else {
					mi = plus1(mi)
				}
			}
			if gtlt == "<" {
				pr = "-0"
			}
			ret = gtlt + num(M) + "." + num(mi) + "." + num(p) + pr
		case xm:
			ret = ">=" + M + ".0.0" + pr + " <" + plus1(M) + ".0.0-0"
		case xp:
			ret = ">=" + M + "." + mi + ".0" + pr + " <" + M + "." + plus1(mi) + ".0-0"
		}
		return ret
	})
}

func replaceGTE0(comp string, o rangeOpts) string {
	comp = strings.TrimSpace(comp)
	if !o.includePrerelease && reGTE0().MatchString(comp) {
		return ""
	}
	if o.includePrerelease && reGTE0Pre().MatchString(comp) {
		return ""
	}
	return comp
}

func hyphenReplace(m []string, incPr bool) string {
	// groups: 0 all, 1 from, 2 fM, 3 fm, 4 fp, 5 fpr, 6 fb, 7 to, 8 tM, 9 tm, 10 tp, 11 tpr, 12 tb
	from, fM, fm, fp, fpr := m[1], m[2], m[3], m[4], m[5]
	to, tM, tm, tp, tpr := m[7], m[8], m[9], m[10], m[11]
	z := ""
	if incPr {
		z = "-0"
	}
	switch {
	case isX(fM):
		from = ""
	case isX(fm):
		from = ">=" + fM + ".0.0" + z
	case isX(fp):
		from = ">=" + fM + "." + fm + ".0" + z
	case fpr != "":
		from = ">=" + from
	default:
		from = ">=" + from + z
	}
	switch {
	case isX(tM):
		to = ""
	case isX(tm):
		to = "<" + plus1(tM) + ".0.0-0"
	case isX(tp):
		to = "<" + tM + "." + plus1(tm) + ".0-0"
	case tpr != "":
		to = "<=" + tM + "." + tm + "." + tp + "-" + tpr
	case incPr:
		to = "<" + tM + "." + tm + "." + plus1(tp) + "-0"
	default:
		to = "<=" + to
	}
	return strings.TrimSpace(from + " " + to)
}

// Test reports whether v satisfies the range (node-semver Range.test, prerelease rule included).
func (r *Range) Test(v *Version) bool {
	for _, set := range r.set {
		if testSet(set, v, r.includePrerelease) {
			return true
		}
	}
	return false
}

func testSet(set []comparator, v *Version, includePrerelease bool) bool {
	for _, c := range set {
		if !c.test(v) {
			return false
		}
	}
	if len(v.Pre) > 0 && !includePrerelease {
		for _, c := range set {
			if c.any {
				continue
			}
			if len(c.v.Pre) > 0 && c.v.Major == v.Major && c.v.Minor == v.Minor && c.v.Patch == v.Patch {
				return true
			}
		}
		return false
	}
	return true
}

// IsAny reports whether the range is "*" (node-semver formats it as the empty string).
func (r *Range) IsAny() bool { return len(r.set) == 1 && len(r.set[0]) == 1 && r.set[0][0].any }

// Satisfies is semver.satisfies(version, rng, {loose: true}): false when either does not parse.
func Satisfies(version, rng string) bool {
	r, err := ParseRange(rng)
	if err != nil {
		return false
	}
	v, err := ParseVersion(version, true)
	if err != nil {
		return false
	}
	return r.Test(v)
}

// ValidVersion is semver.valid(s, true) != null.
func ValidVersion(s string) bool {
	_, err := ParseVersion(s, true)
	return err == nil
}

// satisfiesEngine is the engines.node check of npm-install-checks: strict parsing with
// includePrerelease.
func satisfiesEngine(version, rng string) bool {
	r, err := parseRange(rng, rangeOpts{includePrerelease: true})
	if err != nil {
		return false
	}
	v, err := ParseVersion(version, false)
	if err != nil {
		return false
	}
	return r.Test(v)
}

// String is node-semver's Range.range: comparators joined by " ", sets by "||".
func (r *Range) String() string {
	sets := make([]string, len(r.set))
	for i, set := range r.set {
		cs := make([]string, len(set))
		for k, c := range set {
			cs[k] = c.value()
		}
		sets[i] = strings.Join(cs, " ")
	}
	return strings.Join(sets, "||")
}
