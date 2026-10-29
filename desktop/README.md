# Desktop app

A [Tauri](https://tauri.app) v2 shell around the user's `baum daemon`. The
shell adds no review features: the daemon serves the local application
(`../app`) over loopback HTTP, and the window shows it. The shell handles only
what the operating system has to: the window, the tray, start at login,
notifications, installing the `baum` command and agent marketplace plugins,
and later signing and updates.

The app bundles the release `baum` binary next to its executable. `baum`
itself is unchanged and stays `CGO_ENABLED=0`.

## Attaching to the daemon

There is one daemon per user, shared by the app and coding-agent hooks. Either
may start it, and it outlives the app: quitting the app leaves it running.

1. The window opens on a bundled connecting screen (`../app/src/desktop/connecting.tsx`).
2. The app runs the bundled `baum daemon ensure --json`, which starts the daemon
   if none is running and prints its status as one JSON line. A daemon that
   hooks already started is the normal case. The command has 45 seconds.
3. The app reads the launch URL from `credential.json` in the status's `state`
   directory. The file must be private (mode 0600, owned by the user on Unix).
   The URL must be `http://127.0.0.1:<port>/#token=<capability>`, and its
   origin must match the status's `app` field.
4. The window loads that URL. The daemon's bootstrap page exchanges the
   capability for a session cookie, as it does in a browser. The capability is
   never shown or logged.
5. While attached, the app checks the daemon's `/api/health` every 5 seconds.
   - If the daemon rejects the credential, a newer daemon has replaced it, and
     the app attaches to that one, once.
   - After three failures in a row, the window returns to the connecting
     screen. That screen shows the last lines of `daemon.log` and `daemon.out`
     from the state directory, and a button to start the daemon again.
   - While that screen shows, the app watches the credential file. When
     another client starts a new daemon (a hook that finds none running starts
     one in the background, except after `baum daemon stop`), the app
     attaches to it.
   - The app never restarts the daemon on its own, so a crashing daemon
     cannot restart in a loop.

A second launch focuses the existing window instead of opening another one.

## First run: the baum command and the hooks

First run uses a three-step setup in the landing page's sand, espresso and green
palette, set like every screen of the app in its bundled type: Instrument
Sans for text and Manrope for headings, both under the SIL Open Font License
(`../viewer/src/fonts/`). Studio Feixen, the website's font, is licensed for the
web only and is never bundled in the app. The screen is a typed React entry in
`../app/src/desktop/`, built alongside the local application by its Vite build.
Vite writes the daemon assets to `../internal/localapp/dist` and assembles the
native assets in `dist/`, where the connecting screen is `index.html`. Tauri embeds
`dist/` so setup stays on its local origin with the existing native permissions;
daemon-served pages may open the native folder picker (below) or manage
the bundled CLI and agent hooks directly in Settings. Each step is optional and has Back
and Continue controls; changes happen only when the user selects an install
or add action. Displayed paths abbreviate the current home directory as
`~`; actions still use the full path:

1. Connect GitHub and pick repositories.
   - Device sign-in: the app attaches to the daemon while setup shows, and the
     step reaches the daemon's GitHub sign-in through the setup screen's
     `github_connection` command, which relays status, start, cancel and retry
     over the tray's loopback client; disconnecting stays in Settings. Signing
     in opens GitHub's device page in the default browser and copies the code
     in the same click. The daemon runs the device flow and keeps the
     credential, so the screen never holds a token.
   - Below it, pick GitHub repositories from the signed-in account. The list
     shows the first eight and any picked ones; search finds the rest. Rows show
     GitHub details only and nothing is picked on its own. Once a repository
     is picked, baum looks for its clone in folders agents worked in and common
     code folders, with the nine-dots loader while it looks, then shows the
     clone it found or says none was found. A picked repository without a
     clone offers Choose folder and Clone it (with an editable default folder);
     unticking it unpicks it. A picked repository without a clone still lists its pull requests;
     generating waits for a clone. Repositories show only once GitHub is
     connected: baum works with GitHub repositories only, with no local-folder
     fallback. On macOS, Also look in Documents
     starts a single opt-in scan. The setup `repositories` command relays only
     allowlisted list, pick/unpick, discovery, folder registration and clone
     actions to the daemon. It cannot provide arbitrary HTTP routes or see a
     GitHub token. Explicit folder attachment may file a fork under its
     upstream repository when a remote matches it.
2. Set up reviews: the harness (automatic, Claude Code, Codex, Cursor Agent or
   the Anthropic API), its model (the harness default, a listed model or a
   custom one), whether reviews run automatically and, if so, for which pull
   requests (authored, requested, both, or all open pull requests). The
   screen reads them with `baum internal reviews show` and saves changes with
   `baum internal reviews set` when the user opens baum at the end of setup.
   They are stored as app preferences in the repository registry, which the
   daemon rereads for every job.
3. Install the CLI and connect agents.
   - Install the bundled `baum` command onto the login shell's `PATH` (below).
     The step states in one line where baum will be installed, or which other
     baum it replaces; PATH and administrator warnings show beneath it.
   - Connect the agents you use, with separate Connect or Enable buttons. The
     Codex marketplace plugin is installed with `baum hooks install --user
     --agent codex`. The screen explains reviewing trust with `/hooks`. The
     Claude Code plugin is installed separately with `--agent claude-code`.
     Both use the native agent CLI to fetch the public marketplace.
     Installation and enablement do not mean the user has trusted the hooks.

Failed actions leave the user on the step with their output and a retry action.
"Check again" rereads the installed state. Finishing records `setup.json`,
attaches to the daemon and opens the inbox. If diagnostics find a problem,
the inbox shows a notice linking to the fixes. Later, "Command Line and Hooks…"
in the tray, or the menu bar on macOS, reopens the same steps, with removal controls for
the command and each agent plugin. Settings also provides these controls inline, without reopening setup.

Setup, Diagnostics and Settings offer GitHub device sign-in: copy the code and open
GitHub in the default browser. The daemon completes sign-in independently of
the window and saves credentials in the operating system credential store.
Disconnect removes the saved credential; explicit environment/config overrides
remain effective, and saved reviews stay readable.

The screen gets the facts from the bundled baum: `baum daemon login-path
--json` resolves the login shell's `PATH` as the daemon does and names the
first `baum` on it (the one hooks run) with its version. On Unix, the login
shell’s PATH comes first and inherited PATH directories are appended without
duplicates, preserving tools available to the process that launched the app.
`baum hooks status --user --json` reads each agent's native plugin registry, reporting
plugin ID, installation and enablement separately. Disabled plugins offer an
Enable action; trust must be reviewed in the agent. Each native action has a
90-second bound and status queries have 10 seconds. The desktop gives both
agents, login PATH resolution and process overhead a 240-second total budget.
Status, install and removal run with the resolved login shell PATH so agents
installed through Homebrew or npm are found from GUI launchers too. If login
PATH resolution fails, the app uses its inherited PATH and displays the reason.

### Where the command goes

- **macOS and Linux:** a symbolic link named `baum` to the baum the app runs.
  - If a different `baum` comes first on the `PATH` (`go install`, a release
    archive), the screen shows its path and version, and replaces it only
    when the user clicks "Replace <path>": that file is moved aside to
    `<path>.replaced-by-baum-app` and the link takes its place, so it keeps
    its position on the `PATH`. One a package manager owns (Homebrew's
    `Cellar`, the Nix store, `/usr/bin`, `/bin`, `/usr/sbin`, `/sbin`, or an
    asdf or mise `shims` directory) is not replaced: the screen says to remove
    it with that manager (for example `brew uninstall baum`).
  - Otherwise the link goes in the first of `~/.local/bin` and `~/bin` that
    is on the `PATH`, else in `/usr/local/bin`, else in `~/.local/bin` with a
    note to add it to the `PATH`.
  - A directory the user cannot write is changed through the system's
    password prompt (`osascript` with administrator privileges on macOS,
    `/usr/bin/pkexec` on Linux) only when it is a system directory: outside
    the home directory, existing or with an existing parent, reached without
    symbolic links, owned by root and writable by no one else, below
    directories that are root's and not writable by others either (sticky
    ones such as `/tmp` aside); in practice `/usr/local/bin`. Anywhere else
    the screen says what is in the way instead.
  - Root runs a fixed script that receives the paths as arguments. It
    refuses (exit 4) anything but absolute paths, the link `<dir>/baum` and
    a backup `<dir>/baum.replaced-by-baum-app` with at most a numeric suffix.
    It changes into the parent and then the directory (creating only the
    last one), checks that the physical path is the one named and that root
    owns it and no one else can write it, and then works by relative names
    with `--`, so a directory swapped in after the app's check is never
    followed. Uninstalling also requires the link to be a symbolic link to
    the recorded target (exit 3 otherwise). If placing the link fails, the
    replaced baum is put back. These scripts were exercised as root in a
    container against path traversal, newlines, option injection, relative
    paths, an empty target and a directory swapped for a symbolic link.
  - The link never needs a password when it would point into the home
    directory (an AppImage's copy, an app in `~/Applications`): the user can
    rewrite that file, and a root-owned link to it would put it on every
    user's `PATH` and on sudo's `secure_path`. Such a link goes in
    `~/.local/bin` (created if needed, with the note to add it to the
    `PATH`); if a baum in a system directory would still come first, the
    screen says to remove it or reorder the `PATH`.
  - The link points at a stable path: the binary inside the installed app
    bundle (`baum.app/Contents/MacOS/baum`, or `/usr/bin/baum` from the deb),
    which updates replace in place. On macOS an app still on its disk image
    or translocated by Gatekeeper must be moved to Applications first.
  - An AppImage runs from a mount that disappears when it quits. The app
    keeps a copy of its baum in its data directory (`bin/baum`, mode 0755),
    checks it before every run of baum and copies again when the stamp
    beside it (`baum.stamp`: the bundled binary's size and modification time
    and the copy's modification time) does not match, so a changed app or a
    rewritten copy heals; the new file is renamed into place, so a daemon
    running the old one keeps it. It runs that copy for everything: the
    daemon, `login-path` and the hooks commands. A copy that cannot be made
    is an error on the connecting screen, whose Retry tries again; the app
    never falls back to the binary in the mount. The link points at the copy,
    and removing the command leaves the copy. The app counts as an
    AppImage only when `APPIMAGE` and `APPDIR` are set and its executable is
    under `APPDIR`, since programs started from an AppImage inherit
    `APPIMAGE`.
  - The deb's `/usr/bin/baum` already is the app's binary: the screen shows
    it as installed and leaves it to the package manager.
  - A link the app made whose app was since deleted points nowhere; shells
    and the hooks' `command -v baum` skip it, so the hooks do nothing. The
    next install replaces such a broken link without asking, and the app's
    own link to an older copy of the app is updated without asking.
- **Windows:** the app keeps a copy of its `baum.exe`, alone in `bin` in its
  local (not roaming) data directory and refreshed the same way (a running
  copy is renamed aside under a name of its own, and earlier ones are
  removed once they no longer run), runs that copy, and adds the directory to the front of the
  user's `PATH` (the registry value, keeping unexpanded variables, compared
  with them expanded so `%LOCALAPPDATA%\…` counts as the same directory;
  refused if the value would pass 32,000 characters). New terminals pick it
  up. A `baum` in a system `PATH` directory still comes first; the screen
  says so.

`setup.json` is the user's file, so the app uses a record only when it has
this shape (a link named `baum`, a backup named as above, an absolute target
named `baum`), still blocks a package manager's baum and asks before
replacing anything it did not itself link, and asks for a password to remove
a link only when the record names the baum the app runs now. Remove deletes only what the app recorded: the link, while it
still points where the app put it (a link someone changed since is left
alone, and the screen names a backup still beside it), and puts back a baum
it had replaced; on Windows it takes the directory off the user `PATH`.
`setup.json` lists every link the app made and has not removed: installing
in a new place removes the earlier one, and one that cannot be removed (a
cancelled password prompt) stays listed, shown on the screen, until Remove
succeeds for it. Updating the app's own link keeps the backup of the baum it
replaced, so Remove still puts that back. (A `setup.json` of the first
release, with a single `command`, is read as a one-item list.)
Hooks are removed with the bundled `baum hooks uninstall --user`, which
removes only the native `baum@baum` plugin and leaves the marketplace and
other plugins.

### Restarts into the app's baum

A busy older daemon restarts into a newer baum only if it trusts the
executable (`internal/daemon/trust.go`): its own, or the `baum` on its login
shell's `PATH` after resolving links, owned by the user or root and not
group- or world-writable, in directories that pass the same test up to `/`.
Installing the link makes the bundled binary the `baum` on the `PATH`, so:

- Linux deb (`/usr/bin/baum`) and the AppImage's copy in the user's data
  directory are trusted (the copy only while the home directory and
  `~/.local/share` are not group-writable, which a umask of 002 can cause).
  The binary inside an AppImage's mount (under `/tmp`) never would be, which
  is one more reason the app runs the copy.
- macOS: `/Applications` is `root:admin`, mode 0775. The trust check accepts
  a directory that only the admin group may also write (its members can
  become root anyway), so an app there or in `~/Applications` is trusted, as
  is Intel Homebrew's `/usr/local/bin`.
- Windows checks only that the file is the daemon's own or the `PATH` baum,
  which the user `PATH` entry makes it for daemons started after the change.

## Tray, closing and quitting

The daemon outlives the app, so the app's presence is a tray icon.

- **Closing the window hides it.** The page keeps its state and reopening it
  (tray Open, a second launch, the Dock icon on macOS) shows it as it was; it
  does not reconnect.
- **Where no tray is available** (some Linux desktops, which have no
  StatusNotifier host), nothing could reopen a hidden window, so closing it
  asks "Quit baum?" and quits the app, leaving the daemon running. The app asks
  the session bus with `busctl` or `gdbus` each time it matters, so a host that
  starts after the app counts; when neither tool exists it assumes no tray and
  keeps the window reachable. A start at login waits up to 10 seconds for the
  host before it shows the window after all. The deb depends on
  `libayatana-appindicator3-1` (the Tauri bundler adds it for the tray), and a
  tray library that fails to load leaves the app running without a tray.
- **Tray menu:** what the daemon is doing (Idle, "2 running, 1 queued");
  anything needing attention; each agent session asking for a permission or
  an answer (clicking one opens its transcript); "Plan usage left" with a
  line per readable plan window ("Claude Code · 5-hour: 62% left · resets in
  3 h"); Open baum; Pause or Resume automatic reviews; Start at login; Command
  Line and Hooks… (see "First run"); a Notifications submenu (see
  "Notifications"); Quit; Quit and stop background reviews. Plan usage shows
  only the plans the daemon could read, and nothing when it can read neither
  Claude Code nor Codex. The menu is rebuilt whenever a "resets in" text would
  read differently, which the 3–10 s polls check.
- **Tray panel (macOS and Windows):** a left click on the icon opens a small
  borderless window under the icon (above it, on a bottom taskbar), drawn
  like a menu: the status line, waiting sessions as highlighted rows that
  open them, the plan usage left as coloured meters, Open baum, Pause or
  Resume automatic reviews and Quit and stop background reviews. It hides
  when it loses focus or on Escape. As in a native menu, `↓` and `↑` move
  through its items (from the first or last, wrapping), `Home` and `End` go
  to either end, and `Enter` or `Space` presses the focused one. The right
  click keeps the full menu. Linux tray hosts
  report no clicks, so Linux has the menu only. The panel is the bundled
  `tray.html`, created hidden at startup.
- **Waiting on you.** While some pull requests wait on the user (review
  requested from them, or their own with changes requested, failing checks or
  a conflict, the "Review requested" and "Needs changes" groups of the app's
  Pull requests),
  the menu shows a disabled line such as "3 pull requests waiting on you", the
  tooltip appends it, and the dock badge (macOS; any other platform whose
  window badge Tauri supports, ignored silently elsewhere) shows the count.
  Zero, an unknown count and a daemon that is not running clear all three. The
  daemon counts them: `GET /api/index?view=waiting`, polled with the other
  tray requests.
- **Quit** ends the app only. Background refresh and automatic generation
  continue in the daemon.
- **Quit and stop background reviews** runs the bundled `baum daemon stop`, so
  the daemon stops and is marked stopped by the user: hooks do not start it
  again until the app or `baum daemon ensure` does. When a review is running
  the app first asks, saying it will be interrupted (queued reviews have not
  started and are not mentioned as interrupted). If the daemon will not stop,
  the app shows the error and stays open.
- **Pause automatic reviews** sets the daemon's global automatic switch
  (`PUT /api/automatic {"enabled":false}`), the same switch as the app's
  settings, so queued automatic jobs are cancelled and running ones finish.
  The item reads Resume while it is off.

### Icon states

| State | When |
| --- | --- |
| Idle | Nothing running or queued, nothing new to look at. |
| Running | A review is running or queued. |
| Attention | A review failed, a diagnostics check reports a problem, or the daemon is not running. |

Attention for the first two clears when the window is opened or focused and
returns for something new (a failure found when the app first looks at the
queue is not new; the daemon keeps finished jobs). Diagnostics warnings do not
count. The menu lists what needs attention, and the tooltip shows the summary.

### How the app talks to the daemon

Two channels, each the least that does the job:

1. **The bundled `baum` command** for what changes the daemon's lifecycle:
   `daemon ensure --json` (attach) and `daemon stop`. It reaches the daemon
   over its control socket, owns the stopped-by-user marker, and the app needs
   no credential for it.
2. **The app API on loopback, with the launch capability as a bearer token**,
   for what the daemon exposes only there: `GET /api/jobs`,
   `GET /api/automatic`, `GET /api/index?view=waiting` and
   `GET /api/live-sessions` every 3 s while jobs run and every 10 s otherwise,
   `GET /api/diagnostics` every 5 minutes (it runs the doctor's checks),
   `GET /api/plan-usage?refresh=1` every 5 minutes (a fresh reading; the
   daemon reads again at most once a minute), and
   `PUT /api/automatic`. This is the credential the app already reads for the
   window and the health check, accepted on `/api/` routes only; the request
   that changes state sends the daemon's own origin and JSON, as the daemon
   requires. It is polled rather than streamed: the events stream carries only
   change notices, so the tray would fetch the same data anyway.

Notifications read the events stream separately (see "Notifications").

The tray menu adds no Tauri capability, and none names a remote URL: the
tray, the dialogs and the autostart entry are used from Rust only, so pages
the daemon serves still reach no command or plugin. The tray panel's
capability (`tray-panel`) lets only its bundled page, in its own window, read
the tray's last view (`tray_panel`), fit the window to its height
(`tray_panel_height`) and run Open, pause or resume, Quit and stop or open a
waiting session, or only hide the panel (`close`) (`tray_action`, which
refuses any other menu id).

## Start at login

Off by default. Toggle it from the tray menu or the window's **Background**
menu (the app's settings: the pages the daemon serves cannot call the app, so
the toggle is native). On, the operating system starts the app at login with
`--hidden`: the window stays closed (it opens if there is no tray), and the app
runs `baum daemon ensure`, so the daemon and automatic reviews resume after a
reboot. Starting it this way clears a stopped-by-user mark, which is what
turning start at login on asks for.

| Platform | Mechanism |
| --- | --- |
| Linux | XDG autostart entry `$XDG_CONFIG_HOME/autostart/baum.desktop` (default `~/.config`) |
| macOS | LaunchAgent `~/Library/LaunchAgents/com.usebaum.desktop.plist` with `RunAtLoad` |
| Windows | Per-user Run key value `baum` under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, set with `reg.exe` |

The entry names the running executable (for an AppImage, its own path; a
trailing " (deleted)" that Linux adds to a replaced executable is dropped). An
enabled entry is rewritten on every start of an installed build, so moving or
updating the app keeps it working; development builds, `BAUM_DESKTOP_BAUM` runs
and binaries under `target/debug` or `target/release` never write one. On macOS
an app run from a quarantined download (App Translocation) cannot turn it on:
the app says to move it to Applications first. Turning it off removes the entry.

An entry the desktop environment turned off counts as off and is never
re-enabled by the app: on Linux `Hidden=true` or
`X-GNOME-Autostart-enabled=false`, on Windows a disabled StartupApproved value
(Task Manager's Startup tab). Turning it on from the app clears that disable
and, on Linux, rewrites only `Exec` and the enabled flag, keeping any other keys.

The macOS LaunchAgent is temporary: once the app is signed (release work),
SMAppService login items replace it.

## Notifications

The app turns daemon events into operating system notifications. It reads the
daemon's `GET /api/events` stream itself (`src/notifications/`), so they
arrive while the window is hidden (closing it hides it) or minimised, for as
long as the app runs. Where no tray exists, closing the window quits the app
(see "Tray, closing and quitting"), and notifications stop with it. Pages the
daemon serves are not involved.

| Notification | Daemon event |
| --- | --- |
| Review finished, with the review's risk score | `notice` `review_finished` |
| Review failed, with its failure category | `notice` `review_failed` |
| Review requested from me | `notice` `review_requested` |
| Review generated automatically | `notice` `review_automatic` |
| My pull request: checks failing | `notice` `checks_failed` |
| My pull request: merge conflict | `notice` `conflict` |
| My pull request: changes requested | `notice` `changes_requested` |
| My pull request: ready to merge | `notice` `ready_to_merge` |
| Fix pushed, with its short commit | `notice` `fix_pushed` |
| Fix failed, with its failure category | `notice` `fix_failed` |
| Agent needs my permission | `notice` `session_permission` |
| Agent waiting for my answer | `notice` `session_input` |

The agent session notifications read like "Claude Code needs your
permission · acme/demo" or "Codex is waiting for your answer · acme/demo",
with the line the agent showed (masked by the daemon; Codex sends none, so
"It asks to run a tool.") and the branch and pull request as the text. The
daemon publishes one when a session begins waiting for a permission or an
answer, once per wait, never for a turn that just finished (see "Live
sessions" in ARCHITECTURE.md).

The pull request notifications read like "Checks failing · acme/demo #12",
with the pull request's title as the text. They are about the user's own pull
requests and fire when the state changes between two daemon polls, never for a
pull request seen for the first time or for the daemon's first look at a
repository.

`notice` events are published by the daemon (`oss/internal/notices`, see
`ARCHITECTURE.md`). The text comes only from fields the daemon already
redacted: the repository, the pull request title as GitHub returned it
(control characters stripped, 200 characters at most), the stored risk score,
the failure category, and an agent session's branch and masked message. A title is shown unchanged; where a Linux
notification server renders markup, its `<`, `>` and `&` are escaped so the
title is not interpreted.

- **Click.** Clicking a notification shows the window if it is hidden or
  minimised, brings it to the front and loads
  the pull request (`?clone=<id>&pr=<number>`), for an agent session its
  transcript (`?page=sessions&session=<agent>:<id>`) or, for a notice with no
  registered clone, the jobs list (`?page=jobs`). The app learns
  the daemon's secret app path with one `POST /api/auth/exchange` and loads the
  URL itself; the page's own scripts do not navigate. Linux, macOS and Windows
  toasts report clicks through `notify-rust`. The logic is unit-tested with a fake
  platform; no real notification was shown in testing.
- **Focus.** No notification shows while the window has focus on the same pull
  request (the same clone and number), nor an agent session's while it has
  focus on that session's transcript.
- **Permission.** The app never asks at launch. The operating system is asked
  when the first notification is due. Linux and Windows have no per-app prompt
  and macOS prompts when the first notification posts; `notify-rust` does not
  report a macOS refusal, so a refusal there simply shows nothing. A refusal
  the platform does report is remembered and the app stops trying; switching a
  kind on again in the menu asks once more.
- **Settings.** Each kind is on by default, and so is a kind added after
  `notifications.json` was written. A "Notifications" submenu with one
  check item per kind is in the macOS menu bar and in the tray menu. Linux and
  Windows windows have no menu bar, so there the tray holds them. The choices
  and the permission answer are saved in `notifications.json` in the app's
  config directory
  (`~/.config/com.usebaum.desktop/` on Linux). Those saved settings are the
  only copy of the state: a click on either menu flips them, then both menus
  are rebuilt or updated from them. Native menus need no page capability.
- **Authentication.** The daemon accepts one bearer credential, the launch
  capability the app already holds for `/api/health`; it has no events-only or
  read-only scope. The app sends it only in an `Authorization` header, only to
  `GET /api/events` and the exchange, and never in a URL or log. The stream
  starts with `Last-Event-ID` beyond the latest event, so the daemon sends only
  new events and history is never shown as news; after a reconnect the app
  resumes after the last event it saw. A review requested or finished while the
  daemon was down is not announced.

## Zoom

The window zooms with the webview's own page zoom (WebKitGTK, WebView2 or
WKWebView), not CSS or the desktop's scale factor. It therefore works the same on X11,
Wayland, macOS and Windows, and it scales every page the window shows: the
bundled screens, the daemon's pages and any page added later, without that page
doing anything (`src/zoom.rs`).

- **Shortcuts.** Ctrl (Cmd on macOS) with `+` or `=` zooms in, `-` zooms out
  and `0` returns to 100%, from 67% to 200% in browser-like steps. Linux and
  Windows windows have no menu bar to hold accelerators, so the window injects
  a small script (`src/zoom.js`) into every page it loads. The script calls
  `step_zoom` and announces the new level to the page as a `baum:zoom` event.
  It matches the typed character and, for layouts where it differs (AZERTY's
  0 key types `à`), the physical key.
  The webview's built-in zoom keys, mouse-wheel and pinch zoom are not used.
- **Settings.** The desktop Settings page has a Zoom card with the same levels,
  through `zoom_state` and `set_zoom`. It follows changes made by the shortcuts.
  It stays hidden when the app does not offer these commands, as when a
  different daemon's pages show in an older app.
- **Saved.** The level is saved in `window.json` in the app's config
  directory and applied when the window opens and whenever a page starts
  loading. A missing, damaged or unknown level is 100%.

## What pages may do

- The window loads only the bundled screens and the attached daemon's origin.
  Of the bundled files it shows exactly `/` and `/index.html`, and
  `/setup.html` only while the app shows the setup screen; any other path,
  including other spellings the asset server maps to a page (`/setup`,
  `/setup.html/`, percent-encoded names), is refused. The bundled pages'
  content security policy has `frame-ancestors 'none'`.
  `http` and `https` links to other hosts open in the default browser. Other
  loopback servers, other schemes and new-window requests are refused.
- Tauri treats the daemon's pages as remote. One capability names them,
  `daemon-pages`, for `http://127.0.0.1:*` in the main window: it allows CLI and hook status, installation and removal, plus
  `choose_repository`, the native folder picker the Repositories page's Browse
  button opens, which changes nothing and returns only the chosen folder. The
  page registers it through the daemon's own API. It also allows the zoom
  commands (`zoom_state`, `set_zoom` and `step_zoom`), which accept only the
  offered levels. They can call no other Tauri command or plugin.
  `withGlobalTauri` is off.
- The bundled screens have two capabilities, for the main window's bundled
  origin only: `connecting-screen` allows the app's `connection` and
  `reconnect` commands, and `setup-screen` the setup commands
  (`setup_state`, `install_command`, `uninstall_command`, `install_hooks`,
  `uninstall_hooks`, `choose_repository`, `add_repository`, `review_settings`,
  `save_review_settings` and `finish_setup`). Both also allow `step_zoom`
  for the zoom shortcuts. Every change they make runs in the
  Rust side and only when the user requests it in setup or Settings;
  replacing a different baum also requires the confirmed path. Daemon pages
  share `setup_state`, the four CLI and hook install/removal commands and
  `choose_repository`. GitHub connection, repository registration, review
  settings and finishing setup remain restricted to the bundled setup screen.
- The opener plugin is used only from Rust, to open links in the browser. The
  dialog and updater plugins are used only from Rust too (see Updates). No
  page is granted any plugin's commands.

The app itself makes no network requests other than loopback requests to the
daemon and the update checks below. Outside its own configuration and data
directories it writes only what the user installs through setup or Settings: the `baum` link
(or the user `PATH` entry on Windows), and, through the bundled baum, the
agent marketplace plugin installations. Notifications use the operating system's
notification service (`notify-rust`) only from Rust.

## Updates

A release build checks for a newer release about 30 seconds after it starts
and then once a day (by the wall clock, so a computer that slept through the
day checks when it wakes). It reads `latest.json` from the latest GitHub
release of baum-ai/baum. Local and snapshot builds (version 0.0.0) and debug
builds never check; `BAUM_DESKTOP_UPDATES=0` turns checks off.

- The macOS app, the Linux AppImage and the Windows installer update
  themselves. The app downloads the update in the background; the download
  must carry a signature from the release key whose public half is
  `plugins.updater.pubkey` in `tauri.conf.json`, made for the version the
  manifest announces (`requireSignedVersion`), or it is discarded. While the
  daemon runs a generation job (`baum daemon status`, then `GET /api/jobs`;
  queued jobs survive a restart), the app waits. Then it asks; **Restart now**
  installs and restarts the app, **Later** keeps the download and asks again
  at the next daily check. A download that receives nothing for two minutes
  is abandoned until the next check. On Windows the app stops the daemon
  first, and the installer (whose hook stops it too) closes the app, updates
  it for the current user and starts it again; the updated app refreshes its
  copy of `baum.exe` and starts the daemon. An updated AppImage refreshes its
  copy of `baum` in the data directory on the next start, renaming the new
  file into place, so a daemon still running the old copy keeps it until the
  updated app's `baum daemon ensure` replaces that daemon.
- A deb install, or any copy the updater cannot replace (an AppImage in a
  directory the user cannot write, an app on a read-only volume), only says
  that a new version exists, once per version and app start, and offers to
  open its release page. Updating it is the user's job, as with any package.
  A version whose installation failed is remembered (`updates.json` in the
  app's config directory) and from then on only announced this way.
- Restarting the app does not stop the daemon. The first client to run the
  new `baum` replaces an older daemon, as after any upgrade.

## Development

`just desktop::frontend` runs the shared local-app Vite build in `ui-test`.
Desktop check, test, build, bundle, run and dev recipes build that frontend first.
Rust builds and checks run in the `desktop-test` Docker service (Rust, the
WebKitGTK and GTK development packages, and the Tauri CLI). The bundled
binary is built in `oss-test`. From the repository root:

```sh
just desktop::check         # cargo fmt --check, clippy -D warnings, tests, updater-test
just desktop::test          # tests only; pass cargo test arguments after it
just desktop::sidecar       # build binaries/baum-<target triple>
just desktop::build         # Linux bundle (deb by default) in target/release/bundle
just desktop::bundle        # the release's Linux deb and AppImage in target/release-bundles
just desktop::updater-test  # updater manifest checks reject tampered releases
just desktop::run           # run the app on this Wayland session's display
just desktop::dev           # native host app, with Vite hot reload in Docker
just desktop::icons         # regenerate icons/ from icons/source.svg
```

`run` starts the container's own daemon in the container's home directory,
not yours. Set `BAUM_DESKTOP_BAUM` to run a different `baum` binary than the
bundled one.

For UI work and host integration, use `just desktop::dev` on Linux in a
Wayland or X11 session. Vite, dependencies and generated frontend code stay in
Docker; the CLI sidecar is built in `oss-test`. The native Rust app is compiled
and run on the host, with separate output in `target/host-dev`. This is an
intentional host execution exception: a container cannot resolve the host user's
login environment, installed agent CLIs, repository paths, credential agents or
native desktop services faithfully by mounting a display socket. The launcher
uses your real home directory and the shared host daemon, so installation and
repository actions affect your actual machine. It respects `BAUM_DESKTOP_BAUM`
when set; otherwise it atomically refreshes `target/host-dev/bin/baum` from the
freshly built sidecar and runs that copy. This gives installation a stable target
named `baum`, as the install-record validation requires; the platform-suffixed
sidecar filename is only the build artifact. Existing daemons can finish using
the previous executable after the copy is replaced.

The host needs Rust (at least the version in `Cargo.toml`), a C toolchain,
pkg-config and the Tauri GTK/WebKit development libraries. On Debian/Ubuntu,
install `build-essential pkg-config libwebkit2gtk-4.1-dev libgtk-3-dev
libayatana-appindicator3-dev librsvg2-dev libssl-dev libxdo-dev`. Docker continues
to own checks and release builds. This launcher currently requires Linux host
networking; macOS and Windows need a different loopback bridge.

Saving React components or CSS in `oss/app` or `oss/viewer` refreshes the window,
including the setup screen. Native Rust and connecting screen changes require
restarting the command. Ctrl-C removes the Vite container and its private shared directory;
the host daemon continues running, just as when the normal desktop app closes.

Only debug builds with `BAUM_DESKTOP_DEV_DIR` set use the development frontend
on port 1420. The private directory shares only the daemon origin with Vite,
never its launch capability. Vite uses Docker host networking and binds only
`127.0.0.1`, allowing the host window and the container proxy to reach the host
daemon without exposing a development port beyond loopback. It proxies the normal
login exchange and namespaced API, including event streams, rewriting the expected
development Origin for CSRF validation. The setup screen also loads from Vite,
at `http://localhost:1420/setup.html`. Tauri grants commands by origin, not
path, so setup uses `localhost` while the daemon's pages use `127.0.0.1`; a
capability added at runtime grants only the `localhost` origin the setup
commands, and the window may navigate only to the setup page there. Release builds ignore the override.

Tauri expects the bundled binary at `binaries/baum-<target triple>`, which
`scripts/sidecar.sh` builds (run it from the module root; it takes the triple
as an argument and defaults to the Go host). Every build, including
`cargo check`, needs it. `binaries/`, `target/` and the generated `gen/` are
ignored.

## Releases

The desktop app is released with the CLI, from the same `v*` tag, to the same
GitHub release. Pushing the tag to baum-ai/baum runs
`.github/workflows/release.yml`:

```text
test, viewer-dist, desktop-test          checks of the tagged release commit
  └─ release                             parent on main? goreleaser: CLI archives, draft release
       └─ desktop (desktop.yml)          unsigned bundles per platform; no secrets
            ├─ sign-macos    ┐
            ├─ sign-windows  ├─ environment "release": sign what desktop built
            └─ sign-linux    ┘
                 └─ desktop-manifest     latest.json, desktop-checksums.txt; read-only
                      └─ desktop-publish attach files, latest.json last, publish the draft
```

- A tag points at a release commit: a commit on `main` plus the built viewer
  and local app (`internal/viewer/dist`, `internal/localapp/dist`), which no
  other commit carries, so `go install` of the tag needs only Go.
  `viewer-dist` rebuilds both from the tagged sources and fails unless they
  match what the release commit carries.
- `release` fails unless the tagged commit has one parent, that parent is on
  `main`, the commit changes nothing outside the two built directories, and
  the previous `v*` tag in version order was made from a commit in the
  parent's history (both with `git merge-base --is-ancestor`; the same
  parent is allowed, so a release candidate can be promoted). goreleaser creates the release as a draft with empty
  notes (a re-run reuses that draft), and keeps its `baum` binaries as the
  artifact `cli-binaries`.
- `desktop` compiles the app around those binaries with `scripts/bundle.sh`,
  on Linux in this directory's Docker image. This is where third-party code
  runs: hundreds of crates' build scripts and macros, and the bundler's
  downloads (linuxdeploy, NSIS). It has no secrets, and `bundle.sh` refuses to
  run if a signing variable is set. It produces the deb and AppImage, the
  unsigned macOS app (as a tar) and the unsigned Windows installer.
- The three sign jobs use the `release` environment, the only place the
  signing secrets live. Each waits for a reviewer's approval (all three
  together), and the environment accepts only `v*` tags. They compile
  nothing: they run Apple's tools (`codesign`, `notarytool`, `stapler`,
  `hdiutil`) through `scripts/macos-finalize.sh`, Microsoft's `signtool`
  through `scripts/windows-sign.ps1`, and minisign 0.12 (the official binary,
  pinned by SHA-256 in `scripts/install-minisign.sh`) through
  `scripts/updater-sign.sh`, which signs each updater bundle for the release
  version. OS signing comes first, so the updater signature covers the final
  file. Each job downloads only its own platform's artifact, by name, and
  takes only that platform's file names from it; `updater-sign.sh` and
  `updater-manifest.sh` refuse any minisign but the pinned one there.
- `desktop-manifest` downloads each sign job's artifact by name, accepts
  only that platform's file names and fails if two artifacts carry the same
  name. It writes `latest.json` with `scripts/updater-manifest.sh` and checks
  every bundle in it against the release key, the version and its expected
  download URL. If the environment holds the updater key but a
  signature is missing, it fails. It also writes `desktop-checksums.txt`.
- `desktop-publish`, the only job besides `release` with a token that can
  write, attests the files' build provenance, uploads exactly the expected
  files (`latest.json` last), and only then publishes the draft.

To release, run the private monorepo's "Tag an open-source release" workflow
(`oss-release-tag.yml`) on its `main` with the next version (`v1.2.3`). In a
clone of this repository at the commit published for that `main`, it builds
the apps, makes the release commit, and runs this workflow's checks on it
(`scripts/release-check.sh`, `viewer-fresh`, the tests) before pushing only
the tag: the Go module proxy keeps a version for good once anyone fetches it,
so nothing unchecked may get that far. To promote a release candidate, give
its `base` input the commit the candidate was made from (`git rev-parse
v1.2.3-rc.1^`); otherwise later commits on `main` would come along. The
reviewer then works through the checklist below
before approving the `release` environment. Write the release notes on the draft while the run waits
for the environment approval; the draft is published, with those notes, when
the desktop files are attached. A tag with a pre-release suffix
(`v1.2.3-rc.1`) becomes a GitHub pre-release, which apps do not update to:
they read the latest full release.

If a job fails, re-run the failed jobs: uploads replace files of the same
name, and nothing is public until `desktop-publish` finishes, because the
release stays a draft. A failure before the sign jobs needs no approval to
re-run; a sign job asks again. To give up on a tag, delete the draft and the
tag. Publishing the draft by hand without the desktop files is possible but
leaves installed apps without the release.

### Approving a release

The approval releases the signing keys to that run, so check, in the run's
summary and a local clone of baum-ai/baum:

1. The run is for the commit you meant to release. Its SHA is a release
   commit whose parent is on `main` and does not predate the previous
   release, and it adds only the built apps; the `release` job checks all
   three, so it must have passed.
2. The release machinery has not changed unexpectedly since the previous
   release. Read the changes to the workflows and the signing scripts:

   ```sh
   git fetch --tags origin
   git diff PREVIOUS_TAG RUN_SHA -- .github desktop/scripts desktop/Dockerfile .goreleaser.yaml
   ```

   and that the release commit itself adds only the built apps:

   ```sh
   git diff --stat RUN_SHA^ RUN_SHA
   ```

3. The settings still hold: the `release` environment's deployment policy is
   the tag pattern `v*` (not branches); a tag ruleset restricts who may create
   or move `v*` tags (recommended; the publish deploy key the tagging workflow
   pushes with needs a bypass for creating them); with more than one reviewer, "prevent
   self-review" is on; and the repository has no signing secrets of its own
   (Settings → Secrets and variables → Actions → Repository secrets), only the
   environment's.
4. The draft release has its notes.

### Snapshots

Each pull request and push to `main` also runs `desktop.yml` from `check.yml`
with version 0.0.0, packaging the macOS app ad hoc with `macos-finalize.sh`,
so the bundling and packaging scripts are exercised before a tag. Those
snapshot bundles are kept as workflow artifacts for 14 days.

### Files on a release

| File | What it is |
| --- | --- |
| `baum-desktop-darwin-universal.dmg` | macOS disk image, Apple silicon and Intel, macOS 12 or newer |
| `baum-desktop-darwin-universal.app.tar.gz` (`.sig`) | macOS app the updater installs |
| `baum-desktop-linux-amd64.AppImage` (`.sig`) | Linux x86_64 AppImage; also what the updater installs |
| `baum-desktop-linux-amd64.deb` | Debian and Ubuntu package (no self-update) |
| `baum-desktop-linux-arm64.AppImage` (`.sig`), `.deb` | Linux arm64, when that build succeeds |
| `baum-desktop-windows-amd64-setup.exe` (`.sig`) | Windows x64 installer; also what the updater runs |
| `desktop-checksums.txt` | SHA-256 of the desktop files |
| `latest.json` | the updater manifest |

The desktop files also carry a GitHub build provenance attestation
(`gh attestation verify FILE --repo baum-ai/baum`).

A macOS bundle that is not signed with a Developer ID is named
`…-unsigned.dmg` (signed but not notarised: `…-unnotarized.dmg`); an unsigned
Windows installer is `…-unsigned-setup.exe`. Gatekeeper and SmartScreen warn
about those. `latest.json` lists `darwin-aarch64` and `darwin-x86_64` (both
the universal app), `linux-x86_64`, `windows-x86_64` and, when built,
`linux-aarch64`, each at
`https://github.com/baum-ai/baum/releases/download/vVERSION/<its file>`.

### Signing

Signing runs only in the sign jobs, on a `v*` tag, after a reviewer approves
the `release` environment, and each kind is skipped while its secrets are
unset. Add them as secrets of the `release` environment of baum-ai/baum,
never as repository secrets.

| Secrets | What they enable |
| --- | --- |
| `TAURI_SIGNING_PRIVATE_KEY` (the content of the key file `tauri signer generate` writes), `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | updater signatures and `latest.json`. Without them the bundles are published without a manifest, and installed apps are not offered the release. |
| `APPLE_CERTIFICATE` (the Developer ID Application certificate and key as a base64 `.p12`), `APPLE_CERTIFICATE_PASSWORD`, `APPLE_SIGNING_IDENTITY` (for example `Developer ID Application: Name (TEAMID)`) | macOS signing with the hardened runtime, of the app and the bundled `baum` |
| `APPLE_API_ISSUER`, `APPLE_API_KEY` (key ID), `APPLE_API_PRIVATE_KEY` (the `.p8` contents) — or `APPLE_ID`, `APPLE_PASSWORD` (an app-specific password), `APPLE_TEAM_ID` | notarisation and stapling of the app and the disk image; needs the signing secrets |
| `WINDOWS_CERTIFICATE` (base64 `.pfx`), `WINDOWS_CERTIFICATE_PASSWORD` | Windows signing with a code signing certificate |
| `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `AZURE_TRUSTED_SIGNING_ENDPOINT`, `AZURE_TRUSTED_SIGNING_ACCOUNT`, `AZURE_TRUSTED_SIGNING_PROFILE` | Windows signing with Azure Artifact Signing instead |

The updater key's public half is `plugins.updater.pubkey` in
`tauri.conf.json`; a test fails if it is not a minisign key or the manifest
URL changes, and `updater-sign.sh` refuses a signature that key does not
verify. Rotating the key means shipping a release with the new public key,
signed with the old private key, before signing with the new one.

### macOS

One universal app (`--target universal-apple-darwin`, built with
`--no-sign`) bundles a universal `baum` that `lipo` makes from goreleaser's
darwin/arm64 and darwin/amd64 binaries. With the Apple secrets, `sign-macos`
imports the certificate into a temporary keychain, and `macos-finalize.sh`
signs the bundled `baum` and then the app with the hardened runtime and no
entitlements (no App Sandbox), checks that both executables carry the
Developer ID and hardened runtime, notarises and staples the app, builds the
disk image with `hdiutil`, signs, notarises and staples it, and checks both
with `stapler validate` and `spctl`. Before extracting the unsigned app, it
rejects any archive entry that is not a regular file or directory inside
`baum.app/`. Two passwords still reach a command line, on the ephemeral
runner only: `security import` takes the certificate's password only as an
argument (`-P`), and notarising with an Apple ID passes the app-specific
password to `notarytool`; the App Store Connect API key avoids the second.
The keychain's own password is random per run. Without them the app is signed ad hoc,
so it runs on Apple silicon once the user allows it in System Settings. The
updater archive is the finished app in a gzipped tar, as Tauri's bundler
makes it.

### Linux

The Linux bundles are built in this directory's Docker image, on Debian 12
(the base image pinned by digest), so they need glibc 2.34 or newer and
WebKitGTK 2.38 or newer with the 4.1 API (`libwebkit2gtk-4.1-0`; Debian 12,
Ubuntu 22.04 and newer). The deb declares that dependency and installs
`baum-desktop` and `baum` in `/usr/bin`. The AppImage carries its own
WebKitGTK and updates itself in place, so keep it in a directory you can
write (for example `~/Applications`). There is no Flatpak or Snap. x86_64 is
the supported architecture; arm64 is built on GitHub's arm64 runners and
published when that build succeeds. Linux bundles are not OS-signed; the
AppImage carries the updater signature.

### Windows

An NSIS installer for the current user (no administrator rights), which
installs the WebView2 runtime with Microsoft's bootstrapper when it is
missing. With signing secrets, `windows-sign.ps1` signs the installer with
`signtool`: a certificate is imported into the current user's store for the
signature and removed afterwards, so its password never appears on a command
line; Azure Artifact Signing uses Microsoft's `signtool` plug-in from a
NuGet package pinned by SHA-256. The app and the bundled `baum.exe` inside
the installer, and the uninstaller, stay unsigned: signing them would mean
running the Tauri bundler, which downloads and runs NSIS, in a job holding
the secrets. Signing the installer is what SmartScreen checks on download.

The installer's hooks (`windows/installer-hooks.nsh`) run `baum daemon stop`
before installing or uninstalling, with the installed `baum.exe` and the
app's copy in its local data directory, whichever exist; the command reaches
the running daemon whichever binary it runs from. An in-app update stops it
too, after waiting for running jobs, and the updated app starts it again.
This is untested on Windows so far.

On Windows the daemon finds `gh` and the agent CLIs on the `PATH` a new
logon would get (the system `Path`, then the user's, from the registry), so
tools installed after the app or the agent started are found.

### What is checked where

- `cargo test`: the updater configuration (key, manifest URL,
  `requireSignedVersion`, Windows passive install), that the key rejects
  another key's signature, and the record of failed installs.
- `scripts/updater-manifest-test.sh` (Check on Linux, and with the pinned
  minisign on macOS and Windows; the release's `desktop-test`; and `just
  desktop::check`): `updater-sign.sh` signs with a password-protected key
  read from standard input and refuses a wrong password or key, and both
  scripts refuse an unpinned minisign in release mode; a correctly
  signed release passes; a tampered bundle, a signature of another bundle or
  from another key, another platform's bundle, a foreign host, another tag,
  plain `http`, an unknown platform, an announced version the bundles were
  not signed for, an older release's bundle, a missing platform and an
  unsigned bundle are all rejected.
- The release runs the same verification on the real bundles before
  anything is attached, and the app verifies each download again.

The onboarding browser regression runs against the compiled React bundle
mounted through `ui-test`: `pnpm --dir /oss/app test:desktop` after building the app. It uses mocked native
IPC and checks separate agent actions, retries, multiple repositories, back/skip,
completion and the minimum window width. Screenshots go to a temporary directory, or `ONBOARDING_SCREENSHOT_DIR` when set.
