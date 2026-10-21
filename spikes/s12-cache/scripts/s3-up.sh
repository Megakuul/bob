#!/usr/bin/env bash
# Start SeaweedFS (weed mini) as the artifacts bucket on ports 21200-21219.
set -euo pipefail
D=$(cd "$(dirname "$0")/.." && pwd)
DATA=${S12_S3_DATA:-/root/scratch-s12/seaweed}
mkdir -p "$DATA"
exec "$D/.tools/seaweedfs/bin/weed" mini -dir="$DATA" -ip=127.0.0.1 -ip.bind=127.0.0.1 \
  -master.port=21200 -master.port.grpc=21210 \
  -volume.port=21201 -volume.port.grpc=21211 \
  -filer.port=21202 -filer.port.grpc=21212 \
  -s3.port=21203 -s3.port.grpc=21213 -s3.port.iceberg=21219 \
  -admin.port=21204 -admin.port.grpc=21214 -admin.ui=false \
  -debug.port=21205 -s3.config="$D/scripts/s3.json" -bucket=artifacts \
  -webdav=false -master.volumeSizeLimitMB=1024
