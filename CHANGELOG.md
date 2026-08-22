# Changelog

All notable changes to Miruri will be documented here.

## [0.1.0-alpha.9.12] - 2026-08-22

### Added

- Add the macOS/Metal Marble Maze GIF demo to the repository and surface it near the top of the README.
- Document the verified showcase result: Microsoft's Direct3D Marble Maze was ported to Apple Silicon macOS in one Codex attempt (18m47.758s), preserving the original C++ gameplay/asset semantics while generating target-native AppKit/Metal/AVFoundation/GameController integration.

## [0.1.0-alpha.9.11] - 2026-08-22

### Changed

- `miruri port` and `auto` now prefer one persistent local Codex app-server (`ws://127.0.0.1:0`) and reuse a single Codex thread across repair attempts instead of launching a fresh `codex exec` process for every turn.
- Attempt 1 still receives the full Miruri port/fidelity contract; later app-server turns receive a compact delta prompt containing only the newest build diagnostics, continuation state, and live custom instructions. This reduces repeated repository/context reconstruction on long ports.
- The app-server is started lazily only when the first Codex repair is actually required, binds loopback on an automatically selected free port, and is terminated with the Miruri build.
- If the installed Codex CLI cannot start/connect to app-server, Miruri logs the reason and transparently falls back to the existing per-attempt `codex exec` transport.
- App-server output is normalized into the existing Miruri Codex event/provenance format, preserving command auditing, thread/turn IDs, structured response validation, and attempt artifacts.

## [0.1.0-alpha.9.10] - 2026-08-22

- Preserve complete native macOS `.app` bundles, including `Info.plist` and `Contents/Resources`, when collecting linked Mach-O artifacts.
- Preserve the final Codex-modified isolated source tree as `ported-source/` in the published artifact set, so successful ports do not depend on the disposable cache workspace.
- Record and strictly verify `ported_source_dir` in the artifact manifest.

## [0.1.0-alpha.9.9] - 2026-08-22

- Refine the feature-fidelity gate to judge completion by preserved product semantics and observable behavior rather than identical source/class topology.
- Treat source-topology debt such as duplicated target-native orchestration, non-direct reuse of platform-coupled UI/controller classes, and refactoring opportunities as advisory when no concrete behavior/content loss is declared.
- Keep explicit behavior loss blocking: missing states/features, changed scoring/semantics, approximation, substitution, unsupported shipped content, and other observable fidelity gaps still force another attempt.
- Update the Codex port contract so target-native orchestration may faithfully re-express platform-coupled control flow and Codex does not endlessly refactor merely to eliminate structural duplication.

## [0.1.0-alpha.9.8] - 2026-08-22

### Fixed

- Refined full-port completion so a successful Miruri rebuild can promote Codex `progress` to completion when only advisory caveats remain.
- `remaining_risks` is now treated as project-relevant by contract: hypothetical unsupported formats/cases not exercised by shipped project data, optional-hardware absence, runtime-not-executed notes, and optimization opportunities no longer needlessly drive repair loops.
- Codex-side relink/build verification failures caused by isolated-workspace permissions are superseded when Miruri's own subsequent rebuild succeeds.
- A zero-change `ported`/`repaired` response is accepted as a completion reassessment when the immediately preceding Miruri build already linked successfully.
- Added Japanese/English feature-loss recognition and advisory-risk classification for fidelity diagnostics.

## [0.1.0-alpha.9.7] - 2026-08-22

### Changed
- `--instructions-file` is now re-read immediately before every Codex attempt, so editing the file while `miruri build`, `miruri port`, or `miruri matrix` is running affects the next attempt.
- Live instruction files disable artifact reuse because the effective Codex prompt may change during a build.

## [0.1.0-alpha.9.6] - 2026-08-22

### Changed

- Add a built-in Codex repair instruction for every attempt: when an error occurs, do not only fix the line where the error occurred; also check related lines and related parts of other files for the possibility of errors and fix them if necessary. The wording intentionally preserves the broad meaning of “related” rather than constraining Codex to a predefined dependency category.

## [0.1.0-alpha.9.5] - 2026-08-22

### Added

- Add `--instructions "..."` to `build`, `port`, and `matrix` Codex workflows for operator-supplied inline instructions that are injected into every Codex attempt.
- Add `--instructions-file <path>` for UTF-8 instruction files. When both forms are supplied, file contents are applied first and the inline instruction is appended after a blank line.
- Include the custom-instruction content digest in the build request identity so `--reuse` cannot reuse an artifact set produced under different Codex instructions.

### Safety

