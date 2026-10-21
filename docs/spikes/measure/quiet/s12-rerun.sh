#!/usr/bin/env bash
cd /root/bob/spikes/s12-cache
rm -rf /root/scratch-s12/seaweed; mkdir -p /root/scratch-s12
scripts/s3-up.sh > /root/scratch-s12/weed.log 2>&1 & W=$!
for i in $(seq 60); do curl -s -o /dev/null http://127.0.0.1:21203/ && break; sleep 1; done
scripts/srv-up.sh > /root/scratch-s12/srv.log 2>&1 & S=$!
sleep 2
log=/root/bob/docs/spikes/measure/quiet/s12-cache.log
echo "== s12-cache rerun start $(date -u +%FT%TZ) load $(cut -d' ' -f1-3 /proc/loadavg)" > $log
XDG_CACHE_HOME=/root/.cache-s12 timeout 3600 ./measure.sh >> $log 2>&1
echo "== exit $? end $(date -u +%FT%TZ)" >> $log
kill $S $W; wait 2>/dev/null
