# Desktop app

A [Tauri](https://tauri.app) v2 shell around the user's `baum daemon`. The
shell adds no review features: the daemon serves the local application
(`../app`) over loopback HTTP, and the window shows it. The shell handles only
what the operating system has to: the window, installing the `baum` command
and the user-wide agent hooks, and later the tray, start at login,
notifications, signing and updates.

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

The "Command Line and Hooks…" menu item (the application menu on macOS, a
"baum" menu elsewhere) reopens the same screen: what is installed, Update,
Replace, Remove, and Install or Remove hooks. "Done" attaches to the daemon
again. `onboarding::show` opens it from other places, such as a tray menu.

The screen gets the facts from the bundled baum: `baum daemon login-path
--json` resolves the login shell's `PATH` as the daemon does and names the
first `baum` on it (the one hooks run) with its version, and `baum hooks
status --user --json` reports each agent's hooks.

### Where the command goes

- **macOS and Linux:** a symbolic link named `baum` to the bundled binary.
  - If a different `baum` comes first on the `PATH` (`go install`,
    Homebrew, a release archive), the screen shows its path and version, and
    replaces it only when the user clicks "Replace <path>": that file is
    moved aside to `<path>.replaced-by-baum-app` and the link takes its
    place, so it keeps its position on the `PATH`.
  - Otherwise the link goes in the first of `~/.local/bin` and `~/bin` that
    is on the `PATH`, else in `/usr/local/bin`, else in `~/.local/bin` with a
    note to add it to the `PATH`. A directory the user cannot write
    (usually `/usr/local/bin`) is changed through the system's password
    prompt (`osascript` with administrator privileges on macOS, `pkexec` on
    Linux), which runs a fixed script that receives the paths as arguments.
  - The link points at a stable path: the binary inside the installed app
    bundle (`baum.app/Contents/MacOS/baum`, or `/usr/bin/baum` from the deb),
    which updates replace in place. An AppImage runs from a mount that
    disappears when it quits, so the app keeps a copy in its data directory
    (`bin/baum`, mode 0755) and refreshes it at every launch. On macOS an app
    still on its disk image or translocated by Gatekeeper must be moved to
    Applications first.
  - The deb's `/usr/bin/baum` already is the app's binary: the screen shows
    it as installed and leaves it to the package manager.
  - A link the app made whose app was since deleted points nowhere; shells
    and the hooks' `command -v baum` skip it, so the hooks do nothing. The
    next install replaces such a broken link without asking, and the app's
    own link to an older copy of the app is updated without asking.
- **Windows:** the directory of the bundled `baum.exe` is added to the front
  of the user's `PATH` (the registry value, keeping unexpanded variables);
  new terminals pick it up. A `baum` in a system `PATH` directory still comes
  first and is shown.

Remove deletes only what the app recorded: the link, while it still points
where the app put it (a link someone changed since is left alone), and puts
back a baum it had replaced; on Windows it takes the directory off the user
`PATH`. Hooks are removed with the bundled `baum hooks uninstall --user`,
which removes only baum's own entries.

### Restarts into the app's baum

A busy older daemon restarts into a newer baum only if it trusts the
executable (`internal/daemon/trust.go`): its own, or the `baum` on its login
shell's `PATH` after resolving links, owned by the user or root and not
group- or world-writable, in directories that pass the same test up to `/`.
Installing the link makes the bundled binary the `baum` on the `PATH`, so:

- Linux deb (`/usr/bin/baum`) and the AppImage's copy in the user's data
  directory are trusted (the copy only while the home directory and
  `~/.local/share` are not group-writable, which a umask of 002 can cause).
  The binary inside an AppImage's mount (under `/tmp`) never is.
- macOS: `/Applications` is `root:admin`, mode 0775, so an app there is
  refused, both as the `PATH` baum and as the daemon's own executable. An
  older busy daemon is then not restarted when its jobs finish; it keeps
  running and is replaced the next time the app or a hook finds it idle. An
  app in `~/Applications` is trusted.
- Windows checks only that the file is the daemon's own or the `PATH` baum,
  which the user `PATH` entry makes it for daemons started after the change.

## What pages may do

- The window loads only the bundled screen and the attached daemon's origin.
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
