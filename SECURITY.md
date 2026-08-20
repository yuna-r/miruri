# Security Policy

## Supported versions

Miruri is currently pre-1.0. Security fixes are applied to the latest development branch and latest tagged alpha release.

## Reporting

公開issueへexploit detailsやcredentialを投稿しないでください。repository ownerへprivate security advisoryまたは非公開連絡手段で報告してください。

## Threat model

Miruri processes untrusted source trees and may invoke compilers, build scripts and optionally Codex CLI. Build scripts are executable code.

Current v0.1 protections:

- source is copied into an isolated workspace before Codex repair
- symlinks escaping the isolated workspace are rejected before Codex starts
- every repair is captured against a disposable Git checkpoint
- Codex uses `workspace-write` sandbox and never uses dangerous bypass flags
- API-key environment variables are removed in the default ChatGPT-auth policy
- explicit emulator/compatibility-runner commands observed in JSONL are rejected
- target artifacts are not executed
- output includes runtime-unvalidated status
- binary-only dependencies are blockers until reviewed

Current v0.1 limitations:

- local CMake/Make configuration can execute host-side project scripts
- Miruri does not yet provide an OS sandbox for build tools
- network access is not centrally filtered
- dependency downloads are not yet content-pinned

Use disposable Docker/VM workers for untrusted projects until the worker sandbox is implemented.
