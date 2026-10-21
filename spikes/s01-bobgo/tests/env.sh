# shared environment for S1 tests (source me)
S1=/root/bob/spikes/s01-bobgo
export PATH=$S1/.tools/go/bin:$S1/.tools/gopls/bin:/usr/bin:/bin
export GOTOOLCHAIN=local GOFLAGS= GOPROXY=off GOSUMDB=off CGO_ENABLED=0
export GOCACHE=$S1/tmp/gocache GOMODCACHE=$S1/tmp/gomod GOPATH=$S1/tmp/gopath XDG_CACHE_HOME=$S1/tmp/xdg