- Custom instructions are additive to Miruri's target/fidelity/mandatory contracts rather than replacing them, so operator guidance cannot accidentally turn a faithful port back into a placeholder/remake workflow.

## [0.1.0-alpha.9.4] - 2026-08-21

### Fixed

- Make `blocked` and `no-change` non-terminal during `port`/`auto` while Codex retry budget remains. Miruri now records the attempt, feeds the previous summary/risks back as a continuation directive, and requires another implementation attempt instead of aborting after an analysis-only refusal.
- Add structured Codex status `progress` for large faithful ports that need multiple attempts. Incremental source/build changes are retained, rebuilt, and continued without allowing an incomplete `progress` result to pass the final feature-fidelity gate.
- Tighten full-port prompting so broad native backends, many platform-only APIs, and large refactors are explicitly implementation work rather than terminal blockers; `blocked` is reserved for concrete prerequisites outside the workspace/toolchain that prevent any further meaningful local work.
- Add macOS-specific guidance for Windows/UWP/DirectX projects: preserve original C/C++ product/game logic, use Objective-C++ only as thin interop where practical, and port platform services through AppKit/Metal/CoreAudio or AVFoundation/GameController/CoreText/Foundation rather than recreating the application.
- Make `miruri port` default to Codex `port` mode instead of `auto`; explicit user flags can still override it.
- Add a regression test reproducing an initial `blocked` response with zero changed files, then proving Miruri retries with an explicit continuation directive, accepts a later portable build-system implementation, and completes the linked artifact.

## [0.1.0-alpha.9.3] - 2026-08-21

### Fixed

- Add a feature-fidelity gate for `port`/`auto` builds so a newly linked target artifact is not accepted when Codex replaced the original product with a simplified, procedural, placeholder, or parallel reimplementation.
- Capture an immutable original-project baseline before Codex runs and include the original translation-unit and asset/shader inventory in every full-port prompt.
- For CMake/Meson-style builds with `compile_commands.json`, require the target build to compile a minimum set of translation units that existed in the original project; a new target entry point that ignores the original implementation is rejected and fed back into the next Codex attempt.
- Reject successful port claims when Codex itself reports unresolved feature loss such as approximated behavior, undecoded original assets, omitted rendering/audio, placeholders, or other non-preserving substitutions. Runtime-only caveats such as “artifact not executed” remain allowed.
- Strengthen the full-port contract: target-native code is adaptation glue, original domain/game/application logic and content remain authoritative, copied-but-unused assets do not count as preserved, and an unresolvable fidelity gap must return `blocked` instead of a fake `ported` result.
- Add regression coverage where Codex first creates a replacement application from scratch, Miruri rejects it at 0/N original translation-unit reuse, then a later attempt reuses the original implementation and succeeds.

## [0.1.0-alpha.9.2] - 2026-08-21

### Fixed

- Allow `port`/`auto` Codex workflows to bootstrap projects that do not yet have a Miruri-supported portable build system, including Visual Studio/UWP `.sln`/`.vcxproj` source trees.
- Re-detect the build system after every accepted Codex port so a newly generated CMake, Meson, Autotools, or Make build is used on the next build attempt instead of remaining stuck on `unknown`.
- Add a regression test covering an unsupported Visual Studio-style project that is ported to CMake and successfully built.

## [0.1.0-alpha.9.1] - 2026-08-21

### Fixed

- Canonicalize both the artifact-set root and candidate path before symlink-boundary comparison, fixing false `artifact-set path escapes through symlink` failures on macOS where `/var` resolves to `/private/var`.
- Make canonical-path regression expectations filesystem-canonical instead of assuming the lexical temporary-directory prefix is stable across macOS aliases.
- Add regression coverage proving artifact roots reached through symlinked ancestors are accepted while true symlink escapes remain rejected.

## [0.1.0-alpha.9.0] - 2026-08-21

### Added

