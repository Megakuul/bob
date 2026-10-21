# S8 — Outputs from closures: OCI images and AppImages

## 1. Question and requirements

Can bob turn a store closure into (a) reproducible multi-arch OCI images without Docker and
(b) AppImages that run on plain distros without Nix, FUSE or user namespaces? Which layer
strategy, runtime, relocation method and fixup does that need?

Requirements: R-OUT-001..004 (§8), R-INST-001/002 (§11), R-REG-003/004 (§15); decisions D3, D19,
D42, D47. Reuses S12 (registry pull API, tokens, rootless podman) and B (fixture `api`).

## 2. What was built

All code in `spikes/s08-bundles/` (module `bobspike/s08`, CGO off, stdlib only).

| Path | What |
|---|---|
| `closure/` | Closure query (spike: `nix path-info --json-format 1 -r`) and a popularity score per path |
| `oci/` | Image builder: one reproducible `tar+gzip` layer per store path (or grouped), customisation layer (`/etc/passwd`, cert and zoneinfo symlinks, `/tmp`), config, manifest, multi-platform index, OCI layout, layer cache keyed by store path |
| `appimage/` | AppImage builder: AppDir with the closure under `nix/store`, `.bob/launch.json`, desktop file, icon, `AppRun`; `mksquashfs` (zstd, fixed times); type-2 runtime prepended; optional `prune` globs and `strip` |
| `cmd/bobrun/` | `AppRun` launcher (static Go, 2.3 MiB): **userns** mode (tmpfs root, bind host top-level dirs, bind bundled store at `/nix/store`, chroot, exec) with automatic fallback to **ld.so** mode (exec bundled `ld-linux` with `--library-path`) |
| `cmd/s08/` | CLI: `s08 oci spec.json`, `s08 appimage spec.json` |
| `specs/` | Every image and bundle built here, as JSON |
| `scripts/distro-matrix.sh`, `scripts/vulkan.sh` | Distro tests in an unprivileged `bwrap` sandbox as `bobtest` |
| `measure.sh` | `./measure.sh 8c|2c` reruns the build timings |
| `gen/`, `gen-v2/` | Copies of the B spike; `gen-v2` changes one string in `api` to get version 2 |
| `apistub/` | Go static program used as the amd64 half of the `api` index |

Tools from the pin: crane 0.21.6, skopeo 1.22.2, podman 5.8.8, bubblewrap 0.12.0,
squashfs-tools 4.7.5, qemu-user 10.2.4. Type-2 runtime: AppImage/type2-runtime release
`20251108`, `runtime-aarch64` sha256 `00cbdfcf…7444`, `runtime-x86_64` sha256 `2fca8b44…260d`.
Distro rootfs from `crane export --platform linux/arm64` of the official Docker Hub images
(digests in §3.6).

## 3. Results

### 3.1 Fixture closure and fixup [8c/15G]

`api` v1 = `/nix/store/59ds6rah…-api-api`, 13 paths, **75.9 MiB** NAR (B's number confirmed).

| Path | Own | Notes |
|---|---|---|
| glibc-2.42 | 44.4 MiB | `lib/gconv` 19.4 MiB, `share/i18n` 15.0 MiB (locale sources), `share/locale` 4.5 MiB (translations) |
| gcc-15.3.0-lib | 9.4 MiB | libstdc++ 3.1 MiB; sanitizer runtimes (asan/tsan/lsan/ubsan/hwasan) 5.4 MiB |
| openssl-3.6.5 | 9.0 MiB | libssl + libcrypto, needed |
| api | 7.7 MiB | not stripped (`with debug_info`); stripped: **5.2 MiB** |
| tzdata, iana-etc | 2.0 + 0.6 MiB | referenced by nixpkgs Go (tz path, `/etc/services`) |
| libidn2, libunistring | 2.4 MiB | referenced by glibc |
| itoa-1.0.18-src | 0.05 MiB | Rust panic paths; needs `--remap-path-prefix` |

What fixup cuts, measured on the AppDir of the `api` bundle (77.4 MiB incl. cacert):

| Fixup | AppDir | AppImage (zstd-19) |
|---|---|---|
| none | 77.4 MiB | 21.2 MB |
| strip `api` + drop itoa-src + prune glibc `share/i18n`, `share/locale`, `lib/gconv` + sanitizer libs | **31.0 MiB** | **10.8 MB** |

The pruned bundle still runs (`digest=81b637…`, both launcher modes). Strip and
`remove-references-to` belong in the package's own fixup (a derivation step, B's backlog); the
glibc/gcc-lib pruning is a bundle-level filter because those are nixpkgs paths. Pruning `gconv`
breaks `iconv` to non-builtin charsets; it must be an explicit option.

