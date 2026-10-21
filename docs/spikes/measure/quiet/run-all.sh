#!/usr/bin/env bash
# Quiet-machine reruns of the timing-sensitive Phase 2.2 spikes, one after another.
cd /root/bob
for s in s02-go s03-rust s01-bobgo s04-npm s05-ocaml s06-codegen s07-cross s08-bundles s12-cache s13-sandbox s14-sched; do
  n=${s%%-*}
  log=docs/spikes/measure/quiet/$s.log
  echo "== $s start $(date -u +%FT%TZ) load $(cut -d' ' -f1-3 /proc/loadavg)" > "$log"
  ( cd spikes/$s && XDG_CACHE_HOME=/root/.cache-$n timeout 5400 ./measure.sh ) >> "$log" 2>&1
  echo "== $s exit $? end $(date -u +%FT%TZ)" >> "$log"
done
echo done > docs/spikes/measure/quiet/ALL-DONE
