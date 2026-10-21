#!/usr/bin/env bash
# S12 throughput: upload (worker -> staging), promote (orchestrator verify+copy+sign), and
# download (Nix substitution, raw curl) of the ~100 MiB fixture closure.
# Usage: ./measure.sh 8c|2c [runs]   (needs scripts/s3-up.sh running; root)
# In the 2c profile the worker (bobup), the orchestrator (cachesrv) and the Nix client each run
# in a 2 vCPU / 4 GiB cgroup; SeaweedFS stays unconstrained (it stands in for the S3 service).
set -u
D=$(cd "$(dirname "$0")" && pwd); cd $D
PROF=${1:-8c}; RUNS=${2:-3}; PORT=21222; U=http://127.0.0.1:$PORT
export XDG_CACHE_HOME=/root/.cache-s12
LIM=(); [ "$PROF" = 2c ] && LIM=(systemd-run --quiet --scope -p AllowedCPUs=0-1 -p MemoryMax=4G --)
"${LIM[@]}" ./bin/cachesrv -addr 127.0.0.1:$PORT -public-url $U -key tmp/keys/cache.sec -config tmp/tokens.json > tmp/measure-srv-$PROF.log 2>&1 &
SRV=$!; sleep 1
PUB=$(cat tmp/keys/cache.sec.pub); SC=/root/scratch-s12/measure; mkdir -p $SC
printf 'machine 127.0.0.1\nlogin bob\npassword alice-token-123\n' > $SC/netrc
now() { date +%s.%N; }
printf '| profile | run | upload s | upload MiB/s | promote s | nix substitute s | nix MiB/s | curl NARs s | curl MiB/s |\n|---|---|---|---|---|---|---|---|---|\n'
for i in $(seq 1 $RUNS); do
  SALT=m$PROF$i$RANDOM
  TOP=$(NIX_CONFIG=$'cores = 4\nmax-jobs = 2' nix build --impure -f fixture/closure.nix --argstr salt $SALT top --no-link --print-out-paths 2>/dev/null)
  MANY=$(nix path-info -r $TOP | grep s12-many)
  BOBP=$(nix path-info -r $TOP | grep s12-)
  MIB=$(nix path-info -S $MANY | awk '{printf "%.1f", $2/1048576}')
  t0=$(now); "${LIM[@]}" ./bin/bobup -build $SALT $TOP > $SC/up.log 2>&1; t1=$(now)
  curl -s -X POST -u x:admin-token-000 "$U/internal/promote?build=$SALT&project=demo" > $SC/pr.json; t2=$(now)
  chmod -R u+w $SC/store 2>/dev/null; rm -rf $SC/store $SC/xdg
  t3=$(now); XDG_CACHE_HOME=$SC/xdg NIX_CONFIG="substituters = $U/nix
trusted-public-keys = $PUB
netrc-file = $SC/netrc" "${LIM[@]}" nix build --store $SC/store --no-link --max-jobs 0 $MANY > $SC/dl.log 2>&1; rc=$?; t4=$(now)
  t5=$(now); for p in $BOBP; do "${LIM[@]}" curl -s -u x:alice-token-123 -o /dev/null $U/nix/nar/$(basename $p | cut -c1-32).nar.zst; done; t6=$(now)
  awk -v p=$PROF -v i=$i -v m=$MIB -v a=$t0 -v b=$t1 -v c=$t2 -v d=$t3 -v e=$t4 -v f=$t5 -v g=$t6 -v rc=$rc 'BEGIN{
    printf "| [%s] | %d | %.2f | %.0f | %.2f | %.2f%s | %.0f | %.2f | %.0f |\n", (p=="2c"?"2c/4G":"8c/15G"), i, b-a, m/(b-a), c-b, e-d, (rc?" (FAILED)":""), m/(e-d), g-f, m/(g-f)}'
done
echo; echo "upload log: $(tail -1 $SC/up.log)"; echo "promote: $(cat $SC/pr.json)"
curl -s $U/internal/stats; kill $SRV
