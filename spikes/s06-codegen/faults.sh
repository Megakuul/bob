#!/usr/bin/env bash
# Fault catalogue for codegen packages: each case breaks one input of a copy of the fixture,
# runs bob-s06 and records what the user sees. Output: tmp/out/faults.txt
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd); cd "$here"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-s06} BOB_TMP="$here/tmp"
export NIX_CONFIG=${NIX_CONFIG:-$'cores = 4\nmax-jobs = 2'}
F=tmp/faults; rm -rf "$F"; cp -r fixture "$F"; mkdir -p tmp/out
reset() { cp "fixture/$1" "$F/$1"; }
{
echo "== F1 proto syntax error"; sed -i 's/string name = 1;/string name = 1/' $F/proto/greet/v1/greet.proto; ./bob-s06 gen -root $F; echo "exit=$?"; reset proto/greet/v1/greet.proto
echo "== F2 unknown import"; sed -i 's|common/v1/common.proto|common/v1/nope.proto|' $F/proto/greet/v1/greet.proto; ./bob-s06 gen -root $F; echo "exit=$?"; reset proto/greet/v1/greet.proto
echo "== F3 graphql schema error"; echo 'type Broken { x: Nope }' >> $F/schema/schema.graphqls; ./bob-s06 gen -root $F; echo "exit=$?"; reset schema/schema.graphqls
echo "== F4 gqlgen runtime mismatch"; sed -i 's|github.com/99designs/gqlgen v0.17.90|github.com/99designs/gqlgen v0.17.89|' $F/svc/go.mod; ./bob-s06 gen -root $F; echo "exit=$?"; reset svc/go.mod
echo "== F5 protobuf-es runtime mismatch"; jq '.packages["node_modules/@bufbuild/protobuf"].version="2.10.0"' fixture/web/package-lock.json > $F/web/package-lock.json; ./bob-s06 gen -root $F; echo "exit=$?"; reset web/package-lock.json
echo "== F6 openapi spec error"; sed -i 's|#/components/schemas/Item|#/components/schemas/Itm|' $F/schema/items.yaml; ./bob-s06 gen -root $F; echo "exit=$?"; reset schema/items.yaml
echo "== F7 resolver does not match schema (compile error in user code)"; sed -i 's/hello(name: String!): Hello!/hello(name: String!, polite: Boolean): Hello!/' $F/schema/schema.graphqls; ./bob-s06 build -root $F //svc:bin; echo "exit=$?"; reset schema/schema.graphqls
} 2>&1 | tee tmp/out/faults.txt
