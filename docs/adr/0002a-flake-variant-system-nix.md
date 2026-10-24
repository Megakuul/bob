# ADR 0002a — Flake variant: bob without an embedded Nix

- **Status:** Accepted by owner chat decision 2026-10-24 (A439). Amends ADR 0002 (locked) for the
  flake variant only; supersedes nothing else.
- **Date:** 2026-10-24
- **Amends:** ADR 0002 Decision 3 ("Distribution of the Nix binary inside bob": always embedded)
  and the R-NIX-020 sentence "`bob` embeds nixpkgs `nixStatic` 2.34.9", for bob built from the
  repository's `flake.nix`. Decisions 1, 2, 4 and 5 of ADR 0002 are unchanged for every variant.
- **Requirements:** R-NIX-020, R-NIX-021, R-NIX-022, R-NIX-024, R-NIX-025, R-ARCH-005
- **Decisions:** plan/decisions.md A440–A447 (unit N1, plan/progress/N1.md)

## Context

The owner decided (chat, 2026-10-24) that bob can be installed with Nix:
`nix profile add github:megakuul/bob`, or on NixOS
`environment.systemPackages = [ inputs.bob.packages.${pkgs.system}.default ];`. A Nix user already
has Nix, so bundling a second copy inside the bob binary adds 14 MB for nothing, and nixpkgs-style
packaging expects a package to reference its runtime dependencies through the store rather than
carry them. The owner: the flake build must not bundle Nix; bob then uses the Nix that installed it,
as a build variant; the default build (AppImage, source bootstrap) keeps the embedded static Nix.

Constraints that shape how "uses the Nix that installed it" can work (measured on this host,
plan/progress/N1.md):

- bob's namespace mounts its private store over `/nix` (ADR 0002 Decision 1). Inside it, the
  host's `/nix/store` is hidden, so no binary from the host store can run there by its store path,
  and a dynamically linked Nix cannot run at all (its libraries live in the hidden store).
- bob owns its Nix configuration and never uses or touches a system Nix (R-NIX-024, R-NIX-025):
  talking to the user's nix-daemon or reading `/etc/nix/nix.conf` is out.
- The error translator, the derivation JSON bob writes (strategy B) and the internal-json log
  parser are verified against Nix 2.34 only (S15 captures of 2.34.8 and 2.34.9).

## Decision

1. **Build variant, selected at compile time.** `go build -tags nixsystem` leaves out the embed
   (`embed_<arch>.go`, `nixbin.go`) and compiles `pkg/engine/store/nixsystem.go` instead. The
   absolute path of the Nix binary is set at link time:
   `-ldflags "-X github.com/megakuul/bob/pkg/engine/store.systemNix=/nix/store/…/bin/nix"`.
   No `nix.gz` is needed to build this variant. The default build is unchanged.
2. **Which Nix.** The flake pins it: `nixStatic` 2.34.9 from the nixpkgs revision that
   `scripts/fetch-nix.sh` embeds from (`cd4ea8d5…`, input `nixpkgs-nix`), i.e. the same Nix
   build as the embedded one, unstripped. Its store path is written into the bob binary, so it is
   part of bob's runtime closure: `nix profile add` and NixOS install it with bob, independent of
   `PATH` and of the Nix version the user runs. "The Nix that installed it" therefore means the Nix
   in bob's own closure, installed by the user's Nix, not the user's daemon or `nix` on `PATH`.
3. **How it runs.** On first use `store.Open` checks that binary once (absolute clean path, a
   statically linked ELF, `nix --version` reports Nix, not Lix, in the 2.34 series, at least
   2.34.0) and copies it to `<home>/bin/nix-<first 12 hex of sha256(path)>/bin/nix` with the same
   legacy symlinks, through a temporary directory and an atomic rename, exactly where and how the
   embedded Nix is extracted. Later runs only stat. From there on everything is identical to the
   embedded variant: the same private rootless store root, namespace, `nix.conf`, netrc,
   `NIX_*`/`XDG_*` isolation and flags. The host's `nix` is still never executed inside the
   namespace; only bob's copy in `<home>/bin` is.
4. **Refusal.** A Nix that fails the check gives `BOB-E-1026` (`nixpkgs mismatch: {what}`) whose
   hint is the fix: install bob from its flake without overriding its `nixpkgs-nix` input, or use
   the AppImage. A bob built with the tag but without the path gives `BOB-E-7003`. A dedicated
   code needs a codes.lock / codes.golden change, which is an owner item (NEEDS in N1.md).
5. **Builder reproducibility.** The flake regenerates `bob-builder` (`go generate
   ./pkg/engine/store`) with the Go of its `nixpkgs` input (1.26.9, the Go that generated
   `builders.go`) and fails, like `scripts/bootstrap.sh`, when the table differs. Bob-builder
   store paths, and with them all derivation hashes and cache keys, are therefore the same for the
   flake variant and the AppImage.

## Options rejected

- **Run the user's `nix` / nix-daemon:** violates R-NIX-024/025, and the user's Nix version (and
  Lix) is unknown, which breaks the error translation (principle 2).
- **Run the closure's Nix by its store path inside the namespace:** hidden by the `/nix` mount.
  Bind-mounting single host store paths into the private store would put paths into bob's store
  that its database does not know.
- **Dynamically linked `nixVersions.nix_2_34`:** needs its library closure inside the namespace;
  the static build needs nothing.
- **Fallback to the embedded Nix in the flake build:** defeats the owner's "must not bundle".

## Consequences

- The flake variant ships no Nix inside the bob binary; Nix (LGPL-2.1-or-later, with busybox
  GPL-2.0 inside the static binary) reaches the user as a separate store path from nixpkgs,
  delivered by Nix itself. ATTRIBUTION.md records that the embedding obligations apply to the
  AppImage/bootstrap builds only.
- First use copies about 40 MB (the unstripped static binary) into `<home>/bin` instead of
  extracting 14 MB; it is a one-time cost per state home.
- `inputs.bob.inputs.nixpkgs.follows` or `…nixpkgs-nix.follows` changes the Go or the Nix: the
  first fails the build (builder table), the second is refused at run time unless it is another
  static Nix 2.34. The install documentation says not to override bob's inputs.
- `bob version` keeps printing `NixVersion` (2.34.9), which is what the flake pins.
