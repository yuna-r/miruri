# ADR-0002: Artifact-only validation before emulation

- Status: Accepted
- Date: 2026-08-21

## Context

Rosetta, QEMU and other emulation layers do not perfectly reproduce every CPU instruction, feature probe, memory-ordering behavior, driver, graphics API or platform service. Emulation can both create false failures and hide real portability defects.

## Decision

Miruri v0.1 does not execute target artifacts. It generates, links, packages and statically inspects them.

## Consequences

- build success is not reported as runtime or behavioral validation
- `manifest.json` records an explicit Artifact Assurance level
- native/remote validation is a separate future layer
- target executables discovered during configure/build must not be used as probes in artifact-only mode
