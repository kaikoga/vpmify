#!/bin/bash
# Builds the release files of Vpmify into dist/<version>/:
#   - the CLI for macOS, Linux and Windows, each for amd64 and arm64
#     (vpmify_<version>_<os>_<arch>.tar.gz, .zip for Windows),
#   - the GUI as a universal .dmg for macOS and as NSIS installers for Windows
#     (vpmify-gui_<version>_...),
#   - SHA256SUMS of them.
# The CLI's archives carry LICENSE and THIRD_PARTY_NOTICES.txt; the GUI shows
# them in its About dialog.
#
# Usage: scripts/release.sh [--snapshot] [--cli-only]
#
# The version is taken from the tag v<version> on HEAD, and the working tree
# must be clean. The repository keeps the development version 0.0.0-dev in
# gui/build/config.yml and in the GUI's build files made from it; a release
# writes its own version into those build files while it builds, and puts them
# back afterwards. Releases are built by .github/workflows/release.yml.
# THIRD_PARTY_NOTICES.txt must match what scripts/third_party_notices.sh
# prints now (it reads GO_LICENSE, as that script says).
#
#   --snapshot  build HEAD without a tag, as <config.yml version>+<commit>,
#               to try the build; the working tree may have changes, and
#               outdated notices only warn
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
  version="$config_version+$commit"
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
  # The version goes into sed replacements and file names below.
  if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]; then
    echo "the tag v$version does not name a SemVer version" >&2
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

# The notices read the front end's npm packages.
if [ ! -d gui/frontend/node_modules ]; then
  (cd gui/frontend && npm ci)
fi
scripts/third_party_notices.sh >"$work/THIRD_PARTY_NOTICES.txt"
if ! cmp -s "$work/THIRD_PARTY_NOTICES.txt" THIRD_PARTY_NOTICES.txt; then
  message="THIRD_PARTY_NOTICES.txt is not what scripts/third_party_notices.sh prints now; make it again and commit it"
  if $snapshot; then
    echo "warning: $message" >&2
  else
    echo "$message" >&2
    exit 1
  fi
fi

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
  cp LICENSE THIRD_PARTY_NOTICES.txt "$work/$name/"
  if [ "$os" = windows ]; then
    (cd "$work" && zip -qrX "$out/$name.zip" "$name")
  else
    tar -C "$work" -czf "$out/$name.tar.gz" "$name"
  fi
done

# Write the release's version over the development version in the GUI's build
# files, field by field. `wails3 task common:update:build-assets` would make
# them from config.yml, but it also writes Wails' templates back over the
# changes kept in them, such as the quoted executable path in wails_tools.nsh.
# The Windows manifest's assembly version takes four numbers, and the bundle
# version of macOS numbers only, so they get the version without its
# prerelease and build metadata.
stamped=(gui/build/darwin/Info.plist gui/build/windows/info.json
  gui/build/windows/nsis/wails_tools.nsh gui/build/windows/wails.exe.manifest)
restore_stamped() {
  git checkout -- "${stamped[@]}"
}
stamp_gui_version() {
  local core=${version%%[-+]*}
  trap restore_stamped EXIT
  plutil -replace CFBundleShortVersionString -string "$version" gui/build/darwin/Info.plist
  plutil -replace CFBundleVersion -string "$core" gui/build/darwin/Info.plist
  sed -i '' -e "s/\"0\.0\.0-dev\"/\"$version\"/" gui/build/windows/info.json
  sed -i '' -e "s/INFO_PRODUCTVERSION \"0\.0\.0-dev\"/INFO_PRODUCTVERSION \"$version\"/" \
    gui/build/windows/nsis/wails_tools.nsh
  sed -i '' -e "s/name=\"net\.kaikoga\.vpmify\" version=\"0\.0\.0\.0\"/name=\"net.kaikoga.vpmify\" version=\"$core.0\"/" \
    gui/build/windows/wails.exe.manifest
  local f
  for f in "${stamped[@]}"; do
    if git diff --quiet -- "$f"; then
      echo "$f does not have the development version to replace" >&2
      exit 1
    fi
  done
  if grep -q -- 0.0.0-dev "${stamped[@]}"; then
    echo "the development version is left in the GUI's build files:" >&2
    grep -n -- 0.0.0-dev "${stamped[@]}" >&2
    exit 1
  fi
}

if ! $cli_only; then
  if ! $snapshot; then
    stamp_gui_version
  fi
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
