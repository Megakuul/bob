// Package strutil has small string helpers shared by the api and tools modules.
package strutil

import "strings"

// Title upper-cases the first letter of every word.
func Title(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
