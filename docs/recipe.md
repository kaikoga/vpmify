# Writing recipes

[日本語](recipe.ja.md)

A recipe is a JSON file that says where a UPM package comes from and how to fix it up into a VPM package. Vpmify reads the recipe, lists the versions upstream offers, converts the versions that are chosen, and registers them with ALCOM as user packages.

A recipe does not say which versions to install. The user chooses them with the arguments of `vpmify package pull`, or in the GUI.

Working recipes are in [examples/](../examples/).

## The smallest recipe

This recipe offers a package of an npm registry as it is.

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

## The shape

```
recipe
├─ vpmifyFormat
├─ id, displayName
└─ sources[]              where packages come from
   ├─ type                "npm" or "github"
   ├─ (fields of the type)
   └─ packages[]          what to take, and how to fix it up
      ├─ name
      ├─ versions, prerelease, onDemand
      ├─ path / paths[], versionFromTag, overlays[]   (github only)
      └─ overrides[]
```

- Vpmify ignores the fields that are not described here, so you may add `$schema` or fields for your own notes. A misspelled field name is ignored too, without an error, and a misspelled range (`versions`, `tags`) means every version.
- A package name may come only once in a recipe.

## Recipe

- `vpmifyFormat` (required): the format of the recipe. It is `1` now. A JSON file without it is not read as a recipe. Vpmify refuses a recipe in a format newer than it knows, and says a newer Vpmify is needed.
- `id` (required): the identifier of the recipe. Write it in reverse domain form with the domain of whoever writes the recipe (`com.example.univrm`), as the ids of VPM repositories are. It takes lowercase letters, digits, `.`, `_` and `-`, and starts with a letter or a digit. Commands take it as an argument (`vpmify package pull <id>`). Adding a recipe replaces the recipe that has the same `id`.
- `displayName`: the name people read.
- `sources` (required): the sources, one or more.

A recipe has several sources so that it can offer a package together with the packages it depends on (see [vket-cloud-sdk.json](../examples/vket-cloud-sdk.json)).

## Sources

### npm (UPM registries)

An npm-compatible registry, such as npmjs or OpenUPM.

- `type`: `"npm"`
- `registry` (required): the URL of the registry.
- `packages` (required): the packages, one or more.

Vpmify reads every version of the registry whose number is SemVer.

### github (GitHub repositories)

Vpmify reads the tags of a GitHub repository as versions. For each tag, it reads the `package.json` in the folder of each package.

- `type`: `"github"`
- `url` (required): `https://github.com/<owner>/<repo>`, with or without `.git`. Other hosts cannot be used.
- `tagVersion`: the rule that reads the version of a tag from its name (see "Tag versions" below).
- `tags`: the range of the tags to read, on the version of the tag (see "Ranges" below). Every tag when omitted.
- `packages` (required): the packages, one or more.

When one repository releases several packages, as UniVRM does, list them in the `packages` of one source. Packages of one source can name each other's version in the same tag with `{sameTag}` (see "Dependencies" below).

## Tag versions and package versions

A github source has two kinds of versions. The name of a field tells which kind its range is on.

