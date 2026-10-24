# Runs inside bob's namespace (root in userns). Per-job overlay over /nix: the job's writes,
# including any tampering with input paths, land in a throwaway upper layer.
set -e
J=/home/bobtest/s13ov; rm -rf $J; mkdir -p $J/up $J/wk
cd /home/bobtest/s13n
nix eval --raw --impure --expr 'builtins.toFile "s13-victim-fixed" "original\n"' >/dev/null
echo "db size: $(du -sh /nix/var/nix/db | cut -f1)"
t0=$(date +%s%N)
/usr/bin/mount -t overlay overlay -o lowerdir=/nix,upperdir=$J/up,workdir=$J/wk /nix
t1=$(date +%s%N); echo "overlay mount: $(( (t1-t0)/1000 )) us"
out=$(nix build --no-link --print-out-paths -L -f storewrite-fixed.nix 2>&1); echo "$out" | grep -E 'OK|denied|original|poisoned'
t2=$(date +%s%N); echo "build in overlay: $(( (t2-t1)/1000000 )) ms"
v=$(echo "$out" | grep -o '/nix/store/[a-z0-9]*-s13-victim-fixed' | head -1)
echo "inside job view:"; cat $v
echo "upper layer has: $(cd $J/up && find . -type f | head -20 | tr '\n' ' ')"
echo "upper size: $(du -sh $J/up | cut -f1)"
echo "$v" > $J/victim
