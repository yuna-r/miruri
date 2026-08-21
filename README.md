# Miruri

**Miruri** is an architecture-aware software artifact synthesizer.

既存のC/C++プロジェクト全体を解析し、対象CPU・OS・ABI・SDK・GUI・graphics・shader・audio・input・plugin・assetの要件を分離したうえで、指定ターゲット向けの配布可能な成果物を生成することを目標としています。

> Status: `v0.1.0-alpha.9.11` — verifiable artifact sets, managed cross-Linux sysroots, and multi-target matrices

Miruri v0.1は、対象バイナリをエミュレーション実行しません。まずは **解析、移植計画、CMake/Meson/Autotools/Makeビルド、リンク済み成果物の静的検査、manifest生成** を確実に行う段階です。

## 現在できること

- C/C++、Objective-C、assembly、HLSL、GLSL、Metal shader、SPIR-V、resource、assetを含むプロジェクト全体の走査
- CMake / Meson / Autotools / Makeプロジェクトの検出とビルド
- GUI、3D graphics、shader、audio、input、network、plugin、binary-only依存、CPU固有intrinsicのCapability検出
- CUDA / OpenCL / OpenMP、SVE / RISC-V Vector / WASM SIMD、packed ABI、Win32 / POSIX filesystem・process・virtual memory・clock APIの検出
- `-march=native`、architecture固定flag、CMake `try_run()`などcross buildを壊しやすいbuild設定の検出
- Target Contractに基づく移植strategyの選択
- macOS、Linux、Windows、ARM64、x86_64、RISC-V、POWER向けtarget profile
- isolated source overlay内でのビルド
- ELF、Mach-O、PE、static archiveの形式・CPU architecture・依存ライブラリ検査、およびMesonのinterpreted/resource-only install tree packaging
- length-framed project tree SHA-256 fingerprint、build request digest、Build IDの生成
- `analysis.json`、`plan.json`、`manifest.json`、`build.log`、license evidence、SPDX 2.3 SBOM、checksum indexを含むartifact set生成
- `miruri verify --strict`によるmanifest・metadata・sysroot lock・artifact実体・checksum indexの非実行検証
- `miruri compare`によるtarget、toolchain、sysroot、capability、strategy、license evidence、artifact hashの構造比較
- `miruri matrix`による解析一回・複数targetの並列plan/buildと集約JSON report
- identity一致かつstrict verification成功時だけ利用する`build --reuse` / `matrix --reuse`
- staging上で自己検証してから公開し、失敗時は以前の正常artifact setを維持するtransactional publication
- 任意機能としてCodex CLIを使った制約付きrepair loop
- `miruri port`による、target backend新規生成まで明示許可したfull platform port
- OCI Registryから対象architectureのLinux開発rootfsを非実行で取得するmanaged sysroot
- sysroot manifest/layerのSHA-256検証、content-addressed cache、target lock、offline再利用
- sysroot内GCC runtimeとhost側Clang/LLDを接続するCMake/Meson/Autotools/Make toolchain自動生成

## まだできないこと

- 生成成果物の実機動作保証
- GUI/renderer/audio/inputの決定論的Domain Pack/Provider実装（`miruri port`ではCodexによる生成を試行可能）
- Direct3D、Vulkan、Metal間の実際のtranslation provider実装
- shader reflectionとbinding remapの実装
- source dependency package resolver / proprietary platform SDK downloader
- transitive dependencyを完全に閉じたSBOM・license closure。現状はproject内のlicense evidenceとpackaged artifactを保守的に記録
- artifact署名、SLSA provenance、外部Transparency Log
- remote worker / Docker worker / Parallels workerの制御
- 元実装と移植実装の意味論的等価性検証

これらを後から追加してもcoreを作り直さないよう、v0.1からProject Graph、Target Contract、Capability、Strategy、Artifact Assuranceを分離しています。

## ビルド

Miruri本体はGo標準ライブラリだけで実装されています。`cgo`は不要です。

```bash
git clone https://github.com/yuna-r/miruri.git
cd miruri
go build -o bin/miruri ./cmd/miruri
```

M1 Mac向け:

```bash
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
  go build -o dist-tools/miruri-darwin-arm64 ./cmd/miruri
```

