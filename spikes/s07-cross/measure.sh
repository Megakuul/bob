#!/usr/bin/env bash
# S7 measurements. Rerun on a quiet machine. Usage: ./measure.sh [subst] [builds]
#  subst  : cold substitution of the cross toolchains from cache.nixos.org into a fresh chroot store
#  builds : cold build (all bob outputs of the config deleted, nixpkgs inputs present) per config,
#           [8c/15G] and [2c/4G]
set -uo pipefail
cd "$(dirname "$0")"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s07}
export NIX_CONFIG=${NIX_CONFIG:-$'cores = 4\nmax-jobs = 2'}
what=${*:-subst builds}
ms() { echo $(( ($(date +%s%N) - $1) / 1000000 )); }
if [[ $what == *subst* ]]; then
  S=/root/scratch-s07/store; rm -rf $S
  for p in $(readlink -f .tools/xcc) $(readlink -f .tools/qemu) $(readlink -f .tools/ssl64-dev) $(readlink -f .tools/rustc64); do
    s=$(date +%s%N); nix build --store $S $p 2>/dev/null; echo "subst $(basename $p | cut -c34-): $(ms $s) ms"
  done
  du -sh $S | tail -1; rm -rf $S
fi
LP="systemd-run --quiet --scope -p AllowedCPUs=0-1 -p MemoryMax=4G --"
cold() { # $1 = profile label, rest = bob flags
  local prof=$1; shift
  local outs; outs=$(./bob-s07 outs "$@" -graph fixture.graph.json -target //api:api | awk '$1!="fetch" && $1!="unpack"{print $2}')
  nix-store --delete $outs >/dev/null 2>&1 || for p in $outs; do nix-store --delete "$p" >/dev/null 2>&1 || true; done
  local run=""; [ "$prof" = 2c4G ] && run=$LP
  local s; s=$(date +%s%N)
  local o; o=$($run ./bob-s07 build "$@" -graph fixture.graph.json -target //api:api | tail -1)
  echo "cold [$prof] api $*: $(ms $s) ms -> $o"
}
if [[ $what == *builds* ]]; then
  for prof in 8c15G 2c4G; do
    cold $prof
    cold $prof -platform linux-amd64
    cold $prof -platform linux-amd64 -sanitize address
    cold $prof -platform linux-amd64 -profile debug
  done
fi
