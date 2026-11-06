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

Agent callbacks record bounded session identifiers, terminal location and
notification metadata locally. The daemon event sender retains its bounded
spool, protocol negotiation, CI/BAUM_NO_DAEMON suppression and explicit stop
behavior. Permission requests and queued messages use the existing local daemon
protocol. Hook stdout is otherwise empty, so baum does not alter agent decisions.

`hooks git` records local Git events for desktop commit attribution and refresh
on push. It installs no hooks and runs no delegated executable. It omits remote
URLs that could carry credentials. Legacy repository dispatchers can be removed
from agent settings or Git hooks by their owner; preserve independent hooks and
checkpoint refs. The desktop requires no repository hook setup.
