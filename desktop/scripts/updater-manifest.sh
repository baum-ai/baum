#!/usr/bin/env bash
# Writes and checks the updater manifest (latest.json) for a release.
#
#   scripts/updater-manifest.sh write VERSION DIR
#   scripts/updater-manifest.sh verify DIR
#   scripts/updater-manifest.sh signed VERSION FILE...
#   scripts/updater-manifest.sh electron DIR
#
# write reads the signed updater bundles in DIR (each FILE with its FILE.sig
# from scripts/updater-sign.sh), writes DIR/latest.json and verifies it. The
# macOS universal app and the Linux x86_64 AppImage are required; the Linux
# arm64 AppImage and the Windows x86_64 installer (not released for now) are
# listed when they are there.
#
# verify checks every platform of DIR/latest.json the way the app does before
# it installs anything, and more:
#   - the platform is one the release ships, and the URL is exactly
#     https://downloads.usebaum.com/update/vVERSION/ followed by that
#     platform's bundle name (no other host, tag or file). That Worker
#     (tools/desktop-downloads in the monorepo) counts the download and
#     redirects to the same file on the GitHub release
#     (https://github.com/baum-ai/baum/releases/download/vVERSION/...);
#   - that bundle is in DIR, and its signature was made by the release key in
#     updater-key.pub (minisign);
#   - the signature's trusted comment names the version the manifest
#     announces.
# signed checks that each FILE has a FILE.sig made by the release key for
# VERSION: the release runs it on every file electron-updater's latest*.yml
# files list (scripts/update-yml.mjs), which the app verifies the same way.
#
# electron checks that installed Tauri copies can read DIR/latest.json and
# that every bundle it lists is the Electron app, so they move onto it (the
# bridge release; see "Bridge release" in README.md): the version is a
# semantic version and pub_date an RFC 3339 time, as tauri-plugin-updater
# parses them; the macOS archive holds a single baum.app at its root, as the
# Tauri updater expects (it drops each entry's first path component and puts
# the rest in place of the installed app), with Electron's
# Contents/Resources/app.asar and an executable baum-desktop; the AppImage
# holds resources/app.asar. The Windows installer is not released and is
# not checked.
#
# So a tampered bundle, a swapped signature, a signature from another key, a
# bundle served for the wrong platform or an announced version the bundles
# were not signed for is never published.
#
# UPDATER_PUBKEY overrides the configured key (tests). Needs jq, minisign
# (MINISIGN names another binary) and base64.
set -euo pipefail

usage() {
  echo "usage: scripts/updater-manifest.sh write VERSION DIR | verify DIR | signed VERSION FILE... | electron DIR" >&2
  exit 2
}

