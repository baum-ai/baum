# baumcli

The hosted service commands of the `baum` binary: login, logout, whoami,
setup, upload and the hook dispatchers that agents and Git call. The open-source
commands and these ship as one binary, built from `cmd/baum`; `--help` lists
these under "Hosted service". Local session discovery lives in
[`sessions/collector`](../../sessions/collector); the archive rules are in
[traceupload](../traceupload/README.md), and the HTTP client, with the protocol
handshake, in [`apiclient`](../apiclient).

## Opt-in

- Nothing is sent to a baum server before `baum login`, or a `BAUM_TOKEN`
  provisioned for CI or by an administrator. `login` starts the device flow
  with the server it names; `whoami`, `logout`, `upload`, `setup --sync` and
  `setup --upload` need a token and fail with a `baum login` hint before any
  request without one. `setup` without those flags and the hooks never contact
  a baum server.
- The hook dispatchers committed to customer repositories and installed as Git
  hooks run `baum hooks agent --check` (`baum hooks git --check`) and use baum
  only when it succeeds, falling through to Entire otherwise. `--check`
  succeeds for a known agent event or Git hook only where the hosted service
  was opted in to, by any of:
  - a login: a stored token for any server, or `BAUM_TOKEN`;
  - the repository is onboarded: an agent settings file committed at `HEAD`
    (`.claude/settings.json`, `.codex/hooks.json`, `.cursor/hooks.json`)
    carries baum's dispatcher, as the onboarding pull request and `baum setup`
    write it. Entire's own command or settings that are not committed do not
    count, so a hook configured outside the repository still falls through in
    a repository that was never onboarded;
  - the clone carries the marker of a `baum setup` (`setup-complete`) or of a
    hook bootstrap (`bootstrap-ok`) in `.git/baum`, of its worktree or of the
    main one, so linked worktrees of a set-up clone count.
- `--check` reads only local files. An onboarded repository or a marker lets
  the hooks do only local work: the session-start bootstrap (installing the
  pinned Entire and the Git hook dispatchers) and delegating to Entire. That
  keeps the onboarding promise for a teammate who installed baum but never
  logged in. Nothing a hook does contacts a baum server; uploads and every
  other server request need a login in every case.
- For an open-source user who never logged in, in a repository nobody
  onboarded or set up, the hooks run Entire directly and baum does nothing,
  not even the bootstrap.
- `logout` removes the stored token but leaves the markers in `.git/baum`, so
  a set-up clone's hooks keep running through baum; without a login they still
  reach no baum server.

## Compatibility

- One binary runs against servers of many releases, so the two agree on a CLI
  API protocol number rather than on versions. Every request names the
  client's (`X-Baum-CLI-Protocol`, `apiclient.Protocol`) and every response of
  a server that knows the handshake names the range it serves
  (`X-Baum-CLI-Protocol-Min` and `-Max`). A client outside the range stops
  before reading the response with an error naming the server and the side to
  update: "this baum is too old" (update baum) or "too new" (update the
  server, or use an older baum). The server refuses nothing itself.
- A server that sends no range predates the handshake and counts as protocol 1,
  except in a 5xx answer, which may come from a proxy in front of it.
- Raise `apiclient.Protocol` when the client starts to need an API a server of
  an earlier release lacks, or stops working with one it used to; a new
  `traceupload.ManifestVersion` is such a change. The server built from the
  same commit serves up to that number. The server's minimum is raised only
  when a release drops an API older clients use.
- Requests also carry `User-Agent: baum/<version>`.

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
  specs, the same ones the hosted service's onboarding pull request writes, so
  the two never rewrite each other.

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
