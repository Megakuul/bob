#!/usr/bin/env bash
# Backend B measurements. Run on a quiet machine:  spikes/gen/b/measure.sh [--keep-fetches]
#
# "cold"  = every output of a derivation bob generated for the target is deleted from the store
#           (incl. downloaded crates/modules unless --keep-fetches); nixpkgs toolchains stay in
#           the store; the bob nixpkgs index is warm.
# "no-op" = same build again, nothing changed.
# "incr"  = one comment line appended to libfoo/src/foo.cpp, then rebuild.
# Profiles: [8c/15G] = whole host; [2c/4G] = systemd scope AllowedCPUs=0-1 MemoryMax=4G with
#           Nix max-jobs=2 cores=2 (single-user Nix, so builders run inside the scope).
# Peak RSS = GNU time %M: the largest single process (bob-b or a waited-for child).
set -euo pipefail
keep_fetches=0
[ "${1:-}" = "--keep-fetches" ] && keep_fetches=1

here=$(cd "$(dirname "$0")" && pwd)
gen=$(cd "$here/.." && pwd)
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-b}
cd "$gen"
CGO_ENABLED=0 go build -o b/bob-b ./b/cmd/bob-b
bob="$gen/b/bob-b"
gtime=$(nix build --no-link --print-out-paths nixpkgs#time)/bin/time
work=$(mktemp -d /tmp/bob-b-measure.XXXXXX)
trap 'rm -rf "$work"' EXIT
"$bob" index >/dev/null

declare -A R
measure() { # key cmd... ; records "wall s / rss MB"
	local key=$1; shift
	local f="$work/t.$$"
	if "$gtime" -f '%e %M' -o "$f" "$@" >"$work/out.log" 2>&1; then
		read -r wall rss <"$f"
		R[$key]="$wall s / $((rss / 1024)) MB"
	else
		R[$key]="FAILED"
		tail -5 "$work/out.log" >&2
	fi
}

clean() { # delete outputs of bob-generated derivations for graph $1 target $2
	local paths
	paths=$("$bob" outs -graph "$1" ${2:+-target "$2"} | awk -v k=$keep_fetches '!(k==1 && $1=="fetch"){print $2}')
	# shellcheck disable=SC2086
	nix-store --delete $paths >/dev/null 2>&1 || true
}

run_profile() { # name prefix...
	local name=$1; shift
	local -a pre=("$@")
	rm -rf "$work/root-$name"; mkdir -p "$work/root-$name"
	cp -r "$gen/fixture" "$work/root-$name/fixture"
	jq --arg r "$work/root-$name" '.root=$r' "$gen/fixture.graph.json" >"$work/g-$name.json"
	local g="$work/g-$name.json"

	clean "$g" //api:api
	measure "$name:fixture cold" "${pre[@]}" "$bob" build -graph "$g" -target //api:api
	measure "$name:fixture no-op" "${pre[@]}" "$bob" build -graph "$g" -target //api:api
	echo "// incr $(date +%s%N)" >>"$work/root-$name/fixture/libfoo/src/foo.cpp"
	measure "$name:fixture incr (foo.cpp)" "${pre[@]}" "$bob" build -graph "$g" -target //api:api

	for n in 10 100 1000 10000; do
		local d="$work/syn-$name-$n"
		CGO_ENABLED=0 go run ./cmd/graph -synthetic "$n" -dir "$d" -o "$d/graph.json" >/dev/null
		find "$d" -name '*.c' -exec sh -c 'echo "/* salt $1 */" >> "$2"' _ "$name-$RANDOM" {} \;
		measure "$name:graph→drvs N=$n (new)" "${pre[@]}" "$bob" drvs -graph "$d/graph.json"
		measure "$name:graph→drvs N=$n (again)" "${pre[@]}" "$bob" drvs -graph "$d/graph.json"
	done
	measure "$name:synthetic N=100 cold build" "${pre[@]}" "$bob" build -graph "$work/syn-$name-100/graph.json"
	measure "$name:synthetic N=100 no-op" "${pre[@]}" "$bob" build -graph "$work/syn-$name-100/graph.json"
}

run_profile full
run_profile small systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G \
	env NIX_CONFIG=$'max-jobs = 2\ncores = 2'

# index cost (separate): cold = no cached index file (nixpkgs source already in the store)
mv "$XDG_CACHE_HOME/bob-b" "$work/idx-backup"
measure "full:index cold" "$bob" index
measure "full:index warm" "$bob" index
rm -rf "$XDG_CACHE_HOME/bob-b"; mv "$work/idx-backup" "$XDG_CACHE_HOME/bob-b"

{
	echo "| Measurement (backend B) | [8c/15G] | [2c/4G] |"
	echo "|---|---|---|"
	for k in "fixture cold" "fixture no-op" "fixture incr (foo.cpp)" \
		"graph→drvs N=10 (new)" "graph→drvs N=10 (again)" "graph→drvs N=100 (new)" "graph→drvs N=100 (again)" \
		"graph→drvs N=1000 (new)" "graph→drvs N=1000 (again)" "graph→drvs N=10000 (new)" "graph→drvs N=10000 (again)" \
		"synthetic N=100 cold build" "synthetic N=100 no-op"; do
		echo "| $k | ${R[full:$k]:-} | ${R[small:$k]:-} |"
	done
	echo "| index cold / warm | ${R[full:index cold]:-} / ${R[full:index warm]:-} | |"
	echo
	echo "Run $(date -u +%FT%TZ) on $(hostname), keep-fetches=$keep_fetches. Cells: wall time / peak RSS."
} | tee "$here/measure-results.md"
