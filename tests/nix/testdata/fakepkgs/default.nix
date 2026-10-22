# A miniature nixpkgs for the index tests (R-NIX-027): the attribute shapes resolve.nix reads
# (meta flags, outputs, nested sets) and one attribute whose evaluation aborts.
{ system, config ? { }, overlays ? [ ] }:
let
  lib = rec {
    isDerivation = x: builtins.isAttrs x && (x.type or null) == "derivation";
    splitString = sep: s: builtins.filter builtins.isString (builtins.split sep s);
    attrByPath = path: default: set:
      if path == [ ] then set
      else if builtins.isAttrs set && builtins.hasAttr (builtins.head path) set
      then attrByPath (builtins.tail path) default set.${builtins.head path}
      else default;
    genAttrs = names: f: builtins.listToAttrs (map (n: { name = n; value = f n; }) names);
  };
  mk = name: meta: derivation {
    inherit name system;
    builder = "/bin/sh";
    args = [ "-c" "echo ${name} > $out" ];
    outputs = [ "out" "dev" ];
  } // { inherit meta; version = "1.0"; };
in
{
  inherit lib;
  hello = mk "hello" { };
  broken-pkg = mk "broken-pkg" { broken = true; };
  insecure-pkg = mk "insecure-pkg" { insecure = true; knownVulnerabilities = [ "CVE-2099-0001" ]; };
  unfree-pkg = mk "unfree-pkg" { license = { free = false; }; };
  wrong-system = mk "wrong-system" { available = false; };
  sub.inner = mk "inner" { };
  bad = (mk "bad" { }) // { drvPath = abort "this package cannot be evaluated"; };
}
