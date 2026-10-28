# CI1: GitHub pipelines fixed and fast (2026-10-27)

Owner: "either fix all ci pipelines or remove them, and ensure they are fast".

## Root causes found by reproducing every step in a clean `git clone`

1. **The builder differs per build host (A562).** nixpkgs' Go patches the store paths of tzdata and
   iana-etc into `time`/`net`; their hashes differ per host architecture. Building cmd/bob-builder
   with the pinned x86_64 Go under qemu-user gave 92 differing bytes against the aarch64 build, all
   store hashes. So `builders.go` (committed from an aarch64 host) could never match on an x86_64
   runner: the `builder` stage and `scripts/bootstrap.sh` (release) failed there. genbuilders now
   zeroes every store hash in the binary (BuilderVersion 19); after the scrub both hosts give
   byte-identical binaries for both targets.
2. **Cold module cache broke `fmt` and `vet`.** `go: downloading ...` lines on stderr were merged
   into the output that `buf format -d` and `go list` stages treat as findings. tools/ci drops them.
3. **`bob exec /bin/sh -c ...` in release.yml always failed** (`-c` is parsed as a bob flag); the
   `|| cp` fallback hid it. Now a plain cp (the output is a host path).
4. Every push ran six slow jobs, among them both architectures and `nix flake check`.

## Result

| workflow | when | what |
|---|---|---|
| ci.yml | push, PR | `checks`, `unit`, `build` in parallel; unchanged areas skipped |
| full.yml | nightly, manual | x86_64 + aarch64: flake check, flake-variant store tests, all static stages |
| release.yml | v* tag | AppImage per architecture, sha256, GitHub release |

Local measurements (aarch64 host, 4 cores via taskset, clean clone): static stages 56 s warm,
5 min cold; `nix build .#bob` 40 s; unit check 80 s on 8 cores; release bootstrap 81 s + AppImage 22 s.

## Not verified (needs GitHub or x86_64)

The workflows themselves running on GitHub; `nix build` on x86_64 (the derivation evaluates);
cache sizes of actions/cache; the AppImage on an Ubuntu runner (FUSE is not needed, ADR 0010).
