#!/bin/sh
# Namespace-entry overhead: median of 30 runs of `nix --version`, direct vs via bobns (bind and pivot modes).
N=$1; S=/home/bobtest/.local/share/bob-$N
med() { sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'; }
run() { for i in $(seq 30); do s=$(date +%s%N); "$@" >/dev/null 2>&1; e=$(date +%s%N); echo $(( (e-s)/1000 )); done | med; }
echo "$N direct_us=$(run $N --version) bobns_bind_us=$(run bobns -state $S -- $N --version) bobns_pivot_us=$(run bobns -state $S -force-pivot -- $N --version) bobns_bind_true_us=$(run bobns -state $S -- true)"
