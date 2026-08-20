# Contributing to Miruri

ありがとうございます。Miruriは初期段階からCPU/OSだけでなく、GUI、graphics、shader、audio、input、plugin、asset、license境界を同じ設計で扱います。

## Before opening a change

1. Issueまたはdiscussionで対象Capabilityとtargetを明記してください。
2. 既存のProject Graph / Target Contract / Strategy境界を壊さない方法を優先してください。
3. source-targetの組合せ専用変換器ではなく、requirementとproviderの接続として提案してください。
4. third-party source、SDK、specificationを参照する場合はlicenseとcanonical sourceを記録してください。

## Development

```bash
go test ./...
go vet ./...
go build ./cmd/miruri
```

Smoke test:

```bash
go build -o ./bin/miruri ./cmd/miruri
./bin/miruri analyze fixtures/hello-c
./bin/miruri build --target host fixtures/hello-c
```

## DCO

すべてのcommitにDeveloper Certificate of Originのsign-offが必要です。

```bash
git commit -s -m "Add a graphics capability rule"
```

これは、contributorが変更を提供する権利を持ち、Miruriに適用されるMPL-2.0その他の明示されたproject licenseで公開できることを宣言するものです。

## AI-assisted contributions

Codex等を利用した変更も、人間のcontributorが責任を持ってreviewしてください。

- prompt/outputを著者扱いにしない
- third-party codeの無断copyを避ける
- tests、compiler diagnostics、artifact reportで検証する
- public APIや機能のsilent removalを禁止する
- generated codeのprovenanceを記録する

## Pull request checklist

- [ ] `go test ./...`が成功する
- [ ] `go vet ./...`が成功する
- [ ] 新しいbehaviorにtestがある
- [ ] license/provenanceを確認した
- [ ] target artifactを実行したか否かを明記した
- [ ] compatibility/semantic lossを隠していない
- [ ] commitに`Signed-off-by`がある
