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
  at the next daily check. A download that receives nothing for two minutes
  is abandoned until the next check. On Windows the app stops the daemon
  first (it runs from the installed `baum.exe`), and the installer closes the
  app, updates it for the current user and starts it again; the updated app
  starts the daemon.
- A deb install, or any copy the updater cannot replace (an AppImage in a
  directory the user cannot write, an app on a read-only volume), only says
  that a new version exists, once per version and app start, and offers to
  open its release page. Updating it is the user's job, as with any package.
  A version whose installation failed is remembered (`updates.json` in the
  app's config directory) and from then on only announced this way.
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
GitHub release. Pushing the tag to baum-ai/baum runs
`.github/workflows/release.yml`:

```text
test, viewer-dist, desktop-test          checks of the tagged commit
  └─ release                             tag on main? goreleaser: CLI archives, draft release
       └─ desktop (desktop.yml)          unsigned bundles per platform; no secrets
            ├─ sign-macos    ┐
            ├─ sign-windows  ├─ environment "release": sign what desktop built
            └─ sign-linux    ┘
                 └─ desktop-manifest     latest.json, desktop-checksums.txt; read-only
                      └─ desktop-publish attach files, latest.json last, publish the draft
```

- `release` fails unless the tagged commit is on `main` and the previous
  `v*` tag in version order points into its history (both with `git
  merge-base --is-ancestor`; the same commit is allowed, so a release
  candidate can be promoted). goreleaser creates the release as a draft with empty
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

To release, tag a commit on `main` with the next version (`git tag v1.2.3`,
then push the tag). The reviewer then works through the checklist below
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

1. The run is for the commit you meant to release. Its SHA is on `main` and
   does not predate the previous release; the `release` job checks both, so
   it must have passed.
2. The release machinery has not changed unexpectedly since the previous
   release. Read the changes to the workflows and the signing scripts:

   ```sh
   git fetch --tags origin
   git diff PREVIOUS_TAG RUN_SHA -- .github desktop/scripts desktop/Dockerfile .goreleaser.yaml
   ```

3. The settings still hold: the `release` environment's deployment policy is
   the tag pattern `v*` (not branches); a tag ruleset restricts who may create
   or move `v*` tags (recommended); with more than one reviewer, "prevent
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

The installer's hooks (`windows/installer-hooks.nsh`) stop the daemon before
installing or uninstalling, since it runs from the installed `baum.exe`; an
in-app update stops it too, after waiting for running jobs, and the updated
app starts it again. This is untested on Windows so far.

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
