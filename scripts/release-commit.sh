#!/usr/bin/env sh
# Write a release commit: <base> plus the built viewer and local app, which no
# other commit carries. A v* tag points at a release commit, so go install of a
# tagged version needs nothing but Go. Prints the new commit's hash and moves no
# ref; the caller tags it or points HEAD at it.
#
# Usage: release-commit.sh <git-dir> <base> <prefix> <message>
#
# The built files are read from this module's internal/viewer/dist and
# internal/localapp/dist, so run "just viewer-build" first. <prefix> is where
# the module sits in <base>'s tree: "" in the published repository, "oss/" in
# the private one. The commit identity comes from the GIT_AUTHOR_* and
# GIT_COMMITTER_* variables.
set -eu

if [ $# -ne 4 ]; then
  echo "usage: release-commit.sh <git-dir> <base> <prefix> <message>" >&2
  exit 2
fi
git_dir=$(cd "$1" && pwd)
base=$2
prefix=$3
message=$4

cd "$(dirname "$0")/.."

for app in internal/viewer/dist internal/localapp/dist; do
  if [ ! -f "$app/index.html" ]; then
    echo "release-commit: $app is not built; run 'just viewer-build' first" >&2
    exit 1
  fi
done

index=$(mktemp)
trap 'rm -f "$index"' EXIT
export GIT_DIR="$git_dir" GIT_INDEX_FILE="$index"

git read-tree "$base"
# The built output replaces whatever <base> has there (only the .gitkeep files).
git rm -r -q --cached --ignore-unmatch -- \
  "${prefix}internal/viewer/dist" "${prefix}internal/localapp/dist"
# Stored byte for byte: --no-filters skips any line-ending conversion.
find internal/viewer/dist internal/localapp/dist -type f | LC_ALL=C sort | while read -r file; do
  blob=$(git hash-object -w --no-filters -- "$file")
  git update-index --add --cacheinfo "100644,$blob,$prefix$file"
done

tree=$(git write-tree)
git commit-tree "$tree" -p "$(git rev-parse --verify "$base^{commit}")" -m "$message"
