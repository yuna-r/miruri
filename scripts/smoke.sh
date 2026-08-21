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

MANIFEST=$(find "$OUT" -mindepth 2 -maxdepth 2 -name manifest.json -type f | head -n 1)
if [[ -z "$MANIFEST" ]]; then
  printf 'Miruri smoke: manifest was not generated under %s\n' "$OUT" >&2
  exit 1
fi
ARTIFACT_SET=$(dirname "$MANIFEST")

"$MIRURI" verify --strict "$ARTIFACT_SET"
"$MIRURI" build --target host --out "$OUT" --reuse "$ROOT/fixtures/hello-c" | grep -q 'Reused:              true'
"$MIRURI" compare "$ARTIFACT_SET" "$ARTIFACT_SET"
"$MIRURI" matrix --plan-only --targets host,linux-arm64 --jobs 2 --out "$OUT/matrix" "$ROOT/fixtures/hello-c"

test -f "$OUT/matrix/matrix.json"
printf 'Miruri smoke test passed: %s\n' "$OUT"
