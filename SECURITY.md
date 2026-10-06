# Security policy

## What baum sends where

baum is local-first, but generating a review requires a model. When you
run `baum generate`, the following is sent to the **model provider you
configure** (the Anthropic API, or the `claude`, `codex` or `cursor-agent` CLI,
each of which talks to its own provider under your own account):

- content of the repository at the pull request head, including the diff and
  any file the review agent chooses to read;
- pull request metadata, commits, checks and conversation fetched from GitHub;
- text from your local coding-agent sessions that were linked to the pull
  request: prompts, paraphrased commands and touched paths, **after**
  redaction of secrets and identities with
  [goredact](https://github.com/lastpersonlabs/goredact).

Redaction is best effort. Review `baum bundle` output before using
`generate` on a repository whose sessions may contain sensitive material, and
do not run it against repositories you are not allowed to share with your
provider.

baum sends no telemetry. Besides GitHub and the configured provider, it
contacts only `static.usebaum.com`, to download the public price list used
for cost estimates. That request is a plain download: it carries no token,
repository, pull request or session data, and the list's signature is checked
against a key built into the binary. When the list cannot be fetched or
verified, baum uses a copy embedded at release. Tokens are read from the
environment, `gh auth token` or the 0600 config file and are never written
anywhere else.

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
