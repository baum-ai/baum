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
5. While attached, the app checks the daemon's `/api/health` every 30 seconds
   while the native event stream is up, and every 5 seconds otherwise. A stream
   disconnect wakes the health check immediately.
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

On macOS the app runs no `baum` from a read-only volume: the disk image it
ships in, or the temporary copy App Translocation makes of an app opened
where it was downloaded. Both go away (ejecting the image, quitting the app),
and a daemon started from one keeps running without its executable and stops
answering, which no other client can repair. Instead the window opens on the
connecting screen, skipping first-run setup, and says to move baum to
Applications. Nothing is retried, start at login stays off, and Quit and Stop
just quits, since this app started no daemon.

A second launch focuses the existing window instead of opening another one.

## Native settings

The native shell keeps one `desktop.json` in its OS app-config directory
(`~/.config/com.usebaum.desktop/` on Linux). It reads this file before
connecting to the daemon:

| Section | Holds |
| --- | --- |
| `setup` | first-run completion and installed CLI command records |
| `notifications` | notification-kind toggles and the OS permission answer |
| `window` | the page zoom level |
| `theme` | the colour theme the native screens apply: the mode and the theme in each slot |
| `updates` | versions whose automatic installation failed |

Every change reads the latest document and replaces only its section, under
one lock shared by all native threads. Setup completion and CLI records are
updated independently within their section, so finishing setup while a command
installation runs preserves both. A private temporary file is flushed and
renamed into place; on Unix the saved file is mode 0600. Reads of a damaged
file or section use safe defaults and log the problem; writes refuse damaged
JSON or a malformed section instead of overwriting it. CLI install/removal
also refuses unreadable setup records before changing a link. Separate
pre-release settings files are not imported.

## First run: the baum command and the hooks

First run uses a three-step setup with the local app's shared light and dark
palette, set like every screen of the app in its bundled type: Instrument
Sans for text and Manrope for headings, both under the SIL Open Font License
(`../viewer/src/fonts/`). Studio Feixen, the website's font, is licensed for the
web only and is never bundled in the app. The screen is a typed React entry in
`../app/src/desktop/`, built alongside the local application by its Vite build.
Its rail uses the app sidebar's canvas and Stumpy mark. UI colors come from
`../viewer/src/palette.css`; Stumpy's fixed colors live with the illustration in
`../viewer/src/components/stumpy-artwork.ts`. Native SVG icon templates reuse
both sources, resolved by `scripts/prepare-icons.mjs` before rasterization.
Setup, connecting and tray screens follow the active colour theme, including
changes while they are open. They cannot ask the daemon (their CSP allows only
`ipc:`, and `localStorage` is not shared with the daemon's changing loopback
origin), so the app caches what the saved theme resolves to (`src/theme.rs`):
the daemon pages report it with `save_theme` and the screens read it with
`theme_state`. The app checks only its shape (token names `--[a-z0-9-]{1,40}`, at
most 40 tokens per theme, values of at most 64 bytes, at most 32 KiB, mode
`system`, `light` or `dark`, base `light` or `dark`) and refuses anything else
without changing what is stored. An initialization script on the main window and
the tray panel applies the saved theme before the first paint; it is built
when the window is created, so each page load on the main window also pushes the
current theme with a best-effort `eval` as the load starts (logged if it fails),
and the page applies the live one right after. The tray panel keeps the theme
saved at startup until its page applies the current one. Without a saved theme the
screens follow the system's light or dark.
Vite writes the daemon assets to `../internal/localapp/dist` and assembles the
native assets in `dist/`, where the connecting screen is `index.html`. Tauri embeds
`dist/` so setup stays on its local origin with the existing native permissions;
daemon-served pages may open the native folder picker (below) or manage
the bundled CLI and agent hooks directly in Settings. The GitHub step requires a
connection and at least one picked repository before Continue or the later steps
in the rail become available. Adding the CLI to the user's PATH and connecting
agent hooks remain optional. The app runs its own CLI to start the daemon.
Steps have Back and Continue controls. Changes
happen only when the user selects an install or add action. Displayed paths abbreviate the current home directory as
`~`; actions still use the full path:

