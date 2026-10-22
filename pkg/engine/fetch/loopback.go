//go:build !bobtest

package fetch

// testLoopbackHTTP is false in release builds: unpinned plain http is always refused
// (BOB-E-3002, R-NIX-014). See loopback_bobtest.go.
const testLoopbackHTTP = false
