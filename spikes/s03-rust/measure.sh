#!/usr/bin/env bash
# Spike S3 measurements. Rerun on a quiet machine:  spikes/s03-rust/measure.sh
#
# demo graph = ws/demo target demo: 49 units / 120 derivations (45 crates.io crates incl.
#   serde+derive, clap+derive, tokio+macros, regex, zstd-sys (C via cc), 13 build scripts,
#   4 proc-macros, a local proc-macro and a local lib with build.rs).
# cold   = every non-fetch output bob generated for the target deleted; sources (fixed-output)
#          and nixpkgs toolchains stay in the store.
# no-op  = same build again.
# incr-leaf = comment appended to demo/src/main.rs (1 drv rebuilds).
# incr-lib  = comment appended to corelib/src/lib.rs (corelib + demo rebuild; build-script
#             compile and run drvs of corelib also rebuild because the source is one input).
# chain  = ws/chain: 30 tiny local crates in a chain, max-jobs=1 -> per-derivation cost.
# cargo  = `cargo build --release` on the generated Cargo.toml + vendor (target/ removed).
# Profiles: [8c/15G] max-jobs=8 cores=8 (cargo -j8); [2c/4G] systemd scope AllowedCPUs=0-1
#           MemoryMax=4G, max-jobs=2 cores=2 (cargo -j2).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
cd "$here"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s03} BOB_TMP=$here/tmp
./build.sh
bob=$here/tmp/bob-rs
gtime=$here/.tools/time/bin/time
work=$(mktemp -d "$here/tmp/measure.XXXXXX")
trap 'rm -rf "$work"' EXIT
declare -A R
measure() { local key=$1; shift
	if "$gtime" -f '%e %M' -o "$work/t" "$@" >"$work/out.log" 2>&1; then
		read -r wall rss <"$work/t"; R[$key]="$wall s / $((rss / 1024)) MB"
	else R[$key]=FAILED; tail -5 "$work/out.log" >&2; fi
}
clean() { # ws target
	local p; p=$("$bob" outs -ws "$1" -lock "$(dirname "$1")/bob.lock.json" -target "$2" | awk '$1=="build"{print $2}')
	# shellcheck disable=SC2086
	nix-store --delete $p >/dev/null 2>&1 || true
}
profile() { local name=$1 jobs=$2; shift 2; local -a pre=("$@")
	rm -rf "$work/demo-$name"; cp -r ws/demo "$work/demo-$name"; rm -rf "$work/demo-$name/target"
	jq --arg r "$work/demo-$name" '.root=$r' ws/demo/ws.json >"$work/demo-$name/ws.json"
	local ws=$work/demo-$name/ws.json lk=$work/demo-$name/bob.lock.json
	export NIX_CONFIG=$'max-jobs = '$jobs$'\ncores = '$jobs
	clean "$ws" demo
	measure "$name:demo cold" "${pre[@]}" "$bob" build -ws "$ws" -lock "$lk" -target demo
	measure "$name:demo no-op" "${pre[@]}" "$bob" build -ws "$ws" -lock "$lk" -target demo
	echo "// $(date +%s%N)" >>"$work/demo-$name/demo/src/main.rs"
	measure "$name:demo incr-leaf" "${pre[@]}" "$bob" build -ws "$ws" -lock "$lk" -target demo
	echo "// $(date +%s%N)" >>"$work/demo-$name/corelib/src/lib.rs"
	measure "$name:demo incr-lib" "${pre[@]}" "$bob" build -ws "$ws" -lock "$lk" -target demo
	# cargo baseline on the generated native files (offline, vendored)
	"$bob" cargo -ws "$ws" -lock "$lk" >/dev/null 2>&1
	(cd "$work/demo-$name" && rm -rf target && CARGO_HOME=$work/cargo-home PATH=$here/.tools/cargo/bin:$here/.tools/rustc/bin:$here/.tools/cc/bin:$PATH \
		"$gtime" -f '%e %M' -o "$work/t" "${pre[@]}" cargo build --release -q -j "$jobs" -p demo >"$work/out.log" 2>&1) \
		&& { read -r wall rss <"$work/t"; R[$name:cargo cold]="$wall s / $((rss / 1024)) MB"; } || R[$name:cargo cold]="FAILED"
	rm -rf "$work/chain-$name"; cp -r ws/chain "$work/chain-$name"
	jq --arg r "$work/chain-$name" '.root=$r' ws/chain/ws.json >"$work/chain-$name/ws.json"
	export NIX_CONFIG=$'max-jobs = 1\ncores = 1'
	clean "$work/chain-$name/ws.json" c29
	measure "$name:chain30 cold (max-jobs 1)" "${pre[@]}" "$bob" build -ws "$work/chain-$name/ws.json" -lock "$work/chain-$name/bob.lock.json" -target c29
}
profile full 8
profile small 2 systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G
export NIX_CONFIG=$'max-jobs = 2\ncores = 2'
measure "full:plan+drvs demo (no build)" "$bob" drvs -ws ws/demo/ws.json -lock ws/demo/bob.lock.json -target demo
measure "full:lock big (392 crates, offline index)" "$bob" lock -ws ws/resolve/big/ws.json -no-meta -offline
{
	echo "| Measurement | [8c/15G] | [2c/4G] |"; echo "|---|---|---|"
	for k in "demo cold" "demo no-op" "demo incr-leaf" "demo incr-lib" "cargo cold" "chain30 cold (max-jobs 1)"; do
		echo "| $k | ${R[full:$k]:-} | ${R[small:$k]:-} |"; done
	echo "| plan+drvs demo (no build) | ${R[full:plan+drvs demo (no build)]} | |"
	echo "| lock big (392 crates, offline index) | ${R[full:lock big (392 crates, offline index)]} | |"
	echo; echo "Run $(date -u +%FT%TZ) on $(hostname). Cells: wall time / peak RSS of the largest process."
} | tee "$here/measure-results.md"
