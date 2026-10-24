#!/usr/bin/env bash
# Create buckets, the shipped policies and test users on the running RustFS (via mc admin API).
set -euo pipefail
. "$(dirname "$0")/env.sh"; export MC_CONFIG_DIR=$S11/tmp/mc
$MC alias set rfs http://127.0.0.1:$RUSTFS_PORT $ROOT_AK $ROOT_SK >/dev/null
for b in control fleet analytics artifacts; do $MC mb -q -p rfs/bob-$b; done
$MC admin policy create rfs bob-worker $S11/policies/worker.json >/dev/null
$MC admin policy create rfs bob-orchestrator $S11/policies/orchestrator.json >/dev/null
add() { $MC admin user add rfs "$1" "$2" >/dev/null; $MC admin policy attach rfs "$3" --user "$1" >/dev/null; }
add wkr  wkrsecret-0123456789  bob-worker
add orch orchsecret-0123456789 bob-orchestrator
add wide widesecret-0123456789 bob-orchestrator
# parent of worker STS sessions: worker policy without the sts Deny; only orchestrators hold this key
$MC admin policy create rfs bob-worker-sts-parent $S11/policies/rustfs-worker-sts-parent.json >/dev/null
add wsts wstssecret-0123456789 bob-worker-sts-parent
