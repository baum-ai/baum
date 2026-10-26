# Local application

For desktop UI development, run `just desktop::dev` from the repository root
on Linux in a Wayland or X11 session. The native app and shared daemon run on the
host; Vite and frontend dependencies stay in Docker. Changes to the daemon-served
app and `../viewer` hot reload without rebuilding Go or Rust. See
[desktop development](../desktop/README.md#development).

The desktop app's own screens are React too: `setup.html` mounts
`src/desktop/setup.tsx` and `connecting.html` mounts `src/desktop/connecting.tsx`,
with typed native IPC and the same Vite, TypeScript, lint and test pipeline. The
multi-page production build emits every entry and assembles `../desktop/dist`
for Tauri, where the connecting screen becomes `index.html`, the page the
window opens on; it is taken out of the daemon's copy. Every loading state uses
one component, `NineDots` or `LoadingStatus` in `src/nine-dots.tsx`, whose look
lives only in `src/nine-dots.css`. Every screen uses baum's bundled type from the
viewer (`../viewer/src/fonts/fonts.css`), Instrument Sans for text and Manrope for
headings, through its `--font-text` and `--font-heading` tokens. Both fonts are
under the SIL Open Font License, listed in the third-party notices. Fonts and
icons are bundled as local assets rather than requiring a network. The setup
screen keeps its Tauri-local origin and permissions; serving it from the daemon
or Vite does not grant native access. Setup changes currently require restarting
`just desktop::dev` to rebuild the embedded assets.

`just local_app::e2e` checks the built app, development proxy and compiled native
setup bundle. `pnpm test:desktop` exercises setup with mocked IPC; it needs a
completed `pnpm build`. The proxy suite covers login, CSRF, event streaming and
React/CSS hot reload for the daemon-served application.

Setup and Repositories list the signed-in GitHub account's repositories, with
search. Nothing is picked automatically: once the person picks a repository,
the daemon looks for its local clone and the row says where it was found or
offers to choose or clone one. A picked repository can have
no clone: its pull requests remain in the inbox and link by repository and PR
number. Its page says **Needs a local clone**, with **Choose folder** and
**Clone it**; generating waits for a clone. Clone it shows an editable default
folder and polls the daemon's background clone, including after reopening.
Repositories come from GitHub only: without sign-in there is nothing to pick.

Repository settings apply once per repository, including one with no clone.
**Change local clone** lists registrations and lets the person remove one or
choose another folder; files and saved review stores are left on disk. Keep the
clone baum should use registered. An explicitly chosen fork clone may be filed
under its matching upstream remote. A repository can be skipped without a clone.
The picker explains local discovery and, on macOS, offers **Also look in
Documents** after an explicit click. Later automatic attachments show a notice.
Setup uses the native `repositories` relay; app pages use namespaced local HTTP.
The relay permits only named repository actions and never exposes credentials.

The baum daemon serves this React single-page app from its embedded assets;
the desktop app opens it from the launch URL in the daemon's
`credential.json` (see "Local daemon" in ARCHITECTURE.md). It uses only the
authenticated local server: no hosted baum API, account or login.
The server's own bootstrap page at `/` removes the launch capability from the
address, exchanges it for an HttpOnly cookie scoped to a secret
`/s/<namespace>/` path, and replaces itself with that path. The app is served
only there, so it never sees the capability and only confirms the session with
`api/health`.

Every URL the app builds resolves under that namespace: `appBase()` in
`src/api.ts` takes the first two segments of `location.pathname`, and API
requests, the `api/events` stream, attachment images and in-app links are
built from it. The server rejects the cookie on unprefixed `/api/...` paths,
so an absolute `/api/...` URL fails with 401. Vite builds with `base: "./"`,
so the bundle's own assets load relative to the page. Navigation keeps the
path at the namespace root and changes only the query string (`?page=jobs`,
`?clone=…&pr=…`), so a reload or a copied link opens the same view and the
server needs no fallback route.

The PR page reuses the same `@baum/viewer` `ReviewViewer`, header, section/risk
navigation, version menu/comparison, Markdown/code renderers, session cards,
commit panes, tokens and system theme as the current hosted PR page. Its host
adapter supplies local document, history, attachment, patch and redacted session
sources through the same slots as the hosted PR route. Hosted discussion, approvals and GitHub mutations are absent.

The inbox spans picked repositories, with or without a local clone, with Risk,
Pull request, Review time, Review and Status columns. The pull requests of a
repository without a local clone are listed from discovery (its entries have
`needs_clone` and no clone); a review needs a local clone. Risk is the latest
saved review's score as a coloured pill (a lower bound reads `≥`). Review time
is that review's reading estimate in whole minutes. Review is GitHub's review
decision with a count of unresolved threads beside it. Status combines checks
and merge state into the one that stands first between the pull request and
merging: a conflict, failing checks, a draft, running checks, ready, blocked,
then passing checks; its tooltip names both. GitHub state and unresolved
thread counts also appear for pull requests without saved reviews. Normal or
unknown states (no score, no estimate, no GitHub review, no unresolved
threads, nothing reported) leave the cell empty and are read only by screen
readers. A review being generated shows the nine-dots loader under the title.
Discovery fetches
open pull requests with a paginated GraphQL query per repository, including
review decisions, head checks, merge state and the first 100 review threads;
when more threads exist the unresolved count shows a lower bound (`N+`). The
columns follow the hosted Focus view's labels, icons and tones and fold under
the title below 1024px. Generating and failed reviews remain visible under the
title, with agent sessions in progress beside them. Incomplete risk assessments show a lower bound (`≥N/5`) with an explicit
“at least” description and caution styling rather than a reassuring low-risk
colour. Collapsible groups say who acts next, with each row in one group:
Review requested; the person's own pull requests as Needs changes (changes
requested, failing checks or a conflict), Ready to merge (GitHub's merge state
is ready) and Your pull requests (the rest); Other pull requests; and Saved
history. The inbox opens on the Inbox view, which holds only what is the
person's to check now: pull requests asking for their review and their own.
Everyone else's open pull requests are under Others, so Other pull requests
appears only there; Requested, Mine, Not reviewed and Saved history are the
other views, and a view with nothing in it is dimmed.
Collapsing keeps selections, and Select all selects only currently expanded
visible rows. Requested, authored, outdated, no-review, repository and
saved-history filters do not discard cached reviews. Unknown team membership
remains explicit. Refresh events share the active read and queue one follow-up,
so a completed inbox snapshot is shown even while more updates arrive.
Cached discovery keeps the inbox readable when refresh fails;
one notice covers every repository GitHub could not refresh, says whether
GitHub is disconnected or needs attention, and links to Settings; another
covers an exhausted GitHub allowance. Neither appears merely because the cache
is old. Refresh asks
GitHub through the local server. Checkbox selection requests a batch estimate;
restricted fork/author entries are excluded from batches and require explicit
isolation confirmation when generated individually. Generation confirms an
approximate price and configured model; an unknown model price cannot start a
run. The exception is the `cursor-agent` backend, which Cursor bills through
the user's subscription: its item shows "cost unknown" with that reason in
words instead of a price, the total is unknown, and the run starts only after a
separate acknowledgement that baum cannot estimate it. The app sends it as
`confirm_unpriced`, and the server refuses such a run without it. Exclusion and
estimate reasons are shown in words, never as reason codes.
Progress and completion arrive over the authenticated SSE stream; reconnection
rebuilds the inbox and jobs. Jobs appear newest first and can be cancelled. Existing reviews stay readable
while regeneration runs. A PR page uses the queue’s active job for that
repository and PR across registered clones, showing queued/running progress
and cancellation even before its first document exists. Duplicate generation
controls stay disabled until that job finishes, and completion/cancellation
updates the page through SSE. Repository settings share backend and model overrides across registered clones.
GitHub discovery and the latest saved review's free evidence refresh periodically
while the local server runs, including PR heads/states, checks, conversation,
bot reports and local session links. Cached data loads immediately; offline,
rate-limited and failed refreshes are visible and retried with backoff.

