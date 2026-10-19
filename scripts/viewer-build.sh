#!/usr/bin/env sh
# Rebuild the committed viewer bundle (internal/viewer/dist) and the
# precompiled tile validators (viewer/src/precompiled). Runs from any directory.
set -eu

cd "$(dirname "$0")/.."

pnpm --dir viewer install --frozen-lockfile
pnpm --dir viewer generate:validators
pnpm --dir viewer build:app

pnpm --dir app install --frozen-lockfile
pnpm --dir app build
