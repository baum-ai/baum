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

## What pages may do

- The window loads only the bundled screen and the attached daemon's origin.
  `http` and `https` links to other hosts open in the default browser. Other
  loopback servers, other schemes and new-window requests are refused.
- Tauri treats the daemon's pages as remote, and no capability names a remote
  URL, so they cannot call any Tauri command or plugin. `withGlobalTauri` is
  off.
- The bundled screen has one capability, `connecting-screen`, which allows
  only the app's `connection` and `reconnect` commands.
- The opener plugin is used only from Rust, to open links in the browser. The
  dialog and updater plugins are used only from Rust too (see Updates). No page
  is granted any plugin's commands.

The app itself makes no network requests other than loopback requests to the
daemon and the update checks below.

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
  at the next daily check. On Windows the installer closes the app, updates
  it for the current user and starts it again.
- A deb install, or any copy the updater cannot replace (an AppImage in a
  directory the user cannot write, an app on a read-only volume), only says
  that a new version exists, once per version and app start, and offers to
  open its release page. Updating it is the user's job, as with any package.
- Restarting the app does not stop the daemon. The first client to run the
  new `baum` replaces an older daemon, as after any upgrade.

## Development

Builds and checks run in the `desktop-test` Docker service (Rust, the
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
just desktop::icons         # regenerate icons/ from icons/source.svg
```

`run` starts the container's own daemon in the container's home directory,
not yours. Set `BAUM_DESKTOP_BAUM` to run a different `baum` binary than the
bundled one.

Tauri expects the bundled binary at `binaries/baum-<target triple>`, which
`scripts/sidecar.sh` builds (run it from the module root; it takes the triple
as an argument and defaults to the Go host). Every build, including
`cargo check`, needs it. `binaries/`, `target/` and the generated `gen/` are
ignored.

## Releases

The desktop app is released with the CLI, from the same `v*` tag, to the same
GitHub release. Pushing the tag to baum-ai/baum runs `.github/workflows/release.yml`:

1. goreleaser builds the CLI archives, writes `checksums.txt` and creates the
   GitHub release, as before. Its `baum` binaries are kept as the workflow
   artifact `cli-binaries`.
2. `desktop.yml` bundles the app on each platform around those binaries, with
   `scripts/bundle.sh`, and signs what its secrets allow.
3. A read-only job writes `latest.json` with `scripts/updater-manifest.sh` and
   checks every bundle it names against the release key and the version.
4. A last job attaches the bundles, then `latest.json`, to the release.

To release, tag `main` with the next version (`git tag v1.2.3`, then push the
tag). A tag with a pre-release suffix (`v1.2.3-rc.1`) becomes a GitHub
pre-release, which apps do not update to: they read the latest full release.
Re-running failed jobs is safe; uploads replace files of the same name.

Each pull request and push to `main` also runs `desktop.yml` from `check.yml`
with version 0.0.0 and no secrets, so unsigned snapshot bundles of every
platform are built the way the release builds them. They are kept as
workflow artifacts for 14 days.

### Files on a release

| File | What it is |
| --- | --- |
| `baum-desktop-darwin-universal.dmg` | macOS disk image, Apple silicon and Intel, macOS 12 or newer |
| `baum-desktop-darwin-universal.app.tar.gz` (`.sig`) | macOS app the updater installs |
| `baum-desktop-linux-amd64.AppImage` (`.sig`) | Linux x86_64 AppImage; also what the updater installs |
| `baum-desktop-linux-amd64.deb` | Debian and Ubuntu package (no self-update) |
| `baum-desktop-linux-arm64.AppImage` (`.sig`), `.deb` | Linux arm64, when that build succeeds |
| `baum-desktop-windows-amd64-setup.exe` (`.sig`) | Windows x64 installer; also what the updater runs |
| `latest.json` | the updater manifest |

A macOS bundle that is not signed with a Developer ID is named
`…-unsigned.dmg` (signed but not notarised: `…-unnotarized.dmg`); an unsigned
Windows installer is `…-unsigned-setup.exe`. Gatekeeper and SmartScreen warn
about those. `latest.json` lists `darwin-aarch64` and `darwin-x86_64` (both
the universal app), `linux-x86_64`, `windows-x86_64` and, when built,
`linux-aarch64`.

### Signing

Every kind of signing runs only on a tag in baum-ai/baum, never on a pull
request, and is skipped while its secrets are unset. Set them as repository
secrets of baum-ai/baum.

| Secrets | What they enable |
| --- | --- |
| `TAURI_SIGNING_PRIVATE_KEY`, `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | updater signatures and `latest.json`. Without them the bundles are published without a manifest, and installed apps are not offered the release. |
| `APPLE_CERTIFICATE` (the Developer ID Application certificate and key as a base64 `.p12`), `APPLE_CERTIFICATE_PASSWORD`, `APPLE_SIGNING_IDENTITY` (for example `Developer ID Application: Name (TEAMID)`) | macOS signing with the hardened runtime, of the app and the bundled `baum` |
| `APPLE_API_ISSUER`, `APPLE_API_KEY` (key ID), `APPLE_API_PRIVATE_KEY` (the `.p8` contents) — or `APPLE_ID`, `APPLE_PASSWORD` (an app-specific password), `APPLE_TEAM_ID` | notarisation and stapling of the app and the disk image; needs the signing secrets |
| `WINDOWS_CERTIFICATE` (base64 `.pfx`), `WINDOWS_CERTIFICATE_PASSWORD` | Windows signing with a code signing certificate |
| `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `AZURE_TRUSTED_SIGNING_ENDPOINT`, `AZURE_TRUSTED_SIGNING_ACCOUNT`, `AZURE_TRUSTED_SIGNING_PROFILE` | Windows signing with Azure Trusted Signing instead |

The updater key's public half is `plugins.updater.pubkey` in
`tauri.conf.json`; a test fails if it is not a minisign key or the manifest
URL changes. Rotating the key means shipping a release with the new public
key, signed with the old private key, before signing with the new one.

### macOS

One universal app (`--target universal-apple-darwin`) bundles a universal
`baum` that `lipo` makes from goreleaser's darwin/arm64 and darwin/amd64
binaries. With the Apple secrets, the workflow imports the certificate into a
temporary keychain; `bundle.sh` signs the bundled `baum`, Tauri signs the app
and every binary in it with the hardened runtime and no App Sandbox (no
entitlements), notarises the app and staples it; `bundle.sh` then checks that
both executables carry the Developer ID and hardened runtime, signs,
notarises and staples the disk image, and checks it with `stapler validate`
and `spctl`. Without them the app is ad-hoc signed (`signingIdentity "-"`), so
it runs on Apple silicon once the user allows it in System Settings.

### Linux

The Linux bundles are built in this directory's Docker image, on Debian 12,
so they need glibc 2.34 or newer and WebKitGTK 2.38 or newer with the 4.1 API
(`libwebkit2gtk-4.1-0`; Debian 12, Ubuntu 22.04 and newer). The deb declares
that dependency and installs `baum-desktop` and `baum` in `/usr/bin`. The
AppImage carries its own WebKitGTK and updates itself in place, so keep it in
a directory you can write (for example `~/Applications`). There is no Flatpak
or Snap. x86_64 is the supported architecture; arm64 is built on GitHub's
arm64 runners and published when that build succeeds.

### Windows

An NSIS installer for the current user (no administrator rights), which
installs the WebView2 runtime with Microsoft's bootstrapper when it is
missing. With signing secrets, `bundle.sh` sets Tauri's
`bundle.windows.signCommand` to `scripts/windows-sign.ps1`, which signs the
bundled `baum.exe`, the app, the installer and the uninstaller with whichever
method is configured; the installer's Authenticode signature is checked after
the build. On Windows the daemon finds `gh` and the agent CLIs on the `PATH`
a new logon would get (the system `Path`, then the user's, from the
registry), so tools installed after the app or the agent started are found.

### What is checked where

- `cargo test`: the updater configuration (key, manifest URL,
  `requireSignedVersion`, Windows passive install), and that the key rejects
  another key's signature.
- `scripts/updater-manifest-test.sh` (Check, and `just desktop::check`): a
  correctly signed release passes; a tampered bundle, a signature of another
  bundle or from another key, an announced version the bundles were not
  signed for, an older release's bundle, a plain `http` URL, a missing
  platform and an unsigned bundle are all rejected.
- The release itself runs the same verification on the real bundles before
  anything is attached, and the app verifies each download again.
