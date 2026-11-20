#!/bin/sh
# Runs a profiling scenario against the desktop app in a headless isolated
# instance (isolated.sh --headless): a warm-up, then N runs, and writes
# summary.md and metrics.json under target/profile/<scenario>-<time>/.
# `just desktop::profile` builds the profiling frontend, viewer and sidecar
# first. The runner is ../app/scripts/profile/run.ts.
# With --baseline REF it also builds REF's app (cached by commit) and runs
# both, interleaved, in two instances on the same seeded dataset, and writes
# a comparison under target/profile/<scenario>-vs-<commit>-<time>/.
set -eu

usage() {
  cat <<'EOF'
usage: profile.sh [--check] <scenario> [--runs N] [--name NAME] [--run-timeout SECONDS]
                  [--seed-data[=SEED] | --copy-data] [--workspace DIR]...
                  [--baseline REF [--metric NAME]]

  scenario         cold-start, jk, review, filter, switch-inboxes,
                   session-panel or pages
  --check          only check the arguments (just desktop::profile does,
                   before it builds anything)
  --runs N         measured runs after the warm-up (default 5; with
                   --baseline, 8 a side)
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
  --baseline REF   also build the app at REF (a commit, branch or tag that
                   has the profiling build) and compare: both run in turn,
                   each in its own instance (NAME-head and NAME-base, reset
                   and seeded afresh with the same seed), and the summary
                   says which metrics changed beyond noise
  --metric NAME    the metric the comparison judges first (default: the
                   scenario's headline)
EOF
}

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
desktop=$(CDPATH='' cd -- "$here/.." && pwd)
scenario=
runs=5
name=profile
check=0
timeout=600
runs_given=
baseline=
metric=
pass=
nl='
'
while [ "$#" -gt 0 ]; do
  case $1 in
    --runs) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; runs=$2; runs_given=1; shift ;;
    --runs=*) runs=${1#--runs=}; runs_given=1 ;;
    --baseline) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; baseline=$2; shift ;;
    --baseline=*) baseline=${1#--baseline=} ;;
    --metric) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; metric=$2; shift ;;
    --metric=*) metric=${1#--metric=} ;;
    --name) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; name=$2; shift ;;
    --name=*) name=${1#--name=} ;;
    --run-timeout) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; timeout=$2; shift ;;
    --run-timeout=*) timeout=${1#--run-timeout=} ;;
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
case $name in ''|*[!a-z0-9-]*) echo "profile: the name may only hold lowercase letters, digits and dashes: $name" >&2; exit 2 ;; esac
if [ -n "$metric" ] && [ -z "$baseline" ]; then
  echo "profile: --metric names what a comparison judges first; it needs --baseline" >&2
  exit 2
fi
case $metric in *[!a-z0-9_]*) echo "profile: no metric $metric" >&2; exit 2 ;; esac
if [ -n "$baseline" ]; then
  case $pass in
    *--copy-data*|*--workspace*)
      echo "profile: a comparison runs both sides on the seeded dataset, the same on both; --copy-data and --workspace do not apply" >&2
      exit 2
      ;;
  esac
  case $baseline in
    -*|*[!A-Za-z0-9._/@{}~^-]*) echo "profile: not a ref: $baseline" >&2; exit 2 ;;
  esac
  if ! sha=$(git -C "$desktop" rev-parse --verify --quiet "$baseline^{commit}"); then
    echo "profile: no commit $baseline in this repository (git fetch origin first?)" >&2
    exit 2
  fi
  short=$(git -C "$desktop" rev-parse --short "$sha")
  # The runner waits on the collector's progress() (oss/app/README.md),
  # which came with the profiling runner: an app before it cannot be driven.
  if ! git -C "$desktop" show "$sha:oss/app/src/perf-collector.ts" 2>/dev/null | grep -q 'progress()'; then
    echo "profile: the baseline $baseline ($short) predates the profiling build: its app has no" >&2
    echo "  window.__baumPerf.progress(), which the runner waits on, so it cannot be measured." >&2
    echo "  Compare against a commit that has oss/app/scripts/profile (the profiling runner) or later." >&2
    exit 2
  fi
  [ -n "$runs_given" ] || runs=8
