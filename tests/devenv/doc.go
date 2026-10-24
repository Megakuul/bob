// Package devenv is the acceptance suite of the developer environment (SPEC §02.7, requirements
// R-DEV-001 to R-DEV-011; HARNESS.md §13.1): the shell hook in bash, zsh and fish through a pty,
// shims, bob env, bob exec, the LSP proxy, editor settings, bob dev and Vite live reload.
//
// Run it with `go test ./tests/devenv/...`. VS Code and JetBrains under bob dev are (manual).
package devenv
