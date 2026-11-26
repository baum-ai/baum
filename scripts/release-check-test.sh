#!/usr/bin/env sh
# Exercise ordering across legacy tags, generated releases and RC promotion.
set -eu
script=$(cd "$(dirname "$0")" && pwd)/release-check.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cd "$tmp"
mkdir repo
cd repo
git init -q -b main
git config user.name 'Release test'
git config user.email 'release-test@example.com'
mkdir -p internal/viewer/dist internal/localapp/dist
printf 'old viewer' > internal/viewer/dist/index.html
printf 'old app' > internal/localapp/dist/index.html
git add .
git commit -qm initial
old=$(git rev-parse HEAD)
# A legacy release can itself change only the built apps.
printf 'legacy viewer' > internal/viewer/dist/index.html
git add .
git commit -qm legacy
git tag v1.0.0
legacy=$(git rev-parse HEAD)
rm internal/viewer/dist/index.html internal/localapp/dist/index.html
touch internal/viewer/dist/.gitkeep internal/localapp/dist/.gitkeep
git add .
git commit -qm 'Stop tracking apps'
base=$(git rev-parse HEAD)
release() {
  git checkout -q --detach "$1"
  printf 'release viewer' > internal/viewer/dist/index.html
  printf 'release app' > internal/localapp/dist/index.html
  git add .
  git commit -qm release
  git tag "$2"
  git checkout -q main
}
# reject <tag> <message> [<release-branch>...]
reject() {
  tag=$1
  message=$2
  shift 2
  if sh "$script" "$tag" main "$@" >"$tmp/output" 2>&1; then
    echo "unexpected acceptance: $tag" >&2
    exit 1
  fi
  if ! grep -q "$message" "$tmp/output"; then
    cat "$tmp/output" >&2
    exit 1
  fi
}
# commit_on <branch> <message>: commit a change outside the built apps.
commit_on() {
  git checkout -q "$1"
  printf '%s\n' "$2" >> source.txt
  git add source.txt
  git commit -qm "$2"
  git checkout -q main
}
release "$old" v1.1.0
reject v1.1.0 'does not come after'
git tag -d v1.1.0 >/dev/null
release "$base" v1.1.0
sh "$script" v1.1.0 main
# Moving beyond the legacy era and promoting an RC both work.
release "$base" v1.2.0-rc.1
sh "$script" v1.2.0-rc.1 main
release "$base" v1.2.0
sh "$script" v1.2.0 main
release "$legacy" v1.3.0
reject v1.3.0 'does not come after'
git tag -d v1.3.0 >/dev/null

# A release branch forks from main and leaves out a change main has since
# taken; its tag is released only when the branch is named.
commit_on main 'Feature main has, the release leaves out'
git branch release/v1.2 "$base"
commit_on release/v1.2 'Fix cherry-picked from main'
release release/v1.2 v1.2.1
reject v1.2.1 'not on main or a release branch'
sh "$script" v1.2.1 main release/v1.2
# main comes after the branch's fork point, so its next release follows.
commit_on main 'Fix on main'
release main v1.3.0
sh "$script" v1.3.0 main release/v1.2
# A branch forked before that release cannot follow it.
git branch release/v1.3 "$base"
commit_on release/v1.3 'Fix on a branch that forked too early'
release release/v1.3 v1.3.1
reject v1.3.1 'does not come after' release/v1.2 release/v1.3
git tag -d v1.3.1 >/dev/null
# One that forks from the released commit can.
git branch -f release/v1.3 v1.3.0^
commit_on release/v1.3 'Fix for v1.3.1'
release release/v1.3 v1.3.1
sh "$script" v1.3.1 main release/v1.2 release/v1.3
echo 'release-check ordering tests passed'
