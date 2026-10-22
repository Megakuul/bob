package resolve

import "strings"

// CompareVersions orders opam versions (the Debian algorithm opam uses: alternating non-digit
// and digit runs; in non-digit runs '~' sorts before everything, even the end of the string,
// and letters sort before other characters; digit runs compare numerically).
func CompareVersions(a, b string) int {
	for a != "" || b != "" {
		i, j := nonDigits(a), nonDigits(b)
		if c := compareNonDigits(a[:i], b[:j]); c != 0 {
			return c
		}
		a, b = a[i:], b[j:]
		i, j = digits(a), digits(b)
		if c := compareDigits(a[:i], b[:j]); c != 0 {
			return c
		}
		a, b = a[i:], b[j:]
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func nonDigits(s string) int {
	i := 0
	for i < len(s) && !isDigit(s[i]) {
		i++
	}
	return i
}

func digits(s string) int {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}

// order is the weight of one character in a non-digit run; 0 is the end of the run.
func order(c byte) int {
	switch {
	case c == '~':
		return -1
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		return int(c)
	default:
		return int(c) + 256
	}
}

func compareNonDigits(a, b string) int {
	for k := 0; k < len(a) || k < len(b); k++ {
		var ca, cb int
		if k < len(a) {
			ca = order(a[k])
		}
		if k < len(b) {
			cb = order(b[k])
		}
		if ca != cb {
			return sign(ca - cb)
		}
	}
	return 0
}

func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return sign(len(a) - len(b))
	}
	return strings.Compare(a, b)
}

func sign(x int) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	}
	return 0
}
