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

### Projects and the CLI

| Item | Status | Workaround |
|---|---|---|
| dev servers (`DevShell.Servers`, `--servers-only`) and Vite live reload | not implemented yet | start the server inside the dev shell (`bob dev -- vite`) |
| `bob gc` | `BOB-E-9008` | delete `~/.local/share/bob` when no bob command runs |
| `bob gen --check` | `BOB-E-9008` | `bob status` and `bob check` |
| `bob check` on generated code directories | not checked | `bob status` shows `edited` |
| `bob pipeline run --remote` | `BOB-E-9008` | start pipelines from a forge, the web UI or `BuildService/CreateBuild` |
| lock change lines of the `cc`, `rust` and `opam` sections | one line per section (`+ rust`), without entry names | read `bob.lock` in the diff |
| `bob doctor --repair` | not tried for this documentation | — |

### Ecosystems

| Item | Status |
|---|---|
| OCaml for another platform | refused (`BOB-E-0011`) |
| opam packages with patches or extra sources | not supported |
| C/C++ and OCaml packages of dependency repositories (`bob.Import`) | not implemented (`BOB-E-9008`) |
| cross-compiling a Meson `cc.External` | not implemented |
| Go export-data early cutoff | implemented, off by default (`BOB_GO_CUTOFF=1`) |

### nixpkgs packages

The typed nixpkgs packages (`nixpkgs.Openssl`, …) are generated for the nixpkgs revision bob
was released with (`da6dc0e7b260` of `nixos-26.05` for this bob).

| Item | Status | Workaround |
|---|---|---|
| members of language package sets (`python3Packages.*`, `ocamlPackages.*`, `haskellPackages.*`, …) | not typed handles | use the ecosystem package for that language; for anything else ask for an entry in `cmd/nixpkgsgen/include.json` (bob's source) |
| package sets, aliases, broken and insecure packages, packages that fail to evaluate or exist on neither Linux system | excluded (`cmd/nixpkgsgen/exclude.json`) | use the package an alias points to; broken, insecure and failing packages have no typed handle |
| a package newer than bob's revision | cannot be named until a newer bob | a newer bob, or an `include.json` entry |
| another pin than bob's revision (`bob lock --update nixpkgs`, `NixpkgsRev`) | every used package is checked against the pin; a missing one is `BOB-E-1001` | move the pin back (`bob lock --nixpkgs da6dc0e7b26060f2f71ce3ef7088fb56ee44fa36`) or use another package |
| `bob version` printing the revision of the typed packages | not implemented yet | it is `da6dc0e7b260` (above) |

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
