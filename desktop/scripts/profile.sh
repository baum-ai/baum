#!/bin/sh
# Runs a profiling scenario against the desktop app in a headless isolated
# instance (isolated.sh --headless): a warm-up, then N runs, and writes
# summary.md and metrics.json under target/profile/<scenario>-<time>/.
# `just desktop::profile` builds the profiling frontend, viewer and sidecar
# first. The runner is ../app/scripts/profile/run.ts. With --engine chromium
# the same scenarios run in Chromium against the instance's daemon, which
# also records a CPU profile per run.
# With --baseline REF it also builds REF's app (cached by commit) and runs
# both, interleaved, in two instances on the same seeded dataset, and writes
# a comparison under target/profile/<scenario>-vs-<commit>-<time>/.
set -eu

usage() {
  cat <<'EOF'
usage: profile.sh [--check] <scenario> [--runs N] [--name NAME] [--run-timeout SECONDS]
                  [--seed-data[=SEED] | --copy-data] [--workspace DIR]...
                  [--engine webkit|chromium]
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
  --engine E       webkit (default): the desktop app's WebKitGTK window;
                   chromium: Playwright's Chromium on the same daemon's
                   pages, with a CPU profile per run and its hot functions
                   and stacks in the summary
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
engine=webkit
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
definitions="$desktop/../app/scripts/profile/scenarios.ts $desktop/../app/scripts/profile/pages.ts"
# shellcheck disable=SC2086 # two paths without spaces' worth of words
known=$(sed -n 's/^  name: "\([a-z-]*\)",$/\1/p' $definitions)
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
if [ -n "$baseline" ] && [ "$engine" = chromium ]; then
  echo "profile: a comparison runs in WebKit, the engine users run; --engine chromium is for finding why" >&2
  exit 2
fi
if [ -n "$metric" ] && [ -z "$baseline" ]; then
  echo "profile: --metric names what a comparison judges first; it needs --baseline" >&2
  exit 2
fi
case $metric in *[!a-z0-9_]*) echo "profile: no metric $metric" >&2; exit 2 ;; esac
if [ -n "$baseline" ]; then
  # A comparison judges the scenario's headline first (`headline:` after
  # its name), unless --metric names another.
  # shellcheck disable=SC2086
  if [ -z "$metric" ] && ! awk -v want="  name: \"$scenario\"," \
    'found { exit($0 ~ /^  headline: / ? 0 : 1) } $0 == want { found = 1 } END { if (!found) exit 1 }' $definitions; then
    echo "profile: $scenario has no headline yet, so it cannot be compared; name the metric to judge with --metric" >&2
    exit 2
  fi
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
  # Both sides run on the dataset this checkout's generator writes, in its
  # schemas: a baseline whose application database is older refuses it.
  version() { sed -n 's/^const Version = \([0-9][0-9]*\)$/\1/p'; }
  head_schema=$(version <"$desktop/../internal/appdb/appdb.go")
  base_schema=$(git -C "$desktop" show "$sha:oss/internal/appdb/appdb.go" 2>/dev/null | version)
  if [ -n "$head_schema" ] && [ -n "$base_schema" ] && [ "$head_schema" -gt "$base_schema" ]; then
    echo "profile: this checkout's application database is schema $head_schema and $baseline's is $base_schema:" >&2
    echo "  both sides run on the dataset this checkout writes, which the baseline's daemon cannot open." >&2
    echo "  Compare against a ref with schema $head_schema, such as a commit after the migration." >&2
    exit 2
  fi
  [ -n "$runs_given" ] || runs=8
fi
[ "$check" = 1 ] && exit 0

cache=${BAUM_DEV_CACHE_DIR:-$HOME/.cache/baum-dev}
# Diagnostic switches, to measure what each does (README, Profiling):
# BAUM_PROFILE_PAUSE=0 leaves a comparison's waiting side running, and
# BAUM_PROFILE_SETTLE=0 starts each run without waiting for the instance
# to go quiet.
pause=${BAUM_PROFILE_PAUSE:-1}
settle=${BAUM_PROFILE_SETTLE:-1}
case $pause$settle in 00|01|10|11) ;; *) echo "profile: BAUM_PROFILE_PAUSE and BAUM_PROFILE_SETTLE take 0 or 1" >&2; exit 2 ;; esac
[ "$pause" = 1 ] || echo "profile: BAUM_PROFILE_PAUSE=0: the waiting side keeps running (diagnostic)"
[ "$settle" = 1 ] || echo "profile: BAUM_PROFILE_SETTLE=0: runs start without settling (diagnostic)"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
commit=$(git -C "$desktop" rev-parse --short HEAD 2>/dev/null || echo unknown)
[ -n "$(git -C "$desktop/.." status --porcelain 2>/dev/null)" ] && commit="$commit+changes"

