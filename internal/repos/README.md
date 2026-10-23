# Local repository registry

This package owns `repos.sqlite`, a private (0600) SQLite database beside the
config file. It stores what the user chose to review. It reads local git
metadata only and contacts nothing.

## Stored data

- `repositories(identity, settings)`: settings shared by every clone of a
  repository. `identity` is the lowercase `owner/name` on github.com and
  `host/owner/name` elsewhere. `settings` is JSON (`automatic`, `daily_cap`,
  `backend`, `model`).
- `clones(id, path, common_dir, repository)`: registered working trees. A list
  reprobes each path and reports a changed or missing one unavailable without
  rewriting it.
- `picked(identity)`: repositories picked by name, possibly without a clone.
- `preferences(key, value)`: the global preferences as one JSON row.

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
| `POST /api/repos` `{"path","settings"}` | register a clone |
| `DELETE /api/repos/{id}` | remove a clone (204, 404) |
| `PUT /api/repos/{id}/settings` | replace the repository's settings |
| `GET /api/repositories` | picked repositories, `[{"repository","settings","clones":[]}]` |
| `POST /api/repositories` `{"repository","settings"}` | pick (400 on a bad name or settings) |
| `DELETE /api/repositories/{owner}/{name}` | unpick (204, 404) |
| `PUT /api/repositories/{owner}/{name}/settings` | replace settings (404 unless picked) |

Only `POST` validates the name. `DELETE` and `PUT` only lowercase it and look
it up, so a malformed `owner/name` answers 404, and an Enterprise identity
added through `add` can be unpicked from the CLI.

Settings PUTs replace the whole object and require `automatic` and
`daily_cap`, `null` for the default.

## CLI

`baum internal repos add|remove|list` manage clones. `pick <owner/name>` and
`unpick <owner/name>` manage picked repositories, and `repositories [--json]`
lists them, including those without a clone.