Beside the harness choice, Settings and the desktop setup's review step show a
"Plan usage" meter per provider and window (for example 5-hour and weekly):
the share left, when it resets, coloured by what remains. It reads
`GET api/plan-usage` on mount (asking for a fresh reading) and every five
minutes while visible. Codex figures come from its last session and say how old
they are; a provider the daemon cannot read shows "Unavailable: <reason>" and a
failed request shows nothing. See the daemon's
[plan usage README](../internal/planusage/README.md).

The app's sidebar leads to the Inbox, Sessions, Repositories, Jobs and Settings; Sessions
shows the number of agent sessions working now.
In the desktop app, Settings has separate Command line and Agent hooks sections
with inline CLI install, update and removal controls, plus installation,
enablement and removal for each agent. Home paths are displayed with `~`.
It shows the installed state, paths, trust guidance and action results.
Settings holds the global review settings in the same form as the desktop
setup's review step: the harness, its model (a listed model or a custom name),
whether reviews run automatically and for which pull requests (authored,
requested or both). Saving sends them to `PUT api/automatic` (`enabled`,
`backend`, `model`, `pull_requests`), which the daemon keeps as app
preferences over the config file; `GET api/settings` reports the effective
harness and model. Choosing cursor-agent, globally or for a repository, is the
one-time acknowledgement that its cost cannot be estimated.
Automatic reviews are on by default. The global switch is a master switch (the
desktop tray's pause sets it): off stops automatic generation everywhere. Each
repository chooses "Global setting", "On" or "Off" (`automatic`: `null`,
`true`, `false`); only "Off" differs while the global switch is on, and while
it is off a repository set to "On" is shown as paused by the global switch.
Repositories lists picked GitHub repositories and their clones. Add repositories
opens the searchable account picker, the only way to add one: baum works with
GitHub repositories only. Explicit per-repository folder attachment uses any
matching remote, including an upstream remote in a fork clone. Settings apply
to the repository through `PUT api/repositories/{owner}/{name}/settings`, with
automatic generation and harness/model overrides. Saving clears an old daily
spending cap. Removing a repository unpicks it but leaves its files and saved
reviews on disk. Change local clone offers the existing clone registrations and
lets the person remove one or register another folder. Explicit unpicks are
remembered locally across picker reopens and app reloads; picking it again is
always available.

The jobs list and the PR page mark an automatic job "Started
automatically because new commits were pushed" (or "because your review was
requested", "because the pull request was marked ready for review", or
"Started automatically again" for the one retry of a run a daemon stop or
restart interrupted); it is cancelled like any other. Drafts get no automatic
review, but Generate works on them as usual. `automatic` SSE events (a pause,
its end, a notice, and every repository's status after a settings change in
any window) reload the page and are what the desktop app can notify on.
Agent sessions in progress (`GET api/live-sessions`, tracked by the daemon from
hook events, see "Live sessions" in ARCHITECTURE.md) show as a compact "Agent
running" or "Agent idle" marker on the inbox rows of their pull request and as
a "Sessions in progress" panel (agent, working or waiting, branch, start and
last activity) on its PR page, updated by the `live` event, which carries no
data: the app rereads the list. Only the agent, session id, state, times, last
event kind, repository, branch and PR number reach the app, never a path or
transcript. When a session ends the panel goes and the page reads its sessions
from disk again, where the full session is. A daemon without the record serves
none.

