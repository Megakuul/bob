#!/usr/bin/env bash
# Starts RustFS 1.0.3 (binary downloaded by S9's measure.sh) with S3 on 127.0.0.1:21750.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
dir=${1:-$here/tmp/rustfs}
mkdir -p "$dir"
bin=$here/../s09-dynamitedb/.tools/rustfs
export RUSTFS_ACCESS_KEY=bobadmin RUSTFS_SECRET_KEY=bobadmin-secret RUSTFS_CONSOLE_ENABLE=false \
  RUSTFS_ADDRESS=127.0.0.1:21750 RUSTFS_REGION=us-east-1 RUSTFS_OBS_LOG_DIRECTORY="$dir/../rustfs-logs"
exec "$bin" server "$dir"
