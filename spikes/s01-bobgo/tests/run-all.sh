#!/bin/bash
# Reruns every S1 verification; output goes to results/*.txt. Needs: bin/ built (see README), bobtest set up (shim.sh).
set -u
S1=/root/bob/spikes/s01-bobgo; . $S1/tests/env.sh; R=$S1/results; mkdir -p $R
tag='{"buildFlags":["-tags=bob"]}'
(cd $S1/examples/acme && go build ./... && go vet ./... && go test ./... && echo "untagged build/vet/test: ok") > $R/untagged-go.txt 2>&1
(cd $S1/examples/acme && $S1/bin/lspc -root . -settings "$tag" -- gopls < $S1/tests/acme-tagged.steps) > $R/gopls-acme-tagged.txt 2>&1
{ for s in '{}' '{"standaloneTags":["ignore","bob"]}'; do echo "## settings $s"; (cd $S1/examples/acme && $S1/bin/lspc -root . -settings "$s" -- gopls < $S1/tests/acme-short.steps); done
  echo "## GOFLAGS=-tags=bob, no settings"; (cd $S1/examples/acme && GOFLAGS=-tags=bob $S1/bin/lspc -root . -- gopls < $S1/tests/acme-short.steps); } > $R/gopls-acme-modes.txt 2>&1
$S1/tests/variants.sh > $R/gopls-variants.txt 2>&1
$S1/bin/bobeval -root $S1/examples/acme -check -timing > $R/eval-acme.json 2> $R/eval-acme.timing
(cd $S1/examples/acme-tree && go build ./... && BOB_TREE=1 $S1/bin/bobeval -root bob | jq -c '.packages[].label' && printf 'open bob/cmd/api/bob.go\ndiag bob/cmd/api/bob.go\ncomplete bob/cmd/api/bob.go "foo."\ndef bob/cmd/api/bob.go "foo.Lib"\nopen cmd/api/main.go\ndiag cmd/api/main.go\ncomplete cmd/api/main.go "greet."\n' | $S1/bin/lspc -root . -- gopls) > $R/fallback-tree.txt 2>&1
