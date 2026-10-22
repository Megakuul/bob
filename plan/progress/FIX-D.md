# FIX-D progress
DONE F23 log_live_test, F9 keys (ULID strict; invalid-id left as is, owner item)
DONE F1 R-STORE-004 (+branch, role users, auth miss; build-by-number & release-by-version not reachable: no RPC / no release in acme -> report)
DONE F2 F3 retention
DONE F11 lease chaos
DONE F22 lake compaction
DONE F28 (AttachStreams==0, new live checkout test tests/server/checkout_live_test.go)
DONE F30 chaos (lease sampler, terminal-stability; writer-kill-between-writes not black-box)
DONE F32 intent raw dangling + reverse
DONE F33 rbac seeded resolving rows
DONE F37 F38(widened overlap only)
DONE F48
DONE F53 UI-007
DONE F54
DONE owner-items
VERIFIED all touched pkgs: vet/gofmt/boblint clean; reds = not implemented
