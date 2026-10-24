# Fixture repositories

One directory per fixture repository (HARNESS.md §4). SPEC's `examples/<name>` means
`tests/fixtures/repos/<name>` (decision A22). Protected after Gate H.

All fixtures follow ADR 0020 (owner D71/D72, 2026-10-24): one package per directory with its own
`bob.go`, sources only below it (no `..`, no files of a subpackage), one library or binary per
directory, typed nixpkgs handles (`nixpkgs.Openssl.Lib(p)`), Go declarations in the Go package's
own `bob.go`, main-package binaries reached through `bob.Slot`. `TestRFW070FixtureLocality`
(tests/core) enforces it.
