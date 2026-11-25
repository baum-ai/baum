#!/usr/bin/env bash
# The bridge release, end to end: an installed Tauri AppImage updates itself
# to the Electron AppImage from a latest.json made exactly as the release
# makes it, and the Electron app then updates itself, and refuses a tampered
# signature. See "Bridge release" in README.md.
#
#   scripts/bridge-test.sh prepare DIR   # in the desktop-test service
#   scripts/bridge-test.sh run DIR       # in the desktop-bridge service
#
# `just desktop::bridge-test` runs both after building the bundles they
# take, with scripts/bundle.sh, into DIR (target/bridge):
#
#   DIR/electron/   1.2.3, the release build the Tauri copy updates to
#   DIR/test-1.2.3/ 1.2.3-test.1 and DIR/test-1.2.4/ 1.2.4-test.1, test builds
#                   (updater.ts's testOverrides) for the Electron update
#
# prepare signs with a throwaway minisign key, never the release key:
#   - DIR/release: the Electron AppImage and a stand-in for the macOS archive
#     (the Electron app's app.asar in baum.app, archived as macos-finalize.sh
#     does), signed with scripts/updater-sign.sh for 1.2.3, latest.json
#     written and verified by scripts/updater-manifest.sh write, and checked
#     by its electron command;
#   - the Tauri crate's bridge_manifest_is_accepted test: tauri-plugin-updater's
#     manifest type and its verification rules accept that latest.json for a
#     1.2.2 copy, and reject it with the release key;
#   - DIR/tauri: the Tauri AppImage at 1.2.2 (cargo tauri build, with the
#     bridge-test feature, which installs a downloaded update at once), its
#     updater pointed at http://127.0.0.1:18563/latest.json and trusting the
#     throwaway key;
#   - DIR/feed: latest-linux.yml and the 1.2.4-test.1 AppImage with its
#     signature; DIR/tampered: the same with a damaged signature.
# run (scripts/bridge-test.mjs) runs the apps under Xvfb against a stand-in
# daemon and serves those files.
set -euo pipefail

usage() {
  echo "usage: scripts/bridge-test.sh prepare DIR | run DIR" >&2
  exit 2
}
[ "$#" -eq 2 ] || usage
here=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$2"
dir=$(cd "$2" && pwd)
port=18563
appimage=baum-desktop-linux-amd64.AppImage

need() {
  if [ ! -s "$1" ]; then
    echo "bridge-test: $1 is missing; build it with scripts/bundle.sh (just desktop::bridge-test)" >&2
    exit 1
  fi
}

# The throwaway key, in the Tauri CLI's format (the minisign key file, base64
# encoded) with its password, as the release's secrets hold the real one.
key() {
  local password="bridge test"
  if [ ! -s "$dir/key/key" ]; then
    rm -rf "$dir/key"
    mkdir -p "$dir/key"
    printf '%s\n%s\n' "$password" "$password" | minisign -G -f -p "$dir/key/key.pub" -s "$dir/key/key" >/dev/null 2>&1
  fi
  export TAURI_SIGNING_PRIVATE_KEY TAURI_SIGNING_PRIVATE_KEY_PASSWORD UPDATER_PUBKEY
  TAURI_SIGNING_PRIVATE_KEY=$(base64 <"$dir/key/key" | tr -d '\n')
  TAURI_SIGNING_PRIVATE_KEY_PASSWORD=$password
  UPDATER_PUBKEY=$(base64 <"$dir/key/key.pub" | tr -d '\n')
  printf '%s' "$UPDATER_PUBKEY" >"$dir/key/pubkey.b64"
}

# The files inside an AppImage, extracted without running the app.
extract() {
  local image=$1 into=$2
  shift 2
  rm -rf "$into"
  mkdir -p "$into"
  (cd "$into" && "$image" --appimage-extract "$@" >/dev/null)
}

