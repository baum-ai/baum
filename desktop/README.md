# Desktop app

A [Tauri](https://tauri.app) v2 shell around the user's `baum daemon`. The
shell adds no review features: the daemon serves the local application
(`../app`) over loopback HTTP, and the window shows it. The shell handles only
what the operating system has to: the window, notifications, and later the
tray, start at login, installing the CLI, signing and updates.

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

## Notifications

The app turns daemon events into operating system notifications. It reads the
daemon's `GET /api/events` stream itself (`src/notifications/`), so they
arrive while the window is closed or hidden. Pages the daemon serves are not
involved and get no capability.

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

- **Click.** Clicking a notification brings the window to the front and loads
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
- **Settings.** Each kind is on by default. The app menu has a
  "Notifications" submenu with one check item per kind. The choices and the
  permission answer are saved in `notifications.json` in the app's config
  directory (`~/.config/com.usebaum.desktop/` on Linux). The menu is the
  smallest native surface that needs no page capability; the tray menu is a
  natural home for the same items.
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
