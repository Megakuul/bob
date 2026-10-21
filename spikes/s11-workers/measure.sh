#!/usr/bin/env bash
# S11: rerun every check on fresh servers. Output: tmp/results/*.md. ~6 min + 15 min STS expiry wait.
set -uo pipefail
cd "$(dirname "$0")"; . scripts/env.sh
mkdir -p tmp/results; R=tmp/results; E="$(cat scripts/creds.env)"
go build -o tmp/s11 ./cmd/s11 || exit 1
scripts/down.sh
# RustFS
scripts/rustfs-up.sh && scripts/rustfs-setup.sh >/dev/null
env $E tmp/s11 matrix  -server "RustFS 1.0.3" -endpoint http://127.0.0.1:$RUSTFS_PORT > $R/matrix-rustfs.md
env $E tmp/s11 bpol    -server "RustFS 1.0.3" -endpoint http://127.0.0.1:$RUSTFS_PORT > $R/bpol-rustfs.md
env $E tmp/s11 regflow -server "RustFS 1.0.3" -endpoint http://127.0.0.1:$RUSTFS_PORT > $R/regflow-rustfs.md
env $E S11_STSCALLER=wsts:wstssecret-0123456789 S11_STSOUT=$S11/tmp/sts-rustfs.txt tmp/s11 sts -server "RustFS 1.0.3" -endpoint http://127.0.0.1:$RUSTFS_PORT > $R/sts-rustfs.md
# SeaweedFS (nixpkgs 4.36, then upstream 4.48), static identities + IAM policies
for W in "$S11/.tools/seaweedfs/bin/weed|4.36" "$S11/.tools/dl-weed/weed|4.48"; do
  export WEED_BIN=${W%%|*}; v=${W##*|}
  scripts/weed-down.sh
  scripts/weed-config.sh tmp/weed-s3.json; scripts/weed-up.sh $S11/tmp/weed-s3.json >/dev/null && scripts/weed-buckets.sh >/dev/null
  env $E tmp/s11 matrix -server "SeaweedFS $v" -endpoint http://127.0.0.1:$WEED_S3 > $R/matrix-weed$v.md
  env $E tmp/s11 bpol   -server "SeaweedFS $v" -endpoint http://127.0.0.1:$WEED_S3 > $R/bpol-weed$v.md
  scripts/weed-bypass.sh "SeaweedFS $v default" > $R/bypass-weed$v.md
done
# 4.48 with JWT keys, then JWT + gRPC mTLS
scripts/weed-down.sh
WEED_SECURITY=1 scripts/weed-up.sh $S11/tmp/weed-s3.json >/dev/null && scripts/weed-buckets.sh >/dev/null
scripts/weed-bypass.sh "SeaweedFS 4.48 + JWT keys" >> $R/bypass-weed4.48.md
scripts/weed-down.sh; scripts/weed-mtls.sh
WEED_SECURITY=$S11/tmp/security-mtls.toml scripts/weed-up.sh $S11/tmp/weed-s3.json >/dev/null && scripts/weed-buckets.sh >/dev/null
scripts/weed-bypass.sh "SeaweedFS 4.48 + JWT + gRPC mTLS" >> $R/bypass-weed4.48.md
# 4.48 with -s3.iam.config (STS) + regflow
scripts/weed-down.sh
scripts/weed-iam-config.sh tmp/weed-iam.json; scripts/weed-up.sh $S11/tmp/weed-s3.json $S11/tmp/weed-iam.json >/dev/null && scripts/weed-buckets.sh >/dev/null
env $E tmp/s11 matrix  -server "SeaweedFS 4.48 +iam.config" -endpoint http://127.0.0.1:$WEED_S3 > $R/matrix-weed4.48-iam.md
env $E tmp/s11 regflow -server "SeaweedFS 4.48" -endpoint http://127.0.0.1:$WEED_S3 > $R/regflow-weed4.48.md
env $E S11_STSCALLER=orch:orchsecret-0123456789 S11_ROLEARN=arn:aws:iam::000000000000:role/bob-worker-session S11_STSOUT=$S11/tmp/sts-weed.txt \
  tmp/s11 sts -server "SeaweedFS 4.48" -endpoint http://127.0.0.1:$WEED_S3 > $R/sts-weed4.48.md
# crypto cost
{ echo "[8c/15G]"; tmp/s11 bench; echo "[2c/4G]"; systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G -- tmp/s11 bench; } > $R/bench.md
# STS expiry (waits until the 900 s credentials expire)
[ -n "${SKIP_EXPIRY:-}" ] || scripts/sts-expiry.sh > $R/sts-expiry.md
scripts/down.sh
grep -h -E 'unexpected|failed checks' $R/*.md
