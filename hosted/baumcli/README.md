# baumcli

The `baum` command: login, logout, whoami, setup, upload and the hook
dispatchers that agents and Git call. Local session discovery lives in
[`traceprocessing/collector`](../traceprocessing/collector); the archive rules
are in [traceupload](../traceupload/README.md).

## Credentials

- Server: `--server`, then `BAUM_SERVER`, then the stored URL, then
  `defaultServerURL` (`https://usebaum.com/api`). Token: `BAUM_TOKEN`, then
  the stored token.
- A stored token is sent only to the server recorded beside it
  (`issuedFor`); a different scheme or host is a different deployment.
- The credentials file is written atomically (temporary file, then rename)
  with mode 0600.
- `logout` revokes only the stored token, never `BAUM_TOKEN`, and warns when
  `BAUM_TOKEN` is set; `login` warns that it still takes precedence.
- An expired `BAUM_TOKEN` is reported, never silently replaced by a new
  login.

## Setup

- Setup is local by default: it never reads credentials for auth, calls the
  server or starts the device flow. It installs hooks (and Entire unless
  `--skip-entire`). Re-running changes nothing.
- The completion marker means "hooks configured". It is written only when
  Entire setup ran, succeeded and found a coding agent to configure; with
  `--skip-entire`, no detected agent or after an Entire failure (a warning)
  there is no marker, so `--all` offers the repository again.
- A repository needs a GitHub `origin` remote; its name comes only from there.
- `setup [PATH...]` sets up each checkout (default: the current one). `PATH`
  cannot combine with `--all`. Paths reaching one checkout (symlinks) count once.
- `setup --all` discovers checkouts locally: session metadata is a hint, plus
  Git checkouts up to three levels below the working directory (hidden
  directories, `node_modules`, `vendor` and similar are skipped; a found
  repository is not descended into). Checkouts are deduplicated by their
  symlink-resolved path. It prints the scanned root, lists the plan and asks
  once unless `--yes`. One failing repository does not stop the others.
- `--all` refuses to scan the filesystem root. From the home directory it skips
  user-data folders (Desktop, Documents, Downloads, Pictures, Movies, Music,
  Public, Library, Applications and cloud folders such as Google Drive,
  Dropbox, OneDrive, iCloud Drive); other directories are still scanned.
- `--sync` and `--upload` are the only paths that need credentials. Without a
  token they fail up front with a `baum login` hint and never start the device
  flow. Hooks are installed before the repository lookup and sync request, so a
  server error leaves hooks in place and is reported afterwards. Sessions are
  uploaded only with `--upload`, including with `--all` or `--previous-path`;
  `--previous-path` requires `--upload` and cannot combine with `--all`.
- With `--all`, local repositories the server does not know get hooks but skip
  sync and upload, as do repositories the server reports as not enabled in baum
  (the message points at the repositories page). A checkout without local
  sessions is set up and skipped for upload, not a failure. With `--upload` the
  plan shows a sessions column and a summary line. Duplicate clones of one
  repository get hooks each, but request one sync and share one inventory entry
  per repository ID (roots merged).
- Hook configuration comes from the shared [`agenthooks`](../agenthooks/agenthooks.go)
  specs, the same ones [onboarding](../onboardingpr/README.md) writes, so the two
  never rewrite each other.

## Hooks

- Hooks must not break the agent or Git. Session-start bootstrap is
  best-effort within `sessionStartBootstrapTimeout` and retries next time;
  Entire setup errors are warnings; missing Entire lets the hook continue.
- A delegated agent hook's exit code propagates (signal death maps to 1),
  because the agent acts on it. A failing Git `pre-push` propagates so the push
  stops; other Git-hook failures warn and continue.

## Discovery

- Repository identity comes only from a verified GitHub origin. Folder names,
  branches and commit hashes never prove it.
- Previous paths and Git's retained worktree metadata help recover moved or
  deleted checkouts, but a pruned or reused path can leave evidence incomplete.
- `baum upload` leaves out sessions that ran in an independent clone nested
  inside the repository (a live directory whose nearest checkout has a `.git`
  directory); they belong to that clone's own repository. A deleted directory
  cannot be told apart from a forgotten worktree and stays with the outer one.
- `baum upload` and `upload --all` skip a repository the server reports as not
  enabled in baum before redacting anything; under `--all` that counts as
  skipped, not failed.
- A completed upload does not mean every session mapped.

## Upload size

- An upload sends the repository's whole local history as one archive. Its
  unpacked size is measured as it is built and compared with
  `traceupload.MaxUnpackedBytes` (512 MiB), the bound the ingest worker
  applies ([traceupload](../traceupload/README.md)). An archive over it fails
  before anything is sent, naming its size and the limit; "Uploaded N
  sessions" is printed only for an archive the server will unpack. Splitting a
  large history into several uploads is not supported yet.
