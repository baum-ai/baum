# Agent configuration directories

Claude Code keeps its settings, plugins, sign-in and session transcripts in one
configuration directory: `~/.claude`, or the one `CLAUDE_CONFIG_DIR` names.
Codex keeps the same in its home: `~/.codex`, or the one `CODEX_HOME` names. A
person with two accounts runs the agent with two directories. `agentdirs` is
the one place that says which of them baum reads.

Each agent is a value: `agentdirs.Claude` and `agentdirs.Codex`.

| | `Claude` | `Codex` |
| --- | --- | --- |
| Variable (`Env`) | `CLAUDE_CONFIG_DIR` | `CODEX_HOME` |
| Default | `~/.claude` | `~/.codex` |
| Record | `claude-dirs.json` | `codex-dirs.json` |
| Session stores | `projects` | `sessions` (discovery also reads `archived_sessions` beside it) |

`All(home)` lists an agent's directories, once each and in this order:

1. the default under `home` (the user's home when empty);
2. the directory of this process's variable, when it is an absolute path;
3. the directories of the agent's record.

`Under(home, name)` is `name` inside each, such as
`Claude.Under(home, "projects")`. The directories need not exist; readers
pass over a missing one. A Claude Code directory is never read as a Codex
home, nor the other way round.

## The record

Each agent's record, beside `config.toml`, holds the other directories baum
was told about, as a JSON object whose `dirs` lists their paths. `Remember`
adds one and `Forget` removes one. A directory is stored absolute and clean
(`Normalize` expands a leading `~` and refuses a relative path), must exist
when it is remembered, and is never the default, which is always read. A
record keeps at most `MaxRecorded` (16) directories and drops the oldest
beyond that. A record that cannot be read counts as empty and is replaced by
the next `Remember`. Each change holds an inter-process lock beside the record
(`<record>.lock`, [filelock](../filelock), waiting up to 5 seconds) from its
read to its write, so the hooks of two sessions and the daemon recording at
once each keep the others' directories.

It is written by:

- `baum hooks install --user`, with the directory it installed the plugin into
  (`--claude-config-dir`, `--codex-home`; [hookcli](../hookcli/README.md));
- an agent hook, with the `CLAUDE_CONFIG_DIR` or `CODEX_HOME` of its session,
  when the session starts or takes a prompt;
- the daemon, with a directory chosen in Settings for reviews, fixes or data
  extraction (`Choose`: it must exist and the agent must not say it is signed
  out with it; `PUT /api/automatic`, `PUT /api/settings/decisions`).

Each writer takes the directory from its own environment or from the user's
explicit choice. Nothing a hook event or another request to the daemon carries
is recorded, so a reported transcript path cannot widen what the daemon reads
(see [SECURITY.md](../../SECURITY.md#claude-code-configuration-directories)).

## Readers

Session discovery and store listing (`sessions/collector`, unless a caller
overrides the store), clone finding (`clonefind`), the live transcript store
checks (`livesessions`), the Claude Code running-session registry, and the
cleanup of review runs' sessions (`reviewruns`). Hook readiness still reads
the settings of the process's own directory, and plan usage reads the
credential of that directory plus those chosen for runs
([planusage](../planusage/README.md)), not every recorded one.
