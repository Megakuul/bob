#!/usr/bin/env bash
# Failure cases of the staging -> promotion protocol. Needs weed (s3-up.sh) and cachesrv running.
set -u
D=$(cd "$(dirname "$0")/.." && pwd); cd $D
export XDG_CACHE_HOME=/root/.cache-s12 NIX_CONFIG=$'cores = 4\nmax-jobs = 2'
A="-u x:admin-token-000"; U=http://127.0.0.1:21220
fx() { nix build --impure -f fixture/closure.nix --argstr salt $1 --arg bigMiB ${2:-1024} ${3:-top} --no-link --print-out-paths 2>/dev/null | tail -1; }
narinfo() { curl -s -o /dev/null -w '%{http_code}' $A $U/nix/$(basename $1 | cut -c1-32).narinfo; }

echo "== 1. worker dies mid-upload (no COMPLETE.json)"
T=$(fx ${SALT:-c}1); ./bin/bobup -build ${SALT:-c}1 -crash-after 2 -j 1 $T 2>&1 | tail -1
curl -s -X POST $A "$U/internal/promote?build=${SALT:-c}1&project=demo"; echo " top narinfo: $(narinfo $T)"

echo "== 2. staged NAR replaced after upload (tamper)"
T=$(fx ${SALT:-c}2); ./bin/bobup -build ${SALT:-c}2 $T >/dev/null 2>&1
./bin/tamper -key staging/${SALT:-c}2/nar/$(basename $(nix path-info -r $T | grep s12-text) | cut -c1-32).nar.zst
curl -s -X POST $A "$U/internal/promote?build=${SALT:-c}2&project=demo"; echo " top narinfo: $(narinfo $T)"
for p in $(nix path-info -r $T | grep s12-); do echo "   $(basename $p): $(narinfo $p)"; done

echo "== 3. orchestrator killed during promotion, then re-run"
T=$(fx ${SALT:-c}3 1024 "top big"); ./bin/bobup -build ${SALT:-c}3 $(fx ${SALT:-c}3 1024 top) $(fx ${SALT:-c}3 1024 big) >/dev/null 2>&1
curl -s -X POST $A "$U/internal/promote?build=${SALT:-c}3&project=demo" >/dev/null & sleep ${KILL_AFTER:-2.5}
kill $(cat tmp/srv.pid); wait; sleep 0.3
(S12_OCI_SECRET=spike-shared-secret setsid scripts/srv-up.sh >> tmp/srv.log 2>&1 & echo $! > tmp/srv.pid); sleep 1
for p in $(nix path-info -r $(fx ${SALT:-c}3 1024 top) $(fx ${SALT:-c}3 1024 big) | grep s12-); do echo "   after kill $(basename $p): $(narinfo $p)"; done
curl -s -X POST $A "$U/internal/promote?build=${SALT:-c}3&project=demo"; echo

echo "== 4. two orchestrators promote the same build concurrently"
T=$(fx ${SALT:-c}4); ./bin/bobup -build ${SALT:-c}4 $T >/dev/null 2>&1
(S12_PORT=21221 S12_OCI_SECRET=spike-shared-secret setsid scripts/srv-up.sh > tmp/srv2.log 2>&1 & echo $! > tmp/srv2.pid); sleep 1
curl -s -X POST $A "$U/internal/promote?build=${SALT:-c}4&project=demo" > tmp/p1.json &
curl -s -X POST $A "http://127.0.0.1:21221/internal/promote?build=${SALT:-c}4&project=demo" > tmp/p2.json &
wait; echo " orch1: $(cat tmp/p1.json)"; echo " orch2: $(cat tmp/p2.json)"; kill $(cat tmp/srv2.pid)
XDG_CACHE_HOME=/root/scratch-s12/xdg-${SALT:-c}4 NIX_CONFIG="netrc-file = /root/scratch-s12/auth/netrc-good" nix store verify --store $U/nix --trusted-public-keys "$(cat tmp/keys/cache.sec.pub)" $(nix path-info -r $T | grep s12-) && echo " nix store verify: OK"

echo "== 5. same store path rebuilt with different content (non-reproducible)"
T=$(fx ${SALT:-c}5 1 big); ./bin/bobup -build ${SALT:-c}5a $T >/dev/null 2>&1; curl -s -X POST $A "$U/internal/promote?build=${SALT:-c}5a&project=demo"; echo
nix store delete $T >/dev/null 2>&1; T2=$(fx ${SALT:-c}5 1 big); [ "$T" = "$T2" ] && echo " rebuilt same path $T2"
./bin/bobup -build ${SALT:-c}5b $T2 >/dev/null 2>&1; curl -s -X POST $A "$U/internal/promote?build=${SALT:-c}5b&project=other"; echo
