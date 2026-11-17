#!/usr/bin/env sh
# Build the viewer bundle (internal/viewer/dist) and the local app
# (internal/localapp/dist). Runs from any directory.
set -eu

cd "$(dirname "$0")/.."

pnpm --dir viewer install --frozen-lockfile
pnpm --dir viewer build:app

pnpm --dir app install --frozen-lockfile
pnpm --dir app build
