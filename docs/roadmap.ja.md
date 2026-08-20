# Miruri Roadmap

## v0.1 — Artifact-only bootstrap

- [x] Project Graph
- [x] embedded target profiles
- [x] data-driven domain detection packs
- [x] CMake / Make adapters
- [x] isolated source overlay
- [x] optional Codex repair loop
- [x] ELF / Mach-O / PE / archive inspection
- [x] host artifact fixture
- [x] macOS/Linux CI
- [ ] first external project report (`fzy`)

## v0.2 — Environment and dependency resolver

- sysroot lock and content-addressed store
- trusted provider registry
- source dependency rebuild
- SBOM and license closure
- Docker Linux worker
- Parallels/remote Windows worker protocol

## v0.3 — Portability repair

- Clang compilation database capture
- structured diagnostics
- x86 intrinsic classifier
- inline assembly classifier
- ABI/endianness/alignment analysis
- deterministic rewrite rules
- Codex task packet schema
- patch export back to source repository

## v0.4 — GUI and content artifacts

- Win32/AppKit/GTK/SDL interaction graph
- resource graph
- shader pipeline contract
- HLSL/GLSL/SPIR-V/MSL provider manifests
- app bundle / Windows resource packaging

## v0.5 — 3D/game providers

- renderer feature contract
- translation provider registry
- frame-loop and input contract
- audio provider adapters
- plugin ABI graph
- persistent save/network data contract

## v0.6 — Behavior validator integration

- external validator protocol
- function island extraction
- semantic comparison policy
- Artifact Assurance integration

## v1.0

- reproducible artifact builds
- provider lockfile
- signed provenance
- native worker validation
- stable Project Graph / Target Contract / Provider APIs
