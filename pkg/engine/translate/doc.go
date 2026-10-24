// Package translate turns Nix's `--log-format internal-json` stream into bob diagnostics
// (ADR 0017, SPEC R-NIX-010, R-NIX-012). A Translator is fed every stderr line of one Nix run
// together with bob's own facts (Context: which derivation is which package, where it was
// defined, the sources, limits and caches) and ends with the process outcome and bob's
// structural output check. The Report holds *diag.Error values in bob terms (label, bob.go
// position, log excerpt with repo paths, hint, `bob log` command) and never shows raw Nix text
// except in Raw, which output renders only with -v.
//
// Classification order: the level-0 message by an anchored pattern table of the embedded Nix,
// then the failing package's build log by toolchain patterns, then level-1 warnings for cache
// and download causes; anything else is BOB-E-9999 (a bob bug), and log-format drift with only
// unknown errors is BOB-E-9998.
package translate
