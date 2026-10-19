# Desktop app

A [Tauri](https://tauri.app) v2 shell around the user's `baum daemon`. The
shell adds no review features: the daemon serves the local application
(`../app`) over loopback HTTP, and the window shows it. The shell handles only
what the operating system has to: the window, and later the tray, start at
login, notifications, installing the CLI, signing and updates.

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
   After three failures in a row, the window returns to the connecting screen.
   That screen shows the last lines of `daemon.log` and `daemon.out` from the
   state directory and a button to start the daemon again. The app never
   restarts the daemon on its own, so a crashing daemon cannot restart in a
   loop.

A second launch focuses the existing window instead of opening another one.

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
daemon.

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
