# Native agent plugin installation

`agentplugins` registers the public `baum-ai/baum` marketplace and installs
`baum@baum` through Claude Code or Codex's native CLI. It never edits hook
settings or keeps migration markers. Native list JSON reports installation,
enablement and version separately; it cannot establish hook trust.

Claude's user-scope plugin entries and Codex's installed-plugin entries are
matched by exact plugin ID. Project-scoped Claude installs do not satisfy a
user install. An unexpected JSON shape, unsupported/missing CLI, failed
command, or an install/remove not reflected in a final list returns an
error. An installed-but-disabled plugin is reported explicitly.

Each status query is bounded to 10 seconds. Each install/remove, including
its verification query, has one 90-second budget. Calls execute argument
arrays without a shell and propagate cancellation. The desktop resolves the login shell PATH before running plugin commands and
allows 240 seconds total for that resolution, both agents and overhead.

Installation registers the intended public marketplace through the native
idempotent add command before installing. Removal leaves that marketplace
and other plugins alone. Neither installation nor removal touches repository
hooks, signs in to baum, or asserts that the agent trusted the hooks.
See [plugin usage](../../plugins/README.md) for trust and network behavior.
