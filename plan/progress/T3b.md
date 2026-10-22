# T3b progress
DONE clients → tests/serverenv/clients/clients.go (Nix(t), Podman(t); smoke tests pass; podman version OK)
DONE-STUBS pkg/output/{oci,appimage,tarball}/*.go pkg/release/release.go (SPEC signatures; bob.Source→bob.Sources); tests/outputs helpers_test.go+doc.go
NOTE budget ids skipped: R-OUT-024 (B-086), R-OUT-025 (B-087), R-PLAT-008 (B-085); R-PLAT-002 manual
DONE R-OUT-001 → tests/outputs/build_test.go::TestROUT001ClosureOutput,TestROUT001RemoteClosureSubstitutable
DONE R-OUT-006 → tests/outputs/build_test.go::TestROUT006BundlesAreBuildTargets
PINS NEEDED (tools/pins.json nix map, names used via serverenv.NixTool): crane=crane, skopeo=skopeo, squashfs-tools=squashfsTools, qemu-user=qemu-user (T1b uses same name), bubblewrap not needed (tests/rootless/enter is used), vulkan-tools (R-OUT-020), glibc-locales? (none)
