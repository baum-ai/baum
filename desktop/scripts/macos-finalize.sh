#!/usr/bin/env bash
# Signs, notarises and packages the macOS app that scripts/bundle.sh built
# unsigned:
#
#   scripts/macos-finalize.sh APP_TAR VERSION OUT
#
# It runs only Apple's tools (codesign, notarytool, stapler, hdiutil, spctl,
# ditto), tar and scripts/update-yml.mjs (Node.js, no dependencies), never the
# build, so the release runs it in a job that holds the Apple secrets and
# compiles nothing. OUT receives
#
#   baum-desktop-darwin-universal[-unsigned|-unnotarized].dmg
#   baum-desktop-darwin-universal[-unsigned|-unnotarized].zip
#   baum-desktop-darwin-universal[-unsigned|-unnotarized].app.tar.gz
#   latest-mac.yml
#
# The zip is what electron-updater installs, and latest-mac.yml describes it;
# the .app.tar.gz is the same app in the archive the Tauri updater installs.
# Sign both with scripts/updater-sign.sh. The suffix marks a bundle
# Gatekeeper will not trust as downloaded.
#
#   APPLE_SIGNING_IDENTITY
#       a Developer ID Application identity in a keychain on the search list;
#       without it the app is signed ad hoc, which Apple silicon requires
#   APPLE_API_ISSUER, APPLE_API_KEY, APPLE_API_KEY_PATH
#   or APPLE_ID, APPLE_PASSWORD, APPLE_TEAM_ID
#       notarise and staple the app and the disk image (needs the identity)
#
# Every Mach-O file and nested bundle is signed inside out (the deepest
# first, as @electron/osx-sign does), then the app, with the hardened runtime
# and macos/entitlements.plist, which allows the JIT V8 needs and nothing
# else; there is no App Sandbox. Ad hoc signing leaves the hardened runtime
# off: its library validation would refuse Electron's ad hoc signed framework.
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
# regular files, directories and symbolic links inside baum.app/, so
# extracting it cannot write anywhere else. Electron's frameworks need their
# links; tar does not extract through one, and every link is checked below
# to stay inside the app.
names=$(tar -tf "$app_tar")
types=$(tar -tvf "$app_tar" | cut -c1)
if [ "$(wc -l <<<"$names")" -ne "$(wc -l <<<"$types")" ]; then
  echo "macos-finalize: $app_tar has entries whose names cannot be listed one per line" >&2
  exit 1
fi
while IFS=$'\t' read -r type entry; do
  case "$type" in
    - | d | l) ;;
    *)
      echo "macos-finalize: $app_tar holds $entry, which is not a regular file, directory or link" >&2
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
# A link may only point within the app: relative, without "..".
while IFS= read -r -d '' link; do
  target=$(readlink "$link")
  case "/$target/" in
    //* | */../*)
      echo "macos-finalize: ${link#"$work/"} links to $target, outside the app" >&2
      exit 1
      ;;
  esac
done < <(find "$app" -type l -print0)
main=$app/Contents/MacOS/baum-desktop
sidecar=$app/Contents/Resources/bin/baum
# One architecture per call: Xcode 27's lipo takes no more.
for binary in "$main" "$sidecar"; do
  for arch in arm64 x86_64; do
    lipo "$binary" -verify_arch "$arch"
  done
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

# What to sign, deepest first: every Mach-O file (Electron's framework
# binaries and dylibs, the helpers' executables, crashpad, Squirrel's ShipIt,
# the bundled baum) and every nested .app and .framework bundle; then the app
# itself, which signs its main executable.
signables=()
while IFS= read -r -d '' path; do
  if [ -d "$path" ] || file -b "$path" | grep -q '^Mach-O'; then
    signables+=("$path")
  fi
done < <(find "$app/Contents" \( -type d \( -name '*.app' -o -name '*.framework' \) -o -type f \) -print0)
ordered=()
while IFS=$'\t' read -r _ path; do
  ordered+=("$path")
