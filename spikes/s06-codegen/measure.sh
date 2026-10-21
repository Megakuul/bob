#!/usr/bin/env bash
# Spike S6 measurements (codegen as derivations + materialisation). Rerun on a quiet machine:
#   spikes/s06-codegen/measure.sh
#
# Each profile works on a fresh copy of fixture/ (tmp/m-<profile>).
# "cold"  = outputs of every bob derivation of the graph deleted except downloads (Go module
#           files, npm tarballs); nixpkgs tools stay in the store; nixpkgs index warm.
# Profiles: [8c/15G] = whole host, Nix max-jobs=auto cores=0;
#           [2c/4G]  = systemd scope AllowedCPUs=0-1 MemoryMax=4G, Nix max-jobs=2 cores=2.
# Cells: wall seconds / peak RSS of the largest single process (GNU time %M).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
cd "$here"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s06}
export BOB_TMP="$here/tmp"
mkdir -p "$BOB_TMP"
CGO_ENABLED=0 .tools/go/bin/go build -o bob-s06 ./cmd/bob-s06
bob="$here/bob-s06"
gtime=$(nix build --no-link --print-out-paths github:NixOS/nixpkgs/cd4ea8d57de16f4810bc5f14207bac45fbf19f67#time)/bin/time
work="$here/tmp/measure"
command rm -rf "$work"; mkdir -p "$work"
declare -A R B

measure() { # key cmd...
	local key=$1; shift
	if "$gtime" -f '%e %M' -o "$work/t" "$@" >"$work/out.log" 2>"$work/err.log"; then
		read -r wall rss <"$work/t"
		R[$key]="$wall s / $((rss / 1024)) MB"
	else
		R[$key]="FAILED"
		tail -5 "$work/err.log" >&2
	fi
	B[$key]=$(grep -o '^built [0-9]*: .*' "$work/err.log" | sed 's|//third_party/[^ ]*||g; s/  */ /g' | tr '\n' ' ' || true)
	[ -n "${B[$key]}" ] || B[$key]="nothing built"
}

clean() { # delete bob outputs of the graph (keeps fetches)
	local paths
	paths=$("$bob" outs -root "$1" -kind all | awk '{print $3}')
	# shellcheck disable=SC2086
	nix-store --delete $paths >/dev/null 2>&1 || true
}

edit() { echo "$2" >>"$1"; }

dev_latency() { # profile root prefix...
	local name=$1 root=$2; shift 2
	local log="$work/dev-$name.log"
	"$@" "$bob" dev -root "$root" >"$log" 2>&1 &
	local pid=$!
	until grep -q '\[dev\] watching' "$log"; do sleep 0.1; done
	local f="$root/svc/gen/proto/greet/v1/greet.pb.go" t="$root/web/gen/proto/greet/v1/greet_pb.ts"
	local h1 h2; h1=$(sha256sum "$f" | cut -c1-64); h2=$(sha256sum "$t" | cut -c1-64)
	sed -i 's|^  string name = 1;|  string name = 1; // dev edit '"$RANDOM"'|' "$root/proto/greet/v1/greet.proto"
	local s; s=$(date +%s.%N)
	until [ "$(sha256sum "$f" | cut -c1-64)" != "$h1" ] && [ "$(sha256sum "$t" | cut -c1-64)" != "$h2" ]; do sleep 0.02; done
	local e; e=$(date +%s.%N)
	R["$name:dev: greet.proto edit → Go+TS files updated in tree"]="$(awk -v s="$s" -v e="$e" 'BEGIN{printf "%.2f", e-s}') s"
	B["$name:dev: greet.proto edit → Go+TS files updated in tree"]=$(grep -o 'built [0-9]*: .*' "$log" | tail -1)
	kill $pid; wait $pid 2>/dev/null || true
}

run_profile() { # name prefix...
	local name=$1; shift
	local -a pre=("$@")
	local root="$work/m-$name"
	command rm -rf "$root"; cp -r "$here/fixture" "$root"; command rm -rf "$root/.bob" "$root/web/node_modules"
	find "$root" -path '*/gen' -prune -exec chmod -R u+w {} \; -exec rm -rf {} \;

	clean "$root"
	measure "$name:gen cold (all codegen)" "${pre[@]}" "$bob" gen -root "$root"
	measure "$name:gen no-op" "${pre[@]}" "$bob" gen -root "$root"
	edit "$root/proto/billing/v1/billing.proto" "// m $RANDOM"
	measure "$name:gen after billing.proto change" "${pre[@]}" "$bob" gen -root "$root"
	edit "$root/proto/common/v1/common.proto" "// m $RANDOM"
	measure "$name:gen after common.proto change" "${pre[@]}" "$bob" gen -root "$root"
	edit "$root/schema/schema.graphqls" "# m $RANDOM"
	measure "$name:gen after schema.graphqls change" "${pre[@]}" "$bob" gen -root "$root"

	clean "$root"
	measure "$name:build cold (codegen + Go + TS)" "${pre[@]}" "$bob" build -root "$root"
	measure "$name:build no-op" "${pre[@]}" "$bob" build -root "$root"
	edit "$root/proto/billing/v1/billing.proto" "// m $RANDOM"
	measure "$name:build after billing.proto change" "${pre[@]}" "$bob" build -root "$root"
	edit "$root/proto/greet/v1/greet.proto" "// m $RANDOM"
	measure "$name:build after greet.proto change" "${pre[@]}" "$bob" build -root "$root"
	# whitespace-only change: generated code is byte-identical
	edit "$root/proto/greet/v1/greet.proto" ""
	measure "$name:build after whitespace-only greet.proto change" "${pre[@]}" "$bob" build -root "$root"
	measure "$name:build -cutoff (prime)" "${pre[@]}" "$bob" build -cutoff -root "$root"
	edit "$root/proto/greet/v1/greet.proto" ""
	measure "$name:build -cutoff after whitespace-only change" "${pre[@]}" "$bob" build -cutoff -root "$root"
	measure "$name:status (no build)" "${pre[@]}" "$bob" status -root "$root"

	dev_latency "$name" "$root" "${pre[@]}"
}

run_profile full env NIX_CONFIG=$'max-jobs = auto\ncores = 0'
run_profile small systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G \
	env NIX_CONFIG=$'max-jobs = 2\ncores = 2'

{
	echo "| Measurement | [8c/15G] | [2c/4G] | rebuilt ([8c/15G] run) |"
	echo "|---|---|---|---|"
	for k in "gen cold (all codegen)" "gen no-op" "gen after billing.proto change" "gen after common.proto change" \
		"gen after schema.graphqls change" "build cold (codegen + Go + TS)" "build no-op" \
		"build after billing.proto change" "build after greet.proto change" \
		"build after whitespace-only greet.proto change" "build -cutoff (prime)" \
		"build -cutoff after whitespace-only change" "status (no build)" \
		"dev: greet.proto edit → Go+TS files updated in tree"; do
		echo "| $k | ${R[full:$k]:-} | ${R[small:$k]:-} | ${B[full:$k]:-} |"
	done
	echo
	echo "Run $(date -u +%FT%TZ) on $(hostname). Downloads kept in 'cold'."
} | tee "$here/measure-results.md"
