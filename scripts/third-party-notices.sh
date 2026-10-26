#!/bin/sh
# Third-party notices for what the release ships.
#
#   scripts/third-party-notices.sh go        regenerate third_party/go.txt
#   scripts/third-party-notices.sh assemble  write THIRD_PARTY_NOTICES.txt and
#                                            desktop/THIRD_PARTY_NOTICES.txt
#   scripts/third-party-notices.sh check     fail unless third_party/go.txt and
#                                            both assembled files are current
#
# go needs the Go toolchain and network access, so run it where the other Go
# commands run (a container; `just third-party-notices` does the whole job).
# assemble and check use only POSIX tools.
#
# The notices cover the Go modules linked into the baum binary for every
# release target (linux, darwin and windows; the module set does not depend on
# the architecture), the Go standard library, the npm packages compiled into
# the embedded bundles (third_party/js.txt, written by
# viewer/scripts/third-party-notices.mjs), the fonts embedded with them
# (Instrument Sans and Manrope) and, for the desktop app, the Rust
# crates (third_party/rust.txt, written by desktop/scripts/third-party-notices.sh).
# THIRD_PARTY_NOTICES.txt is for the command-line archives; the desktop app
# ships desktop/THIRD_PARTY_NOTICES.txt, which adds the Rust crates.
set -eu

cd "$(dirname "$0")/.."

# The tool is pinned: a different version can classify files differently and
# change the output.
go_licenses=github.com/google/go-licenses/v2@v2.0.1
instrument_sans_licence=viewer/src/fonts/instrument-sans-OFL.txt
manrope_licence=viewer/src/fonts/manrope-OFL.txt
rule='------------------------------------------------------------------------------'

generate_go() {
  work=$(mktemp -d)
  GOBIN="$work/bin" go install "$go_licenses"
  for os in linux darwin windows; do
    env GOOS="$os" GOARCH=amd64 CGO_ENABLED=0 "$work/bin/go-licenses" save ./cmd/baum \
      --save_path="$work/$os" --force 2>/dev/null
  done
  # One tree with every platform's modules; the files of a module are the same
  # whichever platform needed it. baum itself is not a third party.
  # go-licenses and the module cache write read-only files.
  chmod -R u+w "$work"
  mkdir "$work/all"
  for os in linux darwin windows; do
    cp -Rf "$work/$os/." "$work/all/"
  done
  rm -rf "$work/all/github.com/baum-ai/baum"
  # go-licenses saves only each module's main licence file. These modules
  # compile code with further notices (SQLite's public-domain dedication,
  # musl and other code in libc, code derived from Go), kept as extra files.
  for module in modernc.org/libc modernc.org/sqlite modernc.org/memory; do
    dir=$(go list -m -f '{{.Dir}}' "$module")
    for extra in "$dir"/LICENSE-3RD-PARTY.md "$dir"/SQLITE-LICENSE "$dir"/LICENSE-GO "$dir"/LICENSE-MMAP-GO; do
      if [ -f "$extra" ]; then
        cp -f "$extra" "$work/all/$module/"
        chmod u+w "$work/all/$module/$(basename "$extra")"
      fi
    done
  done
  {
    echo "Go modules linked into the baum binary (linux, darwin and windows builds)"
    echo
    echo "Each section names a Go package path and gives the licence files of its module."
    echo
    (cd "$work/all" && find . -type f | sed 's|^\./||' | LC_ALL=C sort) | while read -r file; do
      echo "$rule"
      echo "$file"
      echo "$rule"
      echo
      sed 's/\r$//' "$work/all/$file"
      echo
    done
    echo "$rule"
    echo "Go standard library and runtime (linked into every build)"
    echo "$rule"
    echo
    cat "$(go env GOROOT)/LICENSE"
    echo
  } >third_party/go.txt
  chmod -R u+w "$work"
  rm -rf "$work"
}

# assemble OUTPUT [desktop]: the notices file for the archives, or for the
# desktop app when the second argument is given.
assemble() {
  {
    cat <<'HEADER'
THIRD-PARTY NOTICES

baum is released under the MIT License (LICENSE). The software below is
compiled into baum or bundled with it, each under its own licence. This file
reproduces those licences and their copyright notices.

Contents
  1. Go modules and the Go standard library (compiled into baum)
  2. npm packages (compiled into the embedded viewer and local application)
  3. Instrument Sans and Manrope fonts (embedded in the viewer and local application)
HEADER
    if [ "${2:-}" = desktop ]; then
      echo "  4. Rust crates (compiled into the desktop app)"
    fi
    echo
    echo "$rule"
    echo "1. Go modules and the Go standard library"
    echo "$rule"
    echo
    cat third_party/go.txt
    echo
    echo "$rule"
    echo "2. npm packages"
    echo "$rule"
    echo
    cat third_party/js.txt
    echo
    echo "$rule"
    echo "3. Instrument Sans and Manrope fonts"
    echo "$rule"
    echo
    echo "The instrument-sans-* woff2 files in the embedded assets are the Instrument Sans"
    echo "variable font, under the SIL Open Font License 1.1:"
    echo
    sed 's/\r$//' "$instrument_sans_licence"
    echo
    echo "The manrope-* woff2 files in the embedded assets are the Manrope variable font,"
    echo "under the SIL Open Font License 1.1:"
    echo
    sed 's/\r$//' "$manrope_licence"
    if [ "${2:-}" = desktop ]; then
      echo
      echo "$rule"
      echo "4. Rust crates"
      echo "$rule"
      echo
      cat third_party/rust.txt
    fi
  } >"$1"
}

case "${1:-}" in
go) generate_go ;;
assemble)
  assemble THIRD_PARTY_NOTICES.txt
  assemble desktop/THIRD_PARTY_NOTICES.txt desktop
  ;;
check)
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  cp third_party/go.txt "$tmp/go.txt"
  generate_go
  if ! cmp -s third_party/go.txt "$tmp/go.txt"; then
    echo "third_party/go.txt is stale; run 'just third-party-notices' and commit the result" >&2
    exit 1
  fi
  assemble "$tmp/notices.txt"
  assemble "$tmp/desktop-notices.txt" desktop
  if ! cmp -s THIRD_PARTY_NOTICES.txt "$tmp/notices.txt" ||
    ! cmp -s desktop/THIRD_PARTY_NOTICES.txt "$tmp/desktop-notices.txt"; then
    echo "the assembled notices are stale; run 'just third-party-notices' and commit the result" >&2
    exit 1
  fi
  echo "third-party notices are current"
  ;;
*)
  echo "usage: scripts/third-party-notices.sh go|assemble|check" >&2
  exit 2
  ;;
esac
