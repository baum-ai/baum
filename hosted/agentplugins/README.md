# Native agent plugin installation

`agentplugins` registers the public `baum-ai/baum` marketplace and installs
`baum@baum` through Claude Code or Codex's native CLI. It never edits hook
settings or keeps migration markers. Claude Code's install, uninstall and
update commands run without `--json`, which baum does not read and which
Claude Code 2.1.248 refuses. Native list JSON reports installation,
enablement and version separately. A separate Codex `hooks/list` query
establishes readiness of the current baum hook definitions.

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
The shared baum CLI installer also configures Codex's terminal title for
Focus after a successful install or update. This is a terminal preference,
separate from plugin installation and hook trust. See
[Codex focus configuration](../../internal/codexconfig/README.md) and
[plugin usage](../../plugins/README.md) for trust and network behavior.

## Codex hook readiness

`HookTrust` reads effective Codex hook inventories using a private stdio
app-server and `hooks/list`, with no thread or model turn. It lets Codex
evaluate the current definition's trust, rather than interpreting trust hashes.
The result exposes only a state: trusted, untrusted (including modified),
disabled, hooks_disabled (lifecycle support turned off), missing or unknown.
Missing required events, partial approval,
malformed output, failed scopes and unsupported methods never report trusted.
`CodexTrust` first checks plugin installation; an absent CLI or plugin reports
not_installed, and a disabled plugin reports disabled without running a server.
The native plugin status itself is unchanged.

Queries default to the user's home, respecting Codex's inherited CODEX_HOME.
The daemon also checks available registered clones for effective project policy.
The checker first reads Codex's effective hooks feature setting so a disabled
lifecycle engine is not mistaken for missing plugin files. The CLI query has a
ten-second budget and an eight-MiB message bound.
Cancellation stops the private server and its descendants on Unix; stderr and
raw protocol errors are discarded because they can include other plugins'
commands or configuration. No hook is executed and no approval is written.

## Claude Code hook readiness

Claude Code has no per-hook approval: an enabled plugin's hooks run in any
folder the user trusted. `ClaudeHooks` reads what can still stop them: the
user's `settings.json` (under `CLAUDE_CONFIG_DIR`, default `~/.claude`) and the
file-based managed settings, `managed-settings.json` then
`managed-settings.d/*.json` in name order. It reports hooks_disabled when
`disableAllHooks` is on (a managed value wins over the user's), managed_only
when managed `allowManagedHooksOnly` is on and managed `enabledPlugins` does
not force-enable baum, unknown when a file exists but cannot be read, and
otherwise ready. Managed settings from MDM or the claude.ai console are not
files, so ready cannot rule them out. Folder trust and project settings are
per repository and are not checked.

`baum hooks status --user --json` includes `trust.state` for installed,
enabled Codex plugins, and the Claude Code state above for installed, enabled
Claude Code plugins. Desktop onboarding preserves it and the app polls the
local `GET /api/agent-hooks/codex` status endpoint for its Settings warning.

The shipped plugin uses `.codex-plugin/plugin.json`. A portable-schema
`plugin.json` installs successfully in Codex 0.166.0 but contributes no hooks;
working project hooks can mask this. The opt-in
`TestShippedCodexPluginHooksWithoutProjectHooks` installs the shipped files in
a clean home and verifies native version reporting and all eight hook sources.
Run it in Docker with the Codex executable mounted and
`BAUM_CODEX_COMPAT_BINARY` set to its container path. It makes no model call.
