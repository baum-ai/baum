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
no clone: its pull requests remain in Pull requests and link by repository and PR
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
sources through the same slots as the hosted PR route. Hosted discussion and approvals are absent. Merging, updating a branch, re-running checks,
submitting a review from the walkthrough, a fix the person starts (with its
replies on the review threads it addressed), and resolving a review thread the
person chose are the app's only GitHub writes (see below).

The Inbox's panel lists a fix's unresolved review threads under the row that
counts them (`ReviewThreads` in `src/threads.tsx`), asked for with `GET
api/prs/review-threads` and discovery's `unresolved` count when the panel shows
the pull request and again when that count changes. The daemon answers from
its cache while the count matches (see `prupdate.Cache`), so moving
through the Inbox does not read GitHub each time. Each shows its first comment's
headline (at most two lines), its author, its file and line linked to the thread on GitHub, whether
it is outdated and how many comments it has. A thread the signed-in account
may resolve (`can_resolve`) has a **Resolve** button that posts `api/prs/resolve-thread`
at once, without a dialog, reads **Resolving…** meanwhile, and removes the thread
from the list once GitHub confirms; a refusal is shown under the thread.

Under "Checks failing" the panel lists the head's failing checks
(`FailingChecks` in `src/checks.tsx`), asked for with `GET
api/prs/failing-checks` and the head commit it shows (`head_sha`), again when
that head changes. Each shows the check's name, its conclusion in words
(`conclusionText`), its title when it has one and a **Details** link (`url`).
A check with a `job_id` has **Retry** when discovery says the account can push
(`viewer_can_push`): it posts `api/prs/rerun-job` at once, reads **Retrying…**,
then shows "Re-running on GitHub"; a refusal is shown under the check. When the
pull request offers a fix, the row has **Fix CI**, which starts at once a fix
of the failing checks alone (`startFix(clone, pr, "checks")`, `"only":"checks"`
on the item).
The PR page's **Walk through** button (beside Regenerate, once a review is
saved) opens the walkthrough (`src/walkthrough.tsx`): `?…&walk=tour` or
`walk=all`, so a reload stays there, and **Full review** returns to the viewer.
`buildTour` (`src/walkthrough-model.ts`) turns the document into chapters, the
logical sections with hunks in `sectionsInReviewOrder` (mechanical and obsolete
sections are left out), and stops, one per behaviour step: its `title`,
`before`, `after` and `question` (schema revision 2026-10-27.1), falling back to
the step's `text` for a review written before them, and to the section summary
for a section without steps. A stop's code is the viewer's `HunkEvidence` for
the step's hunk anchors, reading omitted excerpts through the page's patch
source. **Guided tour** walks intro, stops and wrap-up; Next and → skip stops
already looked at. **Everything** lists every stop by chapter with filters,
details and a comment box per row, and the submit panel beside it. Leaving a
stop, opening its details or commenting marks it looked at; seen marks and
drafts are kept in `localStorage` (`baum.walkthrough`) per repository, pull
request and review version, the 40 most recent. Submitting posts
`POST api/prs/review` with `{repository, pr, head_sha, event, body, comments}`:
`head_sha` is the reviewed head, each comment `{path, line, side, start_line?,
body}` sits on the step's anchored lines (or the hunk's first added line), and
a stop without a place goes into the body under the overall comment. The
daemon sends it once and maps refusals to `{code, message}` (`forbidden` 403,
`not_found` 404, `own_pull_request` and `invalid_line` 422, `failed` 502); on
`invalid_line` the panel offers to resend with every comment in the body. A
sent review clears the drafts and links to it on GitHub. The tour's keys (→/j,
←/k, c, e; in Everything j/k, o, c, t, e) are listed in the shortcuts sheet.

