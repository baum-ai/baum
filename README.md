# reviewpal

[![CI](https://github.com/lastpersonlabs/reviewpal/actions/workflows/check.yml/badge.svg)](https://github.com/lastpersonlabs/reviewpal/actions/workflows/check.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

reviewpal is a local-first pull request review generator and viewer. One
binary on your machine builds a `recountable.review/1` document for a pull
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
layout, vendored schema and release pipeline exist; `generate`, `bundle`,
`view`, `validate`, `refresh` and `sessions` print their help and exit
non-zero until their phase lands.

## Install

With Go:

```sh
go install github.com/lastpersonlabs/reviewpal/cmd/reviewpal@latest
```

Or download an archive for linux, macOS or Windows from the
[releases page](https://github.com/lastpersonlabs/reviewpal/releases). Binaries
are built with `CGO_ENABLED=0` and have no runtime dependencies beyond `git`.

## Commands

```sh
# Build the review document for pull request 191 of the repository in the
# current directory and write it to review.json.
reviewpal generate --pr 191 -o review.json

# Assemble the generation input without calling a model, for debugging.
reviewpal bundle --pr 191 -o bundle.json

# Serve the viewer for a document on localhost and open it in the browser.
reviewpal view review.json
```

`reviewpal validate` checks a document against the schema and its rules,
`reviewpal refresh` updates checks, threads and session evidence without a
model call, and `reviewpal sessions` lists the coding-agent sessions recorded
for the current repository. Run any command with `--help` for its flags.

## Configuration

reviewpal reads `$XDG_CONFIG_HOME/reviewpal/config.toml`, which defaults to
`~/.config/reviewpal/config.toml`. The file is optional and is created with
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
| `REVIEWPAL_BACKEND` | backend name |
| `REVIEWPAL_MODEL` | model name |

`backend = "auto"` picks the first of `claude`, `codex` and `cursor-agent`
found on `PATH`, and falls back to `anthropic` when `ANTHROPIC_API_KEY` is set.

reviewpal sends no telemetry and talks to no server other than GitHub and the
model provider you configure. See [SECURITY.md](SECURITY.md) for what is sent
to the provider.

## Review document schema

Documents follow the `recountable.review/1` JSON Schema published at
<https://static.recountable.ai/schemas/review/1.json>. A copy is vendored in
[`schema/review-1.json`](schema/review-1.json) together with an example
document; `just schema-refresh` updates the copy from the published URL and a
test validates the example against it. The URL is the source of truth for
now; the schema's long-term home is still to be decided.

## Development

Requires Go (version in `go.mod`), [just](https://just.systems) and
[golangci-lint](https://golangci-lint.run). `just check` runs formatting,
vet, build, tests and lint; `just --list` shows the other recipes. See
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT, see [LICENSE](LICENSE).
