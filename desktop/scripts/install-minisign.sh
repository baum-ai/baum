#!/usr/bin/env bash
# Installs the official minisign 0.12 binary for this runner into DIR and
# prints its path:
#
#   scripts/install-minisign.sh DIR
#
# The release archives are pinned by SHA-256 (checked against the author's
# minisign signatures when they were pinned), so the signing jobs run no
# package manager and compile nothing.
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: scripts/install-minisign.sh DIR" >&2
  exit 2
fi
dir=$1
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)
base=https://github.com/jedisct1/minisign/releases/download/0.12

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)
    archive=minisign-0.12-linux.tar.gz member=minisign-linux/x86_64/minisign
    sum=9a599b48ba6eb7b1e80f12f36b94ceca7c00b7a5173c95c3efc88d9822957e73
    ;;
  Linux-aarch64)
    archive=minisign-0.12-linux.tar.gz member=minisign-linux/aarch64/minisign
    sum=9a599b48ba6eb7b1e80f12f36b94ceca7c00b7a5173c95c3efc88d9822957e73
    ;;
  Darwin-arm64) # the macOS build is arm64 only
    archive=minisign-0.12-macos.zip member=minisign
    sum=89000b19535765f9cffc65a65d64a820f433ef6db8020667f7570e06bf6aac63
    ;;
  MINGW64_NT-*-x86_64 | MSYS_NT-*-x86_64)
    archive=minisign-0.12-win64.zip member=minisign-win64/x86_64/minisign.exe
    sum=37b600344e20c19314b2e82813db2bfdcc408b77b876f7727889dbd46d539479
    ;;
  *)
    echo "install-minisign: unsupported runner $(uname -s)-$(uname -m)" >&2
    exit 1
    ;;
esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl --fail --silent --show-error --location --retry 3 --output "$work/$archive" "$base/$archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$work/$archive" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$work/$archive" | cut -d' ' -f1)
fi
if [ "$actual" != "$sum" ]; then
  echo "install-minisign: $archive has SHA-256 $actual, expected $sum" >&2
  exit 1
fi
case "$archive" in
  *.tar.gz) tar -xzf "$work/$archive" -C "$work" "$member" ;;
  *.zip)
    if command -v unzip >/dev/null 2>&1; then
      unzip -q "$work/$archive" "$member" -d "$work"
    else
      pwsh -NoProfile -NonInteractive -Command \
        "Expand-Archive -LiteralPath '$(cygpath -w "$work/$archive")' -DestinationPath '$(cygpath -w "$work")'"
    fi
    ;;
esac
binary=$dir/$(basename "$member")
cp "$work/$member" "$binary"
chmod 0755 "$binary"
"$binary" -v >&2
echo "$binary"
