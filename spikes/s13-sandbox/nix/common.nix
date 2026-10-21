# S13 spike only (throwaway). bob itself never writes .nix files; these probes use builtins.derivation
# with pinned store paths so they test the sandbox, not any generation strategy.
rec {
  bb = builtins.storePath "/nix/store/2fx2paimlr8h2yvh7q70v201d4w9m6p6-busybox-1.37.0";
  bash = builtins.storePath "/nix/store/0m1nl6fyb2zn779if6aaswmbwmmdnlwr-bash-interactive-5.3p9";
  cu = builtins.storePath "/nix/store/3l7ygqcc6v0rbi9d4j1m6phi6sz86j98-coreutils-9.11";
  pg = builtins.storePath "/nix/store/wwl3gqm1rvyhck4n38hffa1q26mxbk2g-postgresql-17.11";
  loopsvc = builtins.path { path = ./loopsvc; name = "loopsvc"; };
  stamp = toString builtins.currentTime;
  sh = name: script: derivation {
    name = "${name}-${stamp}";
    system = "aarch64-linux";
    builder = "${bash}/bin/bash";
    args = [ "-c" script ];
    PATH = "${cu}/bin:${bb}/bin";
  };
}
