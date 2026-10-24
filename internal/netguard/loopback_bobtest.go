//go:build bobtest

package netguard

// testLoopback: test builds (-tags bobtest, the harness's bob-server stacks) may reach loopback
// addresses, where the harness runs its forges and webhook receivers (decision A396). Private,
// link-local and the other refused ranges stay refused.
func testLoopback() bool { return true }
