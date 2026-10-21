#!/usr/bin/env bash
# Reruns all S9 measurements. Starts SeaweedFS (nixpkgs 26.05) and RustFS 1.0.3 on ports
# 20900/20950, runs every experiment against both, writes results/*.txt, stops the servers.
# Numbers depend on machine load: rerun on a quiet host for latency/throughput figures.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd); cd "$here"
export XDG_CACHE_HOME=/root/.cache-s09 CGO_ENABLED=0 NIX_CONFIG=$'cores = 4\nmax-jobs = 2'
[ -x .tools/seaweedfs/bin/weed ] || nix build -o .tools/seaweedfs github:NixOS/nixpkgs/cd4ea8d57de16f4810bc5f14207bac45fbf19f67#seaweedfs
if [ ! -x .tools/rustfs ]; then
  mkdir -p .tools/dl && curl -sSL -o .tools/dl/r.zip https://github.com/rustfs/rustfs/releases/download/1.0.3/rustfs-linux-aarch64-musl-v1.0.3.zip
  echo "28e973c6e630dfa1c7a6d7af86b360e598a34c476d34a0dcbcfbeb70b1f06747  .tools/dl/r.zip" | sha256sum -c
  python3 -I -c 'import zipfile,sys;zipfile.ZipFile(sys.argv[1]).extract("rustfs",sys.argv[2])' .tools/dl/r.zip .tools
  chmod +x .tools/rustfs
fi
[ -d vendor-src ] || git clone https://github.com/megakuul/dynamitedb vendor-src   # D46: current HEAD
go build -o tmp/s09 ./cmd/s09
mkdir -p results tmp
rm -rf tmp/seaweed tmp/rustfs
./start-seaweed.sh tmp/seaweed > tmp/seaweed.log 2>&1 & sw=$!
./start-rustfs.sh tmp/rustfs > tmp/rustfs.log 2>&1 & rf=$!
trap 'kill $sw $rf 2>/dev/null || true; wait 2>/dev/null || true' EXIT
until curl -s -o /dev/null 127.0.0.1:20900 && curl -s -o /dev/null 127.0.0.1:20950; do sleep 1; done
sleep 5
for be in "seaweedfs http://127.0.0.1:20900" "rustfs http://127.0.0.1:20950"; do
  set -- $be; f="-backend $1 -endpoint $2"
  for cmd in probe panics faults oplat lists; do ./tmp/s09 $f $cmd > results/$cmd-$1.txt 2>&1 || true; done
  ./tmp/s09 $f -rounds 50 -jobs 300 stress > results/stress-$1.txt 2>&1 || true
  ./tmp/s09 $f chaos > results/chaos-$1.txt 2>&1 || true
  ./tmp/s09 $f -leader chaos >> results/chaos-$1.txt 2>&1 || true
  for iv in 2s 5s 10s; do ./tmp/s09 $f -interval $iv -for 60s poll >> results/poll-$1.txt 2>&1 || true; done
done
# low-power profile (R-NFR-008): claim stress against RustFS with the client limited to 2 CPUs / 4 GiB
systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G -- ./tmp/s09 -backend rustfs -endpoint http://127.0.0.1:20950 \
  -rounds 20 -jobs 300 stress > results/stress-rustfs-2c4g.txt 2>&1 || true
