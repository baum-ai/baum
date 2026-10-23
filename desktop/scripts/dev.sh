#!/bin/sh
# Native desktop integration runs on the host; frontend tooling stays in Docker.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/../../.." && pwd)
cd "$root"
export BAUM_OP_RUN=0
compose="$root/dev-compose.sh"
shared=$(mktemp -d /tmp/baum-desktop-dev.XXXXXX)
# Match the private directory's owner even when Compose defaults to UID 1000.
container_user="$(id -u):$(id -g)"
frontend=
cleanup() {
    [ -z "$frontend" ] || docker rm -f "$frontend" >/dev/null 2>&1 || true
    rm -rf -- "$shared"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [ "$(uname -s)" != Linux ]; then
    echo "desktop::dev currently requires Linux (the frontend uses Docker host networking)." >&2
    exit 1
fi
command -v cargo >/dev/null 2>&1 || {
    echo "Install Rust on the host to run the native desktop app; see oss/desktop/README.md." >&2
    exit 1
}
if [ -z "${WAYLAND_DISPLAY:-}" ] && [ -z "${DISPLAY:-}" ]; then
    echo "desktop::dev needs a host Wayland or X11 desktop session." >&2
    exit 1
fi
"$compose" build desktop-dev-frontend
frontend="baum-desktop-vite-${shared##*.}"
"$compose" run -d --no-deps --user "$container_user" --name "$frontend" \
    -v "$shared:/desktop-dev" \
    -e BAUM_DESKTOP_DEV_DIR=/desktop-dev --workdir /oss/app \
    desktop-dev-frontend sh -ec 'pnpm generate; exec pnpm exec vite'
echo "Starting Vite; dependency installation may take a moment."
attempt=0
until [ -f "$shared/vite-ready" ]; do
    if [ "$(docker inspect -f '{{.State.Running}}' "$frontend" 2>/dev/null || true)" != true ]; then
        docker logs "$frontend" 2>/dev/null || true
        echo "The frontend container stopped before Vite was ready." >&2
        exit 1
    fi
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 180 ]; then
        docker logs "$frontend" >&2
        echo "Timed out waiting for Vite." >&2
        exit 1
    fi
    sleep 1
done
echo "Vite is ready. Save files in oss/app or oss/viewer to reload the desktop UI."
echo "The native app uses your host shell, agents, repositories and shared baum daemon."
cd "$root/oss/desktop"
# Keep host outputs separate from the Docker checks' target directory.
export CARGO_TARGET_DIR="$root/oss/desktop/target/host-dev"
export BAUM_DESKTOP_DEV_DIR="$shared"
if [ -z "${BAUM_DESKTOP_BAUM:-}" ]; then
    target=$(rustc -vV | sed -n 's/^host: //p')
    # Install records and privileged scripts require a target named baum.
    # Rename a fresh copy into place so an existing daemon can keep running.
    cli_dir="$CARGO_TARGET_DIR/bin"
    mkdir -p "$cli_dir"
    pending_cli=$(mktemp "$cli_dir/.baum.XXXXXX")
    if ! cp "$root/oss/desktop/binaries/baum-$target" "$pending_cli" ||
        ! chmod 755 "$pending_cli" || ! mv -f "$pending_cli" "$cli_dir/baum"; then
        rm -f -- "$pending_cli"
        echo "Could not prepare the development baum command." >&2
        exit 1
    fi
    export BAUM_DESKTOP_BAUM="$cli_dir/baum"
fi
cargo run --locked