### 3.2 OCI: layers, reuse, reproducibility [8c/15G unless marked]

Images: arm64 = fixture `api` + nss-cacert + tzdata; amd64 = `apistub` (static Go, GOARCH=amd64)
+ x86_64 cacert/tzdata. A real dynamic amd64 closure was also tested: index `hello:2.12.3` with
arm64 `hello` and x86_64 `hello` substituted from cache.nixos.org (Nix substitutes foreign-system
paths without building).

| Strategy (arm64 `api`) | Layers | Compressed | Cold build | v1→v2 new bytes |
|---|---|---|---|---|
| one layer per store path (+ customisation) | 14 | 25.4 MB | 1.6–2.0 s [8c]; 2.3 s [2c] | **3.78 MB (1 layer, 15 %)** |
| popularity-grouped, max 4 layers, app on its own top layer | 4 | 25.5 MB | 3.2 s [8c]; 3.7 s [2c] | 3.78 MB (1 layer) |
| popularity-grouped, max 4, app *not* separated (first try, nixpkgs-style) | 4 | 25.5 MB | 2.8 s | 24.6 MB (96 %) |
| single layer (S12 style) | 1 | 25.4 MB | 3.0–3.6 s | 25.4 MB (100 %) |
| incremental v2 into a layout that holds v1 (layer cache) | 14, 13 reused | — | **0.33–0.57 s** | — |

- Grouping by popularity alone puts the application (least popular) into the merged layer with
  big libraries; reuse then drops to ~4 %. The roots must always get their own top layer.
- Reproducible: two builds into fresh layouts are byte-identical (`diff` of all blob hashes);
  the index digest `sha256:40e73051…` was the same in 4 runs across [8c] and [2c].
  skopeo `copy --all` kept the digest (`api:v1-one` index `sha256:a3a39de0…` local = remote).
- Rules used: entries sorted (WalkDir order), mtime 1 s, uid/gid 0, no user/group names,
  perms from the store, symlinks kept, each layer contains its own `nix/` and `nix/store/` dirs,
  Go `compress/gzip` default level with empty header, config without `created` (history uses
  epoch+1 s), JSON from structs.

Verification (local `crane registry serve` on 127.0.0.1:20800, pushed with skopeo):

| Check | Result |
|---|---|
| `crane validate --remote` api:v1, api:v2, api:v1-one; per platform arm64/amd64; hello both platforms | all **PASS** |
| index media type / platforms | `application/vnd.oci.image.index.v1+json`, `[amd64, arm64]` |
| rootless podman (bobtest) pull + run `api:v1` | ✓ `digest=81b637… numhex=3432 proto_bytes=116`; pull 2.7 s |
| rootless podman `curl:1` image: HTTPS to cache.nixos.org | ✓ `200`, verify result 0 (certs via `SSL_CERT_FILE` + `/etc/ssl/certs/*` symlinks) |
| `id` in image | `uid=65534(app) gid=65534(app)` (User from config, `/etc/passwd` in customisation layer) |
| `TZ=Europe/Zurich date -d @0` | `Thu Jan 1 01:00:00 CET 1970` (tzdata via `TZDIR` + `/usr/share/zoneinfo`) |
| amd64 content | `apistub linux/amd64` and x86_64 `hello` → `Hello, world!` under `qemu-x86_64` from the exported image |
| `crane push --index <layout>` | pushes the layout's `index.json` wrapper as a nested index; use skopeo `copy --all oci:dir:ref` or push the inner index (bob will push blobs itself, S12) |

