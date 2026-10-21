#!/usr/bin/env bash
# Reproduces backend B's fault catalogue. Writes faults/out/<name>.bob.txt (what bob prints by default)
# and faults/out/<name>.raw.txt (what raw Nix shows for the same fault, first 15 lines).
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
gen=$(cd "$here/../.." && pwd)
bob="$gen/b/bob-b"
out="$here/out"; work="$here/work"
rm -rf "$out" "$work"; mkdir -p "$out" "$work"
export XDG_CACHE_HOME=${XDG_CACHE_HOME:-/root/.cache-b}
base="$gen/fixture.graph.json"
NP=github:NixOS/nixpkgs/cd4ea8d57de16f4810bc5f14207bac45fbf19f67

mk() { jq "$2" "$base" > "$work/$1.json"; }
runbob() { "$bob" build -graph "$work/$1.json" -target "${2:-//api:api}" > "$out/$1.bob.txt" 2>&1; echo "exit=$?" >> "$out/$1.bob.txt"; }
raw() { local n=$1; shift; ( "$@" ) 2>&1 | sed 's/\x1b\[[0-9;]*m//g' | head -15 > "$out/$n.raw.txt"; }
# raw Nix error messages (level 0) of a bob build, as Nix itself would print them
rawbuild() { "$bob" build -v -graph "$work/$1.json" -target "${2:-//api:api}" 2>&1 | grep '^@nix .*"level":0' | sed 's/^@nix //' | jq -r '.msg // empty' | sed 's/\x1b\[[0-9;]*m//g' | head -15 > "$out/$1.raw.txt"; }

# 1 unknown nixpkgs attribute
mk unknown-attr '(.packages[]|select(.label=="//libfoo:foo")|.nixpkgs)=["opensll"]'; runbob unknown-attr
raw unknown-attr nix build --no-link "$NP#opensll"

# 2 wrong fetch hash (a random but well-formed sha256)
mk wrong-hash '(.packages[]|select(.label=="//third_party:itoa-1.0.18.crate")|.fetch.hash)="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="'; runbob wrong-hash
raw wrong-hash nix build --no-link --impure --expr 'import <nix/fetchurl.nix> { url = "https://static.crates.io/crates/itoa/itoa-1.0.18.crate"; hash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="; }'

# 3 missing link dependency (rcore removed from api deps)
mk missing-dep '(.packages[]|select(.label=="//api:api")|.deps)|=map(select(.!="//rcore:rcore"))'; runbob missing-dep; rawbuild missing-dep

# 3b link error: libfoo no longer defines the symbol api calls
mkdir -p "$work/link-root"; cp -r "$gen/fixture" "$work/link-root/fixture"
sed -i 's/int foo_sha256_hex(const char \*msg, char out\[65\]) {/int foo_sha256_hex_v2(const char *msg, char out[65]) {/' "$work/link-root/fixture/libfoo/src/foo.cpp"
mk link-error ".root=\"$work/link-root\""; runbob link-error; rawbuild link-error

# 4 C++ compile error
mkdir -p "$work/cxx-root"; cp -r "$gen/fixture" "$work/cxx-root/fixture"
sed -i 's/return 0;/return 0  /' "$work/cxx-root/fixture/libfoo/src/foo.cpp"
mk cxx-error ".root=\"$work/cxx-root\""; runbob cxx-error //libfoo:foo; rawbuild cxx-error //libfoo:foo

# 5 dependency cycle (hand-edited JSON, bypasses ir.Validate)
mk cycle '(.packages[]|select(.label=="//libfoo:foo")|.deps)=["//api:api"]'; runbob cycle

# 6 broken and insecure nixpkgs packages (real ones at this rev)
mk broken '(.packages[]|select(.label=="//libfoo:foo")|.nixpkgs)+=["aranym"]'; runbob broken
raw broken nix build --no-link "$NP#aranym"
mk insecure '(.packages[]|select(.label=="//libfoo:foo")|.nixpkgs)+=["bzip2_1_1"]'; runbob insecure
raw insecure nix build --no-link "$NP#bzip2_1_1"

# 7 Go toolchain too old for the module (what a nixpkgs bump to 25.11 would hit)
mk go-too-new '(.packages[]|select(.label=="//api:api")|.go.goVersion)="1.27"'; runbob go-too-new

echo "results in $out"
