# baumcli

The hosted service commands of the `baum` binary: login, logout, whoami,
setup, upload, the hook dispatchers that agents and Git call, and the
installer of user-scoped agent marketplace plugins. The open-source
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
  a baum server; the events they hand the local daemon stay on the machine
  (see "Hook events").
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
    or prompting, and skips a settings file over 1 MiB;
  - or, without any of these, a baum daemon ran for the user: its lock file
    `daemon.lock.sqlite`, which only a daemon creates, exists in its state
    directory (`$XDG_STATE_HOME/baum/daemon`, default
    `~/.local/state/baum/daemon`, found as the daemon finds it, by a `stat`,
    without contacting it). A hook's spool, or `baum daemon stop` where no
    daemon ran, leaves no such file. Such a hook is local: it does nothing
    hosted (no session-start bootstrap, no Entire baum installed outside
    `PATH`) and hands the event to Entire exactly as the fall-through would,
    with the same output and exit status (a failing `pre-push` exits with
    Entire's status, or 128 plus the signal that killed it), and sends the
    event to the daemon (see "Hook events"). A hook run decides this again
    itself, so a hook is hosted only where the hosted opt-in holds, cheapest
    check first so a hook that runs only because of the daemon never waits
    for the committed-settings read (which may take its full five-second
    timeout in a partial clone): a login or a clone marker makes it hosted;
    an open-source install, local; with no daemon ever run, hosted (`--check`
    passed through the opt-in, as before the daemon); otherwise it is local
    unless an agent settings file in the working tree carries baum's
    dispatcher, and only then do the committed files decide.
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
  but never logged in. Nothing a hook does contacts a baum server; uploads and
  every other server request need a login in every case.
- For an open-source user who never logged in and runs no baum daemon, in a
  clone nobody set up, the hooks run Entire directly and baum does nothing,
  not even the bootstrap, whatever the repository commits. The user-wide
  hooks (below) apply the same rules and, where they refuse, do nothing at
  all.
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

- Setup needs no baum sign-in: it never reads credentials for auth, calls a
  baum server or starts the device flow by default. It installs repository
  hooks (and Entire unless `--skip-entire`); user plugin installation fetches
  the public GitHub marketplace through the native agent CLI. Re-running
  uses the native idempotent installation commands.
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
- Setup also installs the user-scoped marketplace plugins below, once per run, for the agents
  it detects, but only for a hosted install or a logged-in user, where they
  add something today, and not with `--skip-user-hooks` or `--skip-entire`.
  The `--all` plan names the agents, public marketplace and network download
  before asking. An
  open-source user without a login gets them only deliberately, from
  `baum hooks install --user` or the desktop app's onboarding, once a daemon
  makes them useful there. Setup keeps writing the repository's own hooks:
  teammates without user-wide hooks need them, and the user-wide ones yield to
  them.

## User-wide hooks

- `baum hooks install --user` registers the public `baum-ai/baum` Git
  marketplace and installs/enables `baum@baum` through Claude Code or Codex's
  native CLI, for executable agents on `PATH` or those named with `--agent`.
  `uninstall --user` removes that user plugin through the native CLI, leaving
  the marketplace and other plugins. No raw hook settings are edited and no
  migration markers or compatibility cleanup are maintained. Installation
  downloads from GitHub; it needs no baum sign-in and calls no baum server.
- [`agentplugins`](../agentplugins/README.md) owns the native commands. Claude
  uses user-scope plugin install/list/uninstall; Codex uses plugin add/list/
  remove. Successful commands are verified with native list JSON. A missing
  or unsupported CLI, unexpected JSON or unverified result is an actionable
  error; install/update the agent or use its plugin UI. Each native action has
  a 90-second budget, each status read 10 seconds, with cancellation of child
  processes where supported. One failing agent does not stop the other.
- `baum hooks status --user [--json]` reads native registries without changing
  them. It reports detection of the CLI, plugin ID, installation, enablement
  and version separately; installed and enabled never imply trusted. A config
  directory alone is not an executable agent. The desktop setup screen uses
  these commands, shows disabled plugins separately and offers installation/
  enablement. It allows both agents' action budgets plus process overhead.
- Review hook trust through the agent after installing or changing plugins;
  Codex uses `/hooks`. The native registry owns installation/enablement, and
  the agent owns trust. See [manual installation, updates and removal](../../plugins/README.md).
- The events, matchers, timeouts and dispatcher are shared in
  [`agenthooks`](../agenthooks/userwide.go). The command is
  `sh -c 'if command -v baum … && baum hooks agent --check --user AGENT EVENT
  …; then exec baum hooks agent --user AGENT EVENT; fi'`. A missing/older CLI
  rejecting the check quietly does nothing. Unlike committed dispatchers it
  never falls through to Entire. The command is pinned by a test because a
  definition change requires renewed Codex trust.
- `baum hooks agent --user` runs the hook as before where the hosted opt-in
  holds. Where `--check --user` passed only because a baum daemon ran, it only
  records the event: no Entire, no bootstrap, no output, exit status 0, as
  where `--check` refuses.
- The user-wide hooks also run record-only events, which Entire has no hook
  for and only the daemon reads: Claude Code's `notification` (its
  Notification hook, every type), `post-tool-use` (PostToolUse for every
  tool, run with `async`) and `permission-request` (PermissionRequest, every
  tool, timeout 3600 s, since plugin 1.2.0), and Codex's
  `permission-request`. For them `baum hooks agent --user` records the
  event, hosted or not, and runs no Entire; it exits 0 and prints nothing,
  except as the next item says.
