# レシピの書き方

[English](recipe.md)

レシピは、UPM のパッケージを「どこから取り、どう直して VPM のパッケージにするか」を書いた JSON のファイルです。Vpmify はレシピを読んで上流の版の一覧を作り、選ばれた版を変換して ALCOM のユーザーパッケージに登録します。

どの版を入れるかはレシピに書きません。利用者が `vpmify package pull` の引数（または GUI）で選びます。

実物の例は [examples/](../examples/) にあります。レシピが受けるすべての欄と値は、[recipe_format.md](recipe_format.md)（英語）に書いてあります。

## いちばん小さいレシピ

npm のレジストリにあるパッケージを、そのまま出すレシピです。

```json
{
  "vpmifyFormat": 1,
  "id": "com.example.needle-deeplink",
  "displayName": "Needle Deeplink",
  "sources": [
    {
      "type": "npm",
      "registry": "https://package.openupm.com",
      "packages": [
        { "name": "com.needle.deeplink" }
      ]
    }
  ]
}
```

## 全体の形

```
レシピ
├─ vpmifyFormat
├─ id, displayName
└─ sources[]              どこから取るか
   ├─ type                "npm" か "github"
   ├─ （type ごとの欄）
   └─ packages[]          何を取り、どう直すか
      ├─ name
      ├─ versions, prerelease, onDemand
      ├─ path / paths[], versionFromTag, overlays[]   （github だけ）
      └─ overrides[]
```

- Vpmify は、ここにない欄を無視するので、`$schema` や、覚え書きの欄を足してもかまいません。書き違えた欄の名前も、エラーにならずに無視されます。範囲の欄（`versions`、`tags`）を書き違えると、「全部」になります。
- 1 つのレシピに、同じ名前のパッケージを 2 回書くことはできません。

## レシピ

- `vpmifyFormat`（必須）: レシピの形の版。今は `1` です。これのない JSON は、レシピとして読みません。Vpmify は、自分が知っているより新しい版のレシピを、新しい Vpmify が要ると言って断ります。
- `id`（必須）: レシピの識別子。VPM のリポジトリの id と同じく、レシピを書く人のドメインを逆さにした形（`com.example.univrm`）にします。使える文字は英小文字・数字・`.`・`_`・`-` で、先頭は英小文字か数字。コマンドの引数（`vpmify package pull <id>`）に使います。同じ `id` のレシピを取り込むと、前のものを置き換えます。
- `displayName`: 人が読む名前。
- `sources`（必須）: ソースの並び。1 つ以上。

ソースを複数書くのは、パッケージと、それが依存するパッケージを、1 つのレシピでまとめて入れられるようにするためです（例: [vket-cloud-sdk.json](../examples/vket-cloud-sdk.json)）。

## ソース

### npm（UPM のレジストリ）

npmjs や OpenUPM のような、npm と互換のレジストリです。

- `type`: `"npm"`
- `registry`（必須）: レジストリの URL。
- `packages`（必須）: パッケージの並び。1 つ以上。

レジストリが持つ版のうち、SemVer として読める版を全部読みます。

### github（GitHub のリポジトリ）

GitHub のリポジトリの tag を版として読みます。tag ごとに、パッケージのフォルダの `package.json` を読みます。

- `type`: `"github"`
- `url`（必須）: `https://github.com/<owner>/<repo>`（末尾の `.git` はあってもなくてもよい）。GitHub 以外のホストは使えません。
- `tagVersion`: tag の名前から「tag の版」を読むルール（下の「tag の版」）。
- `tags`: 読む tag の範囲。tag の版で書きます（下の「範囲の書き方」）。省くと全部。
- `packages`（必須）: パッケージの並び。1 つ以上。

1 つのリポジトリが複数のパッケージを出しているとき（UniVRM など）は、1 つのソースの `packages` に並べます。同じソースのパッケージどうしは、同じ tag での版を `{sameTag}` で参照できます（下の「依存を書く」）。

## tag の版とパッケージの版

github のソースには、版が 2 種類あります。レシピの中の範囲は、欄の名前でどちらの版かが決まります。

- **tag の版**: tag の名前から読んだ版。`tags` という名前の欄（ソースの `tags`、`paths` と `overlays` の中の `tags`、`versionFromTag` の `tags`）は、tag の版の範囲です。
- **パッケージの版**: そのパッケージの版。ふつうは tag の中の `package.json` の `version` です。`versions` という名前の欄（パッケージの `versions`、`overrides` の中の `versions`）は、パッケージの版の範囲です。

2 つは一致するとは限りません。UniVRM の tag `v0.80.0` の中の `com.vrmc.unigltf` は、版が 2.16.0 です。

### tag の版の読み方

`tagVersion` を書かなければ、tag の名前から先頭の `v`（または `V`）を 1 つ落としたものを SemVer として読みます。`v1.2.3` も `1.2.3` も 1.2.3 です。SemVer として読めない tag（`v1.2`、`release-1.2.3` など）は読みません。

SemVer でない tag を使うリポジトリには、`tagVersion` を書きます。

