# Contributing to baum

Thanks for contributing. baum handles code, pull request conversations
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
request. `just --list` shows the other recipes, including `release-snapshot`
(a local goreleaser build for every target).

`CGO_ENABLED=0` must keep working; release binaries are built that way for
linux, macOS and Windows.

The viewer in `viewer/` is a pnpm package with checks of its own: run
`pnpm install` and `pnpm check` there (lint, format check, typecheck and unit
tests). Its [README](viewer/README.md) describes the seams a host connects.

## Coding conventions

- Follow the shape of the code already in the package, and `gofmt` it.
- Comment only what would otherwise be hard to understand.
- Table-driven tests with `t.Run` subtests over near-identical test
  functions. Keep regression tests focused on the changed behaviour.
- Never put secret values, tokens or unredacted session text in errors,
  logs or diagnostics. Test fixtures use clearly synthetic values.
- Packages the hosted service imports (`sessions`, `linkage`, `redact`,
  `bundle`, `review`, `agent`, `schema`) live at public paths; only
  CLI-specific plumbing goes under `internal/`, which the hosted service
  cannot import.
- New dependencies must be pure Go and reviewed; prefer the standard library.

## Commits and pull requests

- Imperative subject lines under about 70 characters ("Add X", not "Added
  X"), with a body explaining why when it is not self-evident.
- Reference the Linear issue in the message, for example `Closes ENG-532`.
- Describe what changed, why, and which checks you ran.
- CI must pass before merge.
- This repository is published from the `oss/` directory of the baum
  monorepo. Before anything else is published, a merged pull request is
  pulled into the monorepo from its root with

  ```sh
  git subtree pull --prefix=oss https://github.com/lastpersonlabs/baum.git main
  ```

  so keep its commits as they are: no force pushes to `main`.
- The review schema in `schema/` is shared with the hosted service. Change
  it in the monorepo, where the hosted code and its UI mirror change with it.

## Security issues

Do not open a public issue. See [SECURITY.md](SECURITY.md).
