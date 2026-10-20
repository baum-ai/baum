#!/usr/bin/env bash
# Builds the desktop bundles for one platform and copies the ones a release
# publishes into OUT, under their release names:
#
#   scripts/bundle.sh PLATFORM VERSION OUT
#
# PLATFORM is linux-amd64, linux-arm64, darwin-universal or windows-amd64.
# VERSION is the release version without the leading v; snapshot builds use
# 0.0.0, which never checks for updates. Run it from desktop/ with the bundled
# baum binaries already in binaries/ (scripts/sidecar.sh, or the release's
# own binaries). The release workflow runs it on each platform; on Linux it
# runs in the desktop-test image (Dockerfile).
#
# What is signed depends on the environment. Each kind of signing is skipped
# when its variables are unset:
#
#   TAURI_SIGNING_PRIVATE_KEY, TAURI_SIGNING_PRIVATE_KEY_PASSWORD
#       sign the updater bundles (writes a .sig next to each)
#   APPLE_SIGNING_IDENTITY
#       Developer ID identity, in a keychain on the search list, that signs
#       the app and the bundled baum with the hardened runtime (macOS)
#   APPLE_API_ISSUER, APPLE_API_KEY, APPLE_API_KEY_PATH
#   or APPLE_ID, APPLE_PASSWORD, APPLE_TEAM_ID
#       notarise and staple the app and the disk image (macOS)
#   BAUM_WINDOWS_SIGN=1
#       sign the Windows binaries and installer with scripts/windows-sign.ps1
#
# A macOS or Windows bundle the operating system will not trust as published
# has -unsigned (or, signed but not notarised, -unnotarized) in its name.
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

cd "$(dirname "$0")/.."
mkdir -p "$out" target
out=$(cd "$out" && pwd)

case "$platform" in
  linux-amd64) triple=x86_64-unknown-linux-gnu bundles=deb,appimage ;;
  linux-arm64) triple=aarch64-unknown-linux-gnu bundles=deb,appimage ;;
  darwin-universal) triple=universal-apple-darwin bundles=app,dmg ;;
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

json_string() {
  local s=${1//\\/\\\\}
  printf '"%s"' "${s//\"/\\\"}"
}

updater=false
if [ -n "${TAURI_SIGNING_PRIVATE_KEY:-}" ]; then updater=true; fi

# Settings only this build has, merged over tauri.conf.json.
macos='"signingIdentity": "-"'
identity=${APPLE_SIGNING_IDENTITY:-}
notarize=false
trust=unsigned
if [ "$platform" = darwin-universal ] && [ -n "$identity" ]; then
  macos="\"signingIdentity\": $(json_string "$identity")"
  trust=unnotarized
  if [ -n "${APPLE_API_ISSUER:-}" ] && [ -n "${APPLE_API_KEY:-}" ] && [ -n "${APPLE_API_KEY_PATH:-}" ]; then
    notarize=true
  elif [ -n "${APPLE_ID:-}" ] && [ -n "${APPLE_PASSWORD:-}" ] && [ -n "${APPLE_TEAM_ID:-}" ]; then
    notarize=true
  fi
  if [ "$notarize" = true ]; then trust=trusted; fi
fi
windows='{}'
if [ "$platform" = windows-amd64 ] && [ "${BAUM_WINDOWS_SIGN:-}" = 1 ]; then
  sign_script=$(cygpath -w "$PWD/scripts/windows-sign.ps1")
  windows="{\"signCommand\": {\"cmd\": \"pwsh\", \"args\": [\"-NoProfile\", \"-NonInteractive\", \"-File\", $(json_string "$sign_script"), \"%1\"]}}"
  trust=trusted
fi
if [ "$platform" = linux-amd64 ] || [ "$platform" = linux-arm64 ]; then trust=trusted; fi

config=target/bundle-config.json
cat >"$config" <<EOF
{
  "version": "$version",
  "bundle": {
    "createUpdaterArtifacts": $updater,
    "macOS": {$macos},
    "windows": $windows
  }
}
EOF

# The bundled baum is signed before Tauri copies it, so it carries the
# Developer ID signature and hardened runtime even if the bundler leaves it
# as it is.
if [ "$platform" = darwin-universal ] && [ -n "$identity" ]; then
  codesign --force --options runtime --timestamp --sign "$identity" "binaries/baum-$triple"
fi
if [ "$platform" = windows-amd64 ] && [ "${BAUM_WINDOWS_SIGN:-}" = 1 ]; then
  pwsh -NoProfile -NonInteractive -File scripts/windows-sign.ps1 "binaries/baum-$triple.exe"
fi

case "$platform" in
  # linuxdeploy is itself an AppImage, and containers have no FUSE.
  linux-*) export APPIMAGE_EXTRACT_AND_RUN=1 ;;
