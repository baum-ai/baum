# Local application

The baum daemon serves this React single-page app from its embedded assets;
the desktop app and `baum ui` open it from the launch URL in the daemon's
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

The inbox spans registered clones, showing review status, risk, versions,
generation date, backend/model and recorded cost. It follows the Focus view’s
compact rows: risk at the left, a prominent title over repository/author/PR
metadata, and aligned review and generation figures. Below desktop width the
figures move under the title. Incomplete risk assessments show a lower bound
(`N+/5`) with an explicit “at least” description and caution styling rather
than a reassuring low-risk colour. Collapsible groups show Review requested, Your
pull requests and Other pull requests, with each row in one group; Saved history
has its own group. Collapsing keeps selections, and Select all selects only
currently expanded visible rows. Requested, authored, outdated,
no-review, repository and saved-history filters do not discard cached reviews.
Stale discovery and unknown team membership/cost remain explicit. Refresh asks
GitHub through the local server. Checkbox selection requests a batch estimate;
restricted fork/author entries are excluded from batches and require explicit
isolation confirmation when generated individually. Generation confirms an
approximate price and configured model; unknown prices cannot start a run.
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
rate-limited and failed refreshes are visible and retried with backoff. Model
calls only run after an explicit generation request and spend confirmation.
Legacy automatic/cap settings are retained for compatibility but never schedule
paid work and have no controls in this app.

Run workloads in the repository's `ui-test` Docker service (see root AGENTS.md):
`pnpm check`, `pnpm build`, `pnpm test:e2e`. `pnpm build` writes committed assets
to `../internal/localapp/dist`; the common `viewer-build` / `viewer-fresh` recipes
also build/check this bundle, so `go install` needs no Node.js.

Browser tests run the actual daemon (`daemon.Run`, with its lock, credential
file and control socket in `e2e/daemon-state`) serving a `uiserver.Server`,
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
generation/progress and repository settings. Unit tests cover
first-run/authorization, namespaced URLs and navigation, each review state,
unknown costs, selection and filters.

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