fi
[ "$check" = 1 ] && exit 0

cache=${BAUM_DEV_CACHE_DIR:-$HOME/.cache/baum-dev}
stamp=$(date -u +%Y%m%dT%H%M%SZ)
commit=$(git -C "$desktop" rev-parse --short HEAD 2>/dev/null || echo unknown)
[ -n "$(git -C "$desktop/.." status --porcelain 2>/dev/null)" ] && commit="$commit+changes"

if [ -n "$baseline" ]; then
  # The baseline's build: its profiling frontend, viewer, sidecar and the
  # app's debug build, made by its own recipes in a worktree all comparisons
  # share, and kept by commit.
  builds=$cache/profile-baselines
  build=$builds/$sha
  mkdir -p "$builds"
  exec 9>"$builds/lock"
  if command -v flock >/dev/null 2>&1 && ! flock -n 9; then
    echo "profile: another comparison is building a baseline; waiting"
    flock 9
  fi
  if [ -f "$build/complete" ]; then
    echo "profile: the baseline $baseline ($short) is built already ($build)"
  else
    worktree=$builds/worktree
    echo "profile: building the baseline $baseline ($short) in $worktree"
    if git -C "$worktree" rev-parse --git-dir >/dev/null 2>&1; then
      git -C "$worktree" checkout --quiet --detach --force "$sha"
      git -C "$worktree" clean -fdq
    else
      rm -rf "$worktree"
      git -C "$desktop" worktree prune
      git -C "$desktop" worktree add --quiet --detach "$worktree" "$sha"
    fi
    (
      cd "$worktree"
      just desktop::frontend profile
      just desktop::viewer
      just desktop::sidecar
      BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 ./dev-compose.sh run --build --rm --no-deps desktop-test cargo build --locked --quiet
    )
    rm -rf "$build.tmp"
    mkdir -p "$build.tmp"
    cp "$worktree/oss/desktop/target/debug/baum-desktop" "$build.tmp/baum-desktop"
    cp "$worktree/oss/desktop/binaries/baum-$(uname -m)-unknown-linux-gnu" "$build.tmp/baum"
    cp -R "$worktree/oss/internal/localapp/dist" "$build.tmp/dist"
    printf '%s %s\n' "$sha" "$baseline" >"$build.tmp/complete"
    rm -rf "$build"
    mv "$build.tmp" "$build"
  fi
  touch "$build"
  # The three most recent baselines stay; each is some 400 MB.
  # shellcheck disable=SC2010,SC2012 # the names are commit hashes
  ls -1t "$builds" | grep -E '^[0-9a-f]{40}$' | tail -n +4 | while IFS= read -r old; do rm -rf "${builds:?}/$old"; done
  exec 9>&-

  # Both sides start from homes in the same state: the same seed at the
  # same path in each container gives the same dataset (the runners check
  # its digest), and nothing a previous comparison left. The Claude Code
  # each home installs stays.
  seed=--seed-data
  case $pass in *--seed-data=*) seed=$(printf '%s\n' "$pass" | grep -- '--seed-data=' | tail -n 1) ;; esac
  for side in head base; do
    home=$cache/desktop-homes/$name-$side
    [ -d "$home" ] || continue
    chmod -R u+w "$home"
    find "$home" -mindepth 1 -maxdepth 1 ! -name .local -exec rm -rf {} +
    [ ! -d "$home/.local" ] || find "$home/.local" -mindepth 1 -maxdepth 1 ! -name share ! -name bin -exec rm -rf {} +
    [ ! -d "$home/.local/share" ] || find "$home/.local/share" -mindepth 1 -maxdepth 1 ! -name claude -exec rm -rf {} +
  done

  out=target/profile/$scenario-vs-$short-$stamp
  dir=$desktop/$out
  mkdir -p "$dir"
  run="node /oss/app/scripts/profile/run.ts $scenario --out /oss/desktop/$out --runs $runs --run-timeout $timeout"
  # Each side runs in its own instance, the head's app built from this
  # checkout, the base's mounted from its build. Command lines for the
  # container's shell, so their quotes are meant.
  head_run="cargo build --locked --quiet && BAUM_DESKTOP_BAUM=/oss/desktop/target/debug/baum $run --side head \
--app /oss/desktop/target/debug/baum-desktop --dist /oss/internal/localapp/dist \
--env 'commit=$commit' --env 'instance=$name-head'${metric:+ --headline $metric}"
  base_run="BAUM_DESKTOP_BAUM=/baseline/baum $run --side base --app /baseline/baum-desktop --dist /baseline/dist \
--env 'commit=$short' --env 'ref=$baseline' --env 'instance=$name-base'"
  side() {
    # $1 the side, $2 its command; a side that stops before its runner
    # could say so tells the other, which would otherwise wait for it.
    status=0
    if [ "$1" = base ]; then
      BAUM_ISOLATED_RUN=$2 BAUM_ISOLATED_VOLUMES="$build:/baseline:ro" sh "$here/isolated.sh" "$name-$1" --headless "$seed" || status=$?
    else
      BAUM_ISOLATED_RUN=$2 sh "$here/isolated.sh" "$name-$1" --headless "$seed" || status=$?
    fi
    [ "$status" = 0 ] || [ -f "$dir/failed" ] || printf '%s: its instance exited with status %s\n' "$1" "$status" >"$dir/failed"
    echo "$status" >"$dir/$1.status"
  }
  (side head "$head_run" 2>&1 | awk '{ print "head: " $0; fflush() }') &
  # The base starts once the head's container runs, so the two do not build
  # the image or seed at once.
  until [ -f "$dir/head.status" ] || [ -n "$(docker ps -q --filter "name=^baum-desktop-$name-head\$")" ]; do
    sleep 1
  done
  if [ -f "$dir/head.status" ]; then
    echo "base: not started, since head stopped"
    echo 1 >"$dir/base.status"
  else
    (side base "$base_run" 2>&1 | awk '{ print "base: " $0; fflush() }') &
  fi
  wait
  status=$(cat "$dir/head.status")
  [ "$status" != 0 ] || status=$(cat "$dir/base.status")
  if [ -f "$dir/summary.md" ]; then
    echo "profile: $dir/summary.md"
  else
    echo "profile: no comparison; see $dir" >&2
  fi
  exit "$status"
fi

# A new instance with neither flag gets the seeded dataset, whose numbers
# compare across runs and machines; one that has data keeps it.
case $pass in
  *--seed-data*|*--copy-data*) ;;
  *)
    home=$cache/desktop-homes/$name
    if [ ! -e "$home/.local/state/baum" ] && [ ! -e "$home/.config/baum" ]; then
      echo "profile: $name has no data yet, so it gets the seeded dataset (--seed-data)"
      pass="$pass$nl--seed-data"
    fi
    ;;
esac

out=target/profile/$scenario-$stamp

# In the container: the app's debug build (as isolated runs it), then the
# runner, which launches it through WebKitWebDriver. A command line for the
# container's shell, so its quotes are meant.
# shellcheck disable=SC2089,SC2090
BAUM_ISOLATED_RUN="cargo build --locked --quiet && node /oss/app/scripts/profile/run.ts $scenario \
--app /oss/desktop/target/debug/baum-desktop --out /oss/desktop/$out --runs $runs \
--dist /oss/internal/localapp/dist --run-timeout $timeout --env 'commit=$commit' --env 'instance=$name'"
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
