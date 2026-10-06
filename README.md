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
layout, review schema, release pipeline and `baum sessions` exist;
`generate`, `bundle`, `view`, `validate` and `refresh` print their help and
exit non-zero until their phase lands.

## Install

With Go:

```sh
go install github.com/lastpersonlabs/baum/cmd/baum@latest
```

Or download an archive for linux, macOS or Windows from the
[releases page](https://github.com/lastpersonlabs/baum/releases). Binaries
are built with `CGO_ENABLED=0` and have no runtime dependencies beyond `git`.

## Commands

```sh
# Build the review document for pull request 191 of the repository in the
# current directory and write it to review.json.
baum generate --pr 191 -o review.json

# Assemble the generation input without calling a model, for debugging.
baum bundle --pr 191 -o bundle.json

# Serve the viewer for a document on localhost and open it in the browser.
baum view review.json
```

`baum validate` checks a document against the schema and its rules and
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
<https://static.usebaum.com/schemas/review/1.json>; a test validates the
example document next to it. The hosted baum service reads the same file, and
baum supports every field it defines.

## Packages

The Go packages other tools may import sit at public paths; only the CLI's
own plumbing is under `internal/`. [ARCHITECTURE.md](ARCHITECTURE.md)
describes how they fit together and what a host implements.

| Package | What it does |
| --- | --- |
| `bundle` | the evidence bundle types a generation run reads, the diff index (hunk IDs, roles, diff URLs) built from a comparison, the `REVIEW.md` loader, reference extraction and bot flags, the code checkout archive, description image handling, and the caps, input redaction, byte trimming and digest applied to a bundle |
| `sessions` | finds the sessions recorded for a repository and maps them into the shared `session.Graph` model |
| `sessions/collector` | discovers Claude Code, Codex and Cursor stores in the home directory and Entire checkpoints in the clone |
| `sessions/{claude,codex,cursor,entire}*` | one parser and mapper per vendor, with golden tests |
| `linkage` | matches commits of a pull request to the sessions that produced them |
| `linkage/sessioncorrelate` | extracts commit refs, branches and touched files from a session graph |
| `redact` | masks secrets and e-mail addresses with [goredact](https://github.com/lastpersonlabs/goredact), and removes e-mail addresses and @mentions from text bound for a code host |
| `schema` | the review schema and its validator |

## Development

Requires Go (version in `go.mod`), [just](https://just.systems) and
[golangci-lint](https://golangci-lint.run). `just check` runs formatting,
vet, build, tests and lint; `just --list` shows the other recipes. See
[CONTRIBUTING.md](CONTRIBUTING.md).

This repository is published from the `oss/` directory of the private baum
monorepo, where the hosted service imports these packages. Pull requests are
reviewed and merged here as usual and then pulled into the monorepo.

## License

MIT, see [LICENSE](LICENSE).
