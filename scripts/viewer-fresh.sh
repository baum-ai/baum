#!/usr/bin/env sh
# Rebuild the viewer and local app, then fail when the committed precompiled
# tile validators differ from the fresh build. On a release commit, which also
# carries the built apps, those must match the fresh build too. The module is
# the repository root in the published repository and the oss/ subdirectory in
# the private one, so every path here is relative to it.
#
# Untracked and ignored files count on a release commit: a changed source file
# renames its hashed chunk, which shows up as one new file and one deleted file.
#
# BAUM_SKIP_VIEWER_FRESH=1 skips the check, for a pipeline in which another job
# has already run it.
set -eu

if [ -n "${BAUM_SKIP_VIEWER_FRESH:-}" ]; then
  echo "viewer freshness check skipped: BAUM_SKIP_VIEWER_FRESH is set"
  exit 0
fi

cd "$(dirname "$0")/.."

sh scripts/viewer-build.sh

paths="viewer/src/precompiled"
if git ls-files --error-unmatch internal/viewer/dist/index.html >/dev/null 2>&1; then
  echo "HEAD is a release commit: checking the built apps it carries"
  paths="$paths internal/viewer/dist internal/localapp/dist"
fi
# shellcheck disable=SC2086 # paths is a list of paths without spaces
changed=$(git status --porcelain --untracked-files=all --ignored=matching -- $paths)
if [ -n "$changed" ]; then
  echo "the committed build output is stale:" >&2
  echo "$changed" >&2
  echo "rebuild with 'just viewer-build'; commit viewer/src/precompiled, or make a new release commit" >&2
  exit 1
fi
echo "the committed build output is fresh"
