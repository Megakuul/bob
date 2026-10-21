#!/usr/bin/env bash
# Use the saved 900 s STS credentials before and after expiry on both servers.
. "$(dirname "$0")/env.sh"
for f in rustfs:$RUSTFS_PORT weed:$WEED_S3; do $S11/tmp/s11 stscheck -server ${f%%:*} -endpoint http://127.0.0.1:${f##*:} -file $S11/tmp/sts-${f%%:*}.txt; done
exp=$(sed -n 4p $S11/tmp/sts-rustfs.txt); wait_s=$(( $(date -d "$exp" +%s) - $(date +%s) + 10 )); sleep $wait_s
for f in rustfs:$RUSTFS_PORT weed:$WEED_S3; do $S11/tmp/s11 stscheck -server ${f%%:*} -endpoint http://127.0.0.1:${f##*:} -file $S11/tmp/sts-${f%%:*}.txt; done
