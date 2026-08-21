# ADR 0006: Deterministic identity and verifiable artifact-set publication

- Status: Accepted
- Date: 2026-08-21

## Context

Miruriは同じprojectを複数targetへ繰り返しbuildし、artifact-only policyのまま成果物を配布する必要があります。従来の`dist/<target>`はbuild結果を保存できましたが、次の問いへ機械的に答えられませんでした。

- source treeが本当に同一か
- target、sysroot、Codex設定を含むbuild requestが同一か
- 既存artifact setの内部整合性が保たれているか
- metadataと実artifactが整合しているか
- failed buildが以前の正常setを壊していないか
- 二つのsetで何が変わったか

mtimeやabsolute pathをidentityへ含めると、checkout場所やcopy時刻だけでcache keyが変化します。一方、単にdirectoryが存在するという理由で再利用すると、部分的に変更されたartifactやpartial publicationを受け入れる危険があります。

## Decision

### 1. Project fingerprint

project内のregular fileとsymlinkをrelative path順に処理し、domain tag付きのlength-prefixed binary framingで次をSHA-256へ入力します。

- entry kind
- slash-normalized relative path
- execute bit
- regular file content、またはsymlink target文字列

mtime、owner、absolute pathは除外します。VCS、Miruri output、一般的なbuild/cache directoryに加え、callerが明示したoutput/report pathはfingerprint対象外です。project rootと生成pathは既存ancestorのsymlinkを解決したcanonical pathで比較し、output aliasがproject rootへ戻る場合は拒否します。project内部のsymlinkは追跡せず、link textだけをhashします。payload lengthをheaderへ含めるため、file内容が次entryのheaderを吸収するdelimiter ambiguityは生じません。

### 2. Build request identity

project digestに次のresolved inputを組み合わせ、canonical JSONのSHA-256を`request_digest`とします。

- complete target profile
- selected build system
- sysroot mode、provider、platform、immutable manifest digest
- explicit sysrootではnormalized path
- generator
- Codex enablement、mode、model、profile、repair limit
- dry-run state
- Miruri version

`build_id`はrequest digestの先頭20 hexadecimal charactersです。短縮IDは表示とdirectory lifecycle用であり、完全なrequest digestもmanifestへ保存します。

### 3. Self-describing artifact set

公開setは少なくとも次を持ちます。

- `analysis.json`
- `plan.json`
- `build.log`
- `licenses.json`
- `sbom.spdx.json`
- `manifest.json`
- `checksums.sha256`
- packaged artifacts
- managed sysroot使用時の`sysroot.lock.json`
- Codex使用時のattempt provenance

license reportはevidenceとconfidenceを記録し、法的結論をheuristic matchから断定しません。SPDX documentはproject packageとpackaged artifactsを列挙し、document `DESCRIBES`、package `CONTAINS`、project/artifact checksumを記録します。source全fileとdependency closureを完全列挙していないため、packageは`filesAnalyzed=false`とし、transitive closureを主張しません。

### 4. Strict non-executing verification

verifierはtarget codeを実行せず、次を検査します。

- manifest、analysis、plan、license report、SPDX document/package/file/relationship
- project名、project digest、target、license identity、SPDX project/artifact checksumの相互整合性
- sysroot lock targetとmanifest digest
- package-relative path、path traversal、symlink境界
- artifact hash、size、format、architecture、kind、architecture result、dependency metadata
- checksum index内の各file
- strict modeでは未index regular file

invalid setはread errorと区別し、structured findingを伴う`valid=false`として返します。

### 5. Verified reuse

`--reuse`は次の全条件を満たす場合だけ既存setを返します。

- `build_status`が`succeeded`または`dry-run`
- project digestが一致
- request digestが一致
- strict verificationが成功

一つでも満たさなければ通常buildへfallbackします。

### 6. Transactional publication

新しいsetは`<out>/.miruri-staging/`内で完成させ、strict self-verification後に同一filesystem上のrenameで`<out>/<target>`へ公開します。既存target directoryは一時backupへ移し、新setのrename成功後に削除します。publicationが失敗した場合はbackupを復元します。

failed buildのdiagnostic setはstagingに残し、以前の正常target directoryを上書きしません。

### 7. Structural comparison and matrix aggregation

artifact-set comparisonはdirectory timestampではなく、identity、target、sysroot、toolchain、Capability、strategy、license evidence、artifact metadataを分類して比較します。

matrix executionはproject analysisを一回だけ行い、bounded worker poolでtargetごとのplanまたはbuildを実行します。output/report exclusionは各builderのisolated copyとlicense scanにも伝播します。結果順序はcallerのtarget順を維持し、target prefix付きprogressと集約JSON reportを生成します。

## Consequences

### Positive

- 内部不整合またはpartialなsetをcache hitとして扱わない
- failed buildから以前の正常成果物を保護できる
- CIとrelease pipelineがexit codeとstructured reportを利用できる
- source、target、sysroot、toolchain、artifact差分を一つのmodelで追跡できる
- artifact-only policyを保ったままassuranceを高められる

### Trade-offs

- source tree全体のcontent hashingとstrict verificationにI/O costが発生する
- explicit sysrootはimmutable digestが無い場合path identityに留まり、sysroot内容自体の完全fingerprintではない
- 20-character Build IDには理論上collision possibilityがあるため、完全なrequest digestをauthoritative identityとして保持する
- rename transactionは同一filesystem内を前提とし、cross-process global lockはまだ提供しない
- SPDXとlicense evidenceはtransitive dependency closure、法的判定、署名付きattestationを代替しない
- `checksums.sha256`はset内部のintegrity consistencyであり、checksum indexとmanifestを含むset全体のorigin authenticityは保証しない
