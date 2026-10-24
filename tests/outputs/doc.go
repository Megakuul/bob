// Package outputs holds the acceptance tests of SPEC §04 outputs (R-OUT-*): closures, OCI images,
// AppImages, tarballs, release manifests (HARNESS.md §7.3, §8.2, §14).
//
// The fixture is tests/fixtures/repos/polyglot (cgo api with C++/openssl and Rust parts; deploy/bob.go
// declares //deploy:api-image, api-appimage, api-tar and the release //deploy:api). Variants that
// the fixture does not declare (a script entry, a pruned bundle, a static tarball, an image with
// curl and coreutils) are overlays: the test writes an extra bob.go package into a copy of the
// fixture. The pure-Go fixture binary for Static tarballs is //tools/cmd/stamp:stamp of the go fixture.
//
// Every test builds first and returns at the first BOB-E-9008 (harness.Red), so a test is red for
// "not implemented" and never earlier for a missing tool. External tools (crane, skopeo,
// squashfsTools, qemu-user, ...) are looked up through serverenv.NixTool after that point; the
// pins they need are listed in plan/progress/T3b.md.
//
// Run: go test ./tests/outputs/... ; the distro matrix (R-OUT-003/017/018/020, HARNESS §7.3) is
// behind the slow tag: go test -tags slow -run Distro ./tests/outputs/ -timeout 3h.
package outputs
