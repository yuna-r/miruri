# Changelog

All notable changes to Miruri will be documented here.

## [0.1.0-alpha.7] - 2026-08-21

### Fixed

- Made `go test ./...` independent of the host macOS SDK for artifact inspection by inspecting the Go test executable instead of compiling a throwaway C executable.
- Made the Make/Codex diagnostic compaction E2E fixture produce a static archive so it does not require the host system linker.
- Skip native-link-dependent builder tests when the host C linker is unavailable or the local SDK installation is broken, while preserving the non-linking repair tests.

### Improved

- `miruri doctor` now verifies that `xcrun --sdk macosx --show-sdk-path` resolves to an existing SDK directory on macOS, instead of treating the presence of `xcrun` alone as sufficient.

## [0.1.0-alpha.6] - 2026-08-21

### Added
- `miruri port` for explicitly authorized full platform/backend ports using Codex.
- `--codex-mode repair|auto|port`; `auto` escalates from local repair to full backend generation within the same Codex task when required.
- Codex structured status `ported` and per-attempt mode provenance in manifests.

### Changed
- Full-port/auto prompts may add platform abstraction layers, target-native GUI/backend implementations, resources and coherent multi-file build-system changes while preserving original platform backends and features.
- A missing target backend is no longer considered sufficient reason for Codex to return `blocked` in `auto`/`port` mode.

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