- Add deterministic, length-framed project-tree SHA-256 fingerprints that exclude timestamps, ownership, absolute paths, VCS state, and generated output directories while retaining relative paths, file content, symlink target text, and execute bits.
- Add build-request digests and 20-character Build IDs covering the resolved target contract, build system, sysroot provenance, generator, Codex policy, dry-run state, and Miruri version.
- Add verified artifact reuse with `build --reuse` and `matrix --reuse`; reuse now requires matching project/request identities and a successful strict verification pass.
- Add conservative `licenses.json` evidence reports and SPDX 2.3 SBOM documents for every artifact set without claiming unresolved transitive dependency closure.
- Add `checksums.sha256` indexes covering artifact-set metadata and packaged files.
- Add `miruri verify` with strict non-executing checks for manifest/analysis/plan/license/SPDX semantic consistency, sysroot lock provenance, path boundaries, symlinks, artifact hashes, sizes, formats, architectures, kinds, dependency metadata, and checksum coverage.
- Add `miruri compare` for categorized identity, target, build, sysroot, toolchain, capability, strategy, license-evidence, and artifact differences.
- Add `miruri matrix` for analyze-once, bounded-parallel multi-target planning and building, fail-fast cancellation, target-prefixed progress, verified reuse, and aggregate `matrix.json` reports.
- Add build-portability, compute, and platform detection rules for native CPU flags, hard-coded architecture flags, target execution probes, CUDA, OpenCL, OpenMP, SVE, RISC-V Vector, WASM SIMD, compiler extensions, packed ABI, Win32/POSIX filesystem/process/virtual-memory/clock APIs, Registry, and COM.
- Add planner strategies for compute backend abstraction, portable vector fallbacks, ABI layout verification, cross-probe cache values, and platform API adapters.
- Add public JSON schemas for license evidence, verification, comparison, and matrix reports, and synchronize the analysis, plan, and manifest schemas with emitted data.
- Add end-to-end CLI lifecycle tests and synchronize public schemas against real generated reports.

### Changed

- Publish successful and dry-run artifact sets transactionally: complete them in `.miruri-staging`, self-verify them, then rename them into the target directory while preserving the previous valid set on failure.
- Keep failed-build diagnostics in staging instead of replacing a previously successful target artifact set.
- Emit stable empty arrays instead of `null` for analysis requirements, graph edges, plan items, and comparison differences.
- Resolve project and output boundaries through existing symlink ancestors, reject output aliases that resolve to the project root, and propagate caller-selected matrix/report exclusions into analysis, isolated source copies, and license scans.
- Emit conservative SPDX package semantics with `filesAnalyzed=false`, a document `DESCRIBES` relationship, and package `CONTAINS` relationships for artifacts and license evidence.
- Reject unreadable or malformed manifest-referenced metadata during artifact-set comparison instead of silently treating it as absent.
- Expand root CLI help and smoke coverage for `verify`, `compare`, `matrix`, and verified reuse.
- Report the release commit as `dev` rather than `dev-dirty` when building from a source archive outside a Git worktree.

### Fixed

- Drain Codex stderr before `Cmd.Wait` closes subprocess pipes, eliminating intermittent `file already closed` failures for fast-exiting Codex CLI processes.

### Security

- Replace delimiter-only fingerprint serialization with length-prefixed binary framing, preventing payload bytes from ambiguously absorbing a following entry header.
- Verify license project identity and SPDX project/artifact checksums and relationships even when a checksum index has been regenerated.
- Reject source-copy and build-output paths that resolve back into unsafe source boundaries through symlinks.

## [0.1.0-alpha.8.9] - 2026-08-21

