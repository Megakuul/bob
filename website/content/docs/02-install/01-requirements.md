---
title: Requirements
section: Install
order: 1
summary: What a machine needs to run bob - Linux on amd64 or arm64, unprivileged user namespaces, disk space and network.
---

Check these before you install bob. Most Linux desktops and CI runners meet them out of the box.

## Operating system and CPU

| Requirement | Supported |
|---|---|
| Operating system | Linux |
| CPU architecture | `x86_64` (linux-amd64) or `aarch64` (linux-arm64) |
| Privileges | none: bob runs as your normal user, without `sudo` |

bob refuses other architectures with `BOB-E-0008`. macOS and Windows are not supported.

You do **not** need Docker, a C compiler, Go or a daemon: every toolchain comes from nixpkgs on
first use. Nix is needed only if you install bob with Nix; the AppImage brings its own static
Nix (see [Install bob](02-install-bob.md#which-nix-bob-uses)).

## Unprivileged user namespaces

bob gives itself a private `/nix` with a user namespace and a mount namespace. That needs
unprivileged user namespaces. They are on by default on Fedora, Debian 11 and later, Arch,
openSUSE and most cloud images.

Check your machine:

```bash
unshare --user --map-root-user true && echo "user namespaces work"
```

If they are blocked, bob stops with `BOB-E-7005` and prints the exact fix for your system,
for example:

```text
✗ bob cannot create its private /nix: unprivileged user namespaces are blocked on this system
  cause: user namespaces are disabled (user.max_user_namespaces = 0)
  fix (run once as an administrator):
    echo 'user.max_user_namespaces = 15000' | sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system
```

bob exits with status 7 in that case.

| Situation | What bob tells you to do |
|---|---|
| Ubuntu 24.04 and later (AppArmor restricts user namespaces) | install a small AppArmor profile for the `bob` binary, or set `kernel.apparmor_restrict_unprivileged_userns = 0` |
| `user.max_user_namespaces = 0` (some hardened RHEL setups) | set it to `15000` |
| `kernel.unprivileged_userns_clone = 0` (older Debian, linux-hardened) | set it to `1` |
| NixOS with `security.allowUserNamespaces = false` | enable it in `configuration.nix` |
| Inside a container | allow user namespaces in the container runtime (for example `--security-opt seccomp=unconfined`) |

There is no fallback mode: without user namespaces, bob does not run.

> [!NOTE]
> The AppArmor fix is tested on simulated cases only; a real Ubuntu 24.04 run is still
> pending. If the profile does not help, use the sysctl alternative bob prints.

## Disk space

bob keeps its store in `~/.local/share/bob` (or in `$BOB_HOME`). Plan for:

- about 3 GB for one ecosystem's toolchain and a small project;
- 10 GB or more when you use several ecosystems (Go, GCC, Rust, Node and OCaml together take
  about 7 GB);
- more for large projects and many variants.

`bob gc` is not implemented yet. To reclaim space today, delete the store directory when no
bob command is running; bob recreates it and downloads what it needs again. See
[Limitations and status](../07-reference/03-status.md).

## Network

bob downloads on demand:

- toolchains and system libraries from `https://cache.nixos.org`;
- the nixpkgs source tarball from GitHub (once per pinned revision);
- third-party code from the usual registries: `proxy.golang.org`, crates.io, the npm
  registry, opam-repository on GitHub.

Builds themselves never use the network. After the first build, `bob build --offline` works
without network as long as everything is in the store.

## Next steps

- [Install bob](02-install-bob.md).
- [Concepts](../01-introduction/02-concepts.md).
