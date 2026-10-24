#!/usr/bin/env bash
# S10 measurements. Rerun on a quiet machine; all timings in the write-up depend on load.
# Needs: .tools/seaweedfs (nix build -o .tools/seaweedfs nixpkgs/<pin>#seaweedfs) and
#        .tools/dl-rustfs/rustfs (upstream 1.0.3 aarch64 musl zip).
# Usage: ./measure.sh [quick]
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
cd "$here"
mkdir -p tmp/out
export CGO_ENABLED=0
go build -o tmp/s10 ./cmd/s10
go build -tags lakepatched -modfile go.patched.mod -o tmp/s10p ./cmd/s10

./start-seaweed.sh tmp/seaweed-measure > tmp/out/seaweed.log 2>&1 &
sw=$!
RUSTFS_OBS_LOG_DIRECTORY=$here/tmp/rustfs-logs ./start-rustfs.sh tmp/rustfs-measure > tmp/out/rustfs.log 2>&1 &
rf=$!
trap 'kill $sw $rf 2>/dev/null || true' EXIT
until curl -s -o /dev/null http://127.0.0.1:21000/ && curl -s -o /dev/null http://127.0.0.1:21050/; do sleep 1; done
sleep 5

run() { echo "## $*"; "$@" 2>&1 | tr '\r' '\n' | grep -v '^loaded' ; }

{
run tmp/s10 probe
run tmp/s10 repro-hang
run tmp/s10p repro-hang
run tmp/s10 repro-lost
run tmp/s10p repro-lost
run tmp/s10 cas -s3 seaweed
run tmp/s10 cas -s3 rustfs -g 8 -n 10
run tmp/s10 compact-race -s3 rustfs
run tmp/s10p compact-race -s3 seaweed
} > tmp/out/correctness.txt

for v in s10 s10p; do
  for w in 10 25 50; do run tmp/$v ingest -workers $w -flush 2s -dur 60s -compact 15s -compactors 2; done
  run tmp/$v ingest -workers 25 -flush 5s -dur 60s -compact 15s -compactors 2
done > tmp/out/ingest.txt
[ "${1:-}" = quick ] && exit 0
run tmp/s10p ingest -workers 25 -flush 5s -dur 300s -compact 30s -compactors 2 > tmp/out/ingest-long.txt

{
run tmp/s10 bulk -builds 1000 -par 4
run tmp/s10 qbench
run tmp/s10p qbench
run systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G -- tmp/s10 qbench -n 5
run tmp/s10 bulk -bucket s10-bulk8 -builds 120 -pershard 1 -par 4
run tmp/s10 qbench -bucket s10-bulk8 -builds 120
run tmp/s10 retention -keep 1440h
} > tmp/out/volume.txt
echo "results in tmp/out/"
