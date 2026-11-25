#!/usr/bin/env bash
# Tests scripts/update-yml.mjs: the update files it writes and rewrites
# describe the files beside them, and check rejects a changed file, a missing
# one, a name that leaves the directory, another version and a main file that
# is not listed. Needs Node.js.
set -euo pipefail

scripts=$(cd "$(dirname "$0")" && pwd)
yml() { node "$scripts/update-yml.mjs" "$@"; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=$work/release
mkdir -p "$dir"
head -c 4096 /dev/urandom >"$dir/baum-desktop-darwin-universal.zip"
head -c 2048 /dev/urandom >"$dir/baum-desktop-linux-amd64.AppImage"
head -c 1024 /dev/urandom >"$dir/baum-desktop-linux-amd64.deb"

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

pass yml write 1.2.3 "$dir" latest-mac.yml baum-desktop-darwin-universal.zip
pass yml write 1.2.3 "$dir" latest-linux.yml baum-desktop-linux-amd64.AppImage baum-desktop-linux-amd64.deb
pass yml check 1.2.3 "$dir"
listed=$(yml check 1.2.3 "$dir" 2>/dev/null | sort | tr '\n' ' ')
if [ "$listed" != "baum-desktop-darwin-universal.zip baum-desktop-linux-amd64.AppImage baum-desktop-linux-amd64.deb " ]; then
  echo "FAIL check lists '$listed', not the described files" >&2
  status=1
fi
fail "another version" "not 1.2.4" yml check 1.2.4 "$dir"
fail "a file name that is not an update file" "not an update file name" yml check 1.2.3 "$dir" ../latest.yml

# A signed (changed) bundle no longer matches until the file is rewritten.
echo signed >>"$dir/baum-desktop-darwin-universal.zip"
fail "a changed file" "does not have the listed SHA-512" yml check 1.2.3 "$dir"
pass yml rewrite 1.2.3 "$dir" latest-mac.yml
pass yml check 1.2.3 "$dir"

cp "$dir/latest-linux.yml" "$work/linux.yml"
sed 's|url: baum-desktop-linux-amd64.deb|url: ../baum-desktop-linux-amd64.deb|' "$work/linux.yml" >"$dir/latest-linux.yml"
fail "a name outside the directory" "is not a plain file name" yml check 1.2.3 "$dir"
sed 's|^path: .*|path: baum-desktop-linux-arm64.AppImage|' "$work/linux.yml" >"$dir/latest-linux.yml"
fail "a main file that is not listed" "is not among its files" yml check 1.2.3 "$dir"
cp "$work/linux.yml" "$dir/latest-linux.yml"
rm "$dir/baum-desktop-linux-amd64.deb"
fail "a missing file" "baum-desktop-linux-amd64.deb is not in" yml check 1.2.3 "$dir"

exit "$status"
