# baum

[![CI](https://github.com/lastpersonlabs/baum/actions/workflows/check.yml/badge.svg)](https://github.com/lastpersonlabs/baum/actions/workflows/check.yml)
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
layout, review schema, release pipeline, `baum sessions`, `baum validate` and
`baum view` exist; `generate`, `bundle` and `refresh` print their help and exit
non-zero until their phase lands.

## Install

With Go:

```sh
go install github.com/lastpersonlabs/baum/cmd/baum@latest
```

Or download an archive for linux, macOS or Windows from the
[releases page](https://github.com/lastpersonlabs/baum/releases). Binaries
are built with `CGO_ENABLED=0` and have no runtime dependencies beyond `git`
2.29 or newer.

## Commands

```sh
# Build the review document for pull request 191 of the repository in the
# current directory and write it to review.json.
baum generate --pr 191 -o review.json

# Assemble the generation input without calling a model, for debugging.
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

`baum view` serves a document with schema or rule findings anyway and prints
the findings to stderr, as `baum validate` does. The document must be valid
JSON of at most 32 MiB. `baum view --pr N`, which would open the latest
generated document for a pull request, is not available yet: it needs the
local version store, which is not in this build.

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
```

Precedence is flags, then environment, then the file, then defaults. The
environment variables are:

| Variable | Purpose |
| --- | --- |
| `GITHUB_TOKEN`, `GH_TOKEN` | GitHub token; when neither is set and the file has none, `gh auth token` is used |
| `ANTHROPIC_API_KEY` | key for the `anthropic` backend |
| `BAUM_BACKEND` | backend name |
| `BAUM_MODEL` | model name |

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
| `bundle` | the evidence bundle types a generation run reads, the diff index (hunk IDs, roles, diff URLs) built from a comparison, the `REVIEW.md` loader, reference extraction and bot flags, the code checkout archive, description image handling, and the caps, input redaction, byte trimming and digest applied to a bundle |
| `review` | the review document types, the strict, lenient and stored schema views, normalisation, validation with located findings, derived fields, version history, model-free refresh and the tile catalogue (`review/tilecatalogue`) |
| `sessions` | finds the sessions recorded for a repository and maps them into the shared `session.Graph` model |
| `sessions/collector` | discovers Claude Code, Codex and Cursor stores in the home directory and Entire checkpoints in the clone |
| `sessions/{claude,codex,cursor,entire}*` | one parser and mapper per vendor, with golden tests |
| `linkage` | matches commits of a pull request to the sessions that produced them |
| `linkage/sessioncorrelate` | extracts commit refs, branches and touched files from a session graph |
| `redact` | masks secrets and e-mail addresses with [goredact](https://github.com/lastpersonlabs/goredact), and removes e-mail addresses and @mentions from text bound for a code host |
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
