#!/usr/bin/env bash
# bootstrap.sh — build bob with bob from a clean checkout (SPEC R-ARCH-005).
#
#   scripts/bootstrap.sh
#
# Needs Go (at least the go directive of go.mod), curl and zstd. Steps:
#   1. scripts/fetch-nix.sh: the pinned static Nix that bob embeds (R-NIX-020).
#   2. go generate ./pkg/engine/store: bob-builder for both systems (R-NIX-015). The table it
#      writes must equal the committed pkg/engine/store/builders.go: the builder is
#      bit-reproducible, so a different store path means a stale table or a broken build.
#   3. go build ./cmd/bob: the first bob, embedding that builder and that Nix.
#   4. The first bob runs `bob build //cmd/bob:bob` with bob's own bob.go.
# Progress goes to stderr; the last line on stdout is the path of the bob that bob built.
# BOB_HOME selects bob's state directory as usual.
set -euo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
cd "$here"
for tool in go curl zstd; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "bootstrap.sh: $tool is required" >&2
		exit 2
	}
done
# The flags of the builds below are fixed here, not taken from the caller's environment.
export CGO_ENABLED=0 GOFLAGS= GOWORK=off

work=.bob/bootstrap
mkdir -p "$work"

echo "bootstrap.sh: fetching the static Nix" >&2
sh scripts/fetch-nix.sh >&2

echo "bootstrap.sh: building bob-builder (go generate ./pkg/engine/store)" >&2
cp pkg/engine/store/builders.go "$work/builders.go.committed"
go generate ./pkg/engine/store >&2
if ! cmp -s pkg/engine/store/builders.go "$work/builders.go.committed"; then
	echo "bootstrap.sh: the bob-builder built here is not the one pkg/engine/store/builders.go records:" >&2
	diff "$work/builders.go.committed" pkg/engine/store/builders.go >&2 || true
	exit 1
fi

echo "bootstrap.sh: building the first bob with go" >&2
go build -trimpath -o "$work/bob" ./cmd/bob

echo "bootstrap.sh: bob builds //cmd/bob:bob" >&2
if ! "$work/bob" build //cmd/bob:bob --print-paths >"$work/build.out"; then
	cat "$work/build.out" >&2
	exit 1
fi
out=$(sed -n 's|^//cmd/bob:bob \(/.*\)$|\1|p' "$work/build.out" | tail -n 1)
if [ -z "$out" ] || [ ! -x "$out/bin/bob" ]; then
	cat "$work/build.out" >&2
	echo "bootstrap.sh: bob build //cmd/bob:bob printed no output path" >&2
	exit 1
fi
cat "$work/build.out" >&2
echo "$out/bin/bob"
