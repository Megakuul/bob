#!/usr/bin/env bash
# After gitlab/up.sh is serving: create a root PAT via rails runner (inside up.sh's mount
# namespace) and write /root/scratch-s16/gitlab/env.sh for the live tests.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
ROOT=${S16_GITLAB_ROOT:-/root/scratch-s16/gitlab}
PID=$(cat "$HERE/tmp/gitlab-up.pid")
TOK=glpat-s16$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
nsenter -t "$PID" -m -- bash -c ". $ROOT/gitlab-env.sh; cd \$GL; \$RUBYENV/bin/rails runner \"u=User.find_by_username('root'); t=u.personal_access_tokens.build(name: 's16-root', scopes: [:api, :admin_mode], expires_at: 30.days.from_now, organization: u.organizations.first || Organizations::Organization.first); t.set_token('$TOK'); t.save!; puts 'ok'\"" 2>&1 | grep -v 'missing extensions' | tail -3
cat >"$ROOT/env.sh" <<E2
export S16_GITLAB_URL=https://127.0.0.1:21610 S16_GL_CA=$ROOT/tls/cert.pem S16_GL_ROOT_TOKEN=$TOK
E2
curl -s --cacert "$ROOT/tls/cert.pem" -H "PRIVATE-TOKEN: $TOK" https://127.0.0.1:21610/api/v4/version; echo
