# Contributing to reviewpal

Thanks for contributing. reviewpal handles code, pull request conversations
and coding-agent session transcripts that were never meant to leave a
developer's machine unredacted, so changes to what is read, redacted or sent
to a model provider get a closer look than ordinary hygiene.

## Building and testing

Standard Go tooling is all you need to build and test:

```sh
go build ./...
go vet ./...
go test ./...
```

`just check` runs the same steps CI runs: `gofmt` check, vet, build, tests
with the race detector and `golangci-lint`. Run it before opening a pull
request. `just --list` shows the other recipes, including `schema-refresh`
(re-fetch the vendored review schema) and `release-snapshot` (a local
goreleaser build for every target).

`CGO_ENABLED=0` must keep working; release binaries are built that way for
linux, macOS and Windows.

## Coding conventions

- Follow the shape of the code already in the package, and `gofmt` it.
- Comment only what would otherwise be hard to understand.
- Table-driven tests with `t.Run` subtests over near-identical test
  functions. Keep regression tests focused on the changed behaviour.
- Never put secret values, tokens or unredacted session text in errors,
  logs or diagnostics. Test fixtures use clearly synthetic values.
- Packages the platform will import (`sessions`, `linkage`, `redact`,
  `bundle`, `review`, `agent`, `schema`) live at public paths; only
  CLI-specific plumbing goes under `internal/`.
- New dependencies must be pure Go and reviewed; prefer the standard library.

## Commits and pull requests

- Imperative subject lines under about 70 characters ("Add X", not "Added
  X"), with a body explaining why when it is not self-evident.
- Reference the Linear issue in the message, for example `Closes ENG-532`.
- Describe what changed, why, and which checks you ran.
- CI must pass before merge.

## Security issues

Do not open a public issue. See [SECURITY.md](SECURITY.md).
