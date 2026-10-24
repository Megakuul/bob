#!/usr/bin/env bash
# Derivation identity across platforms and variants (no build). Writes tmp/matrix/<cfg>.txt
set -euo pipefail
cd "$(dirname "$0")"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s07}
mkdir -p tmp/matrix
declare -A C=(
 [arm64-release]=""
 [arm64-debug]="-profile debug"
 [arm64-asan]="-sanitize address"
 [arm64-asan-ubsan]="-sanitize address,undefined"
 [arm64-race]="-race"
 [arm64-shout]="-features //rdemo:rdemo=shout"
 [amd64-release]="-platform linux-amd64"
 [amd64-release-crosslibs]="-platform linux-amd64 -libs cross"
 [amd64-debug]="-platform linux-amd64 -profile debug"
 [amd64-asan]="-platform linux-amd64 -sanitize address"
 [amd64-shout]="-platform linux-amd64 -features //rdemo:rdemo=shout"
)
for k in "${!C[@]}"; do ./bob-s07 list ${C[$k]} -graph fixture.graph.json | sort > tmp/matrix/$k.txt; done
base=tmp/matrix/arm64-release.txt
printf '%-26s %6s %8s %s\n' config drvs new-vs-arm64-release "new derivations (names)"
for k in $(echo "${!C[@]}" | tr ' ' '\n' | sort); do
  f=tmp/matrix/$k.txt
  new=$(comm -13 <(awk '{print $2}' $base | sort) <(awk '{print $2}' $f | sort) | wc -l)
  names=$(comm -13 <(awk '{print $2}' $base | sort) <(awk '{print $2}' $f | sort) | while read d; do grep -F "$d" $f | awk '{print $1}'; done | sed -E 's/^(go-std|go-example.com|go-golang|go-google|go-github)[^ ]*/\1*/' | sort | uniq -c | awk '{printf "%s%s ", ($1>1?$1"x ":""), $2}')
  printf '%-26s %6s %8s %s\n' $k $(wc -l < $f) $new "$names"
done
echo; echo "distinct derivations over all configs: $(cat tmp/matrix/*.txt | awk '{print $2}' | sort -u | wc -l) (sum of per-config counts: $(cat tmp/matrix/*.txt | wc -l))"
