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

## done (final 23:15)
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

- 22:55: licence table per bundle (a bundle's drv no longer depends on which other bundles are
  planned in the same wave: found by comparing two drvs of one bundle); prune check names the
  entry first and resolves candidates inside the AppDir; closure-wide makeWrapper detection
  (scripts in bin/ that exec a /nix/store program, makeBinaryWrapper ELFs).
- Reproducibility: //deploy:small-appimage and //deploy:tools of the cc scratch repo built in two
  fresh bob homes: identical drv paths and identical AppImage bytes; second build counts.built 0.
- Distro matrix by hand (tests/rootless/enter as bobtest, 5 cached rootfs images: alpine-3.22,
  debian-12, fedora-43, ubuntu-22.04, ubuntu-24.04) × {userns, ldso, blocked+auto, blocked+ldso}:
  program line, date, iconv e4, curl HTTPS 200 all pass (arm64 native).
- Start-up [8c/15G, load ~20]: first run 0.30 s (12.9 MB bundle), cached 0.024 s.

## next
- acceptance tests in tests/outputs need the polyglot fixture, which does not evaluate yet
  (`undefined: proto.Library` etc., pkg/proto is M10): see TEST PROBLEM / still red.

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

## still red (not caused by this unit)
- tests/outputs TestROUT007/008/015/016/019 (+ slow TestDistro*): every one builds the polyglot
  fixture, which fails evaluation (`undefined: proto.Library/LibraryOpts/Check/Lint`, pkg/proto is
  M10). Ran TestROUT015|TestROUT019 at 22:07: exit 3 at evaluation, nothing of the AppImage path
  reached. Their checks were reproduced by hand on the cc scratch repo (see done); the golden
  tests/goldens/outputs/appimage.sha256 (frozen polyglot fixture) cannot be produced yet, so no
  golden candidate in out/golden-candidates.
- CI vet stage fails only on out/m12check and out/uicheck (not mine); fmt/lint/layering pass.
  The runtime module (pkg/output/appimage/gen/runtime) is outside the root module: vet it with
  `cd pkg/output/appimage/gen/runtime && GOWORK=off go vet .` (clean); TestRuntimeEmbedded
  (pkg/output/appimage/gen, skipped with -short) rebuilds it and fails when runtimebin/ is stale.

## NEEDS
- coordinator: regenerate pkg/engine/store/builders.go (go generate ./pkg/engine/store) and
  pkg/engine/eval/framework.zip (go generate ./pkg/engine/eval) after I-M12a lands (both fresh
  at 23:15 with BuilderVersion 10).
- decisions.md: copy D-M12b-1..10 and test corrections 1-3 (+ codes golden) from here.

## TEST PROBLEM (corrections applied, owner-approved evidence-based class)
1. tests/outputs/overlays_test.go apptestGo `script` step: Tools were only coreutils but Cmd runs
   `sh -c …`; bob-builder looks argv[0] up only in the declared tools (R-NIX-016) and nixpkgs
   coreutils 9.11 has no sh (checked: `ls <coreutils>/bin | grep -x sh` → none), so the step
   could never run and R-OUT-019's build could not succeed. Applied: Tools gains
   nixpkgs.Tool(p, "bash") (provides sh).
2. tests/outputs/appimage_test.go checkLicences: wanted glibc `LGPL-2.1-or-later`; nixpkgs
   meta.license of glibc at the pin is lgpl2Plus = `LGPL-2.0-or-later` (nix eval of
   legacyPackages.aarch64-linux.glibc.meta.license at cd4ea8d5 and da6dc0e7), and R-OUT-008 says
   licences come from nixpkgs meta. Applied: accept LGPL-2.0-or-later or LGPL-2.1-or-later; SPEC
   R-OUT-008 acceptance aligned (spec/04 + SPEC.md).
3. tests/outputs/appimage_distro_slow_test.go: `date -u -d @0` with LANG=en_US.UTF-8 and the
   bundled en_US locale prints `Thu Jan  1 12:00:00 AM UTC 1970` (glibc en_US date_fmt uses %r;
   the host's own coreutils prints the same); the test wanted the C form only. Applied: accept
   both (the "no locale warnings" check is unchanged).
4. NOT a test edit: tests/goldens/diag/codes.golden regenerated with `go test ./pkg/bob/diag
   -update` for the appended code 2016 (D-M12b-8).
