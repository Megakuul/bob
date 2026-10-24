# Throwaway test graph for S14's real run. Each node sleeps `work` seconds (stands in for
# compute on a separate machine) and writes `size` bytes. Deps are build inputs only (no
# runtime references), like Go/Rust per-package archives.
{ graphFile, bb }:
let
  g = builtins.fromJSON (builtins.readFile graphFile);
  bbp = builtins.storePath bb;
  drvs = builtins.mapAttrs (n: v: derivation {
    name = "s14-${n}";
    system = builtins.currentSystem;
    builder = "${bbp}/bin/sh";
    args = [ "-c" "for d in $deps; do test -e $d || exit 1; done; ${bbp}/bin/sleep ${v.work}; ${bbp}/bin/head -c ${toString v.size} /dev/urandom > $out" ];
    deps = map (d: drvs.${d}) v.deps;
    salt = g.salt;
  }) g.nodes;
in drvs
