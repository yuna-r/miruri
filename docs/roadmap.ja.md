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
- [x] first external Codex repair report (`fzy`, synthetic ARM64 blocker)

## v0.2 — Environment and dependency resolver

- [x] sysroot lock and content-addressed store (`v0.1.0-alpha.8`で先行実装)
- [x] trusted provider registry (`v0.1.0-alpha.8`でLinux組み込みproviderを先行実装)
- source dependency rebuild
- SBOM and license closure
- Docker Linux worker
- Parallels/remote Windows worker protocol

## v0.3 — Portability repair

- Clang compilation database capture
- [x] structured diagnostics and warning-flood reduction
- x86 intrinsic classifier
- inline assembly classifier
- ABI/endianness/alignment analysis
- deterministic rewrite rules
- [x] Codex structured response and diagnostic provenance
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


## v0.1.0-alpha.6: Full platform port mode

- `miruri port` / `--codex-mode auto|port`
- 新規platform backend、GUI adapter、target-native entry point、build branchの生成を明示許可
- 元platform backendとfeature parityを維持し、backend新設だけを理由にblockedにしない
- target実行禁止・network禁止・isolated source overlayは従来どおり維持

## v0.1.0-alpha.7: Host-toolchain-resilient tests

- macOS SDKの破損・古いCommand Line Tools設定がGo単体テストを壊さないようにする。
- `miruri doctor` が実際に利用可能なmacOS SDKを検証する。


## v0.1.0-alpha.8: Managed OCI sysroot

- Linux cross targetの`--sysroot`省略時にtrusted providerを自動選択する。
- OCI imageを実行せず、manifest indexからtarget architectureを選択してlayerを展開する。
- manifest/config/layer digestを検証し、whiteoutとsymlink境界を安全に処理する。
- content-addressed store、target lock、offline reuse、explicit refreshを提供する。
- sysroot内GCC runtimeとhost Clang/LLDをCMake/Makeへ自動配線する。
- `sysroot.lock.json`とmanifestへprovider/toolchain provenanceを保存する。