done < <(for path in "${signables[@]}"; do
  slashes=${path//[^\/]/}
  printf '%d\t%s\n' "${#slashes}" "$path"
done | sort -s -t $'\t' -k1,1nr)
listed=$(printf '%s\n' "${ordered[@]}")
for required in "$main" "$sidecar" "$app/Contents/Frameworks/Electron Framework.framework" \
  "$app/Contents/Frameworks/baum Helper.app"; do
  if ! grep -qxF "$required" <<<"$listed"; then
    echo "macos-finalize: ${required#"$work/"} is not among what is signed" >&2
    exit 1
  fi
done

entitlements=$(cd "$(dirname "$0")/.." && pwd)/macos/entitlements.plist
if [ -n "$identity" ]; then
  for path in "${ordered[@]}" "$app"; do
    codesign --force --options runtime --timestamp --entitlements "$entitlements" \
      --sign "$identity" "$path"
  done
  codesign --verify --strict --deep --verbose=2 "$app"
  for path in "${ordered[@]}" "$app"; do
    details=$(codesign --display --verbose=4 "$path" 2>&1)
    if ! grep -q '^Authority=Developer ID Application' <<<"$details" ||
      ! grep -q 'flags=.*runtime' <<<"$details"; then
      echo "macos-finalize: ${path#"$work/"} is not signed with a Developer ID and the hardened runtime" >&2
      exit 1
    fi
  done
else
  for path in "${ordered[@]}" "$app"; do
    codesign --force --sign - "$path"
  done
  codesign --verify --strict --deep "$app"
fi

# stapler staple and stapler validate both fetch the ticket from Apple's
# CloudKit service, which now and then drops the connection or does not have a
# ticket yet just after notarisation (error 68), so a failed attempt is tried
# again a little later.
stapler_retry() {
  local attempt
  for attempt in 1 2 3 4; do
    if xcrun stapler "$@"; then
      return 0
    fi
    if [ "$attempt" = 4 ]; then
      exit 1
    fi
    echo "stapler $1 failed (attempt $attempt); trying again" >&2
    sleep $((attempt * 20))
  done
}

if [ "$notarize" = true ]; then
  ditto -c -k --keepParent "$app" "$work/baum.zip"
  notarize_file "$work/baum.zip"
  stapler_retry staple "$app"
  stapler_retry validate "$app"
  spctl --assess --type execute --verbose=2 "$app"
fi

# hdiutil now and then fails with "Resource busy" while macOS still holds a
# volume it mounted, so a failed attempt is tried again a little later.
hdiutil_retry() {
  local attempt
  for attempt in 1 2 3; do
    if hdiutil "$@"; then
      return 0
    fi
    if [ "$attempt" = 3 ]; then
      exit 1
    fi
    echo "hdiutil $1 failed (attempt $attempt); trying again" >&2
    sleep $((attempt * 10))
  done
}

# The disk image: the app and a link to /Applications, laid out by
# macos/dmg.DS_Store and macos/dmg-background.tiff (scripts/dmg-layout.py) as
# a small window with large icons and an arrow between them, and the app icon
# as the mounted volume's icon.
desktop=$(cd "$(dirname "$0")/.." && pwd)
mkdir "$work/dmg"
ditto "$app" "$work/dmg/baum.app"
ln -s /Applications "$work/dmg/Applications"
cp "$desktop/macos/dmg.DS_Store" "$work/dmg/.DS_Store"
cp "$desktop/macos/dmg-background.tiff" "$work/dmg/.background.tiff"
cp "$desktop/icons/icon.icns" "$work/dmg/.VolumeIcon.icns"
# Finder uses .VolumeIcon.icns only when the volume root has the custom icon
# flag, which only a mounted, writable image can take. The image is attached
# without Finder seeing it, so Finder cannot rewrite the layout.
hdiutil_retry create -volname baum -srcfolder "$work/dmg" -fs HFS+ -format UDRW -ov "$work/rw.dmg"
mkdir "$work/mount"
hdiutil_retry attach -nobrowse -noautoopen -mountpoint "$work/mount" "$work/rw.dmg" >/dev/null
# Finder info is 32 bytes with the flags word at offset 8; 0x0400 is
# kHasCustomIcon.
xattr -wx com.apple.FinderInfo \
  0000000000000000040000000000000000000000000000000000000000000000 "$work/mount"
rm -rf "$work/mount/.fseventsd"
hdiutil_retry detach "$work/mount" -quiet
hdiutil_retry convert "$work/rw.dmg" -format UDZO -ov -o "$out/$name.dmg"
if [ -n "$identity" ]; then
  codesign --force --timestamp --sign "$identity" "$out/$name.dmg"
fi
if [ "$notarize" = true ]; then
  notarize_file "$out/$name.dmg"
  stapler_retry staple "$out/$name.dmg"
  stapler_retry validate "$out/$name.dmg"
  spctl --assess --type open --context context:primary-signature --verbose=2 "$out/$name.dmg"
fi

# What electron-updater installs (Squirrel.Mac): the stapled app in a zip,
# with its links, as electron-builder makes it; latest-mac.yml gives its
# SHA-512 and size.
ditto -c -k --sequesterRsrc --keepParent "$app" "$out/$name.zip"
node "$(dirname "$0")/update-yml.mjs" write "$version" "$out" latest-mac.yml "$name.zip" >/dev/null

# What the Tauri updater installs: the app at the root of a gzipped tar, as
# the Tauri bundler makes it, without macOS metadata files.
COPYFILE_DISABLE=1 tar --no-mac-metadata -czf "$out/$name.app.tar.gz" -C "$work" baum.app

developer_id=no
if [ -n "$identity" ]; then developer_id=yes; fi
echo "macos-finalize: $version as $name (Developer ID: $developer_id, notarised: $notarize)"
ls -l "$out"