if [ -n "$baseline" ]; then
  have_flock=0
  command -v flock >/dev/null 2>&1 && have_flock=1
  homes=$cache/desktop-homes
  mkdir -p "$homes"
  # One comparison per name: a second would empty the first one's homes.
  # The lock (on fd 8) is held, by this shell and the instances it starts,
  # for the whole comparison.
  exec 8>"$homes/.compare-$name.lock"
  if [ "$have_flock" = 1 ] && ! flock -n 8 ||
    [ -n "$(docker ps -q --filter "name=^baum-desktop-$name-head\$" --filter "name=^baum-desktop-$name-base\$")" ]; then
    echo "profile: a comparison named $name is running; give this one another --name" >&2
    exit 1
  fi

  # The baseline's build: its profiling frontend, viewer, sidecar and the
  # app's debug build, made by its own recipes in a worktree all comparisons
  # share, and kept by commit.
  builds=$cache/profile-baselines
  build=$builds/$sha
  mkdir -p "$builds"
  exec 9>"$builds/lock"
  if [ "$have_flock" = 1 ] && ! flock -n 9; then
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
  # In use until this comparison ends: a shared lock on fd 7, taken while
  # fd 9's lock is held, as every eviction is.
  exec 7>"$build.inuse"
  [ "$have_flock" = 0 ] || flock -s 7
  # The three most recent baselines stay, each some 400 MB, and any other a
  # comparison is using.
  if [ "$have_flock" = 1 ]; then
    # shellcheck disable=SC2010,SC2012 # the names are commit hashes
    ls -1t "$builds" | grep -E '^[0-9a-f]{40}$' | tail -n +4 | while IFS= read -r old; do
      if (flock -n -x 6) 6>"$builds/$old.inuse"; then
        rm -rf "${builds:?}/$old" "${builds:?}/$old.inuse"
      fi
    done
  fi
  exec 9>&-

  # Both sides start from homes in the same state: the same seed at the
  # same path in each container, written by this checkout's generator for
  # both (isolated.sh), since the dataset a seed gives changes between
  # commits, gives the same dataset (the runners check its digest), and
  # nothing a previous comparison left. Only the Claude Code each home
  # installed stays (~/.local/bin/claude and ~/.local/share/claude).
  seed=--seed-data
  case $pass in *--seed-data=*) seed=$(printf '%s\n' "$pass" | grep -- '--seed-data=' | tail -n 1) ;; esac
  for side in head base; do
    home=$homes/$name-$side
    [ -d "$home" ] || continue
    chmod -R u+w "$home"
    find "$home" -mindepth 1 -maxdepth 1 ! -name .local -exec rm -rf {} +
    [ ! -d "$home/.local" ] || find "$home/.local" -mindepth 1 -maxdepth 1 ! -name share ! -name bin -exec rm -rf {} +
    [ ! -d "$home/.local/bin" ] || find "$home/.local/bin" -mindepth 1 -maxdepth 1 ! -name claude -exec rm -rf {} +
    [ ! -d "$home/.local/share" ] || find "$home/.local/share" -mindepth 1 -maxdepth 1 ! -name claude -exec rm -rf {} +
  done

  out=target/profile/$scenario-vs-$short-$stamp
  dir=$desktop/$out
  mkdir -p "$dir"
  container() { echo "baum-desktop-$name-$1"; }

  # Ctrl-C, or any other end: neither container is left frozen or running,
  # and a runner still waiting learns why.
  # shellcheck disable=SC2329 # the trap runs it
  stop() {
    trap - INT TERM HUP
    [ -f "$dir/failed" ] || echo "interrupted" >"$dir/failed"
    for side in head base; do
      docker unpause "$(container "$side")" >/dev/null 2>&1 || true
      docker stop "$(container "$side")" >/dev/null 2>&1 || true
    done
    # shellcheck disable=SC2046 # one job id a word
    kill $(jobs -p) 2>/dev/null || true
    echo "profile: interrupted; stopped both instances; see $dir" >&2
    exit 130
  }
  trap stop INT TERM HUP

  run="BAUM_PROFILE_SETTLE=$settle node /oss/app/scripts/profile/run.ts $scenario --out /oss/desktop/$out --runs $runs --run-timeout $timeout"
  # Each side runs in its own instance, the head's app built from this
  # checkout, the base's mounted from its build. Command lines for the
  # container's shell, so their quotes are meant.
  head_run="cargo build --locked --quiet && BAUM_DESKTOP_BAUM=/oss/desktop/target/debug/baum $run --side head \
--app /oss/desktop/target/debug/baum-desktop --dist /oss/internal/localapp/dist \
--env 'commit=$commit' --env 'instance=$name-head'${metric:+ --headline $metric}"
  base_run="BAUM_DESKTOP_BAUM=/baseline/baum $run --side base --app /baseline/baum-desktop --dist /baseline/dist \
--env 'commit=$short' --env 'ref=$baseline' --env 'instance=$name-base'${metric:+ --headline $metric}"
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

  # The side whose place in the order (compare.ts's schedule) is $1.
  step_side() {
    [ "$1" -eq 0 ] && { echo head; return; }
    [ "$1" -eq 1 ] && { echo base; return; }
    pair=$((($1 - 2) / 2 + 1))
    if [ $((pair % 2)) = $((($1 - 2) % 2)) ]; then echo base; else echo head; fi
  }
  # From the first run on, the side waiting is frozen (docker pause) while
  # the other runs: once the turn reaches a place, the other side's
  # container is paused and this side's unpaused, and only then may it go
  # (the file go). A side is paused only once it handed the turn on, so
  # never inside a run. A side that has run all its runs exits, so it is
  # left as it is. Both are unpaused at the end, or when either fails.
  conduct() {
    total=$((2 * runs + 2))
    until { [ -f "$dir/head.ready" ] && [ -f "$dir/base.ready" ]; } || [ -f "$dir/failed" ] ||
      [ -f "$dir/head.status" ] || [ -f "$dir/base.status" ]; do
      sleep 0.1
    done
    paused=
    granted=-1
    while [ ! -f "$dir/failed" ] && [ ! -f "$dir/head.status" ]; do
      turn=$(cat "$dir/turn" 2>/dev/null || echo 0)
      [ "$turn" -lt "$total" ] || break
      if [ "$turn" != "$granted" ]; then
        going=$(step_side "$turn")
        waiting="head"
        [ "$going" = base ] || waiting="base"
        if [ "$paused" = "$going" ]; then
          docker unpause "$(container "$going")" >/dev/null || true
          paused=
        fi
        if [ "$pause" = 1 ] && [ "$paused" != "$waiting" ] && [ ! -f "$dir/$waiting.status" ]; then
          docker pause "$(container "$waiting")" >/dev/null 2>&1 && paused=$waiting
        fi
        printf '%s' "$turn" >"$dir/go.tmp"
        mv "$dir/go.tmp" "$dir/go"
        granted=$turn
      fi
      sleep 0.05
    done
    [ -z "$paused" ] || docker unpause "$(container "$paused")" >/dev/null 2>&1 || true
  }

  (side head "$head_run" 2>&1 | awk '{ print "head: " $0; fflush() }') &
  # The base starts once the head's container runs, so the two do not build
  # the image or seed at once.
  until [ -f "$dir/head.status" ] || [ -n "$(docker ps -q --filter "name=^$(container head)\$")" ]; do
    sleep 1
  done
  if [ -f "$dir/head.status" ]; then
    echo "base: not started, since head stopped"
    echo 1 >"$dir/base.status"
  else
    (side base "$base_run" 2>&1 | awk '{ print "base: " $0; fflush() }') &
    conduct &
  fi
  wait
  trap - INT TERM HUP
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
BAUM_ISOLATED_RUN="cargo build --locked --quiet && BAUM_PROFILE_SETTLE=$settle node /oss/app/scripts/profile/run.ts $scenario \
--app /oss/desktop/target/debug/baum-desktop --out /oss/desktop/$out --runs $runs \
--dist /oss/internal/localapp/dist --run-timeout $timeout --env 'commit=$commit' --env 'instance=$name'"
# Chromium needs no app: the sidecar the app bundles starts the daemon.
# shellcheck disable=SC2089,SC2090
[ "$engine" = chromium ] && BAUM_ISOLATED_RUN="BAUM_PROFILE_SETTLE=$settle node /oss/app/scripts/profile/run.ts $scenario \
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
