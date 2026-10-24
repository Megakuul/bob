# T3b progress
DONE clients → tests/serverenv/clients/clients.go (Nix(t), Podman(t); smoke tests pass; podman version OK)
DONE-STUBS pkg/output/{oci,appimage,tarball}/*.go pkg/release/release.go (SPEC signatures; bob.Source→bob.Sources); tests/outputs helpers_test.go+doc.go
NOTE budget ids skipped: R-OUT-024 (B-086), R-OUT-025 (B-087), R-PLAT-008 (B-085); R-PLAT-002 manual
DONE R-OUT-001 → tests/outputs/build_test.go::TestROUT001ClosureOutput,TestROUT001RemoteClosureSubstitutable
DONE R-OUT-006 → tests/outputs/build_test.go::TestROUT006BundlesAreBuildTargets
PINS NEEDED (tools/pins.json nix map, names used via serverenv.NixTool): crane=crane, skopeo=skopeo, squashfs-tools=squashfsTools, qemu-user=qemu-user (T1b uses same name), bubblewrap not needed (tests/rootless/enter is used), vulkan-tools (R-OUT-020), glibc-locales? (none)
DONE R-OUT-002,009,010,011,012,013 → tests/outputs/oci_test.go (TestROUT002*,009*,010*,011*,012*,013*); goldens needed: tests/goldens/outputs/oci-index-digest.txt
DONE R-OUT-022,023 → tests/outputs/release_test.go (golden tests/goldens/outputs/release.json needed; schema pkg/release/schema/release-v1.json written)
DONE R-OUT-007,008 → tests/outputs/appimage_test.go::TestROUT007Pruning,TestROUT008LicenceManifest
DONE R-OUT-015,016,019 → tests/outputs/appimage_test.go (goldens needed: tests/goldens/outputs/appimage.sha256)
DONE R-OUT-004 → tests/outputs/tarball_test.go::TestROUT004Tarballs
SPEC problems so far: R-OUT-001 acceptance uses {"type":"result"} but §02.4 events are {"event":...}: tests use package.finish paths; `bob build --version` not in §02 synopsis; bob.Source vs bob.Sources; no constructor for nixpkgs bob.Executable (used nixpkgs.Exe) nor script Executable (bob.Exe) in overlays; R-OUT-018 says bwrap but harness has tests/rootless/enter (used); R-OUT-013 token/netrc and `bob build --remote --version` mechanics unspecified
DONE R-OUT-003,017,018,020 → tests/outputs/appimage_distro_slow_test.go (//go:build slow; TestDistroAppImageMatrix, TestDistroHostGraphics; pin 'mesa' needed; amd64 rows manual). ALL R-OUT done; next tests/platforms
DONE R-PLAT-001,004,005,006,007 → tests/platforms/platform_test.go (decision: BOB_TEST_NIXPKGS_INDEX hook for R-PLAT-007; setarch linux32 for uname). SKIP R-PLAT-002 (manual), R-PLAT-008/R-OUT-024/025 (budgets B-085..087). Remaining: lint/ci check + report
