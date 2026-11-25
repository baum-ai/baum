#!/bin/sh
# Runs the desktop app from this checkout in a container with its own home,
# credential store and daemon, on this Wayland display, beside the installed
# app and other isolated ones. Each name keeps its home between runs under
# ${BAUM_DEV_CACHE_DIR:-~/.cache/baum-dev}/desktop-homes/<name>.
# `just desktop::isolated` builds the frontend, viewer and sidecar first.
set -eu

usage() {
  cat <<'EOF'
usage: isolated.sh [name] [--copy-data | --seed-data[=SEED]] [--workspace DIR]... [--headless]

  name             the instance, letters, digits and dashes (default: default)
  --copy-data      first copy this machine's baum data into the instance's
                   home: repositories, reviews and settings, without the
                   credentials, the daemon's runtime files or the Claude
                   account folders; only when the instance has none yet
  --seed-data[=SEED]
                   instead fill the instance's home with the seeded
                   profiling dataset (oss/internal/seeddata, seed 1 by
                   default): the same pull requests, reviews and
                   sessions on every machine, served with no GitHub and
                   no model; only when it has no data yet
  --workspace DIR  mount DIR read-only at the same path, so repositories
                   the app knows from the copied data are there; repeatable.
                   Their review stores get writable copies of the instance's,
                   which --copy-data fills from the repositories' own
  --headless       run on a virtual X server (Xvfb) inside the container
                   instead of this Wayland display, so no window shows: for
                   agents and profiling. Rendering is in software

BAUM_RELAY_URL, when set, is passed to the instance's daemon.
BAUM_ISOLATED_RUN, when set, is the shell command the container runs in the
prepared home instead of the app (scripts/profile.sh runs its scenarios so).
EOF
}

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH='' cd -- "$here/../../.." && pwd)
name=default
copy=0
seed=
headless=0
workspaces=
while [ "$#" -gt 0 ]; do
  case $1 in
    --copy-data) copy=1 ;;
    --seed-data) seed=1 ;;
    --seed-data=) usage >&2; exit 2 ;;
    --seed-data=*) seed=${1#--seed-data=} ;;
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
case $seed in
  *[!0-9]*) echo "isolated: the seed must be a number: $seed" >&2; exit 2 ;;
esac
if [ -n "$seed" ] && [ "$copy" = 1 ]; then
  echo "isolated: --copy-data and --seed-data both fill the home; choose one" >&2
  exit 2
fi
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

if [ -n "$seed" ]; then
  if [ -f "$home/.local/state/baum/seeddata.json" ]; then
    echo "isolated: $name already has the seeded dataset, kept; delete $home to start again"
  elif [ -e "$home/.local/state/baum" ] || [ -e "$home/.config/baum" ]; then
    # It would run online, with whatever data it holds.
    echo "isolated: $name has baum data that is not the seeded dataset; delete $home to seed it" >&2
    exit 1
  else
    # Its sessions are recorded as ended just now: the Sessions page lists
    # an ended session for a day.
    BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 "$repo/dev-compose.sh" run --rm --no-deps -e HOME="$HOME" -v "$home:$HOME" \
      oss-test go run ./cmd/seeddata -seed "$seed" -home "$HOME" -sessions-at now
  fi
fi

if [ "$headless" = 1 ]; then
  set -- -e BAUM_ISOLATED_HEADLESS=1 -e BAUM_ISOLATED_WIDTH -e BAUM_ISOLATED_HEIGHT
  ozone=x11
else
  set -- -v "$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY:/tmp/wayland-0" -e WAYLAND_DISPLAY=/tmp/wayland-0
  ozone=wayland
fi
[ -f "$HOME/.gitconfig" ] && set -- "$@" -v "$HOME/.gitconfig:/seed/gitconfig:ro"
[ -n "${BAUM_RELAY_URL:-}" ] && set -- "$@" -e BAUM_RELAY_URL
# A seeded home reaches no GitHub and no model (isolated-home.sh).
[ -f "$home/.local/state/baum/seeddata.json" ] && set -- "$@" -e BAUM_ISOLATED_OFFLINE=1
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
# The unpackaged app from this checkout, its main process bundled first.
# --no-sandbox only here: a container has no user namespaces for Chromium's
# sandbox (the desktop-electron service sets ELECTRON_DISABLE_SANDBOX for
# the same reason); the app's renderers keep their sandboxed preload.
compose --name "$container" "$@" \
  desktop-isolated sh scripts/isolated-home.sh app sh -c \
  "${BAUM_ISOLATED_RUN:-pnpm build && exec pnpm exec electron . --no-sandbox --ozone-platform=$ozone}"
