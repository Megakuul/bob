#!/usr/bin/env bash
# Start a local GitLab 19.4.2 (nixpkgs) without NixOS: postgres, redis, gitaly, puma,
# sidekiq, workhorse (TLS on 127.0.0.1:21610). Mirrors nixos/modules/services/misc/gitlab.
# Runs everything inside one private mount namespace where /run/gitlab is bound to the
# state dir (the package hard-links config/log/tmp to /run/gitlab/*).
# Usage: gitlab/up.sh   (re-executes itself under unshare; blocks; Ctrl-C/kill to stop)
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
T=$HERE/.tools
ROOT=${S16_GITLAB_ROOT:-/root/scratch-s16/gitlab}
STATE=$ROOT/state
if [[ "${S16_IN_NS:-}" != 1 ]]; then
  mkdir -p "$STATE"
  exec env S16_IN_NS=1 unshare --mount --propagation private -- "$0" "$@"
fi
mkdir -p /run/gitlab
mount --bind "$STATE" /run/gitlab

GL=$T/gitlab/share/gitlab
RUBYENV=$T/gl-rubyenv
GITALY=$T/gitlab-1
WORKHORSE=$T/gitlab-2
SHELL_PKG=$T/gitlab-3
PG=$T/gitlab-4
REDIS=$T/gitlab-5
GIT=$T/gl-rubyenv-1
NODE=$T/gl-rubyenv-2
RUBY=$T/gl-rubyenv-3
REMARSHAL=$T/gl-rubyenv-4
OPENSSL=$T/gl-rubyenv-5-bin
S=/run/gitlab
export PATH=$GIT/bin:$NODE/bin:$RUBY/bin:$PG/bin:$OPENSSL/bin:$REMARSHAL/bin:$PATH

export LANG=C.UTF-8 LC_ALL=C.UTF-8
export HOME=$S/home PUMA_PATH=$S/ GITLAB_PATH=$(readlink -f $GL)/ SCHEMA=$S/db/structure.sql \
  GITLAB_UPLOADS_PATH=$S/uploads GITLAB_LOG_PATH=$S/log prometheus_multiproc_dir=$S/prom \
  RAILS_ENV=production MALLOC_ARENA_MAX=2 FOSS_ONLY=true BUNDLER_VERSION=$($RUBYENV/bin/bundle --version | awk '{print $NF}') \
  GITLAB_ROOT_PASSWORD=${S16_GL_ROOT_PASS:-Qz7vK2pL9xWm4tRb} GITLAB_ROOT_EMAIL=root@s16.local

export -p >$ROOT/gitlab-env.sh
echo "RUBYENV=$RUBYENV GL=$GL" >>$ROOT/gitlab-env.sh
mkdir -p $S/{config,log,tmp/sockets,tmp/pids,uploads,repositories,shell/hooks,builds,shared,home,custom_hooks,db,prom,pg,run-gitaly}
chmod 700 $S/repositories

# ---- postgres (as uid 1000 inside a user namespace: postgres refuses euid 0) ----
PGDATA=$S/pg/data
asuser() { unshare --user --map-user=1000 --map-group=1000 -- "$@"; }
if [[ ! -f $PGDATA/PG_VERSION ]]; then
  asuser env LANG=C LC_ALL=C $PG/bin/initdb --no-locale -E UTF8 -D $PGDATA -U gitlab --auth=trust >$S/log/initdb.log
fi
asuser $PG/bin/postgres -D $PGDATA -k $S/pg -c listen_addresses='' -c port=21612 -c max_locks_per_transaction=1024 -c shared_buffers=256MB >$S/log/postgres.log 2>&1 &
for i in $(seq 1 50); do [[ -S $S/pg/.s.PGSQL.21612 ]] && break; sleep 0.2; done
psql -h $S/pg -p 21612 -U gitlab -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='gitlabhq_production'" | grep -q 1 ||
  psql -h $S/pg -p 21612 -U gitlab -d postgres -c "CREATE DATABASE gitlabhq_production" >/dev/null
psql -h $S/pg -p 21612 -U gitlab -d gitlabhq_production -c "CREATE EXTENSION IF NOT EXISTS pg_trgm; CREATE EXTENSION IF NOT EXISTS btree_gist;" >/dev/null

# ---- redis ----
$REDIS/bin/redis-server --port 0 --unixsocket $S/tmp/sockets/redis.sock --unixsocketperm 700 --dir $S --save '' >$S/log/redis.log 2>&1 &
for i in $(seq 1 50); do [[ -S $S/tmp/sockets/redis.sock ]] && break; sleep 0.2; done
REDIS_URL="unix://$S/tmp/sockets/redis.sock"

