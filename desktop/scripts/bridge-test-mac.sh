#!/usr/bin/env bash
# The bridge release on macOS, end to end: the installed Tauri baum.app
# updates itself to the Electron baum.app from a latest.json made as the
# release makes it, and the Electron app takes over the daemon and the
# command the Tauri app left. See "Bridge release" in README.md.
#
#   scripts/bridge-test-mac.sh baum DIR     # in the oss-test service (Go)
#   scripts/bridge-test-mac.sh prepare DIR  # on a Mac
#   scripts/bridge-test-mac.sh run DIR      # on a Mac
#
# `just desktop::bridge-test-mac` runs all three. DIR is relative to this
# directory (oss/desktop). Neither app can be built or run in a container, so
# prepare and run need a Mac with Xcode, Rust, the Tauri CLI
# (`cargo install tauri-cli`), Node.js and pnpm, and the frontend built
# (`just desktop::frontend`).
#
# baum builds DIR/bin/baum-1.2.2-{arm64,amd64} and baum-1.2.3-{arm64,amd64}
# for macOS: the versions differ, as in a real update, so the Electron app's
# daemon replaces the Tauri app's.
#
# prepare signs with a throwaway minisign key, never the release key:
#   - DIR/electron: the Electron app at 1.2.3 (scripts/bundle.sh, then
#     scripts/macos-finalize.sh, ad hoc signed), bundling baum 1.2.3;
#   - DIR/release: its .app.tar.gz signed with scripts/updater-sign.sh and
#     latest.json written by scripts/updater-manifest.sh, with a signed
#     placeholder for the Linux AppImage the manifest requires (the Linux
#     bridge test checks the real one);
#   - DIR/tauri/baum.app: the Tauri app at 1.2.2 for this Mac's architecture
#     (cargo tauri build, with the bridge-test feature, which installs a
#     downloaded update at once), bundling baum 1.2.2, its updater pointed at
#     http://127.0.0.1:18563/latest.json and trusting the throwaway key.
# It overwrites binaries/baum-universal-apple-darwin and
# binaries/baum-<this Mac's target>; `just desktop::sidecar` builds them
# again.
#
# run (scripts/bridge-test-mac.mjs) installs the Tauri app in a throwaway
# home and starts it through LaunchServices; see that script for the checks.
set -euo pipefail

