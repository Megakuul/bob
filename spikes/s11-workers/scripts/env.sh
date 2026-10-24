# Shared settings for the S11 scripts. Source me.
S11=/root/bob/spikes/s11-workers
export XDG_CACHE_HOME=/root/.cache-s11
RUSTFS_BIN=$S11/.tools/dl-rustfs/rustfs           # upstream release 1.0.3 (sha256 28e973c6...)
WEED_BIN=${WEED_BIN:-$S11/.tools/dl-weed/weed}      # upstream 4.48; nixpkgs 4.36 at .tools/seaweedfs/bin/weed
MC=$S11/.tools/mc/bin/mc
AWS=$S11/.tools/awscli2/bin/aws
RUSTFS_PORT=21100          # S3 + admin + STS
WEED_MASTER=21110 WEED_VOLUME=21111 WEED_FILER=21112 WEED_S3=21113
ROOT_AK=s11root ROOT_SK=s11rootsecret-0123456789
export CGO_ENABLED=0 GOFLAGS=-mod=mod
