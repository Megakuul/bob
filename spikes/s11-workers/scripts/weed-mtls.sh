#!/usr/bin/env bash
# Create a test CA + shared cert and write tmp/security-mtls.toml (JWT keys + gRPC mTLS).
set -euo pipefail
. "$(dirname "$0")/env.sh"
P=$S11/tmp/pki; mkdir -p $P; cd $P
openssl req -x509 -newkey ed25519 -nodes -keyout ca.key -out ca.crt -days 2 -subj /CN=s11-ca 2>/dev/null
openssl req -newkey ed25519 -nodes -keyout sw.key -out sw.csr -subj /CN=seaweed 2>/dev/null
printf 'subjectAltName=IP:127.0.0.1\nextendedKeyUsage=serverAuth,clientAuth\n' > ext
openssl x509 -req -in sw.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out sw.crt -days 2 -extfile ext 2>/dev/null
{ cat $S11/policies/seaweedfs-security.toml; printf '\n[grpc]\nca = "%s/ca.crt"\n' $P
  for c in volume master filer s3 client admin worker; do printf '[grpc.%s]\ncert = "%s/sw.crt"\nkey = "%s/sw.key"\n' $c $P $P; done; } > $S11/tmp/security-mtls.toml
