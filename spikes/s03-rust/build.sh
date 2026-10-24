#!/usr/bin/env bash
# Builds the spike binaries into tmp/.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p tmp
CGO_ENABLED=0 go build -o tmp/bob-rs ./cmd/bob-rs
CGO_ENABLED=0 go build -trimpath -ldflags=-s -o tmp/bob-builder ./cmd/bob-builder
