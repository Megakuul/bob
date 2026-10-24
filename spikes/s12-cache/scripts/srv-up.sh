#!/usr/bin/env bash
# Start the S12 cache/registry server on 127.0.0.1:21220.
D=$(cd "$(dirname "$0")/.." && pwd)
exec "$D/bin/cachesrv" -addr 127.0.0.1:${S12_PORT:-21220} -public-url http://127.0.0.1:${S12_PORT:-21220} \
  -key "$D/tmp/keys/cache.sec" -config "$D/tmp/tokens.json" "$@"
