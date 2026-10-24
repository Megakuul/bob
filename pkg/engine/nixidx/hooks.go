//go:build !bobtest

package nixidx

// testMissing is the fault hook of R-NIX-032 (fault 90); it exists only with -tags bobtest.
func testMissing(string) bool { return false }
