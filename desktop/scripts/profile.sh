#!/bin/sh
# Runs a profiling scenario against the desktop app in a headless isolated
# instance (isolated.sh --headless): a warm-up, then N runs, and writes
# summary.md and metrics.json under target/profile/<scenario>-<time>/.
# `just desktop::profile` builds the profiling frontend, viewer and sidecar
# first. The runner is ../app/scripts/profile/run.ts. With --engine chromium
# the same scenarios run in Chromium against the instance's daemon, which
# also records a CPU profile per run.
set -eu

usage() {
  cat <<'EOF'
usage: profile.sh [--check] <scenario> [--runs N] [--name NAME] [--run-timeout SECONDS]
                  [--seed-data[=SEED] | --copy-data] [--workspace DIR]...
                  [--engine webkit|chromium]

  scenario         cold-start, jk, review, filter, switch-inboxes,
                   session-panel or pages
  --check          only check the arguments (just desktop::profile does,
                   before it builds anything)
  --runs N         measured runs after the warm-up (default 5)
  --run-timeout S  fail a run (or the launch, or preparing) that takes
                   longer than S seconds, as a hung driver does (default 600)
  --name NAME      the isolated instance to run in (default profile); its
                   home keeps its data between profiles
  --seed-data[=SEED], --copy-data, --workspace DIR
                   as isolated.sh: fill a new instance with the seeded
                   dataset (comparable across runs and machines), or copy
                   this machine's baum data into it, and mount the
                   repositories it names. An instance with no data and
                   neither flag is seeded with seed 1
  --engine E       webkit (default): the desktop app's WebKitGTK window;
                   chromium: Playwright's Chromium on the same daemon's
                   pages, with a CPU profile per run and its hot functions
                   and stacks in the summary
EOF
}

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
desktop=$(CDPATH='' cd -- "$here/.." && pwd)
scenario=
runs=5
name=profile
check=0
timeout=600
engine=webkit
pass=
nl='
'
while [ "$#" -gt 0 ]; do
  case $1 in
    --runs) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; runs=$2; shift ;;
    --runs=*) runs=${1#--runs=} ;;
    --name) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; name=$2; shift ;;
    --name=*) name=${1#--name=} ;;
    --run-timeout) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; timeout=$2; shift ;;
    --run-timeout=*) timeout=${1#--run-timeout=} ;;
    --engine) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; engine=$2; shift ;;
    --engine=*) engine=${1#--engine=} ;;
    --check) check=1 ;;
    --copy-data|--seed-data|--seed-data=*) pass="$pass$nl$1" ;;
    --workspace) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; pass="$pass$nl--workspace$nl$2"; shift ;;
    --workspace=*) pass="$pass$nl$1" ;;
    -h|--help) usage; exit 0 ;;
    -*) usage >&2; exit 2 ;;
    *) [ -z "$scenario" ] || { usage >&2; exit 2; }; scenario=$1 ;;
  esac
  shift
done
[ -n "$scenario" ] || { usage >&2; exit 2; }
# The scenarios the runner knows, from their definitions (scenarios.ts, and
# the modules it registers scenarios from).
known=$(sed -n 's/^  name: "\([a-z-]*\)",$/\1/p' "$desktop/../app/scripts/profile/scenarios.ts" "$desktop/../app/scripts/profile/pages.ts")
if ! printf '%s\n' "$known" | grep -qx -- "$scenario"; then
  echo "profile: no scenario $scenario; the scenarios are: $(printf '%s' "$known" | tr '\n' ' ')" >&2
  exit 2
fi
case $runs in ''|0|*[!0-9]*) echo "profile: --runs takes a number from 1: $runs" >&2; exit 2 ;; esac
case $timeout in ''|0|*[!0-9]*) echo "profile: --run-timeout takes a number of seconds: $timeout" >&2; exit 2 ;; esac
case $engine in webkit|chromium) ;; *) echo "profile: --engine is webkit or chromium: $engine" >&2; exit 2 ;; esac
# The image's Chromium comes with Chrome for Testing's chromedriver, which
# exists for x86-64 Linux only (isolated.Dockerfile).
if [ "$engine" = chromium ] && [ "$(docker info --format '{{.Architecture}}' 2>/dev/null || uname -m)" != x86_64 ]; then
  echo "profile: --engine chromium needs an x86-64 Docker engine: Chrome for Testing has no chromedriver for this one" >&2
  exit 2
fi
case $name in ''|*[!a-z0-9-]*) echo "profile: the name may only hold lowercase letters, digits and dashes: $name" >&2; exit 2 ;; esac
[ "$check" = 1 ] && exit 0

# A new instance with neither flag gets the seeded dataset, whose numbers
# compare across runs and machines; one that has data keeps it.
case $pass in
  *--seed-data*|*--copy-data*) ;;
  *)
    home=${BAUM_DEV_CACHE_DIR:-$HOME/.cache/baum-dev}/desktop-homes/$name
    if [ ! -e "$home/.local/state/baum" ] && [ ! -e "$home/.config/baum" ]; then
      echo "profile: $name has no data yet, so it gets the seeded dataset (--seed-data)"
      pass="$pass$nl--seed-data"
    fi
    ;;
esac

stamp=$(date -u +%Y%m%dT%H%M%SZ)
out=target/profile/$scenario-$stamp
commit=$(git -C "$desktop" rev-parse --short HEAD 2>/dev/null || echo unknown)
[ -n "$(git -C "$desktop/.." status --porcelain 2>/dev/null)" ] && commit="$commit+changes"

# In the container: the app's debug build (as isolated runs it), then the
# runner, which launches it through WebKitWebDriver. A command line for the
# container's shell, so its quotes are meant.
# shellcheck disable=SC2089,SC2090
BAUM_ISOLATED_RUN="cargo build --locked --quiet && node /oss/app/scripts/profile/run.ts $scenario \
--app /oss/desktop/target/debug/baum-desktop --out /oss/desktop/$out --runs $runs \
--dist /oss/internal/localapp/dist --run-timeout $timeout --env 'commit=$commit' --env 'instance=$name'"
# Chromium needs no app: the sidecar the app bundles starts the daemon.
# shellcheck disable=SC2089,SC2090
[ "$engine" = chromium ] && BAUM_ISOLATED_RUN="node /oss/app/scripts/profile/run.ts $scenario \
--engine chromium --baum /oss/desktop/binaries/baum-\$(uname -m)-unknown-linux-gnu --out /oss/desktop/$out \
--runs $runs --dist /oss/internal/localapp/dist --run-timeout $timeout --env 'commit=$commit' --env 'instance=$name'"
# shellcheck disable=SC2090
export BAUM_ISOLATED_RUN

old_ifs=$IFS
IFS=$nl
# shellcheck disable=SC2086 # split on newlines only
set -- $pass
IFS=$old_ifs
status=0
sh "$here/isolated.sh" "$name" --headless "$@" || status=$?
if [ -f "$desktop/$out/summary.md" ]; then
  echo "profile: $desktop/$out/summary.md"
elif [ -d "$desktop/$out" ]; then
  echo "profile: no summary; see $desktop/$out" >&2
fi
exit "$status"