A pull request with a merge conflict (`merge` is `conflict`), failing checks
(`checks` is `failing`) or, on the person's own pull request
(`authored_by_me`), unresolved review threads (`unresolved` above 0) offers a
fix, labelled by what it fixes: **Fix conflicts**, **Fix CI**, **Fix review
comments** or a combination such as **Fix conflicts, CI and review comments**
(`fixLabel` and `fixName` in `src/api.ts`). It is offered only when the pull
request is open, is not a fork,
discovery says the signed-in account can push to the repository
(`viewer_can_push`) and the repository has a local clone. The PR page puts the
button first beside the version menu, Pull requests puts a small **Fix** in
the row's actions, and the Inbox's card offers it as its action; `f` presses
it on each. Any of them starts the fix at once, with no dialog and no cost
estimate: `fix` in `src/app.tsx` posts `POST api/jobs` with
`{"items":[{"clone","pr","kind":"fix"}],"mode":"single"}` (`startFix`), and
the button reads **Starting…** (in Pull requests' row actions, **Fixing…**)
from the click (the app's `fixStarting` set) until the job arrives. The daemon checks GitHub again and runs it with the
fix agent Settings chooses, else the review's harness and model. A fix it refuses answers `409` with
`blocked_reason` (fork, no push access, unsupported harness, nothing to fix),
which `request` turns into the reason in words (`jobReasonText`); the app
shows "Couldn't start the fix: …" where it was asked for: under the PR page's
header, in the Inbox panel, or below Pull requests.
The fix is a job like a review: the Jobs list labels it with the fix's name
and, once done, links the pushed commit and keeps the agent's summary behind
**What the agent did**; the PR page shows its phase with **Cancel fix**, then
the pushed commit or the failure in words, and the button reads **Fixing…**
meanwhile. Both summarise the replies on review threads ("Replied and
resolved 3 of 4 threads", from `thread_outcomes`) and list each thread's
outcome with the agent's summary (`FixDetails`). `reviewJob` ignores fix jobs and `fixJob` finds them, so a fix
never shows as a review being generated. The daemon polls discovery after a
push, so the new head appears without a manual refresh.

A fix can be watched live. A fix job with `has_transcript` has a transcript
page, `?page=jobs&job=<id>` (`JobPage` and `JobTranscript` in
`src/job-transcript.tsx`), its panels in the inbox's light sand on its darker
sand: the job's phase while it runs, then its summary or failure, above the
agent's messages, collapsed reasoning, tool calls (a status mark, expandable to
their output) and errors.
The summary, messages and reasoning are Markdown, without raw HTML. Consecutive
tool calls form one run (`groupEntries`; reasoning does not break it) that shows
only its latest call, with the earlier ones folded behind "N earlier tool
calls" above it. A call shows its tool once (Claude's titles start with the
tool's name) and Codex's tools as Run, Edit, MCP and Search. `JobTranscript` reads
`GET api/jobs/{id}/transcript?after=N` from 0, again at once while `more`,
otherwise every second until `done`, and follows the end while the reader is
there (`useFollowScroll`). Entries are append-only; a later entry with a tool's
`id` replaces the earlier one in place. The Jobs list, the PR page's fix
notice and Up next link to it: **Watch** while the fix runs, **Transcript**
after. A failure's `detail` (git's own words for the `signing` reason, which the
daemon checks before the agent runs) shows under **What happened**.

Merging: a pull request whose Merge state is Ready (open, not a draft) shows a
Merge button in the PR page header and, on hover or keyboard focus, in its
Pull requests row's actions (always drawn on touch screens and below 72rem,
where the columns fold), and as the Inbox card's action. It opens a confirmation dialog
that reads `GET /api/prs/merge-options` for the repository's allowed methods
(merge commit, squash, rebase), preselects GitHub's default for the user and
offers only the allowed ones. "Merge pull request" posts `POST /api/prs/merge`
with the head commit the page showed, so GitHub refuses the merge if the branch
has moved. The daemon accepts only picked repositories and never retries. A
refusal (head moved, conflicts, required checks or branch protection, no
permission, method not allowed) comes back as `{code, message}` with a 409, 422
or 403 status and shows inline in the dialog, in words; on success the daemon
records the merge and re-reads the repository's pull requests, so the lists
drop it at once.

Keyboard shortcuts (`src/shortcuts.tsx`, one key table that both the handling and
the sheet read). Every page is reached from the keyboard. With the command key
(⌘ on a Mac, Ctrl elsewhere): `1` to `7` open the sidebar's pages in its order
(Inbox, Pull requests, Sessions, Analytics, Repositories, Jobs, Settings; digits
are read by their place on the keyboard, so layouts that shift them work),
`,` opens Settings, and `K` opens **Go to** (`src/go-to.tsx`). Back and forward
are `⌘[` and `⌘]` on a Mac and `Alt+←` and `Alt+→` elsewhere. These work in
text fields too, but not under a dialog other than the sheet or Go to, which
they close. Go to searches the pages, the pull requests (by title, repository,
number, author or branch; what waits in the Inbox first), the review groups and
the agent sessions, and, with a daemon that has inboxes, each inbox by its name
(opening its queue) with **New inbox** and **Manage inboxes**: every word typed must match, a label that starts with the
query ranks first, `↑` and `↓` move, `Enter` opens and `Esc` closes. The sidebar
draws each page's key while Alt is held and names it in the link's tooltip and
`aria-keyshortcuts`. Tab moves nothing anywhere, dialogs included: the keys act
on the selection, and a place that needs more gets its own keys. `Esc` gives up
any focus a click left on a control.

