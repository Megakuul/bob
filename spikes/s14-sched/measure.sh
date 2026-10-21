#!/usr/bin/env bash
# S14: rerun on a quiet machine. Real run: 4 local workers (chroot stores) + HTTP cache on :21400.
set -euo pipefail
cd "$(dirname "$0")"
export XDG_CACHE_HOME=/root/.cache-s14
PIN=nixpkgs/cd4ea8d57de16f4810bc5f14207bac45fbf19f67
BB=$(nix build --no-link --print-out-paths "$PIN#pkgsStatic.busybox")
CGO_ENABLED=0 go build -o tmp/real ./cmd/real
CGO_ENABLED=0 go build -o tmp/sim ./cmd/sim
for m in a b c; do ./tmp/real -mode $m -graph layer -work 0.5 -workers 4 -slots 2 -inc -bb "$BB"; done | tee tmp/real-layer.txt
for m in a b c; do ./tmp/real -mode $m -graph chain -workers 1 -bb "$BB"; done | tee tmp/real-chain.txt
for s in cold push inc death fair dup chunks steady; do ./tmp/sim -scen $s > tmp/sim-$s.txt; done
./tmp/sim -scen push -slots 8 > tmp/sim-push-8slots.txt
rm -rf /root/scratch-s14/run