- The desktop app answers permission requests and questions, and sends
  messages, through these hooks (`internal/agentrequests`). Once the daemon
  took a `permission-request` event, the hook reads the whole payload (up to
  16 MiB, waiting at most 2 s for it to end), sends the daemon what the app
  shows of it (rows describing the tool call, suggestion labels, questions)
  with the `agent.request` control request, and holds until the app answers,
  the daemon withdraws the request or the hook is killed. It then prints the
  agent's answer from its own copy of the payload:
  `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{…}}}`
  with `behavior` `allow` or `deny` (a deny says, in `message`, that the user
  denied it in the baum app). For Claude Code an "always allow" carries the
  one `permission_suggestions` entry picked as `updatedPermissions`, an
  AskUserQuestion answer carries the tool's input with `answers` added as
  `updatedInput`, and an ExitPlanMode approval echoes its input there. Codex
  gets the behaviour and message only; the daemon answers a Codex request
  only when the user turned `codex_approvals` on, and the hook gives up after
  28 s so Codex, which hides its prompt meanwhile, asks in its terminal. The
  Codex hook's definition is unchanged, so its trust stands. With no daemon,
  one that did not take the event in time, an incompatible one or any error,
  the hook prints nothing and never starts a daemon to wait for.
- `post-tool-use` events of Claude Code and Codex carry `tool_digest`, the
  SHA-256 of the tool's name and canonical input (`agentrequests.Digest`),
  read from the whole payload, so the daemon withdraws a request the user
  answered in the terminal; the input itself is never kept.
