#!/usr/bin/env bash
# Verifies that tooling sees materialised code: gopls (check + definition + negative case),
# tsserver (diagnostics + go-to-definition), tsc, and git (generated dirs ignored).
# Run after `bob-s06 gen` on fixture/. Output: tmp/out/verify-ide.txt
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd); cd "$here"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s06} BOB_TMP="$here/tmp"
mkdir -p tmp/out
outs=$(./bob-s06 outs)
proxy=$(awk '$2=="//svc:goproxy"{print $3}' <<<"$outs")
nm=$(awk '$2=="//web:node_modules"{print $3}' <<<"$outs")
T=$here/.tools
{
echo "== gopls $(gopls version | head -1) with GOPROXY=$proxy"
cd fixture/svc
export PATH=$T/go/bin:$PATH GOPROXY=file://$proxy GOFLAGS=-mod=readonly GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=0 \
	GOMODCACHE=$here/tmp/gopls-modcache
echo "-- gopls check (expect no output)"; time gopls check cmd/server/main.go cmd/client/main.go graph/resolver.go rest/impl.go
echo "-- definition of greetv1connect.NewGreetServiceHandler"; gopls definition cmd/server/main.go:43:30 | head -1
echo "-- definition of model.Hello"; gopls definition graph/resolver.go:19:20 | head -1
printf 'package main\n\nimport greetv1 "example.com/svc/gen/proto/greet/v1"\n\nvar _ = greetv1.GreetRequest{Nmae: "typo"}\n' >cmd/client/zz_probe.go
echo "-- type error against generated struct (expect unknown field)"; gopls check cmd/client/zz_probe.go
rm -f cmd/client/zz_probe.go
mv gen/proto/greet "$here/tmp/neg-greet"
echo "-- generated dir removed (expect import errors)"; gopls check cmd/server/main.go 2>&1 | head -2
mv "$here/tmp/neg-greet" gen/proto/greet
cd "$here/fixture/web"
ln -sfn "$nm/node_modules" node_modules
echo "== tsc --noEmit (expect no output)"; time "$T/typescript/bin/tsc" -p . --noEmit
F=$PWD/src/client.ts
L=$(grep -n 'createClient(GreetService' src/client.ts | cut -d: -f1)
C=$(awk -v l="$L" 'NR==l{print index($0,"GreetService,")}' src/client.ts)
echo "== tsserver: diagnostics + definition of GreetService"
(printf '{"seq":1,"type":"request","command":"open","arguments":{"file":"%s"}}\n{"seq":2,"type":"request","command":"semanticDiagnosticsSync","arguments":{"file":"%s"}}\n{"seq":3,"type":"request","command":"definition","arguments":{"file":"%s","line":%d,"offset":%d}}\n' "$F" "$F" "$F" "$L" "$C"
	sleep 25; echo '{"seq":4,"type":"request","command":"exit"}') |
	timeout 60 "$T/nodejs/bin/node" "$T/typescript/lib/node_modules/typescript/lib/tsserver.js" --disableAutomaticTypingAcquisition 2>/dev/null |
	grep -E '"command":"(semanticDiagnosticsSync|definition)"' | cut -c1-300
cd "$here"
echo "== git: generated dirs ignored"
rm -rf tmp/gitcheck; cp -r fixture tmp/gitcheck; rm -rf tmp/gitcheck/web/node_modules
git -C tmp/gitcheck init -q; git -C tmp/gitcheck add -A
echo "files staged under gen/ or .bob/: $(git -C tmp/gitcheck ls-files | grep -cE '(^|/)gen/|^\.bob/')"
git -C tmp/gitcheck check-ignore -v svc/gen/proto/greet/v1/greet.pb.go web/gen/graphql/graphql.ts .bob/gen/manifest.json
} 2>&1 | tee tmp/out/verify-ide.txt