here=$(cd "$(dirname "$0")/.." && pwd)
# In the release (BAUM_RELEASE_SIGNING=1) only the pinned minisign that
# scripts/install-minisign.sh installed may run, never one found on PATH.
if [ "${BAUM_RELEASE_SIGNING:-}" = 1 ]; then
  case "${MINISIGN:-}" in
    /*) ;;
    *)
      echo "$(basename "$0"): MINISIGN must be the absolute path of the pinned minisign in the release" >&2
      exit 1
      ;;
  esac
  if [ ! -f "$MINISIGN" ] || [ ! -x "$MINISIGN" ]; then
    echo "$(basename "$0"): MINISIGN ($MINISIGN) is not an executable file" >&2
    exit 1
  fi
fi
minisign=${MINISIGN:-minisign}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
if [ -n "${UPDATER_PUBKEY:-}" ]; then
  printf '%s' "$UPDATER_PUBKEY" | base64 --decode >"$work/key.pub"
else
  cp "$here/updater-key.pub" "$work/key.pub"
fi

# Update downloads go through downloads.usebaum.com so they are counted; it
# redirects /update/vVERSION/FILE to that file on the GitHub release.
releases=https://downloads.usebaum.com/update

# The bundle name an updater platform may point at (an extended regular
# expression), or nothing for a platform the release does not ship.
bundle_for() {
  case "$1" in
    darwin-aarch64 | darwin-x86_64) echo 'baum-desktop-darwin-universal(-unsigned|-unnotarized)?\.app\.tar\.gz' ;;
    linux-x86_64) echo 'baum-desktop-linux-amd64\.AppImage' ;;
    linux-aarch64) echo 'baum-desktop-linux-arm64\.AppImage' ;;
    windows-x86_64) echo 'baum-desktop-windows-amd64(-unsigned)?-setup\.exe' ;;
  esac
}

# Checks one bundle against its signature (Tauri's .sig format: the minisign
# signature file, base64 encoded) and the version it must be signed for.
check() {
  local file=$1 signature=$2 version=$3
  printf '%s' "$signature" | base64 --decode >"$work/bundle.minisig"
  local comment
  if ! comment=$("$minisign" -V -Q -p "$work/key.pub" -m "$file" -x "$work/bundle.minisig" 2>"$work/error"); then
    echo "FAIL $(basename "$file"): the signature does not verify with the release key: $(cat "$work/error")" >&2
    return 1
  fi
  if ! tr '\t' '\n' <<<"${comment%$'\r'}" | grep -qxF "version:$version"; then
    echo "FAIL $(basename "$file"): signed for '$comment', not version $version" >&2
    return 1
  fi
  echo "ok   $(basename "$file") is signed by the release key for $version"
}

verify() {
  local dir=$1
  local manifest=$dir/latest.json
  local version
  # Join-output adds no line ending, preserving raw values even with native Windows jq.
  version=$(jq -ej '.version | strings' "$manifest")
  local base=$releases/v$version
  local status=0
  local platforms
  platforms=$(jq -ej '.platforms | keys | join(" ")' "$manifest")
  for required in darwin-aarch64 darwin-x86_64 linux-x86_64; do
    if [[ " $platforms " != *" $required "* ]]; then
      echo "FAIL latest.json has no $required" >&2
      status=1
    fi
  done
  local platform url signature file expected
  for platform in $platforms; do
    expected=$(bundle_for "$platform")
    if [ -z "$expected" ]; then
      echo "FAIL $platform is not a platform the release ships" >&2
      status=1
      continue
    fi
    url=$(jq -ej --arg p "$platform" '.platforms[$p].url | strings' "$manifest")
    signature=$(jq -ej --arg p "$platform" '.platforms[$p].signature | strings' "$manifest")
    file=${url#"$base/"}
    if [ "$file" = "$url" ] || ! [[ $file =~ ^$expected$ ]]; then
      echo "FAIL $platform: $url is not $base/ followed by its bundle" >&2
      status=1
      continue
    fi
    if [ ! -f "$dir/$file" ]; then
      echo "FAIL $platform: $file is not in $dir" >&2
      status=1
      continue
    fi
    printf '%s: ' "$platform"
    check "$dir/$file" "$signature" "$version" || status=1
  done
  return "$status"
}

write() {
  local version=$1 dir=$2
  local base=$releases/v$version
  local platforms='{}'
  local found key keys file
  # updater target(s), then the bundle that serves them
  for entry in \
    "darwin-aarch64,darwin-x86_64 baum-desktop-darwin-universal*.app.tar.gz" \
    "linux-x86_64 baum-desktop-linux-amd64.AppImage" \
    "linux-aarch64 baum-desktop-linux-arm64.AppImage" \
    "windows-x86_64 baum-desktop-windows-amd64*-setup.exe"; do
    found=()
    for file in "$dir"/${entry#* }; do
      if [ -f "$file" ]; then found+=("$file"); fi
    done
    if [ "${#found[@]}" -eq 0 ]; then
      continue
    fi
    if [ "${#found[@]}" -ne 1 ]; then
      echo "FAIL more than one bundle matches ${entry#* }" >&2
      exit 1
    fi
    file=${found[0]}
    if [ ! -s "$file.sig" ]; then
      echo "FAIL $(basename "$file") has no updater signature; was TAURI_SIGNING_PRIVATE_KEY set?" >&2
      exit 1
    fi
    IFS=, read -ra keys <<<"${entry%% *}"
    for key in "${keys[@]}"; do
      platforms=$(jq --arg key "$key" --arg url "$base/$(basename "$file")" --rawfile sig "$file.sig" \
        '.[$key] = {signature: ($sig | rtrimstr("\n")), url: $url}' <<<"$platforms")
    done
  done
  jq -n --arg version "$version" --arg notes "https://github.com/baum-ai/baum/releases/tag/v$version" \
    --arg date "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson platforms "$platforms" \
    '{version: $version, notes: $notes, pub_date: $date, platforms: $platforms}' >"$dir/latest.json"
  verify "$dir"
}

electron() {
  local dir=$1 status=0 platform url file name entry
  local platforms
  # What tauri-plugin-updater's RemoteRelease parses, or the update is
  # dropped: a semantic version (a leading v allowed) and an RFC 3339
  # pub_date.
  if ! jq -e '.version | strings | test("^v?[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.-]+)?(\\+[0-9A-Za-z.-]+)?$")' \
    "$dir/latest.json" >/dev/null; then
    echo "FAIL latest.json's version is not a semantic version" >&2
    status=1
  fi
  if ! jq -e '.pub_date | strings | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$")' \
    "$dir/latest.json" >/dev/null; then
    echo "FAIL latest.json's pub_date is not an RFC 3339 time" >&2
    status=1
  fi
  platforms=$(jq -ej '.platforms | keys | join(" ")' "$dir/latest.json")
  for platform in $platforms; do
    url=$(jq -ej --arg p "$platform" '.platforms[$p].url | strings' "$dir/latest.json")
    name=$(basename "$url")
    file=$(cd "$dir" && pwd)/$name
    case "$name" in
      *.app.tar.gz)
        if ! tar -tzf "$file" >"$work/entries" 2>"$work/error"; then
          echo "FAIL $platform: $name is not a gzipped tar: $(cat "$work/error")" >&2
          status=1
          continue
        fi
        while IFS= read -r entry; do
          if [ "${entry%%/*}" != baum.app ]; then
            echo "FAIL $platform: $name holds $entry, outside a top-level baum.app" >&2
            status=1
            continue 2
          fi
        done <"$work/entries"
        for entry in baum.app/Contents/Resources/app.asar baum.app/Contents/MacOS/baum-desktop; do
          if ! grep -qxF "$entry" "$work/entries"; then
            echo "FAIL $platform: $name has no $entry; it is not the Electron app" >&2
            status=1
            continue 2
          fi
        done
        # The Tauri updater keeps the modes the archive gives.
        if ! tar -tvzf "$file" baum.app/Contents/MacOS/baum-desktop 2>/dev/null | grep -q '^-..x'; then
          echo "FAIL $platform: $name's baum.app/Contents/MacOS/baum-desktop is not executable" >&2
          status=1
          continue
        fi
        ;;
      *.AppImage)
        # The AppImage lists its own files (its runtime extracts them), so
        # only one built for this machine's architecture can be checked; the
        # other is built from the same commit and configuration.
        local machine
        machine=$(od -An -tx1 -j18 -N2 "$file" | tr -d ' \n')
        case "$machine-$(uname -m)" in
          3e00-x86_64 | b700-aarch64 | b700-arm64) ;;
          *)
            echo "skip $platform: $name is not built for $(uname -m)"
            continue
            ;;
        esac
        rm -rf "$work/appimage"
        mkdir "$work/appimage"
        cp "$file" "$work/appimage.bin"
        chmod 0700 "$work/appimage.bin"
        if ! (cd "$work/appimage" && "$work/appimage.bin" --appimage-extract resources/app.asar >/dev/null 2>&1) ||
          [ ! -f "$work/appimage/squashfs-root/resources/app.asar" ]; then
          echo "FAIL $platform: $name has no resources/app.asar; it is not the Electron app" >&2
          status=1
          continue
        fi
        ;;
      *)
        echo "skip $platform: $name"
        continue
        ;;
    esac
    echo "ok   $platform: $name is the Electron app"
  done
  return "$status"
}

case "${1:-}" in
  electron)
    [ "$#" -eq 2 ] || usage
    electron "$2"
    ;;
  write)
    [ "$#" -eq 3 ] || usage
    write "$2" "$3"
    ;;
  verify)
    [ "$#" -eq 2 ] || usage
    verify "$2"
    ;;
  signed)
    [ "$#" -ge 3 ] || usage
    version=$2
    shift 2
    status=0
    for file in "$@"; do
      if [ ! -s "$file.sig" ]; then
        echo "FAIL $(basename "$file") has no updater signature" >&2
        status=1
        continue
      fi
      check "$file" "$(cat "$file.sig")" "$version" || status=1
    done
    exit "$status"
    ;;
  *) usage ;;
esac