1. Connect GitHub and pick repositories.
   - Device sign-in: the app attaches to the daemon while setup shows, and the
     step reaches the daemon's GitHub sign-in through the setup screen's
     `github_connection` command, which relays status, start, cancel and retry
     over the tray's loopback client and opens GitHub's device page
     (`open`); disconnecting stays in Settings. Signing
     in displays and copies the code, then waits five seconds before opening
     GitHub's device page in the default browser so the code and clipboard
     confirmation stay visible. During the delay, the browser control is disabled
     and shows the shared loading animation beside "Opening GitHub…"; launching
     restores the "Open GitHub" link at the same width.
     The status distinguishes preparing sign-in, opening the browser and waiting
     for GitHub confirmation, with the shared loader while waiting. It explains
     that confirmation can take a few seconds and shows "Connected as…" once
     sign-in is confirmed, while repositories load separately.
     Cancelling or leaving the step cancels the pending browser launch.
     The daemon runs the device flow and keeps the credential, so the screen
     never holds a token.
   - Below it, pick GitHub repositories from the signed-in account. The first
     request fetches eight top repositories, then a background request fetches up
     to one hundred more using the returned `topRepositories` cursor. The list
     shows the first eight and any picked ones; archived repositories are omitted.
     Setup and the app's Add repositories share the picker. Cached top matches
     appear immediately while typing. GitHub repository search runs separately
     after a 250 ms debounce, with results cached per query for five minutes.
     An empty search preserves GitHub's top repository order. Search results
     sort by exact name or owner/name match first, then top status, then
     latest push. A pending or failed search leaves cached matches pickable, so
     the person can continue setup immediately; failures offer a retry. The picker
     refreshes every 300 ms while background requests are pending. The footer
     says how many more repositories or matches there are and offers Show
     more, which adds twenty rows of the same list (asking GitHub for its next
     page once the loaded rows run out); the list then keeps its height and
     scrolls.
     Rows show GitHub details only and nothing is picked on its own. Once a repository
     is picked, baum looks for its clone in folders agents worked in and the
     home directory, with the nine-dots loader while it looks, then shows the
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
2. Configure your agents: Claude Code or Codex (initially resolved from automatic
   selection or the installed agents). Missing CLIs are marked Not installed
   and cannot be selected; a saved missing agent falls back to an installed
   one, preferring Claude Code. When neither is detected, official installation
   guides and Check again replace the model controls. Continue and the final
   step in the rail wait for an installed agent; rechecking selects one as
   soon as it is detected. Detection errors show retry guidance.
   A compact row pairs the model for reviews and data extraction with its
   reasoning effort. A line under Reviews and extraction and under Fixes says
   what each agent does, and a note below the card points to Settings for
   per-task agents and per-repository automatic reviews. The Fixes tiles show
   only agent names (or Not installed). A detected agent that reports no
   sign-in is marked Not signed in but stays selectable (its jobs fail until it
   signs in); choosing it adds a line on how to sign in (`claude` then
   `/login`, or `codex login`) and to choose Check again. Fixes start as Same as reviews;
   choosing Claude Code or Codex for them adds their own model and effort row.
   A model can be a preset or a custom name. Choose whether reviews run
   automatically and, if so, for which pull requests (authored, requested,
   both, all open pull requests, or those in an inbox with automatic reviews on). The screen reads them with `baum internal reviews show` and saves changes
   with `baum internal reviews set` when the user opens baum at the end of
   setup. They are stored as app preferences in the repository registry, which
   the daemon rereads for every job. The native bridge forwards the review,
   fix and extraction flags. Extraction is saved only when setup changes the
   review agent, model or effort: it then follows the review agent, with the
   review model (none when it is the agent's default decision model) and
   effort. An untouched setup leaves the extraction choice from Settings intact.
3. Install the CLI and connect agents.
   - Install the bundled `baum` command onto the login shell's `PATH` (below).
     The step states in one line where baum will be installed, or which other
     baum it replaces; PATH and administrator warnings show beneath it.
   - Connect the agents you use, with separate Connect or Enable buttons. The
     Codex marketplace plugin is installed with `baum hooks install --user
     --agent codex`. Each connected agent shows the same status, plugin
     version, hook note and Update button as Settings (below). A status names
     the state and the button the action; an agent that is installed but
     blocked (disabled, hooks missing, unapproved, or stopped by its settings)
     shows its status in the caution colour. When Codex reports baum hooks
     missing, the note says Update adds them before `/hooks` approves them. The
     Claude Code plugin is installed separately with `--agent claude-code`.
     Both use the native agent CLI to fetch the public marketplace.
     Codex status additionally checks its effective hook inventory. Connected
     requires every baum hook to be present, enabled and trusted; untrusted or
     modified definitions show Needs approval in Codex. Missing, disabled and
     unavailable checks are distinct. Returning from Codex or Check again
     refreshes approval, without granting it. The person can finish setup with
     an unfinished connection; the app keeps a warning beside Settings.

### Backends

The app and its daemon run reviews, fixes, chats and decision extraction with
Claude Code and Codex only. Setup and Settings list those two; `auto` tries
Claude Code, then Codex, and never falls back to the Anthropic API. When
neither is installed, setup replaces the model controls with installation
guides, Settings' Reviews section says so and Diagnostics reports the `auto`
check as a problem. The daemon refuses a job whose backend is `cursor-agent` or
`anthropic`, for example one set in `config.toml`, failing it with a message
that names the allowed choices; it does not switch backends, and pre-release
configurations are not migrated. The CLI exposes only desktop runtime and
support commands; reviews run through the daemon. baum's local agent plugins
never install, configure or run Entire. Independently recorded checkpoints
remain readable without the Entire executable.

### Model effort choices

The onboarding selectors use CLI capabilities, checked on 2026-10-31 against
[Claude Code's model configuration](https://code.claude.com/docs/en/model-config#adjust-effort-level)
and the Codex 0.166 model catalogue. Codex accepts the effort advertised by each
model through [`model_reasoning_effort`](https://learn.chatgpt.com/docs/config-file/config-reference#model_reasoning_effort).

| Models | Effort levels |
| --- | --- |
| Claude Haiku 5.5, Sonnet 5.5 and Opus 5.5 | Low, Medium, High, Extra high, Max |
| GPT-6 Luna | Low, Medium, High, Extra high, Max |
| GPT-6.1 Sol, GPT-6.1 Terra and GPT-6 Astra | Low, Medium, High, Extra high, Max, Ultra |

Every selector also offers Default, which leaves the effort to the agent.
Ultra enables Codex's automatic task delegation. Other models, including
custom ones, offer Low to Max; the agent handles a level a model lacks (Claude
Code falls back to the highest supported level). Changing a model preserves a supported effort and resets an
unsupported one to Default. API-only levels are not offered by the CLI picker.

Open baum uses the app's CLI independently of the command on the user's PATH.
Installing that command and connecting agent hooks remain optional.
Failed actions leave the user on the step with their output and a retry action.
"Check again" rereads the installed state. Finishing records `desktop.json`'s
setup section, attaches to the daemon and opens the app's first saved inbox. If diagnostics find a problem,
the inbox shows a notice linking to the fixes. Setup points to Settings to finish
later, with inline controls for the command and each agent plugin.
"Command Line and Hooks…" in the window's menu bar also reopens the same steps;
the tray menu omits it.

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
plugin ID, installation and enablement separately, plus Codex's hook trust
state. The native bridge preserves this state for onboarding and Settings.
Disabled plugins offer an Enable action; approval still belongs to Codex. Each native action has a
90-second bound and status queries have 10 seconds. The desktop gives both
agents, login PATH resolution and process overhead a 240-second total budget.
Status, install and removal run with the resolved login shell PATH so agents
installed through Homebrew or npm are found from GUI launchers too. If login
PATH resolution fails, the app uses its inherited PATH and displays the reason;
if only a login shell that is not interactive answered, it displays that
warning, since such a shell may skip `~/.bashrc` or `~/.zshrc`. An AppImage's
runtime puts its mount (`$APPDIR/usr/bin`, which holds the bundled baum) first
on the app's PATH; the app removes it from the PATH of every baum it runs, so
the daemon never inherits it and setup never reports it as a different baum.

The status also names each detected agent CLI's path and version and whether
baum supports that version. When one is too old, the agent's row says so,
offers no Connect (the old CLI's plugin commands may fail), and setup and
Settings show a command with a Copy button: the app's own baum by its full
path (the `baum` command may not be installed yet) and `desktop-setup`. Run in
the user's terminal, it records the directories that terminal finds `claude`
and `codex` in, and baum puts them first on its PATH from then on, so a Codex
set up only in `~/.bashrc` (fnm, nvm) is found; "Check again" reads the record
through the same status commands. Once the recorded copy is the old one, the
text asks to upgrade it in the terminal first. When baum finds neither agent,
setup's "Install an agent to continue" box and Settings offer the same command
under "Already installed?"; one agent missing beside another does not, since
most people use only one. Windows shows no command: it has no login shell to
miss an interactive PATH.

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

`desktop.json` is the user's file, so the app uses a setup record only when it has
this shape (a link named `baum`, a backup named as above, an absolute target
named `baum`), still blocks a package manager's baum and asks before
replacing anything it did not itself link, and asks for a password to remove
a link only when the record names the baum the app runs now. Remove deletes only what the app recorded: the link, while it
still points where the app put it (a link someone changed since is left
alone, and the screen names a backup still beside it), and puts back a baum
it had replaced; on Windows it takes the directory off the user `PATH`.
`desktop.json`'s `setup.commands` lists every link the app made and has not
removed: installing in a new place removes the earlier one, and one that cannot be removed (a
cancelled password prompt) stays listed, shown on the screen, until Remove
succeeds for it. Updating the app's own link keeps the backup of the baum it
replaced, so Remove still puts that back.
Hooks are removed with the bundled `baum hooks uninstall --user`, which
removes only the native `baum@baum` plugin and leaves the marketplace and
other plugins. Update, in Settings and in setup, runs the bundled `baum hooks update --user
--agent AGENT`, which refreshes the marketplace and brings the installed
plugin to its latest version, so new hooks arrive without removing and
connecting the agent again. Update shows only when status reports
`update_available` (a connected agent then reads Connected · update
available), or when Codex reports baum hooks missing (Some baum hooks are
missing).

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
- **Tray menu:** what the daemon is doing (Idle, "2 running, 1 queued"),
  which opens the jobs list while reviews run or wait and is inert otherwise;
  an item for each reason the icon is red, which goes where to look (see
  "Icon states"); each agent session asking for a permission or
  an answer (clicking one opens its transcript); "Plan usage left" with a
  line per readable plan window ("Claude Code · 5-hour: 62% left · resets in
  3 h"); Open baum; Pause or Resume automatic reviews; Start at login;
  a Notifications submenu (see
  "Notifications"); Report a problem…; Quit; Quit and stop background reviews. Plan usage shows
  only the plans the daemon could read and the person has not hidden in
  Settings (by default those of the backends baum uses), and nothing when
  none remain. The menu is rebuilt whenever a "resets in" text would
  read differently, which each read of the daemon checks (see the app API below).
- **Tray panel (macOS and Windows):** a left click on the icon opens a small
  borderless window under the icon (above it, on a bottom taskbar), drawn
  like a menu, with no title row (the icon names the app, as for the menu
  bar's own items) and no Open item: first a status dot (red while the icon
  is) with what the daemon is doing, which opens the jobs list while reviews
  run or wait (as in the menu) and is plain text otherwise; the Inbox's
  count, or "Your Inbox is empty" once the daemon counted none (nothing
  while the count is unknown), which opens the app; the alerts and the waiting sessions as highlighted rows that
  open them; the plan usage left as coloured meters; Pause or Resume
  automatic reviews; and Quit and stop background reviews. It asks the tray for its
  view every 2 s while it is visible, once when it is shown, and never while it is
  closed. It is placed on
  the icon's screen, found
  from the cursor in points on macOS (whose monitors report pixels at each
  screen's own scale) and from the icon's pixels elsewhere. It hides
  when it loses focus or on Escape. As in a native menu, `↓` and `↑` move
  through its items (from the first or last, wrapping), `Home` and `End` go
  to either end, and `Enter` or `Space` presses the focused one. The right
  click keeps the full menu. Linux tray hosts
  report no clicks, so Linux has the menu only. The panel is the bundled
  `tray.html`, created hidden at startup.
- **Inbox.** While the app's Inbox lists some pull requests, the menu shows a
  disabled line such as "3 pull requests in your Inbox", the tooltip appends
  it, the dock badge (macOS; any other platform whose window badge Tauri
  supports, ignored silently elsewhere) shows the count. The tray icon does
  not: its dot means only that something runs or needs the user. Zero, an
  unknown count and a daemon that is not
  running clear all of them. The daemon counts what the app's Inbox heading
  totals, by the same definition: each pull request waiting on the user, neither
  snoozed nor archived, once, whichever saved inboxes hold it or none, and whatever their badge
  switches say (see [`internal/inboxqueue`](../internal/inboxqueue/README.md)):
  `GET /api/index?view=waiting`, read with the other tray requests.
- **Quit** ends the app only. Background refresh and automatic generation
  continue in the daemon.
- **Quit and stop background reviews** runs the bundled `baum daemon stop`, so
  the daemon stops and is marked stopped by the user: hooks do not start it
  again until the app or `baum daemon ensure` does. When a review is running
  the app first asks, saying it will be interrupted (queued reviews have not
  started and are not mentioned as interrupted). If the daemon will not stop,
  the app shows the error and offers **Quit anyway**, which quits and leaves
  the daemon as it is; Cancel keeps the app open. The daemon's own stop is
  bounded (see the [daemon implementation](../internal/daemon/daemon.go)), so this is
  for a daemon that does not answer at all.
- **Pause automatic reviews** sets the daemon's global automatic switch
  (`PUT /api/automatic {"enabled":false}`), the same switch as the app's
  settings, so queued automatic jobs are cancelled and running ones finish.
  The item reads Resume while it is off.

### Icon states

| State | When |
| --- | --- |
| Idle | No dot: nothing running or queued, nothing needing the user. Pull requests waiting in the Inbox leave it idle. |
| Running | A blue dot: a review is running or queued. |
| Attention | A red dot: a job failed, a diagnostics check reports a problem, or the daemon is not running. |

Attention outranks Running.

Whenever the icon is red, the menu and the panel offer, under the status
line, one item per reason that goes where the user needs to look:

| Alert | Item | Following it |
| --- | --- | --- |
| A failed job | "Review failed: acme/widgets #7 · Open job" ("Fix failed", "Build failed") | Opens the job (`?page=jobs&job=<id>`) |
| More than three failed jobs | "5 jobs failed · Open jobs" | Opens the jobs list |
| Diagnostics problems | "A diagnostics check reports a problem · Open diagnostics" | Opens Diagnostics |
| No daemon | "Background reviews are not running · Open baum to restart them" | Shows the window, whose connecting screen restarts it |

Whether a failed job was seen is the daemon's record: every failed job
without `seen_at` turns the icon red, including one that failed while the
app was closed, until its alert is followed. That records it
(`POST /api/jobs/{id}/seen` sets the job's `seen_at` in `app.sqlite`; when
that fails, each read records it again until the daemon stops reporting the
job as unseen, and its alert stays hidden meanwhile), and a
failed job with `seen_at` is never flagged again, after a restart either.
Opening or focusing the window does not see a failed job (the window opens on
its own at launch); it acknowledges diagnostics problems, until one clears
and returns. Diagnostics warnings do not count. A missing daemon stays red
until it runs again. The tooltip shows the summary.

### How the app talks to the daemon

Two channels, each the least that does the job:

1. **The bundled `baum` command** for what changes the daemon's lifecycle:
   `daemon ensure --json` (attach) and `daemon stop`. It reaches the daemon
   over its control socket, owns the stopped-by-user marker, and the app needs
   no credential for it.
2. **The app API on loopback, with the launch capability as a bearer token**,
   for what the daemon exposes only there: `GET /api/jobs` (without parameters:
   the current jobs, which hold every running, queued and unseen failed job,
   not the whole history),
   `GET /api/automatic`, `GET /api/index?view=waiting` and
   `GET /api/live-sessions`, read when the daemon's events stream
   (`GET /api/events`, held open by a thread of its own and reopened with a
   backoff from 1 s to 30 s) announces a change, and then only the requests that event can affect:
   `progress` reads jobs, `automatic` the pause switch, `index` the waiting
   count, `live` the live sessions (`reset` and a kind this app does not know
   read all four; a `job` event, a running job's usage, changes no count and
   reads none). Everything is read when the stream (re)connects. The read
   follows a pause of 500 ms (at most 2 s) for a burst of events to end, and
   reads each request once for the whole burst; as a safety net also
   every 60 s while the stream is up and every 30 s while it is down, and
   never with no daemon attached,
   `GET /api/diagnostics` every 30 minutes (the daemon caches the report for 30
   minutes; only the app's Diagnostics dialog forces a fresh run),
   `GET /api/plan-usage` every 15 minutes (the daemon's cached reading, which it
   renews when older than 5 minutes), and `?refresh=1` when the tray icon is
   clicked (a fresh reading; the daemon reads again at most once a minute; no
   event announces either, so
   one worker sleeps until the next is due and does nothing with no daemon
   attached), and
   `PUT /api/automatic`, and once, when first-run setup finishes,
   `POST /api/automatic/catch-up`, which queues automatic reviews of the
   latest open pull requests so they run during the tour (best effort: a
   daemon not attached then, or a failed request, only skips it). This is the
   credential the app already reads for the window and the health check,
   accepted on `/api/` routes only; the requests that change state send the daemon's own origin and JSON, as the daemon
   requires. Tray change events carry no snapshots, so an event makes the
   tray fetch the data; the stream uses the same bearer token.

The tray and notifications share one native event stream. Its reader forwards
change notices to the tray and full `notice` payloads to a notification worker,
so OS notification calls do not delay tray updates. It starts beyond the latest
event and resumes from the last event ID on reconnect. Detaching or replacing
the daemon closes the old stream immediately (see "Notifications").

The tray menu adds no Tauri capability, and none names a remote URL: the
tray, the dialogs and the autostart entry are used from Rust only, so pages
the daemon serves still reach no command or plugin. The tray panel's
capability (`tray-panel`) lets only its bundled page, in its own window, read
the tray's last view (`tray_panel`), fit the window to its height
(`tray_panel_height`) and run Open, pause or resume, Quit and stop or open a
waiting session, or only hide the panel (`close`) (`tray_action`, which
refuses any other menu id).

## Report a problem

The app sends no telemetry or crash reports, so **Report a problem…** opens a
new bug report in the public repository (`baum-ai/baum`) in the default
browser. In the app (the sidebar's `?` menu and Diagnostics' button) it first
opens a dialog (`src/report-problem.tsx` in the local app) where the user
writes what happened and chooses what to attach: **Nothing**, **Versions
only** (baum's version, the OS and each tool's version, no paths or details)
or **All diagnostics** (the summary below), with a preview of exactly what
the link carries. The dialog builds the link from the redacted parts in the
`/api/diagnostics` answer (`report_issue`, `doctor.Report.PublicIssueParts`)
and fits it the same way. The tray menu and the macOS window menu's Help open
the link directly, with the whole summary. The daemon builds that link in its
`/api/diagnostics` answer
(`report_issue_url`) with the version, OS and a redacted summary of the
Diagnostics report (`doctor.Report.PublicSummary`): home directories become
`~`, secret-shaped values and e-mail addresses are masked, and the
configuration's load error, the `PATH` entries, the per-repository store
checks and non-github.com host names are left out. The
summary loses whole lines from its end, with a note, to keep the link under
about 6.5 KB, as GitHub refuses longer ones. The user reads and edits the form
in the browser, and nothing is sent until they submit it. The tray menu asks the
daemon for the link on each click (the checks take a few seconds), opens it only
when it starts with `https://github.com/baum-ai/baum/issues/new?`, and otherwise
(no daemon, failed checks) opens the repository's issue chooser. The tray panel
has no such item; its right click keeps the full menu.

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
daemon's `GET /api/events` stream shared with the tray (`src/events.rs`), so they
arrive while the window is hidden (closing it hides it) or minimised, for as
long as the app runs. Where no tray exists, closing the window quits the app
(see "Tray, closing and quitting"), and notifications stop with it. Pages the
daemon serves are not involved.

| Notification | Daemon event |
| --- | --- |
| Review finished, with the review's risk score | `notice` `review_finished` |
| Review failed, with its failure category | `notice` `review_failed` |
| Couldn't write the missing section details (a retry failed), with its failure category; under Review failed | `notice` `details_failed` |
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

Codex requests delegated to its automatic reviewer do not raise a permission
notification or mark the session as waiting. Its hook runs before review,
so baum checks the active turn's reviewer in the rollout, including session
overrides, before treating it as a request for the person.

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
- **Settings.** Each kind is on by default; an unspecified kind stays on.
  A "Notifications" submenu with one check item per kind is in the macOS
  menu bar and in the tray menu. Linux and
  Windows windows have no menu bar, so there the tray holds them. The choices
  and the permission answer are saved in `desktop.json`'s `notifications`
  section in the app's config directory
  (`~/.config/com.usebaum.desktop/` on Linux). Those saved settings are the
  only copy of the state: a click on either menu flips them, then both menus
  are rebuilt or updated from them. Native menus need no page capability.
- **Authentication.** The daemon accepts one bearer credential for the app,
  the launch capability it already holds for `/api/health`; it has no
  events-only or read-only scope. The app sends the capability only in an
  `Authorization` header, only to `GET /api/events` and the exchange, and
  never in a URL or log. The only other bearer credential is an agent's MCP
  token, which opens nothing but `POST /api/mcp` (see "MCP server for
  agents" in ARCHITECTURE.md). The stream
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
- **Saved.** The level is saved in `desktop.json`'s `window` section in the
  app's config directory and applied when the window opens and whenever a
  page starts loading. A missing, damaged or unknown level is 100%.

## Pointer leaving the window

On Wayland a pointer leave event carries no position, so GTK3 reports the last
one it saw, still inside the window after a quick move. WebKitGTK releases
without the fix for [WebKit bug 326201](https://bugs.webkit.org/show_bug.cgi?id=326201)
pass that to the page as a move inside it, so the page never learns the pointer
left: the folded sidebar opened under it stayed open over the page when the
pointer moved to the bar, an empty workspace or another screen. On Linux the
main window therefore listens for its web view's own leave events, skipping
grabs and moves into child widgets, and announces each one to the page as a
`baum:pointer-left` event (`src/pointer.rs`). The sidebar folds on it as it
does when the window loses focus.

## What pages may do

This section describes the Electron shell (`electron/`). Where the Tauri
shell (`src/`, shipped until the bridge release) differs, a note says so.

- The window loads only the bundled screens and the attached daemon's origin
  (`electron/main/navigation.ts`'s `decide`). Of the bundled files it shows
  exactly `/` and `/index.html`, and `/setup.html` only while the app shows
  the setup screen; any other path, including other spellings the asset
  server maps to a page (`/setup`, `/setup.html/`, percent-encoded names), is
  refused. The bundled pages' content security policy has
  `frame-ancestors 'none'`. Every other navigation is refused and nothing
  happens: other hosts, other loopback servers, other schemes (`about:`,
  `data:`, `blob:`, `file:`, `mailto:` and the rest) and any URL with a user
  name or password. A navigation never opens the default browser.
- The guard (`electron/main/windows.ts`) checks every navigation twice.
  Electron asks it about the page's own navigations and redirects
  (`will-navigate`, `will-redirect`) and about every frame's
  (`will-frame-navigate`): a frame navigating itself, a page setting a
  frame's `src` and a new frame alike. A session filter (`webRequest`) then
  refuses every frame load of a guarded window that the guard does not
  allow, at the network, so a load the events miss is refused too. A `data:`
  or `blob:` frame is refused. `about:blank` and `about:srcdoc` frames load
  nothing from the network and are not asked about: they take the page's
  origin and content security policy, which governs them. Unpackaged apps
  log a refused navigation's or frame load's scheme and host.
- Links to other sites open in the default browser as new-window requests
  (`target="_blank"` links and `window.open`, `decideNewWindow`): `http` and
  `https` URLs of hosts other than loopback ones, without a user name or
  password, go to the browser, and no window ever opens. Electron does not
  say which frame asked for a new window, so the guard cannot tell a page's
  request from a frame's. A tile frame is sandboxed without `allow-popups`,
  so Chromium refuses its requests before the shell is asked. Electron lets
  a script ask at any time, so the app opens the browser at most once a
  second (`OpenLimit`) and drops the rest. The check window and the tray
  panel open nothing.
- The app opens links through the `open_link` command
  (`electron/main/links.ts`), which the daemon's pages and the setup screen
  may call. It opens the same URLs as a new-window request, under the same
  once-a-second limit. The shell declares it as `openLinks: true` (below);
  the app (`app/src/outside-links.ts`) then turns a person's click on any
  link to another site, `target="_blank"` or not, into the command, and its
  own `openExternal` calls it too. With an older shell, which does not
  declare it, the app opens outside links as new-window requests and turns a
  click on a same-window link into one, so no link is dead. Setup opens
  GitHub's device page a few seconds after a sign-in starts, when it can no
  longer ask for a window, through the `open` action of `github_connection`,
  which opens that fixed address whatever the page passes. The Tauri shell
  added the command because WKWebView refuses a `target="_blank"` click as a
  navigation before it asks for a window.
- Before the page's own scripts run, the preload (`electron/preload/`)
  defines `window.__BAUM_DESKTOP__` in the main frame of the main window and
  of the check window (below), a frozen object that cannot be replaced, with
  three fields. `openLinks` says whether the page opens links to other sites
  with `open_link`; it is `true` in the main window and `false` in the check
  window, which may call no command. `checkWindow` says whether the page can
  open the tile check window; it is `true` on every platform (see Tile check
  window). `tileFrames` says whether the page may embed sandboxed frames of
  untrusted content served from the daemon's origin. It is `true` on every
  platform: the shell refuses every navigation of a frame away from the app,
  and Electron runs the preload, and so the command bridge and the
  declaration, in the main frame only (`nodeIntegrationInSubFrames: false`).
  The Tauri shell declares `tileFrames` on Linux and macOS and `checkWindow`
  on Linux only (`src/declaration.rs`).
- No page reaches Node.js or Electron: every window has context isolation,
  a sandboxed renderer and preload, no Node.js integration and no
  `<webview>`, and no web contents the app did not set up opens a window or
  attaches a webview. Pages of the app's origins may ask for notifications
  and clipboard writes; every other permission, and every request from
  another origin, is refused.
- Commands go over one IPC channel. The guard (`electron/main/bridge.ts`)
  runs one only when it comes from the main frame of one of the app's own
  command windows (the main window and the tray panel; never the check
  window), at a page whose role, resolved from the window and the frame's
  origin and path, the command table (`electron/main/commands.ts`) allows.
  A refusal says only that the command is not allowed. The Tauri shell
  grants the same commands through capability files (`capabilities/`).
- The daemon's pages (`daemon-pages`, the attached daemon's origin in the
  main window) may call CLI and hook status, installation and removal, plus
  `choose_repository`, the native folder picker the Repositories page's
  Browse button opens, which changes nothing and returns only the chosen
  folder. The page registers it through the daemon's own API. They may also
  call the zoom commands (`zoom_state`, `set_zoom` and `step_zoom`), which
  accept only the offered levels, `save_theme` (shape-checked, see First
  run), and the updater's (`update_state`, `check_for_updates`,
  `install_update`, `cancel_update` and `dismiss_update_notice`): a page can
  read the state, start a check, install only an update the app has already
  downloaded and verified, and record that a notice was closed. They may
  call `save_tile_type`, which Settings › Tile library's Export calls: it
  opens the native save dialog for `<name>.baumtile.json` and writes the
  exported file only where the person chose, refusing a name that is not a
  tile type's or a file over 3 MiB; `open_check_window` and
  `close_check_window` (see Tile check window); and `open_link` (see above).
  They can call no other command.
- The bundled screens have two roles, for the main window's bundled origin
  only: `connecting-screen` may call `connection` and `reconnect`, and
  `setup-screen` the setup commands (`setup_state`, `install_command`,
  `uninstall_command`, `install_hooks`, `uninstall_hooks`, `update_hooks`,
  `choose_repository`, `add_repository`, `review_settings`,
  `save_review_settings`, `github_connection`, `repositories` and
  `finish_setup`) and `open_link`. Both may call `step_zoom` for the zoom
  shortcuts. Every change they make runs in the main process and only when
  the user requests it in setup or Settings; replacing a different baum also
  requires the confirmed path. Daemon pages share `setup_state`, the four
  CLI and hook install/removal commands, `update_hooks` and
  `choose_repository`. GitHub connection, repository registration, review
  settings and finishing setup remain restricted to the bundled setup
  screen. The tray panel's page (`tray-panel`, macOS and Windows) may call
  only its own commands.
- The main process alone opens the browser (`shell.openExternal`) and shows
  dialogs and notifications. In the Tauri shell the opener, dialog and
  updater plugins are used only from Rust, the opener's click script is
  off, and no page is granted any plugin's commands.

The app itself makes no network requests other than loopback requests to the
daemon and the update checks below. Outside its own configuration and data
directories it writes only what the user installs through setup or Settings: the `baum` link
(or the user `PATH` entry on Windows), and, through the bundled baum, the
agent marketplace plugin installations. Notifications use the operating
system's notification service from the main process only.

## Tile check window

The daemon's page checks a custom tile by rendering it in a second window,
the tile check window (`electron/main/check-window.ts` and
`check-window-electron.ts`), so that a tile whose script never yields stalls
that window and not the main one. The person never sees it: it is hidden,
1280×900, has no frame, cannot take focus and stays out of the taskbar. Its
timers are not throttled, so its page runs as a visible one does
(`document.visibilityState` is `visible` and animation frames run).

- The main window's daemon page opens it with `open_check_window` (no
  arguments; it does nothing when the window is open) and closes it with
  `close_check_window` (no arguments), when a check hangs or after the window
  has been idle. Only the main window can call them; a close returns once the
  window is gone, so an open right after it builds a new one. The shell also
  closes it when the main window starts loading another page it allows and
  when the app starts attaching again or detaches; an open still waiting
  behind a close then builds nothing.
- It loads `/s/<namespace>/?page=tile-check` at the origin of the daemon page
  the main window shows, under the main window's navigation rules (`decide`,
  without the setup screen, and the same network check of frame loads). It
  never opens the browser: every request for a new window is refused.
- It is not one of the app's command windows, so the guard refuses every
  command its page calls; the page reports its results to the daemon over
  HTTP.
- It runs in a session of its own, in memory (`check-window`), so Chromium
  never runs its page or its tiles in a process the main window uses.
  Chromium runs sandboxed frames apart from their page, in one process per
  site that every window of a session shares, so in the main window's
  session a tile spinning in the check window would stall the main window's
  tiles and could not be ended alone. The session gets the default
  session's guards (permissions, the network check of frame loads; no
  window attaches a webview). Before each load the shell copies the cookies
  the default session holds for the daemon's host into it, with their
  attributes (the daemon's session cookie is HttpOnly, `SameSite=Strict` and
  scoped to `/s/<namespace>/`), so the page is signed in as the main
  window's is, and it empties the session when the window closes. The check
  page reads nothing the main window keeps in `localStorage` or
  `sessionStorage`: its runner gets its jobs and the app's appearance from
  the daemon.
- `close_check_window` first asks the page to close (as closing a window
  does, so its `pagehide` runs) and waits 200 ms: a page that still answers
  unloads, and a check it was drawing is reported abandoned and ends as
  unavailable rather than as a hang that blocks its tile type (the main
  window loading another page closes the window mid-check). It then ends the
  window's renderer (`forcefullyCrashRenderer`) unless another window's page
  runs there, destroys the window, and ends any other process only the
  window's frames used, if it is still one of the app's renderers. A page
  that never yields ignores the request and is ended after the wait.
- It is offered (`checkWindow`) on every platform: the process model is
  Chromium's everywhere. The Tauri shell offers it on Linux only, where
  WebKitGTK gives it a web process of its own that the shell ends with
  `webkit_web_view_terminate_web_process`; on macOS it runs the checks in the
  main window, and on Windows the page embeds no tile frames.

The shell harness (Checking navigation, below) checks it on Linux: a mock
daemon's main page opens the window, whose page holds a sandboxed frame that
spins. It fails unless the check window's page and its tile run in processes
the main window's page and tiles do not use, the main window's 100 ms
heartbeat never stops for a second while the frame spins, the spinning
process's CPU use drops within a second of `close_check_window` and the
process is gone a second later, a second open while the window is open loads
nothing, a window opened again after closing works, an open right after a
close builds a new window, the main window loading another page closes it,
closing leaves no cookie in the check window's session, every check page
request carries the daemon's HttpOnly session cookie, and the check page
sees the main window's script cookies and the declaration with
`openLinks: false`, can call no command (`open_check_window`,
`close_check_window`, `zoom_state`, `open_link`), and cannot navigate to or
open another site. It also fails unless the main window's own tile runs in
a process apart from its page, and a tile spinning in the main window
leaves the page's heartbeat running.

Because Chromium runs a tile's frame apart from the check page, a tile that
loops before it says hello leaves the check page running: the daemon's
missing-heartbeat timer never fires, and the runner itself reports the
hang (a frame with no hello in 5 s that does not answer the kit's `ping`
within 2 s and never finished loading; see "Render check" in
`oss/ARCHITECTURE.md`). `tests/shell/check-runner.spec.ts` checks that path
end to end with the app's own check host and runner, bundled from
`oss/app/src` with React and `api.ts` stubbed, and the kit's real bootstrap
in the frame: the main page opens the check window on a `tile_check` event,
the runner claims the job from the mock daemon and draws a frame that
spins, and the spec fails unless the frame runs in a process apart from the
check page, the check page's heartbeat keeps running while it spins, the
report reaching the mock holds `hang` (not `no_hello` or `timeout`), the
`hung` the mock then sends closes the check window, the spinning process's
CPU use drops within 2 seconds of the report and the process is gone, and
the main window's 100 ms heartbeat never stops for a second.

On macOS and Windows a person checks, before a release that changes the
check window, with an unpackaged app started with
`--remote-debugging-port=9222` and the main window's page inspected from
`chrome://inspect` in Chrome or Edge:

1. `await baumDesktop.invoke("open_check_window")`: Activity Monitor (macOS)
   or Task Manager's details (Windows) shows a new renderer process.
2. In the check window's page (also listed in `chrome://inspect`),
   `setTimeout(() => { for (;;) {} }, 1000)`: the main window stays
   responsive and its timers keep running.
3. `await baumDesktop.invoke("close_check_window")` in the main window: the
   spinning process's CPU use drops within a second and the process ends.
4. A window opened again after closing works.

### Checking navigation

`just desktop::electron-shell-test` (in the `desktop-electron` container;
`pnpm build && pnpm test:shell` in `oss/desktop` elsewhere, which needs
Xvfb) runs the harness in `tests/shell`, and CI runs it on Linux for every
change to the desktop app. It launches the built shell with Playwright under
a virtual display, with a home of its own, attached through the real attach
path to a mock daemon: a fake `baum` (`BAUM_DESKTOP_BAUM`) answers `baum
daemon ensure --json` with the mock's status and leaves a private credential.
Another site (`attacker.example`, which Chromium resolves to 127.0.0.1 there)
and another loopback server record any request that reaches them, and
`shell.openExternal` is recorded instead of opening a browser. The mock
serves each page with the daemon's Content-Security-Policy or with none, as
Vite's development server sends none, so nothing but the shell stops a
frame. Clicks are real pointer input. An unpackaged app run with
`BAUM_DESKTOP_LOG_COMMANDS=1` logs each command it runs and for which role,
so the harness can tell the page's calls from a frame's. It fails if:

- a sandboxed frame, with the frame route's policy or only
  `sandbox allow-scripts`, sees `baumDesktop` or `__BAUM_DESKTOP__`, reaches
  the page's (`parent.baumDesktop` must throw), finds `require`, `process`,
  `ipcRenderer` or another shell's bridge, or makes a command run, by any
  call or by posting the page a message shaped like one;
- a frame navigating itself (location, meta refresh, a real click on a
  same-window link), a `target="_blank"` or `target="_top"` click, a
  `window.open` click or loop, a form submitted to the top or to itself, a
  nested frame or the page setting a frame's `src` makes any request to
  another site or local server, opens the browser, or moves the page; and,
  without the daemon's policy, if the guard did not refuse the navigation
  itself;
- with `will-frame-navigate` taken away, the network check does not refuse a
  frame navigating itself and a frame the page inserts;
- the page's own `target="_blank"` link does not open the browser exactly
  once, a same-window link to another site is not refused, or a link to the
  daemon's own page does not navigate;
- `open_link` does not open a link once, a second within the second is not
  dropped, or a `file:`, `data:`, `javascript:`, loopback or userinfo URL is
  not refused; `window.open` in a burst opens more than once, every 100 ms
  for 2.5 s fewer than two or more than three times, or opens such a URL;
- the main frame navigates to another site, another local server, `file:`,
  `data:` or a URL with a user name, or a `javascript:` URL moves it;
- a frame in the setup screen or the connecting screen sees the bridge or
  the declaration while the screen's own calls run;
- any check of the check window above fails.

The tray panel exists on macOS and Windows only, so the harness does not
check it; its window loads only `tray.html` and refuses every navigation and
new window.

Electron's preload, its navigation events and Chromium's sandbox behave the
same on macOS and Windows, but a release that changes the shell is checked
there by a person too, with an unpackaged app started with
`--remote-debugging-port=9222` and its pages inspected from `chrome://inspect`
in Chrome or Edge. Its page comes from the Vite server under
`desktop::dev`, which sends no Content-Security-Policy, so nothing but the
shell stops a frame. In the main window's console:

1. `Object.assign(document.createElement("a"), { href: "https://example.com/?same-window" }).click()`:
   nothing happens and no browser opens (the click is a script's).
2. Load a frame from the page's own origin, which the shell allows, and drive
   it:
   `const f = document.createElement("iframe"); f.sandbox = "allow-scripts allow-same-origin"; f.src = location.href; document.body.append(f)`.
   Once it has loaded, `f.contentWindow.location.href = "https://example.com/?frame"`:
   the frame stays and no browser opens.
3. Reload the frame (`f.src = location.href`), then
   `f.contentDocument.head.insertAdjacentHTML("beforeend", "<meta http-equiv=refresh content='0;url=https://example.com/?refresh'>")`:
   no browser opens.
4. Reload it, then
   `f.contentDocument.body.innerHTML = "<a target=_blank href='https://example.com/?popup' style='font-size:48px'>popup</a>"`
   and click the link with the mouse: no browser opens (no `allow-popups`).
   Repeat with `target=_top` (no `allow-top-navigation`).
5. Click links to other sites as a person does: "Open on GitHub" in a pull
   request's menu, a link in a review's Markdown, the docs in the `?` menu, the
   release notes link of an update notice. Each opens the default browser,
   once, and the app logs no refused navigation for it.
6. `for (let i = 0; i < 5; i++) open("https://example.com/?loop" + i)`: the
   browser opens once at most.
7. In setup's GitHub step (first run, with a fresh configuration directory),
   sign in: the browser opens GitHub's device page about five seconds later.
8. Reload the frame, then `f.contentWindow.eval("for (;;) {}")`: the frame
   is same-origin here, so it runs in the page's process and the window
   freezes; the tray menu still answers, and quitting from it and starting
   the app again recovers. A sandboxed tile frame (no `allow-same-origin`)
   runs in a process of its own and leaves the window running (see
   SECURITY.md).

The Tauri shell was checked on Linux by WebKitGTK harnesses
(`tests/navigation` and `tests/check-window`, now in git history only) and by
hand on macOS and Windows with the same steps in the Web Inspector.

## Updates

A release build checks for a newer release about 30 seconds after it starts
and then once a day (by the wall clock, so a computer that slept through the
day checks when it wakes). A full release (1.2.3) reads `latest.json` from
the latest GitHub release of baum-ai/baum, which is never a pre-release, so it
updates only to full releases. A release candidate (1.2.3-rc.1) reads
`latest.json` from the `desktop-rc-updates` pre-release instead, which every
release, candidate or full, replaces (see Releases), so it updates to the
next candidate or to a newer full release, whichever came last. Local and snapshot builds (version 0.0.0) and debug
builds never check; `BAUM_DESKTOP_UPDATES=0` turns checks off.

The updater never opens a dialog. Settings → **This computer** shows the
app's version and the updater's state in its first card (`update_state`):
up to date with the time of the last check, checking, downloading, ready,
waiting for running jobs, or installing, and why the last attempt failed.
**Check now** checks at once (`check_for_updates`). While an update is ready
to install, or available to download where the app cannot install it, the
sidebar counts it in the Settings badge (as 1, beside any problems), and
the **This computer** entry in Settings carries the same green 1 and says
"Update available". When an update becomes
available, and after every launch of the app while one is, the page also
shows a toast ("baum 1.2.3 is ready to install") that stays until it is
closed, with **Open This computer**. Closing it, following it, or already
looking at This computer is remembered until the app quits
(`dismiss_update_notice`), so reloading the page or reattaching to the
daemon does not bring it back; a newer version does. The page reads the
state every minute, and every second while the updater works.

- The macOS app, the Linux AppImage and the Windows installer update
  themselves. The app downloads the update in the background; the download
  must carry a signature from the release key whose public half is
  `plugins.updater.pubkey` in `tauri.conf.json`, made for the version the
  manifest announces (`requireSignedVersion`), or it is discarded. A download
  that receives nothing for two minutes is abandoned until the next check.
  **Update and restart** (`install_update`) installs it: while the daemon
  runs a generation job (`baum daemon status`, then `GET /api/jobs`, every
  15 seconds; queued jobs survive a restart), the card says how many it
  waits for and offers **Cancel** (`cancel_update`), then the app installs
  the update and restarts. On Windows the app stops the daemon first, and
  the installer (whose hook stops it too) closes the app, updates it for the
  current user and starts it again; the updated app refreshes its copy of
  `baum.exe` and starts the daemon. An updated AppImage refreshes its copy of
  `baum` in the data directory on the next start, renaming the new file into
  place, so a daemon still running the old copy keeps it until the updated
  app's `baum daemon ensure` replaces that daemon.
- A deb install, or any copy the updater cannot replace (an AppImage in a
  directory the user cannot write, an app on a read-only volume), only says
  that a new version exists and links to its release page. Updating it is
  the user's job, as with any package. A version whose installation failed
  is remembered (`desktop.json`'s `updates` section in the app's config
  directory) and from then on only offered this way.
- Restarting the app does not stop the daemon. The first client to run the
  new `baum` replaces an older daemon, as after any upgrade.

## Electron shell (in progress)

An Electron shell in `electron/` is replacing the Tauri one. The Tauri shell
(`src/`) stays the shipped app until the migration's bridge release. So far
the Electron shell:

- Opens the main window on the setup screen or the connecting screen.
- Attaches to the daemon as described in "Attaching to the daemon", and
  loads the daemon's pages.
- Hides the window when it is closed, and keeps a single instance. Where no
  tray is showing, closing the window asks "Quit baum?" instead.
- Applies the rules in "What pages may do".
- Runs first-run setup and "Command Line and Hooks…", and runs the stable
  copy of `baum` where the app keeps one.
- Shows the tray, its menu and, on macOS and Windows, its panel. On macOS it
  also sets the window's menu bar; on Linux and Windows the window has none.
- Reads the daemon's event stream once, for the tray, the notifications and
  the health check, which waits 30 seconds while the stream is open.
- Shows notifications, with their toggles in both menus.
- Zooms with the shortcuts and Settings, and applies the saved theme before
  the first paint. The preload asks the main process for the theme.
- Opens links to other sites in the browser, at most once a second.
- Saves tile type files, keeps start at login and the AppImage's menu entry
  current, and quits with or without stopping background reviews.
- Opens the tile check window, on every platform (see Tile check window).

The updater is still being ported.

- `electron/main/` runs in Node.js in the main process.
- `electron/preload/` runs sandboxed in each window's main frame. It exposes
  `window.baumDesktop.invoke` and the `__BAUM_DESKTOP__` declaration.
- Every command goes over one IPC channel. `electron/main/bridge.ts` runs a
  command only from the main frame of the app's own windows, and only if
  `electron/main/commands.ts` allows it for the page's role.
- The bundled screens are served from `dist/` as `app://bundle`. The tray panel has its own origin, `app://tray`, so the window's zoom does not scale it.
- The app keeps Tauri's identifier (`com.usebaum.desktop`), so it uses the
  same `desktop.json`. Chromium's files go in `chromium/` under the same
  folder.
- In the app, `desktopShell()` (`../app/src/desktop/native.ts`) finds either
  shell's bridge.

Checks and runs happen in the `desktop-electron` Docker service:

```sh
just desktop::electron-check       # format check, typecheck and unit tests
just desktop::electron-shell-test  # the harness in tests/shell (see Checking navigation)
just desktop::electron-fmt         # format
```

To run the app under Xvfb against a real daemon, first build `dist/` and the
bundled `baum` (`just desktop::frontend`, `just desktop::sidecar`). Then, from
the repository root:

```sh
BAUM_OP_RUN=0 ./dev-compose.sh run --rm --no-deps desktop-electron sh -c \
  'pnpm build && HOME=$(mktemp -d) BAUM_DESKTOP_BAUM=/oss/desktop/binaries/baum-x86_64-unknown-linux-gnu \
   BAUM_DESKTOP_SCREENSHOT=/oss/desktop/build/shot.png \
   xvfb-run -a node_modules/.bin/electron . --no-sandbox --enable-logging'
```

`BAUM_DESKTOP_SCREENSHOT` applies only to an unpackaged app. It saves what the
window shows a few seconds after each page load. `BAUM_DESKTOP_LOG_LINKS=1`,
also only for an unpackaged app, logs the links the app would open instead of
opening a browser. Stop the daemon afterwards with `baum daemon stop`.

## Development

`just desktop::frontend` runs the shared local-app Vite build in `ui-test`.
Desktop check, test, build, bundle, run and dev recipes build that frontend first.
Rust builds and checks run in the `desktop-test` Docker service (Rust, the
WebKitGTK and GTK development packages, and the Tauri CLI). The bundled
binary is built in `oss-test`. From the repository root:

```sh
just desktop::check         # cargo fmt --check, clippy -D warnings, tests, updater-test
just desktop::test          # tests only; pass cargo test arguments after it
just desktop::sidecar       # build binaries/baum-<target triple>; pass a triple to cross-build
just desktop::build         # Linux bundle (deb by default) in target/release/bundle
just desktop::bundle        # the release's Linux deb and AppImage in target/release-bundles
just desktop::dmg           # macOS: ad hoc signed DMG in target/local-dmg (--install also installs it)
just desktop::dmg-layout    # regenerate the DMG window layout and background in macos/
just desktop::updater-test  # updater manifest checks reject tampered releases
just desktop::run           # run the app on this Wayland session's display
just desktop::isolated      # run an app with its own home, keyring and daemon (see below)
just desktop::profile       # time a scripted journey in the headless app (see Profiling)
just desktop::dev           # native host app, with Vite hot reload in Docker
just desktop::icons         # resolve palette tokens and regenerate native icons
```

`isolated` runs the app from this checkout beside the installed one, each
instance with its own home, credential store and daemon, so several
branches can be tried at once on Linux with Wayland:

```sh
just desktop::isolated mobile --copy-data --workspace ~/workspace
just desktop::isolated-shell mobile   # a login shell in it, e.g. to run claude
just desktop::isolated agent --headless   # no window: for agents and profiling
BAUM_RELAY_URL=https://… just desktop::isolated mobile
```

An instance's home stays between runs under
`${BAUM_DEV_CACHE_DIR:-~/.cache/baum-dev}/desktop-homes/<name>`; delete the
folder to start again. Its container is `baum-desktop-<name>`, and only one
can run per name. The `desktop-isolated` image adds gnome-keyring on a D-Bus
session, so sign-in can store credentials, plus git, ssh and sqlite3. The
first run installs Claude Code into the home and copies `~/.gitconfig` with
commit signing turned off, since the signing key is not there.

`--copy-data`, when the instance has no data yet, copies this machine's
repositories, reviews and settings with consistent SQLite snapshots. It
leaves out `credentials.json`, the daemon's runtime folder and locks, and the
Claude and Codex account folders the settings name (data extraction's too), which exist only on the
host. `--workspace DIR` mounts DIR read-only at the same path, so the copied
repositories are found. The daemon always opens a repository's existing
review store (`.git/baum/reviews`), so each one under DIR gets a writable copy
in the instance's `review-stores/`, mounted over it; `--copy-data` fills new
copies with a snapshot of the repository's reviews, and without it they start
empty. `BAUM_RELAY_URL` is written to the home's `.profile`,
which the daemon's environment comes from.

`--seed-data[=SEED]`, instead, fills a home that has no data yet with the
seeded profiling dataset ([`seeddata`](../internal/seeddata/README.md),
seed 1 by default): 50 repositories with 1,200 open pull requests and about
1,650 saved review versions, one pull request with a 5,000-line diff, Claude
Code and Codex sessions of up to 3,000 prompts and 20 inboxes. On every
machine a seed gives the same lists, pull requests, commits (by SHA),
reviews, inboxes and transcripts, so before and after numbers compare;
`--copy-data` copies this machine's data, which differs between machines and
changes over time. Clone paths and the transcripts' working directories
follow the home's path, so the dataset's digest is the same only for the
same seed and the same home path. It is generated in `oss-test` (about 15
seconds) and counted in `~/.local/state/baum/seeddata.json` in the
instance's home, written last: a failed generation removes what it wrote. A
home with other baum data and no `seeddata.json` is refused rather than run
online. The daemon serves it with no GitHub and no model: the dataset names
a fake GitHub token whose discovery cache it seeds, automatic reviews are
off, and an instance whose home holds the dataset runs with an unreachable
proxy for everything but loopback, so a refresh fails at once and the cache
stays. The first start of a new home still downloads Claude Code into it,
before the proxy is set, as every isolated home does; after that nothing
leaves the machine. Its
pull requests show as not refreshed from GitHub, and GitHub's panels on a
pull request (checks, comment text) as unavailable. The sessions are recorded
as ended when the dataset is written, and the Sessions page lists an ended
session for a day; delete the home to write it again.

```sh
just desktop::isolated perf --seed-data --headless
just desktop::isolated perf2 --seed-data=7 --headless
```

`--headless` runs the instance without a display of yours: the container
starts its own headless Weston (`BAUM_ISOLATED_WIDTH` × `BAUM_ISOLATED_HEIGHT`,
1440 × 900 by default) and the app draws there, so agents can run the real
WebKitGTK app with no window on your screen and no Wayland session at all.
`just desktop::isolated-screenshot <name> [out.png]` saves what it shows.
Weston's headless backend renders with Pixman, in software, and has no input
seat (GDK logs `gdk_seat_get_keyboard` assertions, which are harmless here),
so JavaScript, layout and React costs are realistic and painting and
compositing costs are not.

`BAUM_FRONTEND_MODE=profile` before any recipe that builds the frontend
(`isolated`, `run`, `build`) builds the profiling build
([local app](../app/README.md)) instead, and the sidecar embeds it, so the
daemon's pages expose `window.__baumPerf`:

```sh
BAUM_FRONTEND_MODE=profile just desktop::isolated perf --copy-data --headless
```

### Profiling

`just desktop::profile <scenario>` measures how fast the app's JavaScript
renders and reacts, for people and agents tuning it. It builds the profiling
frontend, the viewer and the sidecar (which stay the profiling build until
another recipe builds them), starts a `--headless` isolated instance
(`profile` unless `--name` says otherwise) and, inside its container, runs
`../app/scripts/profile/run.ts` with Node. The runner starts
`WebKitWebDriver` (Ubuntu's `webkit2gtk-driver`, pinned to the version of
the app's WebKitGTK), which launches the app's debug build with
`TAURI_WEBVIEW_AUTOMATION=true`, so WebDriver drives the real main webview
from the connecting screen on to the daemon's page. Key presses are WebKit's
own synthesized events, which reach the page without an input seat, and the
collector records them as interactions. The runner marks the instance set up
(`desktop.json`), so it opens on the Inbox rather than first-run setup, and
closes the first-run tour if it shows.

```sh
just desktop::profile jk --runs 10                  # seeded dataset, seed 1
just desktop::profile review --seed-data=7 --name profile-7
just desktop::profile cold-start --name mine --copy-data --workspace ~/workspace
```

Profile on the seeded dataset (`--seed-data[=SEED]`, above): a seed gives
the same pull requests, reviews, inboxes and sessions on every machine and
every run, and the instance runs offline, so numbers before and after a
change, or from two machines, compare. An instance that has no data yet
and is given neither flag is seeded with seed 1, so the plain command does
this. An instance that already has data keeps it, so `--copy-data` users
get what they had; a seeded home is never mixed with copied data
(`isolated.sh` refuses). `--copy-data` and `--workspace DIR` work as for
`isolated`, the copy made once per instance: this machine's data shows how
the app behaves on it, but differs between machines and changes over time.
The summary's environment line says which data a profile ran on (the seed
and the dataset's digest), the WebKitGTK package version and the CPUs.
`--runs N` (5 by default) sets the measured runs,
after one warm-up whose numbers are kept apart. The recipe checks the
scenario and the options before it builds anything. `--run-timeout S`
(600 by default) fails a run, the launch or the preparation that takes
longer, as a hung WebKitWebDriver would, with a message saying so. The container runs on
`BAUM_DEV_CPUSET`, like every development container, and the summary names
the CPUs it had. The scenarios:

| Scenario | Journey | Needs |
| --- | --- | --- |
| `cold-start` | Quit and launch the app, until the Inbox shows content and settles; `launch_to_content`, from the launch to the Inbox's first content frame, is the headline | |
| `jk` | 50 presses of j and k down the list with the most rows (an inbox or Pull requests) and back; each must move the selection one row | a list of two rows |
| `review` | From the Inbox, open the largest reviewed pull request (lines changed, else review size), then read up to 12 of its places: scroll each to its end, 400 px a frame (at most 500 steps), and Shift+J to the next | a review |
| `filter` | `/` on the Inbox, type `retry` a key at a time, Enter to search titles; reports the Inbox's rows before and after | the Inbox's filter |
| `switch-inboxes` | Shift+J through up to six inboxes and Shift+K back, each settled; each must switch | two inboxes |
| `session-panel` | Open the session panel with Control+\` on the latest console conversation (a running one if any) and keep it open 60 s | the console |
| `pages` | Every page and its main sub-views, rendered two ways and ranked slowest first (below) | |

`pages` is the scenario for making every page render fast: it ranks the
pages, so the slowest is the one to work on, and a change shows as that
row's numbers going down. It covers the sidebar's pages (Inbox, Sessions,
Timeline, Pull requests, Analytics, Repositories, Jobs, Settings) and the
sub-views of a pull request's review (the largest reviewed pull request
the Pull requests page links to), a session's transcript (the largest),
New inbox and Edit inbox (opened, never saved); the summary lists the app's
routes it does not measure. The pages are read from the sidebar and checked
against the app's own page list (`src/pages.ts`): the run fails if the two
differ, or if the app has a page the scenario does not know (add it to
`KNOWN` in `app/scripts/profile/pages.ts`), so a new page is not left out
unseen.

Each page is measured two ways. `nav` moves to it as a person does, from the
page before: Ctrl+1 to Ctrl+8 where they work, else a click on the sidebar's
link (WebDriver's synthesized keys leave the key's place, `code`, which the
app reads, empty; the summary says so, and each run probes it once before
its first mark); a sub-view clicks its link on the page, else uses the
history entry the app's own links make. Its times run from the start of that
navigation, with the app's code and in-memory caches loaded, so a `nav` row
depends on the pages before it. `direct` reloads the page's address and its
times run from the document's own start, so the bundle's fetch and parse are
in them. Which of the two goes first alternates from run to run.

A page must show its own content (a row, a field, a message: never a wrapper
around a loader) or its own empty state, and a failure panel, a "not
available" or "not found" panel, or a loader that stays fails the run. A
page the data should fill (the Inbox, Pull requests, Repositories, a review,
a transcript, and Sessions when a session runs) fails the run if empty. Pages
that seeded data leaves empty (Timeline, Jobs, and Sessions: its sessions
all ended and "Ended recently" starts folded) report `empty` in the table and
are not timed; a page empty in some runs only, or empty one way and not the
other, is flagged. Analytics' Delivery card reads GitHub, which an offline
instance never answers promptly, so its content and settled times can be
missing: the table says in how many runs (`n/N`).

The `## Pages` table has a row per page and way, slowest `content` first,
then the empty ones: `content` and `settled` are the collector's navigation
times (first frame with no loader on screen, then with no request in flight
as well), `commits` and `render ms` the root `app` region's in the page's own
window (between marks around it), `long frames`, `requests` and `request ms`
those that began in it (a request begun in the window counts whole), and
`DOM` the element count once it settled. Numbers are medians over the runs.
The summary's other sections (metrics, interactions, components, requests)
describe a last load of the Inbox, the same every run, and are not the
scenario's result.

A scenario the data cannot run exits with status 3 and a summary saying
why. A journey that does not go as planned fails rather than report
numbers for something else: a j or k that did not move the selection one
row, an inbox switch or Shift+J that did not move, a filter that left the
Inbox's rows as they were or none.

Each run starts from the same state. After preparing, the runner saves the
daemon's record of the Inbox (what was seen today, snoozes) and the
window's storage (the last inbox shown, the sidebar, the session panel),
and puts them back before every run, from the settings page, which holds
neither; typed filters (which the daemon keeps) go back to none, and the
filter scenario also clears its own after each run. What it cannot put
back: the daemon's and WebKit's caches, which the warm-up fills, a new day
starting (the Inbox's record of the day starts empty then), and anything a
journey asks the daemon to do, which none of these does. Profiling clears
an instance's typed filters.

Every run but cold-start's loads its start page afresh in the same app,
so its JavaScript heap and React state start clean, then resets the
collector: relaunching would add some ten seconds a run and change nothing a
page reload does not. Cold start must launch, so each run quits the app and
starts it again; the daemon keeps running between launches, as it does for a
user, and WebKit's cache in the instance's home stays warm. The runner waits
for each key press's frame, then 50 ms, before the next. Waiting, it polls
the page and talks to WebKitWebDriver, which adds a roughly constant
overhead to interaction durations: fine for comparing before and after a
change, not for absolute numbers. Modifier keys pressed for the key after
them (Shift for Shift+J) are left out of the interactions.

Each profile goes to `target/profile/<scenario>-<time>/`, whose path the
command prints at the end:

- `summary.md`, short enough to read whole: the metrics' median, p95 (which
  interpolates between the slowest runs, so with few runs it is near the
  maximum), minimum and maximum over the runs; interactions by key and the
  slowest of them; render time and commits per `<Profiled>` region; the
  requests that took the most time; and the JavaScript and CSS chunks
  `index.html` loads, with their sizes and gzip sizes, and the largest of
  the rest, since parsing them dominates cold start. They are the profiling
  build's, with React's profiling build and the collector; production's
  are somewhat smaller. `render_total` and `commits_count` are the `app`
  region's, which holds the others.
- Both files are written after every run and marked partial until the
  last, so a failure keeps the runs before it.
- `metrics.json`: the same, with every run's metrics and the collector's raw
  snapshot (`__baumPerf.snapshot()`, [local app](../app/README.md)).
- `driver.log`, WebKitWebDriver's output, and `failure.png`, the window
  when a run failed.

The numbers come with the headless instance's limits. Weston renders with
Pixman, in software, so script, style, layout and React times are
realistic and paint and compositing times are not; frame gaps include
software painting. The app's Rust shell is a debug build, as `isolated`
runs it; the measured pages come from the daemon and run in the system's
WebKitGTK, so that only slows the shell's own commands. Data copied with
`--copy-data` holds no GitHub sign-in, so the daemon finds no open pull
requests and the inboxes and Pull requests stay empty: `jk` skips, and
`filter` types into an empty list. The seeded dataset has them. Its
sessions are recorded as ended, so in `session-panel` nothing streams: it
measures the panel open on an ended conversation, and the summary says so.

#### CPU profiles in Chromium

The WebKit runs say what is slow (interactions, frames, renders) but not
why. `--engine chromium` runs the same scenarios in Chromium and records a
CPU profile of every measured run, which says which functions the time went
to:

```sh
just desktop::profile review --engine chromium --runs 3
just desktop::profile jk --engine chromium --runs 3
```

The instance starts as usual, with its daemon, its data and the profiling
frontend in the sidecar, but no app: the runner starts the daemon with the
bundled `baum daemon ensure`, as the app does, and Playwright's Chromium
(Chrome for Testing, headless, in the `desktop-isolated` image) opens the
launch URL from its `credential.json` (the capability in it is never
logged: chromedriver logs only severe messages, and the runner scrubs
`#token=` from `driver.log`; see
[Attaching to the daemon](#attaching-to-the-daemon)). chromedriver of the
same version drives it with the same WebDriver commands as WebKitWebDriver,
so the scenarios are the same code. The image takes both at the Playwright
version `../app`'s lockfile pins (`chromium.test.ts` checks), from Chrome for
Testing, which has chromedriver for x86-64 Linux only, so on another Docker
engine `--engine chromium` stops before building anything. Each measured run
(not the warm-up) samples the page's main thread every 250 µs through
chromedriver's DevTools Protocol endpoint (`Profiler.start` and `stop`):
enough samples to tell a 16 ms frame's stacks apart at a few percent of
overhead, where 100 µs costs more on long journeys and makes long profiles
large.

The summary then ends with a CPU section:

- the time a run spent in JavaScript, outside it (`(program)`: style,
  layout, paint and the browser's own work), in garbage collection and idle,
  from the collector's reset to its snapshot, the window the rest of the
  summary measures (cold start relaunches the browser each run and counts
  from the page's start, sampling from before it loads, which adds the
  profiler's start to its launch times);
- the 20 functions with the most self time per run, with their total time
  (callees included, a recursive function counted once). Each is named by
  the profiling build's source maps (`oss/internal/localapp/dist/assets/*.map`),
  at the line its function starts, `oss/app/src/up-next.tsx:1396`, not the
  hot line inside it: V8's line ticks give a bundle line without a column,
  and the minified bundles are a few lines long, so they map to nothing. A
  package's functions show as the package (`react-dom`) but are told apart
  by their own source line; `browser` marks the DOM's and V8's own
  functions, whose time includes the style and layout they force
  (`getBoundingClientRect`, `scrollIntoView`). A minified name the map
  cannot resolve keeps its bundle place beside it. `(program)` and
  `(garbage collector)` are rows too, and often the top one: time outside
  JavaScript;
- for the five slowest interactions and the three longest frames, that
  breakdown within the interaction (from the input to the frame after it)
  or frame, its functions with the most self time, and its three heaviest
  JavaScript stacks, innermost first, at most eight frames, a package's
  consecutive frames as one (`[react-dom ×56]`: 56 more frames of
  react-dom, mostly React walking its tree of components). Idle time within an
  interaction or frame was spent off the main thread: raster, compositing
  or the driver, which the profile does not see.

To place interactions and frames on the profile, the runner lines up the
page's `performance.now()` with the profile's clock: after the snapshot it
runs a function, `__baumProfileClock`, that spins 30 ms and returns the
page times it started and ended. Its first sample is no earlier than the
start and its last no later than the end, so the clocks' offset is the mean
of the two differences, to within half a sampling interval. A run whose
spin got fewer than ten samples, or samples spanning less than 80% of it (a
sampler starved on shared CPUs), is left unaligned rather than
misattributed: the summary says so and leaves its interactions and frames
out. The offset is kept in
each profile as `baumClockOffset` (microseconds, profile time less page
time).

`run-N.cpuprofile` beside the summary holds each run's whole profile: open
it in Chrome DevTools (Performance, Load profile) or
[speedscope](https://www.speedscope.app) for the flame chart. The bundles
are minified, so their names there are the minified ones unless DevTools
can load the source maps.

Chromium's numbers are not WebKit's. V8 and JavaScriptCore compile and
collect garbage differently, Blink's style and layout differ from WebKit's,
and headless Chromium paints with its own software renderer, so a frame or
an interaction may be much faster or slower in one than the other, and a
problem only one engine has (a slow path in its layout or its regular
expressions) shows only there. Use Chromium's profile to find which code is
hot, and WebKit's runs to judge how slow the app is for its users. The rest
of the summary (metrics, interactions, components) is measured the same way
in both. The driver's own polling shows in the profile as anonymous
functions with no source, a little in every interaction.

`dmg` builds the frontend, the viewer and both darwin sidecars in Docker, then runs
`scripts/macos-local.sh` on the host: `bundle.sh` and `macos-finalize.sh` as the
release runs them, without a Developer ID, so the app is signed ad hoc and not
notarised. This is a host execution exception, because the bundle needs Apple's
`lipo`, `codesign` and `hdiutil`. The host needs Rust with the
`aarch64-apple-darwin` and `x86_64-apple-darwin` targets, the Xcode command line
tools and the Tauri CLI version the Dockerfile pins. With `--install` it
replaces `/Applications/baum.app`; quit the app first with Quit and stop
background reviews, so the next launch starts a daemon from the new binary.

The disk image opens as a small window with 128 px icons, `baum.app` beside
the `Applications` link with an arrow between them, and mounts with the app
icon (`icons/icon.icns`) as its volume icon. The window layout is
`macos/dmg.DS_Store` and the arrow is the background
`macos/dmg-background.tiff`, which `macos-finalize.sh` copies into the image;
regenerate both in Docker with `just desktop::dmg-layout` after changing
`scripts/dmg-layout.py`.

`run` starts the container's own daemon in the container's home directory,
not yours. Set `BAUM_DESKTOP_BAUM` to run a different `baum` binary than the
bundled one.

For UI work and host integration, use `just desktop::dev` on macOS, or on Linux
in a Wayland or X11 session. Vite, dependencies and generated frontend code stay in
Docker; the CLI sidecar is built in `oss-test` for the host's target (Go
cross-compiles it without cgo). The native Rust app is compiled
and run on the host, with separate output in `target/host-dev`. This is an
intentional host execution exception: a container cannot resolve the host user's
login environment, installed agent CLIs, repository paths, credential agents or
native desktop services faithfully by mounting a display socket. The launcher
uses your real home directory and the shared host daemon, so installation and
repository actions affect your actual machine. It respects `BAUM_DESKTOP_BAUM`
when set; otherwise it checks the sidecar first: `binaries/baum-<target>.stamp`
(written by `scripts/sidecar.sh`) holds a content hash of the Go module under
`oss/` (`scripts/sidecar-stamp.sh`: sources, `go:embed` files and the built
viewer and local app, not the frontend sources or the desktop crate). If the
binary or stamp is missing or the hash differs, it says why and rebuilds the
sidecar in `oss-test` before launching; an unchanged checkout skips the build.
It then atomically refreshes `target/host-dev/bin/baum` from the sidecar and
runs that copy. The check runs once at start: after a `git pull` while the app
runs, stop and rerun `just desktop::dev` to pick up daemon changes (the launcher
prints the commit its daemon was built from). Set `BAUM_DESKTOP_DEV_SIDECAR_ONLY=1`
to run only this check and stop before Vite and the app. This gives installation a stable target
named `baum`, as the install-record validation requires; the platform-suffixed
sidecar filename is only the build artifact. Existing daemons can finish using
the previous executable after the copy is replaced.

The host needs Rust (at least the version in `Cargo.toml`), a C toolchain,
pkg-config and the Tauri GTK/WebKit development libraries. On Debian/Ubuntu,
install `build-essential pkg-config libwebkit2gtk-4.1-dev libgtk-3-dev
libayatana-appindicator3-dev librsvg2-dev libssl-dev libxdo-dev`. On macOS,
Rust and the Xcode command line tools are enough. Docker continues to own checks
and release builds.

On Linux, Vite runs with host networking (`desktop-dev-frontend`) and shares the
host's loopback with the app and the daemon. Docker Desktop on macOS has no
such loopback, so `desktop-dev-frontend-bridge` publishes Vite on
`127.0.0.1:1420` and proxies to the daemon through `host.docker.internal`,
keeping the daemon's own `Host` header, which it requires. Windows is not
supported.

Saving React components or CSS in `oss/app` or `oss/viewer` refreshes the window,
including the setup screen. Native Rust and connecting screen changes require
restarting the command. Ctrl-C removes the Vite container and its private shared directory;
the host daemon continues running, just as when the normal desktop app closes.
A second Vite, such as another worktree's, can serve the same daemon beside
it with `BAUM_DESKTOP_DEV_PORT` set to another port; open it in a browser with
the launch URL from the daemon's `credential.json`.

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
            ├─ sign-macos    ┐ environment "release": sign what desktop built
            └─ sign-linux    ┘
                 └─ desktop-manifest     latest.json, desktop-checksums.txt; read-only
                      └─ desktop-publish attach files, latest.json last, publish the draft,
                                         then the release candidates' latest.json
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
  run if a signing variable is set. It produces the deb and AppImage and the
  unsigned macOS app (as a tar). The Windows installer is not released for
  now: the release calls `desktop.yml` with `windows: false`, so only Check's
  snapshots build it (see "Windows" below).
- The two sign jobs use the `release` environment, the only place the
  signing secrets live. Each waits for a reviewer's approval (both
  together), and the environment accepts only `v*` tags. They compile
  nothing: they run Apple's tools (`codesign`, `notarytool`, `stapler`,
  `hdiutil`) through `scripts/macos-finalize.sh`, and minisign 0.12 (the official binary,
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
  files (`latest.json` last), and only then publishes the draft. It then
  copies `latest.json` to the `desktop-rc-updates` pre-release, which it
  creates on the first release, unless that already holds a newer version (a
  re-run of an older release). That pre-release is not a release: it only
  holds the manifest release candidates read, and being a pre-release, it is
  never the latest release.

Before tagging a stable release, write the version's section (`## v1.2.3`) in
`CHANGELOG.md` in a PR and merge it. The tagging workflow and the release
workflow both read it from `main`, not from the tagged commit, and a stable tag
without notes fails before anything is pushed. A release candidate uses its
version's section if present and has no notes otherwise, so notes written
after a candidate still reach its promotion. The release workflow gives them to
goreleaser as the draft's notes.

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
before approving the `release` environment. The draft is published, with the
changelog notes, when the desktop files are attached. A tag with a pre-release suffix
(`v1.2.3-rc.1`) becomes a GitHub pre-release. Full releases of the app do not
update to it, since they read the latest full release; installed release
candidates do, through `desktop-rc-updates`.

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

A pull request or push to `main` that changes `desktop/`, `app/`,
`internal/localapp/`, the viewer build scripts or these workflows also runs
`desktop.yml` from `check.yml` with version 0.0.0, packaging the macOS app ad
hoc with `macos-finalize.sh`, so the bundling and packaging scripts are
exercised before a tag. Like the release, it skips the Windows installer.
Snapshots reuse a cached Linux build image and Rust build output; release
builds use neither. Those snapshot bundles are kept as workflow artifacts for
14 days.

### Files on a release

| File | What it is |
| --- | --- |
| `baum-desktop-darwin-universal.dmg` | macOS disk image, Apple silicon and Intel, macOS 12 or newer |
| `baum-desktop-darwin-universal.app.tar.gz` (`.sig`) | macOS app the updater installs |
| `baum-desktop-linux-amd64.AppImage` (`.sig`) | Linux x86_64 AppImage; also what the updater installs |
| `baum-desktop-linux-amd64.deb` | Debian and Ubuntu package (no self-update) |
| `baum-desktop-linux-arm64.AppImage` (`.sig`), `.deb` | Linux arm64, when that build succeeds |
| `desktop-checksums.txt` | SHA-256 of the desktop files |
| `latest.json` | the updater manifest |

The download links at `downloads.usebaum.com`, used by the website and the
repository README, take the version from the updater manifests and find these
files by name; renaming one means updating them too.

The desktop files also carry a GitHub build provenance attestation
(`gh attestation verify FILE --repo baum-ai/baum`).

A macOS bundle that is not signed with a Developer ID is named
`…-unsigned.dmg` (signed but not notarised: `…-unnotarized.dmg`); Gatekeeper
warns about those. `latest.json` lists `darwin-aarch64` and `darwin-x86_64`
(both the universal app), `linux-x86_64` and, when built, `linux-aarch64`,
each at
`https://downloads.usebaum.com/update/vVERSION/<its file>`.

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

The Linux bundles are built in this directory's Docker image, on Ubuntu
24.04 (the base image pinned by digest), the oldest supported distribution.
They require Ubuntu 24.04 or newer, or a compatible distribution with glibc
2.39 or newer and the corresponding system libraries. Ubuntu 22.04 and
Debian 12 are no longer supported. The deb uses the system's WebKitGTK with
the 4.1 API (`libwebkit2gtk-4.1-0`), 2.40 or newer: the first release that
sends the Fetch Metadata request headers (`Sec-Fetch-*`), without which no
custom tile's frame loads. It declares its package dependencies and
installs `baum-desktop` and `baum` in `/usr/bin`. Video in reviews plays
through the system's GStreamer: WebKitGTK depends on the base and good
plugins (WebM: VP8, VP9, Opus), and the deb also depends on
`gstreamer1.0-libav` for MP4 (H.264, AAC).

The AppImage carries its own WebKitGTK, GTK and Pango but uses the system's
glibc, HarfBuzz and the other libraries linuxdeploy leaves out. It also
carries the image's GStreamer with its base and good plugins
(`bundleMediaFramework` in `tauri.conf.json`) and uses those instead of the
system's, so WebM videos play without any GStreamer on the system. It
carries no H.264 or AAC decoder (libav), so MP4 videos show as unavailable
there. The image
requires WebKitGTK 2.50.5 or newer; Ubuntu 24.04 currently supplies 2.52.6.
This includes the [NetworkLoadClient lifetime fix](https://github.com/WebKit/WebKit/commit/b99f8c3daae0d6d0fdb99338c2e6f63cf4c6dc49)
for the resource-loader memory leak in 2.50.4. The AppImage updates itself in place,
so keep it in a directory you can write (for example `~/Applications`).

A downloaded AppImage is not executable. With an integration tool that
handles AppImages (AppImageLauncher 3.x, Gear Lever), double-clicking it
still opens it: the tool recognises the AppImage magic bytes (`AI\x02` at
offset 8), which `scripts/bundle.sh` checks are present, and makes it
executable. Without one, users run `chmod +x` first. AppImageLauncher 2.x
registers a kernel binfmt rule on those bytes whose interpreter cannot start
the static AppImage runtime ("Can't open squashfs image: Bad address"), so
the AppImage does not start there; 3.x fixed that. Other AppImages built on
this runtime, such as Cursor's, have the same limitation.

On NVIDIA GPUs under Wayland, WebKitGTK only uses its fast GPU renderer
from 2.54. The AppImage carries Ubuntu 24.04's WebKitGTK (2.52), so it renders
slowly there, as does the deb on systems whose WebKitGTK is older than 2.54.
The move to Ubuntu 24.04 fixes the loader leak but does not fix this limitation.
User-facing limitations like these are listed in
[KNOWN_ISSUES.md](../KNOWN_ISSUES.md).

The app adds itself to the applications menu (`src/menu_entry.rs`): every
start of an installed AppImage writes
`$XDG_DATA_HOME/applications/baum-appimage.desktop` (default
`~/.local/share`), naming the AppImage's current path, with `TryExec` so
menus drop it once the file is gone and the icon copied to `baum.png` in the
app's data directory. Hiding the entry with `NoDisplay=true` or
`Hidden=true` keeps it hidden, and the app never removes a hidden entry.
When another visible entry in that directory already starts the AppImage,
through its `TryExec` or the program in its `Exec` (as AppImageLauncher's
and Gear Lever's do), the app removes its own entry instead, so baum is
listed once. There is no Flatpak or Snap. x86_64 is
the supported architecture; arm64 is built on GitHub's arm64 runners and
published when that build succeeds. Linux bundles are not OS-signed; the
AppImage carries the updater signature.

### Windows

Not released for now. Check still builds the installer for every pull
request, so it keeps compiling, but the release skips it and has no Windows
sign job. Releasing it again means restoring that job and the Windows entries
in `desktop-manifest` and `desktop-publish`, passing `windows: true` (the
default) to `desktop.yml`, and requiring `windows-x86_64` in
`updater-manifest.sh` again; it was removed in one change, which reverting
restores. Its signing secrets are `WINDOWS_CERTIFICATE` (base64 `.pfx`) and
`WINDOWS_CERTIFICATE_PASSWORD`, or for Azure Artifact Signing
`AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`,
`AZURE_TRUSTED_SIGNING_ENDPOINT`, `AZURE_TRUSTED_SIGNING_ACCOUNT` and
`AZURE_TRUSTED_SIGNING_PROFILE`.

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
  `requireSignedVersion`, Windows passive install), that release candidates
  read the `desktop-rc-updates` manifest and full releases the configured one, that the key rejects
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
IPC and checks required GitHub sign-in and repository selection, separate agent
actions, retries, multiple repositories, back navigation,
completion and the minimum window width. Screenshots go to a temporary directory, or `ONBOARDING_SCREENSHOT_DIR` when set.
