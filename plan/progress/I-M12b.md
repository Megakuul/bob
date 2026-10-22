# I-M12b progress (M12.3 AppImage: pkg/output/appimage + gen + bop + runtime)

Owner of: pkg/output/appimage (API, internal/aispec, gen, gen/runtime, bop). Small additive
changes elsewhere are listed under "files".

## coordination with I-M12a (oci/tarball/release)
- Shared helpers (landed; use them, tell me if you need changes):
  - pkg/builder/closure.go: `(*Ctx).ReferencesGraph(attr)` reads exportReferencesGraph.<attr>
    from $NIX_ATTRS_JSON_FILE; `(*Ctx).CodedDiag(severity, code, args, msg)` writes a diagnostic
    with a bob code. The translator maps code 2015 (+ args pattern/file/user) to BOB-E-2015 and
    renders it `✗ <label>: prune pattern "…" removes <file>, which <user> needs` (exit 1); code
    2016 (severity warning, args reason) to the warning line `warning: <label>: <reason>`.
  - pkg/builder/licences.go: `builder.Licences` / `builder.WriteLicences(file, storeDir, closure,
    table map[string]bspec.LicenceInfo)`, `builder.SplitName`; bspec.LicenceInfo {name, version,
    licenses}.
  - pkg/engine/nixidx: Entry.Pname, Entry.Licenses (json licenseIds; SPDX ids or shortName),
    indexVersion 2. Generator side: see pkg/output/appimage/gen/eco.go licenceTable (looks up the
    bundle's nixpkgs deps + glibc, gcc-unwrapped, cacert, tzdata; maps every output path).
  - translate render: definitionLine case 2015 (plan-time BOB-E-2015 errors render as ✗ too).
- BuilderVersion: 9 = appimage (I-M12a took 10 after me).

## done (checkpoint 22:35)
- runtime: pkg/output/appimage/gen/runtime (own Go module, only klauspost/compress): squashfs
  4.0 reader (zstd, fragments, ext inodes), extraction cache ($XDG_CACHE_HOME/bob/appimage/<sha>,
  ~/.cache, $TMPDIR/bob-appimage-<uid>; temp dir + rename + .complete), flags --appimage-extract
  [glob]/-offset/-version/-help, launcher modes userns (re-exec with CLONE_NEWUSER|NEWNS, ambient
  CAP_SYS_ADMIN/CHROOT for the setup only, tmpfs root + binds of every top-level entry but /nix,
  bundled store at /nix/store, chroot, cwd, ambient caps cleared) / ldso (bundled ld.so
  --library-path) / auto (fallback on setup errors reported through fd 3), signals forwarded,
  exit 127/child/128+sig, HostGraphics dirs, BOB_ENTRY/argv[0], UsernsOnly refusal.
  Runtime size 2.8 MB (budget 3 MiB). Embedded gz in gen/runtimebin (TestRuntimeEmbedded checks
  freshness; -update-runtime rewrites).
- bop kind "appimage": closure copy with Prune globs (+NoIconv gconv), DT_NEEDED check → 2015,
  launch.json, licences.json, .desktop + icon/.DirIcon, locale archive via localedef, AppRun,
  mksquashfs, runtime + image with AI\x02. NoFixup.
- gen: one drv per bundle (exportReferencesGraph closure, unsafeDiscardReferences,
  allowSubstitutes=false), runtime imported as source path bob-appimage-runtime-<arch>.
- API: NewBundle (validations, Platform via ext.Elsewhere), nixpkgs.Exe, bob.Exe.
- Verified by hand on a cc scratch repo: build 4-10 s, run in userns/ldso/auto, BOB_ENTRY, iconv
  e4, curl HTTPS 200, en_US.UTF-8, SIGTERM → 143 both modes, script warning, prune ✗ 2015.

## next
- acceptance tests tests/outputs (polyglot), reproducibility, framework.zip, CI checks.

## files (outside ownership)
- pkg/builder/closure.go, licences.go (new), bspec (LicenceInfo, BuilderVersion 9),
  cmd/bob-builder/main.go (aibop), pkg/cli/link.go (blank import), pkg/engine/store/builders.go
  (regenerated), pkg/engine/nixidx (resolve.nix, Entry, indexVersion 2), pkg/engine/translate
  (classify.go, translate.go, render.go, errors.go), pkg/bob/diag/codes.go + codes.lock (2016
  appended) + tests/goldens/diag/codes.golden (regenerated with -update), pkg/bob/exe.go,
  pkg/nixpkgs (Exe, Program, ExampleExe).

## DECISIONS
- D-M12b-1 (O-04-2 by its recommendation, A24): bob embeds prebuilt static runtimes for both
  platforms and imports them as content-addressed source paths; no compile on first bundle.
  The runtime is a separate Go module (pkg/output/appimage/gen/runtime) so that its module graph
  is exactly what ships (stdlib + klauspost/compress BSD-3) and so that the layer table (L3 may
  not import third-party code) and the framework zip (gen/ is skipped) are untouched.
- D-M12b-2 (O-04-3): own minimal squashfs reader + klauspost zstd.
- D-M12b-3: mksquashfs runs without -all-time/-mkfs-time: it refuses them next to
  SOURCE_DATE_EPOCH, which bob-builder always sets to 1 (same effect; superblock mkfs_time 1 and
  inode times 1 verified).
- D-M12b-4: userns mode maps the caller's uid/gid to themselves; the setup runs with ambient
  CAP_SYS_ADMIN+CAP_SYS_CHROOT (needed after exec as non-root), cleared before the program.
- D-M12b-5: entries run with argv[0] = the entry's file name (multi-call programs such as
  nixpkgs coreutils dispatch on it); ARGV0/APPIMAGE/APPDIR carry the AppImage's own values.
- D-M12b-6: a nixpkgs entry's program is taken from its "bin" output when it has one (curl,
  glibc keep their programs there), else "out".
- D-M12b-7: launch.json libDirs = directories holding lib*.so* files (plug-in dirs such as gconv
  or OpenSSL engines are not on the search path); interp = PT_INTERP of the main entry (or of
  the first dynamic entry); in ldso mode the runtime reads each entry's own PT_INTERP.
- D-M12b-8: new code BOB-E-2016 `{label}: {reason}` (build class, warning of a successful
  build), rendered `warning: <label>: <reason>` (R-OUT-019 text). codes.lock appended, codes
  golden regenerated with its -update mechanism (SPEC §02.3 row to add).
- D-M12b-9: Bundle.Platform other than p's platform evaluates the programs with ext.Elsewhere.
- D-M12b-10: licences: nixpkgs meta.license SPDX ids for every output path of the bundle's
  nixpkgs deps and glibc/gcc-unwrapped/cacert/tzdata (host index; cross bundles get "unknown"
  for target paths); other paths: store-name pname/version, "unknown".

## NEEDS

## TEST PROBLEM
