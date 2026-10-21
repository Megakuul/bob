#!/usr/bin/env bash
# Single-node SeaweedFS (nixpkgs 26.05 pin, 4.36), S3 on 127.0.0.1:21000. Foreground.
# Usage: start-seaweed.sh [datadir]
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
dir=${1:-$here/tmp/seaweed}
mkdir -p "$dir"
exec "$here/.tools/seaweedfs/bin/weed" server -dir="$dir" -ip=127.0.0.1 -ip.bind=127.0.0.1 \
  -master.port=21010 -master.port.grpc=21011 \
  -volume.port=21020 -volume.port.grpc=21021 -volume.max=300 -master.volumeSizeLimitMB=1024 \
  -filer.port=21030 -filer.port.grpc=21031 \
  -s3 -s3.port=21000 -s3.port.grpc=21001 -s3.port.iceberg=0 -s3.config="$here/s3.json" -s3.iam=false \
  -iam=false -webdav=false -metricsPort=0
