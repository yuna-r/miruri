# Miruri Architecture v0.1

## 1. 目的

Miruriは単なるcross compiler wrapperではありません。

入力はC/C++ sourceだけでなく、build script、GUI resource、shader、asset、plugin、binary dependency、packaging metadataを含む**software project全体**です。出力はobject fileだけでなく、対象platformで配布可能なartifact setです。

## 2. 不変条件

1. artifact-only build中にtarget executableを実行しない
2. host toolとtarget artifactをBuild Realmで分離する
3. source-target APIの組合せごとの変換器を作らない
4. source requirementとtarget providerをCapabilityで接続する
5. transformationは元repositoryではなくisolated overlayで行う
6. build成功とruntime動作保証を混同しない
7. source licenseを勝手に変更しない
8. source identityとbuild request identityを分離して記録する
9. artifact setはstaging上でstrict verificationを通過してから公開する
10. failed buildで以前の正常artifact setを破壊しない

## 3. Project Graph

```go
type ProjectGraph struct {
    Nodes []GraphNode
    Edges []GraphEdge
}
```

主なnode:

- project
- source-unit
- build-step
- dependency
- resource
- shader
- asset
- plugin
- capability-requirement
- generated-artifact
- package

主なedge:

- contains
- depends-on
- compiles-to
- links-to
- generates
- loads-at-runtime
- requires
- provides
- packages
- signs

v0.1 analyzerはdomain packの規則を使って、source/resourceからCapability Requirementを抽出します。

## 4. Build Realm

将来のbuilderは4 Realmを区別します。

### Host Realm

build host上で実行可能:

- CMake / Ninja / Make
- code generator
- resource compiler
- shader compiler
- asset packer

### Target Realm

生成のみ。artifact-only modeでは実行禁止:

- executable
- shared library
- plugin
- target-side generator

### Content Realm

CPU ISAとは独立したcontent artifact:

- shader binary
- texture archive
- audio bank
- localization bundle

### Platform Realm

対象SDKまたはplatform workerが必要:

- Apple app bundle / framework embedding / signing
- Windows resource / manifest / installer
- Linux package metadata

### Managed Sysroot Boundary

Linux cross buildでは、Target Realmのheader・startup object・C/C++ runtimeをmanaged sysrootとして供給します。sysroot provisioning自体はHost Realmのnetwork/filesystem処理ですが、image内のexecutableやpackage managerは一切起動しません。

```text
trusted provider declaration
        │
        ▼
OCI index ── select exact linux/<arch>
        │
        ▼
manifest/config/layer SHA-256 verification
        │
        ▼
safe overlay extraction
        │
        ├─ whiteout semantics
        ├─ path traversal rejection
        ├─ absolute symlink rebasing
        └─ device/FIFO omission
        │
        ▼
content-addressed rootfs + target ref
        │
        ▼
Clang --target/--sysroot/--gcc-toolchain + LLD
```

provider tagは発見用であり、buildの再現性境界は選択後のimmutable manifest digestです。通常buildはtarget refを再利用し、明示的なrefreshだけがtagを再解決します。artifact setへ`sysroot.lock.json`をコピーし、source、platform、manifest/config/layer digestを保存します。

## 5. Target Contract

Target Profileは以下の直積ではなく、能力集合として表現します。

- CPU ISA
- endianness
- pointer width
- ABI
- object format
- OS
- SDK
- libc
- filesystem
- threading
- GUI
- graphics
- audio
- input
- packaging

例:

```json
{
  "id": "macos-arm64",
  "os": "darwin",
  "arch": "arm64",
  "triple": "arm64-apple-darwin",
  "object_format": "macho",
  "capabilities": [
    "cpu.arm64",
    "gui.appkit",
    "graphics.metal",
    "audio.coreaudio"
  ]
}
```

## 6. Strategy Planner

各Portability Islandでstrategyを選択します。

- native-rebuild
- source-rewrite
- generated-adapter
- compatibility-runtime
- api-translation
- replacement-dependency
- manual-review
- unresolved

同じgame内でstrategyは混在できます。

```text
game core       -> native rebuild
x86 intrinsic   -> portable fallback
Win32 window    -> generated adapter
D3D renderer    -> API translation provider
HLSL            -> shader pipeline
vendor DLL      -> unresolved / replacement
assets          -> content pipeline
```

## 7. Domain Pack

GUI、graphics、audioなどの知識をcoreへ直書きしません。

v0.1のdomain pack:

- core-c
- build-portability
- compute
- platform
- gui
- graphics
- shader
- audio
- input
- plugin
- assets

`build-portability`は`-march=native`、architecture固定flag、target executableを使うconfiguration probeを検出します。`compute`はCUDA、OpenCL、OpenMP、SVE、RISC-V Vector、WASM SIMDを扱います。`platform`はcompiler extension、packed ABI、Win32/POSIX filesystem・process・virtual memory・clock、Registry、COMなどをCapabilityへ正規化します。

現在はembedded JSON detection rulesですが、将来は次のinterfaceへ発展させます。

```go
type DomainPack interface {
    Detect(ProjectGraph) []Detection
    Requirements([]Detection) []CapabilityRequirement
    Strategies(CapabilityRequirement, TargetContract) []Candidate
    Transform(SelectedStrategy, Workspace) PatchSet
    Inspect(ArtifactSet, TargetContract) []Finding
}
```

## 8. GUIへの耐性

GUIは`CreateWindowEx -> NSWindow`の文字列置換では扱いません。

