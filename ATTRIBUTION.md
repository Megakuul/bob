# Attribution

bob is licensed under AGPL-3.0 (see `LICENSE`). This file lists third-party material that bob
ships or embeds, and code taken under the cleanroom exception (AGENTS.md §5.7, D42).

Every file under `assets/` and `web/static/`, and every embedded binary, needs a row here. The
`licence` CI stage fails when a file under those directories has none (a row names the file or a
parent directory in backticks). Go and npm dependency licences are checked separately against
`tools/ci/licences.allow`; they are listed here only when they carry an obligation or an owner
decision.

## Embedded binaries

| Item | Version | Licence | Where | Obligations | Decision |
|---|---|---|---|---|---|
| Static Nix (stripped, gzip-9, embedded in `bob`) | 2.34.9 (ADR 0002) | LGPL-2.1-or-later, plus the licences of its static dependencies | embedded in `cmd/bob` (path set when embedding lands) | Ship the licence text; offer the corresponding source of the exact Nix build (and the means to relink, LGPL-2.1 §6) with every release | D50, D52 |
| busybox (sandbox shell of static Nix) | as pinned with Nix | GPL-2.0-only | inside the embedded Nix closure | Ship the licence text; provide the complete corresponding source with every release | D52 |

**[open, release process]** The full list of static dependencies of the Nix build (and their
licences) is generated from the pinned derivation when embedding lands; it is appended to the table
above and to the release notes.

## Build-time tools (not shipped)

| Item | Licence | Use | Decision |
|---|---|---|---|
| squashfs-tools `mksquashfs` (nixpkgs) | GPL-2.0-or-later | Build tool inside the bundle derivation; not linked into or shipped with bob or its bundles | A16 |

The AppImage runtime is bob's own static Go code, so no libfuse is shipped (A15).

## Dependencies with a recorded decision

| Dependency | Licence | Note | Decision |
|---|---|---|---|
| `codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3` | MIT (checked against v3.0.0 `LICENSE`) | Forgejo adapter; MIT is on the allowlist, so it needs no exception | A12 |

## Assets

None yet. Fonts, icons and images under `assets/` or `web/static/` get a row here when added.

| Path | Source | Licence | Notes |
|---|---|---|---|

## Code taken under the cleanroom exception (D42)

None.
