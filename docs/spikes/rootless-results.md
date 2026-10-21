# S0b — Rootless embedded Nix: results

Spike for R-NIX-020…025 (D6, D11, D40). Code: `spikes/rootless/` (throwaway). Date 2026-10-20.
Host: see `docs/environment.md`. All numbers **[8c/15G]** unless marked **[2c/4G]** (cgroup
`AllowedCPUs=0-1 MemoryMax=4G`). Other spike workers were running in parallel during the
measurements, so timings are upper bounds.

## Method

- Test user `bobtest` (uid 1000), created for the spike. Every test runs through
  `spikes/rootless/as-bobtest.sh`, which runs `env -i` with a PATH that contains only bob's state `bin/` and `/usr/bin:/bin`. No
  test invokes the system `/usr/bin/nix`.
- `cmd/bobns` (Go, stdlib only, `CGO_ENABLED=0`) is the mechanism a single `bob` binary would use.
  It re-executes itself through `/proc/self/exe` with `CLONE_NEWUSER|CLONE_NEWNS`, a single uid/gid
  mapping (`0 → real uid`) and `setgroups=deny`. Then:
  1. It makes `/` `MS_REC|MS_PRIVATE`, so no mount propagates to the host.
  2. **If `/nix` exists on the host:** it bind-mounts `<state>/nix` onto `/nix`.
  3. **If `/nix` does not exist:** it mounts a tmpfs root and mirrors every top-level entry of `/`
     into it (recursive bind mounts, symlinks recreated). It then adds `nix/` bound to
     `<state>/nix`, calls `pivot_root`, and detaches the old root.
  4. It sets `NIX_CONF_DIR=<state>/etc`, `NIX_USER_CONF_FILES`, and all four `XDG_*` directories
     under `<state>`, then execs the command.
- A host without `/nix` was simulated with `bobns -hide-nix`. That builds a root without `/nix`
  (real `pivot_root`, not chroot), and a second, nested `bobns` runs inside it and must take the
  pivot path.
- `features.sh` runs the same probes F1–F8 inside the namespace for each implementation.
  `authcache-test.sh` and `cmd/authcache` cover authenticated substitution, and `overhead.sh`
  measures startup time.
- bob-owned `nix.conf` (the complete config; nothing in `/etc` is touched):
  ```
  build-users-group =
  sandbox = true
  require-drop-supplementary-groups = false
  experimental-features = nix-command flakes
  substituters = https://cache.nixos.org
  trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
  ```

## Candidates (nixpkgs `nixos-26.05@cd4ea8d5`)

| | Nix (`nixStatic`) | Lix (`lixStatic`) |
|---|---|---|
| Version | 2.34.9 | 2.94.3 (2.95.4 exists as `pkgsStatic.lixPackageSets.latest.lix` but is **not cached**) |
| Static aarch64, cached on cache.nixos.org | yes | yes |
| Static x86_64, cached on cache.nixos.org | yes (narinfo present) | yes (narinfo present) |
| Binary size: raw / stripped / stripped+zstd-19 | 38.4 / 32.5 / 10.9 MB | 41.8 / 33.4 / 11.0 MB |
| Stripped + gzip-9 (stdlib-decodable) | 13.8 MB | n/m |
| Licence (nixpkgs `meta.license`, upstream `COPYING`) | LGPL-2.1-or-later | LGPL-2.1-or-later |
| Embedded sandbox shell | yes (`sandbox-paths = /bin/sh=__embedded_sandbox_shell__`, busybox inside) | yes (same) |

Static x86_64 binaries were checked only for cache presence; they were not run on this host.

## Feature probes (both run inside `bobns` as `bobtest`)

