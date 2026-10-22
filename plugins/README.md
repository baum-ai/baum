# baum agent plugins

The public [baum repository](https://github.com/baum-ai/baum) is a Git
marketplace for Claude Code and Codex. This directory and both catalogs are
published from the monorepo's `oss/` subtree: paths are relative to that public
repository's root, not the monorepo root. These plugins contain only lifecycle
hooks; they install no binary, skills, MCP servers, credentials or daemon.

## Compatibility and behavior

Plugin **1.0.0** needs a baum CLI that supports
`baum hooks agent --check --plugin AGENT EVENT` (first shipped with these
plugins). Install/update the CLI or desktop app from the same release before
migrating. The dispatcher's capability check is the compatibility boundary:
a missing baum or an older CLI rejects the check silently and the plugin does
nothing. Keep `baum` and `sh` available in the agent's execution environment;
on Windows use an environment providing a POSIX shell. The plugin never
fetches or upgrades a CLI on your behalf.

The plugin uses the same events, matchers and timeouts as legacy user-wide
hooks. The check adds one rule: yield to an existing baum user-wide handler
for that event, including an older dispatcher. Unreadable/invalid legacy
settings also suppress the plugin until repaired. Thus enabling a plugin
before migration does not duplicate the legacy handler. Use only one enabled
copy of the baum plugin per agent. Arbitrary third-party plugins that run
Entire cannot be enumerated; disable overlapping handlers yourself.

All existing opt-ins stay in force: a Git worktree with Entire enabled, no
active project/user/managed hook already delegating that event to Entire, and
a login, setup marker, hosted onboarding or an existing local daemon. Hosted
opt-ins delegate to Entire; daemon-only hooks record events locally. Hooks
never contact a baum server. Installing a plugin grants neither repository
access nor upload consent. See the exact opt-in and hook-event rules in the
[CLI README](../hosted/baumcli/README.md#user-wide-hooks).

The daemon's existing hook-event protocol/version handshake and bounded spool
remain the compatibility contract. An absent daemon is started only under
those existing rules; an incompatible daemon receives events through the
spool and an older busy daemon is restarted when idle. The plugin introduces
no daemon protocol or independent daemon version requirement.

Claude Code needs plugin marketplace and command-hook support. Codex needs
Git marketplaces, plugin lifecycle hooks, and hook trust review (`/hooks`).
These are capability requirements rather than a guessed minimum agent version;
update the agent if the installation commands or hooks UI are unavailable.
Codex lifecycle-hook plugins are supported through manual/Git marketplaces,
but are not eligible for OpenAI's universal public plugin directory.

## Install and migrate

1. Update baum, then run `baum hooks migrate --user --agent claude-code` or
   `baum hooks migrate --user --agent codex` (omit `--agent` for both).
   Do this for fresh installs too: it remembers your plugin choice so a later
   `baum setup` or desktop onboarding does not add legacy settings entries.
   The command removes only baum-owned handlers, backs up changed files, keeps
   other settings byte for byte, and reports any Codex hooks whose trust
   positions moved. Invalid settings are refused unchanged. Retrying is safe.
2. Add the Git marketplace and install the plugin for your user:

   Claude Code:

   ```text
   /plugin marketplace add baum-ai/baum
   /plugin install baum@baum --scope user
   ```

   Codex:

   ```sh
   codex plugin marketplace add baum-ai/baum
   ```

   Open the Plugins directory in Codex/ChatGPT desktop, choose the **baum**
   marketplace, install **baum**, and enable it. The catalog makes it
   available; it does not force installation or authentication. If the CLI
   only manages marketplace sources, finish installation in that desktop UI.
3. Review the installed hooks and trust them in the agent. In Codex, `/hooks`
   must approve each current definition; installation/enabling alone grants
   no hook trust. Restart/start a fresh agent session as required to reload
   plugins. Re-trust your own Codex hooks listed by migration, if any.
4. `baum hooks status --user` reports **managed by plugin** after migration.
   That is your recorded choice, not an assertion that an agent installed,
   enabled or trusted it; inspect the agent's plugins and hooks UI to verify
   those. The desktop setup screen shows the same distinction and does not
   offer legacy installation for migrated agents.

Migration markers are `$XDG_STATE_HOME/baum/user-hooks/AGENT.plugin` (default
`~/.local/state/baum`). They are written only after successful legacy removal.
If removal succeeded but writing the marker failed, repair the reported file
access problem and retry; no unrelated entries have been removed. Legacy
backups retain the existing `baum/backups/AGENT-FILE` path and mode 0600.

## Update, disable and roll back

Claude Code: refresh with `/plugin marketplace update baum`, then update baum
in the plugin UI (or `claude plugin update baum@baum --scope user`). Codex:
`codex plugin marketplace upgrade baum`, then refresh/update the installed
plugin in its Plugins UI and start a fresh session. Review hook changes and
trust changed definitions again. Plugin versions are independent of the CLI;
release changes to hook definitions with a new manifest version and matching
Claude catalog version. Capability checks keep an old CLI quiet during an
upgrade, so upgrade baum before relying on a new plugin capability.

Disable/uninstall **baum** in the agent's plugin UI to stop plugin hooks.
Claude Code also supports `/plugin disable baum@baum` and
`/plugin uninstall baum@baum --scope user`. Removing a marketplace alone is
not a substitute for disabling its installed plugin. This does not uninstall
the CLI/daemon or change repository hooks. Migration markers remain so setup
continues to respect the choice.

To return to legacy user-wide hooks, first disable/uninstall the baum plugin,
then run `baum hooks install --user --legacy --agent AGENT`. This explicitly
clears that agent's migration marker and installs legacy dispatchers using the
existing settings editor; trust changed Codex hooks with `/hooks` again. An
ordinary install refuses to override a migrated choice. Roll back the CLI only
after restoring legacy hooks if the older CLI lacks plugin support. Do not
restore an entire settings backup over unrelated edits made since migration.

## Source contracts and verification

[Claude manifest reference](https://code.claude.com/docs/en/plugins-reference),
[Claude marketplaces](https://code.claude.com/docs/en/plugin-marketplaces), and
[OpenAI plugin packaging](https://developers.openai.com/plugins/build/plugins)
describe the formats. Each plugin has exactly one default `hooks/hooks.json`;
its manifest does not also declare that file (Claude merges both sources).
The Codex catalog is `.agents/plugins/marketplace.json`; its local source path
resolves from the marketplace repository root. Claude uses
`.claude-plugin/marketplace.json` and a `.claude-plugin/plugin.json` manifest.

`TestMarketplacePluginsMatchUserWideHooks` validates the shipped catalogs,
manifests, full event coverage, matchers, timeouts and dispatcher strings
against `agenthooks`. CLI tests execute the shipped commands against real
opt-in checks and exercise fresh install, legacy coexistence, migration,
setup and rollback. Run with Docker from the monorepo root:

```sh
just oss-go test -count=1 ./hosted/agenthooks ./hosted/userhooks ./hosted/baumcli
```
