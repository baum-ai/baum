#!/usr/bin/env bash
# Builds the macOS disk image on this Mac, as the release does, but signed ad
# hoc and not notarised, into target/local-dmg:
#
#   scripts/macos-local.sh [--install]
#
# --install also replaces /Applications/baum.app with the new build. Run it
# from desktop/ with the frontend and both darwin sidecars built;
# `just desktop::dmg` builds them in Docker first. The build runs on the host
# because the bundle needs Apple's lipo, codesign and hdiutil.
#
# Local builds are version 0.0.0, so the app never checks for updates.
set -euo pipefail

install=false
case "${1:-}" in
  "") ;;
  --install) install=true ;;
  *)
    echo "usage: scripts/macos-local.sh [--install]" >&2
    exit 2
    ;;
esac

if [ "$(uname -s)" != Darwin ]; then
  echo "macos-local: needs macOS" >&2
  exit 1
fi

cd "$(dirname "$0")/.."
export PATH="${CARGO_HOME:-$HOME/.cargo}/bin:$PATH"

# The Tauri CLI the desktop-test image pins.
tauri=$(sed -n 's/.*tauri-cli --version \([0-9.]*\).*/\1/p' Dockerfile)
if [ "$(cargo tauri --version 2>/dev/null)" != "tauri-cli $tauri" ]; then
  echo "macos-local: needs tauri-cli $tauri: cargo install --locked tauri-cli --version $tauri" >&2
  exit 1
fi

if [ "$install" = true ] && pgrep -qx baum-desktop; then
  echo "macos-local: quit baum first (tray: Quit and stop background reviews)" >&2
  exit 1
fi

lipo -create -output binaries/baum-universal-apple-darwin \
  binaries/baum-aarch64-apple-darwin binaries/baum-x86_64-apple-darwin

out=target/local-dmg
rm -rf "$out"
scripts/bundle.sh darwin-universal 0.0.0 "$out"
scripts/macos-finalize.sh "$out/baum-desktop-darwin-universal.app.tar" 0.0.0 "$out"
dmg=$(cd "$out" && pwd)/baum-desktop-darwin-universal-unsigned.dmg

if [ "$install" = true ]; then
  mount=$(mktemp -d)
  trap 'hdiutil detach "$mount" -quiet 2>/dev/null || true; rmdir "$mount" 2>/dev/null || true' EXIT
  hdiutil attach -nobrowse -readonly -mountpoint "$mount" "$dmg" >/dev/null
  rm -rf /Applications/baum.app
  ditto "$mount/baum.app" /Applications/baum.app
  echo "macos-local: installed /Applications/baum.app"
fi

echo "macos-local: $dmg"
