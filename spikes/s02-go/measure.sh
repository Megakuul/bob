#!/usr/bin/env bash
# Spike S2 (Go granularity) measurements. Rerun on a quiet machine:
#   spikes/s02-go/measure.sh [full|small|both]      (default both)
#
# Modes (bob-s02 -go):
#   single    = backend B as in S0a: one derivation per Go binary, `go build` with an empty cache
#   module    = one derivation per Go module (direct `go tool compile`/`link`, archives as outputs)
#   hybrid    = third-party per module, workspace per package
#   package   = one derivation per Go package (direct compile)
#   cacheprog = one derivation per package running `go build` with GOCACHEPROG on store layers
#   cutoff    = hybrid + -linkobj; dependents compile against export data added to the store by
#               content, built level by level (early cutoff)
#
# "cold"   = every Go derivation output of the target is deleted (kind "go" in `bob-s02 outs`);
#            std library archives, C/C++/Rust deps, downloads and unpacked modules stay.
# "std"    = the std-library derivation alone (once per toolchain + cgo setting; then cached).
# "no-op"  = same build again.  "incr X" = a comment line appended to file X, then rebuild.
# Cells: wall s / peak RSS MB (largest single process, GNU time %M) / derivations built.
# Profiles: [8c/15G] host defaults (max-jobs=8, cores=all);
#           [2c/4G] systemd scope AllowedCPUs=0-1 MemoryMax=4G, Nix max-jobs=2 cores=2.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
cd "$here"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s02}
unset NIX_CONFIG
CGO_ENABLED=0 go build -o bob-s02 ./cmd/bob-s02
CGO_ENABLED=0 go build -o tmp/graph ./cmd/graph
bob="$here/bob-s02"
[ -x .tools/time/bin/time ] || nix build -o .tools/time github:NixOS/nixpkgs/cd4ea8d57de16f4810bc5f14207bac45fbf19f67#time
gtime="$here/.tools/time/bin/time"
work="$here/tmp/measure"
rm -rf "$work"; mkdir -p "$work"
which=${1:-both}
out="$here/measure-results-$which.md"
: >"$work/results.tsv"

measure() { # key cmd...
	local key=$1; shift
	local f="$work/t"
	if "$gtime" -f '%e %M' -o "$f" "$@" -timing >"$work/out.log" 2>"$work/err.log"; then
		read -r wall rss <"$f"
		local nb
		nb=$(sed -n 's/.*to-build=\([0-9]*\).*/\1/p' "$work/err.log" | tail -1)
		printf '%s\t%s s / %d MB / %s\n' "$key" "$wall" $((rss / 1024)) "${nb:-?}" | tee -a "$work/results.tsv"
	else
		printf '%s\tFAILED\n' "$key" | tee -a "$work/results.tsv"
		tail -5 "$work/err.log" >&2
	fi
}

clean() { # graph target mode kinds-regex
	local paths
	paths=$("$bob" outs -graph "$1" -target "$2" -go "$3" | awk -v re="$4" '$1 ~ re {print $2}')
	# shellcheck disable=SC2086
	[ -z "$paths" ] && return 0
	nix-store --delete $paths >/dev/null 2>&1 && return 0
	for p in $paths; do nix-store --delete "$p" >/dev/null 2>&1 || true; done
}

ndrv() { "$bob" outs -graph "$1" -target "$2" -go "$3" | awk -v re="$4" '$1 ~ re' | wc -l; }

touchf() { echo "// incr $(date +%s%N)" >>"$1"; }

