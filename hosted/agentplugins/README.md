# Native agent plugin installation

`agentplugins` registers the public `baum-ai/baum` marketplace and installs
`baum@baum` through Claude Code or Codex's native CLI. It never edits hook
settings or keeps migration markers. Native list JSON reports installation,
enablement and version separately; it cannot establish hook trust.

Claude's user-scope plugin entries and Codex's installed-plugin entries are
matched by exact plugin ID. Project-scoped Claude installs do not satisfy a
user install. Codex's unscoped plugin list fetches every remote marketplace,
which can take 30 seconds without a cache, so Codex status first lists the
configured marketplaces and, when `baum` is among them, lists only that
marketplace; without it the plugin is reported not installed. Both commands
stay local. An unexpected JSON shape, unsupported/missing CLI, failed
command, or an install/remove not reflected in a final list returns an
error. An installed-but-disabled plugin is reported explicitly.

Each status query is bounded to 10 seconds. Each install/remove, including
its verification query, has one 90-second budget. Calls execute argument
arrays without a shell and propagate cancellation. The desktop resolves the login shell PATH before running plugin commands and
allows 240 seconds total for that resolution, both agents and overhead.

Installation registers the intended public marketplace through the native
idempotent add command before installing. Neither agent updates an installed
plugin on a repeated install, so update is separate: it refreshes the
marketplace (Claude `plugin marketplace update`, Codex `plugin marketplace
upgrade`), then updates the plugin (Claude `plugin update --scope user`;
Codex has no update command, and adding the plugin again installs the
refreshed version), and verifies it installed and enabled. It refuses a
plugin that is not installed or disabled, and reports the version before and
after. Removal leaves that marketplace
and other plugins alone. Neither installation nor removal touches repository
hooks, signs in to baum, or asserts that the agent trusted the hooks.
See [plugin usage](../../plugins/README.md) for trust and network behavior.
