# fzyを使った最初のCodex-assisted repair実験

## 目的

Miruriの次のloopを、同梱fixtureではなく既存OSSで確認します。

```text
failed target build
  -> diagnostic capture
  -> constrained Codex repair
  -> clean rebuild
  -> target artifact inspection
```

対象はMIT LicenseのC製terminal fuzzy finder [`jhawthorn/fzy`](https://github.com/jhawthorn/fzy) です。第三者sourceはMiruri repositoryへvendorしていません。

## 実験条件

- host: Apple Silicon macOS
- target: `macos-arm64`
- build system: Make
- fzyは元からmacOS ARM64へ対応しているため、repair loop確認用にARM64だけを拒否するsynthetic `#error`を一時追加
- target executableは実行せず、compile/linkとMach-O static inspectionだけを実施

## 結果

Codex repair 1回でsynthetic blockerが除去され、MiruriはARM64 Mach-O executableを収集して`statically-validated`と判定しました。

この実験でalpha.4の次の課題も観測しました。

1. system header warningが大量にCodex promptへ流入した
2. Codexの確認buildで変化したexecutableとobjectが`repair.patch`へ混入した
3. assumption記録用の`MIRURI_REPAIR_NOTES.md`が対象projectへ追加された

alpha.5では、error中心のstructured diagnostics、source patch boundary、`discarded_changes` provenanceへ置き換えています。

## 再現例

```bash
git clone https://github.com/jhawthorn/fzy.git fzy-miruri-test
cd fzy-miruri-test

python3 - <<'PY'
from pathlib import Path
p = Path("src/fzy.c")
p.write_text('''#if defined(__aarch64__) || defined(__arm64__)
#error "Miruri demo: this source currently supports x86 only"
#endif

''' + p.read_text())
PY

/path/to/miruri build \
  --target macos-arm64 \
  --codex \
  --max-repairs 2 \
  --keep-work \
  .
```

期待される出力には、`Codex repairs: 1`、`Assurance: statically-validated`、ARM64 executableが含まれます。
