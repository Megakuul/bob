#!/usr/bin/env bash
# Host Nix substituting the s12 closure from cachesrv with different credential mechanisms.
# Usage: nix-auth-matrix.sh <top-store-path>
set -u
D=$(cd "$(dirname "$0")/.." && pwd); T=$1; PUB=$(cat $D/tmp/keys/cache.sec.pub)
U=http://127.0.0.1:21220/nix
SC=/root/scratch-s12/auth; mkdir -p $SC
printf 'machine 127.0.0.1\nlogin bob\npassword alice-token-123\n' > $SC/netrc-good
printf 'machine 127.0.0.1\nlogin bob\npassword wrong\n' > $SC/netrc-bad
: > $SC/netrc-empty
try() { # name, substituter-url, extra-config
  local name=$1 url=$2 extra=$3 st=$SC/store-$1
  chmod -R u+w $st 2>/dev/null; rm -rf $st $SC/xdg-$1
  local t0=$(date +%s.%N)
  out=$(XDG_CACHE_HOME=$SC/xdg-$1 NIX_CONFIG="substituters = $url https://cache.nixos.org
trusted-public-keys = $PUB cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
$extra" nix build --store $st --no-link --max-jobs 0 $T 2>&1)
  local rc=$? t1=$(date +%s.%N)
  printf '%-14s rc=%d %5.1fs  %s\n' "$name" $rc $(awk "BEGIN{print $t1-$t0}") "$(echo "$out" | grep -v '^copying\|^this path\|^  /nix' | tail -3 | tr '\n' ' ' | cut -c1-220)"
}
echo "== $(date -Is) server log offset $(wc -l < $D/tmp/srv.log)"
try netrc        $U "netrc-file = $SC/netrc-good"
try anonymous    $U "netrc-file = $SC/netrc-empty"
try access-token $U "netrc-file = $SC/netrc-empty
access-tokens = 127.0.0.1:21220=alice-token-123 127.0.0.1=alice-token-123"
try userinfo     "http://bob:alice-token-123@127.0.0.1:21220/nix" "netrc-file = $SC/netrc-empty"
try netrc-bad    $U "netrc-file = $SC/netrc-bad"
