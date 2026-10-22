#!/usr/bin/env bash
# Builds the desktop bundles for one platform, unsigned, and copies them into
# OUT:
#
#   scripts/bundle.sh PLATFORM VERSION OUT
#
# PLATFORM is linux-amd64, linux-arm64, darwin-universal or windows-amd64.
# VERSION is the release version without the leading v; snapshot builds use
# 0.0.0, which never checks for updates. Run it from desktop/ with the bundled
# baum binaries already in binaries/ (scripts/sidecar.sh, or the release's own
# binaries). On Linux it runs in the desktop-test image (Dockerfile).
#
# This step compiles the app: hundreds of crates' build scripts and macros,
# and the bundler's own downloads (linuxdeploy, NSIS). It never sees a
# signing secret and refuses to run when one is in its environment. Signing
# happens afterwards on what it produced, with the platform's own tools
# (scripts/macos-finalize.sh, scripts/windows-sign.ps1) and minisign
# (scripts/updater-sign.sh), in release jobs that build nothing.
#
# OUT receives:
#   linux-*           baum-desktop-linux-<arch>.deb, baum-desktop-linux-<arch>.AppImage
#   darwin-universal  baum-desktop-darwin-universal.app.tar (the unsigned app)
#   windows-amd64     baum-desktop-windows-amd64-setup.exe (unsigned)
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: scripts/bundle.sh PLATFORM VERSION OUT" >&2
  exit 2
fi
platform=$1
version=$2
out=$3

semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'
if ! [[ $version =~ $semver ]]; then
  echo "bundle: VERSION must be a version like 1.2.3 or 1.2.3-rc.1, not '$version'" >&2
  exit 2
fi

for name in TAURI_SIGNING_PRIVATE_KEY TAURI_SIGNING_PRIVATE_KEY_PASSWORD TAURI_SIGNING_PRIVATE_KEY_PATH \
  APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_SIGNING_IDENTITY APPLE_API_ISSUER APPLE_API_KEY \
  APPLE_API_KEY_PATH APPLE_API_PRIVATE_KEY APPLE_ID APPLE_PASSWORD APPLE_TEAM_ID \
  WINDOWS_CERTIFICATE WINDOWS_CERTIFICATE_PASSWORD AZURE_CLIENT_SECRET; do
  if [ -n "${!name:-}" ]; then
    echo "bundle: $name is set; the build must never see a signing secret" >&2
    exit 1
  fi
done

cd "$(dirname "$0")/.."
mkdir -p "$out" target
out=$(cd "$out" && pwd)

case "$platform" in
  linux-amd64) triple=x86_64-unknown-linux-gnu bundles=deb,appimage ;;
  linux-arm64) triple=aarch64-unknown-linux-gnu bundles=deb,appimage ;;
  darwin-universal) triple=universal-apple-darwin bundles=app ;;
  windows-amd64) triple=x86_64-pc-windows-msvc bundles=nsis ;;
  *)
    echo "bundle: unknown platform '$platform'" >&2
    exit 2
    ;;
esac

# Tauri copies binaries/baum-<triple> into the bundle; a universal macOS
# build compiles each architecture and needs all three.
sidecars=("binaries/baum-$triple")
case "$platform" in
  darwin-universal) sidecars+=(binaries/baum-aarch64-apple-darwin binaries/baum-x86_64-apple-darwin) ;;
  windows-amd64) sidecars=("binaries/baum-$triple.exe") ;;
esac
for sidecar in "${sidecars[@]}"; do
  if [ ! -s "$sidecar" ]; then
    echo "bundle: $sidecar is missing; build it with scripts/sidecar.sh" >&2
    exit 1
  fi
done

case "$platform" in
  # linuxdeploy is itself an AppImage, and containers have no FUSE.
  linux-*) export APPIMAGE_EXTRACT_AND_RUN=1 ;;
esac

# Updater bundles stay off (tauri.conf.json): the AppImage and the NSIS
# installer are themselves what the updater installs, and the macOS archive
# is made from the signed app by macos-finalize.sh.
cargo tauri build --ci --no-sign --target "$triple" --bundles "$bundles" \
  --config "{\"version\": \"$version\"}" -- --locked

bundle=target/$triple/release/bundle

# The single file a glob matches.
one() {
  local matches=()
  local candidate
  for candidate in "$@"; do
    if [ -e "$candidate" ]; then matches+=("$candidate"); fi
  done
  if [ "${#matches[@]}" -ne 1 ]; then
    echo "bundle: expected one file matching $*, found ${#matches[@]}" >&2
    exit 1
  fi
  printf '%s\n' "${matches[0]}"
}

name=baum-desktop-$platform
case "$platform" in
  linux-*)
    cp "$(one "$bundle"/deb/baum_"$version"_*.deb)" "$out/$name.deb"
    cp "$(one "$bundle"/appimage/baum_"$version"_*.AppImage)" "$out/$name.AppImage"
    ;;
  darwin-universal)
    lipo "$bundle/macos/baum.app/Contents/MacOS/baum" -verify_arch arm64 x86_64
    lipo "$bundle/macos/baum.app/Contents/MacOS/baum-desktop" -verify_arch arm64 x86_64
    COPYFILE_DISABLE=1 tar -cf "$out/$name.app.tar" -C "$bundle/macos" baum.app
    ;;
  windows-amd64)
    cp "$(one "$bundle"/nsis/baum_"$version"_*-setup.exe)" "$out/$name-setup.exe"
    ;;
esac

echo "bundle: $platform $version (unsigned)"
ls -l "$out"
