#!/bin/sh
# mkshims.sh <project-dir> <node-store-path> <bobns> <state> : stand-in for bob generating .bob/bin shims
# (ADR 0002 Decision 5). Each shim = `bob exec <tool>`: enter the namespace, set the dev env, exec.
P=$1 NODE=$2 BOBNS=$3 STATE=$4
mkdir -p "$P/.bob/bin"
for t in node $(ls "$P/node_modules/.bin"); do
  if [ "$t" = node ]; then target="$NODE/bin/node"; else target="$P/node_modules/.bin/$t"; fi
  cat > "$P/.bob/bin/$t" <<SH
#!/bin/sh
exec $BOBNS -state $STATE -- /bin/sh -c 'PATH=$NODE/bin:\$PATH; exec "\$0" "\$@"' $target "\$@"
SH
  chmod +x "$P/.bob/bin/$t"
done
