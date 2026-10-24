// Package gen implements engine.Ecosystem for pkg/golang (SPEC §03.2, ADR 0004): bob reads Go
// sources itself (go/build, no cmd/go), plans one derivation per workspace package and per
// third-party module, std once per toolchain and target, the link and the test binaries, and
// generates go.mod/go.sum from bob.go and bob.lock.
package gen