回収する意味:

- application lifecycle
- window hierarchy
- event loop
- command routing
- menu / dialog
- clipboard / drag-and-drop
- text input / IME
- accessibility
- DPI / scaling
- native handle escape
- localization/resource

出力policyも分離します。

- fidelity-ui: 元の見た目とイベント挙動優先
- native-ui: 対象OSのnative UX/accessibility優先

## 9. 3Dとshaderへの耐性

graphics requirementはAPI名だけでなく、feature単位へ細分化します。

- resource model
- pipeline model
- synchronization model
- presentation model
- texture/compression format
- shader stage
- mesh/geometry/tessellation capability

shaderは独立pipelineです。

```text
HLSL / GLSL / SPIR-V / MSL
        │
        ▼
preprocess -> compile -> reflect -> binding remap -> package
```

## 10. Gameへの耐性

Game Packは次を第一級contractとして扱います。

- frame loop / timestep / frame pacing
- keyboard / text / raw mouse / gamepad / rumble
- audio device and stream semantics
- save-data layout
- network packet layout
- asset streaming
- dynamic plugin ABI
- platform service integration

特にsave dataとnetwork packetはCPU ABIと切り離し、persistent Data Contractとして扱います。

## 11. Artifact Assurance

```text
generated
linked
packaged
statically-validated
native-runtime-validated
behavior-validated
performance-validated
```

v0.1が到達するのは`statically-validated`までです。

静的検査:

- object format
- CPU architecture
- foreign object混入
- shared dependency一覧
- plugin/resource/shader presence
- package manifest
- SHA-256

実行していないartifactを`behavior-validated`とは表示しません。

### Verifiable artifact-set lifecycle

```text
project tree
    │ deterministic content fingerprint
    ▼
project_digest
    │ + target / build system / sysroot / Codex policy / Miruri version
    ▼
request_digest ── short display key ──► build_id
    │
    ▼
.miruri-staging/<target>-<build-id>-<nonce>/
    ├─ analysis / plan / build log
    ├─ license evidence / SPDX 2.3 SBOM
    ├─ artifact inspection metadata
    ├─ manifest
    └─ checksums.sha256
    │ strict non-executing verification
    ▼
atomic rename
    ▼
dist/<target>/
```

fingerprintはmtime、owner、absolute pathを含めず、relative path、content、symlink target、execute bitを含めます。entry headerとpayloadはlength-prefixed binary framingでdomain separationし、payloadが次entryのheader境界を曖昧化できないようにします。project root、output、report pathは既存ancestorのsymlinkを解決してcanonicalizeし、利用者が明示した生成pathをanalysis、fingerprint、isolated copy、license scanから一貫して除外します。`--reuse`はproject/request digest一致だけでなく、既存setのstrict verification成功を必須とします。

strict verifierはmetadata相互参照、license project identity、SPDX document/package/file relationship、project/artifact checksum、sysroot lock、path boundary、artifact hash・size・format・architecture・kind・dependency metadata、checksum coverageを検査します。target executableは実行しません。
SPDX packageはsource全fileを列挙済みとは主張せず`filesAnalyzed=false`とし、document `DESCRIBES`とpackage `CONTAINS`で既知のartifact・license evidenceだけを結びます。

publication前に自己検証するため、不完全setが正常な`dist/<target>`として見える時間を作りません。failed buildはstaging側へdiagnostic evidenceを残し、以前の正常setを維持します。

## 12. Codexの位置

Codexはcompilerの代わりではなく、制約付きPorting Agentです。

```text
raw build.log
        │
        ▼
error-focused diagnostic reducer
        │
        ├─ diagnostics.txt / diagnostics.json
        ▼
Codex repair in isolated overlay
        │
        ▼
source patch boundary
        ├─ accept text source / build scripts
        └─ discard objects / binaries / caches / unsafe links
        │
        ▼
compiler / linker / inspector verdict
```

Miruriは`codex --ask-for-approval never exec --sandbox workspace-write`を利用し、dangerous bypassを使いません。repair前にCodex CLI option compatibilityを検査し、repair後にはsymlink境界を再検査します。

## 13. Multi-target matrixと比較

`miruri matrix`はProject Graphとsource fingerprintを一度だけ生成し、targetごとのplannerまたはbuilderへimmutableなanalysis snapshotを渡します。matrix output/reportの除外pathもbuilderへ伝播し、analysis snapshotとisolated overlay・license evidenceのsource boundaryを一致させます。bounded worker poolを使い、result arrayはcompletion順ではなくrequestされたtarget順へ戻します。

```text
single analysis snapshot
     ├─ linux-x86_64 worker ──┐
     ├─ linux-arm64 worker  ──┼─► matrix.json
     └─ linux-riscv64 worker ─┘
```

progress streamはtarget prefixを付けてline単位で同期し、並列logの混線を抑えます。fail-fastでは最初のfailed/blocked result後にcontextをcancelし、未開始targetを`canceled`として明示します。

`miruri compare`は二つのartifact setを、build identity、target、sysroot、toolchain、Capability、strategy、license evidence、artifact path/hash/format/architecture/dependencyのcategoryへ分解して比較します。artifact binaryが同じでもtoolchainやstrategyが違えばset全体はequivalentではありません。

## 14. Worker将来像

```text
M1 Mac host
├─ native macOS worker
├─ Docker Linux worker
└─ Parallels/remote Windows worker
```

workerはtarget executionではなくSDK/toolchainを持つArtifact Builderとして始めます。