The Sessions page (`?page=sessions`) lists the same sessions: "Running now" (agent,
working or waiting for you, repository and branch, a link to the pull request's page
when the inbox has it, start and last activity) and "Ended recently" (sessions that
ended in the last 24 hours, newest first, with when they ended and how long they ran).
Times are relative, with the exact time on hover. The daemon keeps ended sessions for
24 hours, across restarts, and the `live` event updates the page and the sidebar count.

GitHub connection is available in Settings, the first-run
diagnostics dialog and the desktop setup's first step, which share
`src/github-auth.ts`. Sign in with GitHub starts device authorization through
the daemon and, in the same click, opens the fixed
`https://github.com/login/device` page in the browser and copies the code once
the daemon returns it (a clipboard write given a promise, since by then the
click no longer counts and the window has lost focus). The panel observes only safe local connection status;
Go polls GitHub even after the window closes. Denial, expiry, network failures,
missing scopes, revoked authorization and locked credential stores have retry
or reconnect controls. An explicit environment/config credential is shown
separately from the saved desktop account and continues to take precedence.
Disconnect removes only baum's saved credential, not the explicit override or
GitHub's authorization. Cached inbox entries and saved reviews remain readable
without signing in. Credentials never reach the UI.

Diagnostics (`GET api/diagnostics`, the `baum internal doctor` report) open
in a dialog from Settings' "Check this computer" button, which shows how many
problems they found. `?page=diagnostics` opens Settings with the dialog open;
the bootstrap page can carry `page=diagnostics` from the launch URL's
fragment. Desktop setup opens the inbox instead. Each warning and problem
shows its fix. The app runs the diagnostics once when it opens and, while they
find a problem, shows a notice on every page linking to them; the dialog
updates it when it runs the checks again. GitHub discovery shows a note only
when a refresh failed or the request allowance is used up; a cache that is
merely older than the poll interval is normal and says nothing.
The daemon keeps generating after the app closes, until "Quit and stop
background reviews" or `baum daemon stop`. Every other model call runs only
after an explicit generation request and spend confirmation.

