// Package devenv implements the pieces of the developer environment of SPEC §02.7 that do not
// need the engine: .bob/env.json and the shims, the shell hook scripts and the enter/leave
// scripts of bob env, the trust list, the hook's eval-key stamps, the LSP path-rewriting proxy
// and the editor settings. pkg/cli wires them to the engine (build.Dev) and the namespace.
package devenv
