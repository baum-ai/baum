#!/usr/bin/env bash
# Writes and checks the updater manifest (latest.json) for a release.
#
#   scripts/updater-manifest.sh write VERSION DIR
#   scripts/updater-manifest.sh verify DIR
#
# write reads the signed updater bundles in DIR (each FILE with its FILE.sig
# from scripts/updater-sign.sh), writes DIR/latest.json and verifies it. The
# macOS universal app, the Linux x86_64 AppImage and the Windows x86_64
# installer are required; the Linux arm64 AppImage is listed when it is there.
#
# verify checks every platform of DIR/latest.json the way the app does before
# it installs anything, and more:
#   - the platform is one the release ships, and the URL is exactly
#     https://github.com/baum-ai/baum/releases/download/vVERSION/ followed by
#     that platform's bundle name (no other host, tag or file);
#   - that bundle is in DIR, and its signature was made by the release key in
#     tauri.conf.json (minisign);
#   - the signature's trusted comment names the version the manifest
#     announces.
# So a tampered bundle, a swapped signature, a signature from another key, a
# bundle served for the wrong platform or an announced version the bundles
# were not signed for is never published.
#
# UPDATER_PUBKEY overrides the configured key (tests). Needs jq, minisign
# (MINISIGN names another binary) and base64.
set -euo pipefail

# Native jq emits CRLF on Windows; shell values must use LF on every host.
jq() { command jq "$@" | tr -d '\r'; }

usage() {
  echo "usage: scripts/updater-manifest.sh write VERSION DIR | verify DIR" >&2
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
pubkey=${UPDATER_PUBKEY:-$(jq -er '.plugins.updater.pubkey' "$here/tauri.conf.json")}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
printf '%s' "$pubkey" | base64 --decode >"$work/key.pub"

releases=https://github.com/baum-ai/baum/releases/download

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
  if ! tr -d '\r' <<<"$comment" | tr '\t' '\n' | grep -qxF "version:$version"; then
    echo "FAIL $(basename "$file"): signed for '$comment', not version $version" >&2
    return 1
  fi
  echo "ok   $(basename "$file") is signed by the release key for $version"
}

verify() {
  local dir=$1
  local manifest=$dir/latest.json
  local version
  version=$(jq -er '.version | strings' "$manifest")
  local base=$releases/v$version
  local status=0
  local platforms
  platforms=$(jq -er '.platforms | keys[]' "$manifest")
  for required in darwin-aarch64 darwin-x86_64 linux-x86_64 windows-x86_64; do
    if ! grep -qxF "$required" <<<"$platforms"; then
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
    url=$(jq -er --arg p "$platform" '.platforms[$p].url | strings' "$manifest")
    signature=$(jq -er --arg p "$platform" '.platforms[$p].signature | strings' "$manifest")
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

case "${1:-}" in
  write)
    [ "$#" -eq 3 ] || usage
    write "$2" "$3"
    ;;
  verify)
    [ "$#" -eq 2 ] || usage
    verify "$2"
    ;;
  *) usage ;;
esac
