#!/bin/bash
# Builds the release files of Vpmify into dist/<version>/:
#   - the CLI for macOS, Linux and Windows, each for amd64 and arm64
#     (vpmify_<version>_<os>_<arch>.tar.gz, .zip for Windows),
#   - the GUI as a universal .dmg for macOS and as NSIS installers for Windows
#     (vpmify-gui_<version>_...),
#   - SHA256SUMS of them.
#
# Usage: scripts/release.sh [--snapshot] [--cli-only]
#
# The version is taken from the tag v<version> on HEAD. The working tree must
# be clean, and gui/build/config.yml must have the same version: the GUI's
# Info.plist and Windows resources are generated from it, by changing the
# version there and running `wails3 task common:update:build-assets` in gui/.
#
#   --snapshot  build HEAD without a tag, as <config.yml version>-snapshot+<commit>,
#               to try the build; the working tree may have changes
#   --cli-only  skip the GUI, whose build takes npm and longer
#
# Run it on macOS, outside a sandbox: the GUI build fetches npm and Go modules,
# and actool, which makes the macOS app icon, does not run in one.
set -euo pipefail

snapshot=false
cli_only=false
for arg in "$@"; do
  case "$arg" in
    --snapshot) snapshot=true ;;
    --cli-only) cli_only=true ;;
    *) echo "usage: scripts/release.sh [--snapshot] [--cli-only]" >&2; exit 2 ;;
  esac
done

root=$(git rev-parse --show-toplevel)
cd "$root"

config_version=$(sed -n 's/^  version: "\([^"]*\)".*/\1/p' gui/build/config.yml)
if [ -z "$config_version" ]; then
  echo "no application version in gui/build/config.yml" >&2
  exit 1
fi

commit=$(git rev-parse --short HEAD)
dirty=$(git status --porcelain)
if $snapshot; then
  version="$config_version-snapshot+$commit"
  if [ -n "$dirty" ]; then
    version="$version.dirty"
  fi
else
  tags=$(git tag --points-at HEAD --list 'v[0-9]*')
  if [ -z "$tags" ]; then
    echo "HEAD ($commit) has no tag v<version>; tag it, or try the build with --snapshot" >&2
    exit 1
  fi
  if [ "$(echo "$tags" | wc -l | tr -d ' ')" != 1 ]; then
    echo "HEAD ($commit) has more than one tag v<version>:" $tags >&2
    exit 1
  fi
  version=${tags#v}
  if [ -n "$dirty" ]; then
    echo "the working tree has changes; commit or stash them first" >&2
    exit 1
  fi
  if [ "$version" != "$config_version" ]; then
    echo "the tag is v$version but gui/build/config.yml has $config_version" >&2
    exit 1
  fi
fi

if ! command -v wails3 >/dev/null 2>&1; then
  PATH="$(go env GOPATH)/bin:$PATH"
fi
if ! $cli_only && ! command -v wails3 >/dev/null 2>&1; then
  echo "wails3 is not found in PATH or in \$(go env GOPATH)/bin" >&2
  exit 1
fi

out="$root/dist/$version"
work="$out/.work"
rm -rf "$out"
mkdir -p "$work"
echo "building Vpmify $version into dist/$version"

# Keep macOS's extended attributes and ._ files out of the archives.
export COPYFILE_DISABLE=1

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name="vpmify_${version}_${os}_${arch}"
  exe=vpmify
  if [ "$os" = windows ]; then
    exe=vpmify.exe
  fi
  echo "CLI $os/$arch"
  mkdir -p "$work/$name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=$version" -o "$work/$name/$exe" ./cmd/vpmify
  if [ -f LICENSE ]; then
    cp LICENSE "$work/$name/"
  fi
  if [ "$os" = windows ]; then
    (cd "$work" && zip -qrX "$out/$name.zip" "$name")
  else
    tar -C "$work" -czf "$out/$name.tar.gz" "$name"
  fi
done

if ! $cli_only; then
  echo "GUI macOS universal"
  (cd gui && wails3 task darwin:package:universal VERSION="$version" && wails3 task darwin:create:dmg)
  cp gui/bin/vpmify-gui.dmg "$out/vpmify-gui_${version}_macos_universal.dmg"
  for arch in amd64 arm64; do
    echo "GUI Windows $arch"
    (cd gui && wails3 task windows:package ARCH=$arch INSTALL_SCOPE=user VERSION="$version")
    cp "gui/bin/vpmify-gui-$arch-installer.exe" "$out/vpmify-gui_${version}_windows_${arch}_installer.exe"
  done
fi

rm -rf "$work"
(cd "$out" && shasum -a 256 vpmify* > SHA256SUMS)
echo "done:"
ls -l "$out"
