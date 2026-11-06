# Codex terminal focus configuration

Codex's shared app server runs hooks separately from its terminal clients.
baum identifies a client by the thread ID in its terminal title. Codex's
default title contains only the spinner and project, so baum configures
`tui.terminal_title` with `thread-id` last when connecting or updating the
Codex plugin. Existing users get the same repair when Focus cannot locate
their terminal. Running terminal sessions need restarting and resuming to
load the new preference; baum reports this instead of asking for a manual
TOML edit. A terminal must allow application title updates.

`EnsureTerminalTitle` uses `$CODEX_HOME/config.toml`, defaulting to
`~/.codex/config.toml`. Missing titles keep Codex's default spinner and
project items. Custom items stay in order, with thread-id moved to the end
where the focuser expects it; an empty title becomes just thread-id.
Already configured files are left byte-for-byte unchanged. Unrelated
settings and comments are preserved, including hook trust and profile
settings. Project or command-line overrides of the terminal title still take
precedence over this user default.

Edits support table, dotted-key and inline-table TOML. Invalid TOML or
unexpected setting types are left unchanged with an actionable error;
parser source lines are omitted because they may contain credentials.
Writes use a temporary file and atomic rename, retain an existing file's
permissions and symlink target, create new files with mode 0600, serialize
within the process and refuse to replace a config changed during the edit.
Plugin removal leaves this terminal preference alone.

Run the helper, Focus repair and installer regression tests in Docker:

```sh
just oss-go test -race ./internal/codexconfig ./internal/focus ./internal/hookcli
```
