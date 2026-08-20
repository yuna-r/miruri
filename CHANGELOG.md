# Changelog

All notable changes to Miruri will be documented here.

## [0.1.0-alpha.4] - 2026-08-21

### Fixed

- invoke Codex approval policy as a top-level CLI option (`codex --ask-for-approval never exec ...`) for compatibility with current Codex CLI releases
- add a regression test that requires the approval option to appear before the `exec` subcommand

## [0.1.0-alpha.3] - 2026-08-21

### Changed

- Miruri Core license changed to MPL-2.0 before public contribution intake
- explicitly designated generated runtime/adapter support code uses Apache-2.0
- license, contribution, generated-code and redistribution policies aligned with the dual-scope model

## [0.1.0-alpha.2] - 2026-08-21

### Added

- Go standard-library-only Miruri CLI
- `analyze`, `plan`, `build`, `inspect`, `doctor`, `targets` and `version` commands
- Project Graph and Capability Requirement reports
- domain packs for core C/ABI, GUI, graphics, shader, audio, input, plugins and assets
- target profiles for macOS, Linux and Windows across x86_64, ARM64, RISC-V and POWER
- CMake and Make artifact builders
- isolated source overlay
- optional constrained Codex CLI repair loop
- ELF, Mach-O, PE and static archive inspection
- JSON schemas for analysis, plan and manifest
- MPL-2.0 core license, Apache-2.0 generated-support policy and DCO contribution policy
- GitHub CI for M1 macOS and Linux
- release workflow for macOS/Linux/Windows ARM64 and x86_64 Miruri binaries
