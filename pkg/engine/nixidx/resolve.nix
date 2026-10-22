# bob's only Nix evaluation (SPEC R-NIX-027): facts about nixpkgs attributes, no build logic.
# Every argument is a string; attrs and permittedInsecure are JSON lists, names and allowUnfree
# JSON booleans. result is data only: { attrs = { <attr> = {...}; }; names = [...] | null; }.
{ src, system, attrs ? "[]", names ? "false", allowUnfree ? "false", permittedInsecure ? "[]" }:
let
  pkgs = import src {
    inherit system;
    config = {
      allowUnfree = builtins.fromJSON allowUnfree;
      permittedInsecurePackages = builtins.fromJSON permittedInsecure;
    };
    overlays = [ ];
  };
  try = x: default: let r = builtins.tryEval x; in if r.success then r.value else default;
  split = s: builtins.filter builtins.isString (builtins.split "\\." s);
  find = path: set:
    if path == [ ] then { ok = true; value = set; }
    else if try (builtins.isAttrs set && builtins.hasAttr (builtins.head path) set) false
    then find (builtins.tail path) set.${builtins.head path}
    else { ok = false; };
  isDrv = x: try (builtins.isAttrs x && (x.type or null) == "derivation") false;
  licenses = l: if builtins.isList l then l else [ l ];
  info = attr:
    let
      found = find (split attr) pkgs;
      p = found.value;
      meta = try (p.meta or { }) { };
      vulns = try (builtins.deepSeq (meta.knownVulnerabilities or [ ]) (meta.knownVulnerabilities or [ ])) [ ];
      drv = builtins.tryEval (builtins.deepSeq p.drvPath p.drvPath);
      outs = try (builtins.deepSeq (p.outputs or [ "out" ]) (p.outputs or [ "out" ])) [ "out" ];
      outPaths = builtins.tryEval (builtins.listToAttrs (map (o: { name = o; value = p.${o}.outPath; }) outs));
      outputs = if drv.success && outPaths.success
        then builtins.deepSeq outPaths.value outPaths.value else { };
    in
    if !(found.ok && isDrv p) then { missing = true; } else {
      missing = false;
      broken = try (meta.broken or false == true) false;
      insecure = try (meta.insecure or (vulns != [ ]) == true) (vulns != [ ]);
      knownVulnerabilities = vulns;
      unfree = try (builtins.any (l: builtins.isAttrs l && (l.free or true) == false) (licenses (meta.license or [ ]))) false;
      available = try (meta.available or true == true) true;
      version = try (toString (p.version or "")) "";
      drvPath = if drv.success then drv.value else null;
      inherit outputs;
      evalRefused = !drv.success;
    };
in
{
  # bob evaluates the attribute "result" (nix eval only auto-calls a file's function then).
  result = {
    attrs = builtins.listToAttrs (map (a: { name = a; value = info a; }) (builtins.fromJSON attrs));
    names = if builtins.fromJSON names then builtins.attrNames pkgs else null;
  };
}
