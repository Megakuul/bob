#!/usr/bin/env bash
# Compare bob-ml's resolver with opam 2.5.1 (dry run, toolchain faked at nixpkgs versions).
set -u
export LC_ALL=C
D=$(cd "$(dirname "$0")/.." && pwd)
export OPAMROOT=$D/tmp/opamroot OPAMYES=1 OPAMNOENVNOTICE=1
O=$D/.tools/opam/bin/opam
graphs=(
  "cmdliner fmt yojson lwt"
  "ppx_deriving ppx_deriving_yojson logs"
  "cohttp-lwt-unix"
  "dream"
  "core core_unix"
  "irmin-fs"
  "ocaml-lsp-server"
  "eio_main"
)
for g in "${graphs[@]}"; do
  t0=$(date +%s.%N)
  $D/bin/bob-ml resolve $g 2>$D/tmp/bob.err | sort >$D/tmp/bob.txt
  t1=$(date +%s.%N)
  $O install --dry-run $g dune.3.21.1 ocamlfind.1.9.8 ocaml-base-compiler.5.4.1 2>/dev/null \
    | sed -n 's/^ *∗ installed \([^ ]*\).*/\1/p; s/^ *- install \([^ ]*\) *\([^ ]*\).*/\1.\2/p' | sort >$D/tmp/opam.txt
  t2=$(date +%s.%N)
  n=$(wc -l <$D/tmp/opam.txt); b=$(wc -l <$D/tmp/bob.txt)
  diff=$(diff $D/tmp/bob.txt $D/tmp/opam.txt | grep '^[<>]' | tr '\n' ' ')
  printf '%-40s bob=%3d opam=%3d bob %.2fs opam %.2fs %s\n' "$g" "$b" "$n" "$(awk "BEGIN{print $t1-$t0}")" "$(awk "BEGIN{print $t2-$t1}")" "${diff:-IDENTICAL}"
  [ -s $D/tmp/bob.err ] && grep -v "packages," $D/tmp/bob.err | head -2
done
