#!/bin/sh
# Runs the desktop app from this checkout in a container with its own home,
# credential store and daemon, on this Wayland display, beside the installed
# app and other isolated ones. Each name keeps its home between runs under
# ${BAUM_DEV_CACHE_DIR:-~/.cache/baum-dev}/desktop-homes/<name>.
# `just desktop::isolated` builds the frontend, viewer and sidecar first.
set -eu

usage() {
  cat <<'EOF'
usage: isolated.sh [name] [--copy-data] [--workspace DIR]... [--headless]

  name             the instance, letters, digits and dashes (default: default)
  --copy-data      first copy this machine's baum data into the instance's
                   home: repositories, reviews and settings, without the
                   credentials, the daemon's runtime files or the Claude
                   account folders; only when the instance has none yet
  --workspace DIR  mount DIR read-only at the same path, so repositories
                   the app knows from the copied data are there; repeatable.
                   Their review stores get writable copies of the instance's,
                   which --copy-data fills from the repositories' own
  --headless       run on a headless compositor inside the container instead
                   of this Wayland display, so no window shows: for agents
                   and profiling. Rendering is in software

BAUM_RELAY_URL, when set, is passed to the instance's daemon.
EOF
}

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH='' cd -- "$here/../../.." && pwd)
name=default
copy=0
headless=0
workspaces=
while [ "$#" -gt 0 ]; do
  case $1 in
    --copy-data) copy=1 ;;
    --headless) headless=1 ;;
    --workspace) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; workspaces="$workspaces
$2"; shift ;;
    --workspace=*) workspaces="$workspaces
${1#--workspace=}" ;;
    -h|--help) usage; exit 0 ;;
    -*) usage >&2; exit 2 ;;
    *) name=$1 ;;
  esac
  shift
done
case $name in
  ''|*[!a-z0-9-]*) echo "isolated: the name may only hold lowercase letters, digits and dashes: $name" >&2; exit 2 ;;
esac

if [ "$headless" = 0 ]; then
  : "${WAYLAND_DISPLAY:?isolated: needs a Wayland session (WAYLAND_DISPLAY is not set); --headless runs without one}"
  : "${XDG_RUNTIME_DIR:?isolated: XDG_RUNTIME_DIR is not set}"
fi
container=baum-desktop-$name
if [ -n "$(docker ps -q --filter "name=^$container\$")" ]; then
  echo "isolated: $name is already running; stop it with: docker stop $container" >&2
  exit 1
fi
home=${BAUM_DEV_CACHE_DIR:-$HOME/.cache/baum-dev}/desktop-homes/$name
mkdir -p "$home"
chmod 700 "$home"
compose() { BAUM_OP_RUN=0 "$repo/dev-compose.sh" run --build --rm --no-deps -e HOME="$HOME" -v "$home:$HOME" "$@"; }

nl='
'
# The repositories' review stores (.git/baum/reviews) under the workspaces.
# The daemon always opens an existing one, which the read-only mount would
# make fail, so each gets a writable copy of the instance's mounted over it.
dirs=
stores=
old_ifs=$IFS
IFS=$nl
for dir in $workspaces; do
  dir=$(CDPATH='' cd -- "$dir" && pwd)
  dirs="$dirs$nl$dir"
  stores="$stores$nl$(find "$dir" \( -name node_modules -o -name target -o -name .cache -o -name dist \) -prune \
    -o -path '*/.git/baum/reviews/versions.sqlite' -print 2>/dev/null | sed 's#/versions.sqlite$##')"
done
IFS=$old_ifs
store_key() { printf %s "$1" | sha256sum | cut -c1-16; }

if [ "$copy" = 1 ]; then
  # Mounted writable so sqlite3 can read databases a running daemon holds
  # open (WAL) and take a consistent snapshot; nothing is written.
  set --
  if [ -e "$home/.local/state/baum" ] || [ -e "$home/.config/baum" ]; then
    echo "isolated: $name already has baum data, kept; delete $home to start again"
  else
    for pair in "${XDG_CONFIG_HOME:-$HOME/.config}/baum:/seed/config" "${XDG_STATE_HOME:-$HOME/.local/state}/baum:/seed/state"; do
      [ -d "${pair%:*}" ] && set -- "$@" -v "$pair"
    done
  fi
  IFS=$nl
  for store in $stores; do
    key=$(store_key "$store")
    [ -e "$home/review-stores/$key" ] || set -- "$@" -v "$store:/seed/reviews/$key"
  done
  IFS=$old_ifs
  [ "$#" -gt 0 ] && compose "$@" desktop-isolated sh scripts/isolated-home.sh seed
fi

if [ "$headless" = 1 ]; then
  set -- -e BAUM_ISOLATED_HEADLESS=1 -e BAUM_ISOLATED_WIDTH -e BAUM_ISOLATED_HEIGHT
else
  set -- -v "$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY:/tmp/wayland-0" -e WAYLAND_DISPLAY=/tmp/wayland-0
fi
[ -f "$HOME/.gitconfig" ] && set -- "$@" -v "$HOME/.gitconfig:/seed/gitconfig:ro"
[ -n "${BAUM_RELAY_URL:-}" ] && set -- "$@" -e BAUM_RELAY_URL
IFS=$nl
for dir in $dirs; do
  set -- "$@" -v "$dir:$dir:ro"
done
for store in $stores; do
  copy=$home/review-stores/$(store_key "$store")
  mkdir -p "$copy"
  set -- "$@" -v "$copy:$store"
done
IFS=$old_ifs
echo "isolated: $name, home $home"
compose --name "$container" "$@" \
  -e GDK_BACKEND=wayland -e WEBKIT_DISABLE_DMABUF_RENDERER=1 \
  desktop-isolated sh scripts/isolated-home.sh app cargo run --locked
