#!/bin/bash
# Prints the licenses of the third-party code in Vpmify's release files, for
# THIRD_PARTY_NOTICES.txt:
#
#   scripts/third_party_notices.sh > THIRD_PARTY_NOTICES.txt
#
# scripts/release.sh stops when the file differs from the output.
#
# The CLI contains only Go (its standard library and runtime). The GUI also
# contains the Go modules that its macOS and Windows builds compile, and code
# from two npm packages in the front end's bundle: @wailsio/runtime, and
# Vite's modulepreload polyfill. A module's license is the license file
# nearest above each package compiled from it, so code that a module keeps
# under another license in a subdirectory gets its own section.
#
# Go's license is read from $(go env GOROOT)/LICENSE, or from GO_LICENSE where
# the Go installation keeps it elsewhere (MacPorts installs it as
# /opt/local/share/doc/go-<version>/LICENSE). The npm packages are read from
# gui/frontend/node_modules, which `npm install` in gui/frontend makes.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

# go list reads the //go:embed pattern of gui/main.go, which needs a file in
# the front end's build output. A fresh checkout has none until the GUI is
# built, so put a placeholder there while the dependencies are listed.
if [ ! -e gui/frontend/dist ]; then
  mkdir gui/frontend/dist
  touch gui/frontend/dist/placeholder
  trap 'rm -rf gui/frontend/dist' EXIT
fi

go_license=${GO_LICENSE:-$(go env GOROOT)/LICENSE}
if [ ! -f "$go_license" ]; then
  echo "no Go license at $go_license; give its path as GO_LICENSE" >&2
  exit 1
fi
node_modules=gui/frontend/node_modules
for f in "$node_modules/@wailsio/runtime/package.json" "$node_modules/vite/package.json" "$node_modules/vite/LICENSE.md"; do
  if [ ! -f "$f" ]; then
    echo "no $f; run npm install in gui/frontend" >&2
    exit 1
  fi
done

# section <title> prints the heading of one piece of software; its license
# text follows on stdin.
section() {
  echo
  echo "================================================================================"
  echo "$1"
  echo
  cat
}

npm_version() {
  sed -n 's/^  "version": "\([^"]*\)".*/\1/p' "$1"
}

cat <<'EOF'
Third-party software in Vpmify

Vpmify itself is under the MIT License (LICENSE). Its release files contain
the following third-party software under their own licenses: the CLI contains
only Go, and the GUI contains all of them.
EOF

section "Go (https://go.dev)" <"$go_license"

# Lines of "<module path> <version> <module dir> <license file>", one for each
# license file that a compiled package of a dependency is under.
licenses=$(
  for os in darwin windows; do
    (cd gui && GOOS=$os go list -deps -tags production \
      -f '{{with .Module}}{{if .Main}}- - -{{else}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{else}}- - -{{end}} {{.Dir}}' .)
  done | while read -r path version moddir dir; do
    # "-" is a package of Go itself or of the GUI; Vpmify's core is left out too.
    if [ "$path" = - ] || [ "$path" = github.com/kaikoga/vpmify ]; then
      continue
    fi
    d=$dir
    while :; do
      found=
      for f in "$d"/LICENSE "$d"/LICENSE.* "$d"/LICENCE "$d"/LICENCE.* "$d"/COPYING "$d"/COPYING.*; do
        if [ -f "$f" ]; then
          found=$f
          break
        fi
      done
      if [ -n "$found" ]; then
        echo "$path $version $moddir $found"
        break
      fi
      if [ "$d" = "$moddir" ] || [ "$d" = / ]; then
        echo "no license file for $dir in $path $version" >&2
        exit 1
      fi
      d=$(dirname "$d")
    done
  done | sort -u
)

wails_license=
while read -r path version moddir file; do
  sub=${file#"$moddir"/}
  sub=${sub%/*}
  title="$path $version"
  if [ "$sub" != "${file##*/}" ]; then
    title="$title, $sub"
  else
    if [ "$path" = github.com/wailsapp/wails/v3 ]; then
      wails_license=$file
    fi
  fi
  section "$title" <"$file"
done <<EOF
$licenses
EOF

if [ -z "$wails_license" ]; then
  echo "the GUI does not compile github.com/wailsapp/wails/v3; check the module list" >&2
  exit 1
fi

# @wailsio/runtime has no license file of its own; it is published from the
# Wails repository under the same MIT License.
section "@wailsio/runtime $(npm_version "$node_modules/@wailsio/runtime/package.json") (npm, from the Wails project)" <"$wails_license"

# Vite's LICENSE.md also carries the licenses of the dependencies bundled
# into Vite itself, which stay out of the front end's bundle.
sed -n '/^# Vite core license/,/^# Licenses of bundled dependencies/p' "$node_modules/vite/LICENSE.md" \
  | sed '1d;$d' | sed '/./,$!d' \
  | section "vite $(npm_version "$node_modules/vite/package.json") (npm, the modulepreload polyfill)"
