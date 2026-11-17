#!/bin/sh
# Runs inside the desktop-isolated container that isolated.sh starts.
#   seed      copy the host's baum data, mounted at /seed/config and
#             /seed/state, and review stores at /seed/reviews/<key>, into
#             this home
#   app CMD   prepare the home, start a D-Bus session with an unlocked
#             keyring for the app's credential store, then run CMD
set -eu

# Account folders are the host's, which the instance does not have: settings
# that name one would fail validation, so they go back to the defaults.
FOLDER_KEYS="'\$.claude_config_dir', '\$.fix_claude_config_dir', '\$.codex_home', '\$.fix_codex_home', '\$.decisions.claude_config_dir', '\$.decisions.codex_home'"

seed() {
  for pair in /seed/config:"$HOME/.config/baum" /seed/state:"$HOME/.local/state/baum"; do
    src=${pair%%:*}
    dst=${pair#*:}
    [ -d "$src" ] || continue
    mkdir -p "$dst"
    chmod 700 "$dst"
    tar -C "$src" --exclude=./daemon --exclude=./credentials.json --exclude=./claude-dirs.json \
      --exclude='*.lock' --exclude='*.lock.sqlite*' --exclude='*-wal' --exclude='*-shm' --exclude='*-journal' \
      -cf - . | tar -C "$dst" -xpf -
    # The copies of databases the daemon has open may lack what is still in
    # their WAL, so each is replaced by a snapshot.
    (cd "$src" && find . -name '*.sqlite' ! -name '*.lock.sqlite') | while IFS= read -r db; do
      sqlite3 "file:$src/$db?mode=ro" ".backup '$dst/$db'"
      chmod 600 "$dst/$db"
      for table in preferences:value repositories:settings; do
        if [ -n "$(sqlite3 "$dst/$db" "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = '${table%:*}'")" ]; then
          sqlite3 "$dst/$db" "UPDATE ${table%:*} SET ${table#*:} = json_remove(${table#*:}, $FOLDER_KEYS) WHERE json_valid(${table#*:})"
        fi
      done
    done
  done
  # Review stores, mounted over the repositories' own when the app runs.
  for src in /seed/reviews/*; do
    [ -f "$src/versions.sqlite" ] || continue
    dst=$HOME/review-stores/${src##*/}
    mkdir -p "$dst"
    chmod 700 "$dst"
    sqlite3 "file:$src/versions.sqlite?mode=ro" ".backup '$dst/versions.sqlite'"
    chmod 600 "$dst/versions.sqlite"
  done
  echo "isolated: copied baum data into $HOME"
}

app() {
  profile=$HOME/.profile
  # The daemon rebuilds its environment from the login shell, so what it
  # needs goes in the profile.
  # shellcheck disable=SC2016 # expanded by the login shell
  grep -qs 'HOME/.local/bin' "$profile" || printf '%s\n' 'export PATH="$HOME/.local/bin:$PATH"' >>"$profile"
  if [ -n "${BAUM_RELAY_URL:-}" ]; then
    sed -i '/^export BAUM_RELAY_URL=/d' "$profile"
    printf "export BAUM_RELAY_URL='%s'\n" "$BAUM_RELAY_URL" >>"$profile"
  fi
  if [ ! -f "$HOME/.gitconfig" ]; then
    [ -f /seed/gitconfig ] && cp /seed/gitconfig "$HOME/.gitconfig"
    # The host's signing key is not here.
    git config --global commit.gpgsign false
    git config --global tag.gpgsign false
  fi
  if [ ! -x "$HOME/.local/bin/claude" ]; then
    echo "isolated: installing Claude Code into this home"
    curl -fsSL https://claude.ai/install.sh | bash || echo "isolated: Claude Code did not install; the app will ask for an agent" >&2
  fi
  export XDG_RUNTIME_DIR=/tmp/runtime
  mkdir -p "$HOME/.local/share/keyrings"
  mkdir -m 700 "$XDG_RUNTIME_DIR"
  # shellcheck disable=SC2016 # expanded by the inner shell
  exec dbus-run-session -- sh -c '
    printf baum-dev | gnome-keyring-daemon --unlock --components=secrets >/dev/null
    gnome-keyring-daemon --start --components=secrets >/dev/null
    printf %s "$DBUS_SESSION_BUS_ADDRESS" >"$XDG_RUNTIME_DIR/bus"
    exec "$@"' sh "$@"
}

command=${1:-}
[ "$#" -gt 0 ] && shift
case $command in
  seed) seed ;;
  app) app "$@" ;;
  *) echo "usage: isolated-home.sh seed | app CMD..." >&2; exit 2 ;;
esac
