#!/usr/bin/env bash
# Compares bob's resolver with `cargo generate-lockfile` on each workspace in ws/resolve.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd); s3=$(cd "$here/../.." && pwd)
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s03} CARGO_HOME=$s3/tmp/cargo-home
export PATH=$s3/.tools/cargo/bin:$s3/.tools/rustc/bin:$PATH
for d in "${@:-serde_json tokio_full web big}"; do for w in $d; do
  ws=$here/$w; "$s3/tmp/bob-rs" cargo -ws "$ws/ws.json"
  t0=$(date +%s.%N); "$s3/tmp/bob-rs" lock -ws "$ws/ws.json" -no-meta -cargo-lock "$ws/bob.Cargo.lock" -o "$ws/bob.lock.json" 2>"$ws/bob.err"; t1=$(date +%s.%N)
  (cd "$ws" && rm -f Cargo.lock && cargo generate-lockfile -q 2>/dev/null); t2=$(date +%s.%N)
  n=$(grep -c '^\[\[package' "$ws/Cargo.lock")
  if diff <(grep -v '^#' "$ws/Cargo.lock") <(grep -v '^#' "$ws/bob.Cargo.lock") >"$ws/diff.txt"; then r=identical; else r="DIFF $(grep -c '^[<>]' "$ws/diff.txt") lines"; fi
  printf '%-12s packages=%-4s bob=%.2fs cargo=%.2fs %s | %s\n' "$w" "$n" "$(awk "BEGIN{print $t1-$t0}")" "$(awk "BEGIN{print $t2-$t1}")" "$r" "$(cat "$ws/bob.err")"
done; done
