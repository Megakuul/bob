# Environment (Stage 2 host)

Probed 2026-10-20 after rescale. All Stage 2 measurements are taken here unless labelled otherwise.

| Item | Value |
|---|---|
| Host | `vibe-engine`, QEMU VM |
| CPU | 8 vCPU, ARM Neoverse-N1 @ 2.0 GHz (**aarch64**) |
| RAM | 15 GiB |
| Disk | 151 GB (`/`), 142 GB free |
| OS | Fedora 44, kernel 7.1.10-200.fc44.aarch64 |
| SELinux | Permissive |
| AppArmor | not present |
| User namespaces | `user.max_user_namespaces = 62246`; `unshare --user --map-root-user --mount` works |
| KVM | not available (`/dev/kvm` missing) |
| Containers | none installed (podman/docker absent) |
| System Nix | Nix 2.34.8 (Fedora package), single-user as root, daemon inactive, `/nix/store` owned by root; flakes + nix-command enabled; sandbox on; substituter cache.nixos.org |
| Tools (root profile, via Nix) | go 1.26.9, gopls 0.24.0, git 2.56.0, jq 1.8.2, gnumake |

## Pinned inputs for spikes

| Input | Pin |
|---|---|
| nixpkgs (primary) | `nixos-26.05` @ `cd4ea8d57de16f4810bc5f14207bac45fbf19f67` |
| nixpkgs (bump test, one release older) | `nixos-25.11` @ `b6018f87da91d19d0ab4cf979885689b469cdd41` |
| dynamitedb, lake | current HEAD at time of use (D46) |

## Low-power profile (R-NFR-008)

Budgets for small machines are measured with this host limited to **2 vCPU / 4 GiB** through a
cgroup (`systemd-run --scope -p AllowedCPUs=0-1 -p MemoryMax=4G …`). Both full-host and
limited numbers are reported, labelled `[8c/15G]` and `[2c/4G]`.

## Not available here

- x86_64: only via cross-compilation + qemu-user (to be installed through Nix when needed).
- No physical second machine; multi-node server tests run as multiple processes on this host.

## Changes made during Stage 2

- 2026-10-20 (S0b): test user `bobtest` (uid 1000) created; state under `/home/bobtest/.local/share/bob*`
  and `/home/bobtest/cache-*`. Root profile additionally has `binutils`, `zstd`.
- Build Go spike binaries with `CGO_ENABLED=0` (no host gcc).
