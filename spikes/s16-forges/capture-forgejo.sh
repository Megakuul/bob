#!/usr/bin/env bash
# Replays the event sequence that produced fixtures/forgejo/raw: hook -> hookrecv (:21601),
# push main, new branch, PR open, PR sync, lightweight + annotated tag, release, fork PR, merge.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=${S16_FORGEJO_ROOT:-/root/scratch-s16/forgejo}
. "$ROOT/env.sh"
OUT=${1:-$HERE/tmp/capture}
(cd "$HERE" && CGO_ENABLED=0 go build -o tmp/hookrecv ./cmd/hookrecv)
"$HERE/tmp/hookrecv" -dir "$OUT" >"$HERE/tmp/hookrecv.log" 2>&1 & RP=$!
trap 'kill $RP' EXIT
B=https://127.0.0.1:21600/api/v1; C="curl -sf --cacert $S16_CA"; J='content-type: application/json'; H="Authorization: token $S16_OWNER_TOKEN"
$C -H "$H" -X POST $B/repos/alice/demo/hooks -H "$J" -d "{\"type\":\"forgejo\",\"active\":true,\"branch_filter\":\"*\",\"events\":[\"push\",\"create\",\"delete\",\"pull_request\",\"pull_request_sync\",\"release\"],\"config\":{\"url\":\"http://127.0.0.1:21601/hook\",\"content_type\":\"json\",\"secret\":\"$S16_HOOK_SECRET\"}}" >/dev/null
export GIT_SSL_CAINFO=$S16_CA GIT_AUTHOR_NAME=Alice GIT_AUTHOR_EMAIL=alice@s16.local GIT_COMMITTER_NAME=Alice GIT_COMMITTER_EMAIL=alice@s16.local
W=$HERE/tmp/capture-work; rm -rf "$W"; git clone -q "https://alice:$S16_OWNER_TOKEN@127.0.0.1:21600/alice/demo.git" "$W"; cd "$W"
echo 'package main' >main.go; git add .; git commit -qm 'add main.go'; git push -q origin main; sleep 1
git checkout -qb feature; echo '// feature' >>main.go; git commit -qam 'feature 1'; git push -q -u origin feature; sleep 1
$C -H "$H" -X POST $B/repos/alice/demo/pulls -H "$J" -d '{"title":"Feature","head":"feature","base":"main"}' >/dev/null; sleep 1
echo '// feature 2' >>main.go; git commit -qam 'feature 2'; git push -q; sleep 1
git checkout -q main; git tag v0.1.0; git tag -a v0.1.1 -m annotated; git push -q origin v0.1.0; sleep 1; git push -q origin v0.1.1; sleep 1
$C -H "$H" -X POST $B/repos/alice/demo/releases -H "$J" -d '{"tag_name":"v0.1.1","name":"v0.1.1"}' >/dev/null; sleep 1
$C -H "$H" -X PUT $B/repos/alice/demo/collaborators/mallory -H "$J" -d '{"permission":"read"}' >/dev/null
$C -u 'mallory:S16-mallory-pass!' -X POST $B/repos/alice/demo/forks -H "$J" -d '{}' >/dev/null; sleep 1
M=$HERE/tmp/capture-mwork; rm -rf "$M"; git clone -q "https://mallory:S16-mallory-pass!@127.0.0.1:21600/mallory/demo.git" "$M"
(cd "$M" && git checkout -qb evil && echo evil >evil.txt && git add . && git commit -qm evil && git push -q -u origin evil)
$C -u 'mallory:S16-mallory-pass!' -X POST $B/repos/alice/demo/pulls -H "$J" -d '{"title":"Fork PR","head":"mallory:evil","base":"main"}' >/dev/null; sleep 1
$C -H "$H" -X POST $B/repos/alice/demo/pulls/1/merge -H "$J" -d '{"Do":"merge","delete_branch_after_merge":true}' >/dev/null; sleep 3
ls "$OUT"
