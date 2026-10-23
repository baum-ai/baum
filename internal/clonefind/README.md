# Finding local clones

`internal/clonefind` finds the local git clones of GitHub repositories without
asking. Picked repositories without a clone get one attached automatically, and
the setup and Repositories screens can pre-select repositories whose clone was
found. It reads local files and runs read-only git commands. It makes no
network request, and nothing about local folders leaves the computer.

## Sources

1. **Folders agents worked in.** The working directory of every agent hook
   event in the record (`session.cwd`, else the hook's `dir`; Git hooks are
   ignored), and of every Claude Code, Codex (root sessions only) and Cursor
   session store. The stores are found with `collector.FindStores`; one cheap
   probe per file reads the recorded working directory, never the transcript
   body. Only the newest 3000 stores are probed. Each directory keeps the time
   of its latest activity (an event's time, a store's modification time).
2. **Code folders.** `~/code ~/src ~/dev ~/projects ~/workspace ~/repos ~/git
   ~/go/src/github.com`, and `~/Documents` (see below), those that exist, plus the parent directory of each
   clone found in 1 when it lies below the home directory.

## Folders read and never read

Read: the agent session stores (`~/.claude/projects`, `~/.codex/sessions`,
`~/.codex/archived_sessions`, Cursor's `state.vscdb`), the hook event record,
the folders listed above, and a directory an agent worked in (to find the clone
containing it). In a clone, git reads `.git/config` and the repository
directory; no file of the working tree is read.

A walk goes at most 4 levels below its folder, stops at a directory with a
`.git` entry, and never follows a symlink to a directory. It never enters hidden
folders, `node_modules`, `vendor`, `target`, `dist`, `build`, `__pycache__`,
`Library` or `Applications`. It never starts in, or below, `Desktop`,
`Downloads`, `Pictures`, `Movies`, `Music`, `Public`,
`iCloud Drive`, or a `Google Drive`, `Dropbox` or `OneDrive` folder (the lists
are shared with `baum setup --all`, in `internal/scanskip`), after resolving
symlinks, so a code folder that links there is skipped. It refuses the
filesystem root, the home directory and anything above it as a starting
point, and never reports any of them as a clone. A directory an agent worked
in is allowed anywhere (the agent was there), but the folder around it is
walked only if it is below the home directory and not one of those.

### Documents

`~/Documents` is walked like the code folders on Linux and Windows. On macOS
it is a protected folder: the first read makes the system ask the user, so the
automatic scan leaves it out. `Options.IncludeDocuments` (`GOOS` is injectable
for tests) or `POST /api/clones/found/refresh` with `{"include_documents":
true}` runs one scan that includes it; the choice is not stored, and a later
scan leaves Documents out again. A directory an agent worked in inside
Documents is a candidate on every platform, since no walk is needed. `Desktop`
and `Downloads` are never walked anywhere.

## Matching

A clone is any working tree with a remote on `github.com` (also `www.` and
`ssh.github.com`). Every such remote counts, so a fork (origin is the fork,
`upstream` the picked repository) is listed under both names. An SSH alias is
not resolved (no `ssh -G`), and Enterprise hosts are ignored. Names are
lowercase `owner/name`. Linked worktrees sharing a common directory collapse
into one candidate: the main working tree (common directory `<path>/.git`) if
found, else the most recently used.

For each repository, candidates are listed best first: clones an agent worked
in, most recent first, then clones only a walk found, by path. A candidate is
`{path, common_dir, last_activity, source}`, `source` being `agent` or `scan`.

## Limits

2000 directories probed with git (two commands each, 3 s per command), 50000
directories walked per folder, depth 4, 3000 session stores, and the run's
context. A run that hits a limit reports what it found.

## The service

`Service` (run by the daemon) keeps the last result in memory and attaches
clones:

- **When it scans.** At daemon start in the background (readiness does not wait
  for it); after a repository is picked or any registry write (`Trigger`
  attaches from the last result first, and scans again unless the last scan was
  under a minute ago); every hour while a picked repository has no available
  clone; and when asked (`POST /api/clones/found/refresh`) or when the event
  subscription missed events.
- **What it attaches.** For each picked repository with no available clone it
  calls `Registry.AddFor(path, repository, nil)` with the best candidate,
  trying the next on failure (logged, never fatal). `AddFor` files the clone
  under the picked repository when any remote names it, so a fork clone
  (origin the fork, `upstream` the picked repository) is attached to the
  upstream without registering or picking the fork. Unpicked repositories are
  never attached.
- **Events.** It subscribes to agent hook events. A directory not seen before,
  while some picked repository has no clone, is probed; if its clone is one of
  such a repository it is attached at once.
- **Telling the app.** After attaching it publishes the daemon's `index`
  change, and `attached` in the API lists the last 20 attachments. No `notice`
  event is published: those are the desktop app's notifications and each names
  a pull request.

## HTTP

Mounted by the daemon in `internal/uiserver` behind the authenticated host and
origin checks, since it returns local paths.

| Route | Result |
| --- | --- |
| `GET /api/clones/found` | the last result |
| `POST /api/clones/found/refresh` `{}` or `{"include_documents": true}` | starts a scan unless one runs; 202 `{"started","running"}` |

```json
{
  "scanned_at": "2026-07-01T10:00:00Z",
  "running": false,
  "repositories": {
    "owner/name": [
      {"path": "~/code/name", "common_dir": "~/code/name/.git",
       "last_activity": "2026-07-01T09:30:00Z", "source": "agent"}
    ]
  },
  "attached": [{"repository": "owner/name", "path": "...", "at": "..."}]
}
```

`scanned_at` is null before the first scan, `last_activity` null for a clone
only a walk found.
