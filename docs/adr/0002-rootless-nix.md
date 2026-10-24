# ADR 0002 — Rootless embedded Nix

- **Status:** Accepted (locked) — owner at Gate 0, 2026-10-20: Nix (D50), DX model (D51), licences (D52)
- **Date:** 2026-10-20
- **Requirements:** R-NIX-020 (only executable), R-NIX-021 (rootless, `/nix/store`), R-NIX-022 (refuse
  with the exact fix), R-NIX-023 (Nix vs Lix), R-NIX-024 (bob owns its config), R-NIX-025 (don't
  touch a system Nix)
- **Evidence:** `docs/spikes/rootless-results.md` (all measurements [8c/15G] unless marked [2c/4G]),
  code in `spikes/rootless/`

## Context

- `bob` must be the only thing a developer or consumer installs, and it must work without root.
- Store paths contain `/nix/store` in their hashes. Relocating the store would make every binary
  cache useless (cache.nixos.org and the bob server alike). So bob has to provide `/nix/store`
  itself, unprivileged.

## Decision 1 — Mechanism

The `bob` binary re-executes itself (`/proc/self/exe`) into an **unprivileged user + mount
namespace**:
- A single uid/gid mapping (`0 → real uid`), `setgroups=deny`, and `/` made recursively private.
- **If the host has `/nix`:** bind-mount `~/.local/share/bob/nix` onto `/nix`.
- **If the host has no `/nix`:** build a tmpfs root mirroring the host's top-level entries, add
  `nix/`, then `pivot_root`. chroot is not used, because it would prevent the nested user
  namespaces that the Nix sandbox needs.
- Nix runs with `NIX_CONF_DIR`, `NIX_USER_CONF_FILES` and all `XDG_*` directories under bob's state,
  using a bob-written `nix.conf`.
- That config must contain `build-users-group =`, `require-drop-supplementary-groups = false` and
  `sandbox = true`.
- A system Nix is never touched (R-NIX-025). Inside bob's namespace its store is hidden.

Measured:
- Entering the namespace costs about **5–7 ms** per invocation (setup itself 3.4 ms bind, 5.1 ms
  pivot); the same at [2c/4G].
- The nested Nix sandbox works on both paths: network blocked, `/home` hidden, loopback present.
- Four concurrent bob processes on one store work.
- Substitution from cache.nixos.org works, and so does an authenticated, signed cache configured
  only by bob (netrc + `trusted-public-keys`).

Options rejected:
- **Relocated store** (`--store ~/…`): breaks all binary caches. Unacceptable per R-NIX-003.
- **proot-style syscall emulation:** slow, explicitly excluded by D11.
- **Setuid helper or `newuidmap`:** needs installed privileged binaries, which violates
  "only `bob`" and "fully rootless". A single-uid mapping is enough (measured).
- **Bind-mount only, no pivot:** fails on hosts without `/nix`.

## Decision 2 — Nix vs Lix: **Nix**, static, from the pinned nixpkgs (owner: "use nix then for B")

| Criterion | Nix 2.34.9 | Lix 2.94.3 | Weight |
|---|---|---|---|
| Static aarch64 + x86_64 cached | ✓ | ✓ (2.95.4 static not cached) | must |
| Rootless nested sandbox | ✓ | ✓ | must |
| Size, stripped + zstd | 10.9 MB | 11.0 MB | low |
| Startup time (`--version`, direct) | 17.5 ms | 8.7 ms | low (dominated by build time) |
| `derivation add` with output paths left empty, computed by Nix | ✓ | ✗ (`deferred input-addressed derivations are not supported`) | **high for strategy B (ADR 0001)** |
| drv JSON format | current upstream ("version 4") | older format, **diverged** | high: B writes drv JSON |
| Error clarity for the namespace setup failure | actionable (names the option to set) | `unable to start build process` | medium |
| Build and eval error messages | equivalent | equivalent (slightly better caret) | low |
| `--log-format internal-json` | ✓ | ✓ | must (R-NIX-012) |
| Same implementation as cache.nixos.org and nixpkgs CI | ✓ | fork | medium |
| Licence | LGPL-2.1-or-later | LGPL-2.1-or-later | same |

Rationale for Nix:
- Lix's advantages measured here are startup time and one caret position. Nix wins on the
  features bob's backend depends on (`derivation add` computing paths, current drv JSON) and on
  diagnosing namespace setup.
- If Gate 0 picks strategy A′, the `derivation add` difference matters less. The recommendation
  still holds, because Nix stays aligned with nixpkgs and cache.nixos.org.

