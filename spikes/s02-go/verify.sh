#!/usr/bin/env bash
# Spike S2 correctness checks (run after measure.sh, not concurrently):
#  1. every Go derivation of the fixture (package, hybrid, module modes; plus std) rebuilds
#     bit-identically (`nix build --rebuild` fails if an output differs);
#  2. the api binary is byte-identical across module / hybrid / package granularity;
#  3. the binary runs under `env -i` and prints the expected line.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
cd "$here"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s02}
CGO_ENABLED=0 go build -o bob-s02 ./cmd/bob-s02
want="digest=81b637d8fcd2c6da6359e6963113a1170de795e4b725b84d1e0b4cfd9ec58ce9 numhex=3432 proto_bytes=116"
declare -A sum
for m in module hybrid package cacheprog; do
	out=$(./bob-s02 build -graph fixture.graph.json -target //api:api -go $m)
	got=$(env -i "$out/bin/api")
	[ "$got" = "$want" ] && echo "$m: output OK" || { echo "$m: WRONG OUTPUT: $got"; exit 1; }
	sum[$m]=$(sha256sum "$out/bin/api" | cut -c1-16)
	echo "$m: api sha256 ${sum[$m]}"
done
[ "${sum[module]}" = "${sum[hybrid]}" ] && [ "${sum[hybrid]}" = "${sum[package]}" ] &&
	echo "granularity does not change the binary: OK" || echo "binaries differ between granularities"
n=0 bad=0
paths=$(for m in package hybrid module cacheprog; do
	./bob-s02 outs -graph fixture.graph.json -target //api:api -go $m | awk '$1=="go"||$1=="std"{print $2}'
done | sort -u)
for p in $paths; do
	d=$(nix-store -qd "$p")
	n=$((n + 1))
	if ! nix build --no-link --rebuild "$d^out" >/dev/null 2>"tmp/rebuild.err"; then
		bad=$((bad + 1)); echo "NOT REPRODUCIBLE: $d"; tail -2 tmp/rebuild.err
	fi
done
echo "rebuild check: $n derivations, $bad differ"
