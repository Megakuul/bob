#!/bin/sh
# Run a command as bobtest with a clean environment: no Go, gopls or Nix on PATH.
exec su - bobtest -c "env -i HOME=/home/bobtest USER=bobtest PATH=/usr/bin:/bin TERM=dumb $*"
