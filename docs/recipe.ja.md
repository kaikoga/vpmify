# レシピの書き方

[English](recipe.md)

レシピは、UPM のパッケージを「どこから取り、どう直して VPM のパッケージにするか」を書いた JSON のファイルです。Vpmify はレシピを読んで上流の版の一覧を作り、選ばれた版を変換して ALCOM のユーザーパッケージに登録します。

- レシピの書き方の案内は [docs.kaikoga.net](https://docs.kaikoga.net/vpmify/recipes) にあります。
- レシピが受けるすべての欄と値は、[recipe_format.ja.md](recipe_format.ja.md) に書いてあります。
- 実物の例は [examples/](../examples/) にあります。

## レシピを試す

```sh
vpmify recipe add ./my-recipe.json                # 取り込んで、版の一覧を読む
vpmify recipe versions --all com.example.my-recipe # 読めた版を見る
vpmify package pull com.example.my-recipe          # パッケージごとの最新（onDemand を除く）を変換して、ALCOM に登録する
vpmify package pull com.example.my-recipe@1.2.3    # その版だけ
```

レシピを直したら、もう一度 `recipe add` します。同じ `id` のレシピを置き換えて、版の一覧を読み直します。変換の結果が変わる版は、次にそのレシピを `package pull` したときに作り直されます。
