#!/bin/sh
# ci-changes.sh — what a push or pull request changed, for .github/workflows/ci.yml.
#
#   scripts/ci-changes.sh [<base-sha>]
#
# Prints GITHUB_OUTPUT lines on stdout:
#   base=<sha>        the base when it is usable, else empty (for BOB_DOCS_BASE)
#   code=true|false   Go, build or workflow files changed: the build and test jobs run
#   stages=<list>     the tools/ci stages to run (docs always; the rest by what changed)
# Without a usable base (a new branch, a force push, a manual run) everything runs. The
# change is HEAD against the base, so a push of several commits is judged as a whole.
set -eu

base=${1:-}
case "$base" in
*[!0]*) ;;
*) base= ;; # empty or all zeros
esac
if [ -n "$base" ] && ! git cat-file -e "$base^{commit}" 2>/dev/null; then
	base=
fi

all="docs fmt vet lint layering licence builder golden workflows web website"
if [ -z "$base" ]; then
	echo "base="
	echo "code=true"
	echo "stages=$(echo "$all" | tr ' ' ',')"
	exit 0
fi

files=$(git diff --name-only "$base" HEAD)
has() { printf '%s\n' "$files" | grep -Eq "$1"; }

code=false
stages=docs
if has '^(cmd|internal|pkg|proto|tests|tools|schema|scripts)/|^(go\.mod|go\.sum|go\.work|bob\.go|bob\.lock|flake\.nix|flake\.lock)$|^\.github/'; then
	code=true
	stages="$stages fmt vet lint layering licence builder golden workflows"
fi
if has '^web/'; then
	stages="$stages web"
fi
if has '^website/'; then
	stages="$stages website"
fi
echo "base=$base"
echo "code=$code"
echo "stages=$(echo "$stages" | tr ' ' ',')"
