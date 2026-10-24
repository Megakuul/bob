#!/bin/bash
# Spike S1 evaluation-speed measurement. Rerun on a quiet machine:
#   ./measure.sh            -> [8c/15G]
#   ./measure.sh low        -> [2c/4G]  (re-execs itself under systemd-run AllowedCPUs=0-1 MemoryMax=4G)
# Output: one line per (N, scenario): wall / user / sys seconds and the GOCACHE size.
set -eu
S1=/root/bob/spikes/s01-bobgo
if [ "${1:-}" = low ]; then
  exec systemd-run --quiet --scope -p AllowedCPUs=0-1 -p MemoryMax=4G -- "$0" low-inner
fi
PROFILE="[8c/15G]"; [ "${1:-}" = low-inner ] && PROFILE="[2c/4G]"
. $S1/tests/env.sh
W=$S1/tmp/measure-$$; mkdir -p $W
export GOCACHE=$W/gocache
TIMEFORMAT='%R %U %S'
t() { # label cmd...
  local out; out=$( { time "$@" >/dev/null 2>$W/err; } 2>&1 ) || { echo "FAILED $1"; cat $W/err; exit 1; }
  printf '%-6s %-12s wall=%6.2fs user=%6.2fs sys=%5.2fs  %s  gocache=%s\n' "$N" "$SC" $out "$(grep -o 'build=[0-9.]*ms run=[0-9.]*ms' $W/err)" "$(du -sh $GOCACHE 2>/dev/null | cut -f1)"
}
echo "profile $PROFILE  $(date -Is)  load: $(cut -d' ' -f1-3 /proc/loadavg)  go: $(go version | cut -d' ' -f3)"
for N in ${NS:-10 100 1000}; do
  R=$W/synth$N; $S1/bin/synth -n $N -o $R -fw $S1/fw ${SYNTH_FLAGS:-}
  rm -rf $GOCACHE
  SC=cold;       t $S1/bin/bobeval -root $R -timing
  SC=noop;       t $S1/bin/bobeval -root $R -timing
  last=$(ls -d $R/pkgs/*/p* | sort | tail -1)
  sed -i 's#"src/\*.c"#"src/*.c", "x.c"#' $last/bob.go
  SC=leaf-body;  t $S1/bin/bobeval -root $R -timing
  sed -i 's#"src/\*.c"#"src/*.c", "x.c"#' $R/pkgs/g00/p0001/bob.go
  SC=root-body;  t $S1/bin/bobeval -root $R -timing
  printf '\n// Extra changes the export data of the package.\nvar Extra = 1\n' >> $R/pkgs/g00/p0001/bob.go
  SC=root-api;   t $S1/bin/bobeval -root $R -timing
  # a second, different repo of the same size with a warm toolchain/framework cache
  $S1/bin/synth -n $N -o $W/other$N -fw $S1/fw; sed -i 's/example.com\/synth/example.com\/other/' $(grep -rl example.com/synth $W/other$N)
  R=$W/other$N
  SC=new-repo;   t $S1/bin/bobeval -root $R -timing
  rm -rf $W/other$N $W/synth$N
done
rm -rf $W
