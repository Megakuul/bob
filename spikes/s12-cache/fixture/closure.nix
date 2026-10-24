# Test closure for S12 (spike tooling only; bob itself writes no Nix). ~100 MiB of NAR in 4
# locally built paths + one upstream reference (hello), which bobup must skip (R-REG-005).
{ salt ? "0", bigMiB ? 1024 }:
let
  pkgs = (builtins.getFlake "github:NixOS/nixpkgs/cd4ea8d57de16f4810bc5f14207bac45fbf19f67").legacyPackages.${builtins.currentSystem};
  rc = name: cmd: pkgs.runCommand "s12-${name}-${salt}" { } cmd;
in rec {
  rand = rc "rand" "mkdir $out; head -c 41943040 /dev/urandom > $out/rand.bin";
  text = rc "text" "mkdir $out; head -c 31457280 /dev/urandom | base64 -w 76 > $out/text.txt";
  many = rc "many" ''
    mkdir -p $out/d; echo ${rand} ${text} > $out/refs
    for i in $(seq 1 2000); do head -c 7680 /dev/urandom | base64 > $out/d/f$i.txt; done
  '';
  top = rc "top" ''
    mkdir -p $out/bin; printf '#!/bin/sh\n# %s\nexec %s/bin/hello "$@"\n' ${many} ${pkgs.hello} > $out/bin/s12-top
    chmod +x $out/bin/s12-top
  '';
  big = rc "big" "mkdir $out; head -c ${toString (bigMiB * 1048576)} /dev/urandom > $out/big.bin";
  hello = pkgs.hello;
}
