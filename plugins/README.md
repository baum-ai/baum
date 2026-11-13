# baum agent plugins

The public [baum repository](https://github.com/baum-ai/baum) is a Git
marketplace for versioned Claude Code and Codex lifecycle-hook plugins. This
directory and both catalogs are published from the monorepo's `oss/` subtree:
paths resolve from the public repository root. Plugins contain only hooks;
they install no binary, skills, MCP servers, credentials or daemon.

## Install through baum

With a supported Claude Code or Codex CLI on `PATH`, run:

```sh
baum hooks install --user
# Or select one agent:
baum hooks install --user --agent codex
# Bring an installed plugin to the latest version:
baum hooks update --user
```

baum registers the public `baum-ai/baum` Git marketplace and installs/enables
`baum@baum` through the native agent CLI. Repeating it is safe and re-enables
an installed disabled plugin, but keeps the installed version: `update`
refreshes the marketplace and updates the plugin, as the desktop app's
Update in Settings does per agent. Restart running agent sessions to use new
hooks, and review trust again where the agent asks (Codex: `/hooks`). No baum sign-in
is required; installation fetches the public GitHub marketplace but calls no baum server. An old/missing
agent CLI or failed command gives an error with upgrade/manual instructions.
baum never edits hook settings directly. Installing or updating Codex through
baum also configures its terminal title to include the thread ID needed for
Focus, keeping other title items. Restart running Codex terminal sessions to
load that preference. Focus repairs older connections when needed and explains
how to restart and resume the session. The preference follows `CODEX_HOME`
(default `~/.codex`); removal leaves it in place. See
[Codex focus configuration](../internal/codexconfig/README.md). No migration
markers or hook-settings migration are used.

Desktop onboarding invokes this installer after the user chooses to connect
an agent. baum's plugins operate independently of repository and third-party
hooks. Teams may install and run Entire themselves; baum reads their existing
checkpoints but does not manage or run Entire.

## Compatibility and behavior

Plugin **1.2.1** uses the existing shared user-wide dispatcher:
`baum hooks agent --check --user AGENT EVENT`, then
`baum hooks agent --user AGENT EVENT`. It needs a CLI supporting that contract;
a missing baum or one too old to recognize the check makes the plugin quietly
do nothing.

1.2.1 fixes Codex hook discovery by using its compatibility manifest layout.
Hook definitions and commands are unchanged. Update the plugin after updating
baum; review `/hooks` in a new Codex conversation. Working project hooks are
independent and do not prove the plugin is loaded.

1.1.0 adds events baum only records, for the desktop app's "Needs you"
(sessions waiting on their user): Claude Code's `Notification` (every type:
permission prompts, questions, idle prompts) and `PostToolUse` for every tool
(`async`, so a tool call never waits for it; it tells the daemon a session
works again once a permission was given), and Codex's `PermissionRequest`,
which prints nothing, so it never allows or denies anything. They never run
Entire. A baum older than them refuses their check and the hooks stay quiet;
an older daemon ignores their events. Codex asks you to trust the new hook
definitions; the ones 1.0.0 had are unchanged, so their trust stands. Install/update the CLI or desktop app before relying on the
plugin.

1.2.0 lets the desktop app answer permission requests and questions, and
send a running session a message. It adds Claude Code's `PermissionRequest`
for every tool, with a one-hour timeout: the hook holds while the app can
answer, alongside Claude Code's own dialog in the terminal (the first answer
wins, and the hook stops waiting once the terminal answered); when it times
out Claude Code kills it and the dialog stays as it was. A session run with
`claude -p` or the SDK is never held. It also adds `PostToolUseFailure` for
every tool (`async`, record-only), so a tool call that failed after you
allowed it in the terminal still ends the app's request, and the session
shows as working. Codex's hooks are
unchanged, so their trust stands: its `PermissionRequest` hook waits for the
app (about 25 seconds) only when you turn Codex approvals on in the app, since
Codex hides its own prompt while the hook waits, and otherwise still prints
nothing. A message you send from the app reaches the session through its
next Stop hook. All of this needs a baum CLI and daemon that know it; an
older one leaves the hooks deciding nothing. Keep `baum` and `sh` available in the agent's execution environment;
on Windows use an environment providing a POSIX shell. Plugins never download
or upgrade the CLI.

Events, matchers, timeouts, `async` and commands come from
`internal/agenthookdefs`. Plugin checks succeed in a Git worktree after the
local baum daemon has run. They read no hosted credentials or third-party hook
settings. Independent Entire hooks do not suppress baum's local events.
Callbacks never install repository hooks or invoke Entire. See the exact
[local hook rules](../internal/hookcli/README.md).

