# Miruri portability repair policy

The executable policy is implemented by `internal/codex`. This document exists
for contributors reviewing changes to the repair contract.

## Invocation

Miruri invokes Codex non-interactively with:

```text
codex
  --ask-for-approval never
  exec
  --ephemeral
  --json
  --sandbox workspace-write
  --output-schema <attempt>/response-schema.json
  --output-last-message <attempt>/final.json
  -
```

The prompt is passed on stdin so compiler diagnostics do not appear in process
arguments. Each run receives a copied source overlay backed by a disposable Git
repository.

## Required task context

- target ID, OS, architecture, triple and object format
- build system
- Miruriがerror中心に圧縮したcompiler/linker diagnostics（raw logはartifact setに保持）
- repair attempt number
- artifact-only policy

## Hard constraints

- never edit the original repository
- reject symlinks that escape the copied overlay
- never use QEMU, Wine, Rosetta or another compatibility runner
- never execute target binaries, target tests or target configure probes
- preserve public APIs, protocols, file formats, data layouts and behavior
- preserve optimized architecture paths behind feature guards
- prefer portable C/C++ fallbacks
- never silently disable GUI, graphics, shaders, audio, input, networking,
  plugins or assets
- preserve copyright and license notices
- do not fetch network resources
- assumptionとriskはstructured responseだけへ記録し、対象projectへMiruri固有note fileを追加しない

## Result contract

Codex must return a JSON object with:

- `status`: `repaired`, `blocked` or `no-change`
- `summary`
- `changed_files` (advisory only)
- `assumptions`
- `remaining_risks`

Miruri independently derives the authoritative changed-file set and source patch
from Git. Object files, executables, libraries, caches, generated build metadata,
binary changes and legacy `MIRURI_REPAIR_NOTES.md` are restored or removed before
the patch is accepted. A `repaired` response with no accepted source/build-script
diff is rejected. The compiler, linker and artifact inspector, not Codex's
response, determine whether the repair succeeded.

## Output contract

Codexの最終messageはJSON Schemaで次のfieldsへ固定されます。

- `status`: `repaired` / `blocked` / `no-change`
- `summary`
- `changed_files`（参考値。最終値はMiruriがGit diffから確定）
- `assumptions`
- `remaining_risks`

Miruriは`diagnostics.txt`、`diagnostics.json`、`events.jsonl`、`stderr.log`、`final.json`、`result.json`、`repair.patch`をrepair attemptごとに保存します。採用しなかったbuild生成物は`discarded_changes`へ理由付きで残します。
