# Local desktop hooks

`hookcli` provides the `baum hooks` commands used by the desktop app and its
Claude Code and Codex marketplace plugins. Native plugin installation, update,
removal and status remain `hooks install/update/uninstall/status --user`.
The JSON status shape is the desktop onboarding contract. Agent definitions
live in [agenthookdefs](../agenthookdefs); native operations and trust checks
live in [agentplugins](../agentplugins/README.md).

Plugin callbacks keep their existing command strings:
`hooks agent --check --user AGENT EVENT`, then `hooks agent --user AGENT EVENT`.
The check succeeds only in a Git worktree after the local daemon has run.
It reads no credentials or third-party hook settings. Independent hooks may
run alongside baum; baum neither installs nor delegates to another hook system.
Legacy repository agent checks without `--user` and the legacy Git
`--check` always decline, leaving the repository dispatcher free to run its own
independent hook fallback.

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
`session.id`; nothing else of the environment is kept. The hook's ancestor
processes are still sent, for agents that name no session. `post-rewrite`
keeps its `amend` or `rebase` argument and the `old new` lines Git writes.
Adding an agent's variable is one entry of `sessionEnv`.

`hooks git --check-dispatcher HOOK` is the check of the dispatchers the daemon
installs: like the user-wide agent check, it succeeds only in a Git worktree
after the daemon has run. The older `hooks git --check HOOK` always declines, so
dispatchers of earlier versions keep running their own fallback. Legacy
repository dispatchers can be removed by their owner; preserve independent
hooks and checkpoint refs.
