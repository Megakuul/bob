#!/usr/bin/env bash
# Start a local Forgejo (nixpkgs, sqlite, TLS on 127.0.0.1:21600) and create the users,
# repositories and tokens the live tests use. Writes env to $ROOT/env.sh.
# Usage: ./setup-forgejo.sh   then ./capture-forgejo.sh to replay the event sequence
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=${S16_FORGEJO_ROOT:-/root/scratch-s16/forgejo}
export XDG_CACHE_HOME=/root/.cache-s16
P=github:NixOS/nixpkgs/cd4ea8d57de16f4810bc5f14207bac45fbf19f67
[[ -e $HERE/.tools/forgejo ]] || nix build -o "$HERE/.tools/forgejo" "$P#forgejo"
F=$HERE/.tools/forgejo/bin/forgejo
mkdir -p "$ROOT/custom/conf" "$ROOT/data" "$ROOT/tls"
[[ -f $ROOT/tls/cert.pem ]] || (cd "$ROOT/tls" && $F cert --host 127.0.0.1,localhost --ca >/dev/null 2>&1)
if [[ ! -f $ROOT/custom/conf/app.ini ]]; then
cat >"$ROOT/custom/conf/app.ini" <<EOF
I_AM_BEING_UNSAFE_RUNNING_AS_ROOT = true
APP_NAME = s16
RUN_MODE = prod
WORK_PATH = $ROOT
[server]
PROTOCOL = https
CERT_FILE = $ROOT/tls/cert.pem
KEY_FILE = $ROOT/tls/key.pem
HTTP_ADDR = 127.0.0.1
HTTP_PORT = 21600
ROOT_URL = https://127.0.0.1:21600/
DISABLE_SSH = true
OFFLINE_MODE = true
LFS_START_SERVER = false
[database]
DB_TYPE = sqlite3
PATH = $ROOT/data/forgejo.db
[repository]
ROOT = $ROOT/repos
[security]
INSTALL_LOCK = true
SECRET_KEY = $($F generate secret SECRET_KEY)
INTERNAL_TOKEN = $($F generate secret INTERNAL_TOKEN)
[oauth2]
JWT_SECRET = $($F generate secret JWT_SECRET)
[service]
DISABLE_REGISTRATION = true
[webhook]
ALLOWED_HOST_LIST = loopback
DELIVER_TIMEOUT = 5
[log]
MODE = console
LEVEL = Info
[actions]
ENABLED = false
[queue]
TYPE = level
EOF
fi
cd "$ROOT"
$F --config custom/conf/app.ini --work-path "$ROOT" migrate >/dev/null
$F --config custom/conf/app.ini --work-path "$ROOT" admin user create --admin --username bobadmin \
  --password 'S16-admin-pass!' --email admin@s16.local --must-change-password=false >/dev/null 2>&1 || true
if ! curl -sf --cacert tls/cert.pem https://127.0.0.1:21600/api/v1/version >/dev/null; then
  nohup $F --config custom/conf/app.ini --work-path "$ROOT" web >forgejo.log 2>&1 &
  echo $! >forgejo.pid
  for i in $(seq 1 60); do curl -sf --cacert tls/cert.pem https://127.0.0.1:21600/api/v1/version >/dev/null && break; sleep 0.5; done
fi
B=https://127.0.0.1:21600/api/v1
C="curl -sf --cacert $ROOT/tls/cert.pem"
J='content-type: application/json'
for u in alice bobbot mallory; do
  $C -u 'bobadmin:S16-admin-pass!' -X POST $B/admin/users -H "$J" \
    -d "{\"username\":\"$u\",\"email\":\"$u@s16.local\",\"password\":\"S16-$u-pass!\",\"must_change_password\":false}" >/dev/null || true
done
[[ -f alice.tok.json ]] || $C -u 'alice:S16-alice-pass!' -X POST $B/users/alice/tokens -H "$J" \
  -d '{"name":"alice-admin","scopes":["write:repository","write:user","write:issue"]}' >alice.tok.json
AT=$(jq -r .sha1 alice.tok.json)
H="Authorization: token $AT"
for r in demo other; do
  $C -H "$H" -X POST $B/user/repos -H "$J" -d "{\"name\":\"$r\",\"private\":true,\"auto_init\":true,\"default_branch\":\"main\"}" >/dev/null || true
done
$C -H "$H" -X PUT $B/repos/alice/demo/collaborators/bobbot -H "$J" -d '{"permission":"write"}' >/dev/null
$C -H "$H" -X PUT $B/repos/alice/other/collaborators/bobbot -H "$J" -d '{"permission":"read"}' >/dev/null
[[ -f hook.secret ]] || head -c 24 /dev/urandom | base64 | tr -d '/+=' >hook.secret
[[ -f bot.tok.json ]] || $C -u 'bobbot:S16-bobbot-pass!' -X POST $B/users/bobbot/tokens -H "$J" \
  -d '{"name":"bob-status","scopes":["write:repository"],"repositories":[{"owner":"alice","name":"demo"}]}' >bot.tok.json
cat >env.sh <<EOF
export S16_FORGEJO_URL=https://127.0.0.1:21600 S16_CA=$ROOT/tls/cert.pem S16_BOT_PASS='S16-bobbot-pass!'
export S16_STATUS_TOKEN=$(jq -r .sha1 bot.tok.json) S16_HOOK_SECRET=$(cat hook.secret) S16_OWNER_TOKEN=$AT
EOF
echo "forgejo ready; source $ROOT/env.sh"
