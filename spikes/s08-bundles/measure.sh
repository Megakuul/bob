#!/bin/sh
# S8 measurements. Usage: ./measure.sh 8c|2c   (rerun on a quiet machine; timings are indicative)
set -e
cd "$(dirname "$0")"
export XDG_CACHE_HOME=/root/.cache-s08 CGO_ENABLED=0
pre=""; [ "$1" = 2c ] && pre="systemd-run --quiet --scope -p AllowedCPUs=0-1 -p MemoryMax=4G --"
go build -o bin/s08 ./cmd/s08
for s in api-v1 api-v1-one api-v1-g4; do
  rm -rf tmp/oci/$s; echo "[$1] oci $s (cold):"; $pre ./bin/s08 oci specs/$s.json | tail -1
done
echo "[$1] oci api-v2 incremental into a v1 layout:"; rm -rf tmp/oci/m; cp -r tmp/oci/api-v1 tmp/oci/m
sed 's#tmp/oci/api-v2"#tmp/oci/m"#' specs/api-v2.json > tmp/m.json; $pre ./bin/s08 oci tmp/m.json | tail -1
for s in ai-api ai-api-fixed ai-curl; do echo "[$1] appimage $s:"; $pre ./bin/s08 appimage specs/$s.json; done
