package serverenv

import (
	"os"
	"strings"
)

// BobtestTagsFlag returns the `-tags=…` argument for building bob, bob-server and bob-builder in
// tests: always bobtest, merged with the -tags of the caller's GOFLAGS (a command line -tags
// would otherwise replace them). Decision A109.
func BobtestTagsFlag() string {
	tags := []string{"bobtest"}
	for _, f := range strings.Fields(os.Getenv("GOFLAGS")) {
		for _, p := range []string{"-tags=", "--tags="} {
			if v, ok := strings.CutPrefix(f, p); ok {
				for _, t := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
					if t != "bobtest" {
						tags = append(tags, t)
					}
				}
			}
		}
	}
	return "-tags=" + strings.Join(tags, ",")
}