| Probe | Nix 2.34.9 | Lix 2.94.3 |
|---|---|---|
| F1 substitute `hello` from cache.nixos.org (cold) | ✓ 1,070 ms | ✓ 861 ms |
| F3 sandboxed build, `sandbox = true`, **nested inside the user namespace** | ✓ after setting `require-drop-supplementary-groups = false`. Without it: `error: setgroups failed. Set the require-drop-supplementary-groups option to false to skip this step.` | ✓ with the same setting. Without it: `error: unable to start build process` (no hint) |
| F3 inside the build: uid / network / `/home` / interfaces | uid=1000, network blocked, `/home` hidden, loopback `127.0.0.1` present | identical |
| F4 `derivation add`, output paths **left empty** (Nix should compute them) | ✓ computes the paths, the drv builds | ✗ `deferred input-addressed derivations are not supported`: the caller must compute output paths itself |
| F4 drv JSON format (`derivation show`) | "version 4": `inputs.drvs` / `inputs.srcs`, store paths **without** the `/nix/store/` prefix | older format: `inputDrvs` / `inputSrcs`, full paths. **The two formats have diverged.** |
| F5 `--log-format internal-json` | ✓ (`@nix {"action":…}` lines) | ✓ same shape |
| F6 failing build message | `Reason: builder failed with exit code 1.` + output paths + last log lines + `nix log` hint | `builder for '…drv' failed with exit code 1;` + last log lines + hint |
| F7 eval error `{ a = 1; }.b` | `attribute 'b' missing`, caret at column 1, "Did you mean a?" | same text, caret on the attribute (column 12), "Did you mean a?" |
| Sandboxed build on the **pivot path** (host without `/nix`) | ✓ | ✓ |
| 4 parallel bob processes on one store | ✓ (4 builds, each with a 1 s sleep, done in 1.6 s total; no lock errors) | n/m |

## Namespace overhead (median of 30 runs, µs)

| | `nix --version` direct | via `bobns` (bind) | via `bobns` (pivot) | `bobns -- true` |
|---|---|---|---|---|
| Nix [8c/15G] | 17,531 | 24,386 | 24,276 | 8,642 |
| Lix [8c/15G] | 8,732 | 14,229 | 16,055 | 8,619 |
| Nix [2c/4G] | 19,092 | 25,227 | 24,574 | 8,697 |
| Lix [2c/4G] | 8,638 | 14,469 | 15,823 | 8,741 |

- Entering the namespace costs **≈5–7 ms** per invocation. Self-reported setup inside the child:
  3.4 ms on the bind path, 5.1 ms on the pivot path.
- Nix's own startup is about 9 ms slower than Lix's.

## Embedding (`cmd/embedtest`)

- A Go binary that embeds the gzip-9 static Nix (stdlib `compress/gzip`, no dependencies) is
  16.7 MB. The test program itself is about 2.9 MB.
- On first use it extracts the binary to a content-addressed path, with atomic rename, in
  **294 ms**. Later runs only stat the path (0.02 ms).
- Using zstd would save about 3 MB but needs a third-party decoder.
- Note: Nix dispatches legacy commands (`nix-build` …) on argv[0], so bob should extract to
  `…/nix-<hash>/bin/nix` and create the symlinks, not rename the binary.

## Authenticated, signed binary cache (`authcache-test.sh`)

Setup:
- The producer signs a store path with a fresh key (`nix key generate-secret`, `nix store sign`) and
  copies it into a `file://` cache.
- `cmd/authcache` serves that directory over HTTP and requires basic auth.
- The consumer is a fresh bob state whose config is written only by bob: `substituters`,
  `trusted-public-keys`, and `netrc-file = <state>/etc/netrc`.

| Case | Nix | Lix |
|---|---|---|
| A: correct token + correct key | ✓ substituted | ✓ substituted |
| B: wrong token | ✗ refused (401). Nix **silently drops the substituter** after a 401 on `nix-cache-info` and reports only `don't know how to build these paths` / `no substituter that can build it`. With a warm narinfo cache, the NAR 401 is reported as `file … does not exist in binary cache` | same: the 401 is reported as "does not exist" |
| C: correct token, untrusted key | ✗ refused: `ignoring substitute … not signed by any of the keys in 'trusted-public-keys'` | same |

Findings:
- netrc (basic auth) works for both. Bearer tokens were not tested: Nix reads credentials for
  HTTP binary caches from the netrc file.
