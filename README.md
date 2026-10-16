# baum

[![CI](https://github.com/baum-ai/baum/actions/workflows/check.yml/badge.svg)](https://github.com/baum-ai/baum/actions/workflows/check.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

baum is a local-first pull request review generator and viewer. One
binary on your machine builds a `baum.review/1` document for a pull
request and serves an embedded viewer for it on localhost.

Everything runs locally:

- coding-agent sessions (Claude Code, Codex, Cursor, Entire) are read from your
  home directory and redacted before anything leaves the machine;
- the diff and checkout come from your local git clone;
- pull request metadata, checks and conversation come from GitHub with your
  own token;
- the review itself is written by the model backend you configure: the
  Anthropic API, or the `claude`, `codex` or `cursor-agent` CLI already on
  your `PATH`.

## Status

The repository is being stood up phase by phase. The CLI skeleton, config
layout, review schema, release pipeline, `baum sessions`, `baum validate`,
`baum bundle`, `baum generate`, `baum view`, `baum repos` and `baum ui` exist;
`refresh` prints its help
and exits non-zero until its phase lands. `generate` stores immutable local
versions and reuses the previous version for section and risk history.

## Install

With Go:

```sh
go install github.com/baum-ai/baum/cmd/baum@latest
```

Or download an archive for linux, macOS or Windows from the
[releases page](https://github.com/baum-ai/baum/releases). Binaries
are built with `CGO_ENABLED=0` and have no runtime dependencies beyond `git`
2.29 or newer.

## Commands

```sh
# Register a clone and start the authenticated local application API.
baum repos add /path/to/clone
baum ui

# Build the review document for pull request 191 of the repository in the
# current directory and write it to review.json (see "baum generate" below).
baum generate --pr 191 -o review.json

# Open or validate the latest local version, or select an earlier version.
baum view --pr 191
baum view --pr 191 --version 1
baum validate --pr 191

# Assemble the generation input without calling a model, for debugging. Without
# --pr it is the pull request of the current branch; --repo owner/name replaces
# the repository the origin remote names. Only the bundle JSON goes to stdout
# (or to -o); warnings and the bundle digest go to stderr. The clone is only
# read: nothing is checked out or fetched, so a missing commit is reported with
# the `git fetch` to run.
baum bundle --pr 191 -o bundle.json

# Serve the viewer for a document on localhost and open it in the browser.
# The URL is always printed. Nothing leaves your machine: the server listens on
# 127.0.0.1 only and never fetches anything.
baum view review.json

# Reload the viewer whenever the file changes, for example while editing it.
baum view review.json --watch

# Try the viewer on the built-in example document.
baum view --sample

# Print the URL without opening a browser, on a fixed port.
baum view review.json --no-open --port 4173

# Check a stored document against the schema and its own rules. Findings
# print to stderr, one per line, and the exit status is non-zero when there
# are any.
baum validate review.json

# Also check it against the bundle it was generated from: hunk line ranges,
# section outlines, evidence against checks, and restricted sessions.
baum validate review.json --bundle bundle.json
```

### Pull request discovery

The internal discovery service supplies the local UI with open pull requests
for each registered GitHub identity. It records the head SHA, author, draft and
fork flags, direct or team review requests for the token owner, and the author's
repository write permission. Unreadable author permissions stay null; unreadable
team membership is explicitly marked unknown. Existing token permissions are
used as they are; baum never requests additional scopes.

Discovery uses read-only REST requests, with the same host and token routing as
bundle generation. Every page is conditionally fetched with its ETag. Polling
starts at 30 seconds and backs off to 15 minutes when unchanged, honoring a
longer GitHub polling interval, rate-limit reset or Retry-After. Rate budgets
and retry deadlines are shared across repositories for the same host and
credential, and persist across restarts. Rate remaining
and reset time are returned with the snapshot; a network or required API failure
keeps the last complete inbox and marks it stale with a diagnostic.

A private cache under `$XDG_CACHE_HOME/baum/discovery` (the OS cache directory
when unset) lets the UI load the last inbox without network access. Files have
mode 0600 and are partitioned by host, credential fingerprint and repository;
a changed token cannot reuse another account's inbox. The cache stores only
inbox metadata, conditional response fields and polling state, never tokens,
PR bodies or signed image URLs. Cached reads are explicitly stale until a
successful poll. This service neither fetches git objects nor generates reviews.

### Registered repositories

`baum repos add <path>` registers an existing GitHub clone or linked worktree;
`baum repos list` shows its canonical path, common git directory, GitHub identity,
availability and settings (`--json` emits structured records). Registration reads
local git metadata only: it never clones, fetches, calls a model or starts a job.
`baum repos remove <path-or-id>` works even after a clone disappears.

Records live in `repos.sqlite` beside the XDG `config.toml`, with mode 0600.
Independent clones and linked worktrees keep separate IDs; repository settings
are shared by normalized GitHub identity (`owner/name` on github.com, `host/owner/name` on
GitHub Enterprise), matching the review version store.
Origin is preferred when it names GitHub; otherwise other remotes are considered
in name order. Missing paths and paths that now name a different repository or
common git directory remain listed as unavailable. Restoring the same repository
at the same canonical paths makes it available again. Re-add a moved clone at its
new path and remove its old record explicitly.

Automatic generation defaults **off**. `repos add` accepts `--automatic=true`,
`--daily-cap <USD>`, `--clear-daily-cap`, `--backend <name>` and `--model <name>` to edit repository
settings; omitted flags preserve existing settings. A cap must be finite and
nonnegative; zero allows no automatic spending, and an unset cap has no
repository override. `--clear-daily-cap` removes the override so the repository
inherits again; it cannot be combined with `--daily-cap`. Empty `--backend=` or `--model=` resets that override to
inherit the resolved global configuration. These settings are stored for the
future job runner; this command does not schedule or generate reviews.

The registry also provides an internal HTTP handler for `GET/POST /api/repos`,
`DELETE /api/repos/{id}`, and `PUT /api/repos/{id}/settings`. POST takes
`{"path":"/clone","settings":{...}}`; omitting settings preserves them. PUT
replaces all repository settings; null backend/model/cap clears an override.
`baum ui` mounts this handler behind authentication and Host/Origin checks.
`baum view` does not expose these routes.

### baum generate

```sh
baum generate [--pr N] [--repo owner/name] [--backend auto|claude|codex]
              [--model M] [-o review.json] [--bundle-out bundle.json]
              [--diagnostics outcome.json] [--keep-run-dir] [--store directory]
```

`generate` assembles the same bundle as `baum bundle`, unpacks the pull
request's head and merge-base files into a private temporary directory
(`baum-agent-run-*`, mode 0700, removed when the command ends, also on Ctrl-C),
and has a coding agent installed on your machine read them and write the
review. Git objects, refs, index and working tree are only read, as for
`baum bundle`; nothing is checked out or fetched. The local version store is
written under the shared git directory, or the fallback/override below. The agent runs under your own login with its
read-only mode, as described in [SECURITY.md](SECURITY.md#agent-isolation);
baum sends it no credential of its own.

`--backend auto` (the default, or `backend` in the config file) uses `claude`
when it is installed and passes its checks (a recent enough version), and `codex`
otherwise (recent enough, and its read-only sandbox starts on this machine).
`--backend claude` and `--backend codex` use exactly that one. `--model` names
a model of one backend, so with `auto` it applies to `claude` only: when `auto`
falls back to `codex`, codex uses its own default model, and stderr says why
claude was skipped. The `anthropic`
and `cursor-agent` backends are named by the config but are not available in
this version: asking for one fails, and `auto` skips them.

The document goes through the same steps as a hosted one: the model's output is
repaired against the validator (two repairs at most), secrets and e-mail
addresses are masked, a secret written into an identifier rejects the run,
fields baum owns (the summary's title and author, statistics, the index, the
generator block with the model, prompt version `review-public-v2` and schema
revision) are derived, and the stored form is checked strictly. A document that
fails any step is not written. Hunks may be shared by sections or left
unassigned; `generate` does not require each hunk to appear exactly once.

Every successful generation is saved before its document is printed or exported.
The store is `<common git directory>/baum/reviews/versions.sqlite`, shared by
all linked worktrees. If a new store cannot be created because the git directory
is read-only, it uses `$XDG_DATA_HOME/baum/reviews/<clone digest>/versions.sqlite`
(`~/.local/share` when XDG_DATA_HOME is unset or relative). `--store directory`
selects an explicit store root. Repository names (including the GitHub host) and
pull request numbers isolate histories even in a shared override directory.

Publication is one SQLite transaction: the completed document and metadata
(revisions, bundle digest, backend/model, schema/prompt/policy identities and
generation timing/usage) appear together. Earlier versions remain immutable.
A second generation supplies the previous document to the shared history logic,
which retains section IDs, stale sections and risk introductions/mitigations.
Concurrent runs may both generate from the same version; only one publishes,
and the other fails with a conflict asking you to generate again. Generation
failures and cancellation before publication publish no version. A later stdout
or export failure leaves the successfully generated version stored. An existing store is always preferred,
even if read-only; a write then fails rather than silently hiding its history.
A fallback store continues to be used if the clone later becomes writable.

Only the document goes to stdout, or to `-o`, which is written atomically with
mode 0600. Progress goes to stderr: the bundle digest, the backend and its
version, each pass and repair, then the calls and tokens spent (or "usage not
reported") and the wall time. Afterwards it prints `view it with: baum view
review.json` for an exported file, and `baum view --pr N` for the stored version.
`--bundle-out` also writes the exact bundle the agent was given, and
`--diagnostics` writes how the run ended (`ok` or `failed`, the failure's
category and message) with its redacted outcome (usage, repairs, findings and at
most 512 bounded events), also when the run fails, including an early failure
such as a missing commit or an unavailable backend; only a command-line error
writes none. `--keep-run-dir` keeps the run directory and prints its path: it
can hold rejected documents (`rejected-N.json`) that were masked only in part,
and its trees are read-only, so run `chmod -R u+w` on it before `rm -rf`. Budgets are the config file's `run_timeout`
(wall clock for the run), `pass_timeout` (each model call; a call that
exceeds it is not retried), `max_calls` and `max_tokens`.

Exit status:

| Code | Meaning |
| --- | --- |
| 0 | the document was written |
| 1 | any other failure, including an internal one such as masking the output failing (the hosted worker also reports that as a plain failure, not a rejected document) |
| 2 | input: bad flags, no such pull request, a commit missing from the clone, a bundle or checkout over its caps, GitHub unreachable |
| 3 | backend: not installed, too old, its sandbox cannot start, or the provider was unavailable after retries |
| 4 | budget: model calls, tokens, a model call's `pass_timeout` or the run's wall clock were spent |
| 5 | validation: the document was still invalid after the repairs, or held a secret in a closed value |
| 130 | interrupted by SIGINT or SIGTERM |

`baum view` serves a document with schema or rule findings anyway and prints
the findings to stderr, as `baum validate` does. The document must be valid
JSON of at most 32 MiB. `baum view --pr N` and `baum validate --pr N` read the
latest local version from the clone in the current directory, without GitHub
access. `--version N` selects a historical version. `--repo owner/name` and
`--store directory` select the same repository and store used by generation;
these flags require `--pr`. Reads do not create a store. A missing version
reports that a review must first be generated. `--watch` requires a document
file; stored versions are immutable. The viewer opens the selected version;
its browser version picker is not connected to the local store yet.

From local data only, `baum view` can also show the coding-agent sessions
linked to the pull request. When the viewer asks, `baum view` reads the clone
containing the current directory, but only if one of its remotes is the
document's repository (`owner/name`, whether the remote is HTTPS or SSH), and
only if the clone already has the pull request's head and merge base (or base)
commits; abbreviated commit ids in the document are expanded in the clone. A
fork clone, whose `origin` is the fork and whose `upstream` remote is the pull
request's repository, matches. It reads the
sessions recorded on this machine, as `baum sessions` does, but only those last
written within two days of the pull request's first commit, and links them to
those commits the way the hosted service does: a commit can link to several
sessions when each edited part of it, and a weak guess is dropped once the
commit has a confident link. A commit whose `Entire-Checkpoint` trailer names
a checkpoint the clone's checkpoint branch holds is linked to the sessions that
checkpoint recorded first; the matcher then looks only for the files those
sessions did not edit.
Only sessions linked to at least one commit are shown, and all their text is
redacted like `baum sessions` output. It never fetches, not even in a partial
clone: a clone without the
commits, a clone with no remote for the repository, a directory outside a clone and `--sample` show
no local sessions, with the reason, and the viewer falls back to the sessions
in the document. Run `git fetch` yourself to make missing commits available,
then reload the viewer. Run `baum view` from inside the clone.

The same clone fills in code excerpts the document left out. Opening a cited
hunk's evidence asks `baum view` for that file's patch between the merge base
(else the base) and the head, only for files the document cites, read with the
same private, no-fetch reads and the same clone matching. A binary or very
large file (over 1 MiB), a submodule, a file the range does not change and a clone
without the commits keep the link to GitHub instead. A renamed file is read
under its old name too, as the hosted service reads it.

Opening one of those linked sessions shows its full transcript, with the same
session card the hosted app uses, as a conversation: every prompt with the
agent's replies and what it caused, and per prompt the agent's messages and
its tool calls with their input and output.
It is read only when you open the session, redacted exactly as
`baum sessions --dump` redacts it (secrets and e-mail addresses masked), with
your home directory written as `~`, and served only to the page on
`127.0.0.1`. A very long transcript is shown in part, and the page says so: a prompt is
cut at 32 KiB, an agent message at 16 KiB, a tool call's input and output at
4 KiB each, and once the response reaches 4 MiB the rest is left out. Paths
outside your home directory are shown as recorded. `?pane=session&session=<id>&turn=<n>` opens it at a prompt.

`baum refresh` updates checks, threads and session evidence without a
model call. Run any command with `--help` for its flags.

### Sessions

```sh
# Sessions recorded on this machine for the repository in the current
# directory: Claude Code, Codex and Cursor from their home-directory stores,
# Entire from the clone's checkpoint branch.
baum sessions

# Only the last three days, as JSON, including sessions recorded when the
# repository lived somewhere else.
baum sessions --since 72h --json --previous-path ~/old/checkout

# The normalised session graph of one session, redacted, for debugging.
baum sessions --dump <session-id>
```

`--dump` prints valid JSON: secrets and e-mail addresses are found over the
whole graph and in each of its strings as decoded, and masked only inside its
strings.

A session belongs to the repository when it ran in any working tree of the
clone: the main checkout or any of its `git worktree` checkouts, including
ones since deleted, and directories inside them. For an ordinary clone and its
worktrees, `baum sessions` and `baum view` give the same answer from any of
them. A directory that merely shares a name prefix (`~/src/repo-other` beside
`~/src/repo`) is not part of it, but a separate repository nested inside a
checkout is, because sessions are placed by directory. A worktree entry is
used only when its directory points back at this repository (or is gone), and
never when it is the file system root, your home directory, or a directory
above the repository. In a clone made with `--separate-git-dir`, and in a
submodule, git may report the main working tree at an unexpected place, so the
answer can depend on where you run. Each Entire session is listed once, from
the newest checkpoint holding its transcript (a checkpoint can hold only the
prompt), and not at all when its agent's own store already has it.

Titles, prompts and every other text printed go through the same redaction
as the review bundle: secrets and e-mail addresses are masked in place.

The generation bundle's session evidence, which `baum bundle` writes, is
built from the sessions `baum view` links. Each session carries its title, branch, models, timing and outcome, its first
and latest prompts with the agent's final reply, the files inside the clone
each prompt read or edited (subagents' work counts toward the prompt that
started them), and its shell commands as paraphrases: program name, a
category such as `test` or `build`, a target only when an argument is a
path-shaped repository path that the pull request changes, the session
touched, or that exists in the clone, and whether the command succeeded,
failed or was interrupted. Command lines, their output, inline code, commit messages and
paths outside the clone are never included, and all text is redacted before
the bundle is trimmed or written. Sessions of baum's own generation runs are
left out, from the bundle and from `baum view` alike.

## Configuration

baum reads `$XDG_CONFIG_HOME/baum/config.toml`, which defaults to
`~/.config/baum/config.toml`. The file is optional and is created with
mode 0600 when written.

```toml
backend = "auto"          # auto | anthropic | claude | codex | cursor-agent
model = ""                # backend default when empty
github_token = ""         # optional; see the precedence below

[budgets]
pass_timeout = "10m"
run_timeout = "45m"
max_calls = 40
max_tokens = 2000000

[caps]
# Overrides for the generation bundle caps, by name. Defaults apply otherwise.
# max_sessions = 12
```

The caps are `max_sessions` (12), `max_prompts_per_session` (2),
`max_command_events_per_session` (20), `max_comments` (40),
`max_hunk_patch_lines` (200), `max_bytes` (262144; the encoded bundle apart
from the diff index), `max_diff_index_bytes` (524288), `max_diff_files` (3000),
`max_diff_patch_bytes` (104857600), `max_images` (20), `max_image_bytes`
(10485760), `max_image_alt_bytes` (512), and the code checkout's
`checkout_max_file_bytes` (1048576), `checkout_max_total_bytes` (209715200) and
`checkout_max_total_files` (20000). The defaults are the hosted service's, so
the same pull request is trimmed the same way in both places. An unknown name
or a negative value is an error. Trimming cuts text in the documented order
and says so in the bundle's `truncated` notes; it never drops a hunk from the
diff index, and a diff index over its budget fails the command instead.

Precedence is flags, then environment, then the file, then defaults. The
environment variables are:

| Variable | Purpose |
| --- | --- |
| `GITHUB_TOKEN`, `GH_TOKEN` | github.com token, also used for `*.ghe.com` tenants as the GitHub CLI uses it; when neither is set and the file has none, the GitHub CLI's token is used |
| `GH_ENTERPRISE_TOKEN`, `GITHUB_ENTERPRISE_TOKEN` | GitHub Enterprise Server token, read as the GitHub CLI reads it and only ever sent to an Enterprise host the GitHub CLI knows (logged in with `gh auth login`, or named by `GH_HOST`) |
| `ANTHROPIC_API_KEY` | key for the `anthropic` backend |
| `BAUM_BACKEND` | backend name |
| `BAUM_MODEL` | model name |

For `baum bundle`, baum reads the pull request, its
commits, checks and statuses, comments, reviews and review threads, and the
base branch's protection from GitHub's GraphQL API, through the GitHub CLI's
library. It works with github.com, GitHub Enterprise Cloud and GitHub
Enterprise Server, and sends a token only to the host it belongs to: the
variables and file above are for github.com, and any host's token can come
from the GitHub CLI (`gh auth login`). An `api_host` override in the GitHub
CLI's `hosts.yml` is not honoured. A token that cannot read part of it
(a fine-grained token without checks access, say) leaves that part out of
the bundle, recorded as unavailable in its `sources`; a check's `required`
stays unknown unless the protection is readable. The repository is
`--repo` or the one `origin` names, and the pull request `--pr` or the one
for the current branch. See [ARCHITECTURE.md](ARCHITECTURE.md#github-source).

`backend = "auto"` picks the first of `claude`, `codex` and `cursor-agent`
found on `PATH`, and falls back to `anthropic` when `ANTHROPIC_API_KEY` is set.

baum sends no telemetry and talks to no server other than GitHub, the
model provider you configure and `static.usebaum.com`, from which it
downloads the signed price list for cost estimates. See
[SECURITY.md](SECURITY.md) for what is sent where.

## Review document schema

Documents follow the `baum.review/1` JSON Schema in
[`schema/review-1.json`](schema/review-1.json), published at
<https://static.usebaum.com/schemas/review/1.json> (an identifier; baum never
fetches it); a test validates the
example document next to it. The hosted baum service reads the same file, and
baum supports every field it defines.

## Packages

The Go packages other tools may import sit at public paths; only the CLI's
own plumbing is under `internal/`. [ARCHITECTURE.md](ARCHITECTURE.md)
describes how they fit together and what a host implements.

| Package | What it does |
| --- | --- |
| `agent` | the model backend interface and the Claude Code and Codex backends, the generate-and-repair loop (`Generator`) with its `Prompts` interface, repair text and output redaction, the run budgets and the environment allowlist for local CLI backends |
| `agent/prompt` | the public review prompt (`review-public-v2`) this module ships, with a submit block per backend |
| `bundle` | the evidence bundle types a generation run reads, the diff index (hunk IDs, roles, diff URLs) built from a comparison, the `REVIEW.md` loader, reference extraction and bot flags, the code checkout archive, description image handling, the paraphrase of a session's shell commands, and the caps, input redaction, byte trimming and digest applied to a bundle |
| `review` | the review document types, the strict, lenient and stored schema views, normalisation, validation with located findings, derived fields, version history, model-free refresh and the tile catalogue (`review/tilecatalogue`) |
| `sessions` | finds the sessions recorded for a repository and maps them into the shared `session.Graph` model |
| `sessions/collector` | discovers Claude Code, Codex and Cursor stores in the home directory and Entire checkpoints in the clone |
| `sessions/{claude,codex,cursor,entire}*` | one parser and mapper per vendor, with golden tests |
| `linkage` | matches commits of a pull request to the sessions that produced them |
| `linkage/sessioncorrelate` | extracts commit refs, branches and touched files from a session graph, and makes a file touch's path repository-relative |
| `redact` | masks secrets and e-mail addresses with [goredact](https://github.com/baum-ai/goredact), and removes e-mail addresses and @mentions from text bound for a code host |
| `githubapi` | the GitHub GraphQL transport that recognises primary and secondary rate limits, and GitHub's GraphQL scalars |
| `schema` | the review schema and its validator |

[`viewer/`](viewer/README.md) is a pnpm package of the React components
that render a review document and its versions, which the hosted service
imports from this repository too.

## Development

Requires Go (version in `go.mod`), [just](https://just.systems) and
[golangci-lint](https://golangci-lint.run). `just check` runs formatting,
vet, build, tests and lint; `just --list` shows the other recipes. The
viewer needs Node.js and pnpm; `pnpm install && pnpm check` in `viewer/`
runs its lint, format check, typecheck and tests. See
[CONTRIBUTING.md](CONTRIBUTING.md).

The viewer that `baum view` serves is committed, so `go install` needs no
Node.js: `internal/viewer/dist` holds the built app and
`viewer/src/precompiled` the precompiled tile validators. `just viewer-build`
rebuilds both, and `just viewer-fresh` rebuilds them and fails, listing the
changed paths, when they differ from what is committed. CI fails on a stale
bundle, the release snapshot and the release wait for that check, and the
goreleaser before hook runs it too. `just release-snapshot` builds the five release archives into
`dist/`, and `just release-verify` checks that each binary was built with CGO
off and embeds the committed bundle. `just clean-install` runs
`scripts/clean-install.sh`, which installs `baum` from a module zip of the
committed files with no Node.js on `PATH` and smoke tests `baum view --sample`.

This repository is published from the `oss/` directory of the private baum
monorepo, where the hosted service imports these packages. Pull requests are
reviewed and merged here as usual and then pulled into the monorepo.

## License

MIT, see [LICENSE](LICENSE).

### Local application server

`baum ui` starts the local application API on `127.0.0.1` at a free port.
`--port 8080` selects a port; `--no-open` prints the private launch URL without
opening a browser. Keep the process running until you finish; Ctrl-C stops it.
A second invocation opens the existing instance, even if another port was
requested. The initial page currently confirms API readiness; the inbox SPA
is a separate interface over these endpoints.

The URL carries a random capability in its fragment, which the bootstrap
removes from browser history before exchanging it at
`POST /api/auth/exchange` (`{"token":"<capability>"}`). The response is
`{"path":"/s/<namespace>/"}` and an HttpOnly, SameSite=Strict session cookie
scoped to that path; the bootstrap then replaces itself with that path. The
namespace is a separate random secret: browsers send cookies to every port on
`127.0.0.1`, so the cookie only travels to URLs under the namespace, which
other loopback services cannot guess. In the browser, the application and its
API live only under `/s/<namespace>/`: `/s/<namespace>/api/...` and
`/s/<namespace>/api/events` reach the endpoints below, and other `GET` paths
there serve the application's files. A browser application should resolve
API URLs relative to its namespace (`api/repos` from `/s/<namespace>/`, or
the first two segments of `location.pathname`). The cookie authorizes nothing
outside the namespace, and an unknown namespace is 404.

The capability remains valid for the lifetime of this instance so subsequent
invocations and a desktop host can authorize a new browser. It is kept only in
the private per-user instance file and printed launch URL; the namespace is
never printed or stored. API clients may send `Authorization: Bearer
<capability>` to the unprefixed `/api/...` paths instead. All mutations,
including token exchange, require the exact local `Origin` and
`Content-Type: application/json`; no CORS access is enabled. Only the exact
bound `127.0.0.1:port` Host is accepted.

The instance lock and 0600 capability file live under
`$XDG_RUNTIME_DIR/baum`, or `$XDG_STATE_HOME/baum` (default
`~/.local/state/baum`). That directory must be private (0700 on Unix; a protected current-user DACL on Windows). The OS releases
the SQLite instance lock after a crash; stale capability files are replaced
only by the next lock owner. The desktop host can use the same start/lock and
exchange APIs without opening a browser.

Authenticated endpoints include `/api/repos` and repository settings,
`/api/prs`, `/api/index`, `/api/document`, `/api/versions`, `/api/sessions`
(and `/api/sessions/{id}`), `/api/commits`, `/api/patch`,
`/api/attachments/{id}`, `/api/jobs`, `/api/settings` and `/api/diagnostics`.
Document, version, image and linkage endpoints require `?clone=<registry id>&pr=N`
with optional `&version=N` (default latest); paths cannot select arbitrary
files or unregistered clones. Settings and diagnostics never return tokens.
`baum ui --concurrency N` controls concurrent generation (default 1, maximum 16).
The persistent queue lives in `$XDG_STATE_HOME/baum/jobs.json` (default
`~/.local/state/baum/jobs.json`), mode 0600. Jobs remain individually visible;
restarting marks unfinished running jobs interrupted and resumes queued jobs.
Only one queued or running job exists per repository/PR across registered clones.
Confirming the same inputs again returns the queued job. Any other confirmed
input (head, backend, model, budgets, caps or price list version) replaces
queued work; running work must finish or be cancelled first. Cancelling propagates to the backend process group and removes
its temporary checkout and archives. Failed, cancelled and interrupted attempts
never publish a partial version.

`POST /api/jobs/estimate` accepts `{items:[{clone,pr}],mode:"single"|"batch"}`.
An estimate expires after five minutes. `POST /api/jobs` requires its
`estimate_id` and `confirm_estimate:true`; it rechecks head, backend, model,
budgets and price version before enqueueing. Execution checks them again and
refuses changed inputs before a model call. An explicit model with known rates
is required; unknown backend default models have no invented estimate.
Estimates use the signed published full price list, falling back to the embedded
release baseline if download or verification fails. A downloaded list that
becomes active is stored with its signature beside the queue, in
`prices.signed.json`; on restart it is verified again and reinstalled before
queued jobs resume. A queued job confirmed against a newer list than the active
one downloads the signed list once more before its input check. The estimate uses the
highest listed charged-token rates and reserves one budget-sized cache read per
call. It is a planning estimate, not a hard dollar ceiling: cached reads are
outside the charged-token budget and provider usage can overshoot a call limit.
Actual cost is nullable when usage, speed, cache TTL or applicable tier is
unknown; each job records the price list version. Successful versions use the
same in-process generate/repair/validation/publication pipeline as the terminal.

A fork head, author without write permission or unknown permission additionally
requires `confirm_isolation:true` on a single-PR request. This is separate from
spend confirmation: the backend's read-only mode runs under your account, with
its documented limitations. These PRs are excluded from batch estimates and
cannot be forced into a batch by posting confirmation flags. GitHub permissions
and fork identity come from fresh server-side evidence, never browser fields.
`GET /api/jobs` returns history; `POST /api/jobs/{id}/cancel` accepts `{}`.
Progress notifications include job ID, phase, calls, tokens and repair counts,
never prompts or model text.
`GET /api/events` streams index/progress notifications, replaying from
`Last-Event-ID`; a client outside the bounded replay window receives `reset`
and must rebuild its index. A local snapshot watcher also notifies clients when
a terminal generation changes the index. Viewer evidence is reused in a bounded
16-document cache; at most four local git/linkage reads run at once across it.

New generated versions retain captured description images in the same SQLite
transaction as the document. Only listed PNG, JPEG, GIF and WebP copies are
stored, up to 20 images of 10 MiB each (actual images exceeding these bounds
fail before running a model), and are served with their verified
content type. Older versions without retained copies return not found; the
server never downloads a document's image URLs. The renderer host can use
`AttachmentUrlProvider` to resolve these image endpoints. Sessions, transcripts
and patches follow the existing viewer's local linkage, redaction and cited
path restrictions. `baum view` remains the independent document viewer.
### Local review index

The local application's review index joins cached GitHub discovery with the
existing version stores of registered clones. It reads stores through one SQLite
snapshot per store, so concurrent `baum generate` publication cannot mix a new
version with an old count. Linked worktrees sharing a common git directory share
one index row; independent clones keep their own histories and clone IDs. There
is no second index database and rebuilding never copies or deletes reviews.

An inbox row reports `no_review`, `current` (saved and discovered heads match),
`outdated` (heads differ), `generating`, or `failed`. Missing head evidence or an
unreadable store reports `unknown`; filesystem errors are reported without
exposing their paths. Rows carry version count, the latest version's generation
time, backend/model, risk assessment and recorded USD cost. Cost is null when
none was recorded, including current CLI backends, rather than an invented zero.

Clones with distinct common directories keep independent histories, so each store
gets its own row with its `clone_id`; a discovered PR with no saved review in a
store gets a `no_review` row for that store even when another store has one.
Linked worktrees share a store and therefore one row.

Once the repository and store are resolved, terminal generation records its run
separately from successful versions. Failures use only the fixed categories
`input`, `backend`, `budget`, `validation`, `interrupted`, and `internal`; failed
attempts never add a version. A process renews its two-minute lease every thirty
seconds, including while assembling evidence. A killed process becomes
`failed`/`interrupted` when that lease expires. The most recently started attempt
controls status, so an older attempt finishing cannot overwrite a newer one.

The authenticated UI host can mount `GET /api/index`; `?history=1` also includes
saved reviews absent from the currently discovered inbox. Closed and merged
states already recorded in a saved document survive there. A saved open PR
missing from discovery has state `UNKNOWN`, because absence (including offline
or stale discovery) does not prove it closed. Historical rows retain their last
review without claiming to know the current remote head. Explicit `--store`
overrides remain standalone CLI stores unless the host supplies that store
mapping; the default index uses each clone's normal common-directory/XDG store.
