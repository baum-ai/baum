# Security policy

## What baum sends where

baum is local-first, but generating a review requires a model. When you
run `baum generate`, the following is sent to the **model provider you
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

baum sends no telemetry. Besides GitHub and the configured provider, it
contacts only `static.usebaum.com`, to download the public price list used
for cost estimates. That request is a plain download: it carries no token,
repository, pull request or session data, and the list's signature is checked
against a key built into the binary. When the list cannot be fetched or
verified, baum uses a copy embedded at release. Tokens are read from the
environment, `gh auth token` or the 0600 config file and are never written
anywhere else. Local inbox discovery also reads GitHub's REST API using that
same token and derived host, without redirects or extra scopes. Its private
0600 cache contains minimal inbox metadata and ETags, partitioned by a credential
fingerprint and repository. Tokens, PR bodies and signed image URLs are never
put in that cache. Cached and offline results are marked stale; unreadable
author write permissions remain unknown, rather than granting automatic work.

## What `baum doctor` runs and prints

`baum doctor` diagnoses the machine and starts no model. It runs
`<tool> --version` for `git` and `gh`, each with a timeout, in
a process group that is killed whole when the timeout passes, and with the
filtered environment of the local backends, which drops provider keys and
GitHub, cloud and SSH-agent credentials, never the whole of baum's. The tool's
path comes from `backend_paths`, `GH_PATH` or `PATH`;
the same checks `baum generate` runs for `claude`, `codex` and `cursor-agent`,
which include codex's read-only sandbox preflight (a sandboxed `cat` and a
blocked write in a temporary directory it removes) and `cursor-agent --help`
and `--version`, and for `anthropic`, which only looks at
whether `ANTHROPIC_API_KEY` is set and sends it nowhere; and, only when no token comes from the
environment, the config file or `gh`'s own files, `gh auth token
--secure-storage --hostname <host>`. Its one network contact is the signed price
list described above, skipped with `--offline`; nothing else leaves the machine.
It creates no files of its own: it opens the registry read-only (SQLite
`mode=ro`, so no journal, table or file mode changes) and only when it exists,
and does not create a review store. A config file that does not parse is shown
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
shared machine. `baum ui` adds the separate authenticated boundary described below.

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

The `claude`, `codex` and `cursor-agent` backends run on your machine under
your user account, each in its own read-only mode, inside a temporary
checkout of the pull request. **`cursor-agent` cannot be held to read-only as
reliably as the other two**; see [below](#the-cursor-agent-backend). baum adds no container or sandbox of its own,
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
  switches and any tool baum does not know. The CLI reports a call as it
  starts, so this limits what a stray call can do; it does not prevent it.
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
  home directory may be read as instructions. MCP servers you approved before
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

## Local repository registry

`baum repos` stores clone paths, git common directories, normalized GitHub
identities and generation preferences in a 0600 SQLite database beside the XDG
config file. Remote URLs and their credentials are never persisted or returned.
Registration and availability checks read local git metadata only; they do not
resolve tokens, clone, fetch or contact GitHub/model providers. Automatic
preferences default off and registration does not start any background work.

The registry HTTP handler is mounted by `baum ui` behind its authenticated
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

## Authenticated local application

`baum ui` accepts only the bound `127.0.0.1:port` Host. Its bootstrap has no
local data; every API requires the random private launch capability or an
HttpOnly SameSite=Strict cookie obtained by exchanging that capability. The
browser receives the capability in a URL fragment, removes it from history,
and sends it in the same-origin JSON exchange body. Browsers send cookies to
every port of a host, so the cookie is scoped to a second random secret path,
`/s/<namespace>/`, returned only by the exchange; another service on a
loopback port never receives it, and the cookie authorizes nothing outside
that path. All mutations require the
exact local Origin and JSON content type, including requests using Bearer
authentication. No route enables CORS. The capability is valid only while the
instance runs; keep the printed URL and its mode-0600 instance file private.
Processes running as your user can read that file and access the API.

The per-user instance directory must be mode 0700 on Unix; Windows uses a protected current-user DACL. A second invocation verifies
the existing instance using its capability before opening it, without sending
that capability to a redirect, proxy, non-loopback host or baum server. SQLite
OS locking prevents concurrent owners and releases locks after process exit.

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
price list is trusted only after its signature verifies again on load. A second `baum ui` invocation
cannot open a second queue because it reuses the existing instance first.

The server's bootstrap page exchanges the fragment token and clears it from
history before the embedded local application loads; the application's files
are served only under the authenticated namespace, and every application
request resolves under it. Every application request is same-origin; the app has no hosted baum API dependency. Review content uses the
same sanitizers and safe rendering components as the hosted PR page and `baum
view`. Stored document rendering failures remain within the PR pane. The app
never supplies approvals, merge actions or discussion mutation controls.
