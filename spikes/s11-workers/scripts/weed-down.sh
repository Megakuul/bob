#!/usr/bin/env bash
# Stop the S11 SeaweedFS and wait until its ports are free.
pkill -f '^/root/bob/spikes/s11-workers/.tools/(seaweedfs|dl-weed)/' 2>/dev/null
for i in $(seq 100); do pgrep -f '^/root/bob/spikes/s11-workers/.tools/(seaweedfs|dl-weed)/' >/dev/null || break; sleep 0.3; done
pkill -9 -f '^/root/bob/spikes/s11-workers/.tools/(seaweedfs|dl-weed)/' 2>/dev/null
for i in $(seq 100); do ss -ltn | grep -qE '127.0.0.1:2111[0-3]|127.0.0.1:2112[0-3]' || break; sleep 0.3; done; true
