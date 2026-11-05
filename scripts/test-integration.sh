#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
: "${TEST_DATABASE_URL:?Set TEST_DATABASE_URL to a dedicated PostgreSQL database with pg_trgm installed}"
"${GO:-go}" test -race -count=1 ./...