usage() {
  echo "usage: scripts/bridge-test-mac.sh baum DIR | prepare DIR | run DIR" >&2
  exit 2
}
[ "$#" -eq 2 ] || usage
here=$(cd "$(dirname "$0")/.." && pwd)
case "$2" in
  /*) dir=$2 ;;
  *) dir=$here/$2 ;;
esac
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)
port=18563
archive=baum-desktop-darwin-universal-unsigned.app.tar.gz
version_flag=github.com/baum-ai/baum/internal/cli.buildVersion

mac_only() {
  if [ "$(uname -s)" != Darwin ]; then
    echo "bridge-test-mac: $1 runs on a Mac" >&2
    exit 1
  fi
}

baum() {
  cd "$here/.."
  for build in 1.2.2:arm64 1.2.2:amd64 1.2.3:arm64 1.2.3:amd64; do
    local version=${build%%:*} arch=${build##*:}
    CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath \
      -ldflags "-X $version_flag=$version" -o "$dir/bin/baum-$version-$arch" ./cmd/baum
  done
  ls -l "$dir/bin"
}

prepare() {
  mac_only prepare
  for f in baum-1.2.2-arm64 baum-1.2.2-amd64 baum-1.2.3-arm64 baum-1.2.3-amd64; do
    if [ ! -s "$dir/bin/$f" ]; then
      echo "bridge-test-mac: $dir/bin/$f is missing; run '$0 baum' in the oss-test service first" >&2
      exit 1
    fi
  done
  cd "$here"

  echo "== the throwaway key"
  local minisign password="bridge test"
  minisign=$(scripts/install-minisign.sh "$dir/minisign" | tail -1)
  if [ ! -s "$dir/key/key" ]; then
    rm -rf "$dir/key"
    mkdir -p "$dir/key"
    printf '%s\n%s\n' "$password" "$password" |
      "$minisign" -G -f -p "$dir/key/key.pub" -s "$dir/key/key" >/dev/null 2>&1
  fi
  export PATH="$(dirname "$minisign"):$PATH"
  export TAURI_SIGNING_PRIVATE_KEY TAURI_SIGNING_PRIVATE_KEY_PASSWORD UPDATER_PUBKEY
  TAURI_SIGNING_PRIVATE_KEY=$(base64 <"$dir/key/key" | tr -d '\n')
  TAURI_SIGNING_PRIVATE_KEY_PASSWORD=$password
  UPDATER_PUBKEY=$(base64 <"$dir/key/key.pub" | tr -d '\n')

  echo "== the Electron app at 1.2.3"
  lipo -create -output binaries/baum-universal-apple-darwin \
    "$dir/bin/baum-1.2.3-arm64" "$dir/bin/baum-1.2.3-amd64"
  rm -rf "$dir/electron-build" "$dir/electron"
  # bundle.sh and macos-finalize.sh refuse signing secrets in their
  # environment; the updater key is only for the steps below.
  env -u TAURI_SIGNING_PRIVATE_KEY -u TAURI_SIGNING_PRIVATE_KEY_PASSWORD \
    bash scripts/bundle.sh darwin-universal 1.2.3 "$dir/electron-build"
  env -u TAURI_SIGNING_PRIVATE_KEY -u TAURI_SIGNING_PRIVATE_KEY_PASSWORD \
    bash scripts/macos-finalize.sh "$dir/electron-build/baum-desktop-darwin-universal.app.tar" \
    1.2.3 "$dir/electron"

  echo "== latest.json for 1.2.3, as the release writes it"
  rm -rf "$dir/release"
  mkdir -p "$dir/release"
  cp "$dir/electron/$archive" "$dir/release/"
  printf 'placeholder\n' >"$dir/release/baum-desktop-linux-amd64.AppImage"
  scripts/updater-sign.sh 1.2.3 "$dir/release/$archive" "$dir/release/baum-desktop-linux-amd64.AppImage"
  scripts/updater-manifest.sh write 1.2.3 "$dir/release"

  echo "== the Tauri app at 1.2.2, updating from http://127.0.0.1:$port/latest.json"
  local triple arch
  triple=$(rustc -vV | sed -n 's/^host: //p')
  case "$triple" in
    aarch64-apple-darwin) arch=arm64 ;;
    x86_64-apple-darwin) arch=amd64 ;;
    *) echo "bridge-test-mac: unexpected host $triple" >&2; exit 1 ;;
  esac
  cp "$dir/bin/baum-1.2.2-$arch" "binaries/baum-$triple"
  local config
  config=$(jq -cn --arg key "$UPDATER_PUBKEY" --arg url "http://127.0.0.1:$port/latest.json" \
    '{version: "1.2.2", plugins: {updater: {pubkey: $key, endpoints: [$url], dangerousInsecureTransportProtocol: true}}}')
  # Only the updater's key: tauri build would otherwise sign updater bundles
  # it is not asked to make.
  env -u TAURI_SIGNING_PRIVATE_KEY -u TAURI_SIGNING_PRIVATE_KEY_PASSWORD \
    cargo tauri build --features bridge-test --bundles app --config "$config"
  rm -rf "$dir/tauri"
  mkdir -p "$dir/tauri"
  ditto target/release/bundle/macos/baum.app "$dir/tauri/baum.app"
  if [ ! -x "$dir/tauri/baum.app/Contents/MacOS/baum-desktop" ]; then
    echo "bridge-test-mac: $dir/tauri/baum.app is not the Tauri app" >&2
    exit 1
  fi
  echo "bridge-test-mac: prepared $dir"
}

case "$1" in
  baum) baum ;;
  prepare) prepare ;;
  run)
    mac_only run
    for f in tauri/baum.app/Contents/MacOS/baum-desktop release/latest.json "release/$archive"; do
      if [ ! -e "$dir/$f" ]; then
        echo "bridge-test-mac: $dir/$f is missing; run '$0 prepare' first" >&2
        exit 1
      fi
    done
    exec node "$here/scripts/bridge-test-mac.mjs" "$dir" "$port"
    ;;
  *) usage ;;
esac
