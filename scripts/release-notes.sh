#!/usr/bin/env sh
# Print the release notes for <tag>: the body of its version's section in the
# changelog (default CHANGELOG.md), without the heading. The version is the
# tag without any pre-release suffix, so v0.2.0-rc.3 uses "## v0.2.0". A
# stable tag whose section is missing or empty fails; a pre-release tag
# without a section prints nothing.
#
# Usage: release-notes.sh <tag> [changelog]
set -eu

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "usage: release-notes.sh <tag> [changelog]" >&2
  exit 2
fi
tag=$1
changelog=${2:-CHANGELOG.md}

version=${tag%%-*}
stable=1
if [ "$version" != "$tag" ]; then
  stable=0
fi

notes=
if [ -f "$changelog" ]; then
  notes=$(awk -v heading="## $version" '
    /^## / {
      if (found) exit
      line = $0
      sub(/[ \t\r]+$/, "", line)
      if (line == heading) found = 1
      next
    }
    found { print }
  ' "$changelog" | awk '
    NF { for (i = 0; i < blank; i++) print ""; blank = 0; print; started = 1; next }
    started { blank++ }
  ')
fi

if [ -z "$notes" ]; then
  if [ "$stable" -eq 1 ]; then
    echo "release-notes: $changelog has no notes for $version; add a \"## $version\" section" >&2
    exit 1
  fi
  exit 0
fi
printf '%s\n' "$notes"
