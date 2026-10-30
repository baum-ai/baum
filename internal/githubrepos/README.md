# Picking GitHub repositories and cloning one

`internal/githubrepos` lists the GitHub repositories the signed-in account can
reach, so the person can pick some, and clones a picked repository into a
folder they can change. It is the only place that lists the account's
repositories; the GitHub calls are `github.Client.ViewerRepositories` and
`gitlocal.Clone`, both with the account's GitHub token (`config.ResolveGitHubToken`:
explicit token, else the saved device sign-in).

## Listing

`ViewerRepositories` reads `viewer.repositories` (owner, collaborator and
organization-member affiliations, newest push first, archived left out) up to
300, and one search for open pull requests that involve the account
(`is:pr is:open involves:@me archived:false`, first 100) to count them per
repository. If only the search fails the counts are zero. Names are lowercased.

`GET /api/github/repositories?query=&refresh=1` answers

```json
{"fetched_at": "2026-07-01T10:00:00Z",
 "repositories": [{"repository": "owner/name", "private": false, "fork": false,
   "pushed_at": "2026-07-01T09:00:00Z", "description": "", "open_pull_requests": 2,
   "picked": true, "clone": "/work/code/name"}]}
```

Ranking: repositories with open pull requests involving the account first (most
first), then latest push, then name. `query` keeps repositories whose
`owner/name` contains every whitespace-separated term, ignoring case; it filters
the cached list and calls GitHub no further. The GitHub result is cached in
memory for 5 minutes, dropped when the signed-in account changes, and
`refresh=1` bypasses it; simultaneous requests for the same account share one
call. A response started before an account change cannot refill the cache
afterward. `picked` comes
from the registry; `clone` is an available registered path, otherwise the best
path the clone finder's last result has for the repository, or `""`.
`earlier_names` are a picked repository's recorded earlier names (see
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
