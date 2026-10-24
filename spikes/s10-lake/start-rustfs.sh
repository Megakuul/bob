#!/usr/bin/env bash
# Single-node RustFS 1.0.3 (upstream aarch64 musl binary), S3 on 127.0.0.1:21050. Foreground.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
dir=${1:-$here/tmp/rustfs}
mkdir -p "$dir"
exec "$here/.tools/dl-rustfs/rustfs" server --address 127.0.0.1:21050 \
  --access-key bobadmin --secret-key bobadmin-secret "$dir"