- **The version of a tag** is read from the name of the tag. A field named `tags` (the source's `tags`, `tags` in `paths` and `overlays`, and `tags` in `versionFromTag`) is a range on the version of the tag.
- **The version of a package** is usually the `version` of the `package.json` in the tag. A field named `versions` (the package's `versions`, and `versions` in `overrides`) is a range on the version of the package.

The two need not agree. In UniVRM's tag `v0.80.0`, `com.vrmc.unigltf` has version 2.16.0.

### How the version of a tag is read

Without `tagVersion`, the name of the tag is read as SemVer after one leading `v` or `V` is dropped. Both `v1.2.3` and `1.2.3` are 1.2.3. A tag that does not read as SemVer, such as `v1.2` or `release-1.2.3`, is not read.

For a repository whose tags are not SemVer, write `tagVersion`.

```json
"tagVersion": {
  "match": "(\\d+)\\.(\\d+)\\.(\\d+)(?:\\.(\\d+))?",
  "version": "{1}.{2}.{3}{4:03}"
}
```

- `match`: a regular expression on the whole name of the tag, in RE2 syntax (no lookahead, no backreferences). A tag it does not match is not read.
- `version`: a template that builds the version from the groups of the expression. `{1}` is the first group, and `{4:03}` is the fourth group padded with zeros to 3 digits. A group that matched nothing is `0`.

The example reads the tag `0.15.2.2` as 0.15.2002, and the tag `0.15.2` as 0.15.2000.

When two tags read as one version and hold the same version of a package, Vpmify warns. Write a `tagVersion` that tells them apart.

## Ranges

The syntax of the ranges in `tags` and `versions`:

- Comparisons: `>=1.2.3`, `>1.2.3`, `<=1.2.3`, `<1.2.3`, `=1.2.3`. A bare version means `=`.
- Comparisons side by side mean "and": `>=0.59.0 <0.63.1`
- `||` means "or": `>=0.33.0 <0.39.0 || >=0.41.0 <0.45.0`
- An omitted range, or `*`, is every version.
- Write all three numbers of a version, as in `1.2.3`. `1.2`, `^1.2.3` and `~1.2.3` cannot be written.
- Prereleases are compared in SemVer order and nothing more. `0.125.0-rc.1` comes before 0.125.0, so it is in `<0.125.0`.

## Packages

- `name` (required): the name of the package. It must be the `name` of the upstream `package.json`.
- `versions`: the range of the versions to take, on the version of the package. Every version when omitted. A version outside the range is not in the version list, and cannot be taken even when the user names it.
- `prerelease`: with `true`, a `pull` that names no version chooses prereleases too. Without it, such a `pull` chooses stable versions only (prereleases are still in the list, and can be taken by naming them).
- `onDemand`: with `true`, a `pull` that names no package does not choose this package. Its versions are taken only when the `vpmDependencies` of an installed version name the package, or when the user names the package or a version. It keeps a package that upstream no longer releases from showing in ALCOM beside the ones it does (see [univrm.json](../examples/univrm.json)).
- `overrides`: changes to `package.json` (see "Changing package.json" below).

A package of a github source also takes these fields.

- `path`: the folder of the package in the repository, where its `package.json` is. The root of the repository when omitted.
- `paths`: written in place of `path` when the folder differs by tag.

  ```json
  "paths": [
    { "tags": ">=0.131.0", "path": "Packages/VRM" },
    { "tags": "<0.131.0", "path": "Assets/VRM" }
  ]
  ```

  - `tags` is a range on the version of the tag (every tag when omitted), and `path` is the folder (required; the root is `""`).
  - The ranges must not overlap. In a tag that no range takes, the package is not read. For a package that only some tags have, write only the ranges where it is.
- `versionFromTag`: makes the version of the tag the version of the package, in place of the `version` in `package.json`. Vpmify writes it into the `package.json` it converts.
  - `true`: in every tag.
  - `{ "tags": "<range>" }`: only in the tags whose version is in the range. In other tags the package keeps the version in `package.json`.
  - It is for a repository that releases again and again without raising the version in `package.json`. ALCOM tells packages apart by version only.
- `overlays`: puts the content of another repository into the folder of the package (see "Overlays" below).

In a tag, a package is not read when its folder has no `package.json`, when the `name` differs, or when the `version` does not read as SemVer.

## Changing package.json

`overrides` change fields of the `package.json` of the converted package.

```json
"overrides": [
  {
    "versions": ">=3.4.0",
    "set": { "unity": "6000.2", "unityRelease": "6f2" }
  }
]
```

- `versions`: the range of the versions to change, on the version of the package. Every version when omitted.
- `set`: fields and their values. Each replaces a top-level field of `package.json` as a whole. A `null` value removes the field.
- `merge`: keys and values to add to a field that is an object, such as `dependencies` or `vpmDependencies`. The other keys of the field stay, and a key that is already there takes the new value. When `package.json` has no such field, the field is made. A `null` value removes the key.

  ```json
  { "versions": ">=0.35.0 <0.42.0", "merge": { "dependencies": { "com.unity.textmeshpro": "3.0.6" } } }
  ```

- An override needs `set` or `merge`, and may have both. In one override, `set` is applied before `merge`.
- When several overrides apply to a version, they are applied in the order they are written. For one field, the last `set` wins, and every `merge` adds up.
- Use `merge` to add to what upstream writes, and `set` to make the field this value whatever upstream writes.
- `name` and `version` cannot be changed.

ALCOM chooses the versions a project can take by the `unity` field of `package.json`. When upstream's `unity` is not what the package needs, fix it here.

## Dependencies

ALCOM follows dependencies through `vpmDependencies` only. Vpmify does not copy upstream's UPM `dependencies` into `vpmDependencies` on its own. Write the dependencies to install as VPM packages in `vpmDependencies`, with `set` or `merge`.

```json
"overrides": [
  { "set": { "vpmDependencies": { "com.vrmc.gltf": "={sameTag}" } } }
]
```

The values are version ranges that ALCOM reads. `>=1.2.1` means that version or newer, and `=1.2.3` and `1.2.3` mean exactly that version.

In the values of `vpmDependencies`, the placeholders below are filled in.

- `{version}`: this version of this package.
- `{dependencies}`: the value the `dependencies` of `package.json` give the package of the same name. When the recipe changes `dependencies`, it is the value after every override that applies to the version. When there is no such value, the version cannot be converted.
- `{sameTag}`: the version the package of that name, in the same source, has in the same tag. It works in a github source, for the other packages of the same source. When the tag has no version of that package that can be taken, the version cannot be converted.

A dependency may also be a package of another recipe that the user adds. `pull` installs it from there too.

Dependencies that come from Unity's registry (`com.unity.*`) stay in `dependencies`. Add the ones upstream's `package.json` leaves out to `dependencies` with `merge` (see [vci.json](../examples/vci.json)).

## Overlays

`overlays` put the content of a commit of another GitHub repository into an empty folder of the package. The archives GitHub serves leave a Git submodule out, as an empty folder.

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

- `tags`: the range of the tags to apply it to, on the version of the tag. Every tag when omitted.
- `path` (required): the folder to fill, relative to the folder of the package (not to the root of the repository).
- `url` (required): the repository to take the content from, `https://github.com/<owner>/<repo>`.
- `commit` (required): the commit to take, in 40 lowercase hex digits.

The folder must be in upstream's archive, empty. When it is not, the version cannot be converted.

## Trying a recipe

```sh
vpmify recipe add ./my-recipe.json                 # add the recipe and read its version lists
vpmify recipe versions --all com.example.my-recipe # show the versions that were read
vpmify package pull com.example.my-recipe          # convert the newest version of each package (but the onDemand ones) and register it with ALCOM
vpmify package pull com.example.my-recipe@1.2.3    # name a version
```

After you change a recipe, run `recipe add` again. It replaces the recipe that has the same `id`, and reads the version lists again. The versions whose conversion changes are built again the next time you `package pull` the recipe.

When a recipe is on the web, a link of the form `vpmify://add?url=<https URL of the recipe>` adds it in the GUI.

## Details

Rules for rare cases, for when a recipe is not read as you expect.

- Field names are case-sensitive: `Versions` is a misspelling of `versions`. A name that comes twice in one object is an error.
- `tagVersion`: a tag whose result is not SemVer is not read, nor is a tag with a group longer than its padding (a fourth group of 4 digits in the example above). Leading zeros of the numbers are dropped.
- When two tags read as one version and hold the same version of a package, the package is taken from the tag whose name comes first. This happens without `tagVersion` too, when the repository has both `v1.2.3` and `1.2.3`.
- When one package version is in several tags, the content of the oldest tag is used. A stable version is taken from the oldest tag whose name is not a prerelease, when there is one.
- Placeholders are filled in only in the values of `vpmDependencies`. Do not write `{…}` as plain text in other values of `set` and `merge`: a later format may fill it in.
- Two overlays cannot fill one folder, or a folder and a folder inside it, in the same tag.