The existing daemon hook-event protocol/version handshake and bounded spool
remain the compatibility contract. An absent daemon starts only under those
existing rules; an incompatible daemon receives events through the spool,
and an older busy daemon restarts once its running jobs finish. Plugins introduce no daemon
protocol or independent daemon version requirement: a permission request
asks the daemon only once it took the hook's event, and a daemon that does
not know the request answers `unknown_method`, which leaves the hook quiet.

Claude Code needs plugin marketplaces and command hooks. Codex needs Git
marketplaces, plugin lifecycle hooks and hook trust review (`/hooks`). These
are capability requirements; update the agent if these controls are missing.
Codex supports manually installed/Git marketplace lifecycle-hook plugins;
they are not eligible for OpenAI's universal public plugin directory.

## Supported agent versions

baum runs reviews, fixes and chat through Claude Code 2.1.248 or newer and
Codex 0.156.1 or newer, and refuses older ones with an upgrade message
(`baum internal doctor` shows it). Claude Code 2.1.247 and older do not accept
`--restricted`. The plugin commands need `claude plugin list --json`, which the
oldest 2.1 releases lack. The [agent version matrix](../scripts/agent-matrix/README.md)
checks the declared floors and several newer releases of each agent.

## Manual installation and trust

Install baum on `PATH`, then add the marketplace and install the plugin.

Claude Code:

```text
/plugin marketplace add baum-ai/baum
/plugin install baum@baum --scope user
```

Codex:

```sh
codex plugin marketplace add baum-ai/baum
codex plugin add baum@baum --json
```

Alternatively, open the Plugins directory in Codex/ChatGPT desktop, select
the **baum** marketplace, install **baum**, and enable it. The catalog makes it available;
it does not force installation or authentication. If the CLI only manages
marketplace sources, complete installation in the desktop UI.

Review the installed hooks and trust them in the agent. Codex's `/hooks` must
approve the current definitions; installing/enabling alone grants no hook
trust. Start a fresh agent session as required to reload plugins. Verify
installation, enablement and trust through the agent's plugins and hooks UI;
`baum hooks status --user [--json]` reads native registries and reports
installation and enablement separately, plus Codex hook readiness when the
plugin is installed and enabled. An unknown result does not imply approval.
Onboarding and Settings direct users to `/hooks` until Codex reports the
current baum hook definitions trusted. A config
directory without an executable CLI is reported as unavailable.

## Update and remove

Claude Code: `/plugin marketplace update baum`, then update the installed
plugin in its UI or run `claude plugin update baum@baum --scope user`. Codex:
`codex plugin marketplace upgrade baum`, then refresh/update the installed
plugin in its Plugins UI and start a fresh session. Review and trust changed
hook definitions again. Plugin versions are independent of the CLI. Release
hook changes with a new manifest version and matching Claude catalog version;
upgrade baum before relying on a new CLI capability.

Disable/uninstall **baum** through the agent's plugin UI. Claude Code also
supports `/plugin disable baum@baum` and
`/plugin uninstall baum@baum --scope user`. Removing a marketplace alone does
not disable its installed plugins. Uninstalling a plugin does not remove the
CLI/daemon or repository hooks. `baum hooks uninstall --user [--agent AGENT]` and the desktop Remove action
also remove the native plugin, leaving the marketplace and other plugins.

## Source contracts and verification

See the [Claude manifest reference](https://code.claude.com/docs/en/plugins-reference),
[Claude marketplaces](https://code.claude.com/docs/en/plugin-marketplaces), and
[OpenAI plugin packaging](https://developers.openai.com/plugins/build/plugins).
Each plugin has one default `hooks/hooks.json`; its manifest does not also
declare that file (Claude merges both sources). Codex uses
`.codex-plugin/plugin.json`: Codex 0.166.0 installs a portable-schema root
manifest but does not load its hooks, while this compatibility layout does.
Its `.agents/plugins/marketplace.json` local source resolves from the marketplace repository root. Claude uses
`.claude-plugin/marketplace.json` and `.claude-plugin/plugin.json`.

`TestMarketplacePluginsMatchUserWideHooks` checks the shipped catalogs,
manifest layout, events, matchers, timeouts and dispatcher strings against
`agenthookdefs`. CLI tests execute shipped commands to verify local admission,
independent hook coexistence and quiet behavior without a compatible CLI.
[`agentplugins`](../internal/agentplugins/README.md) tests captured native registry
formats, scoped install/remove commands, final-state verification and bounded
child-process cancellation. Run inside
Docker from the monorepo root:

```sh
just oss-go test -count=1 ./internal/agenthookdefs ./internal/agentplugins ./internal/hookcli
```
