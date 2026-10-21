#!/bin/bash
# gopls through the .bob/bin/gopls shim as the non-root bobtest user (bind and pivot mode), then the shim start overhead.
# Setup (done once, as root): /home/bobtest/s01/{bin/{bobexec,lspc,nix},state,fw,acme/.bob/{env,env-gopls,bin,nsbin}} — see docs/spikes/s01-bobgo.md.
H=/home/bobtest/s01; T=/root/bob/spikes/s01-bobgo/tests/as-bobtest.sh; R=/root/bob/spikes/s01-bobgo/results
rm -f $H/go-calls.log
for mode in bind pivot; do F=""; [ $mode = pivot ] && F="BOBEXEC_FLAGS=-force-pivot"; echo "### $mode"; timeout 200 $T "$F $H/bin/lspc -root $H/acme -- $H/acme/.bob/bin/gopls < $H/acme-tagged.steps" </dev/null 2>&1; done > $R/shim-gopls.txt
{ echo "go subprocesses started by gopls: $(wc -l < $H/go-calls.log)"; echo "with uid_map 0 1000 1: $(grep -c "uid_map=\[ 0 1000 1 \]" $H/go-calls.log)"; grep -o "mntns=[^ ]*" $H/go-calls.log | sort | uniq -c; } > $R/shim-go-subprocesses.txt
for i in 1 2 3; do timeout 200 $T "$H/overhead.sh" </dev/null; done > $R/shim-overhead.txt 2>&1