Not tested: running the amd64 image under podman (needs binfmt registration on the host,
which is host config); docker CLI (not installed; S12 note applies).

### 3.3 AppImage runtime

| Runtime | Licence | FUSE on host (root) | User without `fusermount` | `APPIMAGE_EXTRACT_AND_RUN=1` |
|---|---|---|---|---|
| type2-runtime 20251108 (static-pie, musl) | runtime MIT; statically links **libfuse 3.15 (LGPL-2.1)**, squashfuse 0.5.2 (BSD-2), zstd, zlib (from its `install-dependencies.sh`) | ✓ 143 ms | ✗ rc=127, `$FUSERMOUNT_PROG not set` / `open dir error`; no automatic fallback | ✓ first run 344 ms, then 68–76 ms |

The Fedora 44 host has no `fusermount3`; inside unprivileged sandboxes setuid helpers don't work
at all. So the FUSE path is the exception, not the rule, for bob's targets (containers, CI,
servers). Not in nixpkgs: the pin has no type-2 runtime package (`appimagekit`, `type2-runtime`
absent), so bob must fetch it pinned by hash or build it.

### 3.4 Relocation: userns vs ld.so (bobrun)

Both modes passed every row of the distro matrix (§3.5). Differences, measured:

| | userns mode | ld.so mode (no userns) |
|---|---|---|
| Needs | unprivileged user namespaces | nothing |
| Store paths in data (certs, locale, gconv, tzdata, Go's tz path) | work as-is | work only through env vars the bundle sets (`SSL_CERT_FILE`, `LOCALE_ARCHIVE`, `GCONV_PATH`, …) |
| `/proc/self/exe` | the program (`coreutils`) | `ld-linux-aarch64.so.1` (breaks re-exec of self) |
| exec of other store paths, `#!/nix/store/...` scripts | work | fail (path does not exist) |
| `LD_LIBRARY_PATH` of host | honoured | replaced by `--library-path`; bobrun appends the host value (fix verified) |
| Start (`api`, extract mode, warm) | 76 ms | 68 ms |

Native `api` start: 15 ms. A third method (rewrite `/nix/store` to a same-length `/tmp/xxxxx`
symlink) was considered and not built: it needs a world-writable shared path that another user
can pre-create (hijack) and it changes file contents.

### 3.5 Distro matrix (arm64 rootfs, unprivileged `bwrap` as bobtest, no Nix, no FUSE)

Rootfs: ubuntu:22.04 `sha256:88881eb3…` (glibc 2.35), ubuntu:24.04 `sha256:18b33ff3…` (2.39),
debian:12 `sha256:441888b7…` (2.36), fedora:43 `sha256:8de9a064…` (2.42), alpine:3.22
`sha256:2e1a7aa4…` (musl, no glibc). Ubuntu and Debian base images ship **no CA certificates**.
`nouserns` = `bwrap --disable-userns`. Fedora and Alpine needed an empty `/etc/resolv.conf` in
the rootfs for the bind mount (test fixture only).

| Distro | Mode | `api` | HTTPS (curl bundle) | Locale `en_US.UTF-8` warnings | `date` | `iconv` ä→ISO-8859-15 | `/proc/self/exe` |
|---|---|---|---|---|---|---|---|
| Ubuntu 22.04 | userns / nouserns | ✓ / ✓ | 200 / 200 | 0 / 0 | ✓ / ✓ | e4 / e4 | coreutils / ld.so |
| Ubuntu 24.04 | userns / nouserns | ✓ / ✓ | 200 / 200 | 0 / 0 | ✓ / ✓ | e4 / e4 | coreutils / ld.so |
| Debian 12 | userns / nouserns | ✓ / ✓ | 200 / 200 | 0 / 0 | ✓ / ✓ | e4 / e4 | coreutils / ld.so |
| Fedora 43 | userns / nouserns | ✓ / ✓ | 200 / 200 | 0 / 0 | ✓ / ✓ | e4 / e4 | coreutils / ld.so |
| Alpine 3.22 (musl) | userns / nouserns | ✓ / ✓ | 200 / 200 | 0 / 0 | ✓ / ✓ | e4 / e4 | coreutils / ld.so |

- glibc: the bundle carries its own ld.so and glibc 2.42, so the host glibc version is
  irrelevant for bundled code (even musl hosts work). The kernel is the only shared ABI.
- Locale: nixpkgs glibc reads `LOCALE_ARCHIVE`, not the host's archive. Control on the host:
  nixpkgs `locale` with `LANG=en_US.UTF-8` and no bundled archive → 3 "Cannot set" warnings;
  with the bundled `glibcLocalesUtf8` (2.9 MiB: C, C.utf8, en_US.utf8) → 0. `C.UTF-8` is
  built into glibc ≥ 2.35 and needs nothing.
- Certificates: bundled nss-cacert via `SSL_CERT_FILE`; works on images that have none.
- **Ubuntu 24.04 kernel policy is not tested here**: Ubuntu ≥ 23.10 restricts unprivileged user
  namespaces through AppArmor (`kernel.apparmor_restrict_unprivileged_userns=1`). A rootfs
  can't reproduce that on this Fedora kernel. bobrun's fallback to ld.so mode covers it
  (the `nouserns` rows), but verify on a real Ubuntu 24.04 host.

### 3.6 GL / Vulkan (headless)

Bundle `vulkaninfo` (vulkan-tools 1.4.341, 16.3 MB AppImage); Fedora 43 rootfs with
`mesa-vulkan-drivers` (lavapipe = CPU Vulkan, works without a GPU).

| Case | userns | ld.so |
|---|---|---|
| default env | loader finds `/usr/share/vulkan/icd.d/*.json`, then `libLLVM.so.21.1: cannot open shared object file` → no device | same |
| `LD_LIBRARY_PATH=/usr/lib64` | ✓ `deviceName = llvmpipe (LLVM 21.1.8, 128 bits)` | ✓ same (after the `--library-path` fix) |

Static evidence from the pinned binaries: nixpkgs vulkan-loader searches `/run/opengl-driver/share`
plus the XDG/`/usr/share` defaults; nixpkgs libglvnd searches EGL vendor files in
`/run/opengl-driver/share/glvnd/egl_vendor.d:/etc/glvnd/egl_vendor.d:/usr/share/glvnd/egl_vendor.d`.
So host drivers are *found*, but their own dependencies only load if the host library directory
is on the search path, and they then run against the bundled glibc/libstdc++.
Can't be tested headless here: real GPU drivers (NVIDIA proprietary, Mesa radeonsi/iris), X11/
Wayland presentation, OpenGL through GLX. Rule that follows: drivers built for a glibc *newer*
than the bundled one will fail; bundling an old glibc to "be compatible" is therefore wrong —
bundle a recent one.

### 3.7 Build sizes and times

| Output | Size | [8c/15G] | [2c/4G] |
|---|---|---|---|
| OCI `api` index, per-path layers, cold | 25.4 MB arm64 + 2.2 MB amd64 | 2.0 s | 2.3 s |
| OCI v2 incremental | 3.8 MB new | 0.43 s | 0.57 s |
| AppImage `api` (zstd-19, 1 MiB blocks) | 21.2 MB | 7.6 s | 26.7 s |
| AppImage `api` fixed (strip + prune) | 10.8 MB | 3.6 s | 11.6 s |
| AppImage `curl` + coreutils + glibc tools + locales | 23.4 MB | 7.9 s | 26.7 s |

AppImage digests were identical across all runs and both profiles (e.g. `api` `27257054…`
after the last launcher change). Timings are indicative (other spikes were running); the
reproducibility results are not timing-dependent. `measure.sh` reruns them.

## 4. Answer

- **OCI:** bob writes images itself (≈580 lines of Go with the closure query, stdlib only): one layer per store path,
  ordered by popularity, the application roots on their own top layer, and a small
  customisation layer; above a layer limit (default 100, below the ~127 overlay limit) the least
  popular *library* paths are merged. Byte-reproducible, validated with crane, runs in rootless
  podman. Version 2 of `api` ships 15 % of the bytes.
- **AppImage:** squashfs of the closure at `nix/store`, a bob `AppRun` launcher (userns → ld.so
  fallback), and a type-2 runtime. Works on Ubuntu 22.04/24.04, Debian 12, Fedora 43 and Alpine
  without Nix, FUSE or user namespaces (extract-and-run). Recommended runtime: **bob's own static
  Go runtime** (owner decision O-S8-1), because the upstream one links LGPL libfuse and fails
  without `fusermount` unless an env var is set.
- **Fixup:** strip and path-prefix remapping in package builds; bundle-level pruning of glibc
  locale sources/translations/gconv and gcc sanitizer runtimes. Cuts the `api` bundle from
  21.2 MB to 10.8 MB (closure 75.9 → ~31 MiB).

## 5. SPEC input

### 5.1 API [proposal]

```go
package oci // pkg/output/oci

type Image struct {
    Name       string            // registry repo, e.g. "acme/api"
    Program    bob.Target        // closure roots; own top layer each
    Extra      []bob.Target      // more roots (e.g. a shell for debugging)
    Entrypoint []string          // default: Program's main binary
    Cmd        []string
    Env        map[string]string
    User       string            // default "65534:65534"; "root" must be explicit
    WorkDir    string            // default "/"
    Ports      []int
    Labels     map[string]string // org.opencontainers.image.* filled from the release
    CACerts    bool              // default true: nss-cacert + /etc/ssl/certs symlinks + SSL_CERT_FILE
    TZData     bool              // default true: tzdata + /usr/share/zoneinfo + TZDIR
    Platforms  []bob.Platform    // default {linux/amd64, linux/arm64}; built natively or cross (S7)
    MaxLayers  int               // default 100
    Prune      []string          // see 5.4; default none for images
}

package appimage // pkg/output/appimage

type Bundle struct {
    Name     string
    Program  bob.Target            // main entry
    Entries  map[string]bob.Target // extra entries: BOB_ENTRY=name or argv0 symlink
    Env      map[string]string     // ${STORE} expands to the bundled store at run time
    CACerts  bool                  // default true
    Locales  []string              // default ["C.UTF-8"]; others add a generated locale-archive
    Desktop  *Desktop              // nil → Terminal=true entry and placeholder icon
    Platform bob.Platform          // one AppImage per platform
    Prune    []string              // default: DefaultPrune
    Strip    bool                  // default true for bob-built paths (package fixup)
}
```

Both are ordinary build targets: outputs are store paths (an OCI layout directory; an
`.AppImage` file), so they cache, sign and promote like everything else (S12).

### 5.2 Layer algorithm [proposal]

1. Closure of all roots. Roots = Program + Extra.
2. Popularity(p) = number of closure paths whose closure contains p.
3. Non-root paths sorted by (popularity desc, path asc). With `L = MaxLayers − 1 (custom) − |roots|`,
   the first `L−1` paths get one layer each, the rest share one layer.
4. Then one layer per root, then the customisation layer (`/tmp` 1777, `/etc/passwd`,
   `/etc/group`, `/etc/nsswitch.conf`, cert and zoneinfo symlinks).
5. Layer cache key: `sha256("bob-oci-layer-v1\n" + sorted store paths + prune set)`. A store path's
   layer is built once per bob layer format and reused by every image and platform.

### 5.3 Reproducibility rules [proposal]

Tar: lexical order, parents before children, mtime = 1 s, uid/gid 0, no uname/gname, store
permissions, symlinks as symlinks, hard links written as files, no xattrs, USTAR or PAX chosen by
`archive/tar`. Gzip: Go `compress/gzip` default level, no name, no mtime. The **diff_id** is the
identity; compressed bytes may change with the Go version, so promoted blobs are reused by
diff_id and never recompressed. Config: no `created`; history entries at 1 s. AppImage:
`mksquashfs -all-root -no-xattrs -all-time 1 -mkfs-time 1 -comp zstd -b 1M` (or bob's own
writer, O-S8-2). Test: build twice in fresh dirs, compare digests (CI test).

### 5.4 Default AppImage prune set [proposal]

```
/nix/store/*-glibc-*/share/i18n           # locale sources, only for localedef
/nix/store/*-glibc-*/share/locale         # glibc message translations
/nix/store/*-gcc-*-lib/lib/lib?san.so*    # sanitizer runtimes
/nix/store/*-gcc-*-lib/lib/libhwasan.so*
/nix/store/*-glibc-*/lib/gconv            # only when the bundle sets Iconv: false (default true)
```

### 5.5 AppImage format and launcher [proposal]

- File = runtime ELF + squashfs. AppDir: `AppRun` (bob launcher), `<name>.desktop`, `<name>.svg`,
  `.DirIcon`, `.bob/launch.json` (`{entry, entries, interp, libDirs, env}`), `nix/store/…`.
- Launcher modes, `BOB_APPIMAGE_MODE=auto|userns|ldso`; `auto` = userns, then ld.so on any
  namespace error. `BOB_APPIMAGE_DEBUG=1` prints the choice.
- Env the builder always sets: `SSL_CERT_FILE`, `GCONV_PATH` (glibc in closure), `LOCALE_ARCHIVE`
  (when locales are bundled). ld.so mode appends host `LD_LIBRARY_PATH`.
- Error texts: `bobrun: user namespace: <err>` (forced userns), `bobrun: unknown entry "<n>"`,
  `bobrun: read launch.json: <err>`; exit 127 for launcher errors, child's code otherwise,
  128+signal when killed.
- Own runtime (if O-S8-1 a): ELF magic `AI\x02` at offset 8, squashfs at end of ELF; mount via
  FUSE only if `fusermount3` is usable, otherwise extract once to
  `$XDG_CACHE_HOME/bob/appimage/<sha256>/` (atomic rename) and reuse; supports
  `--appimage-extract`, `--appimage-offset`, `--appimage-version` for tool compatibility.

### 5.6 Supported-distro matrix [proposal]

| Host | Status |
|---|---|
| glibc distros (Ubuntu ≥ 22.04, Debian ≥ 12, Fedora ≥ 43; RHEL 9 and older Fedora unverified) arm64 + amd64 | supported; tested arm64 only |
| musl distros (Alpine) | supported (bundle has its own ld.so); tested |
| Ubuntu ≥ 24.04 with AppArmor userns restriction | supported via ld.so mode; **verify on a real host** |
| GPU programs | host drivers via `LD_LIBRARY_PATH`/a `HostGraphics: true` option that appends host lib dirs after bundled ones; best effort |
| Kernel minimum | the bundled glibc's (`file` reports "for GNU/Linux 3.10.0" for `api`) |

### 5.7 Size budgets [proposal]

- OCI build: ≤ 3 s cold for a 100 MiB closure, ≤ 1 s incremental [2c/4G].
- AppImage build: ≤ 30 s for a 100 MiB closure at zstd-19 [2c/4G]; use zstd-15 if over.
- Fixture `api` AppImage with default prune ≤ 12 MB; OCI image of `api` ≤ 26 MB compressed.
- Extract-and-run start ≤ 0.5 s first run, ≤ 0.1 s cached [8c/15G].

### 5.8 Licence notes

- type2-runtime: MIT, but the binary contains LGPL-2.1 libfuse 3 statically linked. Shipping it
  inside every user's AppImage makes bob (and its users) distribute an LGPL static binary:
  relinking material or source offer must be provided. Avoided by O-S8-1 a.
- squashfs-tools: GPL-2.0, used as a build tool inside a derivation (not linked, not shipped).
- Bundled closures carry their own licences (glibc LGPL, openssl Apache-2.0, …). bob should
  write a `licences.json` from nixpkgs `meta.license` into each bundle/image label [proposal].

## 6. Decisions, risks, open items

### Agent decisions

| # | Decision | Reason |
|---|---|---|
| S8-1 | Own OCI writer in Go, no go-containerregistry dependency | ~580 lines of stdlib; full control over reproducibility (principles 6, 8) |
| S8-2 | Roots always on their own top layer | measured: 15 % vs 96 % new bytes for v2 |
| S8-3 | Default `User` 65534 in images | non-root by default; podman verified |
| S8-4 | userns first, ld.so fallback, both in one launcher | only combination that passed every distro without FUSE/userns, while keeping full fidelity where userns exists |
| S8-5 | Bundle a recent glibc, never an old one | host GPU drivers need glibc ≥ their build glibc |
| S8-6 | amd64 `api` replaced by a Go static stub | B has no cross flags yet; the pinned `pkgsCross.gnu64` gcc is substitutable from cache.nixos.org (verified), S7 owns cross |
| S8-7 | Pruning is per output, not per package | nixpkgs paths stay pristine in the binary cache |

### Owner decisions needed

**O-S8-1 AppImage runtime.**
a) bob's own static Go runtime (extract-to-cache, FUSE optional) — no LGPL binary in user
bundles, fits "bob is the only executable", ~1 week; *recommended*.
b) Upstream type2-runtime pinned by hash — works today; LGPL relinking obligation for every
bundle; needs `APPIMAGE_EXTRACT_AND_RUN=1` on hosts without fusermount (otherwise exit 127).
c) Upstream runtime rebuilt by bob from source against libfuse as a shared library — LGPL
obligation gone, but then FUSE needs the host's libfuse3 (not on base images).

