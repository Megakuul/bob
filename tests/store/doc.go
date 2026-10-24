// Package store holds the acceptance tests of the server data layer (SPEC §05.1–§05.4):
// R-STORE and the start-up policy self-check R-SEC-102. Tests that need S3 run against a
// fresh RustFS behind the counting proxy (tests/store/env). The fleet suite (R-FLEET) is the
// sub-package tests/store/fleet and runs with `go test ./tests/store/...`.
//
// Records: the SPEC record types carry dynamitedb field types, and go.mod has no dynamitedb
// yet. The tests therefore use the small record rec (rec_test.go) and the SPEC primitives only;
// when dynamitedb is added to go.mod, rec_test.go is the one file that changes.
package store