On Pull requests, `j`/`↓` and `k`/`↑` move a row focus through the
rows as drawn (group rows count; rows of collapsed buckets and collapsed groups
are skipped), `Enter` or `o` opens the focused pull request or group, `x` toggles
its selection, `Space` opens or closes its peek, `m` opens Merge and `f` starts the fix where the row offers them (Merge wins
where both could), `g` generates the pull request's review through the same path
as the PR page's Generate (so the same confirmations and the instant "Starting"
state), `r` refreshes from GitHub at once rather than after the polling backoff,
`h`/`←` and `l`/`→` move to the previous and next view (For you, Requested, …),
and `Esc` clears the focus. Sessions, Jobs and Repositories share the list keys
(`useRowFocus` and `listKeys`): `j`/`↓` and `k`/`↑` move, `Enter` or `o` opens
the row (a session's transcript, a job's pull request or group, a repository's
settings) and `Esc` clears the focus. On Sessions the three sections are one
list and `p` opens the focused session's pull request; on Jobs `t` opens the
transcript (Watch while a fix runs); on Repositories `a` opens or closes the
picker and `/` searches it (the search takes the focus when the picker opens,
and `Esc` in an empty search leaves it). Settings' section list is its
selection, as Up next's list is: `j`/`↓` and `k`/`↑` move it, `1` to `5` jump
to a section, and `Enter`, `l` or `→` go into the shown section, where `j` and
`k` step through its controls, `Space` or `Enter` changes the focused one and
`Esc`, `h` or `←` go back to the list (`Esc` in a text field or select gives
it up first, and `j` and `k` go on from it). On a pull request or group page `j`
and `k` step to the next and previous place in reading order, as the
navigator's arrows do (`ReviewViewer`'s `steps` ref), and `m`, `f` and `g`
merge, fix and generate or regenerate where the header offers them. On any
page, `?` (or the `?` button in Pull
requests' page bar) shows the sheet of every key, grouped by where it works
(anywhere, the Inbox, Pull requests, a pull request or group, Sessions, Jobs,
Repositories, a session or job transcript), with the current page's group
first; `?` or `Esc` closes it. Key hints drawn
on buttons (class `key-hint`) show only while Alt, Option on a Mac, is held:
`useKeyHints` marks the document root then and clears it on release or when
the window loses focus. The focused row has an
accent bar and a faint fill, distinct from hover and selection; focus moves to
the row's own link, so screen readers follow, and the row scrolls into view.
Keys are ignored with Ctrl, Meta or Alt held, in text fields, selects, text areas
and editable content (checkboxes and radios still pass them) and while any dialog
is open. On a pull request or group page, `u`, or `Esc` with nothing focused,
returns to the page it was opened from (the Inbox, or Pull requests with the
filter kept); on a session's or job's transcript the same keys return to
Sessions or Jobs. Settings and Analytics have no keys of their own beyond Tab:
their controls are native fields and buttons (Analytics' window, chart and
breakdown toggles are pressed buttons in a group), and its charts' bars take
the focus for their tooltips.

Update branch and Re-run checks (`src/unblock.tsx`) are secondary buttons in the
PR page header beside Fix and Merge…, not in Pull requests: a row's actions
hold one action (Merge on a ready row, else Fix) beside Peek, and a third
button would crowd the column or wrap. `canUpdateBranch` offers the first
when discovery's `behind` is true (GitHub's `BEHIND`, never for a draft or a
conflict), the pull request is open, not a fork, and `viewer_can_push` is true;
`canRerunChecks` offers the second when `checks` is `failing`, the pull request
is open and `viewer_can_push` is true. Each opens a small confirmation
(`UpdateBranchDialog`, `RerunChecksDialog`) that reads
`GET /api/prs/update-branch-options` or `GET /api/prs/rerun-checks-options`:
the first names the two branches, the second lists the failed GitHub Actions
workflow runs it will re-run and, under "Not re-runnable from baum", the
failing checks of other apps; with no failed run it has nothing to confirm.
Both dialogs put the focus on the confirm button once GitHub allows the write,
so `Enter` confirms and `Esc` cancels without Tab. Confirming posts
`POST /api/prs/update-branch` or `POST /api/prs/rerun-checks`
with the head commit the page showed (labels turn to "Updating…" and
"Re-running…" in the same frame), so a moved branch is refused in words
(`{code, message}`, shown inline). A successful update stays open until Closed
because GitHub merges asynchronously; a re-run where GitHub refused only some
runs stays open and names them. Either success makes the daemon re-read the
repository's pull requests.

