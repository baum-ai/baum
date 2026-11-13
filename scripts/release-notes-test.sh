#!/usr/bin/env sh
# Exercise release-notes.sh: version matching, pre-release tags and failures.
set -eu
script=$(cd "$(dirname "$0")" && pwd)/release-notes.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cat > "$tmp/changelog.md" <<'EOT'
# Changelog

Intro text.

## v0.2.10

Ten notes.

## v0.2.0

Intro line.

### Highlights

- One

- Two

## v0.1.5


## v0.1.0

First public release.
EOT
c=$tmp/changelog.md

expect() {
  want=$1
  shift
  out=$(sh "$script" "$@" 2>"$tmp/err") || {
    echo "unexpected failure: $*" >&2
    exit 1
  }
  if [ "$out" != "$want" ]; then
    echo "wrong output for $*: $out" >&2
    exit 1
  fi
}
status() {
  want=$1
  shift
  got=0
  sh "$script" "$@" >"$tmp/out" 2>"$tmp/err" || got=$?
  if [ "$got" -ne "$want" ]; then
    echo "exit $got, want $want: $*" >&2
    exit 1
  fi
}

# A version that prefixes another is not confused with it.
expect 'Ten notes.' v0.2.10 "$c"
stable='Intro line.

### Highlights

- One

- Two'
expect "$stable" v0.2.0 "$c"
# A release candidate uses its version's section.
expect "$stable" v0.2.0-rc.3 "$c"
# The last section in the file.
expect 'First public release.' v0.1.0 "$c"

status 0 v9.9.9-rc.1 "$c"
[ ! -s "$tmp/out" ] || { echo "pre-release without a section printed output" >&2; exit 1; }
status 1 v9.9.9 "$c"
grep -q 'v9.9.9' "$tmp/err"
# An empty section and a missing section are both errors for a stable tag.
status 1 v0.1.5 "$c"
status 1 v0.2.1 "$c"
status 1 v0.2.0 "$tmp/missing.md"
status 0 v0.2.0-rc.1 "$tmp/missing.md"
status 2
status 2 a b c

echo 'release-notes tests passed'
