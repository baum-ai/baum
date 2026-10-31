# Local repository registry

This package owns `repos.sqlite`, a private (0600) SQLite database beside the
config file. It stores what the user chose to review. It reads local git
metadata only and contacts nothing; the daemon supplies the one GitHub lookup
its HTTP handler uses.

## Stored data

- `repositories(identity, settings)`: settings shared by every clone of a
  repository. `identity` is the lowercase `owner/name` on github.com and
  `host/owner/name` elsewhere. `settings` is JSON (`automatic`, `decisions`); a `daily_cap`, `backend` or
  `model` an older baum stored is ignored and dropped on the next write, and
  dropped from a request an older app sends: the harnesses and models are
  global preferences.
- `clones(id, path, common_dir, repository)`: registered working trees. A list
  reprobes each path and reports a changed or missing one unavailable without
  rewriting it.
- `picked(identity)`: repositories picked by name, possibly without a clone.
- `aliases(alias, repository)`: earlier github.com names GitHub forwards to a
  repository after a rename or transfer, recorded when a folder whose remote
  uses one is registered (`AddForwarded`). `Open` and `OpenReadOnly` load them
  into `gitlocal` (every `baum` command opens the registry read-only first), so
  in that process every remote URL using an earlier name
  reads as the current one (`gitlocal.RemoteRepository`,
  `github.RepositoryFromRemote`) and the clone is a clone of the repository
  everywhere. Clones registered under the earlier name move to the current
  one, which is picked once and takes the earlier name's settings while its
  own are still the default. Renames chain, and renaming back undoes them.
  The daemon asks GitHub again at startup, and a fix before it pushes for
  its repository (`RecheckAliases`): an alias GitHub now answers with
  another repository than the recorded one (a new one took the name) is
  dropped, so its remotes name that repository again; one it could not
  check or does not show the token is kept, but a fix does not push while
  one is: a repository the token cannot see may have taken the name. The
  remote is never rewritten.
  `Repositories` lists them as `earlier_names`.
- `preferences(key, value)`: the global preferences as one JSON row.
- `preferences(key, value)`: the global preferences as one JSON row. Its
  `appearance` object (`theme`, `key_hints`) stores the device’s General
  settings independently of the daemon’s loopback port. Its
  `decisions` object (`enabled`, `model`) configures local
  decision capture; `Settings.DecisionsEnabled` combines it with the automatic
  master switch and the repository's `decisions` (nil is on). See
  [`decisionlog`](../decisionlog/README.md).

A repository is picked exactly when it has a `picked` row. Adding a clone also
picks its repository, and removing the clone keeps it picked with zero clones.
A repository with no clone is still registered: `RepositorySettings` and
automatic admission treat it as such, and only `unpick` revokes that. Work
that needs a clone waits for one: discovery lists the repository's pull
requests, but the job API answers `needs_clone` and automatic generation
submits nothing until a clone is registered. Clones registered
before the `picked` table existed have no picked row (there is no migration),
so they must be re-added with `baum internal repos add <path>`, or their
repository picked, to count as registered.
`Unpick` deletes the repository's clones and its picked row but keeps the
`repositories` row, so its settings survive a re-pick. `Pick` accepts only
github.com `owner/name`; it reads no local git and checks nothing on GitHub.

`PRAGMA user_version` is 1: `Open` turns the `automatic: false` that earlier
registrations stored into `null` (follow the global preference). `OpenReadOnly`
reads such a file the same way without writing it, and lists no picked
repositories when the `picked` table is missing.

## HTTP

`Registry.Handler` has no authentication; the daemon mounts it behind its
authenticated host and origin checks. JSON is decoded strictly.

| Route | Result |
| --- | --- |
| `GET /api/repos` | clones |
| `GET /api/repos/check?path=` | whether a path can be registered |
| `POST /api/repos` `{"path","settings"?,"repository"?}` | register a clone; optional repository uses any matching remote (400 `{"message"}`) |
| `DELETE /api/repos/{id}` | remove a clone (204, 404) |
| `PUT /api/repos/{id}/settings` | replace the repository's settings |
| `GET /api/repositories` | picked repositories, `[{"repository","settings","clones":[]}]` |
| `POST /api/repositories` `{"repository","settings"}` | pick (400 on a bad name or settings) |
| `DELETE /api/repositories/{owner}/{name}` | unpick (204, 404) |
| `PUT /api/repositories/{owner}/{name}/settings` | replace settings (404 unless picked) |

Only `POST` validates the name. `DELETE` and `PUT` only lowercase it and look
it up, so a malformed `owner/name` answers 404, and an Enterprise identity
added through `add` can be unpicked from the CLI.

`Add` files a clone under its first GitHub remote, `origin` first. `AddFor(path,
repository, settings)` files it under `repository` when any of its GitHub
remotes names it (a fork clone whose `upstream` is the picked repository), and
fails with `ErrNoMatchingRemote` when none does. The fork's own repository is
neither registered nor picked. A list reprobes a clone by its stored
repository the same way, so such a clone stays available while some remote
names that repository. Pull request commits a clone lacks are fetched by
generation from the remote whose URL matches the registered repository, not
`origin`, into `refs/baum/pull/<N>/`.

Cloning a picked repository is served beside these routes by
[`internal/githubrepos`](../githubrepos/README.md) (the registry package
contacts nothing): `GET /api/repositories/{owner}/{name}/clone-target` (the
default folder), `POST /api/repositories/{owner}/{name}/clone` `{"dir"?}` (picks
the repository if needed, clones in the background and registers the clone with
`AddFor`; 202, 400, 409) and `GET .../clone` (the current or last state).

The explicit Choose folder action supplies `repository` to `POST /api/repos`,
which calls `AddFor`. When no remote names the repository, `HandlerWith`'s
`CurrentName` (the daemon asks GitHub) checks each GitHub remote, origin
first; the first GitHub answers with the repository's current name is
registered through `AddForwarded`. Otherwise the request fails with 400 and a
JSON `message` naming the repositories the remotes use, or saying GitHub could
not be asked. `Handler` has no `CurrentName` and refuses such folders. Omitting
it preserves the standalone path registration behavior.

`AddFor` moves a path already registered to another repository to the new
one. The daemon's clone finder uses `AttachFound` instead, which never changes
what is stored: it fails with `ErrClonedElsewhere` when the path or its
repository directory belongs to another repository, and with `ErrNotPicked`
unless the repository is picked, checked in the same transaction, so an unpick
in between is not undone. `PickedWithoutClones` (picked repositories with no
clone row) and `CloneRows` (clones as stored, `available` false) read the
registry only and run no git, unlike `List` and `Repositories`, which probe
every clone.

Settings PUTs replace the whole object and require `automatic`, `null` for
the default. A `daily_cap` an older app still sends, in a settings PUT or in
the settings of a `POST`, is dropped rather than refused: baum sets no
spending limits.

## CLI

`baum internal repos add|remove|list` manage clones. `pick <owner/name>` and
`unpick <owner/name>` manage picked repositories, and `repositories [--json]`
lists them, including those without a clone.
