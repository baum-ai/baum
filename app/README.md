# Local application

For desktop UI development, run `just desktop::dev` from the repository root
on Linux in a Wayland or X11 session. The native app and shared daemon run on the
host; Vite and frontend dependencies stay in Docker. Changes to the daemon-served
app and `../viewer` hot reload without rebuilding Go or Rust. See
[desktop development](../desktop/README.md#development).

The native onboarding screen is React too: `setup.html` mounts `src/desktop/setup.tsx`,
with typed native IPC and the same Vite, TypeScript, lint and test pipeline. The
multi-page production build emits both entries and assembles `../desktop/dist`
for Tauri, including the small connecting screen from `../desktop/ui`. Fonts and
icons are bundled as local assets rather than requiring a network. The setup
screen keeps its Tauri-local origin and permissions; serving it from the daemon
or Vite does not grant native access. Setup changes currently require restarting
`just desktop::dev` to rebuild the embedded assets.

`just local_app::e2e` checks the built app, development proxy and compiled native
setup bundle. `pnpm test:desktop` exercises setup with mocked IPC; it needs a
completed `pnpm build`. The proxy suite covers login, CSRF, event streaming and
React/CSS hot reload for the daemon-served application.

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

The inbox spans registered clones with Risk, Pull request, Review time,
Unresolved, Review, Checks and Merge columns. Review time is the latest saved
review's reading estimate in whole minutes; GitHub state and unresolved thread
counts also appear for pull requests without saved reviews. A dash means unknown
or no checks, while a known unresolved count of zero shows 0. Discovery fetches
open pull requests with a paginated GraphQL query per repository, including
review decisions, head checks, merge state and the first 100 review threads;
when more threads exist the unresolved count shows a lower bound (`N+`). The
columns follow the hosted Focus view's labels, icons and tones and fold under
the title below 1024px. Generating and failed reviews remain visible under the
title. Incomplete risk assessments show a lower bound (`N+/5`) with an explicit
“at least” description and caution styling rather than a reassuring low-risk
colour. Collapsible groups show Review requested, Your pull requests and Other
pull requests, with each row in one group; Saved history has its own group.
Collapsing keeps selections, and Select all selects only currently expanded
visible rows. Requested, authored, outdated, no-review, repository and
saved-history filters do not discard cached reviews. Unknown team membership
remains explicit. Cached discovery keeps the inbox readable when refresh fails;
a calm notice appears for a failed refresh or exhausted GitHub allowance,
never merely because the cache is old. Refresh asks
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
rebuilds the inbox and jobs. Jobs can be cancelled. Existing reviews stay readable
while regeneration runs. A PR page uses the queue’s active job for that
repository and PR across registered clones, showing queued/running progress
and cancellation even before its first document exists. Duplicate generation
controls stay disabled until that job finishes, and completion/cancellation
updates the page through SSE. Repository settings share backend and model overrides across registered clones.
GitHub discovery and the latest saved review's free evidence refresh periodically
while the local server runs, including PR heads/states, checks, conversation,
bot reports and local session links. Cached data loads immediately; offline,
rate-limited and failed refreshes are visible and retried with backoff.

The app's sidebar leads to the Inbox, Jobs, Repositories and Settings.
Settings holds the global review settings in the same form as the desktop
setup's last step: the harness, its model (a listed model or a custom name),
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
Repositories adds a clone from a path: `GET api/repos/check?path=` says
whether the daemon would register it (and as which GitHub repository, or why
not), and only then does the button read "Add repo". Until then, in the
desktop app, it reads "Browse…" and opens the native folder picker, the one
Tauri command daemon pages may call; in a plain browser it stays a disabled
"Add repo". The page lists each registered clone with its own automatic choice, daily
cap, and harness and model overrides (the same tiles and dropdown, with a
"Global setting" choice that inherits). Turning automatic generation off, for
everything or one repository, cancels the affected automatic
jobs that have not started (the jobs list shows the reason); a running job
finishes. Each repository also has a daily automatic cap in USD (0 permits no
automatic spending) or an explicit "No daily cap" checkbox; an empty,
negative or unreadable amount is an error and is not saved, never read as no
cap. It shows today's automatic spend, how many triggered pull requests wait
to start and the latest notice: a daily-cap pause until the next day or until
the cap is raised or removed (also a banner on every page), cursor-agent not
acknowledged, an unpriced model, or GitHub unavailable. The jobs list and the PR page mark an automatic job "Started
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
Diagnostics (`GET api/diagnostics`, the `baum internal doctor` report) open
in a dialog from Settings' "Check this computer" button, which shows how many
problems they found. `?page=diagnostics` opens Settings with the dialog open;
the desktop app opens it after its first-run setup (the bootstrap page carries
`page=diagnostics` from the launch URL's fragment). Each warning and problem
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
