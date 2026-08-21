#!/usr/bin/env bash
set -euo pipefail

MIRURI=${1:-./bin/miruri}
for tool in git clang cmake; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'missing required tool: %s\n' "$tool" >&2
    exit 2
  fi
done
if ! command -v codex >/dev/null 2>&1; then
  printf 'Codex CLI is not on PATH\n' >&2
  exit 2
fi

case "$(uname -m)" in
  arm64|aarch64) ;;
  *)
    printf 'codex-smoke is designed for the Apple Silicon/ARM64 development path; host is %s\n' "$(uname -m)" >&2
    exit 2
    ;;
esac

"$MIRURI" codex status --auth chatgpt

ROOT=$(mktemp -d "${TMPDIR:-/tmp}/miruri-codex-smoke.XXXXXX")
PROJECT="$ROOT/project"
OUT="$ROOT/dist"
mkdir -p "$PROJECT"
if [[ "${MIRURI_CODEX_SMOKE_KEEP:-0}" != "1" ]]; then
  trap 'rm -rf "$ROOT"' EXIT
fi

cat > "$PROJECT/CMakeLists.txt" <<'CMAKE'
cmake_minimum_required(VERSION 3.16)
project(miruri_codex_smoke C)
add_executable(miruri-codex-smoke main.c)
CMAKE

cat > "$PROJECT/main.c" <<'C'
#include <stdio.h>

#if defined(__x86_64__) || defined(_M_X64)
static int architecture_value(void) {
    return 42;
}
#else
#error MIRURI_CODEX_SMOKE_NEEDS_A_PORTABLE_NON_X86_FALLBACK
#endif

int main(void) {
    printf("%d\n", architecture_value());
    return 0;
}
C

args=(
  build
  --target host
  --out "$OUT"
  --codex
  --max-repairs 1
  --codex-auth chatgpt
  --codex-timeout "${MIRURI_CODEX_TIMEOUT:-20m}"
)
if [[ -n "${MIRURI_CODEX_MODEL:-}" ]]; then
  args+=(--codex-model "$MIRURI_CODEX_MODEL")
fi
args+=("$PROJECT")

"$MIRURI" "${args[@]}"

python3 - "$OUT" "$PROJECT/main.c" <<'PY'
import json
import pathlib
import sys

out = pathlib.Path(sys.argv[1])
source = pathlib.Path(sys.argv[2]).read_text()
manifests = list(out.glob('*/manifest.json'))
if len(manifests) != 1:
    raise SystemExit(f'expected one manifest, found {len(manifests)}')
manifest = json.loads(manifests[0].read_text())
repairs = manifest.get('codex_repairs', [])
if len(repairs) != 1 or repairs[0].get('status') != 'repaired':
    raise SystemExit(f'unexpected Codex repair provenance: {repairs!r}')
if not manifest.get('artifacts'):
    raise SystemExit('no artifact was produced')
if 'MIRURI_CODEX_SMOKE_NEEDS_A_PORTABLE_NON_X86_FALLBACK' not in source:
    raise SystemExit('the original project was modified')
for field in ('diagnostics_file', 'diagnostics_json_file'):
    path = manifests[0].parent / repairs[0][field]
    if not path.is_file() or path.stat().st_size == 0:
        raise SystemExit(f'missing {field}: {path}')
patch = manifests[0].parent / repairs[0]['patch_file']
if not patch.is_file() or patch.stat().st_size == 0:
    raise SystemExit(f'missing repair patch: {patch}')
patch_text = patch.read_text(errors='replace')
for forbidden in ('MIRURI_REPAIR_NOTES.md', 'GIT binary patch'):
    if forbidden in patch_text:
        raise SystemExit(f'generated/non-source content leaked into repair patch: {forbidden}')
print(f'Miruri Codex smoke test passed: {manifests[0].parent}')
PY

if [[ "${MIRURI_CODEX_SMOKE_KEEP:-0}" == "1" ]]; then
  printf 'Kept smoke workspace: %s\n' "$ROOT"
fi
