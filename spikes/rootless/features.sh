#!/bin/sh
# S0b feature probe. Runs INSIDE bobns as bobtest. $1 = nix binary name. Prints labelled results.
N=$1
HELLO=/nix/store/54clm95cr7piy1g6pvsar032v5jxw4wi-hello-2.12.3
BB=/nix/store/kwyc8ijhah0xh443r2vqvfxcqiwd2k13-busybox-static-aarch64-unknown-linux-musl-1.37.0
t() { s=$(date +%s%N); "$@"; r=$?; e=$(date +%s%N); echo "  [rc=$r, $(( (e-s)/1000000 )) ms]"; }
echo "== version"; $N --version | head -1
echo "== F1 substitute hello from cache.nixos.org"; t $N build --no-link $HELLO 2>&1 | tail -2
$HELLO/bin/hello
echo "== F2 substitute static busybox (for sandbox builds)"; t $N build --no-link $BB 2>&1 | tail -1
echo "== F3 sandboxed build of a trivial derivation (sandbox=true)"
cat > /tmp/t-$N.nix <<NIX
let bb = builtins.storePath "$BB"; in
derivation {
  name = "sandbox-probe-$N-\${toString builtins.currentTime}";
  system = "aarch64-linux";
  builder = "\${bb}/bin/sh";
  args = [ "-c" "set -e; PATH=\${bb}/bin; echo uid=\$(id -u) > \$out; (wget -q -T 3 -O- http://1.1.1.1 >/dev/null 2>&1 && echo NET=open || echo NET=blocked) >> \$out; (ls /home >/dev/null 2>&1 && echo HOME=visible || echo HOME=hidden) >> \$out; (ip -o addr 2>/dev/null | grep -o 'inet [0-9.]*' >> \$out) || true" ];
}
NIX
out=$(t $N build --no-link --print-out-paths -f /tmp/t-$N.nix 2>&1); echo "$out" | tail -3; p=$(echo "$out" | grep '^/nix/store' | head -1); [ -n "$p" ] && cat "$p"
echo "== F4 nix derivation add (JSON, output paths left empty for Nix to compute)"
if [ -f /tmp/drvadd-$N.json ]; then t sh -c "$N derivation add < /tmp/drvadd-$N.json 2>&1 | tail -3"; else $N derivation show -f /tmp/t-$N.nix > /tmp/drvshow-$N.json 2>/dev/null; echo "  (drv JSON exported to /tmp/drvshow-$N.json; rerun after transform)"; fi
echo "== F5 --log-format internal-json"
$N build --no-link --log-format internal-json -f /tmp/t-$N.nix --rebuild 2>&1 | head -4 | cut -c1-160
echo "== F6 failing build error"
cat > /tmp/f-$N.nix <<NIX
let bb = builtins.storePath "$BB"; in
derivation { name = "fails"; system = "aarch64-linux"; builder = "\${bb}/bin/sh"; args = [ "-c" "echo compiling foo.c; echo 'foo.c:3: error: SSL_new undeclared' >&2; exit 1" ]; }
NIX
$N build --no-link -f /tmp/f-$N.nix 2>&1 | tail -8
echo "== F7 eval error (missing attribute)"
$N eval --expr '{ a = 1; }.b' 2>&1 | tail -6
echo "== F8 store size"; du -sh /nix/store 2>/dev/null
