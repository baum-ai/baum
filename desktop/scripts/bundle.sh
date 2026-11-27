#!/usr/bin/env bash
# Builds the desktop bundles for one platform with electron-builder, unsigned,
# and copies them into OUT:
#
#   scripts/bundle.sh [--dir] PLATFORM VERSION OUT
#
# PLATFORM is linux-amd64, linux-arm64, darwin-universal or windows-amd64.
# VERSION is the release version without the leading v; snapshot builds use
# 0.0.0, which never checks for updates. Run it from desktop/ with the
# bundled screens in dist/ (`just desktop::frontend`) and the bundled baum
# binaries in binaries/ (scripts/sidecar.sh, or the release's own binaries;
# darwin-universal takes binaries/baum-universal-apple-darwin, which lipo
# makes from the two darwin builds). On Linux it runs in this directory's
# Docker image (Dockerfile). --dir builds only the unpacked app, in
# target/electron-builder, checks it and copies nothing.
#
# This step installs the npm dependencies and runs electron-builder, which
# downloads Electron and its own tools (AppImage runtime, fpm, NSIS). It never
# sees a signing secret and refuses to run when one is in its environment.
# Signing happens afterwards on what it produced, with the platform's own
# tools (scripts/macos-finalize.sh, scripts/windows-sign.ps1) and minisign
# (scripts/updater-sign.sh), in release jobs that build nothing.
#
# OUT receives:
#   linux-*           baum-desktop-linux-<arch>.deb, baum-desktop-linux-<arch>.AppImage,
#                     latest-linux.yml (amd64) or latest-linux-arm64.yml
#   darwin-universal  baum-desktop-darwin-universal.app.tar (the unsigned app)
#   windows-amd64     baum-desktop-windows-amd64-setup.exe (unsigned), latest.yml
set -euo pipefail

dir_only=false
if [ "${1:-}" = --dir ]; then
  dir_only=true
  shift
fi
if [ "$#" -ne 3 ]; then
  echo "usage: scripts/bundle.sh [--dir] PLATFORM VERSION OUT" >&2
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

# electron-builder signs on its own when it finds CSC_* or a keychain
# identity; none of that may reach this step.
for name in TAURI_SIGNING_PRIVATE_KEY TAURI_SIGNING_PRIVATE_KEY_PASSWORD TAURI_SIGNING_PRIVATE_KEY_PATH \
  APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_SIGNING_IDENTITY APPLE_API_ISSUER APPLE_API_KEY \
  APPLE_API_KEY_ID APPLE_API_KEY_PATH APPLE_API_PRIVATE_KEY APPLE_ID APPLE_PASSWORD \
  APPLE_APP_SPECIFIC_PASSWORD APPLE_TEAM_ID APPLE_KEYCHAIN APPLE_KEYCHAIN_PROFILE \
  CSC_LINK CSC_KEY_PASSWORD CSC_NAME WIN_CSC_LINK WIN_CSC_KEY_PASSWORD \
  WINDOWS_CERTIFICATE WINDOWS_CERTIFICATE_PASSWORD WINDOWS_CERTIFICATE_PATH AZURE_CLIENT_SECRET \
  GH_TOKEN GITHUB_TOKEN; do
  if [ -n "${!name:-}" ]; then
    echo "bundle: $name is set; the build must never see a signing secret or a token" >&2
    exit 1
  fi
done
export CSC_IDENTITY_AUTO_DISCOVERY=false

cd "$(dirname "$0")/.."
mkdir -p "$out" target
out=$(cd "$out" && pwd)

# electron-builder's platform flag and targets, its architecture, the
# bundled baum, and where the unpacked app lands.
exe=
name=baum-desktop-$platform
case "$platform" in
  linux-amd64) os=--linux arch=--x64 sidecar=binaries/baum-x86_64-unknown-linux-gnu unpacked=linux-unpacked ;;
  linux-arm64) os=--linux arch=--arm64 sidecar=binaries/baum-aarch64-unknown-linux-gnu unpacked=linux-arm64-unpacked ;;
  darwin-universal) os=--mac arch=--universal sidecar=binaries/baum-universal-apple-darwin unpacked=mac-universal ;;
  windows-amd64) os=--win arch=--x64 sidecar=binaries/baum-x86_64-pc-windows-msvc.exe unpacked=win-unpacked exe=.exe ;;
  *)
    echo "bundle: unknown platform '$platform'" >&2
    exit 2
    ;;
esac
if [ "$dir_only" = true ]; then
  flags=("$os" dir "$arch")
else
  case "$platform" in
    linux-*) flags=("$os" deb AppImage "$arch" -c.deb.artifactName="$name.deb" -c.appImage.artifactName="$name.AppImage") ;;
    darwin-universal) flags=("$os" dir "$arch") ;;
    windows-amd64) flags=("$os" nsis "$arch" -c.nsis.artifactName="$name-setup.exe") ;;
  esac
fi

if [ ! -s "$sidecar" ]; then
  echo "bundle: $sidecar is missing; build it with scripts/sidecar.sh (and lipo for macOS)" >&2
  exit 1
fi
if [ ! -f dist/index.html ]; then
  echo "bundle: dist/ is not built; run 'just desktop::frontend' (or pnpm --dir ../app build) first" >&2
  exit 1
fi

