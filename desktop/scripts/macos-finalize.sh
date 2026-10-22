#!/usr/bin/env bash
# Signs, notarises and packages the macOS app that scripts/bundle.sh built
# unsigned:
#
#   scripts/macos-finalize.sh APP_TAR VERSION OUT
#
# It runs only Apple's tools (codesign, notarytool, stapler, hdiutil, spctl)
# and tar, never the build, so the release runs it in a job that holds the
# Apple secrets and compiles nothing. OUT receives
#
#   baum-desktop-darwin-universal[-unsigned|-unnotarized].dmg
#   baum-desktop-darwin-universal[-unsigned|-unnotarized].app.tar.gz
#
# the second being what the updater installs (sign it with
# scripts/updater-sign.sh). The suffix marks a bundle Gatekeeper will not
# trust as downloaded.
#
#   APPLE_SIGNING_IDENTITY
#       a Developer ID Application identity in a keychain on the search list;
#       without it the app is signed ad hoc, which Apple silicon requires
#   APPLE_API_ISSUER, APPLE_API_KEY, APPLE_API_KEY_PATH
#   or APPLE_ID, APPLE_PASSWORD, APPLE_TEAM_ID
#       notarise and staple the app and the disk image (needs the identity)
#
# Every executable is signed with the hardened runtime and no entitlements,
# so there is no App Sandbox.
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: scripts/macos-finalize.sh APP_TAR VERSION OUT" >&2
  exit 2
fi
app_tar=$1
version=$2
out=$3
mkdir -p "$out"
out=$(cd "$out" && pwd)

identity=${APPLE_SIGNING_IDENTITY:-}
# How to authenticate to the notary service: an App Store Connect API key
# (preferred: no secret on a command line, only the key file's path) or an
# Apple ID with an app-specific password, which notarytool takes only as an
# argument.
notary=
if [ -n "$identity" ]; then
  if [ -n "${APPLE_API_ISSUER:-}" ] && [ -n "${APPLE_API_KEY:-}" ] && [ -n "${APPLE_API_KEY_PATH:-}" ]; then
    notary=api-key
  elif [ -n "${APPLE_ID:-}" ] && [ -n "${APPLE_PASSWORD:-}" ] && [ -n "${APPLE_TEAM_ID:-}" ]; then
    notary=apple-id
  fi
fi
notarize=false
if [ -n "$notary" ]; then notarize=true; fi
name=baum-desktop-darwin-universal
if [ -z "$identity" ]; then
  name=$name-unsigned
elif [ "$notarize" = false ]; then
  name=$name-unnotarized
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The archive came from a job that ran third-party build code: accept only
# regular files and directories inside baum.app/, so extracting it cannot
# write anywhere else or plant a link.
names=$(tar -tf "$app_tar")
types=$(tar -tvf "$app_tar" | cut -c1)
if [ "$(wc -l <<<"$names")" -ne "$(wc -l <<<"$types")" ]; then
  echo "macos-finalize: $app_tar has entries whose names cannot be listed one per line" >&2
  exit 1
fi
while IFS=$'\t' read -r type entry; do
  case "$type" in
    - | d) ;;
    *)
      echo "macos-finalize: $app_tar holds $entry, which is not a regular file or directory" >&2
      exit 1
      ;;
  esac
  case "/$entry/" in
    */../* | */./*)
      echo "macos-finalize: $app_tar holds the path $entry" >&2
      exit 1
      ;;
  esac
  case "$type:$entry" in
    d:baum.app | ?:baum.app/ | ?:baum.app/?*) ;;
    *)
      echo "macos-finalize: $app_tar holds $entry, outside baum.app/" >&2
      exit 1
      ;;
  esac
done < <(paste <(printf '%s\n' "$types") <(printf '%s\n' "$names"))

tar -xf "$app_tar" -C "$work"
app=$work/baum.app
main=$app/Contents/MacOS/baum-desktop
sidecar=$app/Contents/MacOS/baum
for binary in "$main" "$sidecar"; do
  lipo "$binary" -verify_arch arm64 x86_64
done

notarize_file() {
  local file=$1
  local result=$work/notarization.json
  if [ "$notary" = api-key ]; then
    xcrun notarytool submit "$file" --wait --timeout 30m --output-format json \
      --key "$APPLE_API_KEY_PATH" --key-id "$APPLE_API_KEY" --issuer "$APPLE_API_ISSUER" >"$result"
  else
    xcrun notarytool submit "$file" --wait --timeout 30m --output-format json \
      --apple-id "$APPLE_ID" --password "$APPLE_PASSWORD" --team-id "$APPLE_TEAM_ID" >"$result"
  fi
  local status
  status=$(plutil -extract status raw -o - "$result")
  if [ "$status" != Accepted ]; then
    echo "macos-finalize: notarisation of $(basename "$file") ended '$status':" >&2
    cat "$result" >&2
    exit 1
  fi
  echo "notarised $(basename "$file")"
}

# Inside out: the bundled baum, then the app, which signs its main executable.
if [ -n "$identity" ]; then
  codesign --force --options runtime --timestamp --sign "$identity" "$sidecar"
  codesign --force --options runtime --timestamp --sign "$identity" "$app"
  codesign --verify --strict --deep --verbose=2 "$app"
  for binary in "$main" "$sidecar"; do
    details=$(codesign --display --verbose=4 "$binary" 2>&1)
    if ! grep -q '^Authority=Developer ID Application' <<<"$details" ||
      ! grep -q 'flags=.*runtime' <<<"$details"; then
      echo "macos-finalize: $binary is not signed with a Developer ID and the hardened runtime" >&2
      exit 1
    fi
  done
else
  codesign --force --sign - "$sidecar"
  codesign --force --sign - "$app"
  codesign --verify --strict --deep "$app"
fi

if [ "$notarize" = true ]; then
  ditto -c -k --keepParent "$app" "$work/baum.zip"
  notarize_file "$work/baum.zip"
  xcrun stapler staple "$app"
  xcrun stapler validate "$app"
  spctl --assess --type execute --verbose=2 "$app"
fi

# The disk image: the app and a link to /Applications.
mkdir "$work/dmg"
cp -R "$app" "$work/dmg/"
ln -s /Applications "$work/dmg/Applications"
hdiutil create -volname baum -srcfolder "$work/dmg" -fs HFS+ -format UDZO -ov "$out/$name.dmg"
if [ -n "$identity" ]; then
  codesign --force --timestamp --sign "$identity" "$out/$name.dmg"
fi
if [ "$notarize" = true ]; then
  notarize_file "$out/$name.dmg"
  xcrun stapler staple "$out/$name.dmg"
  xcrun stapler validate "$out/$name.dmg"
  spctl --assess --type open --context context:primary-signature --verbose=2 "$out/$name.dmg"
fi

# What the updater installs: the app at the root of a gzipped tar, as the
# Tauri bundler makes it, without macOS metadata files.
COPYFILE_DISABLE=1 tar --no-mac-metadata -czf "$out/$name.app.tar.gz" -C "$work" baum.app

developer_id=no
if [ -n "$identity" ]; then developer_id=yes; fi
echo "macos-finalize: $version as $name (Developer ID: $developer_id, notarised: $notarize)"
ls -l "$out"
