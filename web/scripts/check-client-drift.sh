#!/usr/bin/env bash
# Drift gate: regenerate src/api/generated.ts from ../openapi/openapi.json
# and refuse to proceed if the committed file differs. Mirrors the
# openapi-check gate on the Go side — the OpenAPI spec is the source
# of truth, both the server and client are derived; the two cannot
# silently drift.
set -euo pipefail

cd "$(dirname "$0")/.."

TARGET="src/api/generated.ts"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

npx openapi-typescript ../openapi/openapi.json -o "$TMP" >/dev/null

if ! diff -u "$TARGET" "$TMP" >/dev/null 2>&1; then
  echo "::error::gui client is out of sync with openapi/openapi.json"
  echo "Regenerate it with: cd web && npm run gen:client"
  diff -u "$TARGET" "$TMP" || true
  exit 1
fi

echo "gui-client-check: $TARGET is in sync with openapi/openapi.json"
