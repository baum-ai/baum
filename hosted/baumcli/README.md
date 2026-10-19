# baumcli

The hosted service commands of the `baum` binary: login, logout, whoami,
setup, upload, the hook dispatchers that agents and Git call, and the
installer of the user-wide agent hooks. The open-source
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
  request without one. `setup` without those flags and the hook commands
  never contact a baum server. The hooks hand their events to the local
  daemon, which forwards an ended session to the server only for a logged-in
  user (see "Hook events").
- The hook dispatchers committed to customer repositories and installed as Git
  hooks run `baum hooks agent --check` (`baum hooks git --check`) and use baum
  only when it succeeds, falling through to Entire otherwise. `--check`
  succeeds for a known agent event or Git hook only where the hosted service
  was opted in to, by any of:
  - a login: a stored token for any server, or `BAUM_TOKEN`;
  - the clone carries the marker of a `baum setup` (`setup-complete`) or of a
    hook bootstrap (`bootstrap-ok`) in `.git/baum`, of its worktree or of the
    main one, so linked worktrees of a set-up clone count;
  - for a hosted install only, the repository is onboarded: an agent settings
    file committed at `HEAD` (`.claude/settings.json`, `.codex/hooks.json`,
    `.cursor/hooks.json`) carries baum's dispatcher, as the onboarding pull
    request and `baum setup` write it. Entire's own command, the pre-rename
    dispatcher and settings that are not committed do not count, so a hook
    configured outside the repository still falls through in a repository
    that was never onboarded. The read runs under a timeout, without fetching
    or prompting, and skips a settings file over 1 MiB.
- A hosted install is a build linked with
  `-X github.com/baum-ai/baum/hosted/baumcli.distribution=hosted`, as the
  hosted release builds it, or a baum the hosted install script installed,
  which writes `hosted-install` beside the credentials. The open-source and
  desktop builds are neither, so they never set up Entire, whose hooks push
  transcripts, in a repository merely because its committed files ask for it.
- `--check` reads only local files. An onboarded repository or a marker lets
  the hooks do only local work: the session-start bootstrap (installing the
  pinned Entire and the Git hook dispatchers) and delegating to Entire. That
  keeps the onboarding promise for a teammate who installed the hosted baum
  but never logged in. Nothing a hook does contacts a baum server; uploads,
  the daemon's forwarding included, and every other server request need a
  login in every case.
- For an open-source user who never logged in, in a clone nobody set up, the
  hooks run Entire directly and baum does nothing, not even the bootstrap,
  whatever the repository commits. The user-wide hooks (below) apply the same
  rules and, where they refuse, do nothing at all.
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
- Setup also installs the user-wide hooks below, once per run, for the agents
  it detects, but only for a hosted install or a logged-in user, where they
  add something today, and not with `--skip-user-hooks` or `--skip-entire`.
  The `--all` plan names the files it will change before asking. An
  open-source user without a login gets them only deliberately, from
  `baum hooks install --user` or the desktop app's onboarding, once a daemon
  makes them useful there. Setup keeps writing the repository's own hooks:
  teammates without user-wide hooks need them, and the user-wide ones yield to
  them.

## User-wide hooks

- `baum hooks install --user` adds baum's hooks to `~/.claude/settings.json`
  (Claude Code) and `~/.codex/hooks.json` (Codex; it also reads inline hooks in
  `config.toml`, which baum leaves alone), for the detected agents or those
  named with `--agent`; `uninstall --user` removes them. The editing lives in
  [`userhooks`](../userhooks/userhooks.go), the events, matchers, timeouts and
  command in [`agenthooks`](../agenthooks/userwide.go). The desktop app's
  onboarding is meant to call the same command.
- The files belong to the user. Only handlers whose command is baum's
  user-wide dispatcher are added, rewritten or removed; every other byte
  stays, CRLF line endings included. An install with nothing to change writes
  nothing; uninstalling right after installing restores the original bytes,
  and a file baum created is removed once only an empty object is left. A
  file that is not JSON, or whose `hooks` are not event → list of groups →
  list of handler objects, is refused and left alone. The previous file is
  saved as `$XDG_STATE_HOME/baum/backups/<agent>-<file>` (default
  `~/.local/state/baum`, mode 0600), never beside a symbolic link's target,
  where a dotfiles repository could commit it; the link itself is kept. The
  new file is renamed into place unless the file changed meanwhile. Root
  editing another user's home (sudo with `HOME` kept) is refused.
- baum's entries are baum's: an edit made to one of them, such as a longer
  timeout, is reverted the next time setup or install runs.
- Codex keys its trust in a hook to its file, event and position (group and
  handler index), so moving a hook makes Codex skip it until the user trusts
  it again with `/hooks`. baum appends its groups after the user's and never
  moves one it placed; when an uninstall, or the relocation of a misplaced
  baum handler, moves hooks of the user's, the command lists them with that
  instruction (`Result.Shifted`).
- The command is `sh -c 'if command -v baum … && baum hooks agent --check
  --user AGENT EVENT …; then exec baum hooks agent AGENT EVENT; fi'`. Unlike
  the committed dispatcher it never falls through to Entire. It is pinned by a
  test, because Codex asks the user to trust a hook again once its definition
  changes.
