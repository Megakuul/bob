#!/bin/sh
# S0b task 4: authenticated, signed binary cache consumed by rootless Nix with bob-written config only.
# Usage (as root): ./authcache-test.sh <nix-binary-name> <port>
set -u
N=$1; PORT=$2; H=/home/bobtest; SRC=$H/.local/share/bob-$N; CON=$H/.local/share/bob-auth-$N; CACHE=$H/cache-$N
R=$(cd "$(dirname "$0")" && pwd)
PROBE=$(ls -d $SRC/nix/store/*-sandbox-probe-* 2>/dev/null | grep -vE '\.(drv|lock)$' | head -1 | sed "s|$SRC||")
echo "probe path: $PROBE"
rm -rf $CON $CACHE; mkdir -p $CON/etc $CACHE; chown -R bobtest $CON $CACHE
# 1. producer: key + signed copy into a file:// cache (inside bob ns of the source store)
$R/as-bobtest.sh "bobns -state $SRC -- sh -c '$N key generate-secret --key-name bob-test-1 > $H/key-$N.sec && $N key convert-secret-to-public < $H/key-$N.sec > $H/key-$N.pub && $N store sign --key-file $H/key-$N.sec $PROBE && $N copy --to file://$CACHE $PROBE && echo copied'"
PUB=$(cat $H/key-$N.pub)
# 2. HTTP server with basic auth
$R/bin/authcache -dir $CACHE -addr 127.0.0.1:$PORT -pass s3cret > /tmp/authcache-$N.log 2>&1 &
SRV=$!
sleep 0.5
# 3. consumer: fresh store, config written only by bob
write_conf() { mkdir -p $CON/etc; cat > $CON/etc/nix.conf <<C
build-users-group =
sandbox = true
require-drop-supplementary-groups = false
experimental-features = nix-command flakes
substituters = http://127.0.0.1:$PORT
trusted-public-keys = $1
netrc-file = $CON/etc/netrc
C
printf 'machine 127.0.0.1\nlogin bob\npassword %s\n' "$2" > $CON/etc/netrc; touch $CON/etc/nix.user.conf; chown -R bobtest $CON; }
run() { $R/as-bobtest.sh "bobns -state $CON -- sh -c '$N build --no-link $PROBE 2>&1 | tail -3; ls -d $PROBE >/dev/null 2>&1 && echo RESULT=SUBSTITUTED || echo RESULT=NOT-PRESENT'"; }
echo "--- case A: correct token + correct key"; write_conf "$PUB" s3cret; run
rm -rf $CON/nix $CON/cache; echo "--- case B: wrong token"; write_conf "$PUB" wrong; run
rm -rf $CON/nix $CON/cache; echo "--- case C: correct token, untrusted key"; write_conf "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=" s3cret; run
echo "--- server log (tail)"; cat /tmp/authcache-$N.log
kill $SRV
