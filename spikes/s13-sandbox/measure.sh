#!/bin/bash
# S13 measurements. Run as root on a quiet machine:  ./measure.sh > tmp/measure.out 2>&1
# Needs: bobtest user, run-part1.sh prerequisites (bin/ built), nixs13.sh state (see write-up).
set -u
S=/root/bob/spikes/s13-sandbox; H=/home/bobtest/s13
GO=$(dirname "$(dirname "$(readlink -f "$(command -v go)")")")
BASH_STORE=$(grep -- '-bash-' $S/tmp/go-closure.txt | head -1)
cat > $H/overhead.sh <<EOF2
#!/bin/bash
cd $H
med() { sort -n | awk '{a[NR]=\$1} END {print a[int((NR+1)/2)]}'; }
t() { for i in \$(seq 21); do s=\$(date +%s%N); "\$@" >/dev/null 2>&1; echo \$(( (\$(date +%s%N)-s)/1000 )); done | med; }
echo "direct bash -c true (us):          \$(t $BASH_STORE/bin/bash -c true)"
echo "evalbox, no cgroup (us):           \$(t ./bin/evalbox -cgroup=false -store @$H/store.list -dir / -- $BASH_STORE/bin/bash -c true)"
echo "evalbox, cgroup leaf (us):         \$(t ./bin/evalbox -store @$H/store.list -dir / -- $BASH_STORE/bin/bash -c true)"
EOF2
chown bobtest $H/overhead.sh; chmod +x $H/overhead.sh
for prof in "8c/15G|" "2c/4G|-p AllowedCPUs=0-1 -p MemoryMax=4G"; do
  name=${prof%%|*}; props=${prof#*|}
  echo "##### evalbox overhead [$name]"
  systemd-run -p User=bobtest -p Delegate=yes $props --wait --pipe --quiet -- $H/overhead.sh
done
echo "##### part 1 functional run [8c/15G]"; $S/run-part1.sh | grep -E '^(===|PASS|LEAK|✗|rc=|seed)'
echo "##### part 2 services rootless [8c/15G]"; $S/nixs13.sh sh /home/bobtest/s13n/services.sh
echo "##### part 2 services rootless [2c/4G]"; systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G -- $S/nixs13.sh sh /home/bobtest/s13n/services.sh
