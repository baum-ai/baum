#!/usr/bin/env bash
# Tests scripts/updater-sign.sh and scripts/updater-manifest.sh with a
# throwaway, password-protected key: a correctly signed release passes, and
# every kind of tampering fails verification. Needs jq, minisign and base64
# (the desktop-test image has them); MINISIGN names another minisign, such as
# the pinned one Check installs on macOS and Windows.
set -euo pipefail

scripts=$(cd "$(dirname "$0")" && pwd)
manifest=$scripts/updater-manifest.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=$work/release
mkdir -p "$dir"
base=https://github.com/baum-ai/baum/releases/download/v1.2.3

minisign=${MINISIGN:-minisign}

# Keys in the Tauri CLI's format: the minisign key file, base64 encoded. The
# passwords go through standard input, as in the release.
printf 'release password\nrelease password\n' | "$minisign" -G -f -p "$work/key.pub" -s "$work/key" >/dev/null 2>&1
printf 'other\nother\n' | "$minisign" -G -f -p "$work/other.pub" -s "$work/other" >/dev/null 2>&1
export UPDATER_PUBKEY
UPDATER_PUBKEY=$(base64 <"$work/key.pub" | tr -d '\n')
release_key=$(base64 <"$work/key" | tr -d '\n')
other_key=$(base64 <"$work/other" | tr -d '\n')

# sign KEY PASSWORD VERSION FILE...
sign() {
  local key=$1 password=$2 version=$3
  shift 3
  TAURI_SIGNING_PRIVATE_KEY=$key TAURI_SIGNING_PRIVATE_KEY_PASSWORD=$password \
    "$scripts/updater-sign.sh" "$version" "$@" >/dev/null
}

for name in baum-desktop-darwin-universal.app.tar.gz baum-desktop-linux-amd64.AppImage \
  baum-desktop-windows-amd64-setup.exe; do
  head -c 4096 /dev/urandom >"$dir/$name"
