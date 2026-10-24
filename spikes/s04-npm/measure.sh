#!/usr/bin/env bash
# S4 measurements. Rerun on a quiet machine:  ./measure.sh 8c  and  ./measure.sh 2c
# (2c runs itself under systemd-run AllowedCPUs=0-1 MemoryMax=4G). Writes tmp/measure-<profile>.txt.
set -euo pipefail
cd "$(dirname "$0")"
PROF=${1:-8c}
if [ "$PROF" = 2c ] && [ -z "${IN_SCOPE:-}" ]; then
  exec systemd-run --scope -q -p AllowedCPUs=0-1 -p MemoryMax=4G -- env IN_SCOPE=1 "$0" 2c "${2:-}"
fi
export XDG_CACHE_HOME=/root/.cache-s04
if [ "$PROF" = 2c ]; then export NIX_CONFIG=$'cores = 2\nmax-jobs = 2'; else export NIX_CONFIG=$'cores = 8\nmax-jobs = 8'; fi
T=$PWD/.tools/time/bin/time
BOB=$PWD/bin/bob-npm
OUT=tmp/measure-$PROF${2:+-$2}.txt
: > "$OUT"
log() { echo "$*" | tee -a "$OUT"; }
secs() { local s=$(date +%s.%N); "$@" > /dev/null 2> tmp/last.err || { tail -20 tmp/last.err; exit 1; }; awk -v a="$s" -v b="$(date +%s.%N)" 'BEGIN{printf "%.2f", b-a}'; }
del() { # delete given store paths (bob's own outputs only) so the next build is cold
  # with everything in the store that refers to them (other spike projects' node_modules)
  local v=() p
  for p in "$@"; do [ -e "$p" ] && v+=("$p"); done
  [ ${#v[@]} -gt 0 ] || return 0
  mapfile -t refs < <(nix-store -q --referrers-closure "${v[@]}" | grep -v "\.drv$")
  nix-store --delete "${refs[@]}" > tmp/del.log 2>&1 || { echo "delete failed:"; tail -2 tmp/del.log; }
}

if [ "${2:-}" != depchange ]; then
log "== resolve ($PROF): cold registry cache / warm (ETag revalidation) / offline cache"
for g in vite-react ts-lib fastify native mono stress; do
  rm -rf tmp/regcache
  c=$(BOB_NPM_CACHE=$PWD/tmp/regcache secs $BOB lock ws/$g)
  w=$(BOB_NPM_CACHE=$PWD/tmp/regcache secs $BOB lock ws/$g)
  o=$(BOB_NPM_CACHE=$PWD/tmp/regcache BOB_NPM_OFFLINE=1 secs $BOB lock ws/$g)
  n=$(jq '.packages|length' ws/$g/bob.lock.json)
  log "$g packages=$n cold=${c}s warm=${w}s offline=${o}s"
done
$BOB lock ws/fastify 2>/dev/null # restore lock used by the builds below (extend)

log "== install: isolated (one drv per package) vs hoisted (one drv), vite-react; tarballs already fetched"
for t in web web-hoisted; do
  mapfile -t built < <($BOB paths ws/vite-react $t built 2>/dev/null)
  del "${built[@]}"
  cold=$(secs $BOB build ws/vite-react $t)
  noop=$(secs $BOB build ws/vite-react $t)
  nm=$($BOB build ws/vite-react $t 2>/dev/null | awk '/:node_modules/{print $2}')
  if [ $t = web ]; then
    size=$(nix path-info -S "$nm" | awk '{printf "%.1f", $2/1048576}'); npaths=$(nix-store -qR "$nm" | grep -c -- '-npm-' || true)
  else
    size=$(nix path-info -s "$nm" | awk '{printf "%.1f", $2/1048576}'); npaths=1
  fi
  log "$t cold(build all package drvs + node_modules + tsc + vite build)=${cold}s noop=${noop}s node_modules=${size}MiB storePaths=$npaths"
done

log "== cold install including downloads (vite-react isolated)"
mapfile -t all < <($BOB paths ws/vite-react web all 2>/dev/null)
del "${all[@]}"
log "web cold+fetch=$(secs $BOB build ws/vite-react web)s"

log "== node_modules only (materialise), vite-react: cold / no-op"
mapfile -t built < <($BOB paths ws/vite-react web built 2>/dev/null)
del "${built[@]}"
log "materialise cold=$(secs $BOB materialise ws/vite-react web)s noop=$(secs $BOB materialise ws/vite-react web)s"

log "== one source file changed -> rebuild"
f=ws/vite-react/src/App.tsx; cp $f tmp/App.tsx.bak
sed -i "s/marker-v1/marker-v1-$RANDOM/" $f
log "vite-react App.tsx changed: isolated=$(secs $BOB build ws/vite-react web)s"
sed -i "s/marker-v1-[0-9]*/marker-v1-$RANDOM/" $f
log "vite-react App.tsx changed: hoisted=$(secs $BOB build ws/vite-react web-hoisted)s"
cp tmp/App.tsx.bak $f
f=ws/ts-lib/src/index.ts; cp $f tmp/index.ts.bak; echo "export const v$RANDOM = 1;" >> $f
log "ts-lib index.ts changed (tsc lib)=$(secs $BOB build ws/ts-lib lib)s"
cp tmp/index.ts.bak $f
f=ws/fastify/src/server.ts; cp $f tmp/server.ts.bak; echo "// $RANDOM" >> $f
log "fastify server.ts changed (tsc + esbuild bundle)=$(secs $BOB build ws/fastify server)s"
cp tmp/server.ts.bak $f

fi
log "== dependency added (clsx) -> relock + rebuild node_modules"
cp ws/vite-react/package.json tmp/pkg.bak
jq '.dependencies.clsx="^2.1.0"' tmp/pkg.bak > ws/vite-react/package.json
mapfile -t before < <({ $BOB paths ws/vite-react web built; $BOB paths ws/vite-react nm-hoisted built; } 2>/dev/null | sort)
r=$(secs $BOB lock ws/vite-react)
mapfile -t after < <({ $BOB paths ws/vite-react web built; $BOB paths ws/vite-react nm-hoisted built; } 2>/dev/null | sort)
mapfile -t new < <(comm -13 <(printf '%s\n' "${before[@]}") <(printf '%s\n' "${after[@]}"))
del "${new[@]}" # earlier runs of this script may have built the clsx variant already
log "derivations that change: $(printf '%s\n' "${new[@]}" | grep -c .)"
i=$(secs $BOB materialise ws/vite-react web)
h=$(secs $BOB build ws/vite-react nm-hoisted)
log "relock=${r}s isolated materialise=${i}s hoisted node_modules rebuild=${h}s"
cp tmp/pkg.bak ws/vite-react/package.json
$BOB lock ws/vite-react 2>/dev/null; $BOB materialise ws/vite-react web >/dev/null 2>&1
log "done"
