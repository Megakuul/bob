#!/usr/bin/env bash
# Start a fresh single-node RustFS on 127.0.0.1:21100 (console off).
set -euo pipefail
. "$(dirname "$0")/env.sh"
D=$S11/tmp/rustfs; rm -rf "$D"; mkdir -p "$D/data"
RUSTFS_ACCESS_KEY=$ROOT_AK RUSTFS_SECRET_KEY=$ROOT_SK RUSTFS_OBS_LOGGER_LEVEL=warn \
  nohup "$RUSTFS_BIN" server --address 127.0.0.1:$RUSTFS_PORT "$D/data" >"$D/log" 2>&1 &
echo $! >"$D/pid"
export MC_CONFIG_DIR=$S11/tmp/mc
for i in $(seq 150); do "$MC" alias set rfs http://127.0.0.1:$RUSTFS_PORT $ROOT_AK $ROOT_SK >/dev/null 2>&1 && "$MC" ls rfs >/dev/null 2>&1 && break; sleep 0.2; done
echo "rustfs up pid $(cat $D/pid)"
