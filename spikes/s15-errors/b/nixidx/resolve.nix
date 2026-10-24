# The ONLY Nix expression backend B uses. It never builds anything and holds no build logic:
# it looks up nixpkgs attributes (bob's package source) and reports their status, so all
# failures become data for bob's Go pre-checks instead of raw Nix evaluation errors.
{ src, system, attrs ? [ ], names ? false }:
let
  pkgs = import src { inherit system; config = { }; overlays = [ ]; };
  lib = pkgs.lib;
  one = name:
    let
      found = builtins.tryEval (lib.attrByPath (lib.splitString "." name) null pkgs);
      p = if found.success then found.value else null;
      isDrv = p != null && (builtins.tryEval (lib.isDerivation p)).value;
      meta = if isDrv then p.meta or { } else { };
      drv = builtins.tryEval (if isDrv then p.drvPath else null);
    in
    if !isDrv then { missing = true; }
    else {
      broken = meta.broken or false;
      insecure = meta.insecure or false;
      knownVulnerabilities = meta.knownVulnerabilities or [ ];
      unfree = !(meta.license.free or true);
      available = meta.available or true;
      version = p.version or "";
    } // (if drv.success then {
      drvPath = drv.value;
      outputs = lib.genAttrs (p.outputs or [ "out" ]) (o: p.${o}.outPath);
    } else { evalRefused = true; });
in
(lib.genAttrs attrs one) // (if names then { __names = builtins.attrNames pkgs; } else { })
