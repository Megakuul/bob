// Package cli holds the acceptance tests of SPEC §02 (command line): the command set and its
// --help output, exit codes, the --json event stream (schema v1), human output rules, logs,
// login, completion, and the check, lock, graph and pipeline commands (R-CLI-001 … R-CLI-012).
//
// The tests drive the bob binary through tests/harness. Programs that only one requirement
// needs live in testdata/repos. The fake bob server of the login tests is package fakeserver.
package cli
