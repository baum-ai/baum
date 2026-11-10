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

`just check` runs the same steps CI runs: `gofmt` check, `go mod tidy -diff`,
vet, build, tests with the race detector and `golangci-lint`. Run it before
opening a pull request. The built viewer and local app (`internal/viewer/dist`,
`internal/localapp/dist`) are not committed: only a release commit, the commit
a `v*` tag points at, carries them, so `go install` of a tagged version needs
nothing but Go. Run `just viewer-build` before building a binary with a working
UI or running the tests that read the built apps, which skip without it; a
binary built without it says so in place of each app. Because release commits
sit beside `main` rather than on it, `go install …@main` (or any untagged
commit) gets such a binary, and its pseudo-version derives from an older tag
than the latest release; install a tagged version (`@latest`). The precompiled tile
validators are committed, and `just viewer-fresh` fails when they differ from a
fresh build, as CI does. `just --list` shows the other recipes, including
`release-snapshot` (a local goreleaser build for every target).

`CGO_ENABLED=0` must keep working; release binaries are built that way for
linux, macOS and Windows.

The viewer in `viewer/` is a pnpm package with checks of its own: run
`pnpm install` and `pnpm check` there (lint, format check, typecheck and unit
tests), and `pnpm test:e2e` for the browser tests of the standalone app. Its
[README](viewer/README.md) describes the seams a host connects.

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

## Pull requests

1. Fork [baum-ai/baum](https://github.com/baum-ai/baum) and create a branch
   from `main`.
2. Make your change and run `just check`, plus the viewer checks above if you
   changed `viewer/`.
3. Open a pull request against `main`. CI runs the checks on Linux, macOS and
   Windows. On your first pull request a maintainer has to approve the run
   before it starts.

Write commit subject lines in the imperative and keep them under about 70
characters ("Add X", not "Added X"). Add a body explaining why when the reason
is not obvious. In the pull request, say what changed and why, list the checks
you ran, and reference the issue it fixes, for example `Fixes #12`. CI must
pass before merge.

For a large change, or one that changes what baum reads, redacts or sends to
a model provider, open an issue first so the approach can be agreed before
you write it.

### How this repository relates to the monorepo

baum is developed in a private monorepo, and this repository is published
from its `oss/` directory. You do not need access to the monorepo: open your
pull request here and it is reviewed and merged here. Afterwards a
maintainer brings it into the monorepo with your commits and authorship
unchanged, and later changes are published on top of it. What this means for
you:

- Most commits on `main` come from the monorepo. A pull request number in
  their messages, such as "Merge pull request #1139 from baum-ai/…", refers
  to the monorepo, not to a pull request in this repository.
- `main` is never force-pushed, so you can always rebase a branch onto it.
- A maintainer may add follow-up commits after merging your pull request.
  They are published here like any other change.
- The review schema in `schema/` is shared with the hosted service, which
  has to change with it. Open an issue before changing the schema; a
  maintainer makes the matching hosted change in the monorepo.

## Security issues

Do not open a public issue. See [SECURITY.md](SECURITY.md).
