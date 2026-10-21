#!/usr/bin/env bash
# Write the SeaweedFS advanced IAM config (-s3.iam.config) that enables STS AssumeRole for bob.
set -euo pipefail
. "$(dirname "$0")/env.sh"
out=${1:?out}
key=$(head -c 32 /dev/urandom | base64 -w0)
jq -n --arg key "$key" --argjson wp "$(cat $S11/policies/worker.json)" '{
  sts: {tokenDuration: "1h", maxSessionLength: "12h", issuer: "seaweedfs-sts", signingKey: $key},
  policy: {defaultEffect: "Deny"},
  roles: [{
    roleName: "bob-worker-session",
    roleArn: "arn:aws:iam::000000000000:role/bob-worker-session",
    trustPolicy: {Version: "2012-10-17", Statement: [{Effect: "Allow",
      Principal: {AWS: "arn:aws:iam::000000000000:user/orch"}, Action: ["sts:AssumeRole"]}]},
    attachedPolicies: ["bob-worker-role"]
  }],
  policies: [{name: "bob-worker-role", document: $wp}]
}' > "$out"
