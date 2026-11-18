#!/bin/sh
# Builds the check-window harness against the app's check_window.rs,
# navigation.rs, declaration.rs and its capabilities for the daemon's pages
# and the check window, and runs check.mjs under a virtual display. Runs in
# the navigation-check image with oss/desktop at /oss/desktop (see
# `just desktop::check-window-check`).
set -eu
desktop=/oss/desktop
work=$desktop/target/check-window-check
rm -rf "$work/harness"
mkdir -p "$work"
cp -r "$desktop/tests/check-window/harness" "$work/harness"
cp "$desktop/src/check_window.rs" "$desktop/src/navigation.rs" "$desktop/src/declaration.rs" "$work/harness/src/"
# The app's capability for the daemon's pages, granting only the check
# window's commands, and the check window's own, unchanged.
mkdir -p "$work/harness/capabilities"
jq 'del(."$schema") | .permissions = ["allow-open-check-window", "allow-close-check-window"]' \
    "$desktop/capabilities/daemon-pages.json" > "$work/harness/capabilities/daemon-pages.json"
jq 'del(."$schema")' "$desktop/capabilities/tile-check.json" > "$work/harness/capabilities/tile-check.json"
cp "$desktop/Cargo.lock" "$work/harness/Cargo.lock"
mkdir -p "$work/harness/icons" "$work/harness/dist"
echo '<!doctype html>' > "$work/harness/dist/index.html"
cp "$desktop/icons/icon.png" "$work/harness/icons/icon.png"
cargo build --quiet --manifest-path "$work/harness/Cargo.toml" --target-dir "$work/target"
Xvfb :99 -screen 0 1400x1000x24 -nolisten tcp 2>/dev/null &
xvfb=$!
trap 'kill $xvfb' EXIT
sleep 1
DISPLAY=:99 NO_AT_BRIDGE=1 node "$desktop/tests/check-window/check.mjs" "$work/target/debug/check-window-harness"
