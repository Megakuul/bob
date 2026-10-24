# I-M2b progress (M2.2 store, then M2.5 srcs, M2.3 drv import)

## done
- M2.2 pkg/engine/store + scripts/fetch-nix.sh (library level complete; CLI wiring is another unit's)
  - fetch-nix.sh: pinned NAR URL + sha256 + NAR sha256 + bin/nix sha256 per arch (cache.nixos.org,
    nixStatic 2.34.9 at the nixpkgs pin); strip with host strip; gzip-9 in Go; writes
    pkg/engine/store/nixbin/<arch>/{nix.gz,nix.sha256} (gitignored by nixbin/<arch>/.gitignore)
  - layout.go (state home, layout, flock, nix.conf/netrc/user.conf), nixbin.go (extraction),
    ns.go (namespace child via init hook, Enter, Entered), refusal.go (R-NIX-022 causes/fixes),
    hooks{,_bobtest}.go (BOB_TEST_CLONE_ERRNO / BOB_TEST_MOUNT_ERRNO), store.go (Open, Command,
    NixEnv, Invalid, Import, sqlite busy retry → 7004), export.go (WriteExport, DrvExport)
  - own tests pkg/engine/store/store_test.go green as root and as bobtest; pivot mode verified
    nested in the S0b bobns -hide-nix; real max_user_namespaces=0 → 7005 verified
    (unshare -Ur, TestOpenRefusal); Enter entry 4–5 ms [8c/15G]; extraction ~0.3 s

- M2.5 pkg/engine/srcs: Path, Import (one hash+serialise pass, NARs kept ≤ 256 MiB, one
  batched Invalid, one import stream), Resolve/ResolveAll (R-FW-021 file sets: globs with "**"
  and "!", gitignore semantics in Go incl. nested .gitignore and .git/info/exclude, always-excluded
  .git/ .bob/ bob.go zz_bob_cgo.go .gitignore, skip func for the materialisation manifest,
  1025 outside/noglob/symlink). Green: TestRNIX009SourcePathEqualsNixStoreAdd,
  TestRNIX009ImportMissingOnly, TestRNIX009Import10000SourceDirs; own tests resolve_test.go,
  import_test.go.
  - Measured [8c/15G, shared host, tmpfs /tmp]: 10,000 dirs cold import 4.0–4.3 s (budget 5 s;
    nix-store --import itself ≈ 3.5 s of it), warm 0.5 s. On disk (/var/tmp) Nix's import alone
    takes ≈ 8 s → B-076 has little margin; only reachable by splitting the stream (SPEC says one
    stream), so left as is and flagged.
- M2.3 pkg/engine/store/drvcache.go: DrvKey (length-prefixed sha256 of canonical node, sorted
  input drvs, generator version, builder path), DrvCache (<home>/cache/drv/<k[:2]>/<k>, atomic
  writes), Store.ImportDrvs (batched validity, topological one-stream import, returns the
  drv.imported count). Green: TestRNIX007ExportStreamGolden; own TestImportDrvs, TestDrvCache.

## still red (not in my packages)
- every CLI-driven test of R-NIX-007/020/021/022/024/025/026 (bob build / doctor are stubs in
  pkg/cli, pkg/engine/build): see NEEDS. TestRNIX020Attribution: ATTRIBUTION.md (NEEDS).
- tests/core TestRFW021*: need build.SourcePaths (pkg/engine/build) → srcs.ResolveAll + srcs.Path.

## files
- scripts/fetch-nix.sh, pkg/engine/store/{layout,nixbin,ns,refusal,hooks,hooks_bobtest,store,
  export,drvcache,embed_arm64,embed_amd64}.go, store_test.go, nixbin/{arm64,amd64}/.gitignore,
  pkg/engine/srcs/{srcs,ignore}.go, resolve_test.go, import_test.go

## DECISIONS
- D-M2b-1: namespace child = same binary, recognised by an init() hook on argv[1] ==
  "__bob_namespace" (works for bob, bob-server and test binaries without main changes). The
  child, which has run no bob code, ends with syscall.Exit on setup failure (os.Exit is banned
  outside main; there is nothing to clean up). Status reported on a close-on-exec fd 3.
- D-M2b-2: two entry modes: `Enter` (one entry per bob command, the CLI re-execs itself; Nix
  then runs directly) and, for library callers outside the namespace (tests, bob-server), each
  Command enters through the child (~5 ms). "Inside" is detected structurally: /nix and
  <home>/nix have the same dev+inode (no env var can fake it).
- D-M2b-3: pivot mode needs an empty mount point: `<home>/nsroot` (not in SPEC's layout list;
  an addition, not a change).
- D-M2b-4: the extraction dir hash is the sha256 of the uncompressed embedded binary (written
  by fetch-nix.sh as nix.sha256, verified while extracting); later runs only stat.
- D-M2b-5: a bob built without running fetch-nix.sh compiles (embed pattern matches the
  committed .gitignore) and fails at Open with BOB-E-7003 "built without its embedded Nix".
- D-M2b-6: amd64 Nix is embedded unstripped when the host strip cannot read x86_64 ELF (arm64
  host); pinned download and binary hashes are still verified.

- D-M2b-7: srcs.Resolve also excludes `.gitignore` files (not in SPEC's list): otherwise
  editing an ignore list changes source paths, contradicting R-FW-021 acceptance (a).
- D-M2b-8: Glob negation is order-independent (a file is selected when it matches any positive
  and no "!" pattern); a positive pattern naming a directory selects its subtree (bob.Dir);
  every positive pattern must match at least one file (1025 noglob); symlinks are kept, never
  followed, and must resolve inside the repo (absolute targets count as outside).

## NEEDS
- pkg/engine/build (owner of build.SourcePaths / the build pipeline): per node
  `files := srcs.ResolveAll(root, node.Srcs, pos, manifestSkip)`, name `<store-name>-src`,
  `srcs.Path`/`srcs.Import`; drvs via `store.DrvKey` + `Store.DrvCache()` and
  `Store.ImportDrvs` (its count is the `drv.imported` event).
- ATTRIBUTION.md (coordinator; I do not own it): TestRNIX020Attribution wants source locations
  (https://). Proposed: in the Static Nix row set Where = `pkg/engine/store/nixbin/<arch>/nix.gz`
  (fetched by scripts/fetch-nix.sh) and add Source = https://github.com/NixOS/nix/tree/2.34.9 and
  the derivations s55n25gmlcyyhvchm0bdcfdqf2nqyxxp-nix-static-aarch64-unknown-linux-musl-2.34.9.drv /
  yxd2ay3ha18vcx4n0gw7f5d1f7xhps3w-nix-static-x86_64-unknown-linux-musl-2.34.9.drv of nixpkgs
  https://github.com/NixOS/nixpkgs/tree/cd4ea8d57de16f4810bc5f14207bac45fbf19f67; busybox row:
  https://busybox.net/downloads/ (version as in that derivation's closure).
- CLI (pkg/cli owner): call store.Enter first in commands that touch the store (build, dev,
  exec, lock, doctor …) and return its exit when !inside; emit the `namespace` stage event with
  durationMs from store.Entered(); `bob doctor --print-nix-env` prints Store.NixEnv() one per
  line; render BOB-E-7005 with Error.Cause and the multi-line fix indented 4 spaces.
- Build steps: run Nix through Store.Command (never exec.Command on the host PATH).

## TEST PROBLEM
