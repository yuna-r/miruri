# Third-party components

Miruri v0.1 runtime has no third-party Go module dependencies. It uses the Go standard library.

External tools may be invoked when installed by the user:

- Git
- Clang / LLVM / LLD
- CMake
- Ninja
- Make
- Codex CLI
- platform SDK tools

These tools are not bundled in this repository and remain subject to their respective licenses and installation terms.

The `fixtures/hello-c` source is original Miruri test material and is covered by the repository MPL-2.0 license.

Candidate external test projects are not vendored. Their repository licenses remain independent.
