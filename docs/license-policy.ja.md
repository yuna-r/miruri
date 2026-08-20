# Miruri License Policy

## 方針

Miruri Coreは **Mozilla Public License 2.0 (MPL-2.0)** で提供します。

Miruriが生成成果物へ組み込む目的で提供する小さなruntime・adapter template等は、該当ファイルに明示した場合に限り **Apache License 2.0** で提供します。

この分離の目的は、Miruri本体への改変をfile-level copyleftの範囲で還流可能にしつつ、Miruriを使って移植される第三者・商用ソフトウェアへMiruri Coreのlicenseを不必要に波及させないことです。

## 著作権とlicenseの境界

### Miruri Core

Miruri contributorsが著作権を持ち、MPL-2.0で提供します。CoreにはCLI、Project Graph、analyzer、planner、builder、inspector、Codex integration、domain/provider framework等を含みます。

### 生成用runtime / adapter template

Miruri自身が成果物へ組み込むために提供し、`SPDX-License-Identifier: Apache-2.0`等で明示されたファイルはApache-2.0です。MPL-2.0のファイルを自動的にApache-2.0へ変換するものではありません。

### 変換対象source

元projectのlicenseを維持します。Miruriを通したことだけでMPL-2.0またはApache-2.0にはなりません。

### Miruri生成code

- Apache-2.0指定templateだけから生成: Apache-2.0のnoticeを維持
- 元sourceの変形を含む: 元sourceのlicenseを維持
- MPL-2.0のMiruri Core codeをコピー・改変して含む: MPL-2.0の適用範囲を維持
- 複数sourceを含む: 適用されるnoticeと条件をすべて維持

### dependency / SDK / translation layer

各providerのlicense、redistribution、patent、trademark、SDK利用条件を維持します。MiruriはSDKのlicense制約を迂回しません。

## Contribution

外部contributionはDeveloper Certificate of Origin (DCO)方式です。

`Signed-off-by`は、contributorがその変更を提供する権利を持ち、対象ファイルに適用されるproject licenseで公開できることを示します。

```bash
git commit -s -m "Implement provider manifest validation"
```

## License変更

public contributionを受け入れた後のrelicenseは著作権者の同意が必要になる場合があります。そのため、Core=MPL-2.0、明示的な生成用runtime/template=Apache-2.0という境界を最初から維持します。

## 自動取得

Miruriが将来SDK/toolchain/libraryを取得する場合、artifact metadataへ次を記録します。

- canonical source
- version
- content hash
- signature verification result
- license identifier
- redistribution condition
- build recipe
- transitive notices

license不明のartifactを自動再配布しません。
