# Security policy

## What baum sends where

baum is local-first, but generating a review requires a model. When you
run `baum internal generate`, the following is sent to the **model provider you
configure** (the `claude`, `codex` or `cursor-agent` CLI, each of which talks
to its own provider under your own account, Cursor to the model it routes the
run to, or the Anthropic API, which the `anthropic` backend calls with your
`ANTHROPIC_API_KEY`):

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
`refs/pull/<N>/head` and bases by id, into `refs/baum/pull/<N>/`, where
they stay. A failed fetch is tried again after 15 minutes. It keeps the pull requests' titles,
descriptions, authors, commits and changed paths in the private decision
store, and links the commits to the sessions on this machine there.

Redaction is best effort. Review `baum internal bundle` output before using
`generate` on a repository whose sessions may contain sensitive material, and
do not run it against repositories you are not allowed to share with your
provider.

Description images are downloaded from URLs GitHub signed in the pull
request's rendered body, which your token read. The downloads carry no
credentials, and only these hosts are contacted: `github.com`
(`/user-attachments/assets/`), `private-user-images.githubusercontent.com`,
`user-images.githubusercontent.com`, `objects.githubusercontent.com` and
`github-production-user-asset-*.s3.amazonaws.com`, over HTTPS. A redirect to
any other host is refused, and an image hosted elsewhere is not downloaded.

