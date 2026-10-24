// Package install holds the acceptance tests of the consumer side of releases (SPEC §02.8, §04
// §4.9): bob install, upgrade, rollback, uninstall, list, registries, profile generations, shims
// (R-INST-001 … R-INST-006).
//
// The tests run the bob binary against fakeserver (tests/cli/fakeserver), a protocol-faithful
// registry with a signed binary cache, because the real server is built later. The installed
// program is testdata/prog, compiled at test time with the version burned in.
package install