- **Nix never surfaces auth failures as auth failures.** bob must validate its token against the
  server itself, before and after a Nix run, and translate the errors (R-NIX-012).
- Nix's narinfo disk cache lives under `$XDG_CACHE_HOME/nix`. bob must point every `XDG_*`
  directory into its own state. Otherwise separate stores, and the user's own Nix, share stale
  narinfo; an early run of this test was misled exactly this way.
- The test server answered `PUT /nix-cache-info` with 200 because `http.FileServer` ignores the
  method. A real cache server must refuse writes on its read path.

## Programs outside the namespace

On a host **without** `/nix`, binaries from bob's store can't run outside the namespace. Their
ELF interpreter is a store path:
`hello` → `/nix/store/…-glibc-2.42-100/lib/ld-linux-aarch64.so.1`; running it from the `-hide-nix`
root gives "no such file or directory". It happened to run on this host only because root's
`/nix/store` contains the same glibc.

**Consequence:** everything bob gives the user must run *inside* bob's namespace. That covers:
- installed programs (`bob install` needs shims in `~/.local/bin` that re-enter the namespace,
  about 5–7 ms each);
- dev shells (`bob dev` spawns the shell inside);
- editors and language servers (gopls, clangd and rust-analyzer started by an editor outside
  `bob dev` can't see `/nix/store` toolchains).

## Unprivileged user namespaces by distro (R-NIX-022)

Legend: **V** = verified on this host. **D** = documented, with source. **?** = not confirmed.

| Distro | Default | Fix if blocked | Basis |
|---|---|---|---|
| Fedora 44 | allowed (`user.max_user_namespaces=62246`) | — | **V** (SELinux was *Permissive* here; enforcing mode is untested) |
| Ubuntu 22.04 | allowed (`apparmor_restrict_unprivileged_userns` normally 0) | — | D [1] |
| Ubuntu 24.04 | **restricted by default.** Creation is allowed, but the default AppArmor profile denies capabilities inside the namespace, so bob's first mount fails | AppArmor profile for bob's path with `userns,` and `flags=(unconfined)`, or sysctl `kernel.apparmor_restrict_unprivileged_userns=0` | D [1][2][3] |
| Ubuntu 26.04 | reported restricted like 24.04 (sysctl normally 1) | same as 24.04 | D (third-party guide only) [4] |
| Debian 12 / 13 | allowed: "The default is 1 for all modern Debian and Ubuntu kernels" (`kernel.unprivileged_userns_clone`); the knob may be absent | `kernel.unprivileged_userns_clone=1` | D [5][6] |
| RHEL / Rocky 9 (8+) | allowed by default; the DISA STIG hardening rule sets `user.max_user_namespaces=0` | `user.max_user_namespaces=15000` | D (community + STIG) [7][8] |
| RHEL / Rocky 10 | ? (not found) | as RHEL 9 | ? |
| Arch (`linux`) | allowed | — | D [9] |
| Arch (`linux-hardened`) | blocked (`kernel.unprivileged_userns_clone=0`) | `kernel.unprivileged_userns_clone=1` | D [9][10] |
| openSUSE Tumbleweed / Leap | ? (no source found) | — | ? |
| NixOS | allowed (`security.allowUserNamespaces` defaults to true; disabled → ENOSPC) | `security.allowUserNamespaces = true;` | D [11] |

Also noted: Lix's own test suite has an issue about Ubuntu's AppArmor userns rules breaking tests [12].

### Detection (implemented in `bobns`, verified where marked)

1. **Pre-check from sysctls:** `/proc/sys/kernel/apparmor_restrict_unprivileged_userns == 1`,
   `/proc/sys/kernel/unprivileged_userns_clone == 0`, `/proc/sys/user/max_user_namespaces == 0`.
   **V** for `max_user_namespaces=0`, simulated by writing 0 in a throwaway namespace (the setting
   is per namespace, so the host was untouched). bob printed the fix text.
2. **Classifying the clone error** (pre-check skipped): ENOSPC/EPERM/EACCES from
   `fork/exec /proc/self/exe` is mapped to the fix text. **V** for ENOSPC.
3. **Capabilities denied inside the namespace** (the Ubuntu 24.04 behaviour): the child gets EPERM
   from its first mount and exits with code 125, which the parent maps to the AppArmor fix. **Not
   verified.** It couldn't be reproduced here, because a nested namespace regains full
   capabilities even after `setpriv --bounding-set -sys_admin`. It needs a real Ubuntu 24.04 host.
4. The pre-check alone is not enough: on Ubuntu the sysctl reads 1 even when an AppArmor profile
   exempts bob. bob must therefore treat (1) as a hint and rely on the actual attempt (2, 3)
   for the decision.

Exact fix text printed by `bobns` (the AppArmor case; bob inserts its real executable path):
```
sudo tee /etc/apparmor.d/bob >/dev/null <<'PROFILE'
abi <abi/4.0>,
include <tunables/global>
profile bob /home/alice/.local/bin/bob flags=(unconfined) {
  userns,
  include if exists <local/bob>
}
PROFILE
sudo apparmor_parser -r /etc/apparmor.d/bob
(or, system-wide: echo 'kernel.apparmor_restrict_unprivileged_userns = 0' | sudo tee /etc/sysctl.d/60-bob-userns.conf && sudo sysctl --system)
```
The profile shape (`flags=(unconfined)` plus `userns,`) follows the Ubuntu release notes [2] and the
Steam profile fix [3]. The `abi <abi/4.0>` line is an assumption that has to be checked on a real
Ubuntu host.

## Licences (facts only, no legal conclusion)

- Nix and Lix: LGPL-2.1-or-later (nixpkgs `meta.license`, `COPYING` in both repositories).
- bob would **ship the static binary and execute it as a separate process**; bob does not link
  against Nix.
- The static binaries also contain statically linked dependencies with their own licences. For
  Lix, the build inputs include openssl, curl, boost, sqlite, libarchive, libseccomp, editline,
  boehm-gc, brotli, bzip2, xz, zstd, capnproto, toml11 and libunwind; Nix's are comparable.
- Both binaries also embed a **busybox** sandbox shell (busybox is GPL-2.0).
- Shipping them means meeting the source/notice obligations of each component. That is a task
  for `ATTRIBUTION.md` and the release process, and it needs owner review (BOOTSTRAP §5.7).

## Sources

1. Ubuntu Discourse, "Understanding AppArmor User Namespace Restriction" — https://discourse.ubuntu.com/t/understanding-apparmor-user-namespace-restriction/58007
2. Ubuntu 24.04 LTS release notes — https://documentation.ubuntu.com/release-notes/24.04/
3. Steam AppArmor profile, `flags=(unconfined)` + `userns,` — https://github.com/ValveSoftware/steam-for-linux/issues/12620
4. linuxcapable, AppArmor on Ubuntu 26.04/24.04/22.04 — https://linuxcapable.com/how-to-enable-or-disable-apparmor-on-ubuntu-linux
5. Debian bubblewrap README.Debian — https://metadata.security.debian.org/changelogs/main/b/bubblewrap/stable-security_README.Debian
6. Debian 12.2 missing sysctl report — https://git.tdem.in/dev-sec/ansible-collection-hardening/issues/723
7. nix-user-chroot issue (RHEL 8/9 enabled by default) — https://github.com/nix-community/nix-user-chroot/issues/91
8. RHEL 9 STIG RHEL-09-213105 — https://stigaview.com/products/rhel9/v2r7/RHEL-09-213105
9. Arch forum thread on userns / linux-hardened — https://bbs.archlinux.org/viewtopic.php?id=314344
10. Arch bug FS#63295 — https://bugs.archlinux.org/task/63295
11. NixOS option `security.allowUserNamespaces` — https://mynixos.com/nixpkgs/option/security.allowUserNamespaces
12. Lix issue #545 — https://git.lix.systems/lix-project/lix/issues/545
13. Qualys, bypasses of Ubuntu userns restrictions (context) — https://www.qualys.com/2025/three-bypasses-of-Ubuntu-unprivileged-user-namespace-restrictions.txt
