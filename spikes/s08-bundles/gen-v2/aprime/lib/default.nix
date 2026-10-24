# Entry point of the bob-owned Nix library (strategy A′). Never generated: the only per-project input
# is the graph JSON (data). Called as:
#   nix build -f lib/default.nix --argstr graphFile /abs/graph.json 'packages."//api:api"'
{
  graphFile,
  nixpkgsRev ? null, # override of graph.nixpkgs.rev (bump test)
}:
let
  graph = builtins.fromJSON (builtins.readFile graphFile);
  rev = if nixpkgsRev == null then graph.nixpkgs.rev else nixpkgsRev;
  nixpkgsSrc = builtins.fetchTree {
    type = "github";
    owner = "NixOS";
    repo = "nixpkgs";
    inherit rev;
  };
  pkgs = import nixpkgsSrc {
    system = graph.system;
    config = { };
    overlays = [ ];
  };
in
import ./bob.nix { inherit pkgs graph; }