Linux x86_64向け:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -o dist-tools/miruri-linux-amd64 ./cmd/miruri
```

## 最初の実行

```bash
./bin/miruri doctor
./bin/miruri targets
./bin/miruri analyze fixtures/hello-c
./bin/miruri plan --target host fixtures/hello-c
./bin/miruri build --target host fixtures/hello-c
```

生成物:

```text
fixtures/hello-c/dist/<target>/
├── analysis.json
├── plan.json
├── build.log
├── licenses.json
├── sbom.spdx.json
├── manifest.json
├── checksums.sha256
├── sysroot.lock.json       # managed sysroot使用時
├── codex/                  # Codexを使用した場合
└── artifacts/
    ├── miruri-hello
    └── libgreeting.a
```

`manifest.json`の`assurance`が`statically-validated`でも、対象バイナリは実行していません。CPU形式とartifact closureに関する静的保証です。

## Build identity、再利用、検証、比較

Miruriはsource treeをrelative path順に走査し、entry kind、path、execute bit、payload長、regular file内容またはsymlink target文字列をlength-framed binary encodingへ入れて`project_digest`を生成します。mtime、owner、absolute pathはfingerprintへ含めません。project rootと生成先は既存ancestorのsymlinkを解決したcanonical pathで扱います。`.git`、`.miruri`、`dist`、`build`、`out`、`node_modules`、`target`などに加え、`--out`、matrix report、`analyze --output`、`plan --output`で明示された生成pathも当該処理のsource境界から除外します。

resolved target、build system、sysroot provenance、generator、Codex設定、dry-run状態、Miruri versionをproject digestへ組み合わせて`request_digest`を作り、先頭20 hexadecimal charactersを`build_id`として記録します。

同一入力の正常artifact setを検証して再利用する場合:

```bash
./bin/miruri build --target linux-arm64 --reuse path/to/project
```

`--reuse`は単なる存在確認ではありません。project digestとrequest digestが一致し、既存setの`build_status`が`succeeded`または`dry-run`で、さらにstrict verificationが通った場合だけ再利用します。checksum不一致、metadata不整合、artifact architecture変化、symlink境界違反などがあれば再buildします。

artifact setを単独検証する場合:

```bash
./bin/miruri verify --strict path/to/project/dist/linux-arm64
./bin/miruri verify --strict --json path/to/project/dist/linux-arm64
```

`verify`はtarget codeを実行せず、次を再検査します。

- manifest、analysis、plan、license report、SPDX document/package/file/relationshipのschema identityと相互整合性
- managed sysroot lockのtargetとimmutable manifest digest
- packaged artifactのSHA-256、size、object format、CPU architecture、kind、dependency metadata
- artifact set全体の`checksums.sha256`と未index file
- package root外へ出るpath、symlink、path traversal

二つのartifact setを構造比較する場合:

```bash
./bin/miruri compare dist/linux-x86_64 dist/linux-arm64
./bin/miruri compare --json --output comparison.json dist/old/linux-arm64 dist/new/linux-arm64
```

`compare`は単純なdirectory diffではなく、build identity、target contract、toolchain、sysroot、検出Capability、選択strategy、root license evidence、artifact hash・形式・architecture・dependency metadataを分類して差分化します。manifestが参照するanalysis、plan、license metadataが読めない場合は、欠落を黙って同一扱いせずload errorとして停止します。完全一致でない場合はexit code 3を返します。

新しいartifact setは`<out>/.miruri-staging/`で完成させ、strict self-verification後にtarget directoryへrenameします。buildまたは検証が失敗した場合、既存の正常なtarget directoryは上書きしません。

`licenses.json`とSPDX 2.3 SBOMは法的結論を自動断定するものではありません。SPDX declarationと認識可能なlicense全文をevidence・confidence付きで記録し、packaged artifactを列挙します。source全fileやtransitive dependencyを完全列挙していないため、SPDX packageは保守的に`filesAnalyzed=false`とし、documentの`DESCRIBES`、packageの`CONTAINS`、project/artifact checksumを明示します。transitive dependency closureは今後のresolverで拡張します。

`checksums.sha256`とstrict verificationはartifact set内部の破損・不整合を検出する仕組みです。署名付きattestationではないため、manifestとchecksum indexを含むset全体を悪意ある主体が再生成した場合のorigin authenticityまでは保証しません。

## 複数target matrix

source解析を一度だけ行い、複数targetのplanを並列生成する場合:

```bash
./bin/miruri matrix \
  --plan-only \
  --targets linux-x86_64,linux-arm64,linux-riscv64 \
  --jobs 3 \
  path/to/project
