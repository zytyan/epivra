#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
project="epivra-test-$$"
cleanup() { docker compose -p "$project" -f compose.test.yaml down --volumes --remove-orphans; }
trap cleanup EXIT
docker compose -p "$project" -f compose.test.yaml up --abort-on-container-exit --exit-code-from tests