## Decision 3 — Distribution of the Nix binary inside bob

- Embed the **stripped static Nix as gzip-9** (13.8 MB, decoded with the standard library). The
  zstd alternative is 10.9 MB but needs a dependency.
- Extract it on first use to `~/.local/share/bob/bin/nix-<hash>/bin/nix`, with atomic rename,
  and create the legacy-name symlinks. Extraction takes 294 ms once; later runs only stat.
- One embedded binary per target architecture; bob's own build picks the matching one. This is
  dogfooding territory (R-ARCH-005).

## Decision 4 — Refusal and fix text (R-NIX-022)

1. **Pre-check from sysctls**, treated as a hint only.
2. **Classify the clone error**: ENOSPC / EPERM / EACCES.
3. **Detect capabilities denied inside the namespace** by the first mount failing, i.e. the Ubuntu
   24.04 AppArmor default.

Each case prints the exact one-time admin fix: an AppArmor profile for bob's real path with
`userns,` and `flags=(unconfined)`, or the `apparmor_restrict_unprivileged_userns`,
`unprivileged_userns_clone` or `max_user_namespaces` sysctl, or the NixOS option. Cases 1 and 2
were verified by simulation. **Case 3 still has to be verified on a real Ubuntu 24.04 host.**

## Decision 5 — Tools outside the namespace (owner, Gate 0)

Owner: "can you not do something similar to nix develop... like modify env variables that they
point to the bob loaded toolchains ... but otherwise yes a) is good."

- **Inside the namespace, `bob dev` is bob's `nix develop`:** it sets the full environment
  (`PATH`, `GOROOT`, `CC`/`CXX`, `PKG_CONFIG_PATH`, `RUSTC`, `NODE_PATH`, …) to the store
  toolchains of the project and starts the shell there. Anything started from that shell
  (editor, LSP, dev server) inherits both the namespace and the environment.
- **Outside the namespace, environment variables alone can't work on hosts without `/nix`:**
  a variable pointing to `/nix/store/…/bin/gopls` names a path that does not exist there
  (verified in S0b: missing ELF interpreter / no such file). So variables exported to the outside
  point to **shims** instead: bob generates `.bob/bin/<tool>` (gitignored) for every toolchain
  binary of the project; each shim runs `bob exec <tool>`, which enters the namespace with the
  same environment as `bob dev` (about 5–7 ms per start).
- `bob env` prints the outside environment (`PATH=.bob/bin:$PATH` plus non-store variables) for
  direnv/editor integration, and bob writes matching editor settings (e.g. `go.alternateTools`,
  `rust-analyzer.server.path`) on request.
- On a host that already has a usable `/nix` with bob's paths, nothing changes: bob still uses its
  own store (R-NIX-025), the shims keep working.
- S1/S2 must verify gopls through the shim, including gopls starting `go` subprocesses.

## Consequences

- **Everything runs inside the namespace.** On hosts without `/nix`, store binaries can't run
  outside it (verified: ELF interpreter missing). Therefore:
  - `bob install` provides shims (e.g. `~/.local/bin/<tool>` → `bob exec <tool>`, about 5–7 ms
    each);
  - `bob dev` starts its shell and dev servers inside the namespace;
  - editor integration: see Decision 5.
- **Auth errors are invisible to Nix.** A 401 makes Nix silently drop the substituter, or report
  "file does not exist". bob must check credentials itself and translate errors (R-NIX-012).
- bob must isolate every `XDG_*` directory as well as `NIX_CONF_DIR`; otherwise narinfo caches
  are shared.
- **Licensing:** shipping static Nix means LGPL-2.1 plus the licences of its static dependencies,
  including busybox (GPL-2.0) for the embedded sandbox shell. Source and notice obligations go
  into `ATTRIBUTION.md` and the release process (owner accepted at Gate 0, D52).
- **Developer machines have no build-user isolation:** all builds run as the user's uid inside
  the sandbox. That's acceptable on a developer machine; worker isolation is a separate topic
  (S13, R-SEC-000).

## Open risks

1. Ubuntu 24.04+ AppArmor behaviour and the exact profile syntax (`abi <abi/4.0>`) are unverified.
2. SELinux in enforcing mode is unverified (this host is Permissive).
3. Running bob inside containers (e.g. CI) whose seccomp profile blocks namespace creation is
   unverified.
4. openSUSE and RHEL 10 defaults are not confirmed by any source.
5. Static x86_64 binaries were confirmed cached but not run (no x86_64 host).
