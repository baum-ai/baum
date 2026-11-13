# Local desktop hooks

`hookcli` provides the `baum hooks` commands used by the desktop app and its
Claude Code and Codex marketplace plugins. Native plugin installation, update,
removal and status remain `hooks install/update/uninstall/status --user`.
The JSON status shape is the desktop onboarding contract. Agent definitions
live in [agenthookdefs](../agenthookdefs); native operations and trust checks
live in [agentplugins](../agentplugins/README.md).

Claude Code keeps its plugins per configuration directory. The four `--user`
commands take `--claude-config-dir DIR` (`~` expanded, absolute), which they
set as `CLAUDE_CONFIG_DIR` for the `claude` they run and the settings they
read; without it they follow the inherited `CLAUDE_CONFIG_DIR`, else
`~/.claude`. For a directory other than `~/.claude`, messages and the text
status name it, the JSON status carries it as `config_dir`, and a successful
install records it ([agentdirs](../agentdirs/README.md)) so baum reads its
sessions. Uninstall keeps the record: the sessions are still there.

Codex keeps its plugins in its home, so the same commands take `--codex-home
DIR`, set as `CODEX_HOME` for the `codex` they run, for the app-server the
trust check starts and for the terminal-title setup (`codexconfig`), else
`CODEX_HOME`, else `~/.codex`. A home other than `~/.codex` is named, reported
as the codex entry's `config_dir` and recorded on install the same way. Hook
trust is per home: approve baum's hooks with `/hooks` in a Codex run with it.

Plugin callbacks keep their existing command strings:
`hooks agent --check --user AGENT EVENT`, then `hooks agent --user AGENT EVENT`.
The check succeeds only in a Git worktree after the local daemon has run.
It reads no credentials or third-party hook settings. Independent hooks may
run alongside baum. baum delegates to no other hook system; the Git hooks its
daemon installs chain the hook a clone already had.
Legacy repository agent checks without `--user` and the legacy Git
`--check` always decline, leaving the repository dispatcher free to run its own
independent hook fallback.

A Claude Code callback that locates the terminal (session start and prompts)
also records its session's `CLAUDE_CONFIG_DIR`, when that is not `~/.claude`,
in the same record: a directory the plugin was installed into by hand is then
read too. A Codex callback does the same with the `CODEX_HOME` it inherits from
its session, when that is not `~/.codex`. It never fails the hook.

Agent callbacks record bounded session identifiers, terminal location and
notification metadata locally. The daemon event sender retains its bounded
spool, protocol negotiation, CI/BAUM_NO_DAEMON suppression and explicit stop
behavior. Permission requests and queued messages use the existing local daemon
protocol. Hook stdout is otherwise empty, so baum does not alter agent decisions.

`hooks git HOOK [args...]` records local Git events (`post-commit`,
`post-rewrite`, `pre-push`) for desktop commit attribution and refresh on push.
It installs no hooks and runs no delegated executable; the daemon installs the
dispatchers that call it ([githooks](../githooks)). It omits remote URLs that
could carry credentials. A post-commit, post-rewrite or pre-push event names its
session when the agent exports one: `CLAUDE_CODE_SESSION_ID` (a safe id of at
most 128 characters) sets the event's `agent` to `claude-code` and
`session.id`, but only when `CLAUDE_PID`, the agent's own process id, is a
positive integer among the hook's ancestor processes. Processes an agent starts
(a tmux server, an editor, another agent) inherit its variables, and their Git
commands are not the agent's. The process must also be named like the agent
(`claude`, a version such as `2.1.34`, which a native install can show, or
`node` or `bun` running it), so a pid reused after the agent
exited is not taken for it. The id is sent as `session.agent_pid`; nothing
else of the environment is kept. The ancestors are always sent: the daemon
prefers a tracked agent nearer the hook than `agent_pid`, and falls back to them
for agents that name no session. `post-rewrite` keeps its `amend` or `rebase`
argument and the `old new` lines Git writes. Adding an agent's variables is one
entry of `sessionEnv`.

`hooks git --check-dispatcher HOOK` is the check of the dispatchers the daemon
installs: like the user-wide agent check, it succeeds only in a Git worktree
after the daemon has run, and declines when `git config --bool baum.hooks` is
false there (one `git config` call, bounded to a second), which silences a
dispatcher the daemon could not take out. The older `hooks git --check HOOK` always declines, so
dispatchers of earlier versions keep running their own fallback, until the
daemon repairs those it recognises as baum's (the repaired dispatcher no longer
falls back to `entire`). A hook Entire manages is left as it is. In a repository
that uses Entire (its `.entire/settings.json` is in the default branch's tree (origin's, `main`, `master`, `init.defaultBranch`)
or in a checked-out branch's, or
checked out in a working tree, or git cannot say) legacy dispatchers that run
Entire's hooks are all left in place. Elsewhere the daemon also retires the
legacy `commit-msg` and `prepare-commit-msg` dispatchers, restoring the hook
they replaced or removing them. Dispatchers it does not recognise can be removed
by their owner; preserve independent hooks and checkpoint refs.

The dispatchers guard against loops: one that finds its own hook already running
(`BAUM_GIT_HOOK_ACTIVE`) does nothing, and none runs a backup that is itself a
dispatcher of baum's. A commit made inside a hook of the same repository
runs neither baum nor the chained hooks for that nested commit, which is what
ends the loop. They run baum from `PATH`, or else from the binary path
recorded when the daemon installed them. The daemon does not pass the agent's
session variables or that guard to a daemon it starts from a hook.
