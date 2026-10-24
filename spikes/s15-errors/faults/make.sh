#!/usr/bin/env bash
# S15 fault catalogue: captures Nix's internal-json stream for every fault with each Nix version
# into fixtures/<fault>/<nix-version>/{stream.log,meta.json}.
#   faults/make.sh                 # all faults, both Nix versions
#   VERS=2.34.9 faults/make.sh cxx-error go-error   # selected faults
set -uo pipefail
S=$(cd "$(dirname "$0")/.." && pwd); cd "$S"
export XDG_CACHE_HOME=/root/.cache-s15
export NIX_CONFIG=$'cores = 4\nmax-jobs = 2'
W=$S/tmp/work; ST=/root/scratch-s15/stores; mkdir -p "$W" "$ST"
base=$S/fixture.graph.json
VERS=${VERS:-"2.34.9 2.34.8"}
only=" $* "
REV=cd4ea8d57de16f4810bc5f14207bac45fbf19f67
want() { [ "$only" = "  " ] || [[ "$only" == *" $1 "* ]]; }
# tok FILE V: copy of a graph with @TOK@ replaced by a unique token (so success cases rebuild per version)
tok() { local f=$1 o="${1%.json}.$2.json"; sed "s/@TOK@/$2-$(date +%s%N)/g" "$f" > "$o"; echo "$o"; }
targs() { local v=$1; shift; local a=(); while [ $# -gt 0 ]; do if [ "$1" = -graph ]; then a+=(-graph "$(tok "$2" "$v")"); shift 2; else a+=("$1"); shift; fi; done; printf '%s\0' "${a[@]}"; }
cap() { local n=$1; shift; for v in $VERS; do rm -rf "fixtures/$n/$v"; mapfile -d '' A < <(targs "$v" "$@"); ./s15 capture -name "$n" -nix "$v" "${A[@]}"; done; }
# capture into a fresh chroot store per version: capst NAME ARGS… (store path in $STORE)
capst() { local n=$1; shift; for v in $VERS; do rm -rf "fixtures/$n/$v"; chmod -R u+w "$ST/$n-$v" 2>/dev/null; rm -rf "$ST/$n-$v";
  mapfile -d '' A < <(targs "$v" "$@"); ./s15 capture -name "$n" -nix "$v" -store "local?root=$ST/$n-$v" "${A[@]}"; done; }
variant() { rm -rf "$W/$1"; mkdir -p "$W/$1"; cp -r "$S/fixture" "$W/$1/fixture"; }
gv() { jq "$2" "$base" > "$W/$1.json"; }                       # graph variant of the fixture
at() { grep -n "step.Script(\"$1\"" "$S/fixture/tools/bob.go" | awk -F: '{c=index($0,"step.Script"); print "fixture/tools/bob.go:"$1":"c-length($1)-1}'; }
# spkg NAME SHELL BODY [EXTRA-JSON]   one script package
spkg() { jq -cn --arg l "//tools:$1" --arg at "$(at "$1")" --arg sh "$2" --arg body "$3" --argjson x "${4:-null}" \
  '($x//{}) as $x | {label:$l,kind:"script",definedAt:$at,script:({shell:$sh,body:$body}+($x.script//{}))}+($x|del(.script))'; }
sgraph() { local n=$1; shift; printf '%s\n' "$@" | jq -s --arg root "$S" --arg rev $REV \
  '{version:1,system:"aarch64-linux",nixpkgs:{rev:$rev},root:$root,packages:.}' > "$W/$n.json"; }
fake() { printf 'sha256-%s' "$(printf '%s' "$1" | sha256sum | xxd -r -p | base64)"; }

# ---------- definition errors (Go pre-checks; Nix never runs) ----------
want unknown-attr && { gv unknown-attr '(.packages[]|select(.label=="//libfoo:foo")|.nixpkgs)=["opensll"]'; cap unknown-attr -graph "$W/unknown-attr.json" -target //libfoo:foo; }
want broken-pkg && { gv broken-pkg '(.packages[]|select(.label=="//libfoo:foo")|.nixpkgs)+=["aranym"]'; cap broken-pkg -graph "$W/broken-pkg.json" -target //libfoo:foo; }
want insecure-pkg && { gv insecure-pkg '(.packages[]|select(.label=="//libfoo:foo")|.nixpkgs)+=["bzip2_1_1"]'; cap insecure-pkg -graph "$W/insecure-pkg.json" -target //libfoo:foo; }
want cycle && { gv cycle '(.packages[]|select(.label=="//libfoo:foo")|.deps)=["//api:api"]'; cap cycle -graph "$W/cycle.json" -target //api:api; }
want go-too-new && { gv go-too-new '(.packages[]|select(.label=="//api:api")|.go.goVersion)="1.27"'; cap go-too-new -graph "$W/go-too-new.json" -target //api:api; }

# ---------- success ----------
want ok-fixture && cap ok-fixture -graph "$base" -target //api:api

# ---------- user code ----------
want cxx-error && { variant cxx-error; sed -i 's/return 0;/return 0  /' "$W/cxx-error/fixture/libfoo/src/foo.cpp"
  gv cxx-error ".root=\"$W/cxx-error\""; cap cxx-error -graph "$W/cxx-error.json" -target //libfoo:foo; }
want c-error && { variant c-error; mkdir -p "$W/c-error/fixture/cbad/src" "$W/c-error/fixture/cbad/include"
  printf 'int cbad(void);\n' > "$W/c-error/fixture/cbad/include/cbad.h"
  printf '#include "cbad.h"\n\nint cbad(void) {\n    return undeclared_counter + 1;\n}\n' > "$W/c-error/fixture/cbad/src/cbad.c"
  printf '//go:build ignore\n\npackage cbad\n\nimport "github.com/megakuul/bob/pkg/cc"\n\nvar Cbad = cc.StaticLibrary("cbad", cc.Opts{Lang: cc.C17, Srcs: []string{"src/cbad.c"}, Hdrs: "include"})\n' > "$W/c-error/fixture/cbad/bob.go"
  gv c-error ".root=\"$W/c-error\" | .packages += [{label:\"//cbad:cbad\",kind:\"cc_library\",definedAt:\"fixture/cbad/bob.go:7:12\",src:{path:\"fixture/cbad\"},cc:{lang:\"c\",std:\"c17\",srcs:[\"src/cbad.c\"],hdrs:\"include\",shared:false,lib:\"cbad\"}}]"
  cap c-error -graph "$W/c-error.json" -target //cbad:cbad; }
want go-error && { variant go-error; printf '\nfunc broken() int { return "not a number" }\n' >> "$W/go-error/fixture/api/cmd/api/main.go"
  gv go-error ".root=\"$W/go-error\""; cap go-error -graph "$W/go-error.json" -target //api:api; }
want rust-error && { variant rust-error; printf '\npub fn broken() -> u32 {\n    "not a number"\n}\n' >> "$W/rust-error/fixture/rcore/src/lib.rs"
  gv rust-error ".root=\"$W/rust-error\""; cap rust-error -graph "$W/rust-error.json" -target //rcore:rcore; }
want rust-missing-crate && { variant rust-missing-crate; sed -i '2i use serde::Serialize;' "$W/rust-missing-crate/fixture/rcore/src/lib.rs"
  gv rust-missing-crate ".root=\"$W/rust-missing-crate\""; cap rust-missing-crate -graph "$W/rust-missing-crate.json" -target //rcore:rcore; }
want link-error && { variant link-error; sed -i 's/int foo_sha256_hex(const char \*msg, char out\[65\]) {/int foo_sha256_hex_v2(const char *msg, char out[65]) {/' "$W/link-error/fixture/libfoo/src/foo.cpp"
  gv link-error ".root=\"$W/link-error\""; cap link-error -graph "$W/link-error.json" -target //api:api; }
want missing-header && { gv missing-header '(.packages[]|select(.label=="//api:api")|.deps)|=map(select(.!="//rcore:rcore"))'
  cap missing-header -graph "$W/missing-header.json" -target //api:api; }
want missing-lib && { variant missing-lib; sed -i 's/#cgo LDFLAGS: -lfoo -lrcore/#cgo LDFLAGS: -lfoo -lrcore -lzstd_s15/' "$W/missing-lib/fixture/api/cmd/api/main.go"
  gv missing-lib ".root=\"$W/missing-lib\""; cap missing-lib -graph "$W/missing-lib.json" -target //api:api; }
want test-failure && { variant test-failure
  printf 'package main\n\nimport "testing"\n\nfunc TestDigest(t *testing.T) {\n\tif got, want := sha256Hex("bob"), "0000"; got != want {\n\t\tt.Fatalf("sha256Hex(bob) = %%s, want %%s", got, want)\n\t}\n}\n' > "$W/test-failure/fixture/api/cmd/api/main_test.go"
  gv test-failure ".root=\"$W/test-failure\" | (.packages[]|select(.label==\"//api:api\")|.go.test)=true"; cap test-failure -graph "$W/test-failure.json" -target //api:api; }
want two-failures && { variant two-failures; sed -i 's/return 0;/return 0  /' "$W/two-failures/fixture/libfoo/src/foo.cpp"
  printf '\npub fn broken() -> u32 {\n    "not a number"\n}\n' >> "$W/two-failures/fixture/rcore/src/lib.rs"
  gv two-failures ".root=\"$W/two-failures\""; cap two-failures -graph "$W/two-failures.json" -target //api:api; }

# ---------- fetch ----------
crate='(.packages[]|select(.label=="//third_party:itoa-1.0.18.crate")|.fetch)'
want hash-mismatch && { gv hash-mismatch "$crate.hash=\"$(fake s15-hash)\""; cap hash-mismatch -graph "$W/hash-mismatch.json" -target //rcore:rcore; }
want fetch-404 && { gv fetch-404 "$crate.hash=\"$(fake s15-404)\" | $crate.url=\"https://static.crates.io/crates/itoa/itoa-0.0.0-s15.crate\""; cap fetch-404 -graph "$W/fetch-404.json" -target //rcore:rcore; }
want fetch-dns && { gv fetch-dns "$crate.hash=\"$(fake s15-dns)\" | $crate.url=\"https://crates.s15.invalid/itoa-1.0.18.crate\""; cap fetch-dns -graph "$W/fetch-dns.json" -target //rcore:rcore; }
want fetch-refused && { gv fetch-refused "$crate.hash=\"$(fake s15-ref)\" | $crate.url=\"http://127.0.0.1:21599/itoa-1.0.18.crate\""; cap fetch-refused -graph "$W/fetch-refused.json" -target //rcore:rcore; }
want fetch-500 && { ./s15cache -dir "$W" -addr 127.0.0.1:21504 -mode 500 2>/dev/null & P=$!; sleep 0.5
  gv fetch-500 "$crate.hash=\"$(fake s15-500)\" | $crate.url=\"http://127.0.0.1:21504/itoa-1.0.18.crate\""; cap fetch-500 -graph "$W/fetch-500.json" -target //rcore:rcore; kill $P; }
want fetch-stall && { ./s15cache -dir "$W" -addr 127.0.0.1:21503 -mode stall 2>/dev/null & P=$!; sleep 0.5
  gv fetch-stall "$crate.hash=\"$(fake s15-stall)\" | $crate.url=\"http://127.0.0.1:21503/itoa-1.0.18.crate\""
  cap fetch-stall -graph "$W/fetch-stall.json" -target //rcore:rcore -nixarg --option -nixarg stalled-download-timeout -nixarg 3 -nixarg --option -nixarg download-attempts -nixarg 2; kill $P; }
want fetch-ok && { sgraph fetch-ok "$(jq -c '.packages[]|select(.label=="//third_party:itoa-1.0.18.crate")' "$base")"
  capst fetch-ok -graph "$W/fetch-ok.json" -nixarg --option -nixarg substituters -nixarg ''; }

# ---------- builder / sandbox / limits (script packages, root store) ----------
want build-timeout && { sgraph build-timeout "$(spkg sleep bash 'echo start; sleep 30; touch $out')"; cap build-timeout -graph "$W/build-timeout.json" -nixarg --timeout -nixarg 3; }
want timeout-keep-going && { sgraph timeout-keep-going "$(spkg sleep bash 'echo start @TOK@; sleep 30; touch $out')" "$(spkg silent bash 'echo quick @TOK@; touch $out')" "$(spkg user bash 'touch $out' '{"deps":["//tools:sleep"]}')"
  cap timeout-keep-going -graph "$W/timeout-keep-going.json" -nixarg --timeout -nixarg 3; }
want silent-timeout && { sgraph silent-timeout "$(spkg silent bash 'echo start; sleep 30; touch $out')"; cap silent-timeout -graph "$W/silent-timeout.json" -nixarg --max-silent-time -nixarg 3; }
want sandbox-network && { sgraph sandbox-network "$(spkg network bash 'exec 3<>/dev/tcp/1.1.1.1/80; touch $out')"; cap sandbox-network -graph "$W/sandbox-network.json"; }
want sandbox-nochroot && { sgraph sandbox-nochroot "$(spkg nochroot bash 'touch $out' '{"script":{"noChroot":true}}')"; cap sandbox-nochroot -graph "$W/sandbox-nochroot.json"; }
want builder-syntax && { sgraph builder-syntax "$(spkg syntax bash 'if then fi')"; cap builder-syntax -graph "$W/builder-syntax.json"; }
want builder-missing && { sgraph builder-missing "$(spkg missing-builder sh 'true' '{"script":{"builder":"/nix/store/00000000000000000000000000000000-bob-builder/bin/bob-builder"}}')"; cap builder-missing -graph "$W/builder-missing.json"; }
want no-output && { sgraph no-output "$(spkg no-output bash 'echo done')"; cap no-output -graph "$W/no-output.json"; }
want disallowed-ref && { sgraph disallowed-ref "$(spkg base sh 'echo base-s15 > $out')" "$(spkg leak-ref sh 'echo $DEP_0 > $out' '{"deps":["//tools:base"],"script":{"disallowedRefs":["//tools:base"]}}')"
  cap disallowed-ref -graph "$W/disallowed-ref.json" -target //tools:leak-ref; }
want unsupported-system && { sgraph unsupported-system "$(spkg cross sh 'true' '{"script":{"system":"x86_64-linux"}}')"; cap unsupported-system -graph "$W/unsupported-system.json"; }
want log-limit && { sgraph log-limit "$(spkg log-flood sh 'i=0; while [ $i -lt 5000 ]; do echo "line $i of a very chatty build step"; i=$((i+1)); done; echo ok > $out')"
  cap log-limit -graph "$W/log-limit.json" -nixarg --option -nixarg max-build-log-size -nixarg 20000; }
want oom && { sgraph oom "$(spkg memory bash 's=$(head -c 3000000000 /dev/zero | tr "\0" x); echo ${#s} > $out')"
  cap oom -graph "$W/oom.json" -wrap "systemd-run --scope -q -p MemoryMax=300M -p MemorySwapMax=0 -p OOMPolicy=continue --"
  # default OOMPolicy=stop: systemd stops the whole scope with SIGTERM after the kernel OOM kill
  cap oom-scope-stop -graph "$W/oom.json" -wrap "systemd-run --scope -q -p MemoryMax=300M -p MemorySwapMax=0 --"; }
want sigint && { sgraph sigint "$(spkg sleep bash 'echo start; sleep 30; touch $out')"; cap sigint -graph "$W/sigint.json" -sigint 4s; }
want sigkill && { sgraph sigkill "$(spkg sleep bash 'echo start; sleep 30; touch $out')"; cap sigkill -graph "$W/sigkill.json" -sigkill 4s; }
want post-build-hook && { mkdir -p "$W/hook"; printf '#!/bin/sh\necho "uploading $OUT_PATHS"\necho "upload failed: 503" >&2\nexit 1\n' > "$W/hook/hook.sh"; chmod +x "$W/hook/hook.sh"
  sgraph post-build-hook "$(spkg hook sh 'echo hooked-@TOK@ > $out')"; cap post-build-hook -graph "$W/post-build-hook.json" -nixarg --option -nixarg post-build-hook -nixarg "$W/hook/hook.sh"; }
want ok-structured && { sgraph ok-structured "$(spkg structured sh 'echo plain line; echo "@nix {\"action\":\"setPhase\",\"phase\":\"bob:compile\"}"; echo "@nix {\"action\":\"msg\",\"level\":0,\"msg\":\"builder says error\"}"; echo "@nix {\"action\":\"start\",\"id\":7,\"type\":0,\"text\":\"bob step\",\"level\":3,\"parent\":0,\"fields\":[]}"; echo "@nix {\"action\":\"result\",\"id\":7,\"type\":101,\"fields\":[\"nested\"]}"; echo "@nix {\"action\":\"stop\",\"id\":7}"; echo "bob:: {\"event\":\"diag\",\"file\":\"src/a.c\",\"line\":3}"; echo @TOK@ > $out')"
  cap ok-structured -graph "$W/ok-structured.json"; }
want lock-wait && { for v in $VERS; do tok=$(date +%s%N); sgraph lock-wait "$(spkg lock sh "echo locking $tok; i=0; while [ \$i -lt 600000 ]; do i=\$((i+1)); done; echo done > \$out")"
    rm -rf "fixtures/lock-wait/$v" "fixtures/lock-wait-first/$v"; ./s15 capture -name lock-wait-first -nix "$v" -graph "$W/lock-wait.json" & sleep 1.5
    ./s15 capture -name lock-wait -nix "$v" -graph "$W/lock-wait.json"; wait; done; }
want store-permission && { sgraph store-permission "$(spkg hook sh 'echo perm-@TOK@ > $out')"
  # bobtest can't read /root, so it runs a copy of the Nix binary from its home
  mkdir -p /home/bobtest/s15; for v in $VERS; do cp -L nixbin/$v/nix /home/bobtest/s15/nix-$v; chmod 755 /home/bobtest/s15 /home/bobtest/s15/nix-$v
    rm -rf "fixtures/store-permission/$v"; mapfile -d '' A < <(targs "$v" -graph "$W/store-permission.json")
    ./s15 capture -name store-permission -nix "$v" "${A[@]}" -wrap "runuser -u bobtest --" -nixexe /home/bobtest/s15/nix-$v -env XDG_CACHE_HOME=/home/bobtest/cache-s15 -env HOME=/home/bobtest; done; }
want upstream-fail && { NPSRC=$(jq -r .src "$XDG_CACHE_HOME/bob-b/index-${REV:0:12}-aarch64-linux.json")
  sgraph upstream-fail "$(spkg upstream bash 'hello > $out' '{"nixpkgs":["hello-s15"]}')"
  cap upstream-fail -graph "$W/upstream-fail.json" -inject "hello-s15=(import $NPSRC { system = \"aarch64-linux\"; }).hello.overrideAttrs (o: { pname = \"hello-s15\"; postPatch = \"echo checking s15 >&2; false\"; })"; }

# ---------- cold store: everything substituted from cache.nixos.org (catalogue samples) ----------
want ok-cold && { capst ok-cold -graph "$base" -target //api:api -copydrvs; }

# ---------- store (chroot stores) ----------
want disk-full && { for v in $VERS; do d=/root/scratch-s15/small-$v; mkdir -p $d; mountpoint -q $d || mount -t tmpfs -o size=24m,mode=755 tmpfs $d
  rm -rf "fixtures/disk-full/$v"; sgraph disk-full "$(spkg diskfull sh 'i=0; s=xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; s=$s$s$s$s$s$s$s$s$s$s; while [ $i -lt 70000 ]; do echo $s; i=$((i+1)); done > $out')"
  ./s15 capture -name disk-full -nix "$v" -graph "$W/disk-full.json" -store "local?root=$d/store"; df -h $d | tail -1; umount $d; done; }
want corrupt-store && { sgraph corrupt-prep "$(spkg base sh 'echo base-s15 > $out')"; sgraph corrupt-store "$(spkg base sh 'echo base-s15 > $out')" "$(spkg user sh 'read x < $DEP_0; echo $x > $out' '{"deps":["//tools:base"]}')"
  for v in $VERS; do st=$ST/corrupt-$v; chmod -R u+w $st 2>/dev/null; rm -rf $st "fixtures/corrupt-store/$v" "fixtures/corrupt-prep/$v"
    ./s15 capture -name corrupt-prep -nix "$v" -graph "$W/corrupt-prep.json" -store "local?root=$st"
    p=$(jq -r '.stdout[0]' "fixtures/corrupt-prep/$v/meta.json"); chmod -R u+w "$st$p"; rm -rf "$st$p"; echo "removed $st$p"
    ./s15 capture -name corrupt-store -nix "$v" -graph "$W/corrupt-store.json" -target //tools:user -store "local?root=$st"; done; }

# ---------- substituters (bob-configured binary cache on 127.0.0.1:2150x) ----------
subst_prep() {
  [ -f "$W/cache/key.sec" ] && [ -d "$W/cache/bin" ] && return
  rm -rf "$W/cache"; mkdir -p "$W/cache/bin"
  nix key generate-secret --key-name s15-cache-1 > "$W/cache/key.sec"; nix key convert-secret-to-public < "$W/cache/key.sec" > "$W/cache/key.pub"
  nix key generate-secret --key-name other-1 | nix key convert-secret-to-public > "$W/cache/other.pub"
  sgraph payload "$(spkg payload sh 'i=0; while [ $i -lt 3000 ]; do echo "payload line $i"; i=$((i+1)); done > $out')"
  st=$ST/producer; chmod -R u+w $st 2>/dev/null; rm -rf $st
  ./s15 capture -name subst-producer -nix 2.34.9 -graph "$W/payload.json" -store "local?root=$st" -out "$W/prodfix"
  p=$(jq -r '.stdout[0]' "$W/prodfix/subst-producer/2.34.9/meta.json")
  NIX_REMOTE="local?root=$st" nix store sign -k "$W/cache/key.sec" "$p"
  NIX_REMOTE="local?root=$st" nix copy --to "file://$W/cache/bin?compression=none" "$p"
  printf 'machine 127.0.0.1 login bob password wrong\n' > "$W/cache/netrc.bad"
  printf 'machine 127.0.0.1 login bob password s3cret\n' > "$W/cache/netrc.good"
}
sub() { echo -nixarg --option -nixarg substituters -nixarg "$1" -nixarg --option -nixarg trusted-public-keys -nixarg "$(cat "${2:-$W/cache/key.pub}")" -nixarg --option -nixarg netrc-file -nixarg "${3:-$W/cache/netrc.good}" -nixarg --option -nixarg narinfo-cache-negative-ttl -nixarg 0; }
nolocal="-nixarg --option -nixarg max-jobs -nixarg 0"
server() { ./s15cache -dir "$W/cache/bin" -addr "127.0.0.1:$1" -auth bob:s3cret -mode "$2" 2>"$W/cache/server-$1.log" & SP=$!; sleep 0.5; }
if want ok-substitute || want subst-401 || want subst-401-fallback || want subst-nar404 || want subst-refused || want subst-stall || want subst-untrusted || want subst-corrupt; then
  subst_prep
  want ok-substitute && { server 21510 ok; capst ok-substitute -graph "$W/payload.json" $(sub http://127.0.0.1:21510) $nolocal; kill $SP; }
  want subst-401 && { server 21511 ok; capst subst-401 -graph "$W/payload.json" $(sub http://127.0.0.1:21511 "" "$W/cache/netrc.bad") $nolocal; kill $SP; }
  want subst-401-fallback && { server 21512 ok; capst subst-401-fallback -graph "$W/payload.json" $(sub http://127.0.0.1:21512 "" "$W/cache/netrc.bad"); kill $SP; }
  want subst-nar404 && { server 21513 nar404; capst subst-nar404 -graph "$W/payload.json" $(sub http://127.0.0.1:21513) $nolocal; kill $SP; }
  want subst-refused && { capst subst-refused -graph "$W/payload.json" $(sub http://127.0.0.1:21598) $nolocal; }
  want subst-stall && { server 21514 stall; capst subst-stall -graph "$W/payload.json" $(sub http://127.0.0.1:21514) $nolocal -nixarg --option -nixarg stalled-download-timeout -nixarg 3 -nixarg --option -nixarg connect-timeout -nixarg 3; kill $SP; }
  want subst-untrusted && { server 21515 ok; capst subst-untrusted -graph "$W/payload.json" $(sub http://127.0.0.1:21515 "$W/cache/other.pub") $nolocal; kill $SP; }
  want subst-corrupt && { server 21516 corrupt; capst subst-corrupt -graph "$W/payload.json" $(sub http://127.0.0.1:21516) $nolocal; kill $SP; }
fi
echo done
