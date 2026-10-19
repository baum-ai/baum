# Security policy

## What baum sends where

baum is local-first, but generating a review requires a model. When you
run `baum generate`, the following is sent to the **model provider you
configure** (the `claude` or `codex` CLI, each of which talks to its own
provider under your own account):

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

Redaction is best effort. Review `baum bundle` output before using
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
- Agent and Git hooks contact no baum server themselves. The hook commands
  committed to repositories run baum only for a logged-in user or in a clone
  set up with `baum setup`, and, for a baum built or installed for the hosted
  service, also in a repository whose committed agent hook settings dispatch
  through baum (as the hosted service's onboarding pull request and `baum
  setup` write them). There a hook may install the pinned Entire release from
  GitHub and the clone's Git hook dispatchers, then hands the event to Entire.
  Everywhere else the hooks run Entire directly, and baum does nothing at all:
  without a login, an open-source build does not install Entire or its hooks
  in a repository just because the repository's files ask for it, unless the
  machine still has the hosted install script's marker (below). Uploading, or
  anything else that contacts a baum server, needs `baum login` in every case.
- Where a hook runs baum, it also reports the run to the baum daemon (below),
  starting it if needed: the agent and event or Git hook, the working
  directory and exit status, and from the agent's payload only its session
  id, transcript path, working directory, event name, start source and end
  reason, never prompts or tool input and output. A Git `pre-push` event
  leaves out the remote URL, which can carry credentials, and keeps the refs
  being pushed. When the daemon cannot take it within 300 ms, the event waits
  in a private spool (`spool/` in the daemon's state directory, mode 0700, one
  0600 file per event, at most 1000 events and 8 MiB, oldest dropped first).
- For a user logged in to a baum server, the daemon uploads the session an
  agent reports ended to that server, exactly as `baum upload` would upload
  that one session: secrets are masked by goredact on this machine before
  anything is archived (best effort, as above), repository and worktree paths
  are sent as they are, a repository the server reports not enabled in baum
  gets nothing, and the token is sent only to the server it was issued for.
  The daemon reads `BAUM_TOKEN` from your login shell, not from the agent.
  Without a login no event leaves the machine.
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

## What `baum doctor` runs and prints

`baum doctor` diagnoses the machine and starts no model. It runs
`<tool> --version` for `git`, `gh` and `cursor-agent`, each with a timeout, in
a process group that is killed whole when the timeout passes, and with the
filtered environment of the local backends, which drops provider keys and
GitHub, cloud and SSH-agent credentials, never the whole of baum's. The tool's
path comes from `backend_paths`, `GH_PATH` or `PATH`;
the same checks `baum generate` runs for `claude` and `codex`, which include
codex's read-only sandbox preflight (a sandboxed `cat` and a blocked write in a
temporary directory it removes); and, only when no token comes from the
environment, the config file or `gh`'s own files, `gh auth token
--secure-storage --hostname <host>`. Its one network contact is the signed price
list described above, skipped with `--offline`; nothing else leaves the machine.
It creates no files of its own: it opens the registry read-only (SQLite
`mode=ro`, so no journal, table or file mode changes) and only when it exists,
and does not create a review store. It asks a running baum daemon for its
version, process id and uptime over the daemon's socket, and never starts one. A config file that does not parse is shown
by position, and by key for a wrong type or value, and never by its text. Asking
gh for a token may wait up to 30 seconds for a keyring unlock prompt. `gh auth token` keeps the environment gh needs to find
its own configuration, as `baum generate` does.

It never prints a token or an API key, or a prefix, length or hash of one. For a
GitHub token it prints only where it was found (an environment variable's name,
the config file, `hosts.yml` or the keyring), and for `ANTHROPIC_API_KEY` only
whether it is set. It does print paths, tool versions and the `PATH` entries,
so look at a report before pasting it into a public issue.

## What `baum view` shows

`baum view` listens only on `127.0.0.1` and never contacts a server. Besides
the document, it shows the coding-agent sessions on your machine that it
linked to the pull request, and, when you open one, that session's full
transcript: your prompts, the agent's messages, and each tool call's input and
output, which can include file contents and command output. The transcript is
redacted with the engine `baum sessions --dump` uses, so secrets and e-mail
addresses are masked as described above, and your home directory is written as
`~` where it starts a path; paths outside it (a clone elsewhere, a Windows
home under `/mnt/c` in WSL) are shown as recorded, as are home paths glued
to other text (`-I/home/…/include`, a path after an escaped backslash), and
nothing else is removed. Only sessions linked to the pull request are served.

The server has no login. It refuses requests whose `Host` is not its own
address, which stops other websites in your browser from reading it, but any
other user or process on the same machine can request the port while it runs
and read everything it serves, transcripts included, even without permission
to read your `~/.claude` or other session stores. Do not run `baum view` on a
shared machine. The baum daemon, which serves the local application, adds the
separate authenticated boundary described below.

## Reading your clone without downloading anything

`baum sessions` and `baum view` read your clone with git but never use the
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
planted entry cannot widen the sessions read to your whole machine. An `Entire-Checkpoint` trailer in a commit is looked up on
the checkpoint branch already in your clone, in the private repository; a
checkpoint you have not fetched is not fetched for it.

## Agent isolation

The `claude` and `codex` backends run on your machine under
your user account, each in its own read-only mode, inside a temporary
checkout of the pull request. baum adds no container or sandbox of its own,
so isolation is only as strong as that tool's read-only mode. Repository
content and pull request text are untrusted input to the agent: a pull
request could try to persuade it to read files outside the checkout. Use
baum on pull requests you are prepared to have an agent read under your
account.

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

## Local repository registry

`baum repos` stores clone paths, git common directories, normalized GitHub
identities and generation preferences in a 0600 SQLite database beside the XDG
config file. Remote URLs and their credentials are never persisted or returned.
Registration and availability checks read local git metadata only; they do not
resolve tokens, clone, fetch or contact GitHub/model providers. Registration
starts nothing by itself, but automatic generation is on by default: once the
daemon has seen a registered repository's open pull requests, later new heads
and review requests can start paid runs (see "The baum daemon"). The registry
also holds the global preferences that control this.

The registry HTTP handler is mounted by the baum daemon behind its authenticated
Host/Origin boundary: its write routes can register local paths, remove records
and change settings. `baum view` exposes none of these routes.

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

The local application (the desktop app, or `baum ui` in a browser) is served
by the baum daemon, one process per user that keeps running in the background
after whatever started it exits. The desktop app starts it with `baum daemon
ensure`, as `baum ui` does; an agent or Git hook that runs baum starts it when
it is not running.

What it runs while it is up:

- the local application's server on `127.0.0.1` (described below) and a
  control socket only your user can open;
- every minute or so, free GitHub reads with your token for registered clones:
  open pull requests, the authenticated user and teams, author permissions,
  and the latest saved review's checks, conversation and head, with
  conditional requests and backoff (see "What baum sends where");
- once when it starts, the signed price list download;
- the record of the events agent and Git hooks report (above), and, for a
  user logged in to a baum server, the upload of each session an agent
  reports ended;
- generation jobs, the only thing that spends money: each runs the configured
  backend under your account, within the configured budgets, as
  `baum generate` does. Some you confirmed in the app with their price
  estimate; the others it starts by itself, unattended (below).

Automatic generation is on by default and runs without anyone watching. It
starts a job when a pull request in a registered repository that you authored,
or whose review is requested from you or one of your teams, gets a new head, a
new review request or is marked ready for review after automatic generation
was enabled for that repository; pull requests already open then, and drafts,
are left alone. It never starts
one for a pull request from a fork or by an author without known write access:
those run the agent on someone else's content and always need your
confirmation in the app. Each automatic job is prepared exactly like a
confirmed one (same backend, model, budgets, price list and recheck of the head
before any model call), and a model without a known price is never run
automatically. What it can spend: up to the configured per-run budget for each
new head (twice when a daemon stop or restart interrupts the first run, which
is retried once; failed or cancelled runs are not retried), with no daily limit unless you set a repository's daily cap, which
pauses that repository's automatic generation until the next day once reached.
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

It contacts a baum server only to upload ended sessions for a logged-in user,
as above. To stop it, quit the desktop app with "stop
background reviews" or run `baum daemon stop`: running jobs are cancelled and
recorded as interrupted (after at most 20 seconds), and nothing runs until
something starts it again. `baum daemon status` and `baum doctor` show whether
it runs, its version, process id and uptime. When a newer baum finds an older
daemon running jobs, it lets them finish: the daemon then starts the newer
`baum` executable that asked (a program any process of yours could start) and
exits. Only a daemon speaking an incompatible protocol is stopped at once.

Its state directory, `$XDG_STATE_HOME/baum/daemon` (default
`~/.local/state/baum/daemon`), is mode 0700 and holds the lock, `daemon.json`
(version, process id, start time, app address), the app credential and the
logs, which are rotated at 1 MiB with two older files kept. The logs hold
events, addresses and paths, never tokens, credentials or environment values.
It also holds the record of hook events (`events/events.jsonl`, 0600, rotated
at 1 MiB with two older files kept), the hook event spool, and `hooks.log`,
where hooks note what they spooled, dropped or started, never an event's
contents.

The control socket, `$XDG_RUNTIME_DIR/baum/daemon.sock` or `daemon.sock` in
the state directory, is mode 0600 in a 0700 directory, so other users cannot
connect; Windows uses the same `AF_UNIX` socket, protected by the state
directory's current-user DACL. Any process running as you can connect, ask
for the daemon's status and stop it. It can also send hook events, which the
daemon records; for a logged-in user an event reporting a session end makes
the daemon upload that session from your machine, as `baum upload` would.

The daemon does not use the environment of whatever started it. It runs your
login shell (`$SHELL -i -l -c`, without a terminal, for at most 10 seconds)
with only session variables and reads the environment your startup files set,
then keeps only `PATH`, locale, proxy and certificate settings, the SSH agent
socket and ssh command git uses, the desktop session bus and display,
GitHub tokens (`GITHUB_TOKEN`, `GH_TOKEN`, `GH_ENTERPRISE_TOKEN`,
`GITHUB_ENTERPRISE_TOKEN`) and gh settings, `ANTHROPIC_API_KEY`,
`CLAUDE_CONFIG_DIR`, `CODEX_HOME` and `BAUM_*`. A token set only in an agent's
environment is therefore not used. Your home, XDG and temporary directories
come only from the starting environment, never from the shell, because they
decide which state directory, lock and job journal the daemon uses. The SSH
agent socket is the starting environment's, else the shell's; it is used only
to fetch a missing pull request commit from your clone's remote, as `baum
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
Keep the URL `baum ui` prints and that file private. Processes running as your
user can read that file and access the API.

The state directory must be mode 0700 and owned by you on Unix; Windows uses a
protected current-user DACL. Clients never send the capability anywhere: they
read it from the file and open it on the loopback address it names. SQLite OS
locking keeps one daemon per user and releases the lock after process exit.

Document selectors must name a registered clone. Linked transcripts use the
same redaction and linked-session allowlist as `baum view`; file patches are
restricted to cited paths. Settings/diagnostics omit provider and GitHub keys.
`GET /api/diagnostics` serves the `baum doctor` report behind the same
authentication, Origin and Host checks, runs the same bounded commands as
`baum doctor` but never the price-list fetch, and is limited to `GET`.
Stored image APIs return only document-listed, content-checked PNG, JPEG, GIF
or WebP copies. They reject SVG/HTML and never follow document URLs. Images
carry nosniff and a sandboxed, deny-all content security policy. Local successful
versions retain their captured images atomically alongside the document;
removing a registry entry does not delete that history.

The authenticated job API derives repository, head, fork and author permissions
from registered clones and fresh GitHub evidence. A five-minute estimate is
bound to the head, selected model/backend, budgets and price list version;
confirmation cannot authorize changed inputs. Forks, non-writers and unknown
permissions require a separate single-PR isolation acknowledgement and are
excluded from batch generation. Cancellation reaches the existing backend
process-group cleanup. Job history contains fixed failure reasons and bounded
usage metadata, never arbitrary provider errors, prompts or rejected documents.
The private journal persists queued/running/terminal states; crashed running
entries become interrupted before work resumes. The stored copy of a downloaded
price list is trusted only after its signature verifies again on load. Only
the daemon that owns the per-user lock opens the queue, so there is never a
second one.

The server's bootstrap page exchanges the fragment token and clears it from
history before the embedded local application loads; the application's files
are served only under the authenticated namespace, and every application
request resolves under it. Every application request is same-origin; the app has no hosted baum API dependency. Review content uses the
same sanitizers and safe rendering components as the hosted PR page and `baum
view`. Stored document rendering failures remain within the PR pane. The app
never supplies approvals, merge actions or discussion mutation controls.
