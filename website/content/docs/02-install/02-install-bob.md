---
title: Install bob
section: Install
order: 2
summary: Install bob with Nix, on NixOS, or as an AppImage, then check it with bob doctor and set up shell completion.
---

You install `bob`, check that it can create its store, and turn on shell completion. Pick one
of three methods.

| Method | Good for | Nix that bob uses |
|---|---|---|
| [Nix profile](#with-nix) | machines that already have Nix | static Nix 2.34.9, installed with bob from its flake |
| [NixOS flake](#on-nixos) | NixOS systems configured with flakes | static Nix 2.34.9, installed with bob from its flake |
| [AppImage](#as-an-appimage) | any Linux machine, no Nix needed | static Nix 2.34.9, embedded in bob |

Your own Nix only installs bob; it needs flakes (see below) and its version does not matter for
builds. The `nix profile add` command is the current name; Nix releases from before the rename
call it `nix profile install`. The Nix methods were tested with Nix 2.34.

> [!NOTE]
> The AppImage files are produced by the release workflow on the first tagged release; until
> then the download links below return 404.

## With Nix

With flakes enabled, install bob into your profile:

```bash
nix profile add github:megakuul/bob
```

Without flakes enabled in your Nix configuration, add the flag:

```bash
nix --extra-experimental-features 'nix-command flakes' profile add github:megakuul/bob
```

The flake supports `x86_64-linux` and `aarch64-linux`. It also has `packages.bob-server`, an
overlay (`overlays.default`) and a dev shell for working on bob.

To try bob without installing it:

```bash
nix run github:megakuul/bob -- --help
```

## On NixOS

Add bob as a flake input and install its package:

```nix
{
  inputs.bob.url = "github:megakuul/bob";

  outputs = { nixpkgs, ... }@inputs: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      specialArgs = { inherit inputs; };
      modules = [
        ({ pkgs, inputs, ... }: {
          environment.systemPackages = [ inputs.bob.packages.${pkgs.system}.default ];
        })
      ];
    };
  };
}
```

Then run `sudo nixos-rebuild switch`. On current nixpkgs, `pkgs.system` prints a rename
warning; `pkgs.stdenv.hostPlatform.system` is the same value without it.

> [!WARNING]
> Do not set `inputs.bob.inputs.nixpkgs.follows` (or `nixpkgs-nix.follows`). bob's flake pins the
> Go that reproduces bob's builder bit for bit and the Nix bob is tested with; with another
> nixpkgs the build fails, or bob refuses its Nix with `BOB-E-1026`.

## As an AppImage

The AppImage is one file that runs on any Linux machine. It carries its own static Nix, so
the machine needs no Nix:

```bash
mkdir -p ~/.local/bin
curl -fLo ~/.local/bin/bob https://github.com/megakuul/bob/releases/latest/download/bob-x86_64.AppImage
chmod +x ~/.local/bin/bob
```

On arm64 machines, download `bob-aarch64.AppImage` instead. Make sure `~/.local/bin` is on
your `PATH`. Each release also has `bob-x86_64.AppImage.sha256` and
`bob-aarch64.AppImage.sha256`:

```bash
cd ~/.local/bin
curl -fLO https://github.com/megakuul/bob/releases/latest/download/bob-x86_64.AppImage.sha256
sed 's/bob-x86_64.AppImage/bob/' bob-x86_64.AppImage.sha256 | sha256sum -c
```

## Which Nix bob uses

bob always keeps its own store in `~/.local/share/bob` (or `$BOB_HOME`) and its own
configuration, so a bob build never changes your system's Nix store.

- **Nix profile and NixOS installs** contain no Nix inside the bob binary. bob's flake pins
  nixpkgs' static Nix 2.34.9 and installs it with bob as part of bob's closure; on first use bob
  copies that binary into its state directory and runs it there. Your system Nix and its daemon
  are never used by builds.
- **The AppImage and source builds** run the same static Nix (2.34.9), embedded in the bob binary
  and unpacked on first use.
- In both cases a system Nix, if there is one, is hidden from bob, and bob accepts only Nix 2.34
  (from 2.34.0): a bob built against another Nix stops with `BOB-E-1026` and the fix.

## Check the installation

```bash
bob version
```

```text
bob v0.0.0-dev
nix 2.34.9
nixpkgs nixos-26.05
```

`bob doctor` enters bob's namespace and verifies the store and your configuration files:

```bash
bob doctor
```

```text
ok      namespace
ok      builder
ok      store
ok      config

bob: no problems
```

If user namespaces are blocked, you get `BOB-E-7005` with the fix for your system (see
[Requirements](01-requirements.md#unprivileged-user-namespaces)). If the store is damaged,
`bob doctor --repair` verifies every file and repairs what it can.

To keep bob's state on another disk, set `BOB_HOME`:

```bash
export BOB_HOME=/data/bob
```

## Shell completion

bob completes commands, flags and package labels. Labels come from the last evaluation of
the project (`.bob/labels`), so completion never runs your `bob.go` files.

| Shell | Add this line to |
|---|---|
| bash | `~/.bashrc`: `source <(bob completion bash)` |
| zsh | `~/.zshrc`: `source <(bob completion zsh)` |
| fish | `~/.config/fish/config.fish`: `bob completion fish \| source` |

> [!NOTE]
> The zsh and fish scripts are golden-tested but were not run in real zsh or fish sessions on
> the documentation host.

## Upgrade

- Nix profile: `nix profile upgrade bob`.
- NixOS: `nix flake update bob`, then `nixos-rebuild switch`.
- AppImage: download the file again.

Your projects keep working: `bob.lock` does not depend on the bob version. The first command
in each project recompiles its evaluator, and outputs that did not change are not rebuilt.
Shims written by `bob install` call bob by its absolute path; keep bob at the same path, or
set `BOB` to the new one.

## Uninstall

Remove bob with the method you used (`nix profile remove bob`, your NixOS configuration, or
`rm ~/.local/bin/bob`), then delete its state:

```bash
rm -rf ~/.local/share/bob ~/.config/bob ~/.local/state/bob
```

## For contributors: build from source

From a checkout of the bob repository, with Go (the version in `go.mod`), `curl` and `zstd`:

```bash
sh scripts/fetch-nix.sh
CGO_ENABLED=0 go build -trimpath -o ~/.local/bin/bob ./cmd/bob
```

The first command fetches the pinned static Nix that bob embeds. `scripts/bootstrap.sh` does
both steps and then lets bob build itself (`bob build //cmd/bob:bob`).

## Next steps

- [Go quickstart](../03-quickstart/01-go.md): your first project.
- [Dev environments](../04-guides/05-dev-environments.md): the shell hook and editor setup.
- [CLI reference](../05-cli/01-commands.md).
