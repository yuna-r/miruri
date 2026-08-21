# Changelog

All notable changes to Miruri will be documented here.

## [0.1.0-alpha.5] - 2026-08-21

### Added

- bounded error-focused `diagnostics.txt` and machine-readable `diagnostics.json` for every Codex repair attempt
- source patch boundary with reasoned `discarded_changes` provenance
- Codex CLI automation-option compatibility probe in `miruri codex status` and before repair
- post-Codex symlink boundary revalidation
- first external Codex-assisted repair experiment report using `fzy`
- Linux CI race-detector coverage and a local `make test-race` target

### Changed

- send selected compiler/linker errors and local context to Codex instead of an arbitrary 48 KB log tail
- keep assumptions and remaining risks in structured JSON rather than adding `MIRURI_REPAIR_NOTES.md` to target projects
- accept only textual source/build-script changes in authoritative repair patches

### Fixed

- exclude and restore object files, executables, libraries, caches, generated metadata and binary changes created during Codex validation
- prevent warning floods from dominating Codex context and ChatGPT usage
- reject repair attempts that introduce symlinks escaping the isolated workspace
- correct the fallback CLI version string and remove a duplicate build-context cancel call

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
