#!/usr/bin/env bash
# Check every binary that "just release-snapshot" left under dist/: it was built
# with CGO off and embeds the committed viewer bundle. The bundle marker is the
# hashed main chunk that internal/viewer/dist/index.html loads.
set -euo pipefail

cd "$(dirname "$0")/.."

marker=$(grep -o '/assets/index-[A-Za-z0-9_-]*\.js' internal/viewer/dist/index.html | head -n 1 | sed 's#/assets/##')
if [ -z "$marker" ]; then
  echo "no main chunk referenced by internal/viewer/dist/index.html" >&2
  exit 1
fi
echo "marker: $marker"

targets="linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64"
status=0
for target in $targets; do
  # goreleaser puts each build in dist/baum_<os>_<arch>[_<variant>]/.
  binary=
  for candidate in dist/baum_"$target"*/baum dist/baum_"$target"*/baum.exe; do
    if [ -f "$candidate" ]; then binary=$candidate; fi
  done
  if [ -z "$binary" ]; then
    echo "FAIL $target: no binary under dist/" >&2
    status=1
    continue
  fi
  cgo=$(go version -m "$binary" | grep -o 'CGO_ENABLED=[01]' || true)
  if [ "$cgo" != "CGO_ENABLED=0" ]; then
    echo "FAIL $target: $binary reports '${cgo:-no CGO_ENABLED setting}'" >&2
    status=1
    continue
  fi
  if ! grep -qa -- "$marker" "$binary"; then
    echo "FAIL $target: $binary does not contain $marker" >&2
    status=1
    continue
  fi
  echo "ok   $target: $cgo, contains $marker ($binary)"
done
exit "$status"
