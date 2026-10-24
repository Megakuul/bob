#!/usr/bin/env bash
# Nix's narinfo negative cache vs promotion. Usage: negcache.sh <salt> <negative-ttl|default>
set -u
D=$(cd "$(dirname "$0")/.." && pwd); SALT=$1; TTL=$2
SC=/root/scratch-s12/neg-$SALT; rm -rf $SC; mkdir -p $SC
printf 'machine 127.0.0.1\nlogin bob\npassword alice-token-123\n' > $SC/netrc
XDG_CACHE_HOME=/root/.cache-s12 NIX_CONFIG=$'cores = 4\nmax-jobs = 2' nix build --impure -f $D/fixture/closure.nix --argstr salt $SALT top -o $D/tmp/fx-$SALT >/dev/null 2>&1
T=$(readlink $D/tmp/fx-$SALT)
XDG_CACHE_HOME=/root/.cache-s12 $D/bin/bobup -build b-$SALT $T >/dev/null 2>&1
PUB=$(cat $D/tmp/keys/cache.sec.pub)
C="substituters = http://127.0.0.1:21220/nix https://cache.nixos.org
trusted-public-keys = $PUB cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
netrc-file = $SC/netrc"
[ "$TTL" != default ] && C="$C
narinfo-cache-negative-ttl = $TTL"
b(){ chmod -R u+w $SC/store 2>/dev/null; rm -rf $SC/store; XDG_CACHE_HOME=$SC/xdg NIX_CONFIG="$C" nix build --store $SC/store --no-link --max-jobs 0 $T >$SC/out 2>&1; rc=$?; echo "  rc=$rc $(tail -1 $SC/out | cut -c1-110)"; }
echo "negative-ttl=$TTL"
echo " before promotion:"; b
curl -s -X POST -u x:admin-token-000 "http://127.0.0.1:21220/internal/promote?build=b-$SALT&project=demo" >/dev/null
echo " right after promotion:"; b
if [ "$TTL" != default ] && [ "$TTL" -gt 0 ]; then sleep $((TTL+1)); echo " $((TTL+1)) s after promotion:"; b; fi
