# Security policy

## What baum sends where

Generating a desktop review sends the following to the **model provider of
your selected Claude Code or Codex CLI**, under your own account. The app and
its daemon offer only those two harnesses, and automatic selection never
falls back to an API-key backend:

- the files of the repository at the pull request head and merge base,
  which the review agent may read as they are, unredacted;
- the evidence bundle: pull request metadata, commits, checks and
  conversation fetched from GitHub, the diff, and text from your local
  coding-agent sessions that were linked to the pull request (prompts,
  paraphrased commands and touched paths). Its free-text and URL fields,
  the diff included, are sent **after** secrets and e-mail addresses are
  masked with [goredact](https://github.com/baum-ai/goredact).
  `REVIEW.md` is the exception: it is sent as it is, like the rest of the
  repository.

A mask keeps the shape of the value, not its characters. It has the same
length; each letter becomes `X` or `x` by case and each digit `9`; the
separators `- _ . : / + = @` are kept verbatim; any other byte becomes `*`,
except line breaks and, in a diff, each line's leading `+`, `-` or space.
An address such as `ada.lovelace@example.com` is sent as
`xxx.xxxxxxxx@xxxxxxx.xxx`. @mentions are left as written.

The diff is masked more than that, on purpose: an edit inside a committed
private key shows key lines without the key's armor, so every run of three
or more diff lines that are pure base64 (`A-Z a-z 0-9 + / =`) of 40
characters or more is masked, with a shorter base64 line at either end, and
so is a hunk's section heading that is pure base64 of that length. Base64
data embedded in a file, such as an inline image or a test fixture, is
masked the same way.

Local decision capture, on by default for picked repositories, also sends
to the provider of the backend reviews use, without asking each time: the
redacted transcript of a coding-agent session in such a repository (your
prompts, the agent's visible text, its plans and question-and-answer
dialogue, and the names of the files it edited, never tool output or file
contents), with the published extraction prompt. The `claude` and `codex`
CLIs get no tools besides the structured reply, run in an empty temporary
directory and persist no session; Codex's read-only sandbox applies and its
shell tools are turned off. The reply is masked again before it is stored in
the private decision store. Turning off automatic generation (the tray's
pause), the decisions switch or a repository's decisions setting stops it.
For the same repositories, and stopped by the same switches, the daemon
lists the repository's pull requests through the GitHub API with your
GitHub credential, about once a minute and conditionally. For the open and
merged ones not opened by a bot it fetches the commits the clone lacks, as
a review fetches a missing pull request commit: heads from
`refs/pull/<N>/head` and bases by id, force-updated under
`refs/baum/pull/<N>/`. Each such pull request of the 180 days before the
feature log was activated for the repository (of its whole history once a
first build ran), and every one after, gets refs in the clone, which are
kept and not pruned. When a fetch fails it
asks the remote for its HEAD (`git ls-remote`); when the remote answers,
the failed batch is fetched again one ref at a time, and otherwise the
fetch is tried again after 15 minutes. A pull request whose commits still
cannot be read is tried again after 15 minutes. It keeps the pull
requests' titles, descriptions, authors, commits and changed paths in the private decision store, and links
the commits to the sessions on this machine there; these rows are not
pruned.
The feature log then sends to the same provider, with the published feature
prompts: a pull request's title, description, branch and
changed paths, with candidate features' titles, descriptions, digests and
their pull requests' titles and summaries, to place it; and, to write a
feature's text, its pull requests' titles, descriptions, states and the
summaries of their reviews saved in the clone, the attributed decisions of
your captured sessions (what was decided and why, and the files cited, never
your prompts or quoted evidence) and the feature's paths. A pull request
without a saved review summary gets one generated from its title,
description and those decisions.

A first build first lists the repository's whole pull request history once
and, unless told otherwise, runs feature discovery: it fetches the default
branch into `refs/baum/default` and sends a read-only snapshot of its head
(bounded as a review's checkout) and the pull request history digest
(titles, branches and changed paths, no descriptions) to the review
harness's backend and provider, run as a review is; the reply, a list of
features, is masked before it is stored.

Redaction is best effort. Generate reviews only for repositories and sessions
you are allowed to share with your selected provider.

Description images are downloaded from URLs GitHub signed in the pull
request's rendered body, which your token read. The downloads carry no
credentials, and only these hosts are contacted: `github.com`
(`/user-attachments/assets/`), `private-user-images.githubusercontent.com`,
`user-images.githubusercontent.com`, `objects.githubusercontent.com` and
`github-production-user-asset-*.s3.amazonaws.com`, over HTTPS. A redirect to
any other host is refused, and an image hosted elsewhere is not downloaded.

Local review generation also reads retained inline image payloads from linked
Claude Code and Codex native transcripts and reachable subagent transcripts.
Human-pasted images and tool-result images keep distinct provenance. Entire
checkpoint images are excluded. Image discovery does not fetch transcript URLs
or open image-file paths mentioned in session text. Native transcript reads
are confined to the session stores and bounded by line, scan, image-count and
decoded-byte limits. Unsupported or missing images are nonfatal diagnostics.
Image bytes are sniffed as PNG/JPEG/GIF/WebP and session images above 40 million
pixels are rejected. The shared capture limit is 20 images of at most 10 MiB
each by default; description images come first and identical bytes share one
copy while retaining their origins. Session images longer than 1568 px on a
side and at most 24 million pixels are decoded and re-encoded at that size
when the result is smaller; colour profiles are not carried over. Saved
versions share one stored copy of identical image bytes.

These pixels are not text-redacted. Eligible images are made available to the
configured review model, just as PR-description images are, and stored with the
local review version. They are served only through authenticated local image
routes and are not published to GitHub. Treat sensitive pixels in session
images as sensitive model input; provenance does not imply a test passed or
that an image depicts the reviewed revision.

baum sends no telemetry. Its desktop runtime contacts GitHub, the selected
model provider and
`static.usebaum.com`, from which it downloads the public price list used to
show what usage would cost at API prices. That request is a plain download: it carries no token,
repository, pull request or session data, and the list's signature is checked
against a key built into the binary. When the list cannot be fetched or
verified, baum uses a copy embedded at release. Agent hooks are described
[below](#local-agent-hooks). GitHub tokens are read from the
environment, `gh auth token` or the 0600 config file and are never written
anywhere else. Local inbox discovery also reads GitHub's REST API using that
same token and derived host, without redirects or extra scopes. Its private application database
contains minimal inbox metadata and ETags in `cache/discovery/<hash>` documents,
partitioned by a credential
fingerprint and repository. Tokens, PR bodies and signed image URLs are never
put in that cache. Cached and offline results are marked stale; unreadable
author write permissions remain unknown, rather than granting automatic work.

## Local agent hooks

`baum hooks install --user` registers the public baum marketplace and
installs/enables its native Claude Code or Codex plugin. Update refreshes the
marketplace and installed plugin; uninstall removes only that plugin. baum
edits no raw hook settings and keeps no settings backups or migration markers.
Native plugin operations can download from GitHub. Codex hook trust belongs
to Codex and must be reviewed with `/hooks`; installation grants no trust.

### Claude Code configuration directories

Claude Code keeps its settings, plugins, sign-in and session transcripts in one
configuration directory: `~/.claude`, or the one `CLAUDE_CONFIG_DIR` names, so a
person with two accounts has two. baum reads Claude Code's sessions, and the
registry of its running sessions, from `~/.claude`, from the directory of the
daemon's own `CLAUDE_CONFIG_DIR`, and from the directories listed in
`claude-dirs.json` beside `config.toml` (at most 16, absolute paths).

Only your own baum processes write that file, each from its own environment or
your explicit choice: `baum hooks install --user` adds the directory it
installed into (`--claude-config-dir`, else `CLAUDE_CONFIG_DIR`); a Claude Code
hook adds the `CLAUDE_CONFIG_DIR` its session runs with, when the session
starts or takes a prompt; and Settings adds a directory you choose for reviews,
fixes or data extraction, which must exist. Nothing a request to the daemon or a hook event
carries is added: a transcript path a hook reports is still read only inside
one of these directories' `projects` folders, under the same checks as before.
Knowing a directory lets baum read the sessions in it, find and signal the
sessions running from it as it does for `~/.claude`, and delete the sessions of
its own review runs there. Remove a directory from the file to stop that.

A directory chosen in Settings for reviews, fixes or data extraction is passed
to Claude Code as `CLAUDE_CONFIG_DIR` for those runs, so they use that directory's sign-in,
settings, plugins and hooks. It wins over a `CLAUDE_CONFIG_DIR` in the
environment for agents. Before it is saved, the daemon runs `claude auth
status --json` with that directory and refuses one Claude Code is signed out
with; nothing of the answer but signed in or not is kept. While a directory is
chosen for runs that use Claude Code, plan usage also reads that directory's
stored credential (its `.credentials.json`, on macOS first the Keychain item
Claude Code names after the directory) and asks Anthropic for its plan's
usage, under the same rules as the daemon's own: the token is held only for
the request and never stored, logged, returned or refreshed. The plan is
served with the directory's path, your home written as `~`.

### Codex homes

Codex keeps the same in its home: `~/.codex`, or the one `CODEX_HOME` names.
baum treats homes as it treats Claude Code's directories, with a record of its
own, `codex-dirs.json` beside `config.toml` (at most 16, absolute paths). It
reads Codex's sessions (`sessions` and `archived_sessions`) from `~/.codex`,
from the daemon's own `CODEX_HOME` and from the recorded homes. The record is
written by `baum hooks install --user` (`--codex-home`, else `CODEX_HOME`), by
a Codex hook with the `CODEX_HOME` its session runs with, and by Settings. A
rollout path a hook reports is read only inside one of these homes' `sessions`
folders. A home chosen in Settings reaches Codex alone, as `CODEX_HOME`, over
one in the environment for agents; before it is saved the daemon runs `codex
login status` with it and refuses one Codex says is signed out. Plan usage
then reads that home's `auth.json` and session files under the rules of the
daemon's own Codex plan.

Plugin callbacks check only that they run in a Git worktree and that baum's
local daemon has run. They read no hosted credentials, Entire settings or
third-party hook configuration. baum does not install, configure or invoke
Entire. Independently recorded checkpoint refs remain readable.

The daemon installs Git hook dispatchers (`post-commit`, `post-rewrite`,
`pre-push`) in the registered clones, at registration, at start and daily. A
dispatcher runs baum, then the hook the clone had before, which it moved to
`<hook>.pre-baum`; it never overwrites a hook it cannot preserve, a symlink or a
hook Entire manages, skips clones with `core.hooksPath`, guards against
re-entry and never runs a backup that is itself a dispatcher. `git config
baum.hooks false` opts a clone out, and removing a clone from baum restores its
hooks. Dispatchers of earlier versions are repaired in place; other legacy
repository dispatchers are not migrated, and their owners can remove them,
preserving their other hooks, Entire configuration and checkpoint refs.

A Git hook reads one thing from the environment: the session id an agent exports
(Claude Code's `CLAUDE_CODE_SESSION_ID`), kept only if the agent's exported
`CLAUDE_PID` is among the hook's ancestor processes. Any local process can set
these variables, so the resulting commit attribution is local evidence of what
ran on this machine, not proof of authorship. The daemon does not pass them to
processes it starts.

Callbacks report bounded metadata to the local daemon: agent/event or local
`post-commit`/`post-rewrite`/`pre-push`, working directory, session identifiers, transcript
path, source/end reason and terminal location. A Claude notification can also
carry its type and message, cut to 512 bytes. Ordinary capture is bounded to
300 ms; permission and completed-tool input has a two-second bound. Incomplete
permission payloads never ask the app for a decision. Git events omit remote
URLs, which could hold credentials.

Event delivery has a 300 ms budget. A daemon that cannot take the event leaves
it in a private bounded spool (about 1000 events and 8 MiB, oldest dropped
first). Events stay on this machine. Hooks may start a previously run daemon,
except after an explicit stop, with `BAUM_NO_DAEMON=1` or in CI. Permission
answers and queued messages use the daemon's local protocol; absent or
incompatible support leaves the agent's terminal behavior unchanged. See
[local hook contracts](internal/hookcli/README.md).

## What `baum internal doctor` runs and prints

`baum internal doctor` diagnoses the desktop runtime and starts no model. It
runs bounded git/gh version checks and Claude Code/Codex preflight checks,
including Codex's read-only sandbox preflight in a temporary directory. Each
subprocess uses a filtered environment and a process-group timeout. Explicit
`backend_paths`, `GH_PATH` and `PATH` select the tools. Automatic backend
selection considers only Claude Code and Codex and never an API-key fallback.

The report resolves the GitHub credential source, including `gh auth token
--secure-storage --hostname <host>` when no explicit or file credential exists.
Its only network request is the signed price list described above; `--offline`
skips it.
It creates no files of its own: it opens the registry read-only (SQLite
`mode=ro`, so no journal, table or file mode changes) and only when it exists,
and does not create a review store. It asks a running baum daemon for its
version, process id and uptime over the daemon's socket, and never starts one. A config file that does not parse is shown
by position, and by key for a wrong type or value, and never by its text. Asking
gh for a token may wait up to 30 seconds for a keyring unlock prompt. `gh auth token` keeps the environment gh needs to find
its own configuration.

It never prints a token or an API key, or a prefix, length or hash of one. For a
GitHub token it prints only where it was found (an environment variable's name,
the config file, `hosts.yml` or the keyring). It does print paths, tool
versions and the `PATH` entries,
so look at a report before pasting it into a public issue. The desktop app's
"Report a problem" attaches nothing, only the versions (of baum, the OS and
each tool, without paths), or a shorter summary, as you choose with a preview
in the app (the Help and tray menus attach the summary). In the summary home directories
become `~`, secret-shaped values, e-mail addresses and @mentions are masked,
the configuration's load error, the `PATH` entries, the per-repository store
checks (which name your clones) and the names of GitHub hosts other than
github.com are left out, and each line is clipped. It still lists tool versions
and paths, and a machine or host name inside a tool's error text is not
recognised, so review the form before submitting.

## Local session transcripts

The authenticated desktop app serves transcripts only for sessions linked to
the requested pull request or selected live session. Transcripts can include
prompts, agent replies, tool inputs and tool output, including file contents.
The shared redaction engine masks secrets and e-mail addresses, and writes
the home directory as `~` where it starts a path. Paths outside the home and
home paths embedded inside other text may remain as recorded. Access is
bounded by the authenticated local application boundary described below.

## Reading your clone without downloading anything

Local session and checkpoint readers inspect your clone with git without
using the network. They run fixed git commands with no shell. Refs are read in your clone;
commits, trees and Entire checkpoints are read from a private temporary
repository that borrows the clone's objects and has no remote, so a partial
clone's missing objects (a `--filter` clone with a promisor remote) are skipped
with a warning, never downloaded. Each command also runs with
`GIT_NO_LAZY_FETCH=1` and without prompts. Those object reads also ignore your
global and system git configuration, which could otherwise run a filter (such
as an LFS smudge, which contacts a server) or a signature program on a tree
from a clone you have not vetted. The clone's remote URLs are read as
text to match the pull request's repository, and are never contacted.
The list of the clone's worktrees, which says where else its sessions may have
been recorded, comes from `git worktree list` run against the clone's
repository directory with that same environment: no global or system
configuration, no `GIT_` variable of yours, and nothing that reads an object
or runs a program. Because whoever made the clone controls that bookkeeping,
an entry is used only when the worktree's own `.git` file points back at this
repository (or its directory is gone), and never when it is the file system
root, your home directory, or a directory containing the repository, so a
planted entry cannot widen the sessions read to your whole machine. An `Entire-Checkpoint` trailer in a commit is looked up in the
Entire checkpoints already in your clone: the per-checkpoint refs
`refs/entire/checkpoints/*` and the checkpoint branch `entire/checkpoints/v1`,
both read in the private repository. A checkpoint you have not fetched is not
fetched for it; the warning says so and names the command you can run yourself,
`git fetch <remote> '+refs/entire/checkpoints/*:refs/entire/checkpoints/*'` (and, for a
12-hex id, `git fetch <remote> entire/checkpoints/v1`), with `<remote>` the clone's
remote for the repository when it is known.

## Agent isolation

The `claude`, `codex` and `cursor-agent` backends run on your machine under
your user account, each in its own read-only mode, inside a temporary
checkout of the pull request. **`cursor-agent` cannot be held to read-only as
reliably as the other two**; see [below](#the-cursor-agent-backend). baum adds no container or sandbox of its own,
so isolation is only as strong as that tool's read-only mode. Repository
content and pull request text are untrusted input to the agent: a pull
request could try to persuade it to read files outside the checkout. Use
baum on pull requests you are prepared to have an agent read under your
account. The one exception to read-only is a fix you start, which gives the
agent a sandboxed shell with write access to a temporary checkout and, by
default, network access: see
[Fixing a pull request](#fixing-a-pull-request).

The `claude` backend (`agent/claude`, the `User` profile) restricts Claude Code
as far as the tool allows:

- Only the `Read`, `Glob` and `Grep` tools are available. Restricted mode
  removes every tool that runs commands or reaches the network, and file
  tools are limited to the temporary checkout
  (`blockReadsOutsideWorkingDirectories`).
- `--setting-sources ""` skips the user, project and local settings files, so
  only the inline `--settings` baum passes is applied; administrator-managed
  policy settings may still apply. No MCP server is loaded
  (`--strict-mcp-config` with an empty list) for reviews and fixes. A
  run a quick action starts may get exactly one: baum's own,
  named by `--mcp-config` under `--strict-mcp-config` (so the user's, a
  project's and plugins' stay out) and allowed as `mcp__baum`, with the token
  in the environment only, never in the configuration or the arguments (see
  [MCP server for agents](#mcp-server-for-agents)). No session is written
  (`--no-session-persistence`), unless the caller asks the local run to keep
  or resume (`--resume`) a session, which no review or fix does now; the hosted
  profile never does. `--restricted` and the tool allowlist apply on every
  launch, a resume included. Callers must resume only session ids baum itself
  recorded: an id could otherwise name any session the user's CLI stored. baum
  no longer forks a session (`--fork-session`).
- The process gets an allowlist of your environment variables. Provider
  credentials and configuration (`ANTHROPIC_*`, `OPENAI_*`, `AWS_*`,
  `GOOGLE_*`, `AZURE_*`), GitHub tokens, Git settings and the SSH agent are
  removed. `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL` are set only when you
  configure an endpoint for baum.
- Claude Code older than the version baum was checked against is refused with a
  request to upgrade, because an older release may not honour these flags.
- As a backstop, session discovery skips sessions recorded under a directory
  whose name starts with `baum-agent-run-`, the name local callers are meant
  to give a run's temporary directory. The primary protection is that no
  session is persisted and no hook runs.

What remains:

- It runs under your login and your `HOME`, so it uses your Claude
  subscription or key. Claude Code may still update `~/.claude.json` and its
  caches.
- Credentials kept in files under `HOME` stay readable by the Claude Code
  process itself. Its tools cannot read outside the checkout, but a flaw in
  Claude Code or its restricted mode would not be stopped by baum.
- Only environment variables are filtered, not files, the network or other
  processes running as you.

The `codex` backend (`agent/codex`, the `Local` profile) runs `codex exec` in
Codex's own read-only sandbox:

- `--sandbox read-only` stops commands the model runs from writing files or
  reaching the network. It does **not** confine reads to the checkout: the
  model can read any file your account can, such as `~/.ssh` or `~/.aws`, and
  a pull request could try to persuade it to. Baum adds no further
  restriction, and the model's answer is the only thing baum keeps.
- `--ephemeral` writes no session (a local run that keeps or resumes a
  session omits it and runs `exec resume`, with the sandbox set as
  `-c sandbox_mode="read-only"` because that subcommand takes no `--sandbox`;
  approvals stay `-c approval_policy="never"` and the other flags apply as on
  every launch; no review or fix does this now, and baum never runs
  `exec fork`), and the `hooks`, `apps` and `multi_agent`
  features are disabled. Web search is off and approvals are never requested.
- `--ignore-rules` skips user and project execpolicy `.rules` files, and
  `--ignore-user-config` skips `$CODEX_HOME/config.toml` (auth still uses
  `CODEX_HOME`). Codex's help says nothing more about either. In particular,
  baum does not turn off `AGENTS.md` instructions, whether in `CODEX_HOME` or in
  the checkout, so a pull request's `AGENTS.md` may be read by the model as
  instructions.
- The process gets the same environment allowlist as the `claude` backend, plus
  `CODEX_HOME` when you set it. `CODEX_API_KEY`, `OPENAI_*` and the other
  provider and cloud credentials are removed; `CODEX_API_KEY` and the base URL
  are set only when you configure an endpoint for baum.
- A run with an MCP server (none does yet; quick actions will) gets
  `-c mcp_servers.baum.url` (the daemon's loopback `/api/mcp`) and
  `.bearer_token_env_var="BAUM_MCP_TOKEN"`, so Codex reads the token from its
  own environment and it is never an argument. Codex itself makes those
  requests, outside its sandbox (`read-only` or `workspace-write` with
  `network_access=false`), which confines the commands the model runs and not
  Codex's own MCP client; the model gets no network access from it. See
  [MCP server for agents](#mcp-server-for-agents).
- It uses your own login (`codex login`, kept in `CODEX_HOME` or `~/.codex`).
  The output schema is written to a private temporary file that is removed
  after the run; baum never writes into `CODEX_HOME`.
- Codex older than the version baum was checked against (the one the hosted
  agent runs) is refused with a request to upgrade.
- Before its first run, baum runs a trivial read-only command under
  `codex sandbox -P :read-only`, which starts Codex's sandbox without calling
  a model or the network, then checks that a write under the same profile is
  refused. If either fails (on Linux, typically when the kernel or
  container forbids the user namespaces the sandbox needs), baum reports
  "codex could not start its read-only sandbox on this machine" with Codex's
  first error line and does not run Codex without it. Baum has verified this
  only for Linux. On Windows the local `codex` backend is refused, because
  Codex's Windows sandbox was not examined; on macOS the same preflight runs
  but was not tested.

What remains for `codex`:

- Everything the model reads, which can be any file your account can read, is
  sent to the model provider as part of the conversation.
- Users who keep their Codex credentials in the operating system keyring through
  `config.toml` may appear logged out, because `--ignore-user-config` skips that
  file. Baum does not pass the desktop session variables the keyring needs; sign
  in with `codex login` so the login is stored in `CODEX_HOME`.

A review keeps no agent session and no run directory: each runs in a
temporary directory removed when it ends, its CLI sessions unwritten. Earlier
versions of baum kept a local Claude Code or Codex review's session and run
directory under baum's user cache directory (`<cache>/baum/review-runs/`, mode
0700), with the CLI's transcript of it in its usual store, so that a question
about the review (Ask) could fork it. The daemon still deletes those
directories once untouched for 14 days, together with the CLI sessions that
ran in them (forks included), only ever real directories with the run prefix
directly under that root, Claude project directories named after such a
directory and Codex rollouts that record such a directory as their cwd; it
never follows a symlink.

Questions about a pull request are now asked in the session panel (see
[Session panel](#session-panel)). The conversations of the earlier Ask stay in
the clone's review store and are served read-only (`GET /api/prs/chat`): their
answers were masked for secrets before they were stored, their questions are
shown as you typed them, and no new question is run.

The unsandboxed mode (`--sandbox danger-full-access`) exists only as the
`Container` profile for the hosted review agent, which runs inside its own
container. A test fails if any code under `oss/internal` or `oss/cmd` names it.

### The cursor-agent backend

This backend remains an engine implementation for shared package consumers.
The desktop CLI exposes no standalone generation command.
The desktop app and its daemon refuse a job whose backend is `cursor-agent`.

**baum cannot make the Cursor Agent CLI reliably read-only.** It uses every
restriction the CLI offers, and stops a run that strays, but several of
Cursor's own features stay on and its read-only mode is enforced by Cursor,
not by baum. Use it only on pull requests you are prepared to have an agent
read, and possibly act on, under your account. Choose `claude`, `codex` or
`anthropic` where that matters. For the same reason `auto` never picks
`cursor-agent`: it runs only when you set `backend = "cursor-agent"` or pass
`--backend cursor-agent`.

What baum does (checked against the help of cursor-agent 2026.10.16-e3cb207;
the CLI's behaviour behind the flags was read from that release, not tested
against a model):

- It runs `cursor-agent --print --output-format stream-json --mode ask
  --sandbox enabled --workspace <run directory> --trust` in the run directory.
  Ask mode is the CLI's read-only mode ("Q&A style for explanations and
  questions (read-only)"), and a headless run cannot switch out of it.
  `--sandbox enabled` runs any command the agent still starts in Cursor's
  sandbox, which confines writes to the workspace (the temporary run
  directory, not your clone), and the CLI exits rather than run unsandboxed
  where the sandbox cannot start. `--trust` only answers the workspace trust
  prompt for the run directory; baum never passes `--force`, `--yolo`,
  `--auto-review` or `--approve-mcps`, so commands and MCP servers are not
  approved by baum.
- It stops the run, kills the CLI's process group and fails the attempt
  (`disallowed_tool`) as soon as the CLI reports any tool call other than
  reading, listing or searching files or updating its to-do list: shell
  commands, edits, deletes, MCP tools, web search and fetch, subagents, mode
  switches and any tool baum does not know. Detection fails closed: it is an
  allowlist of the event types, subtypes and text content the CLI writes in a
  run that only reads, and any other event, or one baum cannot decode, stops
  the run too. The
  attempt is not retried, so a pull request that steers the model into such
  a call gets no second try. The CLI reports a call as it starts, so this
  limits what a stray call can do; it does not prevent it.
- The process gets the same environment allowlist as the `claude` backend, plus
  `CURSOR_CONFIG_DIR`, `CURSOR_DATA_DIR` and `AGENT_CLI_CREDENTIAL_STORE` when
  you set them. `CURSOR_API_KEY`, `CURSOR_AUTH_TOKEN` and the other provider,
  cloud and GitHub credentials are removed: it uses your own `cursor-agent
  login`. baum gives it no endpoint or key of its own.
- It runs only an executable whose `--help` names the Cursor Agent and lists
  every option above (an executable called `agent` is used only then), so an
  older CLI that would ignore or reject one is refused.

What remains for `cursor-agent`:

- **Reads are not confined to the checkout** by anything baum controls. The
  read tools ask mode offers may read any file your account can, such as
  `~/.ssh` or `~/.aws`, and what the model reads is sent to Cursor and the
  model provider it routes to.
- **Your Cursor configuration applies.** No flag turns off hooks: user, team
  and enterprise hooks (`~/.cursor/hooks.json`, `/etc/cursor/hooks.json` and
  the like) and the Claude Code user hooks Cursor also loads run during the
  review, as you. Rules, `AGENTS.md` files and skills in the checkout or your
  home directory may be read as instructions. The pull request's own
  `.cursor/hooks.json`, `.cursor/cli.json` and `.cursor/mcp.json` (and its
  `.claude` settings) are inactive only because baum unpacks the checkout
  under `head/` and `merge-base/` in the run directory, not at its root, which
  is the workspace `--trust` covers; a test fails if that layout changes. MCP servers you approved before
  may start, though calling one stops the run. If your Cursor CLI config sets
  the approval mode to run everything, or allow-lists commands, the CLI may
  run a command without asking; it runs in the sandbox, and baum stops the run
  when it sees the call.
- **The run is kept in Cursor's own chat history** (under `~/.cursor` or
  `$XDG_CONFIG_HOME/cursor`), with the prompt and bundle, and the run
  directory may be recorded as a trusted workspace; there is no flag to turn
  either off. baum's own session discovery reads Cursor's editor store, not
  these CLI chats, and the prompt starts with the generation-run marker, so
  the run is never used as evidence.
- Only environment variables are filtered, not files, the network or other
  processes running as you, as for the other CLI backends.
- Usage is the run total the CLI reports at the end, so the token budget is
  checked only then; a call that is not reported is not counted. The cost is
  billed by Cursor and recorded as unknown.

### The anthropic backend

This backend remains an engine implementation for shared package consumers.
The desktop CLI exposes no standalone generation command.
The desktop app and its daemon refuse a job whose backend is `anthropic`, and
`auto` there never falls back to it, so the app spends nothing on an API key.

The `anthropic` backend (`agent/anthropic`) runs no agent process. baum itself
calls the Anthropic Messages API (`https://api.anthropic.com/v1/messages`) and
runs the model's tool calls in-process:

- The model gets four tools: `Read`, `Glob` and `Grep`, which read the
  temporary run directory, and `StructuredOutput`, which submits the document.
  There is no tool that writes, runs a command or reaches the network, and a
  call to any other tool name is refused.
- Every path is relative to the run directory. An absolute path, a `..`
  element or a backslash is refused before any file is opened, and the
  directory is opened as an `os.Root`, so a symbolic link whose target is
  outside it cannot be read; listing and searching never follow a link at all.
- Reads are bounded: a file over 4 MiB is not read, `Read` returns at most
  2,000 lines of 2,000 characters, a search returns at most 500 paths or 200
  matching lines, skips files over 4 MiB and binary files, and visits at most
  200,000 entries, and no text tool result exceeds 64 KiB. `Grep` uses Go's
  RE2 expressions and globs are matched in time proportional to the pattern
  and path lengths (with at most eight `**` segments), so neither can be made
  to run for long.
- `Read` sends a PNG, JPEG or GIF file as an image only when the standard
  library decodes its header, it is at most 2,000 pixels a side and 1 MiB, and
  the run has sent fewer than 10 images and 5 MiB of them; otherwise the
  model gets a short note instead. Other image formats are treated as binary.
- What the model reads is sent to the Anthropic API as part of the
  conversation, as it is, unredacted, like the checkout files the other
  backends read. Nothing outside the run directory is.
- The key comes only from `ANTHROPIC_API_KEY` (or, for a host, the endpoint it
  configures). It is sent only in the `x-api-key` header to the API's own
  address; redirects are not followed, so it never reaches another host. It is
  never written to the config file, the run directory, the diagnostics or the
  logs, and any error text that would contain it has it replaced.
- Diagnostic events record tool names, sizes, statuses and token counts, and
  a path only when it named an existing file or directory in the run
  directory (otherwise `(refused path)`), never file contents, search
  patterns or other model text.

## Fixing a pull request

A fix (`internal/fix`, run as a job of the local application) is the one
time an agent gets **write access**. It is one of baum's few writes to GitHub
(see "Writes to GitHub" below): it pushes a commit and, when it addressed
review comments, replies on those threads and resolves them.
It runs only when you start it on one pull request (its button or key, with
no further dialog); it never runs automatically. The daemon checks fresh
GitHub evidence when you start it and again before anything runs: the pull request is open, its branch
is in the repository itself (a fork is refused), your account can push to the
repository (`permissions.push` on `GET /repos/{owner}/{repo}`, which write,
maintain and admin access give; who wrote the pull request does not matter),
the head is still the one it started from, and it still has a merge conflict
(GitHub's `mergeable`), failing checks or, on a pull request you opened, the
same unresolved review threads with the same comments (a digest of them is
taken when you start it, so a comment added or edited afterwards stops the
fix before the agent runs). A registered local clone is required. Only the `claude` and `codex` backends fix; `cursor-agent` and
`anthropic` are refused (and the desktop app runs neither at all).

A fix lives in `fixes/<id>/` in your user cache directory
(`$XDG_CACHE_HOME/baum` or the platform's), outside the clone and outside the
system temporary directory. Its branch and the base branch are fetched into
`refs/baum/fix/<id>/` of your clone, as a review's missing commits are, for
every fix; a branch that moved on stops the fix. The directory holds:

- `checkout`, the files the agent works on;
- `git`, a git repository of the agent's own, whose `.git` file in the
  checkout names it. baum makes it with no template (so no hook), borrows
  your clone's objects through `objects/info/alternates`, checks the pull
  request's branch out at the head and gives the base branch as
  `origin/<base>`. It has no remote URL and no credentials. Its commits are
  not signed.
- `tmp`, the agent's scratch directory, which also holds its package and
  build caches;
- `hooks`, an empty directory, and `index`, baum's own index, which the agent
  cannot write.

baum writes the checkout's files with its own git and your clone's
configuration before the agent runs, so smudge filters such as Git LFS find
your clone's store and remote; the agent's repository only records them.

For a conflict, baum starts the merge of the base branch in the agent's
repository itself, before the agent runs, so the agent finds the conflict
markers. When the fix ends, however it ends (success, failure, cancellation),
the directory and the temporary refs are removed. A directory a crashed
daemon left is removed by a later fix once it is a day old.

What the agent can do, per backend:

- `claude` runs in restricted mode, as a review does, with the `Read`,
  `Glob`, `Grep`, `Edit`, `Write` and `Bash` tools, `--permission-mode
  acceptEdits` and Claude Code's sandbox (bubblewrap on Linux, Seatbelt on
  macOS) passed in `--settings`: `sandbox.enabled`, `failIfUnavailable`,
  `autoAllowBashIfSandboxed`, `allowUnsandboxedCommands: false`, and an
  allow rule for `Bash` so that no command waits for an approval a `--print`
  run cannot give. Every command runs in the sandbox, and none outside it.
  Commands can write only to the checkout, the agent's repository and the
  scratch directory (and Claude Code's own temporary directory,
  `/tmp/claude-<uid>`, which its sandbox always allows). They can read your
  home directory (`allowRead`, since the sandbox hides it otherwise) and
  your clone's objects, except the credential paths below (`denyRead`).
  Restricted mode keeps the file tools in the checkout
  (`blockReadsOutsideWorkingDirectories` is left out: with it, Claude Code
  hides the home directory from the sandbox and then fails every command
  when a file it protects there, such as `~/.gitconfig`, is a symbolic
  link). Settings files, MCP servers and
  sessions are off as in a review. A write the sandbox blocks can appear to
  succeed and leave nothing; the instructions say so.
  When the sandbox cannot start (no bubblewrap or socat, an unsupported
  platform), Claude Code refuses to run before any model call, and baum runs
  the fix again as before the sandbox: file tools only, no command, no
  network, and the daemon log says why.
- `codex` runs in Codex's sandbox. From Codex 0.170.0 on it is a permission
  profile (`default_permissions` with `permissions.baum_fix`): everything
  readable, the credential paths below unreadable (`none`), and the checkout,
  the agent's repository and the scratch directory writable. An older Codex,
  or one on Windows, runs in `workspace-write` with the agent's repository and
  the scratch directory as `writable_roots` and `/tmp` and your `TMPDIR`
  excluded; it **cannot deny reads**, so its commands can read any file your
  account can. The scratch directory is the commands' `TMPDIR`. baum checks
  before the first run that Codex's sandbox starts (the read-only
  preflight); the write-enabled profile is not checked separately.
- The agent's repository sits outside the checkout because Codex's sandbox
  keeps a `.git` directory under a writable root read-only, and Claude
  Code's protects `.git/config` and hooks; a repository of its own outside
  the checkout is writable in both, so the agent can use git freely: log,
  diff, show, blame, merge, commit, branch.
- **Network access is on by default** (the fixes setting **Network access**,
  `fix_network`). With it the agent can install dependencies and fetch what
  builds and tests need, which most fixes of failing checks require: without
  it, a project whose dependencies are not already on disk cannot be built or
  tested in the sandbox, and the agent fixes blind. For Claude Code every
  domain is reachable (an allow rule `WebFetch(domain:*)`, since the
  sandbox's `allowedDomains` takes no wildcard for all domains; the WebFetch
  tool itself stays off); for Codex it is the profile's `network.enabled` or
  `sandbox_workspace_write.network_access`. Turned off, Claude Code's
  sandbox allows no domain (`allowedDomains: []`), Codex gets no network, and
  the instructions tell the agent so. A setting that allows only package
  registries is not offered.
- Because the agent can reach the network, its commands cannot read the files
  where tools keep credentials, under your home directory: `~/.ssh`,
  `~/.gnupg`, `~/.aws`, `~/.azure`, `~/.config/gcloud`, `~/.kube`,
  `~/.docker/config.json`, `~/.netrc`, `~/.git-credentials`,
  `~/.config/git/credentials`, `~/.config/gh`, `~/.config/hub`, `~/.npmrc`,
  `~/.yarnrc.yml`, `~/.pypirc`, `~/.gem/credentials`, `~/.cargo/credentials`,
  `~/.terraform.d/credentials.tfrc.json`, and the agent CLIs' own logins
  (`~/.codex/auth.json`, `~/.claude/.credentials.json`, and those under
  `CLAUDE_CONFIG_DIR` or `CODEX_HOME`), and the session's runtime directory
  (`$XDG_RUNTIME_DIR`, `/run/user/<uid>` on Linux), whose D-Bus socket
  reaches the Secret Service keyring (where baum keeps its own GitHub token,
  and `gh` and git credential helpers often keep theirs) and which holds the
  SSH, GPG and keyring agents' sockets. Claude Code's sandbox also refuses
  Unix socket connections on Linux. The CLI itself, outside its commands'
  sandbox, still signs in with them. This holds whether the network
  is on or off, and on a Codex older than 0.170.0 it does not hold at all.
- Both get the same environment allowlist as a review: no SSH agent, no
  `GIT_` variable, no GitHub token, no provider credential baum did not set.
  On top of it a fix removes, also from the variables you set yourself, any
  GitHub or git-host token (`GITHUB_*`, `GH_*`, `GITLAB_TOKEN` and the like),
  registry tokens (`NPM_TOKEN`, `NODE_AUTH_TOKEN`, `TWINE_PASSWORD`,
  `CARGO_REGISTRY_TOKEN`), the SSH agent and askpass helpers, Git variables
  that choose a repository, and any variable whose name says it holds a
  token, secret or password, except the agent CLI's own provider settings
  (`ANTHROPIC_*`, `CLAUDE_*`, `OPENAI_*`, `CODEX_*`, `AWS_*`, `GOOGLE_*`,
  `VERTEX_*`, `AZURE_*`), which a Bedrock, Vertex or API-key sign-in needs and
  which therefore reach the commands too. It points `GOCACHE`, `GOMODCACHE`,
  `GOPATH`, `npm_config_cache`, `YARN_CACHE_FOLDER`, `PIP_CACHE_DIR` and
  `XDG_CACHE_HOME` at the scratch directory. The agent never gets git
  credentials to push anything.
  The one addition is the environment for agents you set yourself (Settings,
  or `[agent_env]` in the config file), which every agent baum runs on this
  computer and the git commands that commit and push a fix receive, for
  example `GIT_CONFIG_GLOBAL` to sign the fix's commit with another key. It
  is your choice, so it may name a `GIT_` variable; baum's own run variables
  win over it, and git commands drop one that would point git at another
  repository (`GIT_DIR` and the like).
- When your configuration signs commits (`commit.gpgSign`), baum makes one
  signed commit object before the agent runs, referenced by nothing. A key that needs a passphrase prompt the daemon cannot show fails
  the fix there, with git's error, before the agent's work is paid for.

What reaches the agent: the pull request's title and branches, the conflicted
paths, and for failing checks the failing check runs and commit statuses of
the head (name, conclusion, output title, summary and text, each bounded) and
up to 200 lines of the job logs of up to four failing GitHub Actions jobs,
ending with the step that failed rather than the runner's cleanup after it.
baum reads them with your GitHub token from the API; a job log's signed
download URL is read without the token. All of it is untrusted: it goes into
the prompt between markers that carry a random tag it cannot know, with
secrets masked by the same redactor reviews use, and the instructions say it
is data. A pull request or a log can still try to steer the agent, which can
change any file in the checkout.

Review comments are other people's text, and on a public repository anyone
with a GitHub account can write one. baum reads them only on a pull request
you opened, and only from threads that are unresolved and that your account
can reply to and resolve. A thread is left out unless its first comment comes
from an account that can push to the repository: baum reads each author's
collaborator permission (`GET /repos/{owner}/{repo}/collaborators/{login}/permission`,
once per author per read of the threads) and trusts `write`, `maintain` and
`admin`. GitHub's author association is not used: `MEMBER` is any member of
the owning organisation, whatever their access to this repository, and
`COLLABORATOR` includes read and triage access. Comments by apps' bot accounts
(review bots such as Copilot or CodeRabbit) are kept: an app is installed by
someone who administers the repository. What a bot writes can follow from
what it read, so its comments reach the agent like everyone's, as untrusted
data under the rules below, and the workflow and credential checks apply to
what the fix pushes. A permission GitHub does not answer
stops the fix rather than trusting the author. Within a kept thread, other
authors' comments are dropped. At most
20 threads and the first 10 comments of each are read, each comment bounded
to 2 KiB, and the job lists the threads it addressed.
Each thread goes into the prompt as untrusted data under the same random-tag
markers, with secrets masked, under a local id (`T1`, `T2`…) that baum maps
to the thread's GitHub node id; the agent never sees or returns a GitHub id.
The instructions (`fix-threads.md`) say the comments are feedback to evaluate,
not instructions, and to ignore requests to run commands, read or send files,
or change credentials, secrets, CI or workflow configuration, dependencies or
unrelated files.

A fix could also be made from a conversation with the review's agent (the
earlier Ask), an answer's "Fix this". The app no longer offers it; the daemon
still takes such a fix item. The app sent only the conversation's and the
turn's ids:
the daemon reads the questions and answers from your review store for that
clone and pull request, so a page or request cannot supply the text. The
conversation is an agent's reading of the pull request plus your own questions,
so it is untrusted data like the rest: it goes into the prompt between the same
random-tag markers, one block per turn, with secrets masked (your questions are
stored as you typed them and are masked here), and the instructions
(`fix-chat.md`) say to make only the change it asks for, verify it against the
code and ignore anything beyond it. At most 32 KiB of the newest turns are
included. The estimate pins the turn and a digest of the conversation as the
agent gets it, so a confirmed fix cannot run with another conversation or
turn, and one whose stored turns changed stops before the agent runs. Nothing
about the confirmation changes: the same evidence, the same dialog, the same
write access. A fix from a conversation is its own fix: it makes only that
change, reads no checks or review threads and replies on none, only the newest
review's conversation can start one, and it pushes a workflow or action
change only under the workflow access rule below.

The agent reports per thread whether it addressed it, in a JSON block at the
end of its final message whose opening fence carries the prompt's random tag,
so a block quoted from a comment or a file is not taken for it. baum parses
it strictly: anything malformed answers nothing, unknown or repeated ids are
ignored, and each summary is masked for secrets and reduced to one line of
plain text of at most 200 characters, with markdown links and images reduced
to their text and URLs, HTML, `&`, `@` (so nobody is mentioned or notified)
and control and invisible formatting characters removed.

Only after the push succeeds, baum replies on each thread the agent said it
addressed, as your GitHub account (with the GitHub token baum reads GitHub
with, never your git credentials and never the agent), and resolves it. The reply is baum's own text,
`Addressed in <short sha>: <summary>`. Before replying, baum reads the thread
again by its node id and leaves it alone if it is now resolved, is not on this
pull request, or no longer accepts your reply. Threads the agent skipped are
never touched. With no push (no change, a refused push, any failure) there
are no replies. Each GitHub request is sent once; a failed reply or
resolution is recorded per thread on the job, never retried, and never fails
the fix.

What baum does after the agent:

- It never runs git with the agent's repository as its git directory once the
  agent has run, and never reads its configuration, hooks or attributes:
  anything the agent wrote there (a `core.fsmonitor`, a hook, a filter or
  merge driver, `info/attributes`, an `include`) is never used. baum's own
  commands name your clone's git directory (`GIT_DIR`), the checkout
  (`GIT_WORK_TREE`) and baum's own index (`GIT_INDEX_FILE`), so a `.git` file
  the agent rewrote is ignored, and run with `core.hooksPath` pointing at an
  empty directory, so no hook runs, neither the clone's nor one the pull
  request carries (such as a `.husky` directory); the file-system monitor,
  the untracked cache and sparse checkout are off. Your clone's HEAD, index
  and refs are not touched; only the objects of baum's commit are written to
  it.
- Of the agent's repository baum reads `HEAD`, `MERGE_HEAD` and the refs they
  name, as files (no symbolic link is followed), and then, with your clone's
  git and the agent's objects borrowed for that one command (no commit-graph
  file trusted), whether the base branch's commit is an ancestor of one of
  them. That decides only whether the result merges the base branch.
- baum makes its own commit of everything the agent left in the checkout,
  ignored files excepted, with your git identity and signing configuration:
  a merge commit of the head and the base when the result merges the base (a
  conflict fix always does), one commit on the head otherwise. Nothing the
  agent committed, staged or branched is pushed, so a fix only adds commits
  on the head and never rewrites the branch. A file the base tracks although
  an ignore rule covers it stays in a merge commit. It refuses to commit while
  a conflicted file of its own merge still has a conflict marker, refuses a
  conflict fix whose agent abandoned the merge (`conflicts_remain`), and fails
  when nothing changed.
- It still checks that the result descends from the head it started from.
- It checks what the fix itself changed: the paths and lines the result has
  that the pull request's head did not, and, when the result merges the base
  branch, those that differ from the merge git makes without conflicts (so
  reverting a change the base made counts as the fix's), or, when git's merge
  conflicts or the git is older than 2.38, only paths that also differ from
  the base and lines neither side had. Submodules are included whatever your git configuration says. A
  conflict marker line among those lines refuses the fix
  (`conflicts_remain`), so a merge the agent made and left unresolved is not
  pushed.
- It refuses a result that adds a credential: a line the fix added on which
  the redactor reviews use finds a secret (e-mail addresses aside; the whole
  file is searched, so a private key whose first line the file already had
  is still found), a new or changed file at a well-known credential path
  (`id_rsa`, `id_ed25519` and the other OpenSSH private key names,
  `.git-credentials`, `.netrc`, `.aws/credentials`, `gh/hosts.yml`), or a text
  file over 8 MiB, too large to check. The refusal names the files, with
  secrets in their names masked, never the secret. Binary files are not read.
- It refuses a result that adds or changes a gitlink (mode `160000`): `git add`
  records a repository the agent made in the checkout as a commit of another
  repository, which the push would carry without its content.
- A workflow runs with the repository's secrets as soon as it is pushed, so
  workflow changes follow the workflow access you gave baum's GitHub
  connection, the opt-in **Reconnect with workflow access** in Settings that
  merges of workflow changes already need. When the saved connection is
  known to lack it, a fix whose prompt held review comments or a
  conversation (other people's or an agent's words) refuses a result that
  changes anything under `.github/workflows/` or `.github/actions/`
  (`workflow_changed`), whether or not it also fixes conflicts or checks; the
  refusal says to choose Reconnect with workflow access in Settings and run
  the fix again. With workflow access, or when baum cannot know the token's
  scopes (an explicit token or `GITHUB_TOKEN`), every fix may push one. A
  connection whose status cannot be read refuses them. The push itself uses
  your git credentials: GitHub refuses a workflow change from an HTTPS token
  without the `workflow` scope, but an SSH key, or a token that has the scope,
  is not limited, so with workflow access a review comment that steers the
  agent can bring about a `pull_request` workflow change that runs with the
  repository's secrets. A
  workflow change the merge of the base branch brought is not the fix's. A
  fix of conflicts or failing checks alone may change a workflow.
- The push uses your git credentials, not baum's connection. When GitHub
  refuses it because that credential lacks the `workflow` scope ("refusing to
  allow ... to create or update workflow ... without `workflow` scope", read
  from git's output), the fix fails with `workflow_scope`, which says to give
  the credential the scope (for the GitHub CLI, `gh auth refresh -s
  workflow`), rather than as an ordinary refused push.
- It pushes with a plain `git push --no-verify <remote> <commit>:refs/heads/<branch>`
  from your clone to the remote whose URL names the repository: never forced,
  so GitHub refuses it if the branch moved on, and nothing else (no tags, no
  submodules). The push uses your own git configuration and credentials (your
  credential helper, or your SSH agent as background fetches use it), never
  baum's GitHub sign-in token: push credentials stay with baum and git, never
  with the agent.

What remains:

- The agent's changes are pushed without anyone reviewing them first. Look at
  the commit before you merge the pull request.
- The agent's commands can read files outside the checkout that are not on
  the credential list (a project's `.env` elsewhere, a key in an unusual
  place), and, on a Codex older than 0.170.0, the credential files too. With
  network access on, they could send what they read anywhere; a pull request,
  a check log or a review comment could try to make the agent do so. Without
  network access, they could still copy it into the checkout: baum refuses to
  push what the redactor recognises as a secret or what sits at a
  well-known credential path, but a credential in a format it does not know,
  encoded, renamed, split so no line holds it whole, or inside a binary file
  would still be committed and pushed to the pull request's branch, which may
  be public. Turn **Network access** off for fixes if you want the agent
  offline.
- Provider settings the agent CLI signs in with stay in its environment:
  an API key baum sets for an endpoint, or `ANTHROPIC_*`, `AWS_*` and the
  like from your agent environment. Claude Code's commands inherit them,
  and with network access on they could send them out. A Claude Code
  subscription login is not in the environment.
- On macOS, whether the sandboxes let commands ask the keychain for an item
  without a prompt was not checked.
- With network access on, the agent's commands can also download and run
  code (a dependency's install script, a tool), as a build on your computer
  would, inside the same sandbox.
- Claude Code's sandbox always lets commands write to its own temporary
  directory (`/tmp/claude-<uid>`), which other Claude Code sessions of yours
  use too.
- The commit is yours: your identity, and your signature if you sign commits.
  Clean and smudge filters your git configuration defines (Git LFS, for
  example) run on the checkout as they do in your clone.
- A review comment from a trusted author can still try
  to steer the agent, as a pull request or a log can, and the agent decides
  which threads it addressed: it could claim threads it did not address, and
  baum would resolve them, hiding that feedback until someone reopens it. The
  reply names the commit, so reviewers can check it.
- The summaries baum posts are the agent's words, published on GitHub under
  your name. They are short, plain and masked for known secret formats, but a
  secret in a format the redactor does not know, or text an injected comment
  asked for, could still appear in one, as it could in the pushed commit.

## Writes to GitHub

The local application writes to GitHub only when you click and confirm, one
pull request at a time, with the credential you saved for baum. An agent may
propose a merge, a branch update, a re-run, resolving a thread or a fix, but it
runs only when you confirm it in the app, through the same route the button
calls (see "MCP server for agents"). There are seven writes and no others:

- **Merge** (`internal/github/merge.go`): GraphQL `mergePullRequest` with
  `expectedHeadOid` set to the head you saw.
- **Fix push** (`internal/fix`): a plain `git push` of baum's commit of the
  agent's changes to the pull request's branch, never forced (see "Fixing a pull request").
- **Review thread replies** (`internal/github/threads.go`): after a fix that
  addressed review comments pushed, `addPullRequestReviewThreadReply` with
  baum's own "Addressed in <commit>: <summary>" text, then
  `resolveReviewThread`, only on the threads baum put in the fix's prompt.
- **Update branch** (`internal/prupdate`): `PUT
  /repos/{owner}/{repo}/pulls/{number}/update-branch` with `expected_head_sha`
  set to the head you saw. GitHub merges the base branch into the head branch.
- **Re-run checks** (`internal/prupdate`): `POST
  /repos/{owner}/{repo}/actions/runs/{run_id}/rerun-failed-jobs` for each failed,
  cancelled or timed-out workflow run of the head commit. The daemon reads the
  run list itself; a request names no run.
- **Resolve a review thread** (`internal/prupdate`): `resolveReviewThread` on
  the one thread you clicked **Resolve** on in the Inbox. The click is the
  confirmation, since GitHub undoes it with one click; the daemon first reads
  the thread and refuses one that is not on that pull request or that GitHub
  says your account cannot resolve. It posts no reply.
- **Re-run one job** (`internal/prupdate`): `POST
  /repos/{owner}/{repo}/actions/jobs/{job_id}/rerun` for the one GitHub Actions
  job you clicked **Retry** on in the Inbox. Like Resolve, the click is the
  confirmation. The daemon re-reads the head commit's failing checks and
  refuses a job that is not one of them, so a request cannot re-run any other
  job.

Each write goes through the daemon's authenticated, CSRF-checked local API,
only for repositories you picked, and is re-checked against GitHub just before
it is sent: the pull request is open, the head is still the one you confirmed
(or, for a fix, started from) and, for update branch and both re-runs, your account can push to the
repository (`permissions.push`). Resolving a thread checks the thread instead, as above. Update branch also refuses a fork's branch and a
conflicting pull request. None is retried, and none is ever started by an agent
or automatically: thread replies follow only a fix you started.

## Local repository registry

`internal/repos` stores clone paths, git common directories, normalized GitHub
identities and generation preferences in a 0600 SQLite database beside the XDG
config file. Remote URLs and their credentials are never persisted or returned.
A repository can also be picked by its `owner/name` alone, without a local
clone; that stores only the name, reads no local git and contacts nothing.
Registration and availability checks read local git metadata only; they do not
resolve tokens, clone, fetch or contact GitHub/model providers. Registration
starts nothing by itself, but automatic generation is on by default: once the
daemon has seen a registered repository's open pull requests, later new heads
and review requests can start paid runs (see "The baum daemon"). The registry
also holds the global preferences that control this.

The registry HTTP handler is mounted by the baum daemon behind its authenticated
Host/Origin boundary: its write routes can register local paths, pick repositories, remove records
and change settings. Clone registration through `baum internal repos add`
changes no hooks or Git configuration.

### Listing your GitHub repositories and cloning one

To let you pick repositories by name, the daemon asks GitHub (`api.github.com`
GraphQL, with the token baum already uses for pull requests) for the
repositories you own, collaborate on or reach through an organization, newest
push first, at most 300, and for the open pull requests that involve you. Only
GitHub receives these requests; the token goes nowhere else. The result is kept
in the daemon's memory for five minutes and is served only behind its
authenticated Host/Origin boundary. It needs no scope beyond the `repo
read:org` sign-in already requests.

When you ask to clone a repository, baum runs `git clone` of
`https://github.com/<owner>/<name>.git`, a URL built from the validated name, never
from text you or the app supply. With the GitHub sign-in the token reaches git
only in its private child environment, as an `Authorization` header scoped to
that URL (the same way fetches work); it is never in the arguments, a file, a
log or an error, your global credential helper and header settings are cleared
for that command, redirects are refused, and git cannot prompt. Without a
sign-in a public repository clones anonymously. With a token, certificate
verification is forced on, including when `GIT_SSL_NO_VERIFY` is set.
The clone disables Git hooks (a temporary empty hooks directory), repository
templates, the file-system monitor, submodule recursion and every protocol but HTTPS, and does not keep
those settings. It writes into the folder you choose (default beside your
other clones, else `~/code/<owner>/<name>`), which must be inside your home
directory and absent or empty; a code-folder symlink directly under home may
point to another disk. Overlapping running targets, including symlink aliases,
are refused. Missing parent folders are created with normal permissions (0755).
A failed or cancelled clone removes only its own target and empty parents it
created; a pre-existing empty target is left for git to clean up.
Your own Git configuration (proxy, trusted certificates) still applies. URL
rewrites that change the requested clone URL, including to another GitHub
repository, are refused with an explanation. Global checkout filters such as
git-lfs can still run: the repository selects them with `.gitattributes`, and
they can contact LFS storage hosts (including one chosen by `.lfsconfig`).
The GitHub header stays scoped to
the clone URL; it is not sent to those other hosts.
The clone is then registered like any other.

### Showing what is left of your Claude and Codex plans

To show how much of your plans remains, the daemon reads two things. For Codex
it reads the rate-limit figures Codex wrote into its own session files
(`$CODEX_HOME/sessions`, default `~/.codex/sessions`): only the last megabyte of
the eight newest files, only the usage percentages and reset times, with no
network request. For Claude it reads the sign-in Claude Code stored (the macOS
Keychain item "Claude Code-credentials", else `$CLAUDE_CONFIG_DIR/.credentials.json`,
default `~/.claude/.credentials.json`) and asks `https://api.anthropic.com/api/oauth/usage`,
the endpoint Claude Code's own `/usage` uses, with that token. This is the one
outbound host this feature adds (`api.anthropic.com`), and Anthropic is the same
provider that issued the token. The endpoint is undocumented and may change;
when it fails baum shows the plan as unavailable. The token is held only for
the request: it is never refreshed (that would rotate Claude Code's own
credential), logged, stored, served to the app or put in an error, and redirects
are refused. Results are kept in the daemon's memory for five minutes, at most
one request a minute, and are served only by `GET /api/plan-usage` behind the
daemon's authenticated Host/Origin boundary.

For the Analytics page the daemon also reads the plans every 15 minutes while
it runs, through the same cache, so Claude's endpoint is asked at most that
often even with no window open. It keeps only the snapshot time, provider,
window name, percent used and reset time, in the `state/plan-usage-history` document in `app.sqlite` in its
state directory (`$XDG_STATE_HOME/baum`, default `~/.local/state/baum`), for 90
days.

### Local application database

Durable repository settings, review groups, decisions/features, jobs, inboxes,
analytics/history and persistent caches share `$XDG_STATE_HOME/baum/app.sqlite`
(default `~/.local/state/baum/app.sqlite`). The containing directory and file
are private to the current user (0700/0600 on Unix; a current-user-only DACL on
Windows). SQLite journals inherit that protection. Small state/cache records
are JSON documents in database rows, rather than separate files. The schema
baseline is identified and versioned; newer and unrecognized databases are
refused without being replaced. Pre-release storage is not imported.

Per-clone review versions, attachments and chats remain in the clone's
`versions.sqlite`. OS credential storage, optional configuration, daemon
launch credentials, locks, hook spool/event records, live-session recovery
snapshots, logs, fix transcripts and agent work directories remain separate
so startup and failure recovery can work independently of the database.
See the [database storage map and release checks](internal/appdb/README.md).


### Analytics

The Analytics page (`GET /api/analytics`) is built from what is already on the
computer, plus one GitHub search per picked repository:

- **Agent sessions.** The daemon reads the Claude Code, Codex and Cursor session
  stores (`~/.claude/projects` and the `projects` folder of the other
  [Claude Code configuration directories](#claude-code-configuration-directories)
  it knows, `~/.codex/sessions` and the same folder of the other
  [Codex homes](#codex-homes) it knows, Cursor's `state.vscdb`),
  the same files the PR page reads, but only sessions whose working directory is
  inside a registered clone and that were written in the last 90 days. It keeps
  aggregates only, in the `analytics_sessions` table of its private application database: per
  session file, its path, size and modification time (to
  read only what changed), the agent, session id, clone and repository, the
  top-level branch, start and end, active seconds by hour, counts of human
  prompts, tool calls, failed tool calls, subagents and model calls, token sums
  by model, and the ids of commits the session recorded creating. No prompt,
  reply, tool input or output, file path inside the repository or other session
  text is stored. baum's own generation runs are marked and never counted.
- **Your commits.** In each registered clone it reads the configured
  `user.email` and lists that author's commits on local branches from the last
  90 days (ids, dates and trailers), through the same private read-only
  repository the PR page uses; nothing is fetched. They are held in memory for
  five minutes and not stored.
- **Fix transcripts.** What a fix's agent said and ran, with its tool output,
  in `transcripts/` beside `app.sqlite` (private files, secrets masked, bounded,
  the 50 latest fixes). It stays on your machine.
- **baum's own work.** The jobs table (`jobs` in `app.sqlite`) and a
  small ledger (`state/analytics-actions` in that database) of the merges,
  branch updates and check re-runs you made from the app (time, kind,
  repository and pull request number), kept 90 days.
- **Delivery.** For each picked repository, one GraphQL search of GitHub with
  your GitHub token for the pull requests you opened in the last 90 days
  (`repo:owner/name is:pr author:@me`): number, state, draft, created and
  merged times, the first reviews' times and authors, whether GitHub reports a
  conflict and the head commit's combined check state. It is a read; the answer
  is kept in the daemon's memory for an hour, never on disk, and forgotten when
  the GitHub account changes. A failure is shown in fixed words, never GitHub's
  text.

Nothing of it is sent anywhere else. Costs are computed on the computer from the
price list.

### Timeline

The Timeline page (`GET /api/timeline/...`, behind the app's authentication)
reads the private decision store, the clones and, for a decision's session,
that session's transcript on this computer: the picked repositories whose
decisions are not turned off, their features of the version in use (text,
status, pull requests' numbers, titles, states and authors' logins, counts and
key decisions), their sessions' decisions (summary, category, who decided,
rejected alternative, cited files, branch; current ones, and a decision a later
capture replaced when it is opened by its id, with those replaced with it) and,
per pull request, the summary of the latest review saved in the clone or the
generated one. The sessions' person is the user: the first served clone's git
`user.email` and `user.name`, named also by every served clone's `user.email`
and by their GitHub login's `@users.noreply.github.com` address. A decision's
detail shows the human prompts its decisions cite, read from the session's
transcript where capture stored its path, under the same rules as the Sessions
page's transcripts (`livesessions.Locate`: a regular file inside the agent's own
session folder, links resolved; a transcript over the capture size cap is not
read) and redacted as decision capture renders them (secrets and identities
masked, the harness's wrappers stripped); without a readable transcript they
are withheld. The redacted prompts of at most 16 sessions are held in memory
(`MaxCachedPrompts`), keyed by the transcript's size and modification time, and
are not served once the file can no longer be located. A session's transcript
opens through `GET /api/timeline/sessions/{agent}:{id}/transcript`, as
described below. No rationale or quoted evidence is served, and the Timeline
makes no network request; a repository not picked, or with its decisions off,
is refused.

### Finding clones on this computer

The daemon looks for local clones of the repositories you pick, so you are not
asked for their folders. It makes no network request for this and sends nothing
about your folders anywhere. It reads: the working directories recorded in the
hook event record and in the Claude Code (`~/.claude/projects`, and the same
folder of the other configuration directories baum knows), Codex
(`~/.codex/sessions`, `~/.codex/archived_sessions`, and the same folders of the
other homes baum knows) and Cursor (`state.vscdb`) session stores, without parsing the transcripts; your home directory, walked
breadth first to five levels deep without following symlinks, stopping at
each repository it finds and never entering hidden entries, dependency,
virtual environment or build folders (`node_modules`, `vendor`, `venv`,
`target`, `build`, `dist`, `Pods`, `DerivedData` and the like), or the folders
`Desktop`, `Downloads`, `Pictures`, `Movies`, `Music`, `Public`, `Library`,
`Applications`, `iCloud Drive`, Google Drive, Dropbox or OneDrive (on macOS
nor `Documents`, unless you ask for one scan that includes it; on Windows nor
`AppData`); and, in each candidate clone, the
remote URLs of its git configuration, through `git rev-parse` and `git config`.
A directory an agent worked in is stat'ed, with its parents up to your home
directory, and git runs in the clone containing it, wherever it is (git may
also follow an `include.path` in that clone's configuration). To register what
it found it runs the registry's local checks, which may resolve an SSH alias
with `ssh -G`; that contacts nothing. On macOS it reads no path whose first
read makes macOS ask you for permission (`~/Desktop`, `~/Downloads`,
`~/Pictures`, `~/Movies`, `~/Music`, anything in `~/Library`, including
`Containers` and `Group Containers`, iCloud Drive and cloud-drive folders,
anything under `/Volumes`, and `~/Documents` except in the single scan you ask
for), not even a directory an agent worked in there. The paths it finds are served
only by `GET /api/clones/found`, behind the daemon's authenticated Host/Origin
boundary, and registered only for repositories you picked.

A clone it finds is registered without asking, and from then on the daemon
runs git there (fetches for reviews, the inbox and fixes) with your
credentials and SSH agent, outside any agent's sandbox. Git runs commands the
clone's own configuration names (`core.sshCommand`, `core.fsmonitor`, URL
rewrites, filters). A sandboxed agent decides where it works, so it could
create a repository whose remote names a repository you picked and whose
configuration runs a program, and its working directory would lead the
daemon there. The common case is mitigated: agent sandboxes let an agent
write in its project and in temporary directories, and a directory an agent
worked in is never registered when it, or its clone's repository, lies in a
temporary directory (`/tmp`, `/var/tmp`, `$TMPDIR` and the system's temporary
directory, macOS's `/private/var/folders`), below an agent `scratchpad`
folder, or in `~/Downloads`. The remaining risk is accepted: an agent that can
write a folder that is not temporary can still plant a repository there that
is registered and later fetched. Asking you to confirm every clone found
would close it, but would make ordinary use worse to protect against an
unusual case. Finding a candidate runs only `git rev-parse` and
`git config --get-regexp` in it, which run nothing its configuration names.

## Reporting a vulnerability

Report suspected vulnerabilities privately, not through a public issue or
pull request:

- Email: **security@usebaum.com**

Include a description of the issue and its impact, steps to reproduce or a
minimal test case, and the affected version or commit. We acknowledge reports
as quickly as possible and coordinate a fix and disclosure timeline with you.

Do not include real secret values or unredacted session text in a report. Use
synthetic values that reproduce the shape of the problem. If a real secret was
exposed while triggering the issue, treat it as compromised and rotate it.

## Scope

This policy covers the baum repository. It does not cover the model
providers, GitHub, or the coding-agent tools whose sessions baum reads.

## The baum daemon

The local application (the desktop app) is served
by the baum daemon, one process per user that keeps running in the background
after whatever started it exits. The desktop app starts it with `baum daemon
ensure`; an agent or Git hook that runs baum starts it when it is not
running.

What it runs while it is up:

- the local application's server on `127.0.0.1` (described below) and a
  control socket only your user can open;
- every minute or so, free GitHub reads with your token for registered clones:
  open pull requests, the authenticated user and teams, author permissions,
  and, when GitHub reports its pull request changed or it has not been read
  for 15 minutes (an hour once closed), the latest saved review's checks,
  conversation and head, with conditional requests and backoff (see "What
  baum sends where");
- once when it starts, the signed price list download;
- the record of the events agent and Git hooks report (above), from which it
  also tracks the agent sessions in progress for the app (below);
- when a coding-agent session ends or Git pushes in a registered clone, the
  same free GitHub reads for the pull requests of that branch (for a push,
  about 10 seconds later and once more 30 seconds after that if GitHub does
  not show the pushed head yet), at most once per pull request every 30
  seconds, and, when a head moved, one poll of the repository's open pull
  requests for automatic generation, at most every 30 seconds. They are
  skipped, not postponed, while GitHub's rate limit is exhausted; the periodic
  reads resume after the reset. Events from directories outside
  registered clones, and pushes to other repositories, cause none;
- generation jobs, the only thing that spends money: each runs the configured
  backend under your account, within the configured budgets. Some you started in the app; the others it
  starts by itself, unattended (below). Prices only report what usage would
  cost at API prices: baum shows none before a run, and no price limits,
  refuses or decides a run. Each run's time limit bounds it: 10 minutes for
  a review and 30 for a fix or any other agent run by default, set in
  Settings; call and token budgets apply only when the config file sets them.

Automatic generation is on by default and runs without anyone watching. It
starts a job when a pull request in a registered repository that you authored,
or whose review is requested from you or one of your teams, gets a new head, a
new review request or is marked ready for review after automatic generation
was enabled for that repository; pull requests already open then, and drafts,
are left alone. A push or a session ending only makes the daemon look sooner:
it decides by the same rules, and starts nothing the periodic check would not. It never starts
one for a pull request from a fork or by an author without known write access:
those run the agent on someone else's content and always need your
confirmation in the app. Each automatic job is prepared exactly like a
confirmed one (same backend, model, budgets and recheck of the head before any
model call, which also stops it if the pull request was converted to a draft
while it waited). A model without a known price runs automatically like any
other. What it can use: one review run, within its time limit (and any call
and token budgets the config file sets), for each new head (twice when a daemon stop or restart interrupts the first
run, which is retried once; failed or cancelled runs are not retried). There
is no daily or dollar limit: baum cannot price what a subscription bills, so
it sets none; turn automatic generation off for a repository to stop it.
cursor-agent, which Cursor bills through your subscription and baum cannot
price, needs no acknowledgement. Turning the global switch off (in Repositories
& settings, or pausing automatic generation from the desktop app) stops it in
every repository, including those set to "On"; while it is on, single
repositories can be turned off. Turning off also cancels that scope's automatic jobs that are queued but
not started, and the queue checks the current settings again before starting
each one; only a job already running finishes, within its budget.
The daemon keeps doing this after the app window closes, and the desktop app
can quit without stopping it; only "Quit and stop background reviews" (or
`baum daemon stop`) stops it.

It never contacts a baum server. To stop it, quit the desktop app with "stop
background reviews" or run `baum daemon stop`: running jobs are cancelled and
recorded as interrupted (after at most 20 seconds), and nothing runs until
something starts it again. Hooks do not start a daemon you stopped this way;
the desktop app or `baum daemon ensure` does. `baum daemon status` and `baum
internal doctor` show whether it runs, its version, process id and uptime. When a
newer baum finds an older daemon running jobs, it lets them finish: the
daemon then starts the newer `baum` executable that asked and exits. A hook
of a newer baum only asks an idle older daemon to stop; the next hook starts
the newer version. Only a daemon speaking an incompatible protocol is stopped
at once.

The daemon runs that executable outside any sandbox, with your login shell's
environment and SSH agent, while the process that named it may be an agent
hook run from a sandboxed command that can write files in a workspace. So it
accepts the executable only from a client of a newer version than its own,
and only when, after resolving symbolic links, the file is the daemon's own
executable (an upgrade in place) or the `baum` on its login shell's `PATH`
(which the desktop app's link makes its bundled baum);
it must be a regular, executable file owned by you or root and not group- or
world-writable, in directories up to `/` that are all the same (none may be
world-writable, not even sticky like `/tmp`). On macOS a directory owned by
root or you that only the admin group may also write (group `admin`, as
`/Applications` and Intel Homebrew's `/usr/local/bin` are) passes too: admin
users can become root anyway, so it lets in no one a root-owned file would
keep out. The staff group, to which every local user belongs, does not count,
and the executable itself must still be writable by its owner only. It checks
again just before starting it. A refused executable is logged and the daemon
keeps running; a newer baum then replaces it the next time it finds the
daemon idle. On
Windows, where owners and mode bits do not describe access, only the first
two rules apply.

Its state directory, `$XDG_STATE_HOME/baum/daemon` (default
`~/.local/state/baum/daemon`), is mode 0700 and holds the lock, `daemon.json`
(version, process id, start time, app address), the app credential and the
logs, which are rotated at 1 MiB with two older files kept. The logs hold
events, addresses and paths, never tokens, credentials or environment values.
It also holds the record of hook events (`events/events.jsonl`, 0600, rotated
at 1 MiB with two older files kept), the hook event spool, and `hooks.log`,
where hooks note what they spooled, dropped or started, never an event's
contents; `stopped-by-user`, while you have stopped the daemon; and
`started.json`, the time a hook last started one, which keeps hooks from
starting another for three minutes unless it served. The daemon's lock file
(`daemon.lock.sqlite`), which only a daemon creates, is also what lets agent
and Git hooks run baum, locally, for a user who never logged in; the
directory alone does not.

The control socket, `$XDG_RUNTIME_DIR/baum/daemon.sock` or `daemon.sock` in
the state directory (or `/tmp/baum-<uid>/daemon.sock` when that path is too
long for a socket, whatever `TMPDIR` is), is mode 0600 in a 0700 directory, so
other users cannot connect. The daemon refuses a socket directory that is not
private or not yours, and clients refuse to connect through one, so another
user who creates `/tmp/baum-<uid>` first cannot receive your requests; Windows uses the same `AF_UNIX` socket, protected by the state
directory's current-user DACL. Any process running as you can connect, ask
for the daemon's status and stop it. It can also send hook events, which the
daemon records; a session end or push naming a registered clone makes it run
the bounded GitHub reads above, never a job by itself.

The daemon does not use the environment of whatever started it. It runs your
login shell (`$SHELL -i -l -c`, without a terminal, for at most 10 seconds;
`$SHELL -l -c` within the same time if that prints nothing, as when a startup
file runs `exec tmux`) with only session variables and reads the environment
your startup files set (they can test `BAUM_RESOLVING_ENVIRONMENT` to skip
work that needs a terminal),
then keeps only `PATH`, locale, proxy and certificate settings, the SSH agent
socket and ssh command git uses, the desktop session bus and display,
GitHub tokens (`GITHUB_TOKEN`, `GH_TOKEN`, `GH_ENTERPRISE_TOKEN`,
`GITHUB_ENTERPRISE_TOKEN`) and gh settings, `ANTHROPIC_API_KEY`,
`CLAUDE_CONFIG_DIR`, `CODEX_HOME` and `BAUM_*`. A token set only in an agent's
environment is therefore not used. Windows has no login shell: there the
daemon keeps those variables from the starting environment, except `PATH`,
which it reads from the registry (the system `Path`, then yours). Your home, XDG and temporary directories
come only from the starting environment, never from the shell, because they
decide which state directory, lock and job journal the daemon uses. The SSH
agent socket is the starting environment's, else the shell's; it is used only
to fetch a missing pull request commit from your clone's remote, as `baum internal
generate` would, never given to an agent backend, and dropped when it no longer
exists. The daemon's fetches never prompt: git's terminal prompts and SSH's
askpass are off, plain `ssh` runs in batch mode, and a fetch has a timeout. An
SSH key the agent does not hold (one that needs a passphrase) or an HTTPS
password nobody can type fails the run with a message saying how to make the
credential available; an unknown host key fails it with git's error. An ssh command you configured runs as you wrote it.

## Authenticated local application

The daemon's application server accepts only the bound `127.0.0.1:port` Host. Its bootstrap has no
local data; every API requires the random private launch capability or an
HttpOnly SameSite=Strict cookie obtained by exchanging that capability, except
`POST /api/mcp`, which takes only an agent's MCP token (see
[MCP server for agents](#mcp-server-for-agents)). The
browser receives the capability in a URL fragment, removes it from history,
and sends it in the same-origin JSON exchange body. Browsers send cookies to
every port of a host, so the cookie is scoped to a second random secret path,
`/s/<namespace>/`, returned only by the exchange; another service on a
loopback port never receives it, and the cookie authorizes nothing outside
that path. All mutations require the
exact local Origin and JSON content type, including requests using Bearer
authentication; `POST /api/mcp` requires the JSON content type but no Origin,
and refuses a foreign one (see [MCP server for agents](#mcp-server-for-agents)).
No route enables CORS. Right after the Host check, before any route, the
server refuses with 403 every request whose `Sec-Fetch-Site` header is
`cross-site` or `same-site`, except a top-level document navigation (`GET` or
`HEAD` with `Sec-Fetch-Mode: navigate` and `Sec-Fetch-Dest: document`). Browsers
set these headers on every request and a page cannot change them, so a page
from another origin, a sandboxed frame or a service on another loopback port
cannot make the daemon answer with fetch, XHR, an event stream, an image,
script or other subresource, a frame navigation or a form post, even where the
browser would attach the session cookie to a `GET`. A top-level navigation
passes so that a link or the desktop shell can open the app from elsewhere; it
needs the launch secret and cannot read the response. The header is absent from
agent CLIs calling the MCP endpoint, the hook command, the desktop shell's own
client, health checks and test clients, and those pass; the browser app's own
requests are `same-origin` and a typed launch URL is `none`, and those pass too.
The page's Content-Security-Policy names `frame-src 'self'` explicitly. The
one document route outside the namespace and without the cookie is a custom
tile's frame, `GET /tile-frames/<digest>`, served only to a same-origin frame
navigation (see [Custom tiles](#custom-tiles)); the app's own frames are
same-origin navigations, so the check above lets them through. The capability
is the daemon's app
credential: it is written to `credential.json` (mode 0600) in the daemon's
state directory, where the desktop app reads it, stays valid for repeated
exchanges while that daemon runs, and is replaced when a new daemon starts.
Keep that file and the launch URL in it private. Processes running as your
user can read that file and access the API.

The state directory must be mode 0700 and owned by you on Unix; Windows uses a
protected current-user DACL. Clients never send the capability anywhere: they
read it from the file and open it on the loopback address it names. SQLite OS
locking keeps one daemon per user and releases the lock after process exit.

`GET /api/live-sessions` and the `live` event on the events stream show the
agent sessions in progress, tracked from hook events, behind the same
authentication, Origin and Host checks, and are limited to `GET`. A session is
tracked only when its working directory is inside a registered clone, and the
app receives only: the agent, the session id, the state (running, idle or
ended), when it started and was last active, the kind of the last event
(start, prompt, tool, wait, stop or end), why and since when it waits on you
(a permission, an answer, or a finished turn), and the repository, branch and
pull request number it links to. The working directory, the transcript path, and
anything of a prompt or a tool, which hook events never carry, stay out of it,
and the `live` event carries no data at all: the app reads the list. At most
200 sessions are held in memory, rebuilt from the local record of hook events
when the daemon starts; a session silent for two hours is dropped, and an
ended one is kept for a day, its identifiers and times also in
`live-sessions.json` beside the record. Nothing of it goes to a baum server.
A finished session's content is still read from the session files on disk,
through the redacted transcript routes below.

`GET /api/decisions` serves the decisions decision capture stored, behind the
same checks and limited to `GET`: for a session, or the sessions of a pull
request, each decision's id, title, summary, category, who decided, rationale,
rejected alternative, edited file names, date and whether it is superseded or
provisional, all derived from redacted transcripts and masked again after the
model replied. Each session comes with its agent and id, its capture state
(coverage, skip reason, when it last changed, whether a call is in flight and
why capture is off), and for a pull request whether it is in progress and, for
a session the saved review links, its title as the PR page's sessions list
shows it, and when the saved review the provisional marks are relative to was
generated. Evidence quotes, turns, branches and repositories stay in the
store.
The `decisions` event carries no data. Its settings change only through the authenticated
`PUT /api/settings/decisions` and repository settings routes.

One line of text is the exception: a Claude Code notification's `message`,
the sentence Claude Code shows you when it asks for something ("Claude needs
your permission to use Bash"). Only the `notification` hook's event keeps it,
with its `notification_type`, cut to 512 bytes in the local record of hook
events (the record keeps it as long as it keeps the event). The daemon serves
it as the session's `waiting_message`: format characters dropped, control
characters made spaces, masked as transcripts are (secrets and e-mail
addresses, your home directory as `~`) and cut to 160 characters, and only
while the session waits. The desktop app shows it as the text of the
"needs your permission" or "waiting for your answer" notification, the
`notice` event that names the session (agent, id, repository, branch, pull
request number) and carries the same masked line. A permission request's
event keeps nothing of the command it asks about (what the app is shown of
it is held in memory only, below). Nothing of it goes to a baum
server, and an ended session's saved state (`live-sessions.json`) holds none
of it.

The list also gives each session a `title`, one line read from your own
agent's files on this computer, as the transcript routes below read them: the
title Claude Code shows for it (the one you set, else the one it generated),
Codex's thread name (`~/.codex/session_index.jsonl`), or Cursor's conversation
name; without one, the session's first prompt. The files are read only inside
the agent's own store under your home directory, with the same path checks as
a transcript, and for a session whose hooks named no transcript (such as an
ended one after a restart), found there by its session id. The title is
cleaned, masked and cut as the waiting message is (120 characters), held in
memory only while the session is listed, never written to
`live-sessions.json`, and never sent to a baum server.

Once an agent has compacted a session, the same files give its summary: the
summary Claude Code writes into the transcript when it compacts, or a Codex
compaction's when Codex kept it as text (one done by OpenAI's server is
encrypted and gives none). The list carries its first section, the request
it records, as one masked line of at most 240 characters; the transcript
route sends the whole summary, masked and cut to 32 KiB, with the transcript.
Both are held as the title is.

The Sessions page opens a listed session's transcript through
`GET /api/live-sessions/{agent}/{id}/transcript`, behind the same
authentication, Origin and Host checks, `GET` only. The daemon reads, for it,
only the transcript of a session it is tracking from its hook events: the
request names the session, never a file. The file is the transcript path the
session's own hooks reported (Claude Code and Codex send one), and it is read
only when, with its symbolic links resolved, it is a regular file inside that
agent's own session folder, the one baum already reads sessions from:
`~/.claude/projects` for Claude Code, or the `projects` folder of another
[Claude Code configuration directory](#claude-code-configuration-directories)
baum knows, and `~/.codex/sessions` for Codex, or the `sessions` folder of
another [Codex home](#codex-homes) baum knows. A path elsewhere, a link out of the folder, a directory or a file of
another kind is not read, and the app is told only that the transcript is not
available. A Codex session whose hooks named no path is looked up by id in
those folders; a Cursor session is read from Cursor's own conversation database
by its id. The transcript is redacted exactly as the PR page's linked sessions
(the same code): secrets and e-mail addresses masked, your home directory
written as `~`, nothing else removed. The path itself never reaches the app.
At most 8 transcripts are held in memory, and only while their session is
listed.

The Timeline opens the session of a decision through
`GET /api/timeline/sessions/{agent}:{id}/transcript`, behind the same
checks, for a session decision capture recorded in a repository the Timeline
shows, also once the session ended and is no longer listed. It reads the
transcript path capture stored for that session (the one its hooks reported),
under the same folder rules, with the same redaction; the path never reaches
the app. Such transcripts count towards the same 8 held, the least recently
opened making room. A decision's detail also shows the prompts its decisions
cite, read from that transcript and redacted as decision capture reads it
(secrets and identities masked, the harness's wrappers stripped), and none
once the file is gone.

The desktop app can answer what your agents ask you, and send a running
session a message. Approving from the app means that anything that can reach
the daemon's authenticated local API (the app, or a process running as you
that reads its credential file) can approve commands, file edits and other
tool calls for your Claude Code sessions, and for Codex sessions once you turn
on Codex approvals in the app (`codex_approvals`, off by default).
`GET /api/agent-requests`, `POST /api/agent-requests/{id}/answer`,
`PUT /api/agent-requests/settings`, `POST /api/agent-messages` and
`DELETE /api/agent-messages/{agent}/{id}` sit behind the same authentication,
Origin and Host checks as every application route. How it works:

- Claude Code's `PermissionRequest` hook (user-wide plugin 1.2.0) and Codex's
  hold their agent while the daemon waits for your answer, over the control
  socket only you can reach. Claude Code keeps its own dialog in the terminal
  at the same time, and the first answer wins; the hook stops waiting when
  the same tool call finishes or fails, the session's turn ends or a new
  prompt is submitted (a subagent's request waits for that subagent's stop
  instead), and Claude Code kills it after an hour, leaving the dialog as it
  was. A Claude Code session nobody answers in a terminal (`claude -p` and
  the SDK, whose `CLAUDE_CODE_ENTRYPOINT` is not `cli` or whose
  `CLAUDE_CODE_SESSION_ATTENDED` is `0`) is never held. Codex hides its
  prompt while the hook waits, so the daemon answers a Codex request only
  with the setting on, and only for about 25 seconds; then the hook prints
  nothing and Codex asks in its terminal. With no daemon, an older one or
  any failure, the hooks print nothing and the agent asks as it always did.
- The app is shown everything the approval covers: rows for what the tool
  call does (a command and its description; an edit's file, the text it
  replaces and the new text, every edit of a MultiEdit with their count; a
  file's new content; a URL; a subagent's description, type, model and
  prompt), a row for each switch that widens it when it is on (a command
  run outside the sandbox or in the background, an edit of every
  occurrence), then every other member of the tool's input that is not
  empty, as compact JSON (all of it for a tool baum has no rows for).
  Claude Code's own suggestions for "always allow" are listed, and only the
  one you pick is applied; a question shows its options. Every string is
  cleaned and masked as transcripts are (secrets and e-mail addresses
  masked, your home directory as `~`), so what you approve can differ from
  the real input only where a secret was masked. Values and suggestions are
  shown whole, however long (a long one scrolls in place), because approving
  applies to the whole input. A request too large to reach the app whole
  (over the 56 KiB a request may take once encoded) is not shown, and the
  agent asks in its terminal; so is one whose tool name would be cut. A
  question's text may be cut, since an answer is only the option's label.
  When masking fails, or a question's answers could not be told apart, the
  request is not shown either. The hook keeps the agent's own payload and builds
  the answer from it; the daemon never hands tool input back to an agent.
- To notice an answer given in the terminal, the `post-tool-use` and
  `post-tool-use-failure` hook events carry a SHA-256 digest of the tool's
  name and input (`tool_digest`), never the input, and the subagent's
  `agent_id` when the tool ran in one.
- A message you queue is held for its session until that session's next
  Stop hook, which tells the agent it came from you through the baum app.
  One message per session; a new one replaces it, and a new prompt or the
  session's end drops one queued before it.
- Pending requests and messages are held in the daemon's memory only: never
  written to disk, logged or sent to a baum server, and gone when the daemon
  stops. Only the Codex setting is saved, in the `state/agent-requests` document in `app.sqlite`.

Document selectors must name a registered clone. Linked transcripts use the
shared redaction engine and linked-session allowlist; file patches are
restricted to cited paths. Settings/diagnostics omit provider and GitHub keys.
`GET /api/diagnostics` serves the `baum internal doctor` report behind the same
authentication, Origin and Host checks, runs the same bounded commands as
`baum internal doctor` but never the price-list fetch, and is limited to `GET`.
Stored image APIs return only document-listed, content-checked PNG, JPEG, GIF
or WebP copies. They reject SVG/HTML and never follow document URLs. Images
carry nosniff and a sandboxed, deny-all content security policy. Local successful
versions retain their captured images atomically alongside the document;
removing a registry entry does not delete that history.

The authenticated job API derives repository, head, fork and author permissions
from registered clones and fresh GitHub evidence. A repository picked without a
clone is discovered (names and GitHub metadata only) but never generated:
requests and queued jobs naming no available clone stop with `needs_clone`
before anything runs. A start prepares the job from that evidence and queues
it in the same request, bound to the head, selected model/backend and
budgets; a queued job whose inputs changed does not run. A price list
installed meanwhile is not a changed input: the run is priced with the list
active when it runs. Forks, non-writers and unknown
permissions require a separate single-PR isolation acknowledgement and are
excluded from batch generation. Cancellation reaches the existing backend
process-group cleanup. Job history contains fixed failure reasons and bounded
usage metadata, never arbitrary provider errors, prompts or rejected documents.
The private journal persists queued/running/terminal states; crashed running
entries become interrupted before work resumes. A fix (`"kind": "fix"`, see
[Fixing a pull request](#fixing-a-pull-request)) starts only for a single pull
request a person asked to fix, never in a batch or automatically, and is
refused before anything is queued for a fork, an account that cannot push, a
backend that cannot fix or nothing to fix. A fix
job also keeps the review threads it addressed (file, line, author and the
first comment's headline in plain text) and each one's outcome code with the
agent's cleaned summary. The stored copy of a downloaded
price list is trusted only after its signature verifies again on load. Only
the daemon that owns the per-user lock opens the queue, so there is never a
second one.

The server's bootstrap page exchanges the fragment token and clears it from
history before the embedded local application loads; the application's files
are served only under the authenticated namespace, and every application
request resolves under it. Every application request is same-origin; the app has no hosted baum API dependency. Review content uses the
same sanitizers and safe rendering components as the hosted PR page and the
reference viewer host. Stored document rendering failures remain within the PR pane. The app's
only GitHub writes are the ones listed under "Writes to GitHub": a merge, a
branch update and a re-run you confirm and a fix you start, with that fix's
replies on the review threads it addressed, and resolving a review thread you
chose; it supplies no approvals or other discussion controls.

### Custom tiles

A custom tile is HTML and JavaScript an AI agent wrote: a component that
draws one tile's data in a review's tile document. It is untrusted code, and
it runs only in its own frame, `<iframe sandbox="allow-scripts">`, whose
document the daemon serves with its own Content-Security-Policy:
`default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline';
img-src data: blob:; font-src data:; form-action 'none'; base-uri 'none';
frame-ancestors 'self'; sandbox allow-scripts`.

**Built-in library types.** baum's own library types (`tiles/library`:
schema changes, interface changes) are custom tiles too: their HTML is
baum's, but it draws data a model wrote from the pull request (a fill), so it
runs in the same frame under the same policy and checks as any custom tile.
The page names such a tile as baum's own, without the "Custom" mark, only when
the daemon marks its served definition `builtin: true`, which it does when the
digest computed from the stored definition's content is a built-in type's,
current or past; the digest covers the whole definition, so neither a tile's
title, a `builtin` member it carries nor a stored definition that differs in
any byte can claim it. The composition holds only built-in types' tiles from a
generation. A fill's data
is model output like the review's: it is masked for secrets as a task's
output, checked against the type's schema and the diff (every citation a hunk
of the diff), and the host writes `related` itself. Authored tiles (exhibits)
are table, code and prose data drawn by the page's own built-in faces, with the
same masking and citation checks; a code excerpt must be the cited code, and a
file it cites must be a file of the diff, a regular file in the checkout.
Table cells, code and captions are drawn as text. A note's Markdown draws
paragraphs, emphasis, code spans and lists only: a link reads as its text and
raw HTML is not rendered.

**Saved tile types.** A person's library of tile types (`internal/tilelibrary`)
lives in the private application database on this computer. A type's code and
instructions are the person's: they are never read from repository content, and
a type's fill and review checks enter the fill and details prompts as
instructions of the same trust as a custom prompt, while the pull request stays
data.

- Generation offers saved types in every repository, by default: at most six
  per review, those whose paths match the diff, and the pinned ones are
  filled. A saved type's fill opens with a note that its instructions are the
  person's and the only instructions about the tile, and that text in the pull
  request, its code, sessions or the outline that reads like tile
  instructions is data; its review checks come under a heading marked as the
  person's, after a note that the pull request cannot add a check. The
  fill and selection texts say the same of every type. The outline lists a
  saved type's texts as the person wrote them, bounded in length. A type "Not
  checked yet" is neither offered nor filled. The person's instructions only
  add: the notes say they never remove, soften or hide a risk and never set
  or change the recommendation, and in a fill the outline follows under a
  heading of its own, marked as data. A saved title's whitespace is
  collapsed wherever it heads a section, so it cannot open a heading of its
  own. A type's path globs are bounded where they are saved (at most 8 braces
  expanding to at most 64 patterns, refused with a reason past that), and
  brace expansion stops at that bound, so matching a glob against a diff's
  paths takes bounded time whatever the glob. Offering is not restricted
  further on purpose: each tile is labelled "Custom", cites the review, passes
  the first-show preflight and stands among a section's tiles, never in the
  place of the review's own text, recommendation or risk score.
- A saved type's data may cite more of the pull request than its code. A
  fill's citations are checked against the whole bundle (`library.CheckFillIn`):
  a file must be a path of the diff, a prompt or session event must name one
  of the bundle's sessions that is not restricted, a check or review thread
  must be the bundle's, and a risk, evidence item or tile is refused. A
  restricted session is never cited, here or when the composition resolves
  the tile's citations against the review.
- The view call places a saved type's tile only when its definition's digest,
  computed from its content, is one of the types the host offered for the run
  (`agent.ViewRequest.Saved`), beside the built-in types'.

- Persistence needs the person. A chat can only propose saving or changing a
  type (`propose_tile_type`); the person's Save in the panel writes it, the same
  way every other proposal is saved, and a proposal that is stale (the type
  changed since it was read) saves nothing. A saved type is checked as a
  built-in type is (`library.Check`, which includes the kit's lint) and its name
  may not shadow a built-in tile type, a built-in library type or a tile ID
  prefix.
- Samples are self-contained and checked not to copy the open pull request.
  A proposal needs a render check that passed on a sample whose hunks and
  items are its own, only such samples are saved, and a sample whose citations
  do not resolve to its own hunks and items is refused. On a pull request's
  page the proposal is also refused when a sample's hunks share three or more
  distinct non-trivial lines (not blank, brackets, imports or common
  boilerplate) with that pull request's diff, and when its diff cannot be
  read to check. This is a heuristic on lines, not proof that a sample holds
  none of a pull request's data: the person sees every sample, in the
  proposal's Before and After, before they press Save, and nothing is saved
  until they do.
- "Built in" is decided by digest only. A saved type is never marked built in
  unless its content is byte for byte a built-in type's, which then is baum's
  own code; nothing a proposal, a name or a stored row carries can claim it.
- An agent reads a type's source only through `tile_type`, a chat tool on the
  pull request page and Settings' Tile library section; an action never sees
  it, and the tile routes still serve definitions without their HTML.
- The preview route `GET /api/tile-types/{name}/preview` registers the type's
  digest in the frame registry like the tile routes, and has the session
  namespace's cookie or the launch token like every other `/api` route; a
  request that carries a definition registers nothing, with one exception:
  the import check below.
- Importing a tile type file (`POST /api/tile-type-files/check`, then `POST
  /api/tile-type-files`, from Settings › Tile library) treats the file as
  untrusted. It is refused past 3 MiB, nested deeper than 64 levels or holding
  more than 200,000 JSON values (checked before it is decoded), holding a NUL or not valid UTF-8,
  with a member a tile type file does not have (at any level, a sample's
  included) or a schema other than
  `baum.tile-type/1`; its definition must pass the definition checks (shape,
  UTF-8, dialect, a supported kit) and the kit's lint without an error, its
  samples must validate and cite only their own hunks and items, and its name
  must pass the name rules (a name already in the library is renamed with a
  numeric suffix). Its digest is computed and whether it is built in follows
  from that digest; a digest or `builtin` in the file is never read. Its
  origin is "Imported". The person sees the title, use-when line, the fill
  instructions it would give the review agent, its paths and a preview of a
  sample before anything is saved; to draw that preview, the check registers
  the file's definition in the frame registry once it has passed every check.
  This is the one registration of something a request carries: it is the
  person's own file pick, sent by the authenticated app, which already holds
  that HTML, and the frame is the same sandbox as every custom tile's.
  Bidirectional controls, zero-width characters and Unicode tag characters
  are removed from the title, use-when line, fill instructions, glance and
  review checks on import and on every save (the stored text and its digest
  never hold them), and a path with a control or invisible character is
  refused; the review checks are at most 1,500 characters. The dialog shows
  the fill instructions, glance and review checks, labelled as instructions
  given to the review agent. An import then runs the render check
  (`tilechecks.Checker.ImportCheck`) on the file's first three samples in the
  app's sandboxed frames: a type that fails or hangs is refused. When no
  runner takes the check (the app is closed or cannot draw tiles) the type is
  saved marked "Not checked yet", which is all that mark means: it is saved
  and previewable, but no render check ran on it. Its first preview with a
  runner available runs the check in the background once per daemon run and
  marks it checked on a pass; a failed deferred check leaves the mark. The
  check draws the type's HTML in the same sandbox as any custom tile; it does
  not read the type's content for instructions.
  An exported file holds the type's synthetic samples and no review content.
- A review keeps the definition it was made with: its library tiles embed
  the full definition, so editing or deleting a saved type changes no saved
  review.

**What a tile's frame cannot reach.** The sandbox gives the frame an opaque
origin, so it cannot read the app's page, its cookies or storage, or the
desktop app's bridge, and it cannot open windows, submit forms or navigate
the app's page. The frame's policy allows no request at all: no `fetch`,
images, fonts, styles or frames from anywhere, the daemon included;
everything the kit needs is inlined into the document. A request the frame
tries to send to the daemon anyway would carry no cookie, and the frame
never learns the secret `/s/<namespace>/` path the app's API lives under.

A frame can still navigate itself (`location.href = …`, a meta refresh): its
own policy does not govern that. The control for it is the page's
`frame-src`, which today falls back to the page's `default-src 'self'`, so a
frame can only be navigated to the daemon's own origin, where every route
but `/tile-frames/` needs the cookie and that route refuses a navigation a
frame starts (it is `cross-site`). The frame also talks to the page by
`postMessage`; the frame protocol (see "Custom tile kit" in
`ARCHITECTURE.md`) has no message that approves, merges, comments, changes a
setting or reads anything, and the page must treat every message from a
frame as untrusted.

**How its document is served.** The frame document is
`GET /tile-frames/<digest>`, where the digest is the 64 lowercase hex digits
of the tile type's definition digest. It is the one document route outside
the session namespace and without the cookie, so that a tile never sees the
namespace and its document request carries no credential. In its place it is
served only when all of these hold, and otherwise answers the same plain 404,
which never says whether a digest exists:

- the Host check passes, as for every route;
- the method is `GET` or `HEAD`;
- the request is a frame navigation started by a document of the daemon's own
  origin: `Sec-Fetch-Dest: iframe`, `Sec-Fetch-Mode: navigate` and
  `Sec-Fetch-Site: same-origin`, all three present. A client that sends no
  fetch metadata, a script's fetch, an `<object>` or `<embed>`, an address
  typed into the address bar (`none`), another loopback service (`same-site`),
  any web page and a tile's own frame (opaque, so `cross-site`) are refused;
- the path is exactly `/tile-frames/` and the digits, with no encoding, extra
  segment or trailing slash; the query is ignored;
- the daemon itself handed that type to the authenticated app: the tile
  routes register the types of the custom tiles they serve, the tile type
  preview route the type it draws (see "Saved tile types"), and a
  render-check candidate is registered when the daemon hands it to the app's
  check runner; nothing else a request carries is ever registered (the one
  exception, an imported file the person picked, is registered after the
  import checks, see "Saved tile types"), so the route serves only HTML the
  app already received or sent. At most 32 MiB of documents are kept, the
  least recently
  used going first; reading the tile route again registers its types again.

The answer is `no-store`, sets no cookie, ignores `Range` and carries
`X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and
`Cross-Origin-Resource-Policy: same-origin`.

**What the page checks.** The app's host (see "The app's host" under
"Custom tile kit" in `ARCHITECTURE.md`) treats every message from a frame as
untrusted:

- The frame is `<iframe sandbox="allow-scripts">` with no other flag, no
  `srcdoc`, `referrerpolicy="no-referrer"` and an empty `allow`; so it cannot
  open windows or dialogs, submit forms, navigate the app's page or reach
  storage, and its own policy lets it load nothing.
- **Where frames mount.** In a browser, always. In the desktop app only when
  the shell declares, by a script it runs in the page before the page's own,
  `window.__BAUM_DESKTOP__ = { tileFrames: true }`: a shell says so once it
  refuses every navigation that is not to the daemon's own pages (only a
  request for a new window opens your browser) and once its commands are
  checked to be out of a sandboxed frame's reach. A shell without the
  declaration (an older one, or the Windows shell until its own check is
  done) mounts no frame, and its custom tiles say they aren't available in
  this version of the desktop app yet. A frame cannot set the declaration:
  it cannot write the page's globals.
- A message counts only from a frame's own window (`event.source`) and with
  the opaque origin `"null"`, so no frame can speak for another tile or for
  the page; a frame's kit likewise hears only the page, so a frame posting
  to its neighbours as if it were the page is ignored. Messages are counted
  before their data is read: past 30 in a second the rest of that second's
  are dropped unread, and a frame over that rate in more than three of the
  last ten seconds, or past 300 in one second, is unmounted.
- Every field is read with strict type checks; a string longer than 1024
  UTF-16 code units is dropped, an error's message is cut to 500, and what a
  tile writes is shown only as text.
- A detail's height, whether from `ready` or `resize`, is held to 120 to
  20,000px, applied at most once an animation frame, and stops following a
  height that keeps growing by one amount or keeps changing direction;
  `ready` is taken once for each `init` or `update` the page sent, any other
  is a resize under the same rules. A glance's box is fixed.
- A frame can ask the page only to jump to an item its own data cites,
  which the page resolved from the review it already shows (the frame names
  it by key, never by address), to open the session panel's question about
  the tile, or to act on a whitelisted key; and only from a detail whose
  keyboard the reader gave it (below). A jump or a question also needs the
  browser to report a user activation at that moment (a browser that cannot
  say is taken as no), and the page honours at most one a second.
- **The keyboard.** A frame takes no keys unless the reader gives them: a
  glance never (it lies under its card's link with `tabindex=-1`); a detail
  when a Tab lands on its frame, or when the pointer is over it, the browser
  reports a user activation and the page saw no key press of its own in
  the half second before (the reader typing elsewhere with the pointer
  resting on a tile is not a click). The page cannot see clicks inside a
  frame, so this is how it tells one apart. Focus a frame takes otherwise is
  given back to the page's element that had it, and a frame that takes it a
  fourth time is unmounted.
- **Forwarded keys.** The whitelist is the page's reading keys only: Escape
  and `u` (back to the summary), `j` and `k` (next and previous),
  ArrowLeft and ArrowRight (previous and next tile), `[` and `]` (the
  sidebars) and `?` (the shortcuts sheet). The page dispatches a `keydown` on
  the frame's element carrying the key's name and whether Shift was held,
  nothing else (no other modifier, code or text), and only while the reader
  has given the frame the keyboard. No key that acts outward or changes
  anything is on it: not Enter or Space, `m` (merge), `v` (send a review),
  `c` (comment), `a` (ask), `f` (fix), `t`, `g`, or any key with Ctrl, Alt
  or Meta; the frame's kit forwards none of those, and the page drops any
  other key a frame names.
- A frame that does not start, does not draw in time, says hello twice,
  floods or keeps taking the keyboard is unmounted and its tile says it
  couldn't load, in the host's own words; what the tile reported is shown
  apart, quoted, behind Details. A frame that navigates itself after it
  drew is unmounted where the engine reports the navigation as a load (both
  Chromium and WebKit do for the navigations the suite tries); one that
  navigates before it drew never draws and is unmounted when its time runs
  out. That failure is about that frame only: nothing a frame sends marks
  its tile type, or any other tile, as broken. Only the daemon's own render
  check or the authenticated app may block a type.
- What the tile draws is set apart from the page: its kicker reads
  "Custom ·" and the type's title, whatever the title (one that names a
  built-in tile too), in the card, the detail and the rail, and its detail
  sits inside a faint edge under a "Custom tile" caption that the page
  draws.

The browser suite (`app/custom-tile-tests/tile-frames.spec.ts`) runs hostile
tiles on the real frame route in Chromium, and in WebKit with
`just local_app::e2e-webkit`: every way out it tries (reading the page,
cookies, storage, caches and the shell's message handlers, top and parent
navigation, `window.open`, dialogs, form submission, nested frames, fetch,
XHR, WebSocket, EventSource, beacons, images, fonts, stylesheets and CSS
`url()` to the daemon and to another origin, workers, spoofed and oversized
messages, a second hello, floods, taking the keyboard while the reader
types elsewhere, navigating itself before and after it drew) is refused,
the page stays as it was and a listening server on another port receives
nothing. The desktop shells' own webviews are checked by hand
(`desktop/tests/tile-frames-manual.md`).

**What a tile is given.** The frame protocol gives a tile its own data, the
face and size it draws, the app's theme and language, and the items of the
review its data cites. Nothing else: not the review, the repository, other
tiles, or code it does not cite. Anything given to a tile is visible to
whoever wrote it. The tile routes send each custom tile's definition without
its HTML (its length in bytes instead), to the app and to every read of them
by the chat's agent.

**Residual risks.**

- A tile's author can learn what the tile is given, at low bandwidth: the
  browser may resolve or connect to hosts a tile names in its markup (DNS
  prefetch and preconnect) before any policy applies, and where the engine
  offers WebRTC (Chromium, and WebKit outside WebKitGTK; macOS is
  unverified), a peer connection can reach a server the policy does not
  govern. baum ships no script that tries to block these inside the frame: a
  fresh realm gets around such a script. It minimises what a tile receives
  instead.
- **A tile in an endless loop freezes the app's window until the app is
  closed**, where the engine runs the frame on the page's thread, and
  nothing in the page can stop it; the next time the review is opened it
  freezes again. In the browser suite a 1.5-second loop held the page for
  its length in WebKit every time, and in Chromium in some runs and not
  others. The host unmounts a frame that does not draw in time, but cannot
  interrupt one that holds the thread. Where the desktop shell provides the
  render check a window with its own web process (currently Linux), that
  window takes the freeze instead (see "The render check" below). Elsewhere
  (a browser, a shell without one) the render check draws in the app's own
  page, so a
  candidate that loops freezes it, and the daemon then answers `hang` and
  refuses that type until it restarts.
- A frame can navigate itself to a page of the daemon's own origin: the
  request reaches the daemon without the cookie, the namespace or any data,
  and the daemon's pages refuse to be framed (`frame-ancestors 'none'`); the
  host then unmounts the frame.
- In a desktop shell that hands navigations to external addresses to your
  browser, a frame navigating itself could open a page of its choosing there,
  with what the tile was given in its address, and the page would not see it
  happen. That is why the page mounts frames in a desktop shell only when
  the shell declares it refuses such navigations (above).
- A local process that knows a tile type's digest can fetch that type's
  source by sending the three `Sec-Fetch-*` headers itself; the checks are
  not authentication. It never gets a tile's data, which is not in the
  document, and a process running as you can read the app's credential
  anyway.
- The author today is your own agent, writing for you; a tile shared by or
  imported from someone else trusts its author with what it is given.

Tile code is never read from repository content: a pull request cannot
supply a tile.


**The render check.** Before a tile made in the app is shown, the daemon
checks its type (`internal/tilechecks`) and the app's check runner draws it
in the same sandboxed frames, under the same policy, with the type's own
samples and the data it will show. It is a quality gate for honest
mistakes, not a security control: a tile can fake its `ready`, swallow its
errors or behave differently later. What it does protect against is a
freeze where it would hurt:

- Where the desktop shell provides a check window (currently Linux), the
  runner draws in that hidden window with its own web process. While it
  draws, the runner tells the daemon it is alive every 2 seconds and as
  frames finish; a candidate that never finishes drawing freezes it, so
  after 10 s without a sign of life (or past a limit scaled by the frames
  the check draws) the daemon answers `hang`, marks the type's digest hung
  (the frame route then refuses it until the daemon restarts) and tells the
  main page, which has the shell close the window, ending its process and
  the loop. The main window is not affected.
- Where that window exists, the first time the app shows a custom tile that is not of a
  built-in type, with data it has not drawn before, it first draws that
  pair in the check window (the first-show preflight). A pair that hangs
  stays down, with "This tile stopped responding when it was checked."; a
  pair that drew, or failed in a way the frame's own failure faces show,
  is shown. Verdicts are kept per pair (the type's digest and the data's
  SHA-256) in `app.sqlite`, the newest 10,000.
- The runner's routes (`/api/tile-checks/next`, `/{id}/result`,
  `/preflight`) sit behind the app server's Host, session, Origin and JSON
  checks, and refuse a browser request that is not same-origin: claiming a
  job registers its frame document, so only the app may claim. A job
  carries the candidate's served definition (no HTML: the frame route
  serves that by digest), its samples and their citations' payloads, and
  never more than the page itself would give the frame. The `tile_check`
  event names a check's ID and state only.
- Only the daemon marks a digest hung, from its own timer: no message from a
  frame or a runner can. A runner whose page goes away mid-check reports the
  job abandoned, which ends it unavailable without blocking the type, and a
  long poll whose client left puts its job back.

### MCP server for agents

An agent CLI reaches the server over HTTP at the daemon's loopback
`/api/mcp`; no other process sits between them. The CLI gets its token only
in the `BAUM_MCP_TOKEN` environment variable and sends it as
`Authorization: Bearer`. Its MCP configuration names the variable, never the
token: Claude Code's header is `Bearer ${BAUM_MCP_TOKEN}`, which it expands
from its environment, and Codex's `bearer_token_env_var` is
`BAUM_MCP_TOKEN`. So the token is in no argument, which the process list
would show, and in no configuration file. baum sets the variable after the
user's `agent_env`, so that cannot replace or remove it, and accepts only an
`http` URL on a loopback address, so the token goes nowhere else. Only the
local profiles run an MCP server; the hosted and container profiles refuse a
request that asks for one. Claude Code runs with `--strict-mcp-config` and
Codex with `--ignore-user-config`, so baum's server is the only one. No job
sets it yet: quick actions will, and no other run has it.

The session panel's conversations are the exception, by design: each is your
own Claude Code or Codex session, run as you set it up, with your settings,
hooks, instructions and MCP servers, and baum's server added beside yours.
By default it runs in auto mode: Claude Code's `auto` permission mode, whose
classifier approves or blocks each action, and Codex in its `workspace-write`
sandbox with its automatic reviewer answering each request to leave it. You
can choose full access instead (Claude Code's `bypassPermissions` mode and
Codex without approvals or sandbox, what running the agent yourself with every
prompt approved would allow) or workspace access (`acceptEdits` and Codex's
`workspace-write` sandbox with no requests to leave it). It runs in a
directory baum chose: a worktree of the pull request you opened it from (yours, or one baum
adds without changing your other checkouts and without running hooks) or an
empty directory of its own. Each conversation holds a `chat` token of its own,
bound to its session once the agent names it and revoked when you close it or
the daemon stops; through it the agent can propose changes to baum's state,
which change nothing until you save them, like any chat's.

A conversation's environment is the one every local run gets plus what you
need to work as you would in a terminal: your SSH agent (`SSH_AUTH_SOCK`),
`GITHUB_TOKEN`, `GH_*` and your `GIT_*` settings, so the agent can push and
use `gh` with your credentials. Git variables that point at another
repository (`GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`,
`GIT_OBJECT_DIRECTORY`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`, `GIT_COMMON_DIR`,
`GIT_NAMESPACE`, `GIT_PREFIX`) and provider and cloud credentials are still
removed; reviews and fixes keep the narrower environment. Claude
Code still gets `DISABLE_AUTOUPDATER`, `DISABLE_TELEMETRY` and
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`. `BAUM_MCP_TOKEN` is in the
environment of your Claude Code process, so your own hooks and stdio MCP
servers can read it; that gives them no access they lack, since they run as
you. Codex reads it itself, to authenticate to baum's server, and forwards it
to no MCP server.

#### Session panel

Full mode runs your agent with your permissions, as running it yourself with
every prompt approved would. Saving or confirming a proposal in the app is the
boundary for an agent that cooperates with baum, not a sandbox: an agent in
Full mode can read the daemon's launch credential (`credential.json`) or
`app.sqlite` and act on baum directly, as any program you run can, and its
MCP token, which its own hooks and stdio MCP servers can read too, opens only
the MCP route. Auto mode, the default, and Workspace mode narrow that only as
far as the agent's own checks do: in auto mode Claude Code's classifier and
Codex's reviewer, a model that decides what to allow, and in Workspace mode
Claude Code's `acceptEdits`, which refuses commands it would ask about, and
Codex's `workspace-write` sandbox, which limits where its commands write. None
of them is a boundary baum adds. The panel's sessions are listed on the Sessions page
marked as the panel's, without the end, message or focus actions, and their
routes, the message queue and request answers refuse them (409
`panel_session`): the panel runs and stops them. A panel session's permission
requests are never shown in the app; its own permission mode decides them.

The application server also serves `POST /api/mcp`, an MCP server that lets
an agent read what the app shows. It takes only an MCP token, sent as
`Authorization: Bearer`. The launch capability and the session cookie are
refused there, and `/s/<namespace>/api/mcp` is not served, so the cookie a
browser holds cannot reach it. An MCP token opens no other route: every
other `/api/` route still requires the launch capability or the cookie. The
route keeps the exact Host check and the security headers, takes only
`POST` with a JSON content type, a body of at most 1 MiB and no
`MCP-Protocol-Version` other than `2025-06-18`, and needs no Origin, because
its client is an agent CLI rather than a browser; a request that
carries a foreign Origin is refused.

Each token is 32 random bytes, minted by the daemon for one agent: the app's
chat, or an action working on one pull request of one `owner/name`
repository. Tokens live only in the daemon's memory. They are never written
to `app.sqlite`, `config.toml`, `credential.json` or any other file, never
logged, and die with the daemon or when revoked; a revoked token is refused.
Lookups compare a hash of the token in constant time.

Tools read through the same routes the app calls, only through `GET` routes
on an allowlist and without refresh flags: the reader has no way to send
another method, and refuses the `refresh` and `more` parameters, which make
some `GET` routes fetch from GitHub or rebuild a cache. Each tool reads only
the route prefixes it declares, and no tool can read settings, GitHub
authentication, GitHub listings, diagnostics, debugging, plan usage, the
event stream or `/api/mcp`, with one narrow exception below. No reader reads a pasted image of a session's
transcript (`/transcript/images/`), which is served unmasked, and the
transcript tools drop the image fields, the session's working directory and
the person's address. The agents' requests are read without the person's
settings for answering them. The Timeline's detail routes name their
repository only in the response, which an action's reader checks (an archived
feature names none and is never read); a session's Timeline transcript names
none, so only a chat reads it. Responses are bounded to 8 MiB, apart from a review document and the facts, which are read up to the 32 MiB the daemon serves.

The exception is a fixed list of tool and exact route pairs in the daemon's
source, which a test pins: `diagnostics_summary` with `/api/diagnostics`, and
the Settings tools with the Settings routes their sections show
(`/api/settings`, `/api/settings/appearance`, `/api/settings/prompts`,
`/api/github-auth` and, for This computer's agent setup, `/api/diagnostics`).
These routes stay unreadable to every other tool, and every other route under
`/api/settings` and `/api/github` (the sign-in's start, the plan usage choice,
decision capture's settings) stays unreadable to all. The tools are global,
so an action neither lists nor calls them, and the reader refuses an action the
routes whatever a tool declares. Each returns a typed projection filled only
with what the app shows in that section. The diagnostics give a check's id,
label, status, version and where it was found, scrubbed as the public issue
summary is (home folder and account names, secrets and addresses removed; a
GitHub Enterprise host is `other host`; token and backend checks name no
source), never the home folder, a check's path, detail or suggested fix, the
environment or the issue the Report a problem dialog drafts.

What the Settings tools expose: the theme, key hints and page shortcut
modifier; which plans show their usage; whether automatic reviews are on and
for which pull requests; the review, fix and decision capture harness, model
and effort; the GitHub connection's state, login, `github.com` or `other host`,
the kind of place its token comes from (the app's sign-in, an environment
variable or the config file, never which variable or file), whether workflow
access was granted, an error code and fixed message (scrubbed), and the
organizations it cannot reach; the Codex hooks state the app last saw; job
concurrency and time limits; the names of the agent environment variables and
whether the app or the config file sets them; whether the app answers Codex's
permission requests; the agent checks' labels, statuses and versions; and the
prompts with their texts (built-in, custom and inherited), as the Prompts
section shows them. What they never expose: an agent environment variable's
value (the values are not copied out of the route's answer), the GitHub
token, a waiting sign-in's user code or verification address (the waiting
sign-in is decoded only for whether there is one), the budgets and caps, a
check's folder, detail or fix, the configuration file's folder, an enterprise
host, or the home folder. The route's raw answer is in the daemon's memory for
the length of the call, as any read's is, and is never kept, logged or quoted
in an error.

A repository's page tools (its review choices and automatic status, its
prompts, its registration) are repository-scoped: an action reads its own
repository's, through routes bound to it (the repositories and automatic
lists are rebuilt from its row alone, without clone folders or the global
switches, and the prompts route is bound by its path), and never a clone's
folder. From a repository's prompts an action gets what is the repository's
own: each prompt's title, group, built-in text and version, the repository's
custom text, whether it is outdated or invalid, its safety rules, and which
text runs (`source`: built-in, global or the repository's). It does not get
the text the repository inherits, which is the person's global custom prompt
from Settings, nor, for a prompt that runs the global text, the safety rules
that text dropped; the answer says these were withheld. A chat gets them, as
the app shows them. Listing the repositories, which the review and
registration tools read, probes each registered clone with git on this
computer, as the Repositories page does; an action's call does that work too,
and reaches nothing outside.

Four routes that look like candidates are not exceptions, because they reach
outside or start programs: the plan usage route fetches from Claude or Codex
when its cache is stale, and so does the plan usage choice
(`/api/settings/plan-usage`), which reads the plans to work out the default;
the Codex hooks route starts Codex when its cache is stale or holds a warning;
and the picker's list contacts GitHub (a new query starts a search that spends
the account's rate limit). Their tools read what the daemon already holds,
through accessors that cannot fetch or start anything (a call never waits for
a fetch in progress, the picker's list says when none is loaded, and the Codex
hooks state is the newest the app's own check returned, or says none was).
Decision capture's settings route resolves an auto backend by running the
harnesses' version checks on this computer on every read, as opening the
section in the app does; its tool reads an accessor instead that shares one
resolution between concurrent calls and reuses it for 45 seconds while the
settings are unchanged, so an agent calling it repeatedly starts those checks
at most that often. Reading the GitHub connection may start, as
the section does when it opens, the background check of the organizations the
sign-in cannot reach: at most every 30 seconds, answered from a five-minute
cache. A job's failure
text, which can quote git's output and so the home folder, is scrubbed the same
way in every job tool. The repositories tools never return a folder: a
clone is a count, an availability and an id, and the suggested clone folder is
written from the home folder (`~/...`).

An action's token is bound to its repository. Its tools default to its own
pull request and may read other pull requests of that repository, never
another repository. Tools are either global or scoped to one repository; an
action neither lists nor calls a global tool. The repository-scoped tools
read the repository the server resolves for them, which for an action is
always its own. As a backstop, an action's reader reads only the routes in
a fixed table, each bound to the repository the way its handler finds what
it serves: by the `repository` or `clone` parameter that handler reads, by
an `owner/name` path, or, for a route that finds a record by id, by checking
the record's repository and returning nothing on a mismatch. A route that
names no repository (a job's transcript or draft, a group's document or
versions) is read only
after the record its id names has been checked the same way, and a route that
answers for every repository (the review index, the inbox, the running
sessions) is filtered to the action's repository inside the reader, which also drops other clones'
problems, the user's saved inbox queries and each inbox item's saved-inbox
ids, and recounts the inbox total over what is kept. Any other route, and any query
parameter its entry does not list, is refused. An
action never proposes changes. An action reads a review only in parts, one per
tool call, and through the same bindings: its pull request's saved review, a
running draft of a job of its repository, conversations of its clones, and a
group of its repository. An action may also read, for its own repository, a
file the review cites at the review's head (text cut at 512 KiB), the commits
and commit patches its pull request's commits link, the decisions captured
from the sessions of its pull requests, the group patches and files its groups'
reviews cite, and what the app offers about a branch and the failed checks.
It may also read the last lines of a failing check's log for any check of its
repository's pull requests (`check_log`; see below).
It may read the person's own draft review comments only for its own pull
request: those are kept by a key that names the repository and pull request,
the handler cannot tell whose a key is, so the reader refuses
any key but the action's own pull request's (and any other kind of saved state),
whatever the repository. A chat reads them for any pull request, as it reads
the page the person has open.

A failing check's log tail is the one place the pull request page reads a
check's own output from GitHub beyond its title. It is fetched only when a
person opens **Show log** or an agent calls `check_log`, never in the
background and never to fill a list. The daemon reads it with your GitHub
credentials (`GET /repos/{owner}/{repo}/actions/jobs/{id}/logs`, which answers
with a redirect to a signed download address: the redirect is followed once,
only to https, and the token is sent to the API host alone). It first checks
that the check run is one of the pull request's current checks, so a request
cannot name another run. It asks the download host for the last 8 MiB of the
log only (a range request) and streams it, keeping only the last 200 lines and
64 KiB (timestamps, terminal escapes, control and invisible format characters
dropped) rather than holding the log; a log too long to read the end of, from a
host that ignores the range, is answered as too long, never from its middle.
GitHub masks the secrets it knows in a job log; baum runs the same redactor it
uses for all text it shows on the tail as well, over each whole line before it
is cut, and refuses the log if that fails. The tail is held in the daemon's memory only, per
check run (at most 64, the least recently used going first), is emptied when
the GitHub account changes or a check is re-run, and is never written to disk.
A log is the check's own output and so untrusted text, like any CI output: the
app shows it as plain text, and a tool's answer carries it inside the usual
untrusted-data fence. An action reads it through `/api/prs/check-log`, a route
bound to its repository by the `repository` parameter; it can read the tail of
any check run of its repository's pull requests, not only its own pull
request's, as it can read their other checks.

A chat's token may also propose a change to the page the user has open, and
only to something that page shows (on a pull request's page, only that pull
request's items). A proposal changes nothing until the user saves it in the
app. The value and revision it is compared against are read by the daemon
when the proposal is recorded, never taken from the agent; a value the agent
says it read must match the current one, and a mismatch is refused with an
error that says nothing more. The current value is shown to you, never
returned to the agent: a proposal tool answers only the proposal's id, page,
target and kind. Saving checks that you still have the page open, showing the
same repository, pull request or group, that the target still belongs to the
section you see and that the agent's token is still live, and the page
compares the revision and writes in one step, so a target changed by anyone
since the proposal was made is refused and nothing is saved. A token revoked
while a save is writing takes effect once the write ends.

What an agent can cause is a proposal for you to look at: a setting of the
Settings page (appearance, plan usage, automatic reviews, decision capture,
the harnesses for reviews and fixes, time limits, job concurrency, whether
the app answers Codex's permission requests), a custom prompt or its reset, a
repository's review and decision capture choices, a saved inbox, a review
group's name and members, or a comment added to your pending review through
`propose_review_comment` (Save in the panel, never sent to GitHub: you send
the review). Nothing is saved without your save, and your save is the page's
own, with the page's checks, so a proposal cannot
save what the page itself would refuse. A proposed prompt, or a reset, that
would leave out the text of a safety rule the prompt in use has is refused,
when proposed and again when saved. That check only finds each rule's text;
it cannot tell whether the rest of a text undercuts a rule, so read a
prompt's diff before you save it. An agent never proposes an agent
environment variable's value and never learns one: it may propose a
variable's name to add or remove, the proposal holds only the name, and you
type the value when you save it; the value goes from the app's save to the
setting and is never stored in the proposal, returned to the agent, printed
or logged. Nothing outside that list can be changed through a saved
proposal: not your GitHub connection, a repository's registration or clones,
nor any GitHub write.

One change takes no save: adding a tile to the review on the pull request
page you have open. A chat may add a library tile type or a tile it made for
that page (`add_tile`, `start_tile`, `check_tile`, `add_custom_tile`), on one
of the review's sections, and it applies at once. The agent reads untrusted
pull request text (the diff, the description, threads and transcripts), which
can tell it to add tiles, and baum accepts that without asking, because the
worst such text can do is add a tile to your own page, visibly, removably and
within fixed rules:

- No content changes. An added tile stands among a section's tiles; the
  review's text, risks, recommendation and risk score, the saved version, the
  repository and GitHub are never touched.
- Every added tile shows, and is taken off in one step: Remove on its menu
  takes it off every version of the pull request, and the toast's Undo puts
  it back on the section it stood on. Only you remove; the chat has no tool
  for it.
- Scope is one page. The tools are global, so an action never sees them; they
  refuse any page but the pull request's, and a section they name must be one
  of the review that page shows. A conversation's page follows the person
  around the app, so an add is also refused unless the page open now is the
  pull request the current message was sent from: a long turn begun about one
  pull request cannot change another's page. Added tiles stay on this
  computer, in `app.sqlite`, for that pull request; the event that tells the
  page carries only the pull request's key.
- Bounded: at most 8 added tiles a pull request, the 200 most recently
  changed pull requests; a removed tile is kept a day for its Undo.

An added library tile brings data. Data the agent writes
for it is checked against the type's schema, every hunk it cites must be in
the diff of the version shown (a refusal names the place in the data, never
its values), and it is masked for secrets, as every model output is, before
it is stored on this computer. The type is one of baum's built-in library
types, named by the host; the page labels the tile as baum's own by its
definition's digest, which data cannot change. A review never takes the
agent's data as its own: only fills baum ran itself are reused in a later
version. An add without data starts one fill run with your review agent (a
model call at your expense, as a review's fill is): each version's tile type
is filled at most once while baum runs, however often it is added, removed
and added again, and a failed run is not retried for ten minutes. The run is checked
and masked like any fill.

A tile made in chat brings code as well as data: the agent, which reads
untrusted pull request content, writes a tile type (HTML and JavaScript) for
the pull request page you have open (`start_tile`, `check_tile`,
`add_custom_tile`), and it is added at once, as any added tile is. That is
safe to do without asking for the same reasons, and these:

- The frame is the boundary, unchanged. The tile runs in the custom tile
  frame described above ("Custom tiles"): sandboxed, an opaque origin, the
  frame route's policy with no network, only its own data and the items it
  cites, and the strict message router. The render check and the lint are
  quality gates for honest mistakes, not security controls; the check draws
  the candidate in the check window where the desktop app has one, else in
  the page, as the first-show preflight does.
- It is labelled and grounded. It stands under a "Custom tile" caption on
  the review page, whatever its title says, and is marked baum's own only by a
  built-in type's digest; its name may not be a built-in library type's. Its
  data must cite at least one hunk, file, prompt or session event of the
  review, and every citation must resolve to the version shown, so each
  claim leads to code or a transcript you can open. It stands among a
  section's tiles and cannot take the place of the review's own text,
  recommendation or risk score.
- Its data is masked for secrets before it is checked or stored, as every
  model output is, and kept, with its definition, in `app.sqlite` for that
  pull request only.
- It is yours and the page's only. Saving it as a type for later reviews is
  a proposal (`propose_tile_type`) you save, with synthetic samples only; on
  later versions it is never filled again, because its fill instructions were
  written by the session agent, which reads untrusted pull request content, and
  you have not seen them: it shows its newest data marked as from the version
  it was made for. Save to library shows the fill instructions before you save
  them, and only then does a review agent fill the type on later versions.
- The pending card that shows while the tile is made lives in the daemon's
  memory; its event carries only the pull request's key.
- Where the desktop app does not mount tile frames (Windows, for now), the
  tools refuse before anything is made.

There is no confirmation and no rate cap on purpose: each would make "add
the API changes tile" slower for everyone to guard against an attack whose
worst result is a tile you see and remove. Anything that lasts beyond the page you have open
(standing preferences for a repository's future reviews, for instance) stays
a proposal.

A chat may also propose an operation: one of the buttons of the page you
have open, for something that page shows. The agent never runs anything.
Nothing runs until you confirm it in the app, and then the daemon runs the
same route the button calls, with that route's own checks, once: the
proposal is taken out before it runs, so a second confirm, or two at once,
runs nothing more. Confirming checks that you still have the page the
proposal was made on open, showing the same thing, that its target is still
on it (a request still pending, a job still queued or running) and that the
agent's token is still live; a token revoked before your confirm took the
proposal runs nothing. Each operation sends one fixed method to one fixed
route: the values an agent's arguments put in the path must each be one
plain segment, and the request is checked to be that route before it is
sent, so no argument can reach another route. The agent's answer is the
proposal's id, page, target and kind, never what will run or what the route
answered. You read what will happen and on what before you confirm: the
pull request's number and title and the head, the job, the session's agent,
id, title and repository, or for an agent's request the requesting agent, its
session and exactly what it asked (the tool and every input value, or every
question, in full). The app stores a request as it shows it: a value cut at
its bound (4,000 characters for an input value, 1,000 for a question),
cleaned of control or formatting characters, or with a secret masked is not
exactly what the agent will run, so such a request can be denied from a
proposal but never allowed or answered: you answer it in the app or the
terminal. The summary's sentence is baum's own and names things only by
identifiers that pass their own check (a repository spelled as GitHub spells
one, an id that is one plain path segment); anything else, and any text that
is not baum's (a title, a thread, what a tool asked, what the agent wrote),
is a separate quoted field, never part of the sentence or a field's label.
Text read from the daemon is shown plain: control characters, invalid UTF-8
and invisible characters (bidirectional overrides and isolates, zero-width
characters, the soft hyphen, tags, variation selectors, Hangul fillers and
the other default-ignorable characters) are removed, and line breaks are kept
only in fields that are meant to be multi-line. What the agent wrote (a
message, an answer, a group's name) is shown in full, exactly as it will be
sent: it is never cut, text longer than the operation takes is refused, and
text with control, invisible or line-separator characters is refused rather
than cleaned. A repository is sent and named as baum's registry spells it.
The app confirms with the revision of the summary it showed you: confirming
reads the summary again, and when it reads differently from the one you saw
(another title, harness, model or effort), or the app sends the revision of
an older summary (a second click after the summary changed), nothing runs
and the proposal shows the new summary for you to confirm again. A fix or a
review whose harness, model and effort cannot be read from your settings is
not proposed or confirmed at all.

What you can be asked to confirm: on a pull request's page, a merge (with a
method GitHub allows, at the head the page showed), a branch update, a re-run
of the failed checks or of one failed job, resolving one review thread, a fix
and a new review; on the Inbox and Pull requests, a review, a refresh from
GitHub and (Pull requests) a new review group; on the sessions pages,
answering an agent's pending request, a message to a session, ending a
session and bringing it forward; on the jobs pages, cancelling a job the
page lists as queued or running (on a job's page, that job); on a
group's page, deleting it; on the Repositories page, registering a
repository, removing one or a clone's registration, and cloning a registered
repository into the folder the app would choose by default, which the summary
names (an agent cannot choose the folder). A fix and a review start a paid
agent run: the summary says so and names the harness, model and effort from
your settings. A merge, ending a session, deleting a group and removing a
repository or a clone are marked as not undoable. An agent's request is
answered only from a page that lists it (the Sessions page, or that
session's page for its own), only with allow, deny or answers to its
questions: a permission's suggestions (always allow, a permission mode), which
can change the agent's permissions for good, are yours to apply in the app
and cannot be proposed. A chat's token names the agent session the chat runs
in (Claude Code or Codex), set when the token is minted or once afterwards
(it cannot be changed); a chat never answers a request of its own session,
so it cannot approve its own tool calls, and never messages, ends or brings
forward its own session. While its session is not known, it can propose
none of these for any session: no answer, message, end or focus at all. A
review of a pull request from a fork or an
untrusted author is refused by the route without your isolation
confirmation, which an agent cannot give; you start it from the app.

Every tool result is masked with the same redaction as prompts, cut to at
most 64 KiB, and placed between `BEGIN UNTRUSTED DATA` and
`END UNTRUSTED DATA` lines that carry a fresh random tag, which is removed
from the data first. The rest of a long result is kept masked in memory, for
at most 15 minutes and eight results per agent, under a random cursor that
gives it once and only to the agent it was cut for. Revoking a token drops
its cursors and proposals, including any a call still running would leave.

## Desktop app

The desktop app (`desktop/`) is a window onto your `baum daemon` and adds no
API of its own. It runs the bundled `baum daemon ensure`, then reads the
daemon's launch URL from a credential file in the daemon's state directory.
It accepts the file only when it is private to you and names a
`127.0.0.1` launch URL for that daemon, and it never logs that URL.

The window loads only its bundled screens and the attached daemon's
loopback origin, and refuses every other navigation: other web hosts, other
loopback servers and other schemes, and any address with a user name or
password in it. A navigation never opens your browser or leaves the app. On
macOS and Linux the webview asks the app about a navigation in any frame of
the page, an embedded document or injected content included, and tells it
only the destination; on Windows it asks only about the page's own
navigations, and the page's policy alone governs its frames. The daemon's
pages limit frames to their own origin, which on Linux stops a frame's
navigation elsewhere before the app is asked, but a page without that policy
(the development server's) or an engine that asks first would let any frame
through; if a navigation could open the browser, such a frame could open it
at an address carrying data it read, as often as it liked. Links to other web
hosts open in your default browser only as requests for a new window
(`target="_blank"` links and `window.open`); no window ever opens. A
sandboxed frame can make such a request only when its sandbox allows pop-ups.
On Linux the webview allows a page one only while you interact with it; on
macOS and Windows a page's script can make one at any time, so the app opens
the browser at most once a second and drops the rest. Setup opens GitHub's
fixed device sign-in page itself, a few seconds after you start a sign-in.

The daemon's pages can call a few of the app's commands, granted to pages of
`http://127.0.0.1` on any port and listed in
`desktop/capabilities/daemon-pages.json`: the
folder picker, reading and changing the `baum` command and the user-wide agent
hooks (as Settings does), the window's zoom, and reading, checking for,
installing and dismissing app updates (only an update the app has already
downloaded and verified installs), and opening and closing the tile check
window. They can call no other command and no plugin. Only the page itself can call them: on macOS and Linux the app's
command bridge is injected into the page's main frame only, so a frame
embedded in it, even one served from the daemon's origin, cannot reach a
command; on Linux this is checked with a sandboxed frame trying every way the
bridge offers. On Windows the bridge is injected into every frame, so there
the shell declares to the page (`window.__BAUM_DESKTOP__.tileFrames`, set
before the page's scripts and not replaceable) that it must not embed
frames of untrusted content from the daemon's origin; on macOS and Linux it
declares that it may. The bundled connecting screen may only read the connection state and
ask to reconnect. The bundled setup screen may also read what is installed and
install or remove the `baum` command and the user-wide agent hooks; it does
so only when you click the button for that change, and a change that would
replace a different `baum` needs you to confirm that file's path first. The
app makes no network requests other than loopback requests to the daemon
and the update check described below.
Quitting it leaves the daemon running, and so does closing
its window, which hides it into the tray. The tray reads the daemon's job
list, automatic generation status and diagnostics, and can pause automatic
generation, with the same launch capability the window uses, sent as a bearer
token to the loopback API and nowhere else. "Quit and stop background reviews"
runs `baum daemon stop` after asking when a review is running. Start at login
is off until you turn it on; it adds one per-user entry (an XDG autostart file,
a LaunchAgent or a Run key value) that starts the app hidden, which starts
your daemon. Turn it off in the tray or the window's Background menu, which
removes the entry.

On Linux the daemon's page checks a custom tile in a hidden second window,
the tile check window, which runs in a web process of its own. It starts at
the attached daemon's check page and can load only pages of that daemon's
origin, under the main window's navigation rules, opens no browser, and its page can call no app command. A tile that never
yields there stalls only that window, and closing it ends its web process. On
macOS and Windows there is no check window.

A script that never yields, in the page or in a frame it embeds, freezes the
whole window: the webview runs a page and its frames in one process, and the
app does not detect an unresponsive page. The tray, its menu and the daemon
keep working, and quitting the app from the tray and opening it again loads
the page anew. Your data is not at risk, but nothing stops a page from being
unusable until then.

A navigation to an address the app's URL parser cannot read is allowed by the
app's runtime without asking the app. Such a navigation can never open your
browser, and the page's own policy still applies to it.

On first run, and from "Command Line and Hooks…" in its menu, the app
installs its bundled `baum` as your `baum` command, because agent hooks run
`baum` by name:

- On macOS and Linux it creates one symbolic link named `baum` to the bundled
  binary, in `~/.local/bin` or `~/bin` when one is on your login shell's
  `PATH`, else in `/usr/local/bin`. Writing to a directory you cannot write
  goes through the system password prompt (`osascript` on macOS, `pkexec` on
  Linux) only for a system directory: outside your home directory, reached
  without symbolic links, owned by root and writable by no one else, below
  directories that are root's and not writable by others (in practice
  `/usr/local/bin`), and only for a link to a baum outside your home
  directory (a baum in your home directory gets a link in `~/.local/bin`
  instead). Root then runs a fixed shell script that takes the paths as
  arguments, refuses any link not named `baum` in that directory and any
  backup not named as the app names them, and checks the directory again
  from inside it before it changes anything, so a directory swapped in while
  the prompt shows is refused. A password-prompted link to an app in
  `/Applications` (as other developer tools install their commands) puts
  that app's binary on every user's `PATH` and on sudo's `secure_path`; it
  is only as safe as the app bundle, which on macOS the admin group can
  rewrite. A different `baum` first on your `PATH` is
  replaced only after you confirm, and is kept beside it as
  `baum.replaced-by-baum-app`; one a package manager owns (Homebrew, Nix,
  the system directories, asdf or mise shims) is never replaced. An AppImage
  links to a copy of its baum in the app's data directory, which the app also
  runs, since its own files vanish when it quits.
- On Windows it keeps a copy of its `baum.exe` alone in a directory of its
  data directory and adds that directory to the front of your user `PATH`.
- It records what it created in the `setup` section of `desktop.json` in its
  configuration directory. Notification preferences, zoom and update state
  use other sections of the same file. Native writes preserve unrelated
  sections under one process-wide lock and replace the file atomically
  (mode 0600 on Unix). Corrupt JSON or a malformed target section is never
  overwritten; command install/removal refuses unreadable setup records
  before changing a link. Removing the command deletes only that link, and only while it still points
  at the app's baum, and puts back a baum it replaced; on Windows it removes
  the `PATH` entry. Since you (and your programs) can edit that file, the app
  asks for the password to remove a link only when the record has the shape
  the app writes and names the baum the app runs now.

The user-wide hooks are installed and removed by the bundled `baum hooks
install --user` and `uninstall --user` (see "Hosted service commands"). To see
what is on your `PATH`, the app runs the bundled `baum daemon login-path`,
which starts your login shell the way the daemon does and runs the first
`baum` it finds with `--version`.

When the Claude Code or Codex baum finds is older than it supports, the app
shows a command to run in your terminal: the app's own baum by its full path,
followed by `desktop-setup`. It runs `claude --version` and `codex --version`
from your terminal's `PATH` and writes the directories they are in to
`terminal.json` beside `config.toml` (readable only by you). baum, the daemon
and the app's status checks then put those directories first on the `PATH` they
use, so they run the agents your terminal runs. Anything that can write your
configuration directory could already set `backend_paths` there, so the record
gives no new control; directories that no longer exist are skipped.

Besides loopback requests to the daemon, the app makes one kind of network
request: a release build checks GitHub Releases for a newer version of
baum-ai/baum when it starts and once a day (`BAUM_DESKTOP_UPDATES=0` turns
this off). The check is a plain HTTPS download of `latest.json`; it carries
no token or data of yours. The app installs an update only when the
downloaded bundle carries a minisign signature from the release key built
into the app, made for the version the manifest announces, so neither a
changed bundle nor an older signed release served as a newer one is
installed. It waits while the daemon runs a generation job and restarts only
after you confirm.

The signing keys (the updater key, and Apple's and Windows' code signing
credentials once they exist) are secrets of the `release` environment of
baum-ai/baum, not of the repository. GitHub releases them only to a job of
the release workflow that names that environment, only for a `v*` tag (the
environment's deployment policy), and only after a required reviewer
approves that run. The release workflow also stops unless the tagged commit
is on `main`. The jobs that use the environment build nothing: they sign
bundles that an earlier job compiled without any secret, using Apple's and
Microsoft's signing tools and a minisign binary pinned by checksum. The
release workflow verifies every updater signature, version and download URL
before it publishes the manifest, and the release stays a draft until it
has. See `desktop/README.md` for the jobs.
