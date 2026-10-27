//go:build bobtest

package nixidx

import (
	"os"
	"slices"
	"strings"
)

// testMissing is the fault hook of R-NIX-032 (fault 90), compiled in only with -tags bobtest:
//
//	BOB_TEST_NIXPKGS_MISSING=<attr>[,<attr>…]   the pinned index reports these attributes missing
func testMissing(attr string) bool {
	v := os.Getenv("BOB_TEST_NIXPKGS_MISSING")
	return v != "" && slices.Contains(strings.Split(v, ","), attr)
}