```json
"tagVersion": {
  "match": "(\\d+)\\.(\\d+)\\.(\\d+)(?:\\.(\\d+))?",
  "version": "{1}.{2}.{3}{4:03}"
}
```

- `match`: tag の名前の全体に当てる正規表現（RE2 の文法。先読みと後方参照は使えません）。合わない tag は読みません。
- `version`: 正規表現の組（グループ）から版を組み立てるテンプレート。`{1}` は 1 番目の組、`{4:03}` は 4 番目の組を 3 桁までゼロで埋めたもの。合わなかった組は `0` です。

上の例は、tag `0.15.2.2` を 0.15.2002 と読み、tag `0.15.2` を 0.15.2000 と読みます。

同じ版に読める 2 つの tag が、パッケージの同じ版を持つときは、警告が出ます。`tagVersion` を書いて、区別できるようにしてください。

## 範囲の書き方

`tags` と `versions` に書く範囲の文法です。

- 比較: `>=1.2.3`、`>1.2.3`、`<=1.2.3`、`<1.2.3`、`=1.2.3`。版だけを書くと `=` と同じです。
- 空白で並べると「かつ」: `>=0.59.0 <0.63.1`
- `||` で「または」: `>=0.33.0 <0.39.0 || >=0.41.0 <0.45.0`
- 省くか、`*` と書くと、全部の版です。
- 版は `1.2.3` のように 3 つの数を全部書きます。`1.2`、`^1.2.3`、`~1.2.3` は書けません。
- プレリリースは SemVer の順で比べるだけです。`0.125.0-rc.1` は 0.125.0 より前なので、`<0.125.0` に入ります。

## パッケージ

- `name`（必須）: パッケージの名前。上流の `package.json` の `name` と同じでなければなりません。
- `versions`: 取る版の範囲（パッケージの版）。省くと全部。範囲の外の版は、版の一覧に入らず、利用者が名指ししても取れません。
- `prerelease`: `true` にすると、版を名指ししない `pull` がプレリリースの版も選びます。省くと、名指ししない `pull` は stable の版だけを選びます（プレリリースも一覧には入り、名指しすれば取れます）。
- `onDemand`: `true` にすると、パッケージを名指ししない `pull` が、このパッケージを選びません。入れた版の `vpmDependencies` がこのパッケージを指しているときと、利用者がパッケージか版を名指ししたときだけ取ります。上流がもう出していないパッケージが、今のパッケージと並んで ALCOM に出ないようにするための欄です（例: [univrm.json](../examples/univrm.json)）。
- `overrides`: `package.json` の上書き（下の「package.json を上書きする」）。

github のソースのパッケージには、次の欄も書けます。

- `path`: リポジトリの中の、パッケージのフォルダ（`package.json` のあるフォルダ）。省くとリポジトリのルート。
- `paths`: フォルダが tag によって違うときに、`path` の代わりに書きます。

  ```json
  "paths": [
    { "tags": "<0.131.0", "path": "Assets/VRM" },
    { "tags": ">=0.131.0", "path": "Packages/VRM" }
  ]
  ```

  - `tags` は tag の版の範囲（省くと全部）、`path` はフォルダ（必須。ルートは `""`）。
  - 範囲どうしは重なってはいけません。どの範囲にも入らない tag では、そのパッケージを読みません。パッケージが一部の tag にしかないときは、ある範囲だけを書きます。
- `versionFromTag`: パッケージの版を、`package.json` の `version` ではなく tag の版にします。Vpmify が、変換した `package.json` の `version` を書き換えます。
  - `true`: 全部の tag で。
  - `{ "tags": "<範囲>" }`: tag の版がその範囲にある tag だけで。ほかの tag は `package.json` の版のまま。
  - 上流が `package.json` の版を上げずにリリースを重ねるリポジトリのための欄です。ALCOM は版でしかパッケージを区別しません。
- `overlays`: パッケージのフォルダの中に、別のリポジトリの中身を入れます（下の「overlays」）。

tag のフォルダに `package.json` がない、`name` が違う、`version` が SemVer として読めない、のどれかなら、その tag ではそのパッケージを読みません。

## package.json を上書きする

`overrides` は、変換したパッケージの `package.json` の欄を書き換えます。

```json
"overrides": [
  {
    "versions": ">=3.4.0",
    "set": { "unity": "6000.2", "unityRelease": "6f2" }
  }
]
```

- `versions`: 当てる版の範囲（パッケージの版）。省くと全部。
- `set`: 書き込む欄と値。`package.json` のトップレベルの欄を、欄ごと置き換えます。値を `null` にすると、その欄を消します。
- `merge`: オブジェクトの欄（`dependencies`、`vpmDependencies` など）に足すキーと値。欄のほかのキーは残し、同じキーは値を置き換えます。`package.json` にその欄がなければ、欄を作ります。値を `null` にすると、そのキーを消します。

  ```json
  { "versions": ">=0.35.0 <0.42.0", "merge": { "dependencies": { "com.unity.textmeshpro": "3.0.6" } } }
  ```

