#!/usr/bin/env bash
# Stop every S11 server (matched by binary path) and wait until they are gone.
pkill -f '^tmp/s11 ' 2>/dev/null
pkill -f '^/root/bob/spikes/s11-workers/.tools/' 2>/dev/null
for i in $(seq 50); do pgrep -f '^/root/bob/spikes/s11-workers/.tools/' >/dev/null || break; sleep 0.2; done
pkill -9 -f '^/root/bob/spikes/s11-workers/.tools/' 2>/dev/null; rm -f /root/bob/spikes/s11-workers/tmp/*/pid; true
