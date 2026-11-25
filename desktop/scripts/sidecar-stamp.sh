#!/bin/sh
# Prints a content stamp of everything that goes into the baum binary the
# desktop app bundles: the Go module under oss/ (sources, go:embed files, the
# built viewer and local app in internal/*/dist), without the frontend sources,
# the desktop crate, docs and installed dependencies. Run from the module root
# (oss/). sidecar.sh records it next to the binary and dev.sh compares it, so a
# git pull or checkout that changes an input is noticed whatever the file times.
set -eu

if command -v sha256sum >/dev/null 2>&1; then
  sum=sha256sum
else
  sum='shasum -a 256'
fi

# shellcheck disable=SC2086 # $sum is a command with arguments
find . \( -name node_modules -o -name .git -o -name target -o -name binaries \
  -o -path ./app -o -path ./viewer -o -path ./desktop -o -path ./docs \) -prune \
  -o -type f -print | LC_ALL=C sort | tr '\n' '\0' | xargs -0 $sum | $sum | cut -d' ' -f1
