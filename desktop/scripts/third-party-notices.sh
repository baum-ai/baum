#!/usr/bin/env bash
# Writes ../third_party/electron.txt: Electron's licence, where the bundled
# Chromium licences are, and the licences of the npm packages the desktop app
# ships (scripts/third-party-notices.mjs).
#
#   scripts/third-party-notices.sh [check]
#
# Run it from desktop/ in the desktop-electron container (`just
# desktop::notices`, or `just third-party-notices` for every part), which
# installs the dependencies first. With check it compares with the committed
# file instead of replacing it. The rest of the notices are assembled by
# ../scripts/third-party-notices.sh into THIRD_PARTY_NOTICES.txt, which the
# bundles ship.
set -euo pipefail

cd "$(dirname "$0")/.."

out=../third_party/electron.txt
if [ "${1:-}" = check ]; then
  out=$(mktemp)
fi
node scripts/third-party-notices.mjs "$out"
if [ "${1:-}" = check ]; then
  if ! cmp -s "$out" ../third_party/electron.txt; then
    echo "third_party/electron.txt is stale; run 'just third-party-notices' and commit the result" >&2
    rm -f "$out"
    exit 1
  fi
  rm -f "$out"
  echo "electron notices are current"
fi
