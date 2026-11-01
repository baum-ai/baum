# Picking GitHub repositories and cloning one

`internal/githubrepos` lists the GitHub repositories the signed-in account can
reach, so the person can pick some, and clones a picked repository into a
folder they can change. It is the only place that lists the account's
repositories; the GitHub calls are `github.Client.ViewerRepositories` and
`gitlocal.Clone`, both with the account's GitHub token (`config.ResolveGitHubToken`:
explicit token, else the saved device sign-in).

## Listing

`ViewerRepositories` reads one `viewer.topRepositories` page ordered by latest
push, plus the accessible `totalCount` from `viewer.repositories` in the same
request. The first page has eight nodes, matching the visible rows. Once it
returns, one background request asks for one hundred nodes after its cursor.
Later pages of one hundred load only when asked for (`more=1`, below). Names
already returned are skipped. Archives are excluded after each response. The top list is cached for
five minutes; when it reloads, the earlier extra rows stay listed after the new
first page until the new extra page replaces them, so the list never shrinks
to eight rows and grows back.

Search calls GitHub's `search(type: REPOSITORY, first: 100)` directly. Queries
search repository names and include forks but exclude archives. GitHub search
covers all of GitHub, so a search is limited to repositories the person has a
permission on, read in the same request as the top list: every repository of
the viewer's login and of each organization it belongs to
(`viewer.organizations(first: 100)`), as `user:` qualifiers, and each
repository it collaborates on elsewhere
(`viewer.repositories(affiliations: [COLLABORATOR], first: 100)`, most recently
pushed first), as exact `repo:` qualifiers. GitHub combines the qualifiers with
OR; at most 300 are sent. The owner of a collaboration, or of a top repository
the viewer only contributed to, is never searched as a whole: the viewer has no
permission on that owner's other repositories. Top repositories still match from
the cache. With nothing in scope a search returns nothing rather than searching
all of GitHub. Owners a query names (`owner/name`, or `user:`, `org:` or `repo:`
qualifiers) narrow the scope to theirs and their names become search terms, so
`acme/wid` searches `wid` among acme's repositories in scope, and a stranger's
`owner/name` finds nothing. No accessible-repository enumeration runs beyond
those first hundred collaborations. A search made before the top list exists
loads its first page first, since the scope comes from it.

`GET /api/github/repositories?query=&refresh=1&more=1` answers

```json
{"fetched_at": "2026-07-01T10:00:00Z", "total_count": 147, "searching": true,
 "has_more": true,
 "repositories": [{"repository": "owner/name", "private": false, "top": true,
   "fork": false, "pushed_at": "2026-07-01T09:00:00Z", "description": "",
   "picked": true, "clone": "/work/code/name"}]}
```

A non-empty query returns matching cached top rows immediately while its GitHub
search runs in the background. The response also includes `top_repositories`,
the full cached top list, so the picker can filter it on each keystroke before
any network request or debounce. On completion the search results merge with
matching top rows without duplicates. An empty search preserves the order returned
by `topRepositories`. Search sorting is exact repository name or
owner/name match first, then membership in the top list, then latest push time;
names break remaining ties. Search results are cached for five minutes by
normalized query (case-insensitive, trimmed and collapsed whitespace), with at
most 64 cached queries. Concurrent requests for one query share a request.

`more=1` starts loading the next page of the current listing in the background,
unless one is loading or there is none: one hundred more top repositories after
the last cursor for an empty query, or the search's next page of one hundred
(with the owners it started with) for a query. New rows are appended without
duplicates. `has_more` says another page exists. `total_count` is the
accessible, non-archived total for an empty query; for a query it counts every
match: the rows returned plus the search results not loaded yet (GitHub returns
at most 1,000 per search). A failed next page keeps the loaded rows and reports
`search_error`; `refresh=1` starts the listing again from its first page.

`searching: true` means a search or the extra top page is still pending. Cached
rows remain pickable. A failed request with cached matches returns those rows
and a sanitized `search_error`; with no matches it uses the HTTP errors below.
`refresh=1` retries the current query or refreshes the top list when the query is
empty. The native relay's named `retry-list` action uses this option, and its
`more-list` action sends `more=1`. Account
changes cancel background requests and discard both caches; late responses
cannot refill them. Shutdown cancels background requests and waits for them.
`picked` comes from the registry; `clone` is an available registered path or
`""`. `earlier_names` are the registry's recorded earlier repository names (see
[`internal/repos`](../repos/README.md)).

A failure answers a JSON `{"code","message"}` (never a token or an upstream
body) with the codes the app already explains for the GitHub connection:

| Status | `code` |
| --- | --- |
| 409 | `not_connected`, `reconnect_required`, `insufficient_scopes`, `sso_required`, `permission_denied` |
| 503 | `credential_store_unavailable` |
| 429 | `rate_limited`, with `resume_at` (when known) and `Retry-After` |
| 502 | `github_unavailable` |
| 500 | `registry_unavailable` |

A request GitHub refused is classified by the sign-in's runtime health
(`config.GitHubAuth.Health`), which the GraphQL client feeds from every
response.

## Cloning

| Route | Result |
| --- | --- |
| `GET /api/repositories/{owner}/{name}/clone-target` | `{"dir": "<default folder>"}` |
| `POST /api/repositories/{owner}/{name}/clone` `{"dir"?}` | 202 and a clone state; 400 `invalid_dir`, `invalid_request`, `invalid_repository`; 409 `clone_running` or `dir_in_use`; 503 `stopping` |
| `GET /api/repositories/{owner}/{name}/clone` | the current or last clone state; 404 if none was started |

A clone state is `{"repository","dir","state","error"?,"code"?}`, `state` being
`running`, `done` or `failed`; `error` is sanitized text. `code: "url_rewritten"`
means the person's Git configuration changes the requested clone URL, including
to another repository on GitHub or to SSH. State is in memory.

**Default folder.** The most common parent directory of the registered clones
that lie below the home directory (ties: flat layout first, then alphabetical),
joined with the repository name. If clones follow `<root>/<owner>/<name>` (the parent folder
is named after the registered owner), that layout is kept for the new owner.
With no such clone, `~/code/<owner>/<name>`. While that path
exists, `-2`, `-3`, ... is appended to the name.

**Starting.** The body is strict JSON (an empty body means the default folder).
`dir` must be absolute, inside the home directory but not the home directory
itself, and either missing or an empty directory. A symlink directly under
home (such as `~/code` on another disk) is supported; deeper symlinks cannot
lead outside home or that code folder. A symlinked home works by either path.
The repository is picked if it is not (asking for a clone chooses it, and it stays picked if the clone fails). The clone runs in the
background under the daemon's context, at most one per repository and with no
overlapping target folders (including symlink aliases and case variants), with
`gitlocal.Clone`'s 30 minute timeout. On success the clone is registered with
`Registry.AddFor(dir, repository, nil)` and the app is told the registry
changed. New clones are refused during shutdown. A failed or cancelled clone
removes its own target and any empty parent folders it created; a target that
already existed is left for git to clean up.

`gitlocal.Clone` builds its URL only from a validated owner/name
(`gitlocal.GitHubCloneURL`), passes the token in git's private environment only,
and disables hooks, templates, fsmonitor, submodules and every protocol but
HTTPS. Certificate verification is forced on with a token. Git URL rewrites
that change the requested URL are refused before creating folders. Configured
checkout filters such as git-lfs still run and may contact their own storage
hosts. See `SECURITY.md`.
