# I-C2 (M16.6 bob install / upgrade / rollback / uninstall / list / registry) — pkg/cli, pkg/cli/remote

## Done
- Read AGENTS, impl-brief, HARNESS, I-C1/I-B18/I-M12a/I-M12b/I-M6a, SPEC §02.8 (R-INST-001…004, 006),
  §04 §4.8/§4.9 (bob.release/v1, R-INST-005), §06 §6.8 (server side of R-INST-003), proto release.proto,
  tests/install, tests/cli/fakeserver. The SPEC defines no kill-point hook: "harness kill points" are
  the time offsets of TestRINST001KillPoints (SIGKILL 0…1500 ms after start); nothing to implement.
- pkg/cli/remote (std library only, A110): release.go (ResolveRelease / ListReleases / GetServerInfo,
  Connect GET), narinfo.go (narinfo parser with hostile-input bounds, Nix fingerprint + ed25519 check
  against pinned keys, hash normalisation base-16/SRI → nix32, `Cache.Fetch`: closure walk over the
  registry's /nix with Bearer token or anonymous, NAR download with FileSize/FileHash/NarHash checks,
  laid out as a local binary cache).
- pkg/cli: cmd_install.go (install, upgrade [--minor-only], uninstall, rollback [--to N], list
  [--available], exec --installed), cmd_install_profile.go (generations, flock, create-only files,
  atomic `current` flip, GC roots, shims, conflicts), cmd_install_registry.go (registry add [--key …]
  [--refresh-keys] / remove / list / default, config.json, PATH hint, completion helpers).
  cli.go: table wiring only (run funcs + flags; synopses unchanged: help goldens are protected).
  complete.go: registry names after `registry remove|default`, installed names for uninstall/upgrade.
- Install flow: ref → registry (config.json) → WhoAmI pre-check when a token exists (R-NIX-029) →
  ResolveRelease → `nix-store --check-validity` of the roots → bob downloads the missing closure
  (registry cache; references the registry lacks are left to cache.nixos.org) into
  <home>/cache/install/fetch-*, then `nix-store --realise` with substituters = file://<that dir>
  (+ cache.nixos.org only when needed) and trusted-public-keys = the registry's pinned keys → bins
  = executables of <root>/bin → lock → re-read current → conflicts → commit → shims.
- Crash safety: flock install/lock for every profile change (released by the kernel on SIGKILL);
  roots dir n written first (tmp + rename), then generations/n.json create-only (tmp + fsync + link),
  then `current` flipped (symlink + rename), then old roots pruned; shims are reconciled to `current`
  by every mutating command (also a no-op upgrade), so a later run finishes a killed one; stale
  `.tmp-*` removed under the lock. New generation number = highest on disk + 1.
- Verified with the embedded Nix (temporary test, removed): `nix-store --gc --print-roots` lists
  gcroots/bob-install/<n>/<path>, and `nix-store --gc` keeps the installed path.
- Unit tests: pkg/cli/cmd_install_test.go (ref grammar, generations 1–7 → roots 3–7, rollback re-roots,
  create-only, conflicts, shim text/mode/removal, foreign file kept, cacheURL host pinning),
  pkg/cli/remote/narinfo_test.go (closure walk, missing reference, have-skip, Bearer, untrusted key,
  missing root, hostile narinfos, hash forms).

## Tests (run directly, 23:00–00:20, host load 16–23 on 8 CPUs)
- Green: tests/install TestRINST001Lifecycle, TestRINST001KillPoints (16/16), TestRINST002NoSystemNix,
  TestRINST002NothingToInstall, TestRINST002ShimOverhead, TestRINST003PublicWithoutAuth,
  TestRINST004RegistrySelection, TestRINST004KeyChange, TestRINST004ListAndUpgrade,
  TestRINST005Conflicts, TestRINST005GCRoots; pkg/cli, pkg/cli/remote, pkg TestLayering,
  tests/cli TestRCLI001*.
- TestRINST005Layout: everything passes except the two goldens, which do not exist yet. Candidates
  written (BOB_GOLDEN_UPDATE=1) to out/golden-candidates/tests/goldens/install/{generations-1.json,
  shim-acme.sh} — owner review.
- TestRINST003PrivateNeedsLogin: TEST PROBLEM 1 (every other step passes: 4008 with BOB_TOKEN, WhoAmI
  pre-check, install after login, netrc holds the token).
