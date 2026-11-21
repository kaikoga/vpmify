# Recipe format

This is the complete description of the recipe format: every field a recipe takes, the values it takes, and how Vpmify reads them. For a guide to writing recipes, see [recipe.md](recipe.md).

It describes format 1 (`"vpmifyFormat": 1`).

## The file

- A recipe is a JSON file in UTF-8. A byte order mark at the start is allowed.
- The top level is an object: the recipe.
- Field names are case-sensitive. A field name that comes twice in one object is an error.
- Fields that are not described here are ignored, so a recipe may carry `$schema` or fields for notes. A misspelled field is ignored too, without an error.
- A field of the wrong type (a string where a boolean is described, for example) is an error.
- A field described as "optional" may be omitted. Unless its description says otherwise, writing it as `null` is the same as omitting it.

## Recipe

The top-level object.

- `vpmifyFormat` (integer, required): the format the recipe is written in. A JSON file without it is not a recipe. A value below 1 is an error. A value above the newest format this Vpmify reads is refused, with a message that a newer Vpmify is needed.
- `id` (string, required): the identifier of the recipe. It is made of lowercase letters `a-z`, digits `0-9`, `.`, `_` and `-`, and starts with a letter or a digit. Write it in reverse domain form with the domain of whoever writes the recipe (`com.example.univrm`); Vpmify does not check the form. Adding a recipe replaces the recipe that has the same `id`.
- `displayName` (string, optional): the name people read. It is in the JSON results (`--json`) of `recipe add` and `recipe list`.
- `sources` (array of [sources](#source), required): one or more. An element that is `null` is an error, as is a `null` element of `packages`.

A package name may come only once in a recipe, across all its sources.

## Source

Where packages come from. `type` decides which other fields the source takes.

- `type` (string, required): `"npm"` or `"github"`. Any other value is an error.

### npm source

An npm-compatible registry, such as npmjs or OpenUPM.

- `type`: `"npm"`
- `registry` (string, required): the URL of the registry, with the scheme `https` or `http` and a host.
- `packages` (array of [packages](#package), required): one or more.

An npm source takes no `url`, `tagVersion` or `tags`; a non-empty one is an error.

Vpmify reads the registry's document of each package once. When its `name` is not the package's, the source cannot be read. A version is read when its number is [SemVer](#versions) and it has a tarball; the others are left out. The `package.json` in the tarball must have the package's `name` and the version's number as its `version`, or the version cannot be converted.

### github source

A GitHub repository, whose tags are read as versions.

- `type`: `"github"`
- `url` (string, required): `https://github.com/<owner>/<repo>`, with or without `.git` at the end. The scheme must be `https` and the host `github.com`. Other hosts cannot be used.
- `tagVersion` (object, optional): the rule that reads the version of a tag from its name (see [tagVersion](#tagversion)). Without it, the name of a tag is read as [SemVer](#versions) after one leading `v` or `V` is dropped.
- `tags` ([range](#ranges), optional): the tags to read, as a range on the version of the tag. Every tag when omitted.
- `packages` (array of [packages](#package), required): one or more.

A github source takes no `registry`; a non-empty one is an error.

A tag whose name does not read as a version is not read. For each tag that is read, Vpmify reads the `package.json` in the folder of each package (see [paths](#path-and-paths)).

### tagVersion

- `match` (string, required): a regular expression in RE2 syntax (no lookahead, no backreferences). It must match the whole name of the tag. A tag it does not match is not read.
- `version` (string, required): a template that builds the version from the groups of `match`.
  - `{N}` is group `N` of `match` (`{0}` is the whole name).
  - `{N:0W}` is group `N` padded with zeros on the left to `W` digits. `W` is at most 18.
  - Other text is written as it is. `{` and `}` cannot be written as text.
  - A `{N}` whose `N` is more than the number of groups in `match` is an error.

A tag that `match` matches is read as follows.

- A group that took no part in the match, or matched an empty string, is `0`.
- A group longer than its padding `W` is not read: the tag is not read.
- In the result, leading zeros of the numbers of the version and of its prerelease identifiers are dropped. Build metadata keeps them.
- When the result is not [SemVer](#versions), the tag is not read.

Example: with `"match": "(\\d+)\\.(\\d+)\\.(\\d+)(?:\\.(\\d+))?"` and `"version": "{1}.{2}.{3}{4:03}"`, the tag `0.15.2.2` is 0.15.2002, the tag `0.15.2` is 0.15.2000, and the tag `0.15.2.1000` is not read.

## Package

What to take from a source, and how to fix it up.

These fields are taken by the packages of every source.

- `name` (string, required): the name of the package. It must be the `name` in upstream's `package.json`. It must not contain `/` or `\`, and must not be `.` or `..`.
- `versions` ([range](#ranges), optional): the versions to take, as a range on the version of the package. Every version when omitted. A version outside the range is not in the version list, and cannot be taken even when the user names it.
- `prerelease` (boolean, optional, default `false`): with `true`, a `pull` that names no version chooses prereleases too. With `false`, such a `pull` chooses stable versions only; prereleases in `versions` are still in the version list, and are taken when the user names them, or as [dependencies](#how-dependencies-are-followed).
- `onDemand` (boolean, optional, default `false`): with `true`, a `pull` that names no package does not choose this package. Its versions are still taken as [dependencies](#how-dependencies-are-followed), and when the user names the package or one of its versions.
- `overrides` (array of [overrides](#override), optional): changes to `package.json`.

These fields are taken by the packages of a github source only. In a package of an npm source, a non-empty one (or `versionFromTag` other than `false`) is an error.

- `path` (string, optional): see [path and paths](#path-and-paths).
- `paths` (array, optional): see [path and paths](#path-and-paths).
- `versionFromTag` (optional): see [versionFromTag](#versionfromtag).
- `overlays` (array of [overlays](#overlay), optional): content of other repositories to put into folders of the package.

In a tag, a package of a github source is not read when its folder has no `package.json`, when the file is not a JSON object whose `name` and `version` are strings and whose `dependencies`, if any, is an object of strings, when the `name` there differs, or when it has no `version`. Unless the package takes the version of the tag (`versionFromTag`), the `version` must also be [SemVer](#versions).

### path and paths

The folder of the package in the repository, where its `package.json` is. Write `path` or `paths`, not both.

- `path` (folder, optional): the folder in every tag. The root of the repository when omitted or `""`.
- `paths` (array of objects, optional): the folder by tag. Each element has:
  - `tags` ([range](#ranges), optional): the tags it applies to, as a range on the version of the tag. Every tag when omitted.
  - `path` (folder, required): the folder. `""` is the root of the repository; an element without `path` is an error.

The ranges of `paths` must not overlap. Ranges that no version satisfies together but that meet in order, such as `>1.0.0` and `<1.0.1-0`, still count as overlapping. In a tag that no range takes, the package is not read.

A folder is written with `/` between its names, as in `Assets/VRM`. `\`, empty names (`A//B`), `.` and `..` cannot be written in it. A `/` at its start or end is dropped.

### versionFromTag

Makes the version of the tag the version of the package, in place of the `version` in `package.json`. Vpmify writes it into the `package.json` it converts.

- `true`: in every tag.
- `false`, or omitted: in no tag.
- `{ "tags": "<range>" }`: in the tags whose version is in the range, a range on the version of the tag. In other tags the package keeps the version in `package.json`. `tags` is required; `{}` or `""` is an error.

### Overlay

Puts the content of a commit of another GitHub repository into an empty folder of the package. The archives GitHub serves leave a Git submodule out as an empty folder, and an overlay fills it.

- `tags` ([range](#ranges), optional): the tags it applies to, as a range on the version of the tag. Every tag when omitted.
- `path` ([folder](#path-and-paths), required): the folder to fill, relative to the folder of the package (not to the root of the repository). It cannot be the folder of the package itself (`""`).
- `url` (string, required): the repository to take the content from, written as the `url` of a [github source](#github-source).
- `commit` (string, required): the commit to take, in 40 lowercase hexadecimal digits.
- `resolve` (reserved): writing it is an error in this format.

Two overlays cannot fill one folder, or a folder and a folder inside it, for tags that both their ranges take (counted as for `paths`).

The folder must be in upstream's archive, and empty. When it is not, the version cannot be converted.

## Override

Changes fields of the `package.json` of the converted package. An override needs `set` or `merge`, and may have both.

- `versions` ([range](#ranges), optional): the versions to change, as a range on the version of the package. Every version when omitted.
- `set` (object, optional): top-level fields of `package.json` and their values. A value, of any JSON type, replaces the field as a whole. A `null` value removes the field.
- `merge` (object of objects, optional): for each top-level field of `package.json`, keys and values to add to it. The field must be an object, as `dependencies` and `vpmDependencies` are.
  - A key that is already in the field takes the new value. The other keys of the field stay.
  - A `null` value removes the key.
  - When `package.json` has no such field, or has `null` there, the field is made, unless the `merge` adds no key.
  - When the field is there but is not an object, the version cannot be converted.
  - A field written with `null` or `{}` in place of its keys changes nothing.

`set` and `merge` cannot change `name` or `version`; writing either is an error.

The value of `vpmDependencies`, in `set`, must be an object whose values are strings, or `null`. In `merge`, the values under `vpmDependencies` must be strings or `null`. The strings may hold [placeholders](#placeholders).

When several overrides apply to a version, they are applied in the order they are written. In one override, `set` is applied before `merge`. So for one field, the last `set` wins, and every `merge` after it adds up.

Fields and keys that `package.json` already has keep their place. Fields and keys new to it are added at its end, in the byte order of their names (uppercase before lowercase) within one `set` or `merge`. When anything is changed, including the version by `versionFromTag`, the whole `package.json` is written again with an indent of two spaces, keeping its line endings.

### Placeholders

In the values of `vpmDependencies`, in `set` and `merge`, these placeholders are filled in when a version is converted. A value may hold several, and other text beside them, as in `">={version} <2.0.0"`.

- `{version}`: this version of this package (the version of the tag, when the package takes it with `versionFromTag`).
- `{dependencies}`: the value that the `dependencies` of `package.json` give the package of the same name as the key. It is read after every override that applies to the version, wherever in them `dependencies` is set or merged into. When there is no such value, the version cannot be converted.
- `{sameTag}`: the version that the package of the same name as the key has in the tag this version is taken from. It can be written only in a github source, and only for another package of the same source; naming the package itself, or a package that is not in the source, is an error. When the tag has no version of that package in the version list, the version cannot be converted.

Any other `{…}` in a value of `vpmDependencies` is an error. Placeholders are not filled in elsewhere. Do not write `{…}` as plain text in other values of `set` and `merge`: a later format may fill it in.

### How dependencies are followed

ALCOM reads the values of `vpmDependencies` as version ranges. Vpmify follows them too, with the [range syntax](#ranges) below: after a `pull`, it reads the `vpmDependencies` of every version made available, from every recipe. For a dependency on a package that a recipe provides, and that no available version satisfies, Vpmify takes a version:

- from the dependent's own recipe first, and then from the other recipes that provide the package, in the order of their `id`;
- the version the lower bound of a `>=` range names, when the version list has it; otherwise the newest version in the range, prereleases included. A version that cannot be converted is passed over.

A value Vpmify cannot read as a range, such as `^1.0.0`, is written into `package.json` as it is, and not followed.

## Versions

Versions are [SemVer 2.0.0](https://semver.org/spec/v2.0.0.html): `MAJOR.MINOR.PATCH`, with an optional prerelease after `-` and optional build metadata after `+`.

- All three numbers are written, without leading zeros: `1.2.3`, not `1.2` or `01.2.3`.
- Prerelease and build identifiers are made of `0-9`, `A-Z`, `a-z` and `-`, separated by `.`.
- Versions are compared in SemVer order, in which build metadata is ignored.

## Ranges

The values of `tags` and `versions`.

- A comparison is an operator and a version: `>=1.2.3`, `>1.2.3`, `<=1.2.3`, `<1.2.3`, `=1.2.3`. A bare version means `=`. Space between the operator and the version is allowed.
- Comparisons separated by spaces mean "and": `>=0.59.0 <0.63.1`.
- `||` means "or": `>=0.33.0 <0.39.0 || >=0.41.0 <0.45.0`.
- An omitted range, an empty string, or `*` is every version.
- `^`, `~`, `x` and versions with fewer than three numbers cannot be written.
- Prereleases are compared in SemVer order and nothing more: `0.125.0-rc.1` comes before `0.125.0`, so it is in `<0.125.0`.

## How pull chooses versions

- A `pull` that names no version takes, for each package it chooses, the newest version that `versions` and `prerelease` let it choose and that can be converted. A version that cannot be converted (a placeholder that cannot be filled in, for example) is passed over.
- A version the user names that cannot be converted is an error.

## How a github source chooses versions

- The tags are ordered by their version. Tags of one version are ordered by name, with runs of digits compared by value; names equal by value (`r1.0.0` and `r01.0.0`) are ordered by their text. This order stands for their age, as Git's list of tags has no dates.
- When one version of a package is in several tags, it is taken from the oldest tag. A stable version is taken from the oldest tag whose version is not a prerelease, when there is one. Versions that differ only in build metadata, such as `1.0.0+a` and `1.0.0+b`, are one version here, as they are in ALCOM.
- When two tags read as one version and hold the same version of a package, the package is taken from the first of them in the order above, and Vpmify warns: write a `tagVersion` that tells them apart. This happens without `tagVersion` too, when the repository has both `v1.2.3` and `1.2.3`.
- `{sameTag}` names the version of the other package in the tag the version is taken from.
