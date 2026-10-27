# nixpkgsgen snapshot (ADR 0020 §2): facts about nixpkgs attributes, data only, no build logic.
# Every argument is a string; attrs is a JSON list. result = { attrs = {...}; names = [...] | null; }.
{ src, system, attrs ? "[]", names ? "false" }:
let
  cfg = aliases: { inherit system; overlays = [ ]; config = { allowAliases = aliases; allowUnfree = true; }; };
  pkgs = import src (cfg true);
  plain = import src (cfg false);
  lib = pkgs.lib;
  try = x: d: let r = builtins.tryEval (builtins.deepSeq x x); in if r.success then r.value else d;
  split = s: builtins.filter builtins.isString (builtins.split "\\." s);
  find = path: set:
    if path == [ ] then { ok = true; value = set; }
    else if try (builtins.isAttrs set && builtins.hasAttr (builtins.head path) set) false
    then find (builtins.tail path) set.${builtins.head path}
    else { ok = false; };
  isDrv = x: try (builtins.isAttrs x && (x.type or null) == "derivation") false;
  asList = l: if builtins.isList l then l else [ l ];
  spdx = l: map (x: if builtins.isAttrs x then toString (x.spdxId or x.shortName or "unknown") else toString x) (asList l);
  linux = [ "aarch64-linux" "x86_64-linux" ];
  avail = p: s: try (lib.meta.availableOn (lib.systems.elaborate s) p) false;
  info = attr:
    let
      found = find (split attr) pkgs;
      p = found.value;
      meta = try (p.meta or { }) { };
      vulns = try (meta.knownVulnerabilities or [ ]) [ ];
    in
    if !(found.ok && isDrv p) then { drv = false; } else {
      drv = true;
      alias = !(find (split attr) plain).ok;
      version = try (toString (p.version or "")) "";
      description = try (toString (meta.description or "")) "";
      licenses = try (spdx (meta.license or [ ])) [ ];
      mainProgram = try (toString (meta.mainProgram or "")) "";
      outputs = try (map toString (p.outputs or [ "out" ])) [ "out" ];
      platforms = builtins.filter (avail p) linux;
      broken = try (meta.broken or false == true) false;
      insecure = vulns != [ ];
      unfree = try (builtins.any (l: builtins.isAttrs l && (l.free or true) == false) (asList (meta.license or [ ]))) false;
      evalOk = (builtins.tryEval (builtins.deepSeq p.drvPath true)).success;
    };
in
{
  result = {
    attrs = builtins.listToAttrs (map (a: { name = a; value = info a; }) (builtins.fromJSON attrs));
    names = if builtins.fromJSON names then builtins.attrNames pkgs else null;
  };
}
