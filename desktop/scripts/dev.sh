#!/bin/sh
# Docker orchestration only; both development workloads run in containers.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/../../.." && pwd)
cd "$root"
export BAUM_OP_RUN=0
compose="$root/dev-compose.sh"
shared=$(mktemp -d /tmp/baum-desktop-dev.XXXXXX)
# Match the private directory's owner even when Compose defaults to UID 1000.
container_user="$(id -u):$(id -g)"
desktop=
frontend=
cleanup() {
    [ -z "$frontend" ] || docker rm -f "$frontend" >/dev/null 2>&1 || true
    [ -z "$desktop" ] || docker rm -f "$desktop" >/dev/null 2>&1 || true
    rm -rf -- "$shared"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
: "${XDG_RUNTIME_DIR:?A Wayland session is required}"
: "${WAYLAND_DISPLAY:?A Wayland session is required}"
"$compose" build desktop-test desktop-dev-frontend
desktop="baum-desktop-dev-${shared##*.}"
"$compose" run -d --no-deps --user "$container_user" --name "$desktop" \
    -v "$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY:/tmp/wayland-0" \
    -v "$shared:/desktop-dev" \
    -e XDG_RUNTIME_DIR=/tmp -e WAYLAND_DISPLAY=wayland-0 -e GDK_BACKEND=wayland \
    -e WEBKIT_DISABLE_DMABUF_RENDERER=1 -e BAUM_DESKTOP_DEV_DIR=/desktop-dev \
    desktop-test sleep infinity
export BAUM_DESKTOP_DEV_CONTAINER="$desktop"
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
docker exec "$desktop" cargo run --locked
