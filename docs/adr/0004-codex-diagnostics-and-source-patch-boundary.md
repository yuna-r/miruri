# ADR-0004: Error-focused Codex diagnostics and a source-only repair boundary

## Status

Accepted for `v0.1.0-alpha.5`.

## Context

A Codex repair agent may run compiler and linker commands to validate a proposed
port. For Make projects those commands commonly write object files, executables,
libraries and dependency caches directly into the source tree. Capturing every
Git change therefore produces oversized, non-reviewable repair patches.

Compiler output can also contain hundreds of warnings from SDK or system
headers. Sending an arbitrary raw-log tail to Codex wastes context and can hide
the actual build failure.

## Decision

Miruri applies two independent boundaries:

1. The diagnostic reducer selects compiler/linker errors, nearby source context,
   the relevant command and final build-system failure lines. It records counts
   for suppressed warnings and writes both `diagnostics.txt` and
   `diagnostics.json`. The complete raw output remains in `build.log`.
2. The authoritative repair patch accepts textual source and build-script
   changes. Compiled artifacts, binary changes, generated metadata, caches and
   Miruri-specific note files are restored or removed before Git creates
   `repair.patch`.

Assumptions and remaining risks belong in Codex's structured response rather
than in files added to the target project. Miruri revalidates workspace symlinks
after every Codex run.

## Consequences

- Repair patches remain reviewable and reusable.
- Codex validation builds may run without polluting source provenance.
- Diagnostic context is bounded and reproducible.
- Binary assets are intentionally outside the automatic source-repair boundary.
  A future content/provider subsystem must handle such changes explicitly.
- If Codex changes only discarded files, Miruri rejects the repair as having no
  accepted source or build-script change.