```

複数targetを並列buildする場合:

```bash
./bin/miruri matrix \
  --targets linux-x86_64,linux-arm64 \
  --jobs 2 \
  --reuse \
  path/to/project
```

全built-in targetからSDK workerが必要なtargetを除外する例:

```bash
./bin/miruri matrix --all --exclude macos-x86_64,windows-x86_64,windows-arm64 --plan-only .
```

結果は既定で`<out>/matrix.json`へ保存されます。各targetのstatus、duration、Build ID、assurance、artifact count、reuse有無、errorと全体summaryを機械可読形式で保持します。matrixのoutput/report pathは単一解析だけでなく各builderのisolated copyとlicense scanにも除外情報が伝播します。進行中のlogは`[linux-arm64]`のようなtarget prefix付きで同期出力されます。

## クロス成果物とmanaged sysroot

Linuxクロスターゲットでは、`--sysroot`を省略するとMiruriがtrusted provider registryから対象architectureの開発rootfsを自動取得します。

```bash
./bin/miruri build --target linux-arm64 path/to/project
```

初回のみOCI image indexを解決してlayerを取得し、manifest/config/layer digestをSHA-256で検証して展開します。Docker daemon、container実行、QEMU、target側package managerは使用せず、target codeも実行しません。選択したimmutable manifest digestはtarget別にlockされ、通常の再実行では同じrootfsを再利用します。blob cacheは再利用前に再ハッシュされ、rootfsが不完全な場合はonline buildで同じdigestから自動再構築します。`--offline`ではcacheを書き換えず明示的に失敗します。

組み込みprovider:

- `linux-x86_64`: `docker.io/library/buildpack-deps:bookworm` / `linux/amd64`
- `linux-arm64`: `docker.io/library/buildpack-deps:bookworm` / `linux/arm64`
- `linux-ppc64le`: `docker.io/library/buildpack-deps:bookworm` / `linux/ppc64le`
- `linux-riscv64`: `docker.io/library/buildpack-deps:trixie` / `linux/riscv64`
- `linux-riscv32`: trusted provider未登録のためmanual sysroot

事前取得とcache確認:

```bash
./bin/miruri sysroot providers
./bin/miruri sysroot ensure --target linux-arm64
./bin/miruri sysroot list
./bin/miruri sysroot path --target linux-arm64
```

networkを禁止してlock済みcacheだけを使う場合:

```bash
./bin/miruri build --target linux-arm64 --offline path/to/project
```

provider tagを再解決してtarget lockを更新する場合:

```bash
./bin/miruri build --target linux-arm64 --refresh-sysroot path/to/project
```

cache rootは`--cache-dir`または`MIRURI_CACHE_DIR`で変更できます。指定しない場合はOSのuser cache配下の`miruri/`を使用します。artifact setには選択したprovider、platform、manifest digest、layer digestを記録した`sysroot.lock.json`がコピーされます。

解決優先順位は次のとおりです。

1. `--sysroot`
2. `MIRURI_SYSROOT_<TARGET>`
3. target別にlockされたmanaged sysroot
4. trusted providerからの自動provisioning

manual sysrootも従来どおり利用できます。

```bash
export MIRURI_SYSROOT_LINUX_RISCV32=/opt/miruri/sysroots/linux-riscv32
./bin/miruri build --target linux-riscv32 path/to/project
```

M1 MacでPATH外のHomebrew LLVMを使う場合は、Miruriが`/opt/homebrew/opt/llvm/bin`を自動探索します。別prefixを使う場合は`MIRURI_LLVM_PREFIX`を指定します。foreign Linux artifactではhost側の`ar`/`ranlib`/`strip`へfallbackせず、`llvm-ar`と`llvm-ranlib`を必須にしてhost object formatの混入を防ぎます。

macOS成果物はmacOS + Apple SDK、Windows成果物はWindows + Windows SDKを持つbuild workerで生成する方針です。MiruriはSDKのライセンス条件を迂回しません。

## Managed Meson runtime

Mesonプロジェクトで`meson`がPATH上に無い場合、MiruriはPython 3.10+を検出し、固定したMeson wheelをMiruri cacheへ取得してSHA-256検証後に展開します。グローバルPython環境、Homebrew、system package managerは変更しません。取得済みruntimeは再利用され、`--offline`ではcache済みruntimeのみを使用します。`MIRURI_MESON`で外部Meson executableを、`MIRURI_PYTHON`でmanaged runtime用Pythonを明示指定できます。

## Autotools hotfix

Git checkoutに`configure.ac` / `configure.in`があり`configure`が未生成の場合、Miruriはisolated source overlay内で`autoreconf -fi`を実行し、その後out-of-treeで`configure`と`make`を実行します。既存の`Makefile`が同時に存在してもAutotoolsを優先し、別ターゲット向けに残ったconfigure結果を再利用しません。

```bash
./bin/miruri build --target macos-arm64 path/to/autotools-project
```

クロスターゲットでは`--host=<target-triple>`と、`config.guess`が利用できる場合は`--build=<host-triple>`を自動指定し、`CC` / `CXX` / `AR` / `RANLIB` / `STRIP` / sysroot / pkg-config環境を既存toolchainから引き継ぎます。macOS native buildではHomebrewのkeg-only packageにあるpkg-config metadataとgettext等のaclocal macroも探索します。

## Codex

For multi-attempt `port`/`auto` runs, Miruri prefers a persistent loopback Codex app-server and reuses one conversation thread across attempts. The first turn receives the full port contract; later turns receive compact build/continuation deltas. If app-server is unavailable, Miruri falls back to `codex exec` automatically.
 repair loop

Codex CLIにChatGPTアカウントでログイン済みの場合、失敗したbuildをisolated source overlay内で修正させられます。

```bash
./bin/miruri build \
  --target linux-arm64 \
  --codex \
  --max-repairs 2 \
  path/to/project
