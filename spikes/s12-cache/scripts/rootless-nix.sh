#!/usr/bin/env bash
# Rootless static Nix 2.34.9 (S0b bobns, as bobtest) substituting from cachesrv with netrc.
# Usage: rootless-nix.sh <store-path> [token]
set -u
D=$(cd "$(dirname "$0")/.." && pwd); T=$1; TOK=${2:-alice-token-123}
H=/home/bobtest; ST=$H/.local/share/bob-s12; PUB=$(cat $D/tmp/keys/cache.sec.pub)
rm -rf $ST; mkdir -p $ST/etc
cat > $ST/etc/nix.conf <<C
build-users-group =
sandbox = true
require-drop-supplementary-groups = false
experimental-features = nix-command
substituters = http://127.0.0.1:21220/nix https://cache.nixos.org
trusted-public-keys = $PUB cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
netrc-file = $ST/etc/netrc
narinfo-cache-negative-ttl = 0
C
printf 'machine 127.0.0.1\nlogin bob\npassword %s\n' "$TOK" > $ST/etc/netrc; chmod 600 $ST/etc/netrc
touch $ST/etc/nix.user.conf; chown -R bobtest $ST
su - bobtest -c "env -i HOME=$H USER=bobtest PATH=$H/.local/share/bob/bin:/usr/bin:/bin TERM=dumb XDG_CACHE_HOME=$ST/cache bobns -state $ST -- sh -c 's=\$(date +%s%N); nix-2.34.9 build --no-link --max-jobs 0 $T 2>&1 | tail -2; e=\$(date +%s%N); echo \"elapsed_ms=\$(( (e-s)/1000000 ))\"; nix-2.34.9 path-info --sigs $T; $T/bin/s12-top -g rootless-nix-ok 2>&1 || true'"
