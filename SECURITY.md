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
  masked with [goredact](https://github.com/baumai/goredact).
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
shared machine. `baum ui` will add token authentication.

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

## Reporting a vulnerability

Report suspected vulnerabilities privately, not through a public issue or
pull request:

- Email: **security@lastpersonlabs.com**

Include a description of the issue and its impact, steps to reproduce or a
minimal test case, and the affected version or commit. We acknowledge reports
as quickly as possible and coordinate a fix and disclosure timeline with you.

Do not include real secret values or unredacted session text in a report. Use
synthetic values that reproduce the shape of the problem. If a real secret was
exposed while triggering the issue, treat it as compromised and rotate it.

## Scope

This policy covers the baum repository. It does not cover the model
providers, GitHub, or the coding-agent tools whose sessions baum reads.