# electron-builder.mjs takes the bundled baum from here, and on Linux the
# deb's /usr/bin/baum link.
stage=target/electron-stage
rm -rf "$stage" target/electron-builder
mkdir -p "$stage/bin"
cp "$sidecar" "$stage/bin/baum$exe"
chmod 0755 "$stage/bin/baum$exe"
case "$platform" in
  linux-*)
    mkdir -p "$stage/deb"
    ln -s /opt/baum/resources/bin/baum "$stage/deb/baum"
    ;;
esac

# PNPM_STORE_DIR names a shared store (the Docker service's, CI's cache).
pnpm install --frozen-lockfile ${PNPM_STORE_DIR:+--store-dir "$PNPM_STORE_DIR"}
pnpm build
pnpm exec electron-builder --config electron-builder.mjs --publish never \
  -c.extraMetadata.version="$version" "${flags[@]}"

builder=target/electron-builder
app=$builder/$unpacked
resources=$app/resources
case "$platform" in
  darwin-universal) resources=$app/baum.app/Contents/Resources ;;
esac

# The bundled baum is a file of its own, executable, outside the asar archive.
if [ ! -f "$resources/bin/baum$exe" ] || { [ -z "$exe" ] && [ ! -x "$resources/bin/baum$exe" ]; }; then
  echo "bundle: $resources/bin/baum$exe is missing or not executable" >&2
  exit 1
fi
if ! cmp -s "$sidecar" "$resources/bin/baum$exe"; then
  echo "bundle: $resources/bin/baum$exe is not $sidecar" >&2
  exit 1
fi

# Video plays through Electron's own ffmpeg library, the build of it
# Chromium ships with Electron's codecs. The only ffmpeg library in the
# bundle must be that one, where Electron puts it; nothing else may carry
# ffmpeg (libav).
case "$platform" in
  linux-*) allowed="$app/libffmpeg.so" ;;
  darwin-universal) allowed="$app/baum.app/Contents/Frameworks/Electron Framework.framework/Versions/A/Libraries/libffmpeg.dylib" ;;
  windows-amd64) allowed="$app/ffmpeg.dll" ;;
esac
if [ ! -f "$allowed" ]; then
  echo "bundle: Electron's ffmpeg library ($allowed) is missing" >&2
  exit 1
fi
while IFS= read -r -d '' library; do
  if [ "$library" != "$allowed" ]; then
    echo "bundle: $library is an ffmpeg library other than Electron's" >&2
    exit 1
  fi
done < <(find "$app" \( -iname '*ffmpeg*' -o -iname 'libav*' -o -iname 'avcodec*' -o -iname 'avformat*' \
  -o -iname 'libgst*' \) \( -type f -o -type l \) -print0)

# The Electron fuses electron-builder.mjs sets (no ELECTRON_RUN_AS_NODE,
# NODE_OPTIONS or --inspect; code only from app.asar) are flipped in the
# executable.
case "$platform" in
  linux-*) executable="$app/baum-desktop" ;;
  darwin-universal) executable="$app/baum.app" ;;
  windows-amd64) executable="$app/baum.exe" ;;
esac
node scripts/check-fuses.mjs "$executable"

if [ "$dir_only" = true ]; then
  echo "bundle: $platform $version unpacked in $app (unsigned)"
  exit 0
fi

# electron-updater names its download cache from app-update.yml; without it
# no update could be downloaded.
if ! grep -q '^updaterCacheDirName: ' "$resources/app-update.yml" 2>/dev/null; then
  echo "bundle: $resources/app-update.yml is missing or names no updater cache" >&2
  exit 1
fi

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

# Checks the AppImage type 2 magic ("AI\x02" at offset 8). Desktop
# integration tools (AppImageLauncher, Gear Lever) recognise an AppImage by
# it, and open one that is not executable when it is double-clicked. See
# "Linux" in the README for the AppImageLauncher 2.x rule it also triggers.
check_appimage_magic() {
  local magic
  magic=$(od -An -tx1 -j8 -N3 "$1" | tr -d ' \n')
  if [ "$magic" != 414902 ]; then
    echo "bundle: $1 has no AppImage type 2 magic at offset 8 (found '$magic')" >&2
    exit 1
  fi
}

yml=
case "$platform" in
  linux-*)
    yml=latest-linux.yml
    if [ "$platform" = linux-arm64 ]; then yml=latest-linux-arm64.yml; fi
    cp "$(one "$builder/$name.deb")" "$(one "$builder/$name.AppImage")" "$(one "$builder/$yml")" "$out/"
    check_appimage_magic "$out/$name.AppImage"
    contents=$(dpkg-deb --contents "$out/$name.deb")
    if ! grep -qF './usr/bin/baum -> /opt/baum/resources/bin/baum' <<<"$contents"; then
      echo "bundle: the deb does not link /usr/bin/baum to the bundled baum" >&2
      exit 1
    fi
    ;;
  darwin-universal)
    # One architecture per call: Xcode 27's lipo takes no more.
    for binary in MacOS/baum Resources/bin/baum; do
      for arch in arm64 x86_64; do
        lipo "$app/baum.app/Contents/$binary" -verify_arch "$arch"
      done
    done
    COPYFILE_DISABLE=1 tar -cf "$out/$name.app.tar" -C "$app" baum.app
    ;;
  windows-amd64)
    yml=latest.yml
    cp "$(one "$builder/$name-setup.exe")" "$(one "$builder/$yml")" "$out/"
    ;;
esac
# The update file electron-builder wrote must describe the copied bundles.
if [ -n "$yml" ]; then
  node scripts/update-yml.mjs check "$version" "$out" "$yml" >/dev/null
fi

echo "bundle: $platform $version (unsigned)"
ls -l "$out"
