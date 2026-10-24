//go:build !bobtest

package netguard

// testLoopback is false in release builds: loopback addresses are refused unless the
// configuration allows them. See loopback_bobtest.go.
func testLoopback() bool { return false }
