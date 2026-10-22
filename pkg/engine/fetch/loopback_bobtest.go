//go:build bobtest

package fetch

import "os"

// testLoopbackHTTP: test builds (-tags bobtest) let unpinned plain http reach loopback
// addresses when BOB_TEST_HTTP_LOOPBACK=1 is set, so the acceptance tests' local registries and
// archive servers can be locked (decisions A108, A109). Without the env var, and in release
// builds, it is refused (BOB-E-3002, R-NIX-014).
func testLoopbackHTTP() bool { return os.Getenv("BOB_TEST_HTTP_LOOPBACK") == "1" }