prepare() {
  need "$dir/electron/$appimage"
  need "$dir/test-1.2.3/$appimage"
  need "$dir/test-1.2.4/$appimage"
  key

  echo "== the bridge release: latest.json for 1.2.3, as the release writes it"
  rm -rf "$dir/release" "$dir/mac"
  mkdir -p "$dir/release"
  cp "$dir/electron/$appimage" "$dir/release/"
  # The macOS archive's stand-in: Electron's app.asar from the AppImage, in
  # the layout electron-builder gives the macOS app, archived with the
  # command macos-finalize.sh uses.
  extract "$dir/electron/$appimage" "$dir/mac/extract" resources/app.asar
  mkdir -p "$dir/mac/baum.app/Contents/MacOS" "$dir/mac/baum.app/Contents/Resources"
  cp "$dir/mac/extract/squashfs-root/resources/app.asar" "$dir/mac/baum.app/Contents/Resources/"
  printf '#!/bin/sh\n' >"$dir/mac/baum.app/Contents/MacOS/baum"
  chmod 0755 "$dir/mac/baum.app/Contents/MacOS/baum"
  tar -czf "$dir/release/baum-desktop-darwin-universal.app.tar.gz" -C "$dir/mac" baum.app
  "$here/scripts/updater-sign.sh" 1.2.3 "$dir/release/$appimage" "$dir/release/baum-desktop-darwin-universal.app.tar.gz"
  "$here/scripts/updater-manifest.sh" write 1.2.3 "$dir/release"
  "$here/scripts/updater-manifest.sh" electron "$dir/release"
  jq . "$dir/release/latest.json"

  echo "== tauri-plugin-updater accepts it for an installed 1.2.2"
  cd "$here"
  BAUM_BRIDGE_DIR=$dir/release BAUM_BRIDGE_PUBKEY=$UPDATER_PUBKEY BAUM_BRIDGE_CURRENT=1.2.2 \
    cargo test --locked --bin baum-desktop bridge_manifest_is_accepted -- --ignored --nocapture
  # The same check fails with a key that did not sign it.
  release_key=$(jq -er .plugins.updater.pubkey tauri.conf.json)
  if BAUM_BRIDGE_DIR=$dir/release BAUM_BRIDGE_PUBKEY=$release_key BAUM_BRIDGE_CURRENT=1.2.2 \
    cargo test --locked --bin baum-desktop bridge_manifest_is_accepted -- --ignored >"$dir/wrong-key.log" 2>&1; then
    echo "bridge-test: the release key accepted a manifest the throwaway key signed" >&2
    exit 1
  fi
  echo "ok   rejected with the release key"

  echo "== the Tauri AppImage at 1.2.2, updating from http://127.0.0.1:$port/latest.json"
  local config
  config=$(jq -cn --arg key "$UPDATER_PUBKEY" --arg url "http://127.0.0.1:$port/latest.json" \
    '{version: "1.2.2", plugins: {updater: {pubkey: $key, endpoints: [$url], dangerousInsecureTransportProtocol: true}}}')
  # Only the updater's key: tauri build would otherwise sign updater bundles
  # it is not asked to make.
  # linuxdeploy is an AppImage too, and a container has no FUSE; its strip
  # does not know the newer sections of Ubuntu 24.04's libraries.
  env -u TAURI_SIGNING_PRIVATE_KEY -u TAURI_SIGNING_PRIVATE_KEY_PASSWORD \
    APPIMAGE_EXTRACT_AND_RUN=1 NO_STRIP=1 \
    cargo tauri build --features bridge-test --bundles appimage --config "$config"
  rm -rf "$dir/tauri"
  mkdir -p "$dir/tauri"
  cp target/release/bundle/appimage/baum_1.2.2_amd64.AppImage "$dir/tauri/$appimage"
  extract "$dir/tauri/$appimage" "$dir/tauri/extract" usr/bin/baum-desktop
  if [ ! -x "$dir/tauri/extract/squashfs-root/usr/bin/baum-desktop" ]; then
    echo "bridge-test: $dir/tauri/$appimage is not the Tauri app" >&2
    exit 1
  fi

  echo "== the Electron update: 1.2.4-test.1, signed, and with a damaged signature"
  rm -rf "$dir/feed" "$dir/tampered"
  mkdir -p "$dir/feed" "$dir/tampered"
  # bundle.sh checked that latest-linux.yml describes the AppImage.
  cp "$dir/test-1.2.4/$appimage" "$dir/test-1.2.4/latest-linux.yml" "$dir/feed/"
  "$here/scripts/updater-sign.sh" 1.2.4-test.1 "$dir/feed/$appimage"
  cp "$dir/feed/$appimage" "$dir/feed/latest-linux.yml" "$dir/tampered/"
  # One character of the signature itself (the second line, after the
  # algorithm and key id) changed; the key id still names the key.
  base64 --decode <"$dir/feed/$appimage.sig" |
    awk 'NR == 2 { c = substr($0, 30, 1); $0 = substr($0, 1, 29) (c == "A" ? "B" : "A") substr($0, 31) } { print }' |
    base64 | tr -d '\n' >"$dir/tampered/$appimage.sig"
  base64 --decode <"$dir/tampered/$appimage.sig" >"$dir/tampered/signature.minisig"
  if minisign -V -q -p "$dir/key/key.pub" -m "$dir/tampered/$appimage" \
    -x "$dir/tampered/signature.minisig" >/dev/null 2>&1; then
    echo "bridge-test: the damaged signature still verifies" >&2
    exit 1
  fi
  echo "bridge-test: prepared $dir"
}

case "$1" in
  prepare) prepare ;;
  run)
    need "$dir/tauri/$appimage"
    need "$dir/release/latest.json"
    need "$dir/feed/$appimage.sig"
    exec node "$here/scripts/bridge-test.mjs" "$dir" "$port"
    ;;
  *) usage ;;
esac