run_profile() { # name prefix...
	local name=$1; shift
	local -a pre=("$@")
	local r="$work/root-$name"
	mkdir -p "$r"; cp -r "$here/fixture" "$r/fixture"; cp "$here/lock.json" "$r/"
	jq --arg r "$r" '.root=$r' "$here/fixture.graph.json" >"$r/graph.json"
	local g="$r/graph.json" t=//api:api
	# std once (package mode), counted separately
	clean "$g" $t package '^(go|std)$'
	measure "$name|fixture|package|cold incl. std" "${pre[@]}" "$bob" build -graph "$g" -target $t -go package
	clean "$g" $t cacheprog '^(go|std)$'
	measure "$name|fixture|cacheprog|cold incl. std" "${pre[@]}" "$bob" build -graph "$g" -target $t -go cacheprog
	for m in single module hybrid package cacheprog cutoff; do
		printf '%s\t%s\n' "$name|fixture|$m|derivations (go / all)" "$(ndrv "$g" $t $m '^go$') / $(ndrv "$g" $t $m '.')" >>"$work/results.tsv"
		clean "$g" $t $m '^go$'
		measure "$name|fixture|$m|cold" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
		measure "$name|fixture|$m|no-op" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
		touchf "$r/fixture/api/cmd/api/main.go"
		measure "$name|fixture|$m|incr main.go" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
		touchf "$r/fixture/api/internal/msg/msg.go"
		measure "$name|fixture|$m|incr internal/msg" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
	done
	local s="$work/syn-$name"
	"$here/tmp/graph" -gosyn 200 -width 20 -dir "$s" -o "$s/graph.json" >/dev/null
	find "$s" -name '*.go' -exec sh -c 'echo "// salt $1" >> "$2"' _ "$name-$RANDOM" {} \;
	g="$s/graph.json"; t=//syn:syn
	for m in single module package cacheprog cutoff; do
		printf '%s\t%s\n' "$name|syn200|$m|derivations (go / all)" "$(ndrv "$g" $t $m '^go$') / $(ndrv "$g" $t $m '.')" >>"$work/results.tsv"
		clean "$g" $t $m '^go$'
		measure "$name|syn200|$m|cold" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
		measure "$name|syn200|$m|no-op" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
		touchf "$s/syn/cmd/syn/main.go"
		measure "$name|syn200|$m|incr main" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
		touchf "$s/syn/lib/p199/p199.go"
		measure "$name|syn200|$m|incr top layer (1 dependent)" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
		touchf "$s/syn/lib/p100/p100.go"
		measure "$name|syn200|$m|incr middle layer" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
		touchf "$s/syn/lib/p000/p000.go"
		measure "$name|syn200|$m|incr bottom layer" "${pre[@]}" "$bob" build -graph "$g" -target $t -go $m
	done
	# planning + derivation computation only (R-NFR-001), package mode
	for n in 200 1000; do
		local d="$work/eval-$name-$n"
		"$here/tmp/graph" -gosyn $n -width 20 -dir "$d" -o "$d/graph.json" >/dev/null
		find "$d" -name '*.go' -exec sh -c 'echo "// salt $1" >> "$2"' _ "$name-$RANDOM" {} \;
		measure "$name|eval|package|graph→drvs N=$n (new)" "${pre[@]}" "$bob" drvs -graph "$d/graph.json" -target //syn:syn -go package
		measure "$name|eval|package|graph→drvs N=$n (again)" "${pre[@]}" "$bob" drvs -graph "$d/graph.json" -target //syn:syn -go package
	done
}

case $which in
full | both) run_profile full ;;
esac
case $which in
small | both) run_profile small systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G \
	env NIX_CONFIG=$'max-jobs = 2\ncores = 2' ;;
esac

{
	echo "| Workload | Mode | Measurement | [8c/15G] | [2c/4G] |"
	echo "|---|---|---|---|---|"
	awk -F'\t' '{split($1,k,"|"); key=k[2]"|"k[3]"|"k[4]; if(!(key in seen)){seen[key]=1; order[++n]=key} v[k[1]"|"key]=$2}
		END{for(i=1;i<=n;i++){split(order[i],k,"|"); printf "| %s | %s | %s | %s | %s |\n", k[1],k[2],k[3], v["full|"order[i]], v["small|"order[i]]}}' "$work/results.tsv"
	echo
	echo "Run $(date -u +%FT%TZ) on $(hostname). Cells: wall / peak RSS / derivations built."
} | tee "$out"
