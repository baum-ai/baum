#!/usr/bin/env sh
# Rebuild the viewer and fail when the result differs from what is committed.
# The module is the repository root in the published repository and the oss/
# subdirectory in the private one, so every path here is relative to it.
# Untracked files count: a changed source file renames its hashed chunk, which
# shows up as one new file and one deleted file.
set -eu

cd "$(dirname "$0")/.."

sh scripts/viewer-build.sh

changed=$(git status --porcelain --untracked-files=all -- internal/viewer/dist viewer/src/precompiled)
if [ -n "$changed" ]; then
  echo "the committed viewer bundle is stale; run 'just viewer-build' and commit the result:" >&2
  echo "$changed" >&2
  exit 1
fi
echo "viewer bundle is fresh"