- TestRINST006NoProjectState: not install code. Run 1 (183 s): install leaves the tree untouched, the
  program's env equals the caller's (hook and bob dev) — fails only on "differs from the one from /
  in [PATH]" (NEEDS 1). Run 2: `bob allow` exceeded the harness 4 min (cold evaluation under load).
- gofmt clean; GOWORK=off go vet ./pkg/cli/... clean; tools/ci -only fmt,vet,lint,layering: fmt, lint,
  layering ok; vet fails only on out/uicheck (R-ARCH-001, another unit's leftover directory).

## DECISIONS
- D-IC2-1 bob downloads release closures itself (Bearer token or anonymous, signatures checked against
  the registry's pinned keys) and hands them to the embedded Nix as a temporary local binary cache
  (file://), which checks the same signatures again and decompresses. Reasons: (a) Nix sends netrc
  credentials as HTTP Basic, which the fake server (and only Bearer-aware caches) refuse; (b) no
  nix.conf change per install (D-IC1-4: every command writes the same nix.conf), the token never
  reaches Nix at all (stricter than "only through netrc"); (c) exact BOB-E-4001/4002/4003/4004/4005
  mapping without parsing Nix text; (d) any NAR compression works without a Go zstd/xz (glue is std
  library only). Paths the registry's cache lacks come from cache.nixos.org via Nix.
- D-IC2-2 Entry binaries = every executable in bin/ of each closure root: ResolveRelease's
  PlatformClosure carries roots but not the manifest's `entrypoints` (proto gap; matches the manifest
  default for an empty MainProgram). SPEC/proto could add entrypoints to PlatformClosure.
- D-IC2-3 Profile directory = <store home>/install (= $XDG_DATA_HOME/bob/install unless BOB_HOME is
  set): the GC roots and the store paths live in that store home, so the profile follows it.
- D-IC2-4 GC roots: gcroots/bob-install/<n>/ is a directory of symlinks (one per store path), roots of
  the newest 5 generation files plus the current generation (a rollback to an old generation re-roots
  it; refused with BOB-E-0004 when its paths were collected).
- D-IC2-5 NotFound from ResolveRelease without credentials → BOB-E-4010 with the hint "… is private or
  does not exist; log in with bob login <url> (or set BOB_TOKEN)" (no existence leak, R-INST-003);
  with credentials → BOB-E-4011. Shim/entry conflicts → BOB-E-0004 "bob install needs a free command
  name" with the SPEC text as `cause:` (0004's template is `{flag} needs {what}`).
- D-IC2-6 The cache URL is ResolveRelease's substituter only when it is on the registry's own
  scheme+host, else <registry>/nix: the token is never sent to another host.
- D-IC2-7 `bob registry add` of an existing name with the same URL re-pins (like --refresh-keys); a
  different URL is refused (remove first). The first registry is the default when defaultRegistry is
  unset; otherwise "default", else the only one.
- D-IC2-8 Generation entries get `binDir` only for --bin-dir outside the default (omitempty, so the
  golden layout is unchanged); `bob install` of an identical entry writes no new generation.
- D-IC2-9 `bob upgrade --minor-only` resolves with the current major as constraint ([proposal] in SPEC).

## NEEDS
1. devenv (I-M6a): SPEC §02 line 459 says `bob env --format json` has `outside:{set, prepend:{PATH:[…]}}`;
   env.json's Outside has only `set`, while the hook prepends <root>/.bob/bin to PATH. TestRINST006
   reads the exported variables from there, so PATH counts as a difference.
2. Proto/SPEC: PlatformClosure.entrypoints (D-IC2-2).
3. Real-server end to end (devstack) not run: a published release needs the worker staging of I-B20
   (each release build ≥ 20 min here). The client speaks the proto JSON names of release.proto and
   handles any narinfo hash form and NAR compression.

## TEST PROBLEM
1. tests/install/auth_test.go TestRINST003PrivateNeedsLogin requires `BOB-E-4010` in the output of
   `bob install acme/secret` (line 64) and then calls checkNo401 on the same output (line 69), which
   fails on any occurrence of the substring "401" — contained in "BOB-E-4010" itself. The test cannot
   pass for any implementation. Output: `error[BOB-E-4010]: 127.0.0.1:38613 denied ResolveRelease on
   project acme` / `hint: acme/secret is private or does not exist; log in with bob login …`.
   Fix: let checkNo401 look for " 401" / "HTTP 401" / "401 Unauthorized" (Nix's wording) or strip
   `BOB-E-\d{4}` before the check.