done
sign "$release_key" "release password" 1.2.3 "$dir"/*
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

fail "wrong key password" "signing" sign "$release_key" "wrong" 1.2.3 "$work/key.pub"
cp "$dir/baum-desktop-linux-amd64.deb" "$work/deb"
fail "signing with a key other than the configured one" "does not verify with the configured public key" \
  sign "$other_key" other 1.2.3 "$work/deb"

fail "release signing with a minisign from PATH" "must be the absolute path" \
  env BAUM_RELEASE_SIGNING=1 MINISIGN=minisign TAURI_SIGNING_PRIVATE_KEY="$release_key" \
  "$scripts/updater-sign.sh" 1.2.3 "$work/key.pub"
fail "release verification without the pinned minisign" "must be the absolute path" \
  env -u MINISIGN BAUM_RELEASE_SIGNING=1 "$manifest" verify "$dir"

pass "$manifest" write 1.2.3 "$dir"
keys=$(jq -cj '.platforms | keys' "$dir/latest.json")
if [ "$keys" != '["darwin-aarch64","darwin-x86_64","linux-x86_64","windows-x86_64"]' ]; then
  echo "FAIL unexpected platforms $keys" >&2
  status=1
fi
url=$(jq -j '.platforms["linux-x86_64"].url' "$dir/latest.json")
if [ "$url" != "$base/baum-desktop-linux-amd64.AppImage" ]; then
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

edit '.platforms["linux-x86_64"] = .platforms["windows-x86_64"]'
fail "another platform's bundle, correctly signed" "is not $base/ followed by its bundle" "$manifest" verify "$dir"

edit '.platforms["linux-x86_64"].url |= sub("github.com"; "github.com.example.net")'
fail "foreign host" "is not $base/ followed by its bundle" "$manifest" verify "$dir"

edit '.platforms["linux-x86_64"].url |= sub("/v1.2.3/"; "/v1.2.2/")'
fail "another tag" "is not $base/ followed by its bundle" "$manifest" verify "$dir"

edit '.platforms["linux-x86_64"].url |= sub("https:"; "http:")'
fail "plain http URL" "is not $base/ followed by its bundle" "$manifest" verify "$dir"

edit '.platforms["freebsd-x86_64"] = .platforms["linux-x86_64"]'
fail "a platform the release does not ship" "freebsd-x86_64 is not a platform" "$manifest" verify "$dir"

cp "$dir/baum-desktop-windows-amd64-setup.exe" "$work/other-bundle"
TAURI_SIGNING_PRIVATE_KEY=$other_key TAURI_SIGNING_PRIVATE_KEY_PASSWORD=other UPDATER_PUBKEY=$(base64 <"$work/other.pub" | tr -d '\n') \
  "$scripts/updater-sign.sh" 1.2.3 "$work/other-bundle" >/dev/null
edit ".platforms[\"windows-x86_64\"].signature = \"$(cat "$work/other-bundle.sig")\""
fail "signature from another key" "does not verify" "$manifest" verify "$dir"

edit '.version = "1.2.4" | .platforms[].url |= sub("/v1.2.3/"; "/v1.2.4/")'
fail "announced version the bundles were not signed for" "not version 1.2.4" "$manifest" verify "$dir"

# A genuine bundle of an older release, served as the new version.
darwin=$dir/baum-desktop-darwin-universal.app.tar.gz
cp "$darwin" "$work/darwin"
cp "$darwin.sig" "$work/darwin.sig"
head -c 4096 /dev/urandom >"$darwin"
sign "$release_key" "release password" 1.2.2 "$darwin"
old=$(cat "$darwin.sig")
edit ".platforms[\"darwin-aarch64\"].signature = \"$old\" | .platforms[\"darwin-x86_64\"].signature = \"$old\""
fail "older release's bundle" "not version 1.2.3" "$manifest" verify "$dir"
cp "$work/darwin" "$darwin"
cp "$work/darwin.sig" "$darwin.sig"

for field in version url platform; do
  case "$field" in
    version) edit '.version += "\r"' ;;
    url) edit '.platforms["linux-x86_64"].url |= sub("linux"; "li\rnux")' ;;
    platform) edit '.platforms["linux-x86_64\r"] = .platforms["linux-x86_64"] | del(.platforms["linux-x86_64"])' ;;
  esac
  fail "carriage return in $field" "FAIL" "$manifest" verify "$dir"
done

edit 'del(.platforms["windows-x86_64"])'
fail "missing Windows" "has no windows-x86_64" "$manifest" verify "$dir"

cp "$work/good.json" "$dir/latest.json"
mv "$dir/baum-desktop-windows-amd64-setup.exe.sig" "$work/windows.sig"
fail "bundle without a signature" "has no updater signature" "$manifest" write 1.2.3 "$dir"
mv "$work/windows.sig" "$dir/baum-desktop-windows-amd64-setup.exe.sig"

# Unsigned macOS and Windows bundles are still offered, under their names.
mv "$darwin" "$dir/baum-desktop-darwin-universal-unsigned.app.tar.gz"
mv "$darwin.sig" "$dir/baum-desktop-darwin-universal-unsigned.app.tar.gz.sig"
head -c 4096 /dev/urandom >"$dir/baum-desktop-linux-arm64.AppImage"
sign "$release_key" "release password" 1.2.3 "$dir/baum-desktop-linux-arm64.AppImage"
pass "$manifest" write 1.2.3 "$dir"
if [ "$(jq -j '.platforms["linux-aarch64"].url' "$dir/latest.json")" != "$base/baum-desktop-linux-arm64.AppImage" ] ||
  [ "$(jq -j '.platforms["darwin-x86_64"].url' "$dir/latest.json")" != "$base/baum-desktop-darwin-universal-unsigned.app.tar.gz" ]; then
  echo "FAIL the arm64 AppImage or the unsigned macOS app is not listed" >&2
  status=1
fi

# Reproduce native Windows tool output on Unix too.
mkdir "$work/crlf-tools"
export BAUM_TEST_REAL_JQ BAUM_TEST_REAL_MINISIGN
BAUM_TEST_REAL_JQ=$(type -P jq)
BAUM_TEST_REAL_MINISIGN=$(command -v "$minisign")
cat >"$work/crlf-tools/jq" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
# Join-output has no generated line ending to translate.
for arg in "$@"; do
  case "$arg" in
    --join-output | -*j*) exec "$BAUM_TEST_REAL_JQ" "$@" ;;
  esac
done
"$BAUM_TEST_REAL_JQ" "$@" | awk '{sub(/\r$/, ""); printf "%s\r\n", $0}'
SH
cat >"$work/crlf-tools/minisign" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
"$BAUM_TEST_REAL_MINISIGN" "$@" | awk '{sub(/\r$/, ""); printf "%s\r\n", $0}'
SH
chmod +x "$work/crlf-tools/jq" "$work/crlf-tools/minisign"
pass env PATH="$work/crlf-tools:$PATH" MINISIGN="$work/crlf-tools/minisign" "$manifest" write 1.2.3 "$dir"
pass env PATH="$work/crlf-tools:$PATH" MINISIGN="$work/crlf-tools/minisign" "$manifest" verify "$dir"

exit "$status"
