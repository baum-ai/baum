# Desktop app

A [Tauri](https://tauri.app) v2 shell around the user's `baum daemon`. The
shell adds no review features: the daemon serves the local application
(`../app`) over loopback HTTP, and the window shows it. The shell handles only
what the operating system has to: the window, the tray, start at login,
notifications, and later installing the CLI, signing and updates.

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
     another client starts a new daemon (hooks run `baum daemon ensure` on
     every event), the app attaches to it.
   - The app never restarts the daemon on its own, so a crashing daemon
     cannot restart in a loop.

A second launch focuses the existing window instead of opening another one.

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
  Start at login; a Notifications submenu (see "Notifications"); Quit; Quit
  and stop background reviews.
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

No new Tauri capability exists, and none names a remote URL: the tray, the
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

- The window loads only the bundled screen and the attached daemon's origin.
  `http` and `https` links to other hosts open in the default browser. Other
  loopback servers, other schemes and new-window requests are refused.
- Tauri treats the daemon's pages as remote, and no capability names a remote
  URL, so they cannot call any Tauri command or plugin. `withGlobalTauri` is
  off.
- The bundled screen has one capability, `connecting-screen`, which allows
  only the app's `connection` and `reconnect` commands.
- The opener plugin is used only from Rust, to open links in the browser. No
  page is granted its commands.

The app itself makes no network requests other than loopback requests to the
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
