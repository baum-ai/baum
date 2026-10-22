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
reject() {
  if sh "$script" "$1" main >"$tmp/output" 2>&1; then
    echo "unexpected acceptance: $1" >&2
    exit 1
  fi
  grep -q 'does not come after' "$tmp/output"
}
release "$old" v1.1.0
reject v1.1.0
git tag -d v1.1.0 >/dev/null
release "$base" v1.1.0
sh "$script" v1.1.0 main
# Moving beyond the legacy era and promoting an RC both work.
release "$base" v1.2.0-rc.1
sh "$script" v1.2.0-rc.1 main
release "$base" v1.2.0
sh "$script" v1.2.0 main
release "$legacy" v1.3.0
reject v1.3.0
echo 'release-check ordering tests passed'