Run workloads in the repository's `ui-test` Docker service (see root AGENTS.md):
`pnpm check`, `pnpm build`, `pnpm test:e2e`. `pnpm build` writes the assets the
daemon embeds to `../internal/localapp/dist`, which only a release commit (the
commit a `v*` tag points at) carries, so `go install` of a tagged version needs
no Node.js. The common `viewer-build` recipe also builds this bundle; a binary
built without it answers the application's page with a note saying so.

Browser tests run the actual daemon (`daemon.Run`, with its lock, credential
file and control socket in a temporary directory) serving a `uiserver.Server`,
registry, version stores, queue, `jobs.Local` and generation publisher. The
suite reads the launch URL from the daemon's `credential.json`, as the desktop
app does, and opens it a second time in a fresh browser context to check that
the credential survives relaunches. A temporary fixture git clone and fake
GitHub/model boundaries make the run offline and avoid personal session stores.
Before the browser suite build its Linux test helper in `oss-test`:
`CGO_ENABLED=0 go test -c ./internal/uiserver -o app/e2e/fixture`.
The suite verifies bootstrap into the namespace (every app request stays under
it; the cookie alone gets 401 on unprefixed `/api/...`), filtering, shared PR
viewing and reloading a PR deep link, narrow layout, estimation,
generation/progress and repository settings. The fixture also runs a live
session tracker over a real hook event record, fed from a marker file in the
state directory, and a test follows that session from the inbox and the PR page
through running, idle and ended. The fixture also runs the
automatic generation scheduler, fed a discovery snapshot every quarter second;
a second test writes `push-10` in the state directory to give PR 10 a new head
and follows the automatic review it starts on the PR page and in the jobs list,
then turns the global switch off and on. Unit tests cover
first-run/authorization, namespaced URLs and navigation, each review state,
unknown costs, selection and filters, automatic settings and pause notices,
and the automatic job labels.

Generation usage is retained as provider-reported input, output, cache-read and
cache-creation buckets, with available TTL, speed, prompt range and model-call
reports. Jobs save each update before publishing progress, including failed and
cancelled attempts. Provider-controlled model and speed strings appear only
after secret masking; progress retains numeric buckets while masking runs.
The charged-token budget counter excludes cache reads;
total processed tokens includes them. Missing older accounting remains unknown,
and interrupted providers show reported subtotals rather than exact totals.
Saved review usage follows the version selected in history. Unknown actual
costs show a reason; configured model identity is distinguished from a provider
report. Each attempt is priced independently when its model and pricing inputs
are known.

The confirmation amount is budget planning, not a PR-sized prediction. It shows
the configured charged-token and backend-call limits and assumes one
budget-sized cache read per call at the highest listed rates. Cached reads and
provider overshoot can exceed those assumptions, so this is not a dollar cap.

GitHub browser scenarios use the real auth manager/controller and mocked GitHub
HTTP and credential-store boundaries. The `github-auth-mode` marker selects
pending, alice/bob approval, denial, expiry, network failure, insufficient scopes
or locked persistence. Tests cover copy/open, background completion, reload,
account replacement, disconnect with saved-review access, recovery and the local
session/namespace/CSRF boundary. Waiting and narrow screenshots go to ignored
`test-results/` for PR attachments.
