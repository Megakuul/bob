#!/usr/bin/env bash
# Starts a single-node SeaweedFS (nixpkgs 26.05, 4.36) with S3 on 127.0.0.1:20900.
# Usage: start-seaweed.sh <datadir>   (runs in foreground)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
dir=${1:-$here/tmp/seaweed}
mkdir -p "$dir"
exec "$here/.tools/seaweedfs/bin/weed" server -dir="$dir" -ip=127.0.0.1 -ip.bind=127.0.0.1 \
  -master.port=20910 -master.port.grpc=20911 \
  -volume.port=20920 -volume.port.grpc=20921 -volume.max=300 -master.volumeSizeLimitMB=128 \
  -filer.port=20930 -filer.port.grpc=20931 \
  -s3 -s3.port=20900 -s3.port.grpc=20901 -s3.port.iceberg=0 -s3.config="$here/s3.json" -s3.iam=false \
  -iam=false -webdav=false -metricsPort=0
