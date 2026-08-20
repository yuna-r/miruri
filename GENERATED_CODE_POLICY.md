# Generated Code Policy

Miruriは生成物のlicenseを一律にMPL-2.0へ変更しません。また、Apache-2.0はMiruriが明示的にApache-2.0として提供する生成用runtime・adapter template等にのみ適用します。

## Classification

### Miruri Core derived

Miruri Coreのsourceをコピー・改変したcodeは、原則としてMPL-2.0の適用範囲を維持します。

### Apache-2.0 generated support code

Miruri repository内で、ファイルまたはディレクトリに`SPDX-License-Identifier: Apache-2.0`等で明示されたruntime・adapter templateだけから生成されたcodeです。該当するApache-2.0 noticeを維持します。

### Transformed source

入力projectのsourceを変形したcodeです。入力sourceのlicense、copyright、noticeを維持します。Miruriを通したことだけでMPL-2.0またはApache-2.0にはなりません。

### Adapter containing derived semantics

元source/API usageから生成したadapterです。元sourceの保護対象表現を含む場合、そのlicenseを維持します。単なるinterface declarationかどうかをMiruriが自動で法的に断定しません。

### Third-party provider output

shader compiler、translation layer、SDK tool等が生成したoutputです。各tool/providerのlicenseと利用条件に従います。

## Required metadata

生成artifactには可能な範囲で次を記録します。

- input repository and revision
- input license identifiers
- Miruri version
- target contract
- selected strategy/provider
- generated and transformed file list
- third-party dependency notices
- content hashes
- runtime validation status

## Prohibited behavior

- license headerを自動削除しない
- source licenseをMPL-2.0またはApache-2.0へ自動置換しない
- redistribution不可のSDK/runtimeをpackageへ混入しない
- license不明のbinary-only dependencyを安全と表示しない
