#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
output="${OUTPUT:-bin/knowledgehub}"
mkdir -p "$(dirname "$output")"
CGO_ENABLED=0 "${GO:-go}" build -mod=readonly -trimpath -ldflags='-s -w' -o "$output" ./cmd/knowledgehub
printf 'Built %s\n' "$output"