baum sends no telemetry. Unless you run `baum login`, nothing leaves the
machine except calls to GitHub, the configured model provider and
`static.usebaum.com`, from which it downloads the public price list used for
cost estimates. That request is a plain download: it carries no token,
repository, pull request or session data, and the list's signature is checked
against a key built into the binary. When the list cannot be fetched or
verified, baum uses a copy embedded at release. The hosted service commands
are described [below](#hosted-service-commands). Tokens are read from the
environment, `gh auth token` or the 0600 config file and are never written
anywhere else. Local inbox discovery also reads GitHub's REST API using that
same token and derived host, without redirects or extra scopes. Its private
0600 cache contains minimal inbox metadata and ETags, partitioned by a credential
fingerprint and repository. Tokens, PR bodies and signed image URLs are never
put in that cache. Cached and offline results are marked stale; unreadable
author write permissions remain unknown, rather than granting automatic work.

## Hosted service commands

`baum login`, `logout`, `whoami`, `setup`, `upload` and `hooks`
connect this machine to a hosted baum server. They are opt-in: nothing is sent
to a baum server until you run `baum login` (or provide a `BAUM_TOKEN`).

- `baum login` runs a device-authorization flow with the server you name
  (`--server` or `BAUM_SERVER`, default `https://usebaum.com/api`) and stores
  the token it issues in `~/.config/baum/credentials.json` (mode 0600, or under
  `XDG_CONFIG_HOME`). The token is sent only to that server.
- After that, `baum upload` and `baum setup --upload` send the coding-agent
  sessions recorded for a repository to the server you logged in to, as an
  archive in which goredact masked the secrets it detected, on this machine.
  Redaction is best effort, as above, and repository and worktree paths are
  sent as they are. `setup --sync` asks that server to synchronize the
  repository. Each request also names this baum's version and API protocol.
- `baum setup` without those flags contacts no baum server. It may download
  the pinned release of the [Entire](https://docs.entire.io) CLI from GitHub,
  checked against a SHA-256 built into the binary, and installs Entire's
  hooks and baum's in the repository.
- Agent and Git hooks contact no baum server. The hook commands
  committed to repositories run baum only for a logged-in user or in a clone
  set up with `baum setup`, and, for a baum built or installed for the hosted
  service, also in a repository whose committed agent hook settings dispatch
  through baum (as the hosted service's onboarding pull request and `baum
  setup` write them). There a hook may install the pinned Entire release from
  GitHub and the clone's Git hook dispatchers, then hands the event to Entire.
  Everywhere else the hooks run Entire directly, and baum does nothing at all:
  without a login, an open-source build does not install Entire or its hooks
  in a repository just because the repository's files ask for it, unless the
  machine still has the hosted install script's marker (below), or the baum
  daemon has run for you. Where only the daemon lets the hooks run baum, they
  do nothing for the hosted service: no Entire install, no Git hooks, Entire
  called exactly as without baum (and, for the user-wide hooks, not at all),
  and the event recorded by the daemon (below).
  Uploading, or anything else that contacts a baum server, needs `baum login`
  in every case.
- Where a hook runs baum, it also reports the run to the baum daemon (below),
  starting it if none runs, unless you stopped it with `baum daemon stop`,
  set `BAUM_NO_DAEMON=1`, or `CI` is set: the agent and event or Git hook,
  the working directory and exit status, and from the agent's payload only
  its session id, transcript path, working directory, event name, start
  source and end reason, and of a Claude Code notification its type and the
  sentence it showed you, cut to 512 bytes (see "Authenticated local
  application"), never prompts or tool input and output. A Git
  `pre-push` event never keeps the remote URL, which can carry credentials,
  and keeps the refs being pushed. When the daemon cannot take it within
  300 ms, the event waits in a private spool (`spool/` in the daemon's state
  directory, mode 0700, one 0600 file per event, about 1000 events and 8 MiB,
  oldest dropped first). The daemon keeps these events on your machine; none
  is sent to a baum server.
- `baum hooks install --user` adds baum's hooks to your user-wide agent
  settings: `~/.claude/settings.json` for Claude Code and
  `~/.codex/hooks.json` for Codex. `baum setup` does the same for a hosted
  install or a logged-in user, unless `--skip-user-hooks` or `--skip-entire`,
  and its `--all` plan names those files first. The hooks run in every
  directory an agent works in, so they run baum only in a Git repository with
  Entire enabled where no other hook the agent runs (the repository's, yours,
  managed policy's, `/etc/codex`'s; not plugins') already hands the event to
  Entire, and there only under the rules above; everywhere else they do
  nothing. They read local files only. Nothing else in the files changes; the
  previous version is kept in `~/.local/state/baum/backups/` (or under
  `$XDG_STATE_HOME`), a file baum cannot parse is left alone, and root is
  refused in another user's home. `baum hooks uninstall --user` removes
  exactly those hooks (or delete the entries whose command contains
  `baum hooks agent --check --user`). Codex runs them only after you trust
  them with `/hooks`, and trusts hooks by position: when a removal moves
  hooks of yours, the command lists them for you to trust again.
- Once installed, Entire's own hooks record agent sessions as checkpoints and
  push them, transcripts included, with `git push` to the checkpoint branch of
  the repository's remote (or the checkpoint remote its settings name). That
  is Entire's behaviour, not baum's; whoever can read that remote can read
  them.
- A baum counts as installed for the hosted service when its release build
  says so, or when the hosted install script left the file `hosted-install`
  beside the credentials (`~/.config/baum/`, or under `XDG_CONFIG_HOME`). The
  marker outlives the binary that came with it: an open-source baum installed
  later on the same machine also sets up onboarded repositories without a
  login. Delete the file (`rm ~/.config/baum/hosted-install`) to stop that.
- `baum logout` revokes and removes the stored token but leaves the markers
  `baum setup` and the hook bootstrap write in a clone's `.git/baum`
  directory (`setup-complete`, `bootstrap-ok`). That clone's hooks keep
  running through baum for that local work; without a login they send
  nothing to a baum server.

## What `baum internal doctor` runs and prints

`baum internal doctor` diagnoses the machine and starts no model. It runs
`<tool> --version` for `git` and `gh`, each with a timeout, in
a process group that is killed whole when the timeout passes, and with the
filtered environment of the local backends, which drops provider keys and
GitHub, cloud and SSH-agent credentials, never the whole of baum's. The tool's
path comes from `backend_paths`, `GH_PATH` or `PATH`;
the same checks `baum internal generate` runs for `claude`, `codex` and `cursor-agent`,
which include codex's read-only sandbox preflight (a sandboxed `cat` and a
blocked write in a temporary directory it removes) and `cursor-agent --help`
and `--version`, and for `anthropic`, which only looks at
whether `ANTHROPIC_API_KEY` is set and sends it nowhere; and, only when no token comes from the
environment, the config file or `gh`'s own files, `gh auth token
--secure-storage --hostname <host>`. Its one network contact is the signed price
list described above, skipped with `--offline`; nothing else leaves the machine.
It creates no files of its own: it opens the registry read-only (SQLite
`mode=ro`, so no journal, table or file mode changes) and only when it exists,
and does not create a review store. It asks a running baum daemon for its
version, process id and uptime over the daemon's socket, and never starts one. A config file that does not parse is shown
by position, and by key for a wrong type or value, and never by its text. Asking
gh for a token may wait up to 30 seconds for a keyring unlock prompt. `gh auth token` keeps the environment gh needs to find
its own configuration, as `baum internal generate` does.

It never prints a token or an API key, or a prefix, length or hash of one. For a
GitHub token it prints only where it was found (an environment variable's name,
the config file, `hosts.yml` or the keyring), and for `ANTHROPIC_API_KEY` only
whether it is set. It does print paths, tool versions and the `PATH` entries,
so look at a report before pasting it into a public issue.

## What `baum internal view` shows

`baum internal view` listens only on `127.0.0.1` and never contacts a server. Besides
the document, it shows the coding-agent sessions on your machine that it
linked to the pull request, and, when you open one, that session's full
transcript: your prompts, the agent's messages, and each tool call's input and
output, which can include file contents and command output. The transcript is
redacted with the engine `baum internal sessions --dump` uses, so secrets and e-mail
addresses are masked as described above, and your home directory is written as
`~` where it starts a path; paths outside it (a clone elsewhere, a Windows
home under `/mnt/c` in WSL) are shown as recorded, as are home paths glued
to other text (`-I/home/…/include`, a path after an escaped backslash), and
nothing else is removed. Only sessions linked to the pull request are served.

The server has no login. It refuses requests whose `Host` is not its own
address, which stops other websites in your browser from reading it, but any
other user or process on the same machine can request the port while it runs
and read everything it serves, transcripts included, even without permission
to read your `~/.claude` or other session stores. Do not run `baum internal view` on a
shared machine. The baum daemon, which serves the local application, adds the
separate authenticated boundary described below.

## Reading your clone without downloading anything

`baum internal sessions` and `baum internal view` read your clone with git but never use the
network. They run fixed git commands with no shell. Refs are read in your clone;
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
account. The one exception to read-only is a fix you confirm, which gives the
agent write access to a temporary checkout: see
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
  (`--strict-mcp-config` with an empty list), and no session is written
  (`--no-session-persistence`).
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
- `--ephemeral` writes no session, and the `hooks`, `apps` and `multi_agent`
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

The unsandboxed mode (`--sandbox danger-full-access`) exists only as the
`Container` profile for the hosted review agent, which runs inside its own
container. A test fails if any code under `oss/internal` or `oss/cmd` names it.

### The cursor-agent backend

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
It runs only after you ask for it on one pull request and confirm a dialog
naming the harness, the model, the branch it pushes to and the write access;
it never runs automatically. The daemon checks fresh GitHub evidence when you
confirm and again before anything runs: the pull request is open, its branch
is in the repository itself (a fork is refused), your account can push to the
repository (`permissions.push` on `GET /repos/{owner}/{repo}`, which write,
maintain and admin access give; who wrote the pull request does not matter),
the head is still the one you confirmed, and it still has a merge conflict
(GitHub's `mergeable`), failing checks or, on a pull request you opened, the
same unresolved review threads with the same comments (a digest of them is
part of what you confirm, so a comment added or edited afterwards stops the
fix before the agent runs). A registered local clone is required. Only the `claude` and `codex` backends fix; `cursor-agent` and
`anthropic` are refused.

The checkout is a linked worktree of your registered clone, detached at the
confirmed head, under `fixes/<id>/` in your user cache directory
(`$XDG_CACHE_HOME/baum` or the platform's), outside the clone and outside the
system temporary directory. Its branch and the base branch are fetched into
`refs/baum/fix/<id>/`, as a review's missing commits are; a branch that moved
on stops the fix. For a conflict, baum starts the merge of the base branch
itself, so the agent finds the conflict markers. When the fix ends, however it
ends (success, failure, cancellation), the worktree, its registration in the
clone and the temporary refs are removed. A directory a crashed daemon left is
removed by a later fix once it is a day old, and its registration pruned.

What the agent can do, per backend:

- `claude` runs in the same restricted mode as a review, with the `Read`,
  `Glob`, `Grep`, `Edit` and `Write` tools and `--permission-mode
  acceptEdits`. Restricted mode removes every tool that runs a command or
  reaches the network, confines the file tools to the checkout and leaves
  writes to settings, git and tool-configuration files to a person, which a
  `--print` run never has. So Claude Code edits files but cannot run your
  tests, git or anything else, and cannot read outside the checkout. Settings
  files, MCP servers and sessions are off as in a review.
- `codex` runs in Codex's `workspace-write` sandbox with the network off: the
  commands it runs, such as your tests, can write to the checkout and to a
  private scratch directory that becomes their `TMPDIR`, and nowhere else
  (`/tmp` and your `TMPDIR` are excluded). As in a review, they can read any
  file your account can. baum checks before the first run that Codex's
  sandbox starts (the read-only preflight); the write-enabled profile is not
  checked separately.
- Both get the same environment allowlist as a review: no SSH agent, no
  `GIT_` variable, no GitHub token, no provider credential baum did not set.
  The agent never gets git credentials or network access to push anything.
  The one addition is the environment for agents you set yourself (Settings,
  or `[agent_env]` in the config file), which every agent baum runs on this
  computer and the git commands that commit and push a fix receive, for
  example `GIT_CONFIG_GLOBAL` to sign the fix's commit with another key. It
  is your choice, so it may name a `GIT_` variable; baum's own run variables
  win over it, and git commands drop one that would point git at another
  repository (`GIT_DIR` and the like).
- When your configuration signs commits (`commit.gpgSign`), baum makes one
  signed commit object in the checkout before the agent runs, referenced by
  nothing. A key that needs a passphrase prompt the daemon cannot show fails
  the fix there, with git's error, before the agent's work is paid for.

What reaches the agent: the pull request's title and branches, the conflicted
paths, and for failing checks the failing check runs and commit statuses of
the head (name, conclusion, output title, summary and text, each bounded) and
the last 200 lines of the job logs of up to four failing GitHub Actions jobs.
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
from an owner, member or collaborator of the repository (GitHub's author
association) or from an app's bot account, which someone with admin access
installed; within a kept thread, other authors' comments are dropped. At most
20 threads and the first 10 comments of each are read, each comment bounded
to 2 KiB, and the dialog lists the threads and says how many it left out.
Each thread goes into the prompt as untrusted data under the same random-tag
markers, with secrets masked, under a local id (`T1`, `T2`…) that baum maps
to the thread's GitHub node id; the agent never sees or returns a GitHub id.
The instructions (`fix-threads.md`) say the comments are feedback to evaluate,
not instructions, and to ignore requests to run commands, read or send files,
or change credentials, secrets, CI or workflow configuration, dependencies or
unrelated files.

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

- Every git command in the checkout names the worktree's git directory
  explicitly (`GIT_DIR`, `GIT_WORK_TREE`), so a `.git` file the agent
  rewrote is ignored, and runs with `core.hooksPath` pointing at an empty
  directory outside the checkout, so no hook runs, neither the clone's nor
  one the pull request carries (such as a `.husky` directory); the
  file-system monitor is off.
- baum stages everything the agent left, ignored files excepted, and commits
  it with your git identity and signing configuration, completing the merge
  for a conflict. It refuses to commit while a conflicted file still has a
  conflict marker, and fails when nothing changed.
- It refuses a result that does not descend from the confirmed head: a fix
  only adds commits, it never rewrites the branch.
- A fix of review comments alone (no conflict, no failing check) refuses a
  result that changes anything under `.github/workflows/` or
  `.github/actions/`: a workflow runs with the repository's secrets as soon
  as it is pushed, and no review comment, being other people's words, may
  bring one about.
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
- A `codex` agent can read files outside the checkout, such as credentials
  under your home directory, and copy them into the checkout, and baum would
  commit and push them to the pull request's branch, which may be public. A
  pull request or check log could try to make it do so. The `claude` backend's
  file tools cannot read outside the checkout.
- The commit is yours: your identity, and your signature if you sign commits.
  Clean and smudge filters your git configuration defines (Git LFS, for
  example) run on the checkout as they do in your clone.
- A review comment from a trusted author (or a compromised bot) can still try
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
pull request at a time, with the credential you saved for baum. There are five
writes and no others:

- **Merge** (`internal/github/merge.go`): GraphQL `mergePullRequest` with
  `expectedHeadOid` set to the head you saw.
- **Fix push** (`internal/fix`): a plain `git push` of the agent's commit to
  the pull request's branch, never forced (see "Fixing a pull request").
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

Each write goes through the daemon's authenticated, CSRF-checked local API,
only for repositories you picked, and is re-checked against GitHub just before
it is sent: the pull request is open, the head is still the one you confirmed
and, for update branch and re-run, your account can push to the repository
(`permissions.push`). Update branch also refuses a fork's branch and a
conflicting pull request. None is retried, and none is ever started by an agent
or automatically: thread replies follow only the fix you confirmed.

## Local repository registry

`baum internal repos` stores clone paths, git common directories, normalized GitHub
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
and change settings. `baum internal view` exposes none of these routes.

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
window name, percent used and reset time, in `plan-usage-history.json` in its
state directory (`$XDG_STATE_HOME/baum`, default `~/.local/state/baum`), for 90
days.

### Analytics

The Analytics page (`GET /api/analytics`) is built from what is already on the
computer, plus one GitHub search per picked repository:

- **Agent sessions.** The daemon reads the Claude Code, Codex and Cursor session
  stores (`~/.claude/projects`, `~/.codex/sessions`, Cursor's `state.vscdb`),
  the same files the PR page reads, but only sessions whose working directory is
  inside a registered clone and that were written in the last 90 days. It keeps
  aggregates only, in `analytics-index.json` in its state directory (owner-only, 0600): per
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
  in `transcripts/` beside `jobs.json` (private files, secrets masked, bounded,
  the 50 latest fixes). It stays on your machine.
- **baum's own work.** The jobs list (`jobs.json`) and a small ledger,
  `analytics-actions.json`, of the merges, branch updates and check re-runs you
  made from the app (time, kind, repository and pull request number), kept 90
  days.
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

### Finding clones on this computer

The daemon looks for local clones of the repositories you pick, so you are not
asked for their folders. It makes no network request for this and sends nothing
about your folders anywhere. It reads: the working directories recorded in the
hook event record and in the Claude Code (`~/.claude/projects`), Codex
(`~/.codex/sessions`, `~/.codex/archived_sessions`) and Cursor (`state.vscdb`)
session stores, without parsing the transcripts; the folders `~/code`, `~/src`,
`~/dev`, `~/projects`, `~/workspace`, `~/repos`, `~/git` and
`~/go/src/github.com`, to four levels deep, without following symlinks; the
folders that contain the clones agents worked in, when they are below your home
directory; and, in each candidate clone, the remote URLs of its git
configuration, through `git rev-parse` and `git config`. A directory an agent
worked in is stat'ed, with its parents up to your home directory, and git runs
in the clone containing it, wherever it is (git may also follow an
`include.path` in that clone's configuration). To register what it found it
runs the registry's local checks, which may resolve an SSH alias with
`ssh -G`; that contacts nothing. On macOS it reads none of `~/Documents`,
`~/Desktop`, `~/Downloads`, `~/Library/Mobile Documents` or
`~/Library/CloudStorage`, not even a directory an agent worked in, because
macOS would ask you for permission; `~/Documents` is read only in the single
scan you ask for. On Linux and Windows it also walks `~/Documents` like those folders. It never walks `Desktop`,
`Downloads`, `Pictures`, `Movies`, `Music`, `Public`, `iCloud Drive`, Google
Drive, Dropbox or OneDrive folders, hidden folders, dependency or build
folders, your home directory itself, or the filesystem root. The paths it
finds are served only by `GET /api/clones/found`, behind the daemon's
authenticated Host/Origin boundary, and registered only for repositories you
picked.

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
  and the latest saved review's checks, conversation and head, with
  conditional requests and backoff (see "What baum sends where");
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
  backend under your account, within the configured budgets, as
  `baum internal generate` does. Some you confirmed in the app with their price
  estimate; the others it starts by itself, unattended (below).

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
confirmed one (same backend, model, budgets, price list and recheck of the head
before any model call, which also stops it if the pull request was converted to
a draft while it waited), and a model without a known price is never run
automatically. What it can spend: up to the configured per-run budget for each
new head (twice when a daemon stop or restart interrupts the first run, which
is retried once; failed or cancelled runs are not retried), with no daily limit unless you set a repository's daily cap, which
pauses that repository's automatic generation until the next day once reached.
The cap is checked when a job is queued and again, against the current cap and
the cost already reported that day, right before a queued job starts; a job
over it is stopped before any model call and waits for the pause to end.
cursor-agent, which baum cannot estimate or cap, runs automatically only after
you acknowledge that in the app. Turning the global switch off (in Repositories
& settings, or pausing automatic generation from the desktop app) stops it in
every repository, including those set to "On"; while it is on, single
repositories can be turned off. Turning off (or withdrawing the cursor-agent
acknowledgement) also cancels that scope's automatic jobs that are queued but
not started, and the queue checks the current settings again before starting
each one; only a job already running finishes, within its budget. Raising or
removing a repository's daily cap ends its pause.
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
login shell (`$SHELL -i -l -c`, without a terminal, for at most 10 seconds)
with only session variables and reads the environment your startup files set,
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
askpass are off, plain `ssh` runs in batch mode, and a fetch has a timeout, so
a key that needs a passphrase or an unknown host key fails the run with git's
error. An ssh command you configured runs as you wrote it.

## Authenticated local application

The daemon's application server accepts only the bound `127.0.0.1:port` Host. Its bootstrap has no
local data; every API requires the random private launch capability or an
HttpOnly SameSite=Strict cookie obtained by exchanging that capability. The
browser receives the capability in a URL fragment, removes it from history,
and sends it in the same-origin JSON exchange body. Browsers send cookies to
every port of a host, so the cookie is scoped to a second random secret path,
`/s/<namespace>/`, returned only by the exchange; another service on a
loopback port never receives it, and the cookie authorizes nothing outside
that path. All mutations require the
exact local Origin and JSON content type, including requests using Bearer
authentication. No route enables CORS. The capability is the daemon's app
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
request number) and carries the same masked line. Codex's permission request
keeps nothing of the command it asks about. Nothing of it goes to a baum
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
agent's own session folder in your home directory, the one baum already reads
sessions from: `~/.claude/projects` for Claude Code and `~/.codex/sessions`
for Codex. A path elsewhere, a link out of the folder, a directory or a file of
another kind is not read, and the app is told only that the transcript is not
available. A Codex session whose hooks named no path is looked up by id in
that folder; a Cursor session is read from Cursor's own conversation database
by its id. The transcript is redacted exactly as the PR page's linked sessions
(the same code): secrets and e-mail addresses masked, your home directory
written as `~`, nothing else removed. The path itself never reaches the app.
At most 8 transcripts are held in memory, and only while their session is
listed.

Document selectors must name a registered clone. Linked transcripts use the
same redaction and linked-session allowlist as `baum internal view`; file patches are
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
before anything runs. A five-minute estimate is
bound to the head, selected model/backend, budgets and price list version;
confirmation cannot authorize changed inputs. Forks, non-writers and unknown
permissions require a separate single-PR isolation acknowledgement and are
excluded from batch generation. Cancellation reaches the existing backend
process-group cleanup. Job history contains fixed failure reasons and bounded
usage metadata, never arbitrary provider errors, prompts or rejected documents.
The private journal persists queued/running/terminal states; crashed running
entries become interrupted before work resumes. A fix (`"kind": "fix"`, see
[Fixing a pull request](#fixing-a-pull-request)) runs only from a confirmed
single-PR estimate, never from a direct request, and its estimate is refused
for a fork, an account that cannot push or a backend that cannot fix. A fix
job also keeps the review threads it addressed (file, line, author and the
first line of the first comment) and each one's outcome code with the
agent's cleaned summary. The stored copy of a downloaded
price list is trusted only after its signature verifies again on load. Only
the daemon that owns the per-user lock opens the queue, so there is never a
second one.

The server's bootstrap page exchanges the fragment token and clears it from
history before the embedded local application loads; the application's files
are served only under the authenticated namespace, and every application
request resolves under it. Every application request is same-origin; the app has no hosted baum API dependency. Review content uses the
same sanitizers and safe rendering components as the hosted PR page and `baum internal
view`. Stored document rendering failures remain within the PR pane. The app's
only GitHub writes are a merge and a fix you confirm, with that fix's replies on
the review threads it addressed; it supplies no approvals or other discussion
controls.

## Desktop app

The desktop app (`desktop/`) is a window onto your `baum daemon` and adds no
API of its own. It runs the bundled `baum daemon ensure`, then reads the
daemon's launch URL from a credential file in the daemon's state directory.
It accepts the file only when it is private to you and names a
`127.0.0.1` launch URL for that daemon, and it never logs that URL.

The window loads only its bundled screens and the attached daemon's
loopback origin. Links to other web hosts open in your default browser; other
loopback servers, other schemes and new windows are refused. The daemon's
pages get no Tauri capability, so they cannot call the app's commands or any
plugin. The bundled connecting screen may only read the connection state and
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
- It records what it created in `setup.json` in its configuration directory.
  Removing the command deletes only that link, and only while it still points
  at the app's baum, and puts back a baum it replaced; on Windows it removes
  the `PATH` entry. Since you (and your programs) can edit that file, the app
  asks for the password to remove a link only when the record has the shape
  the app writes and names the baum the app runs now.

The user-wide hooks are installed and removed by the bundled `baum hooks
install --user` and `uninstall --user` (see "Hosted service commands"). To see
what is on your `PATH`, the app runs the bundled `baum daemon login-path`,
which starts your login shell the way the daemon does and runs the first
`baum` it finds with `--version`.

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