esac

cargo tauri build --ci --target "$triple" --bundles "$bundles" --config "$config" -- --locked

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

suffix=
case "$trust" in
  unsigned) suffix=-unsigned ;;
  unnotarized) suffix=-unnotarized ;;
esac
name=baum-desktop-$platform$suffix

# Copies an updater bundle and, when the build signed it, its signature.
publish_updater() {
  local from=$1 to=$2
  cp "$from" "$out/$to"
  if [ "$updater" = true ]; then
    if [ ! -s "$from.sig" ]; then
      echo "bundle: the updater signature $from.sig is missing" >&2
      exit 1
    fi
    cp "$from.sig" "$out/$to.sig"
  fi
}

case "$platform" in
  linux-*)
    cp "$(one "$bundle"/deb/baum_"$version"_*.deb)" "$out/$name.deb"
    publish_updater "$(one "$bundle"/appimage/baum_"$version"_*.AppImage)" "$name.AppImage"
    ;;
  darwin-universal)
    app=$bundle/macos/baum.app
    dmg=$(one "$bundle"/dmg/baum_"$version"_*.dmg)
    if [ -n "$identity" ]; then
      codesign --verify --strict --deep --verbose=2 "$app"
      for binary in "$app/Contents/MacOS/baum-desktop" "$app/Contents/MacOS/baum"; do
        details=$(codesign --display --verbose=4 "$binary" 2>&1)
        if ! grep -q '^Authority=Developer ID Application' <<<"$details" ||
          ! grep -q 'flags=.*runtime' <<<"$details"; then
          echo "bundle: $binary is not signed with a Developer ID and the hardened runtime" >&2
          exit 1
        fi
      done
      lipo "$app/Contents/MacOS/baum" -verify_arch arm64 x86_64
    fi
    if [ "$notarize" = true ]; then
      # Tauri notarised and stapled the app; the disk image is signed,
      # notarised and stapled here.
      xcrun stapler validate "$app"
      codesign --force --timestamp --sign "$identity" "$dmg"
      if [ -n "${APPLE_API_KEY_PATH:-}" ]; then
        xcrun notarytool submit "$dmg" --wait --timeout 30m \
          --key "$APPLE_API_KEY_PATH" --key-id "$APPLE_API_KEY" --issuer "$APPLE_API_ISSUER"
      else
        xcrun notarytool submit "$dmg" --wait --timeout 30m \
          --apple-id "$APPLE_ID" --password "$APPLE_PASSWORD" --team-id "$APPLE_TEAM_ID"
      fi
      xcrun stapler staple "$dmg"
      xcrun stapler validate "$dmg"
      spctl --assess --type open --context context:primary-signature --verbose=2 "$dmg"
    fi
    cp "$dmg" "$out/$name.dmg"
    if [ "$updater" = true ]; then
      publish_updater "$bundle/macos/baum.app.tar.gz" "$name.app.tar.gz"
    fi
    ;;
  windows-amd64)
    installer=$(one "$bundle"/nsis/baum_"$version"_*-setup.exe)
    if [ "${BAUM_WINDOWS_SIGN:-}" = 1 ]; then
      pwsh -NoProfile -NonInteractive -Command \
        "\$s = Get-AuthenticodeSignature -LiteralPath '$installer'; if (\$s.Status -ne 'Valid') { throw \"installer signature: \$(\$s.Status)\" }"
    fi
    publish_updater "$installer" "$name-setup.exe"
    ;;
esac

echo "bundle: $platform $version (updater signatures: $updater, operating system trust: $trust)"
ls -l "$out"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### Desktop $platform $version"
    echo
    echo "- Updater signatures: $updater"
    echo "- Operating system signing: $trust"
    echo
    for file in "$out"/*; do echo "- \`$(basename "$file")\`"; done
  } >>"$GITHUB_STEP_SUMMARY"
fi
