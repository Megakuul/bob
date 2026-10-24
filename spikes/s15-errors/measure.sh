#!/usr/bin/env bash
# S15 measurements (rerun on a quiet machine). Timing-dependent: capture durations, replay speed.
set -euo pipefail
cd "$(dirname "$0")"
export XDG_CACHE_HOME=/root/.cache-s15
CGO_ENABLED=0 go build -o s15 ./cmd/s15 && CGO_ENABLED=0 go build -o s15cache ./cmd/s15cache
echo "== replay throughput [8c/15G]"; CGO_ENABLED=0 go test ./translate -run XXX -bench ReplayCold -benchtime 20x
echo "== replay throughput [2c/4G]"; systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G -- env CGO_ENABLED=0 go test ./translate -run XXX -bench ReplayCold -benchtime 20x
echo "== golden harness"; CGO_ENABLED=0 go test ./translate
echo "== replay one fixture end to end (process start included)"
for i in 1 2 3; do /usr/bin/time -f "%e s %M KB" ./s15 replay fixtures/ok-cold/2.34.9 > /dev/null; done
if [ "${FULL:-0}" = 1 ]; then echo "== full fault capture (both Nix versions)"; /usr/bin/time -f "%e s" faults/make.sh; fi
