# AGENTS.md

## Project goal

Miruri generates target software artifact sets from existing C/C++ projects. It must remain extensible to GUI, graphics, shaders, audio, input, games, plugins and packaging.

## Mandatory invariants

- Never execute target binaries in artifact-only mode.
- Never edit the user's original repository during repair; use the isolated source overlay.
- Never silently disable functionality to make a build pass.
- Preserve public APIs and existing optimized architecture paths behind feature guards.
- Prefer portable C fallbacks before target-specific optimization.
- Do not remove or rewrite license notices.
- Treat binary-only dependencies as unresolved until architecture and license are known.
- Do not add source-target pairwise conversion logic when a Capability/Provider model can express it.

## Validation

Run:

```bash
gofmt -w .
go test ./...
go vet ./...
go build ./cmd/miruri
```

For builder changes, also run:

```bash
./bin/miruri build --target host fixtures/hello-c
```
