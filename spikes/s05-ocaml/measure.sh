#!/usr/bin/env bash
# S5 measurements (indicative while other spikes run). Usage: ./measure.sh [8c|2c]...
# cold  : all bob outputs of the demo graph deleted (sources + nixpkgs toolchain present)
# noop  : same command again
# incr-app / incr-lib : one-line change in app/main.ml / geom/show.ml
set -uo pipefail
cd "$(dirname "$0")"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s05} BOB_TMP=$PWD/tmp
profiles=${*:-8c 2c}
ms() { echo $(( ($(date +%s%N) - $1) / 1000000 )); }
W=ws/demo/ws.json; L=ws/demo/bob.lock
LP="systemd-run --quiet --scope -p AllowedCPUs=0-1 -p MemoryMax=4G --"
for prof in $profiles; do
  if [ $prof = 8c ]; then export NIX_CONFIG=$'cores = 0\nmax-jobs = 8'; run=""; else export NIX_CONFIG=$'cores = 2\nmax-jobs = 2'; run=$LP; fi
  for mode in direct dune; do
    \cp -f ws/demo/app/main.ml tmp/main.orig; \cp -f ws/demo/geom/show.ml tmp/show.orig
    for m in direct dune; do bin/bob-ml outs -mode $m $W $L demo 2>/dev/null; done | sort -u > tmp/outs.txt
    nix-store -q --referrers-closure $(cat tmp/outs.txt) 2>/dev/null | grep -E -- '-(opam|ml|c)-[^/]*$' | grep -v '\.drv$' | xargs -r nix-store --delete >/dev/null 2>&1
    for p in $(cat tmp/outs.txt); do [ -e $p ] && echo "WARN still present: $p"; done
    s=$(date +%s%N); $run bin/bob-ml build -mode $mode $W $L demo >/dev/null 2>tmp/err || cat tmp/err; echo "[$prof] $mode cold demo (11 opam + C + lib + exe): $(ms $s) ms"
    s=$(date +%s%N); $run bin/bob-ml build -mode $mode $W $L demo >/dev/null 2>&1; echo "[$prof] $mode no-op: $(ms $s) ms"
    echo "(* $(date +%s%N) *)" >> ws/demo/app/main.ml
    s=$(date +%s%N); $run bin/bob-ml build -mode $mode $W $L demo >/dev/null 2>&1; echo "[$prof] $mode incr-app: $(ms $s) ms"
    echo "(* $(date +%s%N) *)" >> ws/demo/geom/show.ml
    s=$(date +%s%N); $run bin/bob-ml build -mode $mode $W $L demo >/dev/null 2>&1; echo "[$prof] $mode incr-lib: $(ms $s) ms"
    \cp -f tmp/main.orig ws/demo/app/main.ml; \cp -f tmp/show.orig ws/demo/geom/show.ml
  done
  # baseline: in-tree dune (what an opam/dune user runs), deps from the store, own code only
  (cd ws/demo && rm -rf _build && s=$(date +%s%N) && $run .bob/bin/dune build --cache=disabled ./app/main.exe 2>&1 | tail -2 && echo "[$prof] in-tree dune cold (own code only): $(ms $s) ms" \
    && echo "(* x *)" >> geom/show.ml && s=$(date +%s%N) && $run .bob/bin/dune build --cache=disabled ./app/main.exe && echo "[$prof] in-tree dune incr-lib: $(ms $s) ms"; \cp -f ../../tmp/show.orig geom/show.ml)
done
