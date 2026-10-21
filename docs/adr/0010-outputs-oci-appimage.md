# ADR 0010 — OCI images and AppImages from closures

Status: **Accepted (locked) — owner at Gate S, 2026-10-21** (spike S8, 2026-10-21; SPEC §04). Evidence: `docs/spikes/s08-bundles.md`.

## Context

R-OUT-002/003 require multi-arch OCI images without Docker and AppImages that run on distros
without Nix. Store paths are absolute (`/nix/store/…`); hosts may lack FUSE and unprivileged
user namespaces.

## Decision

1. **OCI images are written by bob in Go** (stdlib tar/gzip/json), no Docker, no
   go-containerregistry. One layer per store path, ordered by popularity; the application
   roots always on their own top layer; least popular libraries merged above `MaxLayers`
   (default 100); a small customisation layer (`/etc/passwd`, `/etc/group`, cert and zoneinfo
   symlinks, `/tmp`). Defaults: user 65534, CA certs and tzdata included. Multi-platform index
   amd64+arm64. Byte-reproducible (rules in the write-up §5.3); layer cache keyed by store path.
   Measured: v2 of the fixture ships 15 % of the bytes vs 100 % for a single layer; crane
   validate PASS; rootless podman runs it.
2. **AppImage = type-2 runtime + squashfs of the closure at `nix/store` + bob's `AppRun`
   launcher.** The launcher uses a user+mount namespace (bind the bundled store at
   `/nix/store`, chroot) and falls back to running the bundled `ld.so --library-path` when
   namespaces are unavailable. Data paths are covered by env vars the builder sets
   (`SSL_CERT_FILE`, `GCONV_PATH`, `LOCALE_ARCHIVE`). Verified on Ubuntu 22.04/24.04, Debian 12,
   Fedora 43, Alpine 3.22 rootfs without Nix, FUSE or userns.
3. **The bundle carries a recent glibc and its own locales and certs**; host libraries are only
   used for GPU drivers, appended after bundled ones.
4. **Fixup**: strip and `--remap-path-prefix`/`remove-references-to` in bob-built packages;
   per-output pruning of glibc locale sources, translations, optional gconv and gcc sanitizer
   libraries. Fixture `api` AppImage: 21.2 MB → 10.8 MB.
5. **Runtime: bob's own static Go runtime** (A15). In v1 it always extracts the squashfs once
   to `$XDG_CACHE_HOME/bob/appimage/<sha256>/` and then acts as the launcher; FUSE mounting is
   roadmap. The upstream type-2 runtime is not used (it statically links LGPL-2.1 libfuse and
   exits 127 without `fusermount`).
6. **Images live in the store as per-layer derivations**: one derivation per layer with
   `unsafeDiscardReferences.out = true` (structured attrs; stable in Nix 2.34, no experimental
   feature flag needed — verified on Nix 2.34.8 and 2.34.9 at SPEC integration), and an image derivation whose layout
   symlinks to the layer outputs. This replaces the spike's out-of-store layer cache and keeps
   image closures free of the paths they contain.
7. squashfs is written by nixpkgs `mksquashfs` inside the bundle derivation (A16); OCI layers are
   gzip only in v1 (A17).

## Consequences

- No new external dependency for images; `mksquashfs` (GPL, build tool only, not shipped)
  inside the bundle derivation.
- ld.so mode can't run programs that exec other store paths or shebang scripts; bob warns at
  build time.
- To verify: real Ubuntu 24.04 host (AppArmor userns restriction), real GPUs, amd64 from S7.

## Owner amendments

- O-S8-1/2/3 settled by agent decisions A15, A16, A17 (above).
- D16/D52 (licence obligations): the own runtime keeps LGPL/GPL code out of user bundles; every
  bundle carries `licences.json` (SPEC R-OUT-008).
- D61: release files (AppImages, tarballs) and OCI blobs are downloaded through the orchestrator
  proxy, never presigned URLs (served by §06).
- D57/D56 do not change this ADR; bundle derivations run inside the isolated worker build like
  every other derivation.

## Prior art (read, not copied)

nixpkgs `dockerTools.streamLayeredImage` (popularity layering), nix2container, nix-bundle /
nix-user-chroot (userns store mount), AppImage type-2 specification and type2-runtime.
