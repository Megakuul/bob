#!/bin/sh
# Run a command as bobtest inside bob's rootless namespace (S0b bobns) with S13's own bob state.
ST=/home/bobtest/.local/share/bob-s13
exec su - bobtest -c "env -i HOME=/home/bobtest USER=bobtest PATH=$ST/bin:/usr/bin:/bin TERM=dumb $ST/bin/bobns -state $ST -- $*"
