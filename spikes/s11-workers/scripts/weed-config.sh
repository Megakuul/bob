#!/usr/bin/env bash
# Write the SeaweedFS static identities config (-s3.config) embedding the shipped IAM policies.
set -euo pipefail
. "$(dirname "$0")/env.sh"
out=${1:?out}
jq -n --arg rak "$ROOT_AK" --arg rsk "$ROOT_SK" \
  --arg wp "$(jq -c . $S11/policies/worker.json)" --arg op "$(jq -c . $S11/policies/orchestrator.json)" '{
  identities: [
    {name: "root", credentials: [{accessKey: $rak, secretKey: $rsk}], actions: ["Admin","Read","List","Tagging","Write"]},
    {name: "wkr",  credentials: [{accessKey: "wkr",  secretKey: "wkrsecret-0123456789"}],  policyNames: ["bob-worker"]},
    {name: "orch", credentials: [{accessKey: "orch", secretKey: "orchsecret-0123456789"}], policyNames: ["bob-orchestrator"]},
    {name: "wide", credentials: [{accessKey: "wide", secretKey: "widesecret-0123456789"}], policyNames: ["bob-orchestrator"]}
  ],
  policies: [ {name: "bob-worker", content: $wp}, {name: "bob-orchestrator", content: $op} ]
}' > "$out"
