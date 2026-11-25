#!/bin/sh
# The Electron app runs on the host, for its desktop integration; its build,
# the daemon's build and Vite stay in Docker. Only the Electron binary runs
# on the host: Docker downloads the release for the host's platform, checked
# against the locked electron package's checksums (scripts/host-electron.mjs),
# and the host unpacks it.
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/../../.." && pwd)
cd "$root"
export BAUM_OP_RUN=0
compose="$root/dev-compose.sh"
shared=$(mktemp -d /tmp/baum-desktop-dev.XXXXXX)
frontend=
cleanup() {
    [ -z "$frontend" ] || docker rm -f "$frontend" >/dev/null 2>&1 || true
    rm -rf -- "$shared"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
case "$(uname -m)" in
x86_64 | amd64) cpu=x86_64 electron_arch=x64 ;;
aarch64 | arm64) cpu=aarch64 electron_arch=arm64 ;;
*)
    echo "desktop::dev requires an x86-64 or arm64 host." >&2
    exit 1
    ;;
esac
case "$(uname -s)" in
Linux)
    if [ -z "${WAYLAND_DISPLAY:-}" ] && [ -z "${DISPLAY:-}" ]; then
        echo "desktop::dev needs a host Wayland or X11 desktop session." >&2
        exit 1
    fi
    # Vite shares the host's loopback with the app and the daemon.
    service=desktop-dev-frontend
    # Match the private directory's owner even when Compose defaults to UID 1000.
    container_user="$(id -u):$(id -g)"
    target=$cpu-unknown-linux-gnu
    electron_platform=linux
    command -v unzip >/dev/null 2>&1 || {
        echo "desktop::dev unpacks Electron with unzip; install it on the host." >&2
        exit 1
    }
    ;;
Darwin)
    # Docker Desktop has no host loopback: Vite's port is published instead.
    service=desktop-dev-frontend-bridge
    # Its file sharing ignores ownership, so keep the user that owns node_modules.
    container_user=
    target=$cpu-apple-darwin
    electron_platform=darwin
    ;;
*)
    echo "desktop::dev requires Linux or macOS." >&2
    exit 1
    ;;
esac
if [ -z "${BAUM_DESKTOP_BAUM:-}" ]; then
    binary="$root/oss/desktop/binaries/baum-$target"
    # The daemon embeds the Go module under oss/ (tool descriptions, tile guide,
    # viewer assets). Rebuild it in Docker, as `just desktop::sidecar` does, when
    # it is missing or its recorded input stamp differs from the checkout's.
    reason=
    if [ ! -f "$binary" ]; then
        reason="$binary is missing"
    elif [ ! -f "$binary.stamp" ]; then
        reason="$binary has no build stamp"
    elif [ "$(cat "$binary.stamp")" != "$(cd "$root/oss" && sh desktop/scripts/sidecar-stamp.sh)" ]; then
        reason="oss/ changed since $binary was built"
    fi
    if [ -n "$reason" ]; then
        echo "Rebuilding the baum daemon: $reason."
        BAUM_DEV_HEAVY=1 "$compose" run --build --rm --no-deps oss-test sh desktop/scripts/sidecar.sh "$target"
    fi
    echo "Daemon built from oss/ at $(git -C "$root" rev-parse --short HEAD); after a git pull, restart desktop::dev to pick up daemon changes."
fi
if [ -n "${BAUM_DESKTOP_DEV_SIDECAR_ONLY:-}" ]; then
    exit 0
fi
# The app's main process and preload, bundled in Docker into
# oss/desktop/build, and the Electron release for this host.
cache=${BAUM_DEV_CACHE_DIR:-$HOME/.cache/baum-dev}
electron_out=$(BAUM_DEV_HEAVY=1 "$compose" run --build --rm --no-deps desktop-electron \
    sh -ec "pnpm build && node scripts/host-electron.mjs $electron_platform $electron_arch")
download=$(printf '%s\n' "$electron_out" | sed -n 's/^host-electron: //p' | tail -n 1)
[ -n "$download" ] || {
    printf '%s\n' "$electron_out" >&2
    echo "Could not download Electron for this host." >&2
    exit 1
}
electron_version=${download%% *}
zip=$cache/electron/${download#* /cache/electron/}
electron_dir=$cache/electron-host/v$electron_version-$electron_platform-$electron_arch
if [ ! -f "$electron_dir/.unpacked" ]; then
    echo "Unpacking Electron $electron_version for this host into $electron_dir."
    rm -rf -- "$electron_dir"
    mkdir -p "$electron_dir"
    if [ "$electron_platform" = darwin ]; then
        ditto -x -k "$zip" "$electron_dir"
    else
        unzip -q "$zip" -d "$electron_dir"
    fi
    touch "$electron_dir/.unpacked"
fi
if [ "$electron_platform" = darwin ]; then
    electron_bin=$electron_dir/Electron.app/Contents/MacOS/Electron
else
    electron_bin=$electron_dir/electron
fi
"$compose" build "$service"
frontend="baum-desktop-vite-${shared##*.}"
"$compose" run -d --no-deps --service-ports ${container_user:+--user "$container_user"} --name "$frontend" \
    -v "$shared:/desktop-dev" \
    -e BAUM_DESKTOP_DEV_DIR=/desktop-dev --workdir /oss/app \
    "$service" sh -ec 'exec pnpm exec vite'
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
echo "The app uses your host shell, agents, repositories and shared baum daemon."
cd "$root/oss/desktop"
export BAUM_DESKTOP_DEV_DIR="$shared"
if [ -z "${BAUM_DESKTOP_BAUM:-}" ]; then
    # Install records and privileged scripts require a target named baum.
    # Rename a fresh copy into place so an existing daemon can keep running.
    # Kept apart from the Docker builds' outputs.
    cli_dir="$root/oss/desktop/target/host-dev/bin"
    mkdir -p "$cli_dir"
    pending_cli=$(mktemp "$cli_dir/.baum.XXXXXX")
    if ! cp "$binary" "$pending_cli" ||
        ! chmod 755 "$pending_cli" || ! mv -f "$pending_cli" "$cli_dir/baum"; then
        rm -f -- "$pending_cli"
        echo "Could not prepare the development baum command." >&2
        exit 1
    fi
    export BAUM_DESKTOP_BAUM="$cli_dir/baum"
fi
"$electron_bin" "$root/oss/desktop"
