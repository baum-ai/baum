# Desktop app

A [Tauri](https://tauri.app) v2 shell around the user's `baum daemon`. The
shell adds no review features: the daemon serves the local application
(`../app`) over loopback HTTP, and the window shows it. The shell handles only
what the operating system has to: the window, the tray, start at login,
notifications, installing the `baum` command and the user-wide agent hooks,
and later signing and updates.

The app bundles the release `baum` binary next to its executable. `baum`
itself is unchanged and stays `CGO_ENABLED=0`.

## Attaching to the daemon

There is one daemon per user, shared by the app and coding-agent hooks. Either
may start it, and it outlives the app: quitting the app leaves it running.

1. The window opens on a bundled connecting screen (`ui/`).
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

Agent hooks run `baum` by name, so the app's first run sets up two things
before the window attaches to the daemon (which starts meanwhile). The window
opens on a bundled setup screen (`ui/setup.html`) that shows what is on this
computer and what installing would change; nothing changes until the user
clicks Install. The Rust side then makes each change:

1. The `baum` command: the bundled binary, linked onto the login shell's
   `PATH` (below). One binary and one version: the terminal, the hooks and
   the daemon all run the app's baum, and updating the app updates them.
2. The user-wide Claude Code and Codex hooks: the bundled
   `baum hooks install --user`, for the agents it finds. Its output (files
   changed, backups, Codex's `/hooks` trust step) is shown.

"Skip for now" is there too, and so is "Keep my baum, install the hooks" when
a different baum is in the way. Leaving the screen records that setup ran
(`setup.json` in the app's configuration directory, with the link the app
created), attaches to the daemon and opens its diagnostics page, which lists
each problem with its fix. Later, every page of the app shows a notice with
a link to that page while the diagnostics find a problem (see
[`../app/README.md`](../app/README.md)).

"Command Line and Hooks…" reopens the same screen: what is installed,
Update, Replace, Remove, and Install or Remove hooks. "Done" attaches to the
daemon again. The item is in the tray menu and in the window's menu bar (the
application menu on macOS, the Background menu elsewhere), both built in
`tray.rs`; it calls `onboarding::show`.

The screen gets the facts from the bundled baum: `baum daemon login-path
--json` resolves the login shell's `PATH` as the daemon does and names the
first `baum` on it (the one hooks run) with its version, and `baum hooks
status --user --json` reports each agent's hooks.

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
removes only baum's own entries.

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
- **Tray menu:** Open baum; what the daemon is doing (Idle, "2 running,
  1 queued"); anything needing attention; Pause or Resume automatic reviews;
  Start at login; Command Line and Hooks… (see "First run"); a Notifications
  submenu (see "Notifications"); Quit; Quit and stop background reviews.
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
| Attention | A review failed, a repository's daily cap paused automatic reviews, a diagnostics check reports a problem, or the daemon is not running. |

Attention for the first three clears when the window is opened or focused and
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
   for what the daemon exposes only there: `GET /api/jobs` and
   `GET /api/automatic` every 3 s while jobs run and every 10 s otherwise,
   `GET /api/diagnostics` every 5 minutes (it runs the doctor's checks), and
   `PUT /api/automatic`. This is the credential the app already reads for the
   window and the health check, accepted on `/api/` routes only; the request
   that changes state sends the daemon's own origin and JSON, as the daemon
   requires. It is polled rather than streamed: the events stream carries only
   change notices, so the tray would fetch the same data anyway.

Notifications read the events stream separately (see "Notifications").

The tray adds no Tauri capability, and none names a remote URL: the tray, the
dialogs and the autostart entry are used from Rust only, so pages the daemon
serves still reach no command or plugin.

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
daemon serves are not involved and get no capability.

| Notification | Daemon event |
| --- | --- |
| Review finished, with the review's risk score | `notice` `review_finished` |
| Review failed, with its failure category | `notice` `review_failed` |
| Review requested from me | `notice` `review_requested` |
| Review generated automatically | `notice` `review_automatic` |
| Daily cost cap reached | `automatic`, when a repository is paused for `daily_cap` |

`notice` events are published by the daemon (`oss/internal/notices`, see
`ARCHITECTURE.md`); the cap notification comes from the scheduler's existing
`automatic` event. There is no default cap, so the cap notification exists only
for a user who set one. The text comes only from fields the daemon already
redacted: the repository, the pull request title as GitHub returned it
(control characters stripped, 200 characters at most), the stored risk score
and the failure category. A title is shown unchanged; where a Linux
notification server renders markup, its `<`, `>` and `&` are escaped so the
title is not interpreted.

- **Click.** Clicking a notification shows the window if it is hidden or
  minimised, brings it to the front and loads
  the pull request (`?clone=<id>&pr=<number>`) or, for the cap and for a
  notice with no registered clone, the jobs list (`?page=jobs`). The app learns
  the daemon's secret app path with one `POST /api/auth/exchange` and loads the
  URL itself; the page's own scripts do not navigate. Linux, macOS and Windows
  toasts report clicks through `notify-rust`. The logic is unit-tested with a fake
  platform; no real notification was shown in testing.
- **Focus.** No notification shows while the window has focus on the same pull
  request (the same clone and number).
- **Permission.** The app never asks at launch. The operating system is asked
  when the first notification is due. Linux and Windows have no per-app prompt
  and macOS prompts when the first notification posts; `notify-rust` does not
  report a macOS refusal, so a refusal there simply shows nothing. A refusal
  the platform does report is remembered and the app stops trying; switching a
  kind on again in the menu asks once more.
- **Settings.** Each kind is on by default. A "Notifications" submenu with one
  check item per kind is in the window's menu bar and in the tray menu (the
  tray reaches Linux and Windows users without a menu bar). The choices, the
  permission answer, and the daily-cap pauses already announced (so a restart
  or a replaced daemon does not announce one again) are saved in
  `notifications.json` in the app's config directory
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

## What pages may do

- The window loads only the bundled screens and the attached daemon's origin.
  Of the bundled files it shows exactly `/` and `/index.html`, and
  `/setup.html` only while the app shows the setup screen; any other path,
  including other spellings the asset server maps to a page (`/setup`,
  `/setup.html/`, percent-encoded names), is refused. The bundled pages'
  content security policy has `frame-ancestors 'none'`.
  `http` and `https` links to other hosts open in the default browser. Other
  loopback servers, other schemes and new-window requests are refused.
- Tauri treats the daemon's pages as remote, and no capability names a remote
  URL, so they cannot call any Tauri command or plugin. `withGlobalTauri` is
  off.
- The bundled screens have two capabilities, for the main window's bundled
  origin only: `connecting-screen` allows the app's `connection` and
  `reconnect` commands, and `setup-screen` the setup commands
  (`setup_state`, `install_command`, `uninstall_command`, `install_hooks`,
  `uninstall_hooks` and `finish_setup`). Every change they make runs in the
  Rust side and only when the user clicked it on the setup screen;
  replacing a different baum also needs the screen to name the path the
  user confirmed. The daemon's pages cannot call any of them.
- The opener plugin is used only from Rust, to open links in the browser. No
  page is granted its commands.

The app itself makes no network requests other than loopback requests to the
daemon. Outside its own configuration and data directories it writes only
what the setup screen installs: the `baum` link (or the user `PATH` entry on
Windows), and, through the bundled baum, the user-wide hook entries.
daemon. Notifications use the operating system's notification service
(`notify-rust`) only from Rust.

## Development

Builds and checks run in the `desktop-test` Docker service (Rust, the
WebKitGTK and GTK development packages, and the Tauri CLI). The bundled
binary is built in `oss-test`. From the repository root:

```sh
just desktop::check     # cargo fmt --check, clippy -D warnings, tests
just desktop::test      # tests only; pass cargo test arguments after it
just desktop::sidecar   # build binaries/baum-<target triple>
just desktop::build     # Linux bundle (deb by default) in target/release/bundle
just desktop::run       # run the app on this Wayland session's display
just desktop::icons     # regenerate icons/ from icons/source.svg
```

`run` starts the container's own daemon in the container's home directory,
not yours. Set `BAUM_DESKTOP_BAUM` to run a different `baum` binary than the
bundled one.

Tauri expects the bundled binary at `binaries/baum-<target triple>`, which
`scripts/sidecar.sh` builds (run it from the module root; it takes the triple
as an argument and defaults to the Go host). Every build, including
`cargo check`, needs it. `binaries/`, `target/` and the generated `gen/` are
ignored.

Signed and notarised macOS bundles, the Linux AppImage and deb, Windows
installers and updates from GitHub Releases are later work.
