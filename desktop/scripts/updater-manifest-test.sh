#!/usr/bin/env bash
# Tests scripts/updater-manifest.sh with a throwaway key: a correctly signed
# set of bundles passes, and every kind of tampering fails verification.
# Needs jq, minisign and base64 (the desktop-test image has them).
set -euo pipefail

manifest=$(cd "$(dirname "$0")" && pwd)/updater-manifest.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=$work/release
mkdir -p "$dir"

minisign -G -W -f -p "$work/key.pub" -s "$work/key" >/dev/null
minisign -G -W -f -p "$work/other.pub" -s "$work/other" >/dev/null
export UPDATER_PUBKEY
UPDATER_PUBKEY=$(base64 -w0 <"$work/key.pub")
export UPDATER_BASE_URL=https://example.invalid/download/v1.2.3

# Signs like the Tauri CLI: a minisign signature whose trusted comment names
# the file and version, base64 encoded into FILE.sig.
sign() {
  local key=$1 file=$2 version=$3
  minisign -S -s "$key" -m "$file" -x "$work/sig" \
    -t "$(printf 'timestamp:1700000000\tfile:%s\tversion:%s' "$(basename "$file")" "$version")" >/dev/null
  base64 -w0 <"$work/sig" >"$file.sig"
}

for name in baum-desktop-darwin-universal.app.tar.gz baum-desktop-linux-amd64.AppImage \
  baum-desktop-windows-amd64-setup.exe; do
  head -c 4096 /dev/urandom >"$dir/$name"
  sign "$work/key" "$dir/$name" 1.2.3
done
# Not an updater bundle; never listed.
echo deb >"$dir/baum-desktop-linux-amd64.deb"

status=0
pass() {
  if "$@" >"$work/out" 2>&1; then
    echo "ok   $*"
  else
    echo "FAIL expected success: $*" >&2
    cat "$work/out" >&2
    status=1
  fi
}
# fail WHY MESSAGE COMMAND...: COMMAND must fail and say MESSAGE.
fail() {
  local why=$1 message=$2
  shift 2
  if "$@" >"$work/out" 2>&1; then
    echo "FAIL expected a rejection ($why): $*" >&2
    cat "$work/out" >&2
    status=1
  elif ! grep -qF -- "$message" "$work/out"; then
    echo "FAIL rejected ($why), but not with '$message':" >&2
    cat "$work/out" >&2
    status=1
  else
    echo "ok   rejected: $why"
  fi
}

pass "$manifest" write 1.2.3 "$dir"
keys=$(jq -c '.platforms | keys' "$dir/latest.json")
if [ "$keys" != '["darwin-aarch64","darwin-x86_64","linux-x86_64","windows-x86_64"]' ]; then
  echo "FAIL unexpected platforms $keys" >&2
  status=1
fi
url=$(jq -r '.platforms["linux-x86_64"].url' "$dir/latest.json")
if [ "$url" != "$UPDATER_BASE_URL/baum-desktop-linux-amd64.AppImage" ]; then
  echo "FAIL unexpected URL $url" >&2
  status=1
fi
cp "$dir/latest.json" "$work/good.json"

# Rewrites latest.json with a jq filter, from the good manifest.
edit() {
  jq "$1" "$work/good.json" >"$dir/latest.json"
}

cp "$dir/baum-desktop-linux-amd64.AppImage" "$work/appimage"
printf x >>"$dir/baum-desktop-linux-amd64.AppImage"
fail "tampered bundle" "does not verify" "$manifest" verify "$dir"
cp "$work/appimage" "$dir/baum-desktop-linux-amd64.AppImage"
pass "$manifest" verify "$dir"

edit '.platforms["linux-x86_64"].signature = .platforms["windows-x86_64"].signature'
fail "signature of another bundle" "does not verify" "$manifest" verify "$dir"

cp "$dir/baum-desktop-windows-amd64-setup.exe" "$work/other-bundle"
sign "$work/other" "$work/other-bundle" 1.2.3
edit ".platforms[\"windows-x86_64\"].signature = \"$(cat "$work/other-bundle.sig")\""
fail "signature from another key" "does not verify" "$manifest" verify "$dir"

edit '.version = "1.2.4"'
fail "announced version the bundles were not signed for" "not version 1.2.4" "$manifest" verify "$dir"

# A genuine bundle of an older release, served as the new version.
darwin=$dir/baum-desktop-darwin-universal.app.tar.gz
cp "$darwin" "$work/darwin"
cp "$darwin.sig" "$work/darwin.sig"
head -c 4096 /dev/urandom >"$darwin"
sign "$work/key" "$darwin" 1.2.2
old=$(cat "$darwin.sig")
edit ".platforms[\"darwin-aarch64\"].signature = \"$old\" | .platforms[\"darwin-x86_64\"].signature = \"$old\""
fail "older release's bundle" "not version 1.2.3" "$manifest" verify "$dir"
cp "$work/darwin" "$darwin"
cp "$work/darwin.sig" "$darwin.sig"

edit '.platforms["linux-x86_64"].url = "http://example.invalid/download/v1.2.3/baum-desktop-linux-amd64.AppImage"'
fail "plain http URL" "not an https URL" "$manifest" verify "$dir"

edit 'del(.platforms["windows-x86_64"])'
fail "missing Windows" "has no windows-x86_64" "$manifest" verify "$dir"

cp "$work/good.json" "$dir/latest.json"
mv "$dir/baum-desktop-windows-amd64-setup.exe.sig" "$work/windows.sig"
fail "bundle without a signature" "has no updater signature" "$manifest" write 1.2.3 "$dir"
mv "$work/windows.sig" "$dir/baum-desktop-windows-amd64-setup.exe.sig"

head -c 4096 /dev/urandom >"$dir/baum-desktop-linux-arm64.AppImage"
sign "$work/key" "$dir/baum-desktop-linux-arm64.AppImage" 1.2.3
pass "$manifest" write 1.2.3 "$dir"
if [ "$(jq -r '.platforms["linux-aarch64"].url' "$dir/latest.json")" != "$UPDATER_BASE_URL/baum-desktop-linux-arm64.AppImage" ]; then
  echo "FAIL the arm64 AppImage is not listed" >&2
  status=1
fi

exit "$status"
