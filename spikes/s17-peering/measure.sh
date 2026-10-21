#!/usr/bin/env bash
# Reruns all S17 measurements: starts RustFS on 127.0.0.1:21750, runs the scenarios (each
# starts 3 orchestrator processes on 21701-21703), writes results/*.txt, stops RustFS.
# Latency and CPU numbers depend on machine load: rerun on a quiet host.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd); cd "$here"
export XDG_CACHE_HOME=/root/.cache-s17 CGO_ENABLED=0 GOFLAGS=-mod=mod
[ -x ../s09-dynamitedb/.tools/rustfs ] || { echo "run spikes/s09-dynamitedb/measure.sh once to fetch RustFS 1.0.3"; exit 1; }
mkdir -p tmp results
go build -o tmp/s17 ./cmd/s17
rm -rf tmp/rustfs
./start-rustfs.sh tmp/rustfs > tmp/rustfs.log 2>&1 & rf=$!
trap 'kill $rf 2>/dev/null || true; wait 2>/dev/null || true' EXIT
until curl -s -o /dev/null 127.0.0.1:21750; do sleep 1; done; sleep 3
./tmp/s17 bench -scenario sec > results/sec.txt 2>&1
: > results/kill.txt
for m in peer pushall lakeonly; do for sc in kill killclient; do
  ./tmp/s17 bench -scenario $sc -mode $m -dur 30s -rate 100 >> results/kill.txt 2>&1
done; done
: > results/retry.txt
for m in peer pushall lakeonly; do ./tmp/s17 bench -scenario retry -mode $m -rate 100 >> results/retry.txt 2>&1; done
: > results/fanout.txt
for m in peer pushall lakeonly lakeprivate; do
  ./tmp/s17 bench -scenario fanout -mode $m -dur 60s -builds 20 -followers 50 -rate 200 >> results/fanout.txt 2>&1
done
: > results/fanout-2c4g.txt
for m in peer pushall; do
  systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G -- ./tmp/s17 bench -scenario fanout -mode $m -dur 60s -builds 20 -followers 50 -rate 200 >> results/fanout-2c4g.txt 2>&1
done
