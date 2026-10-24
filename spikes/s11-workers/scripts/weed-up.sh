#!/usr/bin/env bash
# Start a fresh single-node SeaweedFS (master+volume+filer+s3). $1 = s3 identities config.
set -euo pipefail
. "$(dirname "$0")/env.sh"
CFG=${1:?s3 config}; IAMCFG=${2:-}
D=$S11/tmp/weed; rm -rf "$D"; mkdir -p "$D/data"
extra=(); [ -n "$IAMCFG" ] && extra+=(-s3.iam.config="$IAMCFG")
# WEED_SECURITY=1: run from a dir holding security.toml (JWT on filer + volume HTTP).
if [ "${WEED_SECURITY:-}" = 1 ]; then cp $S11/policies/seaweedfs-security.toml "$D/security.toml"; elif [ -n "${WEED_SECURITY:-}" ]; then cp "$WEED_SECURITY" "$D/security.toml"; fi
cd "$D"
nohup "$WEED_BIN" server -dir="$D/data" -ip=127.0.0.1 -ip.bind=127.0.0.1 \
  -master.port=$WEED_MASTER -master.port.grpc=$((WEED_MASTER+10)) -master.volumeSizeLimitMB=64 \
  -volume.port=$WEED_VOLUME -volume.port.grpc=$((WEED_VOLUME+10)) -volume.max=200 \
  -filer -filer.port=$WEED_FILER -filer.port.grpc=$((WEED_FILER+10)) \
  -s3 -s3.port=$WEED_S3 -s3.port.grpc=$((WEED_S3+10)) -s3.port.iceberg=0 $([ "$("$WEED_BIN" server -h 2>&1 | grep -c s3.port.lance)" -gt 0 ] && echo -s3.port.lance=0) \
  -s3.config="$CFG" "${extra[@]}" -master.telemetry=false >"$D/log" 2>&1 &
echo $! >"$D/pid"
for i in $(seq 150); do curl -s -o /dev/null http://127.0.0.1:$WEED_S3/ && break; sleep 0.2; done
sleep 2
echo "weed up pid $(cat $D/pid)"
