---
title: Limitations and status
section: Reference
order: 3
summary: What works today, what is not finished, and the workarounds - checked against the current code.
---

bob is young. This page says plainly what works, what does not work yet, and what to do
instead. It is updated together with the code: implementation changes that touch a public
surface must update the documentation in the same change.

## What works

Checked end to end for this documentation, with a bob built from the current source:

- The quickstarts for Go, C/C++, Rust, TypeScript, OCaml and protobuf code generation: lock,
  build, test, run, dev shells, `bob check`, `bob gen`, `bob status`.
- Multi-language builds (Go with cgo, a C++ library with OpenSSL, a Rust static library),
  cross-compiling Go for the other architecture, tests of the other architecture under
  qemu-user.
- OCI images (also run with podman), closure tarballs, AppImages and release manifests.
- `bob.Run`, `bob.Fetch`, PostgreSQL as a test service, local pipelines.
- The shell hook in bash, `bob allow`/`deny`, `.bob/bin` shims, `bob editor`.
- A single-host server on RustFS: orchestrator, first admin, worker registration, device
  login, remote builds, the binary cache, the OCI registry with podman, a release published by
  a pipeline run, release downloads, `bob install`/`upgrade`/`uninstall`, roles, service
  accounts and `BOB_TOKEN`.

## Not finished yet

### Installing

| Item | Status | Workaround |
|---|---|---|
| Nix profile, NixOS and AppImage installs | new; being verified | build from source (Install, "For contributors") |
| `scripts/bootstrap.sh` self-build | fails with `BOB-E-9003` (bob-builder reference) | use `.bob/bootstrap/bob` or `go build ./cmd/bob` |

### Projects and the CLI

| Item | Status | Workaround |
|---|---|---|
| a new repository needs a hand-written `go.mod` | `BOB-E-9005 … the repository has no go.mod` without it | create `go.mod` with `module <path>` and `go 1.26.0` |
| `go.mod` with `go 1.26` (two parts) | reported as a hand edit (`BOB-E-1011`) | write `go 1.26.0`, or run `bob gen --force` |
| a root `bob.go` in package `main` | does not evaluate | keep `main` packages in subdirectories (`cmd/<name>`) |
| relative labels (`:name`, `.`, `./...`) | refused | write labels from the root (`//dir:name`) |
| `bob run //dir` (short label) | `BOB-E-0003` | `bob run //dir:name` |
| `bob dev -- <toolchain command>` | `BOB-E-0005` | `bob dev -- sh -c '<command>'`, or `bob exec -- <command>` |
| dev servers (`DevShell.Servers`, `--servers-only`) | not started | start them inside `bob dev` (`bob dev -- sh -c 'vite'`) |
| C/C++ compiler in the dev shell | not added automatically | add `nixpkgs.Tool(p, "gcc")`, `nixpkgs.Tool(p, "clang-tools")` |
| `bob gc` | `BOB-E-9008` | delete `~/.local/share/bob` when no bob command runs |
| `bob gen --check` | `BOB-E-9008` | `bob status` and `bob check` |
| `bob check` on generated code directories | not checked | `bob status` shows `edited` |
| `bob pipeline run --remote` | `BOB-E-9008` | start pipelines from a forge, the web UI or `BuildService/CreateBuild` |
| `bob lock --update <go module>` | moves the module to its newest version despite `golang.Require` | change the version in `bob.go` and run `bob lock` |
| `bob lock --offline` | the Go resolver still downloads | — |
| lock change lines for some sections (`+ cc cc`, `~ go go →`) | printed without names | read `bob.lock` in the diff |
| test failure excerpts | may include a raw `bob:: {"type":"test-result",…}` line | ignore it; `bob log <label>` has the full log |
| `bob doctor --repair` | not tried for this documentation | — |

### Ecosystems

| Item | Status |
|---|---|
| OCaml for another platform | refused (`BOB-E-0011`) |
| opam packages with patches or extra sources | not supported |
| C/C++ and OCaml packages of dependency repositories (`bob.Import`) | not implemented (`BOB-E-9008`) |
| cross-compiling a Meson `cc.External` | not implemented |
| Go export-data early cutoff | implemented, off by default (`BOB_GO_CUTOFF=1`) |

### Server

| Item | Status | Workaround |
|---|---|---|
| remote build of an image whose program was published before | promotion fails (`artifact verification`) | see [OCI registry](../06-server/09-oci-registry.md) |
| TLS | not built in | a TLS reverse proxy in front |
| `server.trusted_proxies`, `worker.min_free`, `bob-server.yaml` | not implemented | — |
| worker capabilities and labels | no setting; one class per platform | — |
| `bob log --build` | runs; printed nothing for the trial builds | the build page of the web UI |
| OIDC and forge webhooks | tested by the server suites, not tried against real providers here | — |
| release binaries of bob-server | not published | build from source |

### Not verified on the documentation host

zsh and fish (hooks and completion scripts), the AppArmor fix on a real Ubuntu 24.04, VS Code
and JetBrains sessions, and multi-orchestrator deployments (covered by the harness's chaos
tests, not run here).

## Next steps

- [FAQ](04-faq.md).
- [Specification](01-spec.md).
