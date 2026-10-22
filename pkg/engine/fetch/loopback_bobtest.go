//go:build bobtest

package fetch

// testLoopbackHTTP: test builds (-tags bobtest) let unpinned plain http reach loopback
// addresses, so the acceptance tests' local registries and archive servers can be locked
// (decision A108). Release builds refuse it (BOB-E-3002, R-NIX-014).
const testLoopbackHTTP = true
