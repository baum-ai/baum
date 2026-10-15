# Local application

`baum ui` serves this React single-page app from its embedded assets. It uses
only the authenticated local server: no hosted baum API, account or login.
The launch capability is exchanged for the server's HttpOnly cookie and removed
from the address before the inbox loads.

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
while regeneration runs. Repository settings share automatic opt-in, daily cap,
backend and model overrides across its registered clones. The background
scheduler is separate work; opting in here only records that preference.

Run workloads in the repository's `ui-test` Docker service (see root AGENTS.md):
`pnpm check`, `pnpm build`, `pnpm test:e2e`. `pnpm build` writes committed assets
to `../internal/localapp/dist`; the common `viewer-build` / `viewer-fresh` recipes
also build/check this bundle, so `go install` needs no Node.js.

Browser tests use an actual `uiserver.Server`, registry, version stores, queue,
`jobs.Local` and generation publisher. A temporary fixture git clone and fake
GitHub/model boundaries make the run offline and avoid personal session stores.
Before the browser suite build its Linux test helper in `oss-test`:
`CGO_ENABLED=0 go test -c ./internal/uiserver -o app/e2e/fixture`.
The suite verifies bootstrap, filtering, shared PR viewing, narrow layout,
estimation, generation/progress and repository settings. Unit tests cover
first-run/authorization, each review state, unknown costs, selection and filters.
