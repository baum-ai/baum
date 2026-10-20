#!/usr/bin/env bash
# Check what "just release-snapshot" left under dist/: the five archives and
# checksums.txt exist, there is no windows/arm64 build, every binary was built
# with CGO off and embeds the committed viewer bundle, and, on a linux/amd64
# host, the linux/amd64 binary serves the sample review. The bundle marker is
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
  work=$(mktemp -d)
  server_pid=
  # shellcheck disable=SC2329 # run through the EXIT trap
  cleanup() {
    if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; fi
    rm -rf "$work"
  }
  trap cleanup EXIT

  started=
  for _ in 1 2 3 4 5; do
    port=$((20000 + RANDOM % 40000))
    : >"$work/stdout"
    "$linux_amd64" internal view --sample --no-open --port "$port" >"$work/stdout" 2>"$work/stderr" &
    server_pid=$!
    for _ in $(seq 1 100); do
      if [ -s "$work/stdout" ] || ! kill -0 "$server_pid" 2>/dev/null; then break; fi
      sleep 0.2
    done
    if [ -s "$work/stdout" ]; then
      started=1
      break
    fi
    wait "$server_pid" 2>/dev/null || true
    server_pid=
  done
  if [ -z "$started" ]; then
    echo "FAIL linux_amd64: baum internal view did not start" >&2
    cat "$work/stderr" >&2
    exit 1
  fi
  url=$(head -n 1 "$work/stdout" | tr -d '\r')
  url=${url%/}

  if [ "$(curl --silent --fail --max-time 20 "$url/api/health")" = '{"status":"ok"}' ]; then
    echo "ok   linux_amd64 serves $url/api/health"
  else
    echo "FAIL linux_amd64: /api/health" >&2
    status=1
  fi
  if curl --silent --fail --max-time 20 "$url/api/document" >"$work/document.json" &&
    cmp -s "$work/document.json" schema/review-example.json; then
    echo "ok   linux_amd64 serves the sample document"
  else
    echo "FAIL linux_amd64: /api/document differs from schema/review-example.json" >&2
    status=1
  fi
fi
exit "$status"
