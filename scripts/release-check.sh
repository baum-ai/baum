#!/usr/bin/env sh
# Fail unless the tag <tag> may be released: it points at a release commit (one
# parent, nothing changed but the built viewer and local app, both present)
# whose parent is on <main>, and the v* tag before it in version order
# (pre-releases before their release), if any, was made from a commit in that
# parent's history. The same parent is allowed, so a release candidate can be
# promoted. Run in a clone of the published repository with all its tags: the
# release workflow runs it on the pushed tag, and the tagging workflow on a
# local tag before pushing it.
#
# Usage: release-check.sh <tag> <main>
set -eu

if [ $# -ne 2 ]; then
  echo "usage: release-check.sh <tag> <main>" >&2
  exit 2
fi
tag=$1
main=$2

fail() {
  echo "release-check: $*" >&2
  exit 1
}

commit=$(git rev-parse --verify "refs/tags/$tag^{commit}")
if [ "$(git rev-list --no-walk --parents "$commit" | wc -w)" -ne 2 ]; then
  fail "$tag ($commit) is not a release commit: it must have exactly one parent"
fi
base=$(git rev-parse "$commit^")
if ! git merge-base --is-ancestor "$base" "$main"; then
  fail "$tag's parent ($base) is not on $main; only commits on main are released"
fi
extra=$(git diff --no-renames --name-only "$base" "$commit" |
  grep -v -e '^internal/viewer/dist/' -e '^internal/localapp/dist/' || true)
if [ -n "$extra" ]; then
  fail "$tag ($commit) changes more than the built apps: $(echo $extra)"
fi
for app in internal/viewer/dist internal/localapp/dist; do
  git cat-file -e "$commit:$app/index.html" 2>/dev/null ||
    fail "$tag ($commit) does not carry the built $app"
done

previous=$(git -c versionsort.suffix=- tag --list 'v*' --sort=v:refname |
  grep -B1 -xF "$tag" | head -n1)
if [ -n "$previous" ] && [ "$previous" != "$tag" ]; then
  previous_base=$(git rev-parse "$previous^{commit}")
  # Legacy tags point directly at source commits. Strip the generated parent
  # only when the tag carries both apps above a source tree without them.
  if [ "$(git rev-list --no-walk --parents "$previous_base" | wc -w)" -eq 2 ]; then
    parent=$(git rev-parse "$previous_base^")
    extra=$(git diff --no-renames --name-only "$parent" "$previous_base" |
      grep -v -e '^internal/viewer/dist/' -e '^internal/localapp/dist/' || true)
    if [ -z "$extra" ] &&
      git cat-file -e "$previous_base:internal/viewer/dist/index.html" 2>/dev/null &&
      git cat-file -e "$previous_base:internal/localapp/dist/index.html" 2>/dev/null &&
      ! git cat-file -e "$parent:internal/viewer/dist/index.html" 2>/dev/null &&
      ! git cat-file -e "$parent:internal/localapp/dist/index.html" 2>/dev/null; then
      previous_base=$parent
    fi
  fi
  if ! git merge-base --is-ancestor "$previous_base" "$base"; then
    fail "$tag ($base) does not come after $previous ($previous_base)"
  fi
  echo "release-check: $tag ($commit on $base) comes after $previous"
else
  echo "release-check: $tag ($commit on $base) is the first v* tag"
fi
