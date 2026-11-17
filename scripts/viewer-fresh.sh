#!/usr/bin/env sh
# Rebuild the viewer and local app. On a release commit, which carries the
# built apps, fail when those differ from the fresh build. The module is
# the repository root in the published repository and the oss/ subdirectory in
# the private one, so every path here is relative to it.
#
# Untracked and ignored files count on a release commit: a changed source file
# renames its hashed chunk, which shows up as one new file and one deleted file.
#
# BAUM_SKIP_VIEWER_FRESH=1 skips the check, for a pipeline in which another job
# has already run it. BAUM_RELEASE_COMMIT=1 fails unless HEAD is a release
# commit, for the checks of one.
set -eu

if [ -n "${BAUM_SKIP_VIEWER_FRESH:-}" ]; then
  echo "viewer freshness check skipped: BAUM_SKIP_VIEWER_FRESH is set"
  exit 0
fi

cd "$(dirname "$0")/.."

sh scripts/viewer-build.sh

paths=""
if [ -n "$(git ls-files -- internal/viewer/dist internal/localapp/dist | grep -v '/\.gitkeep$')" ]; then
  echo "HEAD is a release commit: checking the built apps it carries"
  paths="internal/viewer/dist internal/localapp/dist"
  for app in internal/viewer/dist internal/localapp/dist; do
    if ! git ls-files --error-unmatch "$app/index.html" >/dev/null 2>&1; then
      echo "the release commit does not carry the built $app" >&2
      exit 1
    fi
  done
elif [ -n "${BAUM_RELEASE_COMMIT:-}" ]; then
  echo "BAUM_RELEASE_COMMIT is set, but HEAD carries no built apps" >&2
  exit 1
fi
if [ -z "$paths" ]; then
  echo "HEAD carries no built apps: nothing to compare"
  exit 0
fi
# shellcheck disable=SC2086 # paths is a list of paths without spaces
changed=$(git status --porcelain --untracked-files=all --ignored=matching -- $paths)
if [ -n "$changed" ]; then
  echo "the committed build output is stale:" >&2
  echo "$changed" >&2
  echo "rebuild with 'just viewer-build'; make a new release commit" >&2
  exit 1
fi
echo "the committed build output is fresh"