**O-S8-2 squashfs writer.** a) `mksquashfs` from nixpkgs inside the bundle derivation (GPL tool,
not shipped; *recommended*, verified reproducible) · b) a Go squashfs writer library (licence and
reproducibility unverified) · c) own writer (most work).

**O-S8-3 Image compression.** a) gzip only in v1 (*recommended*: every client) · b) gzip + zstd
variants · c) zstd only (smaller, older Docker can't pull).

### Risks

- Ubuntu 24.04 userns restriction not tested on a real host.
- ld.so mode can't run programs that exec other store paths or use shebang scripts; bob must
  warn at build time when the entry is a script or the closure contains wrappers
  (`makeWrapper` output) and suggest userns-only.
- Mixing host GPU drivers with bundled libstdc++/glibc works in the lavapipe test; proprietary
  drivers untested.
- Popularity is closure-local; a fleet-wide popularity (glibc, openssl shared across projects)
  would improve registry dedupe further — not measured.
- The upstream runtime's FUSE path exits 127 when `fusermount` is missing; users see a confusing
  message unless bob owns the runtime.

### Still to verify

amd64 AppImage and amd64 `api` from S7 cross; Ubuntu 24.04 host with AppArmor; real GPU;
`docker pull`; podman run of the amd64 image with qemu binfmt; own runtime prototype (O-S8-1 a);
OCI images with the prune set applied.
