#!/usr/bin/env bash
# Rerun S16 measurements on a quiet machine. Needs ./setup-forgejo.sh (and, for GitLab,
# gitlab/up.sh + gitlab/bootstrap.sh) to have run. Prints JSON lines; both profiles.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
cd "$HERE"
export CGO_ENABLED=0
. /root/scratch-s16/forgejo/env.sh
mkdir -p /root/scratch-s16/worker
export S16_WORKDIR=/root/scratch-s16/worker
go build -o tmp/clonebench ./cmd/clonebench
ID=$(curl -s --cacert "$S16_CA" -u "bobbot:$S16_BOT_PASS" "$S16_FORGEJO_URL/api/v1/repos/alice/cobra" | jq .id)
if [[ "$ID" == null ]]; then
  echo "alice/cobra missing: mirror https://github.com/spf13/cobra into it (see write-up)" >&2; exit 1
fi
echo "# unit tests (fixtures, replay)"; go test -count=1 ./forge/...
echo "# forgejo live"; go test -tags live -count=1 -v -run Live ./forge/forgejo/ 2>&1 | grep -E 'PASS|FAIL|live_test.go'
echo "# [8c/15G] clone bench"; tmp/clonebench -repo alice/cobra -repo-id "$ID" -runs 7
echo "# [2c/4G] clone bench"
systemd-run --quiet --scope -p AllowedCPUs=0-1 -p MemoryMax=4G -- env S16_CA="$S16_CA" S16_BOT_PASS="$S16_BOT_PASS" \
  tmp/clonebench -repo alice/cobra -repo-id "$ID" -runs 7 -work "$S16_WORKDIR"
if [[ -f /root/scratch-s16/gitlab/env.sh ]]; then
  . /root/scratch-s16/gitlab/env.sh
  echo "# gitlab live"; go test -tags live -count=1 -v -run Live ./forge/gitlab/ 2>&1 | grep -E 'PASS|FAIL|live_test.go'
fi
