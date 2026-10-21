#!/bin/sh
# Builds the baum binary the desktop app bundles, as Tauri expects it:
# desktop/binaries/baum-<target triple>[.exe]. Run from the module root
# (oss/). The target defaults to the Go host platform.
set -eu

target=${1:-}
if [ -z "$target" ]; then
  case "$(go env GOHOSTOS)/$(go env GOHOSTARCH)" in
    linux/amd64) target=x86_64-unknown-linux-gnu ;;
    linux/arm64) target=aarch64-unknown-linux-gnu ;;
    darwin/amd64) target=x86_64-apple-darwin ;;
    darwin/arm64) target=aarch64-apple-darwin ;;
    windows/amd64) target=x86_64-pc-windows-msvc ;;
    windows/arm64) target=aarch64-pc-windows-msvc ;;
    *) echo "sidecar: unsupported host $(go env GOHOSTOS)/$(go env GOHOSTARCH)" >&2; exit 1 ;;
  esac
fi

suffix=
case "$target" in
  x86_64-unknown-linux-gnu) goos=linux goarch=amd64 ;;
  aarch64-unknown-linux-gnu) goos=linux goarch=arm64 ;;
  x86_64-apple-darwin) goos=darwin goarch=amd64 ;;
  aarch64-apple-darwin) goos=darwin goarch=arm64 ;;
  x86_64-pc-windows-msvc) goos=windows goarch=amd64 suffix=.exe ;;
  aarch64-pc-windows-msvc) goos=windows goarch=arm64 suffix=.exe ;;
  *) echo "sidecar: unsupported target $target" >&2; exit 1 ;;
esac

for app in internal/viewer/dist internal/localapp/dist; do
  if [ ! -f "$app/index.html" ]; then
    echo "sidecar: warning: $app is not built, so the app's window will only say so; run 'just viewer-build' first" >&2
  fi
done

mkdir -p desktop/binaries
out="desktop/binaries/baum-$target$suffix"
CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -o "$out" ./cmd/baum
echo "$out"
