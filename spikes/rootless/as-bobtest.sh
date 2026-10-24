#!/bin/sh
# Run a command as bobtest with a clean environment (no system Nix on PATH).
exec su - bobtest -c "env -i HOME=/home/bobtest USER=bobtest PATH=/home/bobtest/.local/share/bob/bin:/usr/bin:/bin TERM=dumb $*"
