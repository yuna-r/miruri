#!/usr/bin/env bash
set -euo pipefail

MIRURI=${1:-./bin/miruri}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CLEAN_OUT=0
if [[ -n "${MIRURI_SMOKE_OUT:-}" ]]; then
  OUT=$MIRURI_SMOKE_OUT
  rm -rf "$OUT"
  mkdir -p "$OUT"
else
  OUT=$(mktemp -d "${TMPDIR:-/tmp}/miruri-smoke.XXXXXX")
  CLEAN_OUT=1
fi
if [[ $CLEAN_OUT -eq 1 ]]; then
  trap 'rm -rf "$OUT"' EXIT
fi

"$MIRURI" version
"$MIRURI" doctor
"$MIRURI" analyze "$ROOT/fixtures/hello-c"
"$MIRURI" plan --target host "$ROOT/fixtures/hello-c"
"$MIRURI" build --target host --out "$OUT" "$ROOT/fixtures/hello-c"

find "$OUT" -name manifest.json -type f | grep -q .
printf 'Miruri smoke test passed: %s\n' "$OUT"
