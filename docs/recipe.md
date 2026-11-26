# Writing recipes

[日本語](recipe.ja.md)

A recipe is a JSON file that says where a UPM package comes from and how to fix it up into a VPM package. Vpmify reads the recipe, lists the versions upstream offers, converts the versions that are chosen, and registers them with ALCOM as user packages.

- A guide to writing recipes is at [docs.kaikoga.net](https://docs.kaikoga.net/en/vpmify/recipes).
- Every field and value a recipe takes is described in [recipe_format.md](recipe_format.md).
- Working recipes are in [examples/](../examples/).

## Trying a recipe

```sh
vpmify recipe add ./my-recipe.json                 # add the recipe and read its version lists
vpmify recipe versions --all com.example.my-recipe # show the versions that were read
vpmify package pull com.example.my-recipe          # convert the newest version of each package (but the onDemand ones) and register it with ALCOM
vpmify package pull com.example.my-recipe@1.2.3    # that version only
```

After you change a recipe, run `recipe add` again. It replaces the recipe that has the same `id`, and reads the version lists again. The versions whose conversion changes are built again the next time you `package pull` the recipe.