- Fix macOS interpreted-app bundles so the generated Python entrypoint no longer uses `<launcher>.py`, which could shadow an application package with the same name (for example Drawing's `drawing` package).
- Use `__miruri_bundle_entry__.py` as the private bundle entrypoint and add a regression test for package-shadow avoidance.

## [0.1.0-alpha.8.8] - 2026-08-21

### Fixed

- Minimal macOS Python/GTK bundles now use a runtime shell trampoline that probes Homebrew Python 3.14/3.13 installations and selects an interpreter that can actually import both `gi` and `cairo`.
- PyGObject/PyCairo discovery now covers Homebrew global, `opt`, `libexec`, and versioned `Cellar` site-packages instead of assuming the bundle build Python minor version matches the installed bindings.

## [0.1.0-alpha.8.7] - 2026-08-21

### Fixed

- Minimal macOS Python `.app` launchers now add Homebrew `pygobject3` and `py3cairo` formula-specific site-packages for the running Python major/minor version before importing application modules.
- Finder/open-launched Python/GTK bundles no longer fail with `ModuleNotFoundError: No module named 'gi'` solely because Homebrew keeps PyGObject outside the Python formula's default site-packages.

## [0.1.0-alpha.8.6] - 2026-08-21

### Fixed

- macOS `.app` launcher bundling now rewrites Linux/GNU-specific `locale.bindtextdomain()` and `locale.textdomain()` calls to their portable `gettext` equivalents.
- Python/GTK applications such as Drawing no longer terminate at startup on macOS solely because Python's `_locale` module lacks GNU gettext bindings.

## [0.1.0-alpha.8.5] - 2026-08-21

### Added

- Minimal macOS `.app` bundling for interpreted/resource-only Meson applications staged through `DESTDIR`.
- Bundle-relative Python launcher rewriting for `/usr/local/share` resources, plus `Info.plist` generation and optional GSettings schema compilation.

### Changed

- macOS Meson install-tree fallback now keeps `install-root.tar` and additionally emits a directly usable `.app` directory plus deterministic `.app.tar` package when a launcher can be identified.

## [0.1.0-alpha.8.4] - 2026-08-21

### Added

- Meson `DESTDIR` install-tree fallback for interpreted/resource-only applications that intentionally produce no linked executable or library.
- Deterministic `artifacts/install-root.tar` packaging with normalized tar metadata and SHA-256 provenance.

### Fixed

- Successful Meson builds such as Python/GTK applications are no longer rejected solely because the build directory contains no ELF, Mach-O, PE, or static-library artifact.
- Meson install-fallback output is persisted to `build.log`.

## [0.1.0-alpha.8.3] - 2026-08-21

### Added

- Managed Meson runtime provisioning when `meson` is not already available on PATH.
- SHA-256-pinned Meson 1.12.0 wheel download into the Miruri cache without global `pip` or Homebrew changes.
- Offline reuse of the managed Meson cache plus `MIRURI_MESON` / `MIRURI_PYTHON` overrides.

### Fixed

- `miruri port` no longer invokes Codex merely because the host is missing the Meson executable.

## [0.1.0-alpha.8.2] - 2026-08-21

### Added

- Meson project builds via `meson setup` and `meson compile`.
- Native macOS Meson builds inherit the Apple SDK and Homebrew pkg-config search paths.
- Meson cross-file generation for non-native targets using Miruri's selected LLVM/sysroot toolchain.

## [0.1.0-alpha.8.1] - 2026-08-21

### Added

- Native Autotools build adapter with isolated out-of-tree `configure` + `make` builds.
- Automatic `autoreconf -fi` bootstrap when Git source contains `configure.ac` / `configure.in` but no current `configure` script.
- Autotools cross configuration with `--host=<target-triple>` and `--build=<host-triplet>` when `config.guess` is available.
- macOS Autotools integration for Apple `SDKROOT` / `-isysroot` and Homebrew keg-only `pkg-config` / `aclocal` metadata.

### Changed

- Autotools is preferred over a generated Makefile when both are detected, preventing stale host/target `configure` results from being reused.
- `miruri doctor` now reports `autoreconf` and `pkg-config` availability.

### Fixed

- Autotools projects such as `jubalh/nudoku` are no longer rejected as an unsupported build system.

## [0.1.0-alpha.8] - 2026-08-21

### Added

- Managed cross-Linux sysroots for `linux-x86_64`, `linux-arm64`, `linux-ppc64le` and `linux-riscv64`.
- Direct OCI Registry v2 pull path with Bearer authentication, multi-platform manifest selection and SHA-256 verification of manifests, configs and layers.
- Safe OCI layer extraction with whiteout handling, absolute-symlink rebasing, path-traversal rejection and omission of host device nodes/FIFOs.
- Content-addressed sysroot store, target and manifest-digest concurrency locks, offline reuse and explicit refresh.
- `miruri sysroot providers|ensure|list|path|remove` management commands.
- `--offline`, `--refresh-sysroot`, `--sysroot-timeout`, `--cache-dir` and `MIRURI_CACHE_DIR` controls.
- `sysroot.lock.json` artifact provenance and manifest-level sysroot/toolchain records.

### Changed

- `miruri build` and `miruri port` now provision a trusted managed sysroot automatically when a supported cross-Linux target omits `--sysroot`.
- Clang/LLVM discovery now recognizes standard Apple Silicon and Intel Homebrew LLVM prefixes plus `MIRURI_LLVM_PREFIX`.
- CMake and Make adapters now connect the managed rootfs GCC runtime to Clang using external-toolchain settings, and configure cross `pkg-config` paths.
- Cross-Linux archive/index tools must be LLVM-native; Miruri no longer falls back to host `ar`, `ranlib` or `strip` for foreign artifacts.
- `miruri plan` reports an available managed provider without downloading registry content.
- `miruri doctor` checks both Clang frontends and LLD, and no longer implies Docker is required for managed sysroots.

### Security

- OCI content is never executed while provisioning a sysroot.
- Blob digest mismatch, manifest digest mismatch, archive path traversal and escaping relative symlinks are rejected.
- Cached blobs are re-hashed before reuse, incomplete rootfs stores self-repair online, and OCI whiteouts are order-independent with respect to entries from the same layer.

### Fixed

- Dry-run manifests now serialize `artifacts` as an empty array instead of JSON `null`.

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
