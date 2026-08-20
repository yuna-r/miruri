# ADR-0001: Go standard-library bootstrap

- Status: Accepted for v0.1
- Date: 2026-08-21

## Context

Miruri is developed primarily on an M1 Mac and jointly evaluated on Linux x86_64. The first milestone is an artifact orchestrator, not a compiler backend.

The bootstrap must provide:

- reliable subprocess control
- filesystem overlays
- JSON schemas and reports
- ELF/Mach-O/PE inspection
- trivial host cross-compilation of the Miruri executable itself
- minimal installation burden

## Decision

Implement the v0.1 orchestrator in Go using only the standard library and `CGO_ENABLED=0`.

## Consequences

Positive:

- single self-contained binaries for macOS ARM64 and Linux x86_64
- no package-manager or native runtime dependency for Miruri itself
- built-in parsers for ELF, Mach-O and PE
- code can be compiled and tested immediately on both collaborators' hosts

Negative:

- deep Clang AST integration is not implemented in Go
- MLIR/LLVM transformations will require a separate service or FFI boundary

Future:

A versioned `miruri-clang-scan` service may be implemented in C++ LibTooling. Performance-sensitive graph/planner components may be moved to Rust without changing the public JSON schemas or worker protocol.