- `set` と `merge` は、どちらか一方は必要で、両方書くこともできます。1 つの上書きの中では、`set` を当ててから `merge` を当てます。
- 1 つの版に複数の上書きが当たるときは、書いた順に当てます。同じ欄への `set` はあとのものが勝ち、`merge` は足し合わさります。
- 上流の値に足すときは `merge`、上流が何を書いていても欄をこの値にしたいときは `set` を使います。
- `name` と `version` は書き換えられません。

ALCOM は `package.json` の `unity` を見て、プロジェクトに入れられる版を選びます。上流の `unity` が実際と違うときは、ここで直します。

## 依存を書く

ALCOM は、依存を `vpmDependencies` からだけ辿ります。Vpmify は、上流の UPM の `dependencies` を `vpmDependencies` に自動では写しません。VPM のパッケージとして入れたい依存は、`set` か `merge` の `vpmDependencies` に書きます。

```json
"overrides": [
  { "set": { "vpmDependencies": { "com.vrmc.gltf": "={sameTag}" } } }
]
```

値は ALCOM が読む版の範囲です。`>=1.2.1` は「以上」、`=1.2.3` と `1.2.3` は「ちょうど」です。

`vpmDependencies` の値の中では、次の記号が置き換わります。

- `{version}`: このパッケージの、この版。
- `{dependencies}`: `package.json` の `dependencies` が、同じ名前のパッケージに書いている値。レシピが `dependencies` を上書きしていれば、その版に当たる上書きを全部当てたあとの値です。書かれていなければ、その版は変換できません。
- `{sameTag}`: 同じソースの、その名前のパッケージが、同じ tag で持つ版。github のソースで、同じソースのほかのパッケージにだけ使えます。その tag にそのパッケージの取れる版がなければ、その版は変換できません。

依存先は、利用者が入れた別のレシピのパッケージでもかまいません。`pull` は、そこからも一緒に入れます。

Unity のレジストリから取る依存（`com.unity.*`）は、`dependencies` に書きます。上流の `package.json` に書かれていないものは、`merge` で `dependencies` に足します（例: [vci.json](../examples/vci.json)）。

## overlays

`overlays` は、パッケージのフォルダの中の空のフォルダに、別の GitHub のリポジトリの commit の中身を入れます。GitHub が配るアーカイブは git の submodule の中身を含まず、空のフォルダを残すためです。

```json
"overlays": [
  {
    "tags": ">=0.107.0 <0.124.2",
    "path": "VRM/MToon",
    "url": "https://github.com/Santarh/MToon",
    "commit": "42b03163459ac8e6b7aee08070d0f4f912035069"
  }
]
```

- `tags`: 当てる tag の版の範囲。省くと全部。
- `path`（必須）: 入れる先。パッケージのフォルダからの相対（リポジトリのルートからではありません）。
- `url`（必須）: 入れるもののリポジトリ。`https://github.com/<owner>/<repo>`。
- `commit`（必須）: 入れる commit。40 桁の小文字の 16 進。

入れる先は、上流のアーカイブの中に空のフォルダとしてある必要があります。なければ、その版は変換できません。

## レシピを試す

```sh
vpmify recipe add ./my-recipe.json                # 取り込んで、版の一覧を読む
vpmify recipe versions --all com.example.my-recipe # 読めた版を見る
vpmify package pull com.example.my-recipe          # パッケージごとの最新（onDemand を除く）を変換して、ALCOM に登録する
vpmify package pull com.example.my-recipe@1.2.3    # 版を名指しする
```

レシピを直したら、もう一度 `recipe add` します。同じ `id` のレシピを置き換えて、版の一覧を読み直します。変換の結果が変わる版は、次にそのレシピを `package pull` したときに作り直されます。

レシピを Web に置くと、`vpmify://add?url=<レシピの https の URL>` のリンクから、GUI に取り込めます。

## 細かい決まり

まれな場面の決まりです。レシピが思ったとおりに読まれないときに見てください。

- 欄の名前は、大文字と小文字を区別します。`Versions` は `versions` の書き違いです。1 つのオブジェクトに同じ名前が 2 回あると、エラーになります。
- `tagVersion`: 組み立てた結果が SemVer にならない tag と、組が埋める桁を超える tag（上の例で 4 番目が 4 桁）は読みません。数の先頭の余分な `0` は落とします。
- 同じ版に読める 2 つの tag が、パッケージの同じ版を持つときは、名前の順で前のほうからパッケージを取ります。`tagVersion` がなくても、`v1.2.3` と `1.2.3` の両方がある上流では起きます。
- パッケージの同じ版が複数の tag にあるときは、いちばん古い tag の中身を使います。ただし stable の版は、名前がプレリリースでない tag のうちいちばん古いものがあれば、そこから取ります。ビルドメタデータだけが違う版（`1.0.0+a` と `1.0.0+b`）は、ALCOM と同じく、ここでも同じ版です。
- 記号が置き換わるのは、`vpmDependencies` の値の中だけです。`set` と `merge` のほかの値の中でも、`{…}` を文字として使わないでください。あとの形の版で置き換えに使うことがあります。
- 同じ tag に、同じフォルダ（または入れ子のフォルダ）へ入れる overlay を 2 つ書くことはできません。
