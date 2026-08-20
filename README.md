# Miruri

**Miruri** is an architecture-aware software artifact synthesizer.

既存のC/C++プロジェクト全体を解析し、対象CPU・OS・ABI・SDK・GUI・graphics・shader・audio・input・plugin・assetの要件を分離したうえで、指定ターゲット向けの配布可能な成果物を生成することを目標としています。

> Status: `v0.1.0-alpha.3` — artifact-only prototype

Miruri v0.1は、対象バイナリをエミュレーション実行しません。まずは **解析、移植計画、CMake/Makeビルド、リンク済み成果物の静的検査、manifest生成** を確実に行う段階です。

## 現在できること

- C/C++、Objective-C、assembly、HLSL、GLSL、Metal shader、SPIR-V、resource、assetを含むプロジェクト全体の走査
- CMake / Makeプロジェクトの検出とビルド
- GUI、3D graphics、shader、audio、input、network、plugin、binary-only依存、CPU固有intrinsicのCapability検出
- Target Contractに基づく移植strategyの選択
- macOS、Linux、Windows、ARM64、x86_64、RISC-V、POWER向けtarget profile
- isolated source overlay内でのビルド
- ELF、Mach-O、PE、static archiveの形式・CPU architecture・依存ライブラリ検査
- `analysis.json`、`plan.json`、`manifest.json`、`build.log`を含むartifact set生成
- 任意機能としてCodex CLIを使った制約付きrepair loop

## まだできないこと

- 生成成果物の実機動作保証
- GUI/renderer/audio/input backendの自動生成
- Direct3D、Vulkan、Metal間の実際のtranslation provider実装
- shader reflectionとbinding remapの実装
- dependency package resolver / SDK downloader
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
├── manifest.json
└── artifacts/
    ├── miruri-hello
    └── libgreeting.a
```

`manifest.json`の`assurance`が`statically-validated`でも、対象バイナリは実行していません。CPU形式とartifact closureに関する静的保証です。

## クロス成果物

クロスターゲットでは、Clang/LLDと対象sysrootが必要です。

```bash
./bin/miruri build \
  --target linux-arm64 \
  --sysroot /opt/miruri/sysroots/linux-arm64 \
  path/to/project
```

環境変数でも指定できます。

```bash
export MIRURI_SYSROOT_LINUX_ARM64=/opt/miruri/sysroots/linux-arm64
./bin/miruri build --target linux-arm64 path/to/project
```

macOS成果物はmacOS + Apple SDK、Windows成果物はWindows + Windows SDKを持つbuild workerで生成する方針です。MiruriはSDKのライセンス条件を迂回しません。

## Codex repair loop

Codex CLIにChatGPTアカウントでログイン済みの場合、失敗したbuildをisolated source overlay内で修正させられます。

```bash
./bin/miruri build \
  --target linux-arm64 \
  --sysroot /opt/miruri/sysroots/linux-arm64 \
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
- repair logをartifact setへ保存

Codexは推論・修正候補を担当し、compiler、linker、artifact inspectorが結果を判定します。

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
```

詳しくは [`docs/architecture.ja.md`](docs/architecture.ja.md) と [`docs/adr/`](docs/adr/) を参照してください。公開JSON形式は [`schemas/`](schemas/) にあります。

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