- `--check --user` succeeds only when all of these hold, in this order:
  1. the working directory is inside a Git worktree (found by its `.git`,
     without starting git) whose Entire is enabled as Entire reads it: a
     `.entire/settings.json` or `settings.local.json` enables it unless an
     `enabled` key says otherwise, the local key wins, and invalid JSON
     disables it. Elsewhere the hook would only hand an event to an Entire
     that ignores it, and its session-start bootstrap would put Git hooks in a
     repository that never asked for them;
  2. no other hook the agent runs for the event calls `entire hooks AGENT
     EVENT` (the committed dispatcher, the pre-rename one and Entire's own
     command all do, so the text is searched for that; a file over 1 MiB or
     unreadable counts as calling it). For Claude Code that is the project's
     `.claude/settings.json` and `settings.local.json` (working directory up
     to the worktree root, and `CLAUDE_PROJECT_DIR`), `~/.claude/settings.json`
     and the managed settings (`managed-settings.json` and
     `managed-settings.d/*.json` in `/etc/claude-code`,
     `/Library/Application Support/ClaudeCode` or
     `C:\Program Files\ClaudeCode`). For Codex it is `~/.codex/hooks.json`
     and `config.toml`, the files in `/etc/codex`, and the project's
     `.codex/hooks.json` and `config.toml`, but those only where Codex runs
     them: the project (working directory or a parent up to the root) is
     `trusted` in `~/.codex/config.toml`, and the hook's
     `<file>:<event>:<group>:<handler>` key is trusted under `[hooks.state]`.
     Codex's trust hash is not documented, so a hook changed since it was
     trusted still counts. Plugin hooks cannot be enumerated reliably and are
     not looked at: a plugin that runs Entire for an event reports it twice.
     This is what keeps an event from being reported twice in a repository set
     up by `baum setup` or the onboarding pull request without a daemon to
     deduplicate, while an event the repository's settings lack (such as
     Codex events added in a later Entire) still arrives through the
     user-wide hook;
  3. the opt-in rules above would run the committed dispatcher here.
- The first two read a few small files and start no process: in a repository
  without Entire the whole user-wide hook costs about one `baum` start
  (measured at about 3 ms on Linux, against 2.4 ms for `baum --version` and
  4 ms for the committed path's `--check` in a repository without agent
  settings).

## Hooks

- Hooks must not break the agent or Git. Session-start bootstrap is
  best-effort within `sessionStartBootstrapTimeout` and retries next time;
  Entire setup errors are warnings; missing Entire lets the hook continue.
- A delegated agent hook's exit code propagates (signal death maps to 1),
  because the agent acts on it. A failing Git `pre-push` propagates so the push
  stops; other Git-hook failures warn and continue.

## Hook events

- Every `baum hooks agent AGENT EVENT` and `baum hooks git HOOK` run, after
  Entire returns, sends one event to the user's daemon over its control
  socket ([`internal/hookevents`](../../internal/hookevents)). `--check` sends
  nothing: where it refuses, baum does not run at all, so the opt-in rules
  above decide which hooks report. Sending changes nothing the agent or Git
  sees: Entire still reads the hook's standard input byte for byte (through a
  pipe the hook fills, keeping a copy of at most 256 KiB), its output still
  reaches them untouched, and the exit status is the one it was. The event
  writes nothing to standard output or error.
- An agent event keeps only the payload's identifiers: `session_id` (Cursor:
  `conversation_id`), `transcript_path`, `cwd`, `hook_event_name`, `source`
  and `reason`. Prompts, tool input and output and every other field are
  never kept. A Git event keeps the hook's arguments, except the remote URL a
  `pre-push` gets, which can carry credentials (its remote name is kept), and
  the lines Git writes to `pre-push` and `post-rewrite` (refs and commits).
  Every event names its working directory, exit status and baum version, and
  is bounded so it fits one control line.
- Latency: sending adds at most `hookevents.Budget` (300 ms) to a hook,
  whatever the daemon does; the request itself gets 150 ms. Against a running
  daemon a hook costs about one `baum` start more than delegating to Entire
  (measured at 5 ms against 4 ms for `baum --version`). Once Entire is done,
  the hook waits at most 20 ms more for its input to end.
- No daemon running: the event is spooled and the hook starts one (`baum
  daemon run --detached`, as `baum daemon ensure` would) without waiting for
  it to serve (15 ms measured), unless one holds the daemon lock (starting,
  stopping or not answering) or another client is starting one. The new
  daemon drains the spool when it starts. Hosted users without the desktop
  app get a daemon this way.
- A daemon that does not answer within the budget, refuses the event or
  predates it (`unknown_method`, `protocol_mismatch`, or `event_version` for
  an event of a newer format) gets it through the spool instead; a running
  daemon drains it every 30 s. One older than this baum is also asked to
  restart as this version once idle (`stop` with `when_idle`). An event that
  cannot be spooled either is dropped.
- The spool is `<state>/spool/` (the daemon's state directory, mode 0700),
  one file per event, mode 0600, written atomically. It holds at most 1000
  events and 8 MiB; a hook that would pass a bound drops the oldest events
  first. What hooks spool, drop or start, and why, goes to `<state>/hooks.log`
  (0600, rotated at 256 KiB), never an event's contents.

## Forwarding to the hosted service

- The daemon forwards hook events with `SessionForwarder`. The hosted service
  receives sessions one way, `baum upload`'s, so forwarding is that upload,
  unchanged, of the session an agent's `session-end` event names (matched by
  session id, or by transcript path): the same endpoint, archive, local
  redaction (goredact, before anything is archived) and 512 MiB bound, no
  upload for a repository the server reports not enabled in baum, and no root
  inventory, which one session's roots could not complete. Other events stay
  local.
- Only for a logged-in user: a `BAUM_TOKEN` in the daemon's environment
  (which it reads from the login shell, not from the hook) or a stored token,
  sent only to the server it was issued for. Without one the daemon logs once
  that events stay on this machine. The login is read for every event, so
  `baum login` and `logout` apply without a restart.
- As with `baum upload`, one attempt: a failure (network, server, an expired
  token) is logged with its reason, never retried, and the session stays on
  disk for a later `baum upload`. Forwarding runs in the daemon, one session
  at a time, never in the hook.

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
