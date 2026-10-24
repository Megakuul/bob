package resolve

// localeCompare orders strings like npm's @isaacs/string-locale-compare('en') (ICU collation
// for "en", default strength). npm sorts its work queue and placements with it, so the order in
// which conflicts are met depends on it. Package names and paths are ASCII; for ASCII the ICU
// order is: primary weight by character class and position in iculOrder (letters case-folded),
// then lowercase before uppercase as the tertiary difference. The table was taken from
// `Intl.Collator('en')` of nodejs 24.21.0 (the pinned oracle) over the printable ASCII range.
const icuOrder = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789aAbBcCdDeEfFgGhHiIjJkKlLmMnNoOpPqQrRsStTuUvVwWxXyYzZ"

var primaryWeight, tertiaryWeight = func() ([128]int, [128]int) {
	var p, t [128]int
	w := 0
	for i := 0; i < len(icuOrder); i++ {
		c := icuOrder[i]
		if c >= 'A' && c <= 'Z' {
			p[c] = p[c+'a'-'A']
			t[c] = 1
			continue
		}
		w++
		p[c] = w
	}
	return p, t
}()

func weights(c byte) (int, int) {
	if c < 128 && primaryWeight[c] != 0 {
		return primaryWeight[c], tertiaryWeight[c]
	}
	// Non-ASCII or control: after everything known, by byte value (stable and rare).
	return 1000 + int(c), 0
}

func localeCompare(a, b string) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		pa, _ := weights(a[i])
		pb, _ := weights(b[i])
		if pa != pb {
			if pa < pb {
				return -1
			}
			return 1
		}
	}
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	for i := 0; i < n; i++ {
		_, ta := weights(a[i])
		_, tb := weights(b[i])
		if ta != tb {
			if ta < tb {
				return -1
			}
			return 1
		}
	}
	return 0
}