- A Claude Code or Codex `stop` hook, user-wide or committed, takes the
  message the user queued in the app for its session (`agent.take-message`,
  within 300 ms, only when the daemon took the stop's event) and prints
  `{"decision":"block","reason":"<the message, introduced as sent from the
  baum app>"}`, which makes the agent go on with it. It never waits for one.
  Entire's output for the stop is held until then: when it is empty, or a
  JSON object that does not already decide or stop, the decision is added
  to it; otherwise it is printed as it is and the message stays queued. `--check --user` applies the same rules (the second
  never matches, as no hook file hands them to Entire). They are not in the
  committed vocabulary: a committed dispatcher or `baum hooks agent` without
  `--user` refuses them as unsupported, so repositories and the hosted
  onboarding are unchanged, and an older baum's `--check` refuses them too,
  leaving the hook quiet.
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
  pipe the hook fills, keeping a copy of at most 256 KiB, or 16 MiB for a
  Claude Code or Codex `post-tool-use` or `permission-request`), its output
  still reaches them untouched (a stop's carries a queued message, above),
  and the exit status is the one it was. The event
  writes nothing to standard output or error.
- An agent event keeps only the payload's top-level identifiers:
  `session_id` (Cursor: `conversation_id`), `transcript_path`, `cwd`,
  `hook_event_name`, `source` and `reason`, read from the whole payload as it
  streams, however large, keeping at most 4 KiB of each (a member given twice
  keeps its last value; a payload cut short yields the members before the
  cut). A Claude Code `notification` event also keeps `notification_type` and
  `message`, the sentence Claude Code showed, cut to 512 bytes; no other event
  keeps them. A `post-tool-use` event keeps `tool_digest`. Prompts, tool input
  and output and every other member are never kept. A Git event keeps the
  hook's arguments and the lines Git writes to `pre-push` and `post-rewrite`
  (refs and commits), but never a pre-push remote URL, which can carry
  credentials: the remote name is kept only when it differs from the URL and
  reads as neither a URL nor an scp-style address (a push to a URL passes the
  URL as both). Every event names its working directory, exit status and
  baum version, and is cut, by its encoded size, to fit one control line.
- Latency: sending adds at most `hookevents.Budget` (300 ms) to a hook,
  whatever the daemon, the file system or a process start does; the request
  itself gets 150 ms. A spool write under way when the budget ends finishes
  while the hook's process lives; a daemon start not yet begun is dropped.
  Against a running daemon a hook costs about one `baum` start more than
  delegating to Entire (measured at 4–5 ms against 3 ms for `baum --version`).
  Once Entire is done, the hook waits at most 20 ms more for its input to end.
- No daemon running: the event is spooled and the hook starts one (`baum daemon
  run --detached`) without waiting for it to serve (5 ms measured), unless one
  holds the daemon lock (starting, stopping or not answering), another client
  is starting one, or one was started less than three minutes ago and has not
  served yet (a daemon that cannot start is not restarted by every hook). Nor
  does it start one after `baum daemon stop` (which the desktop app's "Quit and
  stop background reviews" runs) until `baum daemon ensure` or a foreground
  `baum daemon run`, with `BAUM_NO_DAEMON=1`, or when `CI` is set; the events
  then wait in the spool. A hook starts the daemon with its own environment
  minus `TMPDIR`, which agent sandboxes point at a directory that lasts one
  session, and with no descriptor but its standard ones. The socket path never
  depends on `TMPDIR` (a path too long for a socket falls back to
  `/tmp/baum-<uid>`), so the daemon listens where the hook looked. The new daemon drains the spool when it
  starts.
- A daemon that does not answer within the budget, refuses the event or
  predates it (`unknown_method`, `protocol_mismatch`, or `event_version` for
  an event of a newer format) gets it through the spool instead; a running
  daemon drains it every 30 s. One older than this baum is asked to stop once
  idle (`stop` with `when_idle`): an idle one stops at once and the next hook
  starts this version; a busy one restarts as this version when its jobs
  finish. An event that cannot be spooled either is dropped.
- The spool is `<state>/spool/` (the daemon's state directory, mode 0700),
  one file per event, mode 0600, written atomically. It holds 1000 events
  and 8 MiB: a hook that would pass a bound drops the oldest events first;
  hooks spooling at the same moment can each add one past it, and the next
  one restores it. What hooks spool, drop or start, and why, goes to
  `<state>/hooks.log` (0600, rotated at 256 KiB), never an event's contents.
- The daemon records the events locally only. A session end or a push in a
  registered clone also makes it refresh that branch's pull requests from
  GitHub at once ("Refresh on hook events" in
  [ARCHITECTURE.md](../../ARCHITECTURE.md)). Nothing is forwarded to the
  hosted service yet: that waits for a hosted event endpoint, and hosted
  users' sessions keep reaching it through Entire's checkpoints and
  `baum upload`.

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
