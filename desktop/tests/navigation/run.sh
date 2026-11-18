#!/bin/sh
# Builds the navigation harness against the app's navigation.rs,
# declaration.rs and daemon-pages capability, and runs
# check.mjs under a virtual display. Runs in the navigation-check image with
# oss/desktop at /oss/desktop (see `just desktop::navigation-check`).
set -eu
desktop=/oss/desktop
work=$desktop/target/navigation-check
rm -rf "$work/harness"
mkdir -p "$work"
cp -r "$desktop/tests/navigation/harness" "$work/harness"
cp "$desktop/src/navigation.rs" "$desktop/src/declaration.rs" "$work/harness/src/"
# The app's capability for the daemon's pages (windows, remote URLs), granting
# the harness's one command instead of the app's.
mkdir -p "$work/harness/capabilities"
jq 'del(."$schema") | .permissions = ["allow-record-call"]' \
    "$desktop/capabilities/daemon-pages.json" > "$work/harness/capabilities/daemon-pages.json"
cp "$desktop/Cargo.lock" "$work/harness/Cargo.lock"
mkdir -p "$work/harness/icons" "$work/harness/dist"
echo '<!doctype html>' > "$work/harness/dist/index.html"
cp "$desktop/icons/icon.png" "$work/harness/icons/icon.png"
cargo build --quiet --manifest-path "$work/harness/Cargo.toml" --target-dir "$work/target"
Xvfb :99 -screen 0 1400x1000x24 -nolisten tcp 2>/dev/null &
xvfb=$!
trap 'kill $xvfb' EXIT
sleep 1
DISPLAY=:99 NO_AT_BRIDGE=1 node "$desktop/tests/navigation/check.mjs" "$work/target/debug/navigation-harness"