The Inbox (the app's root, `src/up-next.tsx`) is a split view: a list of
what waits on the person, grouped by what they do about it, beside the panel
of the selected pull request. The page follows the Up next design's own
colours and sizes rather than the app's tokens and type scale (`--un-*` in
`src/style.css`, mapped onto the app's dark tokens in dark mode); its list and
panel scroll on their own, and below 760px the list stacks above the panel.
`upNext` places each open pull request in at most one kind, from discovery's
flags only: **Review** (their review is requested), **Clone** (a requested
review whose repository has no local clone), **Merge** (theirs, and
`canMerge`), **Fix** (theirs, with changes requested, unresolved threads, a
conflict or failing checks) and **Update** (theirs, and `canUpdateBranch`).
The queue runs in that order, which is the order of the list's groups:
**Needs your eyes** (Review, Clone), **Ready to merge** (Merge) and **Hand to
an agent** (Fix, Update, then the person's pull requests a fix agent is working
on, each with the loader and the fix's phase). Within Review a saved review
ready to read comes before one being written, then the riskier first;
otherwise the lower number first. Each pull request appears once, whatever
clones hold it. Their own pull requests with nothing to do (drafts, and those
waiting on reviewers or checks) are counted at the foot of the list and linked
to Pull requests; everyone else's are only there.

The list's header counts what is the person's, sums baum's reading estimates
into minutes (only reviews have one), counts the fixes with agents and what was
done today, and draws a bar of cleared against cleared plus remaining. A row
shows the title, then the kind, repository and reading estimate (or the
agent's phase, or "last fix failed"), and a red dot at risk 5. With two or more
Merge items, Ready to merge offers **Merge all N**: one confirmation that reads
each pull request's merge options, merges each with GitHub's default method for
the person at the head commit shown, one after another, and names any GitHub
refused with the reason; leaving the page stops the merges not yet sent. Below
the groups, **Snoozed** and **Done today** start folded.

The panel names the kind, the repository and number, who asked (or "Yours"),
baum's reading estimate and the title (linking to the PR page), then why it
waits on the person in a sentence built from its state (none for a Merge, whose
group says it). Its default action is always **Open** (the PR page); beside it
is what the kind asks for: Merge (`MergeDialog`), Update the branch
(`UpdateBranchDialog`), the fix's own label (which starts the fix at once, with
`f` as its key), Watch the agent for
a running fix, else Open on GitHub; then **Snooze**. A scored pull request has
a risk card: the score as five blocks, its meaning and, from the saved review,
its first reason. Below it the panel lists, by kind: for a Review or Merge,
"What changed" (the review's summary and its first six sections in review
order, with their scores and lines added and removed); for a Fix, what needs
fixing; for an Update, where it stands; for a Clone, what baum will do. The
saved review is read when its pull request is selected, and again only for a
new version. A pull request back in the queue as a Fix whose latest fix job
failed says "The last fix failed: <reason>", with **See what the agent did**
(the transcript, else the job in the list). Agent sessions working on its
branch are counted at the foot of the panel.

Snooze offers 1 hour, this afternoon (3 PM, while that is more than an hour
away), tomorrow morning (9 AM) and **Until it changes**, which hides the pull
request until its head moves. A snooze selects the next pull request and shows
a toast with **Undo**; **Bring back** under Snoozed ends one early. Keys:
`j`/`↓` and `k`/`↑` move the selection, `Enter` opens, `m` merges a Merge, `f`
starts a Fix item's fix (where its panel offers the button), `s` opens Snooze
and `1`–`4` choose from it, `u` undoes a snooze.

"Done today" lists the pull requests that left the queue since first seen
today because the person's part is done: it merged or closed, the review
request went ("Reviewed", or "Handled" for one that needed a clone), their fix
pushed (a fix counts only once its job completed with a commit: never while it
runs, nor after it failed or was cancelled) and left it waiting on checks or
reviewers, or their branch update did. Their own pull request that only stopped
being mergeable (a push, checks running) waits uncleared, and nothing is
cleared for a repository the latest read lists no open pull request of, since
GitHub may only be unreachable; one that comes back is no longer cleared.
Snoozes and today's record are kept in this browser's `localStorage`
(`baum.inbox.v1`) and start again each day, snoozes excepted; nothing reaches
the daemon. The sidebar's Inbox badge counts the queue without snoozed pull
requests. It is not the desktop tray's waiting count, which counts only review
requests and the person's blocked pull requests.

**Inboxes** are saved filters over this same queue, defined in the daemon (see
its [inboxes README](../internal/inboxes/README.md)) and matched here
(`src/inboxes.ts`): the app never builds a second queue. An inbox holds the
queue items that fit all of its parts: what (any; reviews, which are Review and
Clone items; things ready to merge; things an agent can fix, which are Fix and
Update items and the pull requests a fix agent is working on now; or the
person's own), owners (the repository's owner, ignoring case; none means any),
risk (3 or more, or 4 or more, from the baum review's score; a pull request with
no score yet never matches a risk filter, and "at least N" counts as N),
repositories (none means any) and whether bots are left out. A pull request can
be in several inboxes. An inbox's queue is `?inbox=<id>` on the app root (an
unknown id shows the Inbox): the same page, with only the matching items, the
same groups, order, panel, keys, snoozes and **Merge all N** (which counts only
that inbox's Merge items). Its header shows the inbox's name beside its colour
dot and a sentence saying what it holds ("Anything waiting on me in any
repository, whatever the risk."), and an empty one says "Nothing here right
now." Snoozed lists only the inbox's pull requests. **Done today**, the header's
done count and progress bar, and the group of the person's own pull requests
that wait on checks or reviews are only in the Inbox (all). In the Inbox a row
carries a small tag with the colour and name of the first inbox it is in, and
its panel a tag for each; in an inbox the panel tags the other inboxes it is
also in. Colours are six palette names (`--inbox-*` in `src/style.css`, with
dark variants).

In the sidebar a chevron beside Inbox folds the inboxes nested under it
(remembered in `localStorage`; hidden with the glyph-only rail). The pages and
inboxes scroll within the sidebar, so with many inboxes plan usage, Jobs and
Settings stay in view; a row's menu is placed against the window, under its row
or above it near the bottom, and closes when the sidebar scrolls. Each row links
to `?inbox=<id>`, is the current page when selected (then Inbox is not), and
shows a dot, the name and how many unsnoozed items match, none when 0. The `⋯`
button opens a short list of buttons, focused on the first, to leave the inbox
out of the Inbox badge (its count is then muted, and says so to screen readers)
or count it, and **Delete inbox**. Esc, a click elsewhere or choosing closes it
and returns focus to the `⋯` button (after a delete, to the neighbouring inbox,
or Inbox); focus never drops to the page. The menu's first button is **Edit inbox**,
which opens the inbox's edit page. Delete toasts "Deleted <name>. Its
pull requests stay in Inbox." with **Undo**, which puts back only that inbox,
with its id so links to it still work, into the list as it is then, at its old
place (or the end), so a change made while the toast was up is kept; an inbox
with that id already there is left alone. The chevron is labelled "Inboxes",
with `aria-expanded` and `aria-controls` for the nested list. The Inbox badge counts the unsnoozed
queue items that are in no inbox or in at least one that counts in the badge;
with no inboxes it is the count above. Changes show at once and are sent to the
daemon one at a time, each carrying the newest revision, so an older change
cannot overwrite a newer one; a conflict adopts the daemon's list and any other
failure restores the last saved list with an error. An older daemon (the read
answers 404, or `available: false`) has none of this: no chevron, rows, tags or
changed badge. Any other failed read (a 500 `inboxes_unreadable`, a dropped
connection) is not an older daemon: it keeps the list shown (on the very first
read, nothing inbox-related) and says "Couldn't read your inboxes" in an alert
until a read succeeds, and later saves still go out. Entries the daemon sends
are read defensively (missing lists are empty, unknown values the plain ones,
entries without an id and name dropped).

Inboxes are made and changed on three pages that sit under Inbox in the sidebar
but are not sidebar pages of their own, so they have no number key and ⌘1 still
opens Inbox (`src/inbox-pages.tsx`; routed in `src/app.tsx` by `?page=`). The
nested list always ends with **New inbox** and **Manage inboxes** (shown even
with no inboxes), and an inbox's queue header carries an **Edit** link. With an
older daemon none of these links, pages or Go to entries exist, and a direct
address to them shows Inbox, but only for an older daemon (the read answers 404
or `available: false`): before the first read has answered the page shows
"Loading inboxes…" rather than Inbox, and after a first read that failed it
shows the read error with Retry. On New inbox and Manage inboxes the sidebar's
current page is that entry alone, and Inbox is only the section (as when an
inbox's row is selected); the edit page keeps the inbox's own row current.
Every control is a real button, link or input with
a label, with no key of its own, as Tab moves nothing in the app.

- **New inbox** (`?page=inbox-new`): "What should this inbox catch?" and six
  templates, each a button with a colour dot, name, sentence and how many
  unsnoozed pull requests it would show now. Urgent (red) is any repository at
  risk 4 or 5; Work (blue) is every owner of the person's repositories except
  their login (empty when there is none); Personal (green) is the login's own
  repositories with the badge off; Needs my review (purple), Quick merges
  (amber) and For an agent (grey) pick that kind. A name another inbox has
  becomes "Urgent 2", "Urgent 3"… Picking one appends it to the list with an id
  the app makes in the daemon's shape (so its page opens at once), and opens its
  edit page with "Created and added to the sidebar. Adjust anything below.", its
  Name input focused. At
  20 inboxes the templates are disabled, with a line saying so.
- **Edit** (`?page=inbox-edit&inbox=<id>`): "← <name>" back to its queue, a
  "Saved" mark after each save the daemon accepted, and "Changes save as you
  make them". The name is an input saved on blur or Enter; a blank or
  too-long name, or one another inbox has (ignoring case), is refused in place
  and not sent. Six colour swatches have radio semantics. "What it catches" is
  the sentence with its three parts (what, where, risk) as buttons; one opens its
  options as buttons, each with how many pull requests it would show, and
  picking one saves. Where offers any repository, the person's own, each other
  owner of their repositories and, when the inbox holds a set the list lacks,
  that set. Fine-tune holds repository chips (none picked means all that match
  above) and "Leave out: Bots" (`no_bots`). A switch counts the inbox in the
  Inbox badge. **Delete inbox** is the sidebar's delete with Undo, then goes to
  Inbox with focus on the sidebar's Inbox link. The right-hand preview is "Would show right now" with the same
  unsnoozed queue items and `matches` as the queue, grouped like it, each row
  with its repository and tags of the other inboxes it is in. A change that
  alters the matches says "N added", "N removed" or "N added · N removed" (a
  zero half is left out) for four seconds, in a live region that is always on
  the page and empty otherwise, the removed rows struck through and fading out
  over the last moments (no fade with reduced motion). An unknown id says "This inbox no longer
  exists" and links to Inbox.
- **Manage** (`?page=inboxes`): the inboxes in the sidebar's order, each with
  its colour dot, name, how many are in it now, sentence, up and down buttons
  ("Move <name> up", disabled at the ends), a labelled badge switch and Edit; a
  save sends the whole list in its new order. A tinted note says how many queue items
  are in no inbox (they show only under Inbox and always count in its badge; in
  the caution tone), or that every one is in some (in the positive tone). With none, it leads to New inbox.

Pull requests (`?page=pulls`, `src/inbox.tsx`) lists every open pull request
of the picked repositories, with or without a local clone, with Risk,
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
the title below 72rem. The list is drawn as the Up next design's live table,
in the Inbox's palette (`--un-*`) on the app's type scale. The page bar says
when the list last read the daemon ("Updated just now", then how long ago,
with the exact time as a tooltip), with a turning mark while a refresh runs.
A row's last cell holds its action and **Peek**, which wait for the pointer or
keyboard focus in the wide table. Peek (or `Space` on the focused row) opens
the saved review under the row, read when first opened and again only for a
new version: the summary, the first reason for its risk (in the critical tone
at 4 and 5), the first four sections in review order with their lines added
and removed, and **Open the review**. Without a saved review it says why (no
clone, being written, failed, none yet) and offers **Generate the review**
where it can run. A row whose state (review, checks, merge state, threads,
score, estimate or version) changed in the latest read flashes once. The keys
that work on the list are listed at its foot, except on touch screens. Generating and failed reviews remain visible under the
title, with agent sessions in progress beside them. Incomplete risk assessments show a lower bound (`≥N/5`) with an explicit
“at least” description and caution styling rather than a reassuring low-risk
colour. Collapsible groups say who acts next, with each row in one group:
Review requested; the person's own pull requests as Needs changes (changes
requested, failing checks or a conflict), Ready to merge (GitHub's merge state
is ready) and Your pull requests (the rest); Other pull requests; and Saved
history. The list opens on the For you view (`filter=inbox`), which holds only
what is the person's to check now: pull requests asking for their review and their own.
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
GitHub is disconnected or needs attention, and links to Settings' Connections; another
covers an exhausted GitHub allowance. Neither appears merely because the cache
is old. Refresh asks
GitHub through the local server. While anything is selected a dark bar over
the list counts it and offers **Merge N** for the selected pull requests that
can merge (one opens its Merge confirmation; more open the Inbox's Merge all
confirmation), **Generate reviews (N)**, **Review together (N)** for two or
more, and **Clear**. Generate, for one pull request or a checkbox
selection, starts at once (`POST api/jobs` with the items): no dialog, no
price, whatever the model, `cursor-agent` and models baum has no price for
included. The one confirmation left is for a fork or an author without write
access (`isolation_required`, then `confirm_isolation`); a batch with such an
entry asks the same. Refusals are shown in words, never as reason codes.
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

Review groups (`internal/reviewgroups` owns the daemon side). Selecting two or
more rows shows "Review together (N)" in the selection bar: it asks for a name (prefilled with the
first pull request's title) and creates a group of 2 to 8 pull requests of one
repository, then opens `?group=<id>`; a selection across repositories or of
more than eight is refused in the app without a request. In Pull requests a group
with at least one shown member is one row, placed in the bucket of its
highest-priority shown member (Review requested, then Needs changes, Ready to merge and Your pull
requests, then Other pull requests; Saved history only when every shown member is
historical). Its shown members leave the loose rows and nest under the group
row, which is collapsed by default; members the current filter hides are not
shown, bucket counts count the group once, and Select all covers loose rows and
the members of expanded groups. The group row shows the group's risk, its name,
"Review group · N pull requests · repository" and, when it applies,
"Generating group review", "Group review outdated" or "No group review yet".
The group page (`?group=<id>`) is the shared viewer with the group's name as
heading, member labels on sections, and above the review the members (state and
their own review status, each linking to its page) with Rename, Add pull
request, Remove (never below two members, nor above eight) and Delete group.
Deleting never deletes saved reviews. Generating needs an available clone of
the repository and a saved review of every member: members without one are
named, and "Generate their reviews" queues them. A fork or an author without
write access asks for the same confirmation as a pull request. An outdated
group offers Regenerate. A pull request's page links the groups it belongs to,
and the Jobs page labels a group job "Review group: <name>". Groups across
repositories are not supported yet.

The sidebar keeps plan usage in view above Jobs and Settings: per provider,
each window (for example 5-hour and weekly) with the share left, when it
resets and a thin bar coloured by what remains, and how old the oldest figure
is; hovering "resets in" shows the exact date and time. A narrow window folds
it to the lowest share left. Only
plans the daemon could read show: a provider that is not installed, signed
out or has not run yet is left out, and with neither readable (or a failed
request) the box is not drawn. The desktop setup's review step shows the same
meters, with "Unavailable: <reason>" for a provider it cannot read. Both ask
`GET api/plan-usage?refresh=1` on mount and every five minutes (the daemon
reads again at most once a minute), and work out "resets in" again every 30
seconds between readings. Codex figures are live when Codex is signed in with
ChatGPT; otherwise they come from its last session and, once older than 15
minutes, say how old they are.
See the daemon's [plan usage README](../internal/planusage/README.md).

The app's sidebar leads to the Inbox, Pull requests, Sessions, Analytics, Repositories, Jobs
and Settings; the Inbox shows how many things wait on the person there (its inboxes are nested beneath it), and Sessions shows the number of agent sessions waiting on the person (in the caution tone), or
else the number working now.

The Analytics page (`?page=analytics&days=7|30|90&repo=&agent=`, `src/analytics.tsx`)
reads `GET api/analytics` (see the daemon's [analytics README](../internal/analytics/README.md)).
Its bar holds the window (7, 30 or 90 days, as a segmented control) and the
repository and agent selects in the inbox's select style; all three live in the
address. A new choice shows at once and the last report stays, faded, until the
new one answers. Cards: summary tiles (sessions, active agent time, human prompts,
the cost at API prices, marked "Incomplete" when part is unpriced, and the
agent-written share of commits); agent activity per day, sessions or active time
stacked by agent, with a legend and a table behind "Show as a table"; a breakdown
by agent, model or repository (sessions, prompts, tool calls, failed share, active
time, tokens, cost at API prices); agent-written commits by evidence; baum's
work (reviews, fixes pushed, threads resolved, cost at API prices, failures by
reason, merges,
branch updates and re-runs from the app); delivery from GitHub ("Loading from
GitHub…" for that card only while the daemon searches); plan usage over time per
provider, with how often each window hit its limit; busiest hours; and the longest
sessions. A figure that is unknown is "—" with its reason as a tooltip and to
screen readers, never 0; a floor reads "≥". Charts are HTML and inline SVG in the
viewer's `--data-*` slots, agents in a fixed order (Claude Code, Codex, Cursor),
with tooltips on hover and focus. The page reads again every two seconds while
the daemon is still indexing or searching GitHub, then every five minutes. An
older daemon without the capability gets a note to update baum.
Settings (`?page=settings&section=…`, `SettingsPage` in `src/settings.tsx`)
shows one section at a time, listed on the left with a line on where each
stands and a dot when it needs attention; at narrow widths the list becomes
a scrolling strip above the section. The section is in the address, so a
reload or Back keeps it, and the GitHub notice's **Open settings** goes
straight to Connections. **Overview**, the default, says how many things need
the person ("Everything is working" otherwise) and in one sentence how baum
reviews, then lists GitHub, Reviews, Fixes, the agent hooks (desktop app) and
this computer, each with a dot and a button to the section that deals with it
(**Check** on this computer opens the diagnostics dialog). Fixes needs
attention when it follows reviews whose harness cannot fix. **Reviews** holds
the global review settings, one per row: whether reviews run automatically,
for which pull requests (authored, requested, both, or all open pull requests
in the repository), the harness and its model (a listed model or a custom
name, saved when its field is left or Enter is pressed). Each change is saved
at once, with "Saved" beside the title, through `PUT api/automatic`
(`enabled`, `backend`, `model`, `pull_requests`), which the daemon keeps as
app preferences over the config file; `GET api/settings` reports the
effective harness and model. Each section sends all its choices and runs one
save at a time; changes made while one runs are sent together afterwards, so
an older choice never lands after a newer one. A failed save shows the error
and puts the control back. **Fixes** chooses the agent that fixes: "Same as reviews" (the
default, which follows a repository's own review harness too) or Claude Code
or Codex with a model, sent as `fix_backend` and `fix_model` (`""` returns to
the reviews') and reported by `GET api/settings`. A chosen fix agent applies
in every repository. **Connections** holds the GitHub connection and, in the
desktop app, the Agent hooks card with installation, enablement and removal
for each agent. **This computer** holds the diagnostics, the background
daemon note, in the desktop app the Command line card (inline CLI install,
update and removal) and Zoom, and the environment for agents. Home paths are
displayed with `~`. **Environment for agents** (`AgentEnvSection`) edits
variables set for every agent baum runs and for the git commands that commit
and push a fix: NAME/value rows (names `^[A-Za-z_][A-Za-z0-9_]*$`, no duplicates, at most
32) saved with `PUT api/settings/agent-env` (`{"agent_env": {...}}`; an empty
object clears the app's value). `GET api/settings` returns `agent_env` and
`agent_env_source` (`app`, or `file` while the config file's `[agent_env]`
applies, which the section notes). `~` and `$VARS` are expanded; the values
are stored on this computer, so no secrets belong there.
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
automatic generation and harness/model overrides; there is no spending cap.
Removing a repository unpicks it but leaves its files and saved
reviews on disk. Change local clone offers the existing clone registrations and
lets the person remove one or register another folder. Explicit unpicks are
remembered locally across picker reopens and app reloads; picking it again is
always available.

The jobs list and the PR page mark an automatic job "Started
automatically because new commits were pushed" (or "because your review was
requested", "because the pull request was marked ready for review", or
"Started automatically again" for the one retry of a run a daemon stop or
restart interrupted); it is cancelled like any other. Drafts get no automatic
review, but Generate works on them as usual. `automatic` SSE events (a notice,
and every repository's status after a settings change in any window) reload
the page.
Agent sessions in progress (`GET api/live-sessions`, tracked by the daemon from
hook events, see "Live sessions" in ARCHITECTURE.md) show as a compact "Agent
running" or "Agent idle" marker on the Pull requests rows of their pull request and as
a "Sessions in progress" panel (agent, working or waiting, title, branch, last
activity and Open transcript) on its PR page, updated by the `live` event, which carries no
data: the app rereads the list. Only the agent, session id, state, times, last
event kind, why and since when it waits on the person with the one masked line
its agent showed, repository, branch, PR number, the session's title and its
activity (latest tool calls with masked targets, files written, latest assistant
message; see "Live sessions" in ARCHITECTURE.md) reach the app in the list,
never a path; a session's transcript comes only when the Sessions page opens it
(below). When a session ends the panel goes and the page reads its sessions
from disk again, where the full session is. A daemon without the record serves
none.

The Sessions page (`?page=sessions`) is a live feed of the same sessions, in three
sections of cards; the `live` event updates it and the sidebar count, and times
tick every second. The daemon keeps ended sessions for 24 hours, across restarts.

- "Needs you" (`SessionsPage` in `src/sessions.tsx`) shows only while something waits
  on the person: the requests the app can answer first, then the longest wait. A session with a pending `permission`
  request (from `api/agent-requests`) gets an amber card titled by its tool ("wants to
  run a command", "wants to edit a file", "wants to use X"), the session's title and
  branch, the agent's last message, what the request would do (`code` rows in a dark
  mono block) and the buttons Allow once, one Always allow per suggestion, and Deny. A
  `question` request shows its options as buttons (toggles and Submit for
  multi-select or several questions). A button disables the card and shows "Sending…"
  at once; the card says "Sent" until the next `live` refresh drops the request, and a
  409 `request_gone` shows "Already answered in the terminal". A green notice says
  what was done, and when nobody waits it reads "Nobody is waiting on you." with the
  last action. A session waiting with no request (it finished its turn, or asks for
  something only its terminal can answer) gets a quieter card with the reason, the
  wait, the agent's last message, the line it showed and Open transcript, plus
  "Answer in the terminal" for a permission or an answer. It has no reply box: the app
  sends messages only to running sessions. A daemon without `api/agent-requests`
  shows no requests.
- "Running now" has one card per running session, or one quiet for a while (a muted
  dot and "Quiet for 12 min"): the agent, title, branch, pull request link, and the time
  since it started. From the session's `activity` it shows the current step (the tool
  in its colour and its target, with a blinking caret while running, still under
  reduced motion), a "Trail" of chips for the earlier steps and "N files touched";
  without `activity` it reads "Active 2 min ago". "Send when it finishes" posts a
  message to `api/agent-messages`; the daemon delivers it when the session's turn
  ends, one per session, and the card then shows "Queued for when it finishes" with
  Cancel.
- "Ended recently" has one compact row per session that ended in the last 24 hours,
  newest first: the agent, title, how long it ran and the files it touched, when it
  ended, and Open transcript.

The Settings page has "Answer Codex approvals from the app" (`codex_approvals`, saved
through `PUT api/agent-requests/settings`, hidden when the daemon lacks the route):
while on, Codex hides its own approval prompt for up to 25 seconds while it waits for an
answer from the app.

A session in Needs you is not listed again under Running now; a session idle only
because it went silent, or with a reason this app does not know, stays there. Times
are relative where they are not clocks, with the exact time on hover. While anything
needs the person the sidebar's Sessions badge counts those sessions in the caution
tone; otherwise it counts the running ones. The desktop app notifies when a session
starts asking for a permission or an answer (see the desktop README).

Each row's "Open transcript" opens `?page=sessions&session=<agent>:<id>`
(`src/session-transcript.tsx`), titled with the session's title (else "Claude
Code session"), with the agent's compaction summary, when it has one, closed above
the transcript: the session's header (agent, working, waiting
or ended, repository, branch, pull request, start and last activity) above its
transcript, drawn with the viewer's `SessionCard` as a review's linked session
is on the PR page, with the same "Local transcript; secrets and e-mail
addresses masked." note. It reads
`GET api/live-sessions/{agent}/{id}/transcript` (see "Live sessions" in
ARCHITECTURE.md) when it opens, 500 ms after the last of a burst of `live`
events, and every 3 seconds while the session works (15 while it waits for
you, never once it ended), since an agent writes its replies between hook
events. Each read names the version shown, so an unchanged transcript costs a
few bytes and draws nothing again; a new version keeps the turns the reader
opened (the card's source carries the version as its `revision`, so an opened
turn's activity is read again) and, when the reader is at the end of the page,
keeps them there; a running session opens at its end. A transcript the daemon
does not serve says why (the agent named no transcript, it is outside the
agent's session folder, the file is gone), and a failed read keeps what is
shown. Back to sessions, `u` or Esc returns to the list.

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
after an explicit generation or fix request.

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
through running, idle and ended. A second marker, `live-transcript`, drives
another session whose hooks name a Claude Code transcript the fixture writes
under its home's `.claude/projects` (the marker's first line is the state, the
others the prompts); a test opens it from the Sessions page, adds a turn
without a hook event and sees it appear, its e-mail address masked, without a
reload. A third, `live-needs`, also takes `permission` (a Claude Code
permission prompt notification): a test sees that session move from Running now
to Needs you, with its message and the caution badge, open its transcript, and
leave Needs you once it works again. The fixture also runs the
automatic generation scheduler, fed a discovery snapshot every quarter second;
a second test writes `push-10` in the state directory to give PR 10 a new head
and follows the automatic review it starts on the PR page and in the jobs list,
then turns the global switch off and on. The fixture's fake GitHub also
answers the update-branch and re-run calls: tests re-run PR 8's failed checks
and update PR 11's branch from their pages, each writing a marker (`rerun-8`,
`updated-11`) the fixture's discovery reads and the test removes again. Unit tests cover
first-run/authorization, namespaced URLs and navigation, each review state,
unknown costs, selection and filters, automatic settings and notices,
and the automatic job labels. The fixture also writes two Claude Code sessions in
the demo clone under its fake home, commits of the person's in the other clone,
plan usage samples and GitHub's answer to the delivery search, and a test opens
Analytics and checks the figures they make, at desktop and narrow widths. The
fixture also serves a real inbox store (`inboxes.json` in its state directory), and
`e2e/inboxes.spec.ts` makes an inbox from a template, changes a part of its
sentence and sees the preview say what changed, switches its badge and sees the
Inbox badge change, and deletes it from the sidebar with Undo. `e2e/inbox-screens.spec.ts`
takes pictures of the inbox pages, light and dark, only with `INBOX_SCREENSHOTS=1`.

Generation usage is retained as provider-reported input, output, cache-read and
cache-creation buckets, with available TTL, speed, prompt range and model-call
reports. Jobs save each update before publishing progress, including failed and
cancelled attempts. Provider-controlled model and speed strings appear only
after secret masking; progress retains numeric buckets while masking runs.
The charged-token budget counter excludes cache reads;
total processed tokens includes them. Missing older accounting remains unknown,
and interrupted providers show reported subtotals rather than exact totals.
Saved review usage follows the version selected in history. Its cost reads
"At API prices": what the usage would cost at API prices, not what a
subscription charges. Unknown costs show a reason; configured model identity
is distinguished from a provider report. Each attempt is priced independently
when its model and pricing inputs are known. Pricing only reports: no budget,
cap, confirmation or decision about a run reads it.

GitHub browser scenarios use the real auth manager/controller and mocked GitHub
HTTP and credential-store boundaries. The `github-auth-mode` marker selects
pending, alice/bob approval, denial, expiry, network failure, insufficient scopes
or locked persistence. Tests cover copy/open, background completion, reload,
account replacement, disconnect with saved-review access, recovery and the local
session/namespace/CSRF boundary. Waiting and narrow screenshots go to ignored
`test-results/` for PR attachments.