# ---- config (what gitlab-config.service does) ----
cp -f $GL/VERSION $S/VERSION
rm -rf $S/db/*; cp -rf --no-preserve=mode $GL/db/* $S/db
find $S/config -maxdepth 1 -mindepth 1 -type d -exec rm -rf {} +
cp -rf --no-preserve=mode $GL/config.dist/* $S/config
printf 'production:\n  adapter: redis\n  url: %s\n  channel_prefix: gitlab_production\n' "$REDIS_URL" >$S/config/cable.yml
printf '{"production":{"url":"%s"}}' "$REDIS_URL" >$S/config/resque.yml
cat >$S/config/database.yml <<EOF
{"production":{"main":{"adapter":"postgresql","database":"gitlabhq_production","host":"$S/pg","port":21612,"username":"gitlab","encoding":"utf8","pool":10},
 "ci":{"adapter":"postgresql","database":"gitlabhq_production","host":"$S/pg","port":21612,"username":"gitlab","encoding":"utf8","pool":10,"database_tasks":false}}}
EOF
[[ -f $ROOT/secrets.env ]] || {
  for k in secret db otp ardet arprim arsalt; do echo "$k=$(openssl rand -hex 32)"; done >$ROOT/secrets.env
  openssl genrsa 2048 2>/dev/null >$ROOT/jws.pem
}
. $ROOT/secrets.env
jq -n --arg s "$secret" --arg d "$db" --arg o "$otp" --rawfile j $ROOT/jws.pem --arg p "${arprim:0:32}" --arg q "${ardet:0:32}" --arg r "${arsalt:0:32}" \
  '{production:{secret_key_base:$s,otp_key_base:$o,db_key_base:$d,openid_connect_signing_key:$j,active_record_encryption_primary_key:[$p],active_record_encryption_deterministic_key:[$q],active_record_encryption_key_derivation_salt:$r}}' >$S/config/secrets.yml
openssl rand -hex 32 >$S/gitlab_shell_secret
[[ -f $S/.gitlab_workhorse_secret ]] || openssl rand -base64 32 >$S/.gitlab_workhorse_secret
[[ -f $S/.gitlab_kas_secret ]] || openssl rand -base64 32 >$S/.gitlab_kas_secret
cat >$S/config/gitlab.yml <<EOF
{"production":{
 "gitlab":{"host":"127.0.0.1","port":21610,"https":true,"user":"root","email_enabled":false,"default_projects_features":{"issues":true,"merge_requests":true,"wiki":false,"snippets":false,"builds":false,"container_registry":false}},
 "repositories":{"storages":{"default":{"path":"$S/repositories","gitaly_address":"unix:$S/tmp/sockets/gitaly.socket"}}},
 "artifacts":{"enabled":false},"lfs":{"enabled":false},"gravatar":{"enabled":false},"cron_jobs":{},
 "gitlab_ci":{"builds_path":"$S/builds"},"ldap":{"enabled":false},"omniauth":{"enabled":false},
 "shared":{"path":"$S/shared"},"gitaly":{"client_path":"$GITALY/bin"},
 "gitlab_shell":{"path":"$(readlink -f $SHELL_PKG)","hooks_path":"$S/shell/hooks","secret_file":"$S/gitlab_shell_secret","upload_pack":true,"receive_pack":true},
 "workhorse":{"secret_file":"$S/.gitlab_workhorse_secret"},"gitlab_kas":{"enabled":false,"secret_file":"$S/.gitlab_kas_secret"},
 "git":{"bin_path":"git"},"monitoring":{"ip_whitelist":["127.0.0.0/8"],"sidekiq_exporter":{"enabled":false},"web_exporter":{"enabled":false}},
 "uploads":{"storage_path":"$S"},"registry":{"enabled":false},"pages":{"enabled":false},"incoming_email":{"enabled":false},
 "packages":{"enabled":false},"dependency_proxy":{"enabled":false},"terraform_state":{"enabled":false},"ci_secure_files":{"enabled":false}
}}
EOF
cat >$S/config/initializers/zz-s16.rb <<'EOF'
# s16: allow webhooks to loopback receivers
EOF
$SHELL_PKG/support/make_necessary_dirs 2>/dev/null || true
git config --global core.autocrlf input

# ---- gitaly ----
cat >$S/config/gitaly.toml <<EOF
socket_path = "$S/tmp/sockets/gitaly.socket"
runtime_dir = "$S/run-gitaly"
bin_dir = "$GITALY/bin"
[git]
bin_path = "$GIT/bin/git"
[gitlab-shell]
dir = "$(readlink -f $SHELL_PKG)"
[hooks]
custom_hooks_dir = "$S/custom_hooks"
[gitlab]
secret_file = "$S/gitlab_shell_secret"
url = "http+unix://$(echo $S/tmp/sockets/gitlab.socket | sed 's|/|%2F|g')"
[gitlab.http-settings]
self_signed_cert = false
[[storage]]
name = "default"
path = "$S/repositories"
EOF
$GITALY/bin/gitaly $S/config/gitaly.toml >$S/log/gitaly.out 2>&1 &
for i in $(seq 1 50); do [[ -S $S/tmp/sockets/gitaly.socket ]] && break; sleep 0.2; done

cd $GL
# ---- db (gitlab-db-config.service) ----
if [[ ! -f $ROOT/db-configured ]]; then
  t0=$(date +%s)
  $RUBYENV/bin/rake -f $GL/Rakefile gitlab:db:configure >$S/log/db-configure.log 2>&1
  echo "db:configure took $(( $(date +%s) - t0 ))s" | tee $ROOT/db-configured
fi

# ---- puma, sidekiq, workhorse ----
$RUBYENV/bin/bundle exec puma -e production -C $S/config/puma.rb -w 0 -t 1:8 >$S/log/puma.out 2>&1 &
PATH=$GL/vendor/gems/sidekiq/bin:$PATH $GL/bin/sidekiq-cluster '*' --concurrency 5 >$S/log/sidekiq.out 2>&1 &
cat >$S/config/workhorse.toml <<EOF
[redis]
URL = "$REDIS_URL"
[[listeners]]
network = "tcp"
addr = "127.0.0.1:21610"
[listeners.tls]
certificate = "$ROOT/tls/cert.pem"
key = "$ROOT/tls/key.pem"
EOF
$WORKHORSE/bin/gitlab-workhorse -listenNetwork unix -listenAddr $S/tmp/sockets/workhorse.socket -listenUmask 0 \
  -authSocket $S/tmp/sockets/gitlab.socket -documentRoot $GL/public -config $S/config/workhorse.toml \
  -secretPath $S/.gitlab_workhorse_secret >$S/log/workhorse.out 2>&1 &
echo "gitlab: started; waiting" >&2
trap 'kill $(jobs -p) 2>/dev/null; wait; umount /run/gitlab' EXIT INT TERM
wait
