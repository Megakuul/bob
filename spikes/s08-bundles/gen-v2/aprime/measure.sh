#!/usr/bin/env bash
# S0a A′ measurements. Prints a markdown table to stdout (progress to stderr).
#
#   ./measure.sh            full host  [8c/15G]
#   ./measure.sh --lowpower same, inside systemd-run --scope AllowedCPUs=0-1 MemoryMax=4G, nix -j2 --cores 2  [2c/4G]
#   ./measure.sh --both     both, one table
#
# Definitions
#   cold build  : every derivation built from the repository's own sources (libfoo, rcore, proto stubs, api) is
#                 NEW (fresh fixture copy with a unique run id appended to every source file). Third-party work that
#                 does not depend on repo sources (downloads, unpacking, crates itoa/hex, the Go module proxy) and
#                 all toolchains are already in the store. Network is therefore not measured.
#   no-op       : same command again, nothing changed.
#   incremental : one comment line appended to libfoo/src/foo.cpp, rebuild //api:api (rebuilds libfoo + api).
#   eval        : graph JSON -> all .drv files instantiated (`allDrvPaths`), no build. cold = fresh sources
#                 (run id appended to every synthetic .c), warm = same again (drvs already in the store).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
gen=$(cd "$here/.." && pwd)
lib=$here/lib
work=${WORK:-/root/.cache-aprime/measure}
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-aprime}
T=$(command -v time | grep -v builtin || true); [ -x /root/.nix-profile/bin/time ] && T=/root/.nix-profile/bin/time
mode=${1:-}

(cd "$gen" && CGO_ENABLED=0 go build -o "$here/bob-aprime" ./aprime/cmd/bob-aprime)
bob="$here/bob-aprime"

# run "<label>" cmd... -> prints "| profile | label | wall s | peak RSS MiB |"
measure() {
  local profile=$1 label=$2; shift 2
  local prefix=() nixopts=()
  if [ "$profile" = "2c/4G" ]; then
    prefix=(systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G)
    export NIX_CONFIG=$'max-jobs = 2\ncores = 2'
  else
    export NIX_CONFIG=$'max-jobs = auto\ncores = 0'
  fi
  local out
  out=$("${prefix[@]}" "$T" -f '%e %M' "$@" 2>&1 >/dev/null | tail -1)
  printf '| %s | %s | %s | %s |\n' "$profile" "$label" "${out% *}" "$(( ${out#* } / 1024 ))"
}

one_profile() {
  local profile=$1 id
  id=$(date +%s%N)
  rm -rf "$work/root"; mkdir -p "$work/root"
  cp -r "$gen/fixture" "$work/root/fixture"
  find "$work/root/fixture" -type f \( -name '*.cpp' -o -name '*.h' -o -name '*.rs' -o -name '*.go' -o -name '*.proto' \) \
    -exec sh -c 'echo "// run $0" >> "$1"' "$id" {} \;
  jq --arg r "$work/root" '.root=$r' "$gen/fixture.graph.json" > "$work/fixture.json"
  echo "[$profile] fixture builds" >&2
  measure "$profile" "fixture cold build //api:api" "$bob" build -graph "$work/fixture.json" -target //api:api -lib "$lib"
  measure "$profile" "fixture no-op build" "$bob" build -graph "$work/fixture.json" -target //api:api -lib "$lib"
  echo "// incremental $id" >> "$work/root/fixture/libfoo/src/foo.cpp"
  measure "$profile" "fixture incremental (foo.cpp)" "$bob" build -graph "$work/fixture.json" -target //api:api -lib "$lib"

  for n in 10 100 1000 10000; do
    echo "[$profile] eval n=$n" >&2
    local d="$work/syn$n"
    rm -rf "$d"
    (cd "$gen" && CGO_ENABLED=0 go run ./cmd/graph -synthetic "$n" -dir "$d" -o "$d/graph.json")
    find "$d" -name '*.c' -exec sh -c 'echo "// run $0" >> "$1"' "$id" {} \;
    measure "$profile" "eval n=$n cold" nix eval --raw -f "$lib/default.nix" --argstr graphFile "$d/graph.json" allDrvPaths
    measure "$profile" "eval n=$n warm" nix eval --raw -f "$lib/default.nix" --argstr graphFile "$d/graph.json" allDrvPaths
  done
  echo "[$profile] synthetic n=100 build" >&2
  measure "$profile" "synthetic n=100 cold build (serial chain)" "$bob" build -graph "$work/syn100/graph.json" -target //s00099:s00099 -lib "$lib"
  measure "$profile" "synthetic n=100 no-op build" "$bob" build -graph "$work/syn100/graph.json" -target //s00099:s00099 -lib "$lib"
}

echo "| profile | measurement | wall s | peak RSS MiB |"
echo "|---|---|---|---|"
case "$mode" in
  --lowpower) one_profile "2c/4G" ;;
  --both) one_profile "8c/15G"; one_profile "2c/4G" ;;
  *) one_profile "8c/15G" ;;
esac
