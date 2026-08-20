# Miruri portability repair policy

The executable policy is implemented by `internal/codex`. This document exists
for contributors reviewing changes to the repair contract.

## Invocation

Miruri invokes Codex non-interactively with:

```text
codex exec
  --ephemeral
  --json
  --sandbox workspace-write
  --ask-for-approval never
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
- latest failed compiler/linker diagnostics
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
- record material assumptions in `MIRURI_REPAIR_NOTES.md`

## Result contract

Codex must return a JSON object with:

- `status`: `repaired`, `blocked` or `no-change`
- `summary`
- `changed_files` (advisory only)
- `assumptions`
- `remaining_risks`

Miruri independently derives the authoritative changed-file set and binary-safe
patch from Git. A `repaired` response with no actual diff is rejected. The
compiler, linker and artifact inspector, not Codex's response, determine whether
the repair succeeded.

## Output contract

Codexの最終messageはJSON Schemaで次のfieldsへ固定されます。

- `status`: `repaired` / `blocked` / `no-change`
- `summary`
- `changed_files`（参考値。最終値はMiruriがGit diffから確定）
- `assumptions`
- `remaining_risks`

Miruriは`events.jsonl`、`stderr.log`、`final.json`、`result.json`、`repair.patch`をrepair attemptごとに保存します。
