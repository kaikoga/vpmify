<img src="gui/build/appicon.svg" alt="Vpmify icon" width="128" height="128">

# Vpmify

Convert UPM packages into VPM packages for ALCOM.

Vpmify converts UPM packages, from the tags of GitHub repositories and from UPM registries, into VPM packages on your machine, and registers them with ALCOM as user packages.

The GUI and recipes are explained at [docs.kaikoga.net](https://docs.kaikoga.net/en/vpmify) ([日本語](https://docs.kaikoga.net/vpmify)).

## Install the CLI

Download the archive for your OS and CPU from [GitHub Releases](https://github.com/kaikoga/vpmify/releases):

- macOS: `vpmify_<version>_darwin_arm64.tar.gz` (Apple silicon) or `vpmify_<version>_darwin_amd64.tar.gz` (Intel). macOS 13 or later.
- Linux: `vpmify_<version>_linux_amd64.tar.gz` or `vpmify_<version>_linux_arm64.tar.gz`
- Windows: `vpmify_<version>_windows_amd64.zip` or `vpmify_<version>_windows_arm64.zip`

Extract it, and put `vpmify` (`vpmify.exe` on Windows) in a folder on your `PATH`.

To build it from source instead, with Go 1.27 or later:

```sh
go install github.com/kaikoga/vpmify/cmd/vpmify@latest
```

## Use the CLI

Add a recipe, which tells Vpmify where a package comes from, then pull its newest version. ALCOM lists it as a user package.

```sh
vpmify recipe add https://vpm.kaikoga.net/vpmify/cluster-creator-kit.json
vpmify package pull net.kaikoga.vpmify.cluster-creator-kit
```

Example recipes are in [examples](examples) and on [vpm.kaikoga.net/vpmify](https://vpm.kaikoga.net/vpmify/). `vpmify help` shows every command. To write a recipe, see [docs/recipe.md](docs/recipe.md) and [docs/recipe_format.md](docs/recipe_format.md).

## AI Disclosure

Vpmify is developed with AI coding tools. Most of its code and documentation were written by Claude Code, directed by the maintainer, who is responsible for it.

Vpmify itself contains no generative AI, and sends nothing to an AI service.

## License

[MIT](LICENSE)
