# Security policy

## What baum sends where

baum is local-first, but generating a review requires a model. When you
run `baum generate`, the following is sent to the **model provider you
configure** (the Anthropic API, or the `claude`, `codex` or `cursor-agent` CLI,
each of which talks to its own provider under your own account):

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
anywhere else.

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
and sends it in the same-origin JSON exchange body. All mutations require the
exact local Origin and JSON content type, including requests using Bearer
authentication. No route enables CORS. The capability is valid only while the
instance runs; keep the printed URL and its mode-0600 instance file private.
Processes running as your user can read that file and access the API.

The per-user instance directory must be mode 0700. A second invocation verifies
the existing instance using its capability before opening it, without sending
that capability to a redirect, proxy, non-loopback host or baum server. SQLite
OS locking prevents concurrent owners and releases locks after process exit.

Document selectors must name a registered clone. Linked transcripts use the
same redaction and linked-session allowlist as `baum view`; file patches are
restricted to cited paths. Settings/diagnostics omit provider and GitHub keys.
Stored image APIs return only document-listed, content-checked PNG, JPEG, GIF
or WebP copies. They reject SVG/HTML and never follow document URLs. Images
carry nosniff and a sandboxed, deny-all content security policy. Local successful
versions retain their captured images atomically alongside the document;
removing a registry entry does not delete that history.
