#!/usr/bin/env bash
# Generates every fault variant from ../../fixture.graph.json and records, per fault:
#   raw.txt  – first 25 lines of plain `nix build` output (what a user of raw Nix would see)
#   bob.txt  – what the bob-aprime driver prints (no -v)
# Usage: ./make.sh   (run from anywhere; needs nix, jq, go)
set -u
here=$(cd "$(dirname "$0")" && pwd)
gen=$(cd "$here/../.." && pwd)
lib=$gen/aprime/lib
fix=$gen/fixture.graph.json
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-aprime}
(cd "$gen" && CGO_ENABLED=0 go build -o aprime/bob-aprime ./aprime/cmd/bob-aprime)
bob=$gen/aprime/bob-aprime

mk() { # name target jq-filter [extra driver flags]
  local name=$1 target=$2 filter=$3; shift 3
  mkdir -p "$here/$name"
  jq "$filter" "$fix" > "$here/$name/graph.json"
  nix build -f "$lib/default.nix" --argstr graphFile "$here/$name/graph.json" "packages.\"$target\"" --no-link 2>&1 \
    | sed 's/\x1b\[[0-9;]*[a-zA-Z]//g' | head -25 > "$here/$name/raw.txt"
  "$bob" build -graph "$here/$name/graph.json" -target "$target" -lib "$lib" "$@" > "$here/$name/bob.txt" 2>&1
  echo "exit=$?" >> "$here/$name/bob.txt"
  echo "== $name"; cat "$here/$name/bob.txt"
}

mk unknown-attr //libfoo:foo '(.packages[]|select(.label=="//libfoo:foo")|.nixpkgs)=["opensssl"]'
mk wrong-hash //api:api '(.packages[]|select(.label=="//third_party:uuid-v1.6.0.zip")|.fetch.hash)="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="'
mk missing-link //api:api '(.packages[]|select(.label=="//api:api")|.deps) |= map(select(. != "//rcore:rcore"))'
mk insecure //libfoo:foo '(.packages[]|select(.label=="//libfoo:foo")|.nixpkgs)=["openssl_1_1"]'
mk broken //libfoo:foo '(.packages[]|select(.label=="//libfoo:foo")|.nixpkgs)=["openssl","hyper"]'
# cycle: libfoo depends on api (defined later), api depends on libfoo. ir.Validate catches it; -no-validate shows what Nix does.
mk cycle-validated //api:api '(.packages[]|select(.label=="//libfoo:foo")|.deps)=["//api:api"]'
mk cycle-unvalidated //api:api '(.packages[]|select(.label=="//libfoo:foo")|.deps)=["//libfoo:foo"]' -no-validate

# C++ compile error: same graph, root points at a copy of the fixture with a broken foo.cpp
mkdir -p "$here/cxx-error/root"
rm -rf "$here/cxx-error/root/fixture" && cp -r "$gen/fixture" "$here/cxx-error/root/fixture"
sed -i 's/return 0;/return 0 /' "$here/cxx-error/root/fixture/libfoo/src/foo.cpp"
mk cxx-error //api:api ".root=\"$here/cxx-error/root\""
