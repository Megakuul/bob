// Package goname is the naming rule of bob's typed nixpkgs surface (ADR 0020 §2), shared by
// the pin check of pkg/engine/nixidx and the generator cmd/nixpkgsgen. It imports nothing but
// the standard library.
package goname

import "strings"

// Of is the Go identifier of a nixpkgs attribute in bob's typed surface (ADR 0020 §2,
// cmd/nixpkgsgen): the attribute split at '-', '_' and '.', each part's first letter
// upper-cased, the parts joined with '_' kept only between a part ending in a digit and one
// starting with a digit; a leading digit gets the prefix "N"; "'" becomes "Prime" and "+"
// becomes "Plus". (The generator replaces colliding names by "A_<attr>" fallbacks.)
func Of(attr string) string {
	attr = strings.NewReplacer("'", "Prime", "+", "Plus").Replace(attr)
	parts := strings.FieldsFunc(attr, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	var b strings.Builder
	prev := ""
	for _, p := range parts {
		if prev != "" && isDigit(prev[len(prev)-1]) && isDigit(p[0]) {
			b.WriteByte('_')
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		prev = p
	}
	s := b.String()
	if s != "" && isDigit(s[0]) {
		s = "N" + s
	}
	return s
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
