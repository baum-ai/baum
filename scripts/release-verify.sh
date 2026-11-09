#!/usr/bin/env bash
# Check what "just release-snapshot" left under dist/: the five archives and
# checksums.txt exist, there is no windows/arm64 build, every binary was built
# with CGO off and embeds the viewer bundle the before hook built, and, on a
# linux/amd64 host, the linux/amd64 binary starts the desktop daemon and serves the app. The bundle marker is
# the hashed main chunk that internal/viewer/dist/index.html loads.
set -euo pipefail

cd "$(dirname "$0")/.."

marker=$(grep -m1 -o '/assets/index-[A-Za-z0-9_-]*\.js' internal/viewer/dist/index.html | sed 's#/assets/##' || true)
if [ -z "$marker" ]; then
  echo "no main chunk referenced by internal/viewer/dist/index.html" >&2
  exit 1
fi
echo "marker: $marker"

status=0

for archive in baum-linux-amd64.tar.gz baum-linux-arm64.tar.gz baum-darwin-amd64.tar.gz \
  baum-darwin-arm64.tar.gz baum-windows-amd64.zip checksums.txt; do
  if [ -s "dist/$archive" ]; then
    echo "ok   dist/$archive"
    # The licence and the third-party notices travel with every binary.
    case "$archive" in
      *.tar.gz) listing=$(tar -tzf "dist/$archive") ;;
      *.zip) listing=$(unzip -Z1 "dist/$archive") ;;
      *) listing= ;;
    esac
    if [ -n "$listing" ]; then
      for member in LICENSE THIRD_PARTY_NOTICES.txt; do
        if ! printf '%s\n' "$listing" | grep -qx "$member"; then
          echo "FAIL dist/$archive does not contain $member" >&2
          status=1
        fi
      done
    fi
  else
    echo "FAIL dist/$archive is missing or empty" >&2
    status=1
  fi
done

# No windows/arm64 target is released, in any form.
for unexpected in dist/baum_windows_arm64* dist/baum-windows-arm64*; do
  if [ -e "$unexpected" ]; then
    echo "FAIL unexpected windows/arm64 output: $unexpected" >&2
    status=1
  fi
done

linux_amd64=
targets="linux_amd64 linux_arm64 darwin_amd64 darwin_arm64 windows_amd64"
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
  if [ "$target" = linux_amd64 ]; then linux_amd64=$binary; fi
done

# Run the binary this host can run.
if [ -n "$linux_amd64" ] && [ "$(uname -s)-$(uname -m)" = Linux-x86_64 ]; then
  go run scripts/daemon-smoke.go "$linux_amd64"
fi
exit "$status"
