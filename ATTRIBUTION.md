# Attribution

bob is licensed under AGPL-3.0 (see `LICENSE`). This file lists third-party material that bob
ships or embeds, and code taken under the cleanroom exception (AGENTS.md §5.7, D42).

Every file under `assets/`, `web/static/` and `website/static/`, and every embedded binary, needs a row here. The
`licence` CI stage fails when a file under those directories has none (a row names the file or a
parent directory in backticks). Go and npm dependency licences are checked separately against
`tools/ci/licences.allow`; they are listed here only when they carry an obligation or an owner
decision.

## Embedded binaries

| Item | Version | Licence | Where | Source | Obligations | Decision |
|---|---|---|---|---|---|---|
| Static Nix (stripped, gzip-9, embedded in `bob`) | 2.34.9 (ADR 0002) | LGPL-2.1-or-later, plus the licences of its static dependencies | `pkg/engine/store/nixbin/<arch>/nix.gz` (fetched by `scripts/fetch-nix.sh`), embedded in `cmd/bob` | https://github.com/NixOS/nix/tree/2.34.9; built by the derivations `s55n25gmlcyyhvchm0bdcfdqf2nqyxxp-nix-static-aarch64-unknown-linux-musl-2.34.9.drv` / `yxd2ay3ha18vcx4n0gw7f5d1f7xhps3w-nix-static-x86_64-unknown-linux-musl-2.34.9.drv` of nixpkgs https://github.com/NixOS/nixpkgs/tree/cd4ea8d57de16f4810bc5f14207bac45fbf19f67 | Ship the licence text; offer the corresponding source of the exact Nix build (and the means to relink, LGPL-2.1 §6) with every release | D50, D52 |
| busybox (sandbox shell of static Nix) | as pinned with Nix (the version in the closure of the derivations above) | GPL-2.0-only | inside the embedded Nix binary | https://busybox.net/downloads/ (the version in the closure of the Nix derivations above) | Ship the licence text; provide the complete corresponding source with every release | D52 |

## Embedded data

| Item | Version | Licence | Where | Source | Obligations | Decision |
|---|---|---|---|---|---|---|
| Mozilla CA certificate bundle (fallback roots when the host has no CA bundle) | NSS 3.126 (nixpkgs `nss-cacert-3.126`, file `etc/ssl/certs/ca-no-trust-rules-bundle.crt`, derivation `ry9maqmzlm3ymsmyn7xk88mva4hnrrwj-nss-cacert-3.126.drv`) | MPL-2.0 | `pkg/engine/store/cacert/ca-bundle.crt`, embedded in `cmd/bob` | https://hg.mozilla.org/projects/nss (`lib/ckfw/builtins/certdata.txt`), via nixpkgs `cacert` | Ship the MPL-2.0 text and name the source; the file is unmodified (MPL-2.0 is compatible with AGPL-3.0 as a secondary licence) | A242 (plan/progress/I-B33.md) |

**[open, release process]** The full list of static dependencies of the Nix build (and their
licences) is generated from the pinned derivation when embedding lands; it is appended to the table
above and to the release notes.

## Build-time tools (not shipped)

| Item | Licence | Use | Decision |
|---|---|---|---|
| squashfs-tools `mksquashfs` (nixpkgs) | GPL-2.0-or-later | Build tool inside the bundle derivation; not linked into or shipped with bob or its bundles | A16 |
| qemu-user, binutils, strace, fish, skopeo, crane, mesa (lavapipe), chromium, opam, bun, rustc/cargo, gopls, clangd, rust-analyzer, typescript (nixpkgs, `tools/pins.json`) | GPL/LGPL/MPL/Apache/MIT mix, per package | Test-harness tools run as separate processes by the acceptance tests; never linked into, embedded in or shipped with bob | W2 |

The AppImage runtime is bob's own static Go code, so no libfuse is shipped (A15).

## Dependencies with a recorded decision

