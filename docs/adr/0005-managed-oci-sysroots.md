# ADR-0005: Non-executing managed sysroots from trusted OCI providers

- Status: Accepted for `v0.1.0-alpha.8`
- Date: 2026-08-21

## Context

Cross-Linux linking needs target headers, C runtime startup objects, libc, the GCC runtime and C++ standard-library assets. Requiring users to construct each sysroot manually prevents Miruri from functioning as an automatic artifact synthesizer, especially on the primary M1 Mac host.

Running a foreign container, QEMU or the target package manager during provisioning would violate the artifact-only boundary and introduce host/target execution ambiguity. Mutable image tags alone are also insufficient provenance.

## Decision

Miruri uses an embedded trusted provider registry keyed by target profile. A provider identifies an OCI image source and an exact OCI platform. Provisioning:

1. uses OCI Distribution API reads only;
2. performs Bearer-token authentication when required;
3. selects the exact target platform from an image index;
4. verifies manifest, config and layer SHA-256 digests;
5. extracts layers without executing image content;
6. implements OCI whiteouts and rejects paths or relative symlinks that escape the rootfs;
7. rebases absolute image symlinks into equivalent relative links inside the rootfs;
8. does not materialize device nodes or FIFOs;
9. stores the result by immutable manifest digest;
10. records a target reference and `sysroot.lock.json` containing provider and layer provenance.

Target and manifest-digest locks serialize concurrent provisioning. Cached blobs are re-hashed before reuse. An incomplete extracted store is rejected in offline mode and rebuilt from verified blobs during an online operation. Whiteout application preserves entries originating in the current layer even when marker ordering is adversarial.

The first resolved digest remains pinned until an explicit refresh. Offline mode never accesses the registry. Explicit `--sysroot` and `MIRURI_SYSROOT_<TARGET>` overrides retain higher precedence than managed providers.

The initial providers use Docker Official `buildpack-deps` Debian development root filesystems for supported Linux architectures. `linux-riscv32` remains manual-only until a trusted provider with the required GNU runtime is declared.

## Toolchain integration

Miruri discovers host Clang/Clang++, LLVM archive tools and LLD, including common Homebrew LLVM prefixes. It does not fall back to host-format `ar`, `ranlib` or `strip` for foreign Linux targets. It locates the target GCC installation under the rootfs and emits:

- Clang `--target`, `--sysroot`, `--gcc-toolchain` and LLD selection;
- CMake compiler target, sysroot, external toolchain, linker and root-search policy;
- Make `CC/CXX/AR/RANLIB/STRIP` plus cross `pkg-config` environment.

Target executables remain unexecuted. CMake try-compiles are forced to static libraries.

## Consequences

Positive:

- M1 Mac can synthesize supported Linux artifacts without Docker daemon or QEMU.
- sysroot identity is reviewable and reusable offline.
- target runtime search is tied to the downloaded architecture rather than the host filesystem.
- the provider model remains independent of source/target pairwise conversion logic.

Negative:

- first provisioning downloads a development rootfs and can consume substantial network and disk space;
- provider image/package licenses remain third-party obligations;
- mutable provider tags still require deliberate refresh governance;
- zstd-compressed layers require a future standard-library-compatible decoder or an explicitly governed helper.