```

Miruriは次を強制します。

- 元repositoryを直接編集しない
- `workspace-write` sandboxを使用
- target executableを実行しない
- public APIや機能を黙って削除しない
- x86最適化pathを残し、portable fallbackを優先
- compiler warning洪水を圧縮し、errorと周辺contextだけをCodexへ渡す
- Codexが確認buildで生成したobject・library・executable・cacheをrepair patchから除外する
- Codex実行後にsymlink境界を再検査する
- repair logと採用・破棄した変更をartifact setへ保存する

Codexは推論・source/build-script修正候補を担当し、compiler、linker、artifact inspectorが結果を判定します。Codex CLIの必要optionはrepair前に自動検査されます。

repair attemptごとのprovenanceは次のように保存されます。

```text
dist/<target>/codex/attempt-01/
├── prompt.md
├── diagnostics.txt
├── diagnostics.json
├── response-schema.json
├── events.jsonl
├── stderr.log
├── final.json
├── result.json
└── repair.patch
```

`repair.patch`には採用されたtext source/build-scriptだけが入り、破棄したbuild生成物は`result.json`と`manifest.json`の`discarded_changes`へ理由付きで記録されます。

### Full platform port

局所repairでは足りず、Windows専用GUIなどから新しいtarget backendそのものを作る必要がある場合は`port`を使います。

```bash
./bin/miruri port \
  --target linux-x86_64 \
  --keep-work \
  path/to/windows-project
```

Codexへproject固有の追加指示を渡す場合は、inline文字列またはUTF-8 text fileを指定できます。追加指示は全attemptへ毎回注入されます。

```bash
./bin/miruri port \
  --target macos-arm64 \
  --max-repairs 20 \
  --instructions "GameController inputを優先して実装すること" \
  path/to/windows-project
```

```bash
./bin/miruri port \
  --target macos-arm64 \
  --max-repairs 20 \
  --instructions-file ./miruri-port.md \
  path/to/windows-project
```

両方を指定した場合は`--instructions-file`の内容を先に、`--instructions`の内容を後に連結します。

```bash
./bin/miruri port \
  --target macos-arm64 \
  --instructions-file ./base-port-policy.md \
  --instructions "今回はaudio fidelityを優先" \
  path/to/windows-project
