#!/usr/bin/env bash
# Starts RustFS 1.0.3 (upstream musl release binary) with S3 on 127.0.0.1:20950.
# Usage: start-rustfs.sh <datadir>   (runs in foreground)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
dir=${1:-$here/tmp/rustfs}
mkdir -p "$dir"
export RUSTFS_ACCESS_KEY=bobadmin RUSTFS_SECRET_KEY=bobadmin-secret RUSTFS_CONSOLE_ENABLE=false \
  RUSTFS_ADDRESS=127.0.0.1:20950 RUSTFS_REGION=us-east-1 RUSTFS_OBS_LOG_DIRECTORY="$dir/../rustfs-logs"
exec "$here/.tools/rustfs" server "$dir"
