#!/bin/bash
# shim overhead: N runs via shim vs N runs of the same binary inside one already-entered namespace
N=20
now() { date +%s%N; }
for tool in "gopls version" "go version" "true"; do
  set -- $tool
  s=$(now); for i in $(seq $N); do /home/bobtest/s01/acme/.bob/bin/gopls version >/dev/null 2>&1; done; e=$(now)
  [ "$1" = go ] && { s=$(now); for i in $(seq $N); do /home/bobtest/s01/acme/.bob/bin/go version >/dev/null; done; e=$(now); }
  [ "$1" = true ] && { s=$(now); for i in $(seq $N); do /home/bobtest/s01/bin/bobexec -state /home/bobtest/s01/state -- /bin/true; done; e=$(now); }
  shim=$(( (e-s)/N/1000 ))
  d=$(/home/bobtest/s01/bin/bobexec -state /home/bobtest/s01/state -env /home/bobtest/s01/acme/.bob/env -- bash -c "s=\$(date +%s%N); for i in \$(seq $N); do $tool >/dev/null 2>&1; done; e=\$(date +%s%N); echo \$(( (e-s)/$N/1000 ))")
  echo "$tool: via shim ${shim}us/run, inside namespace directly ${d}us/run, overhead $(( shim-d ))us"
done