```

追加指示はMiruriのtarget contract、feature-fidelity contract、artifact-only policyへ上書きするのではなく、その範囲内で追加適用されます。また追加指示の内容digestはbuild identityへ含まれるため、異なる指示で生成されたartifact setを`--reuse`が誤って再利用することはありません。

`miruri port`はCodexを`auto` modeで起動し、最初は最小修正を試しつつ、必要なら同じtask内でfull portへ自動昇格します。新しいplatform abstraction、target backend、GUI entry point、build-system branch、resource定義などの追加を明示的に許可します。元platform backendと機能は可能な限り維持し、単に「新backendが必要」という理由だけでは`blocked`にしないようCodexへ指示します。

`v0.1.0-alpha.9.4`以降はfeature-fidelity gateも適用されます。MiruriはCodex実行前に元projectのtranslation unitとasset/shader inventoryを固定し、target build後に`compile_commands.json`を照合します。元実装を無視して新しいentry pointだけで別アプリを作った場合は、リンクに成功していてもport成功として受理せず、その不合格理由を次のCodex attemptへ返します。また、Codex自身が「physicsを近似した」「元assetをdecodeしていない」「音声を再生していない」などの既知の機能欠落を報告した場合も`ported`扱いにしません。

同じ動作は`build`からも指定できます。

```bash
./bin/miruri build \
  --target linux-x86_64 \
  --codex \
  --codex-mode auto \
  --max-repairs 12 \
  path/to/project
```

`--codex-mode repair`は従来の局所修復、`auto`は必要時にfull portへ昇格、`port`は最初から大規模platform portを許可します。target artifactを実行しないartifact-only policy、network禁止、isolated workspace、source patch boundaryは全modeで維持されます。

## 設計上の核

```text
Existing Project
      │
      ▼
Project Graph
      │
      ├─ source / build steps / resources / shaders / plugins
      └─ capability requirements
      │
      ▼
Target Contract
      │
      ▼
Strategy Planner
      │
      ├─ native rebuild
      ├─ source rewrite
      ├─ generated adapter
      ├─ compatibility runtime
      ├─ API translation
      └─ unresolved blocker
      │
      ▼
Isolated Artifact Builder
      │
      ▼
Static Artifact Inspector
      │
      ▼
Verifiable Artifact Set
      ├─ manifest / analysis / plan
      ├─ SPDX SBOM / license evidence
      ├─ checksums / Build ID
      └─ strict verifier / structural compare
```

詳しくは [`docs/architecture.ja.md`](docs/architecture.ja.md) と [`docs/adr/`](docs/adr/) を参照してください。公開JSON形式は [`schemas/`](schemas/) にあります。最初の外部projectを使ったCodex repair実験は [`docs/experiments/fzy-codex-repair.ja.md`](docs/experiments/fzy-codex-repair.ja.md) に記録しています。

## ライセンス

Miruri Coreは **Mozilla Public License 2.0 (MPL-2.0)** です。

生成成果物へ組み込むことを目的にMiruri自身が提供するruntime・adapter template等は、明示的に **Apache License 2.0** と指定したものに限りApache-2.0で提供します。

重要な境界:

- Miruri Core / planner / analyzer / builder / domain pack: MPL-2.0
- 明示的にApache-2.0指定された生成用runtime・template: Apache-2.0
- 外部から変換したsource: 元のlicenseを維持
- 依存library / SDK / provider: 各license・利用条件を維持
- 生成artifact: Miruriを使用しただけでMPL-2.0やApache-2.0へ変更されない
- third-party noticeと再配布条件をmanifestへ保持する

ライセンス本文は [`LICENSE`](LICENSE) と [`LICENSES/`](LICENSES/) にあります。詳しくは [`docs/license-policy.ja.md`](docs/license-policy.ja.md) と [`GENERATED_CODE_POLICY.md`](GENERATED_CODE_POLICY.md) を参照してください。

## Contribution

外部contributionはDCO方式です。

```bash
git commit -s -m "Add a target capability provider"
```

詳細は [`CONTRIBUTING.md`](CONTRIBUTING.md) を参照してください。
