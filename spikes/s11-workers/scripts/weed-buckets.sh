#!/usr/bin/env bash
. "$(dirname "$0")/env.sh"; export MC_CONFIG_DIR=$S11/tmp/mc
for i in $(seq 60); do
  $MC alias set sw http://127.0.0.1:$WEED_S3 $ROOT_AK $ROOT_SK >/dev/null 2>&1 && $MC mb -q -p sw/bob-probe >/dev/null 2>&1 && $MC rb -q sw/bob-probe >/dev/null 2>&1 && break
  sleep 1
done
for b in control fleet analytics artifacts; do $MC mb -q -p sw/bob-$b; done
