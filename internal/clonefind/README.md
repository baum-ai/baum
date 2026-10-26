# Finding local clones

`internal/clonefind` finds the local git clones of the GitHub repositories a
person picks. It searches only while a picked repository has no clone, and
attaches the clone it finds; repositories that are not picked are never looked
for. The setup and Repositories screens list GitHub repositories without local
paths, and show a picked repository's search (`running` in the snapshot) and
then its clone, or that none was found. It reads local files and runs
read-only git commands. It makes no
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
Documents is a candidate on Linux and Windows, since no walk is needed. `Desktop`
and `Downloads` are never walked anywhere.

### Protected folders on macOS

macOS asks the user before an app first reads `~/Documents`, `~/Desktop`,
`~/Downloads`, `~/Library/Mobile Documents` (iCloud Drive) or
`~/Library/CloudStorage`. On macOS (`Options.GOOS`) an agent directory under
any of them is dropped lexically, and again after resolving symlinks, before
any stat or git command; only the one-off Documents opt-in lets `~/Documents`
through, and an event never carries it. Names are compared without regard to
case on macOS and Windows (`scanskip.DirFor`, `UserDataFor`), and Windows
`AppData` is never a walk folder or parent. Git may follow an `include.path`
in a clone's configuration to a file elsewhere; baum does not control that.

On every system an agent directory is stat'ed with its parents up to the home
directory, to find the clone containing it, and git then runs in that clone.

## Matching

A clone is any working tree with a remote on `github.com` (also `www.` and
`ssh.github.com`). Every such remote counts, so a fork (origin is the fork,
`upstream` the picked repository) is listed under both names. An SSH alias is
not resolved (no `ssh -G`), `url.<base>.insteadOf` rewrites are not applied,
and Enterprise hosts are ignored. This textual match is deliberately simpler
than the registry's: `AddFor` and `AttachFound` read the clone with
`github.RepositoryFromRemote` after the user's `insteadOf` rewrites, which may
run `ssh -G` to resolve an SSH alias (a local command, no network). A
candidate the registry cannot match is simply not attached. Names are
lowercase `owner/name`. Linked worktrees sharing a common directory collapse
into one candidate: the main working tree (common directory `<path>/.git`) if
found, else the most recently used. An agent worktree in a hidden folder
(kept under the clone or the home directory in a hidden folder) stands for its main
clone: when the common directory ends in `/.git`, the directory above it is
probed as well (it must be a non-bare working tree whose top level is that
directory, at a place a scan may read) and is the candidate. A directory whose
git top level is neither it nor a parent of it (`core.worktree`) is dropped.

For each repository, candidates are listed best first: clones an agent worked
in, most recent first, then clones only a walk found, by path. A candidate is
`{path, common_dir, last_activity, source}`, `source` being `agent` or `scan`.

## Limits

2000 directories probed with git (two commands each, 3 s per command), 50000
directories walked per run (all folders together; a folder inside one already
walked is skipped), depth 4, 3000 session stores, and the run's
context. A run that hits a limit reports what it found.

## The service

`Service` (run by the daemon) keeps the last result in memory and attaches
clones:

- **When it scans.** Only while a picked repository has no available clone:
  at daemon start in the background (readiness does not wait for it), every
  hour, and when the event subscription missed events; after any registry
  write through the API, `Trigger`: it does nothing unless a picked repository
  has no clone registered (one registry query, no git), else attaches from the
  last result, and scans again unless the last scan was under a minute ago.
  From `Trigger` until that check ends or its scan does, the snapshot reports
  `running`, so a repository just picked reads as being looked for. A scan
  also runs when asked (`POST /api/clones/found/refresh`). With nothing picked
  the daemon reads no folder.
- **What it attaches.** For each picked repository with no available clone it
  calls `Registry.AttachFound(path, repository)` with the best candidate,
  trying the next on failure (logged, never fatal). `AttachFound` files the
  clone under the picked repository when any remote names it, so a fork clone
  (origin the fork, `upstream` the picked repository) is attached to the
  upstream without registering or picking the fork. It never changes what is
  stored: the finder skips a working tree or repository directory already
  registered to any repository, and the registry refuses with
  `ErrClonedElsewhere` rather than move one (`AddFor` moves it), so a fork
  clone registered for the fork is not flipped to the upstream and back. It
  requires the repository to be picked in the same transaction
  (`ErrNotPicked`), so a repository unpicked meanwhile is not picked again.
  Unpicked repositories are never attached.
- **Events.** It subscribes to agent hook events. The directory is marked seen
  first, so it is handled once. Then one registry query that runs no git asks
  for picked repositories with no clone registered; while there are none, an
  event costs that and nothing else. Otherwise the directory is probed, the
  question is asked again under the attach lock, and a clone of such a
  repository is attached at once (the main clone for an agent worktree).
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
      {"path": "/work/code/name", "common_dir": "/work/code/name/.git",
       "last_activity": "2026-07-01T09:30:00Z", "source": "agent"}
    ]
  },
  "attached": [{"repository": "owner/name", "path": "...", "at": "..."}]
}
```

`scanned_at` is null before the first scan, `last_activity` null for a clone
only a walk found.