| Dependency | Licence | Note | Decision |
|---|---|---|---|
| `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3` | MIT (checked against v3.0.0 `LICENSE`) | Forgejo adapter (`internal/forge/forgejo`), v3.0.0, with `github.com/42wim/httpsig` v1.2.3 and `github.com/go-fed/httpsig` v1.1.0 (BSD-3-Clause), `github.com/davidmz/go-pageant` v1.0.2 (MIT), `github.com/hashicorp/go-version` v1.7.0 (MPL-2.0); MIT is on the allowlist, so it needs no exception | A12 |
| `github.com/google/go-github/v88` v88.0.0 with `github.com/google/go-querystring` v1.2.0 | BSD-3-Clause (checked against each `LICENSE`) | GitHub adapter (`internal/forge/github`), the official SDK required by D48 | I-B10 (plan/progress/I-B10.md) |
| `gitlab.com/gitlab-org/api/client-go` v1.46.0 with `github.com/hashicorp/go-retryablehttp` v0.7.8, `github.com/hashicorp/go-cleanhttp` v0.5.2 (MPL-2.0) and `golang.org/x/time` v0.14.0 (BSD-3-Clause) | Apache-2.0 (checked against `LICENSE`; the licence stage misreads the module's `license.go` source files as licence texts, see plan/progress/I-B10.md NEEDS) | GitLab adapter (`internal/forge/gitlab`), GitLab's official Go client (SPEC §6.9.1). MPL-2.0 is file-level copyleft, compatible with AGPL-3.0 as a secondary licence; bob ships those files unmodified | I-B10 (plan/progress/I-B10.md) |
| `github.com/ulikunitz/xz` v0.5.17 | BSD-3-Clause (checked against `LICENSE`) | `.tar.xz` unpacking in `pkg/engine/fetch` (R-NIX-014) | I-M2a (plan/progress/I-M2a.md) |
| `connectrpc.com/connect` v1.21.1 (+ `cmd/protoc-gen-connect-go` as a Go tool) | Apache-2.0 (checked against `LICENSE`) | connect API server (R-API-001, D5) and its code generator; v1 as in the S17 spike | I-B3 (plan/progress/I-B3.md) |
| `google.golang.org/protobuf` v1.36.12 (+ `cmd/protoc-gen-go` as a Go tool) | BSD-3-Clause (checked against `LICENSE`) | generated protobuf messages in `internal/gen` and their generator | I-B3 (plan/progress/I-B3.md) |
| `github.com/coreos/go-oidc/v3` v3.17.0 | Apache-2.0 (checked against `LICENSE`) | OIDC relying party: discovery, ID-token verification (R-AUTH-001, SPEC §6.3.2) | I-B4 (plan/progress/I-B4.md) |
| `github.com/go-jose/go-jose/v4` v4.1.3 (dependency of go-oidc) | Apache-2.0 (checked against `LICENSE`) | JWS verification inside go-oidc | I-B4 (plan/progress/I-B4.md) |
| `golang.org/x/oauth2` v0.36.0 | BSD-3-Clause (checked against `LICENSE`) | OAuth 2.0 code exchange with PKCE towards OIDC providers (SPEC §6.3.2) | I-B4 (plan/progress/I-B4.md) |
| `golang.org/x/crypto` v0.58.0 (`argon2`) | BSD-3-Clause (checked against `LICENSE`) | argon2id password hashes (SPEC §6.3.1, R-AUTH-151) | I-B4 (plan/progress/I-B4.md) |
| `github.com/megakuul/lake` v0.5.3 (= commit 045e301, SPEC §05.10.1) with `github.com/parquet-go/parquet-go` (Apache-2.0), `parquet-go/bitpack` (Apache-2.0), `parquet-go/jsonlite` (MIT), `google/uuid` (BSD-3-Clause), `andybalholm/brotli` (MIT), `pierrec/lz4/v4` (BSD-3-Clause), `twpayne/go-geom` (BSD-2-Clause) | MIT (checked against each `LICENSE`) | build logs and analytics in lake (R-LOG-001, D20, ADR 0012), `internal/logstore`; all permissive and AGPL-compatible | I-B7 (plan/progress/I-B7.md) |
| `github.com/klauspost/compress` v1.20.3 (`zstd`) | BSD-3-Clause, with Apache-2.0 and MIT parts (checked against `LICENSE`) | `.tar.zst` unpacking in `pkg/engine/fetch` (R-NIX-014); all parts permissive and AGPL-compatible | I-M2a (plan/progress/I-M2a.md) |

## Assets

None yet. Fonts, icons and images under `assets/`, `web/static/` or `website/static/` get a row here when added. The website (`website/`) uses system font stacks and inline SVG only, so it bundles no font or image files (A412).

| Path | Source | Licence | Notes |
|---|---|---|---|
| `pkg/rust/resolve/testdata/index.tar.gz` | 32 crates.io sparse-index files (index.crates.io metadata: names, versions, checksums, dependency tables) | crates.io data policy: index metadata is public and redistributable; no crate sources | Offline Cargo-equality test of the crate resolver (I-R-crates) |

## Code taken under the cleanroom exception (D42)

| Where | Source | Licence | Reason (decision) |
|---|---|---|---|
| `pkg/npm/resolve` (tree building: `arborist.go`, `place.go`, `tree.go`; `semver.go` incl. its regular expressions) | translation to Go of npm 11.19.0 `@npmcli/arborist` and `node-semver` (github.com/npm/cli, github.com/npm/node-semver) | ISC (permissive, AGPL-compatible) | R-NPM-001a requires results equal to npm, including peer placement; the behaviour is defined by that code and cannot be matched from a spec alone (A85, I-R-npm). The licence notice of both projects stays in the file headers. |
