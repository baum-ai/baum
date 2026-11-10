# Local application

For desktop UI development, run `just desktop::dev` from the repository root
on Linux in a Wayland or X11 session. The native app and shared daemon run on the
host; Vite and frontend dependencies stay in Docker. Changes to the daemon-served
app and `../viewer` hot reload without rebuilding Go or Rust. See
[desktop development](../desktop/README.md#development).

A hidden window or closed tray panel makes no requests and repaints nothing.
Every timer that refreshes data or relative times (`useNow`, the plan usage,
review, repository, updater, chat, transcript and GitHub connection polls and
the tray panel's refresh) runs through `useVisibleInterval` in
`src/visible-interval.ts`: it pauses while `document.hidden` and runs once when
the page becomes visible again. The entry points call `trackHidden`, which sets
`data-hidden` on `<html>` while hidden; the viewer stylesheet pauses every
animation then.

The desktop app's own screens are React too: `setup.html` mounts
`src/desktop/setup.tsx` and `connecting.html` mounts `src/desktop/connecting.tsx`,
with typed native IPC and the same Vite, TypeScript, lint and test pipeline. Setup,
connecting and tray screens use the viewer palette that the local app uses,
following the system's light or dark appearance.
Setup has a decorative leaf banner just above its step label, using the
transparent Discord artwork in both themes. Its width is capped so leaves stay
visible on large screens, and it follows the form on tall windows. It is static,
fades into the page and leaves the step controls in place.

Desktop UI colors are defined in `../viewer/src/palette.css`. The inbox,
walkthrough, sessions, settings and charts reuse its semantic tokens. Stumpy's
fixed illustration colors live with the component in
`../viewer/src/components/stumpy-artwork.ts`; the log mark and native icon
generator reuse them. Fixed vendor logo colors live in
`../viewer/src/components/brand-artwork.ts`. `pnpm lint` rejects color literals
and Tailwind color utilities outside the UI palette,
`../viewer/src/themes/*.css` and those two artwork modules.
The default UI palette has nineteen explicit base colors shared by light and dark mode;
its neutral ramps, status backgrounds and interaction shades are derived mixes.
Custom colors go in `../viewer/src/themes/custom.css`, loaded by every desktop
screen after the default palette in a higher-priority cascade layer. Override
bases to recalculate shades, or any semantic token to replace a derived color.
Put overrides on `:root`, with optional `:root[data-theme="dark"]` exceptions;
components consume those values without redefining them. See
[custom color themes](../viewer/README.md#custom-color-themes) for examples and
named theme selection separate from the light/dark appearance preference.

`pnpm lint:palette` runs the color rule by itself; `pnpm lint` and `pnpm check`
include it, so the normal CI app check enforces it. It scans app sources, shared
viewer components, bundled HTML, native sources/configuration and SVG icon
templates. Hex colors, RGB/HSL and other literal color functions, named CSS
colors, and Tailwind's fixed color utilities are rejected with file and line
numbers. Palette variables, `currentColor`, `transparent` and derived mixes of
palette variables are allowed. Only palette/theme definition files and the two
specific artwork modules may define literal colors; tests and generated files
are excluded. Inbox filter color names are persisted palette keys, not CSS colors.

The multi-page production build emits every entry and assembles `../desktop/dist`
for Tauri, where the connecting screen becomes `index.html`, the page the
window opens on; it is taken out of the daemon's copy. Every loading state uses
one component, `NineDots` or `LoadingStatus` in `src/nine-dots.tsx`, whose look
lives only in `src/nine-dots.css`. A page that reads something says which of four things it is
(`src/page-state.tsx`): waiting for its first read, that read failed (`Couldn't load jobs`,
with the daemon's message and one Retry, never an empty page or an endless wait), empty, or its
content. A failed later read keeps the content shown and says so in a banner. Native dialogs
open through `useModal` (`src/modal.ts`), which gives the focus back to what opened them.
Dialog and panel dismissals use the viewer's `CloseButton`; confirmation actions
keep their Cancel buttons until finished. The keyboard shortcuts dialog keeps
its title and close control visible while the list scrolls.
Every screen uses baum's bundled type from the
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

Generated reviews can show retained inline images from linked native Claude
Code and Codex sessions, including linked subagent work, alongside images
uploaded in the PR description. Human-pasted images show “Human-supplied image”
with their session prompt; tool images show “Agent tool result” and the tool
name. A reference or before-state image is not evidence that the finished
change passed a test. Images are copied into the saved review version, so
history keeps them after a native transcript is removed. Entire checkpoint
images and image paths or URLs merely mentioned in a transcript are excluded.
The full review's Images part opens each copy at full size and retains its
provenance; the walkthrough's Full review action opens that view.

Setup and the app's Add repositories use the same repository picker and daemon
search. They first fetch eight top repositories, matching the visible rows, then
fetch up to one hundred more from `topRepositories` in the background using the
returned cursor. Archived repositories and duplicates are omitted. Cached top
matches appear immediately as the person types, before the 250 ms debounce.
The daemon makes a separate GitHub repository-search request, fetching up to
one hundred results and caching each normalized query for five minutes. It never
scans all accessible repositories. An empty search preserves GitHub's top
repository order. Search results merge with cached top matches and sort
by exact repository name or owner/name match, then top status, then latest push.
While a request is pending, the picker refreshes every 300 ms; matching rows can
be picked and onboarding continued immediately. Failed searches preserve cached
matches and offer Try again. Older responses cannot replace newer results.
The footer says how many more rows the list has ("12 more matches") and offers
Show more, which adds twenty rows of the same list or search. When the loaded
rows run out it asks the daemon for GitHub's next page (`more-list`), and while
GitHub has more pages the count includes rows not loaded yet: the full
accessible, non-archived total without a search, or every match of a search.
Once more rows show, the results keep their eight-row height and scroll, so
the page never moves. Nothing is picked
automatically: once the person picks a repository, the daemon looks for its local
clone and the row says where it was found or
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
Desktop setup requires a GitHub connection and at least one picked repository
before continuing or opening a later step from the rail. The GitHub step has no
skip action; until GitHub is connected, a note under it links to the docs for
using an existing token instead of signing in. Adding the baum CLI to the terminal and connecting agent hooks
remain optional; the app runs its own CLI to start the daemon.
The onboarding agent picker offers Claude Code and Codex. A line under
Reviews and extraction and under Fixes says what each agent does, and a single
note below the card explains that the agent and model can be customised per
task in Settings, and automatic reviews per repository.
Missing agent CLIs are marked Not installed and cannot be selected; one that
reports no sign-in (`signed_in` false from `baum hooks status`) is marked Not
signed in, stays selectable and says how to sign in. A saved
choice is kept only if detected; otherwise setup chooses an installed agent,
preferring Claude Code. When neither is detected, setup shows official install
guides and Check again, and waits for an agent before advancing to the final
step. Rechecking selects a newly installed agent without reloading setup. A
failed detection offers a retry rather than claiming nothing is installed.
Onboarding sets two models: one for reviews and data extraction, and one for
fixes, which defaults to Same as reviews. Each compact model row includes an
effort selector, using the model-specific CLI levels documented in
[desktop setup](../desktop/README.md#model-effort-choices). Default leaves the
choice to the agent; other and custom models offer Low to Max. Changing a model
clears an effort it does not support. A fix agent that is no longer installed
falls back to Same as reviews. The native setup bridge saves the review and fix
choices, and saves extraction as the review choice only when onboarding
changes it, so an extraction setup made in Settings survives an untouched
onboarding.

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

The tour's sample project is served under `sample/` within the namespace
(`internal/sampleapp`), and `appBase()` keeps that segment, so the whole app
runs on it unchanged. The tour moves there, with a reload, for its first run
and later when the person's own index has no pull requests, and back when it
closes. There `SampleGuard`
(`src/sample.tsx`) turns the daemon's refusal of a change (its
`X-Baum-Sample: refused` header) into a toast and holds back links out of the
app; elsewhere it does nothing. Its pull request pages link the sample's own
sessions, from its home folder (`LocalAPI.Sessions`), never this computer's,
with the decisions captured from them. In the sample the tour's Inbox stop
picks the person's own pull request, written with their agent, so Next opens
that one.

Buttons the tour's copy names, such as **Ask** or **Focus session**, are drawn
as those buttons but are not controls: the pointer neither hovers nor presses
them.

The PR page reuses the same `@baum/viewer` `ReviewViewer`, header, section/risk
navigation, version menu/comparison, Markdown/code renderers, session cards,
commit panes, tokens and system theme as the current hosted PR page. Its host
adapter supplies local document, history, attachment, patch and redacted session
sources through the same slots as the hosted PR route. A commit opened alone
in the rail's Commits (`?pane=commit&commit=<sha>`) leads with its own subject,
under no list header (the rail leads back to the list), and shows its message body,
its totals ("+N −M across K files") and the files it changed, each file's
diff read when it is opened and drawn as the Files pane draws one: the page
passes `commitsSlot` loaders (`localCommitLoaders` in `src/review.tsx`, over
`createCommitLoaders`) that read `api/commit` and `api/commit-patch` with the
version's selector, or `api/facts/commit` and `api/facts/commit-patch` with
the facts' selector. The daemon answers only for a commit the sessions answer
links and a file that commit changed (404 otherwise); see "Local commits" in
`oss/ARCHITECTURE.md`. The whole list of commits is drawn as the app's list
rows (`withCommitRows` in `src/commit-rows.tsx`): each commit's subject, which
opens it, its short id, author and time, and the session linked to it (which
opens in Sessions), how it was matched and how confident; `j`/`k` move a row
mark through them. A commit opened alone lists its changed files as blocks
`j`/`k` move through, `Space` opening a file's diff. Hosted discussion and approvals are absent. Merging, updating a branch, re-running checks,
submitting a review from the walkthrough, a fix the person starts (with its
replies on the review threads it addressed), and resolving a review thread the
person chose are the app's only GitHub writes (see below).

A reviewed pull request's rail and a stack's have **Files changed** too, as
GitHub's Files changed shows a change: every file the saved review's index
holds (which is every hunk of the change), each with its lines added and
removed, folded in the pane until it is opened from there or the rail
(`documentFilesSlot` in `src/facts.tsx`). A pull request's file diff is read
with `GET api/patch` for the version on screen; a stack's is its hunks'
patches from `api/groups/<id>/patch`, joined.

A pull request with no saved review still shows its facts (`useFacts` and
`factsFilesSlot` in `src/facts.tsx`): what GitHub, the clone and the sessions
on this machine say about it, with nothing a model wrote. Beside the page's
own state (**Generate review**, queued, generating, failed or **Needs a local
clone**, which gets no facts), the Details tab shows its author, agents and
change size, the Activity tab its CI checks, review tools' findings and
linked sessions, and the rail its Sessions, Commits and Files changed, each
file's patch read when it is opened. The page asks `GET api/facts` with the
head discovery shows, again at each index change; the daemon reads GitHub at
most once per refresh interval for the same head and at once for a new one
(see "Pull request facts" in ARCHITECTURE.md). Sessions, transcripts, commits
and patches come from `api/facts/sessions`, `api/facts/commits`,
`api/facts/commit`, `api/facts/commit-patch` and `api/facts/patch`, read
against the facts the page was shown (each names their head, and a newer build
answers 404). Facts are no
version: they are never in the version menu and there is no **Ask** or
walkthrough for them. A rate limit or a failed read says so above the page,
with **Retry**; a daemon that serves no facts shows the page as before. Once a
review is saved the page shows it instead, as before.

A pull request opens on the full review. The PR page has no **Walk through**
button for now, while the walkthrough is unfinished; its overview still opens
from its address (`src/walkthrough.tsx`, `?…&walk=landing`) and from an Ask
answer's **Open the draft**: what matters about it on one page, with no list beside
it. It shows only what says something: the GitHub states that ask for something
(as the inbox draws them, plus failing checks behind a conflict), unresolved
threads, lines and files changed, the review time, the summary, the review's
attention and breaking change, the risk with its reasons, and the chapters with
their lines. Its actions are **Start the review**, the page's own fix, merge,
update and re-run (and regenerate when the review is outdated), **Full review**
(v) and GitHub; agents on the branch, a regeneration and a review that could not
be read show beneath them. **Start the review** opens the tour, `?…&walk=tour`
(or the retired `walk=all`), so a reload stays there. **Full review** (v)
clears `walk`; the retired `walk=full` also opens the full review.

Up next's list shows a review group or stack (`api/groups`) with a shown member
as one row, in the section and at the place of its first member in the list
(`withGroups` in `src/up-next.tsx`); its members leave their own rows and nest
under it when it is opened (its chevron, or `Space`, which opens or closes it
from the group and closes it from a member, back on the group). Each member says what it waits on, since it may belong to another
section: what needs fixing ("Checks failing · Conflicts", "2 unresolved
threads", "Changes requested"), "Ready to merge", or else its kind. A stack's
member whose base branch is another open member's head (`stackBases`) is not
ready to merge, whatever GitHub's merge state, while an open member lies under
it: it waits, as "Waiting on #N", on the lowest of them, which merges first,
and stays out of Ready to merge and Merge all. A stack's members stay out of **Merge all**, as a stack merges from its
base up. The group's panel is laid out as a pull request's: the branch it
merges into ("Stacked on main"), its members' lines, files and review times
summed, a line when its review is missing or outdated, **Open the stack** (or group) as the default, the
same risk card with the group review's main reason, What changed from the group
review (`api/groups/<id>/document`: its summary and sections), and its members.
Their list opens and closes from its head, which counts them, landed ones
included ("7 pull requests"), and those merged (`landed`); it starts open. A
stack's members run from its base up, its landed ones first, joined by a line,
each with a dot and what it waits on ("#963 · Ready to merge", "Merged");
picking one the queue lists previews it, and the others (landed, or needing
nothing now) are shown as read.

Under its title the panel shows the pull request's facts: its head branch and
the base it merges into (`head_ref`, `base_ref`), its lines and files changed
(`additions`, `deletions`, `changed_files` from discovery), baum's time to
review (`review_minutes`) and a link to it on GitHub.
The branches and a GitHub icon linking to the pull request sit on their own
line above a compact row of counts and the review estimate, with field labels
kept for screen readers.
The panel's review summary and risk explanation render Markdown, including
code, emphasis, links and lists; section titles use inline Markdown.

The panel's rows that open to a detail (`BlockRow`) read it only once opened.
A fix lists what blocks it under What needs fixing, opened: its unresolved
review threads, its conflicting files and its failing checks. Any other pull
request lists them under On GitHub, closed: its checks, its conflict and its
unresolved threads.

The unresolved review threads (`ReviewThreads` in `src/threads.tsx`) are asked
for with `GET api/prs/review-threads` and discovery's `unresolved` count, and
again when that count changes. The daemon answers from its cache while the
count matches (see `prupdate.Cache`), so moving through the Inbox does not read
GitHub each time. Each shows its first comment's headline (at most two lines),
its author, its file and line linked to the thread on GitHub, whether it is
outdated and how many comments it has. The headline opens the thread's
conversation (`messages`, up to 20 comments), with a link to the rest on
GitHub. Each comment is rendered from its Markdown as GitHub renders it (code
blocks, `<details>` disclosures, raw HTML sanitised by the viewer's `Markdown`);
a comment over 16,000 characters, or one from an older daemon, which sends no
`markdown`, shows its plain text (`body`) instead. A thread the signed-in account may resolve
(`can_resolve`) has a **Resolve** button that posts `api/prs/resolve-thread` at
once, without a dialog, reads **Resolving…** meanwhile, and removes the thread
from the list once GitHub confirms; a refusal is shown under the thread.

The checks (`useChecks` and `PullRequestChecks` in `src/checks.tsx`) are asked
for by their row as soon as the panel shows it, open or not, with `GET
api/prs/checks` and the head commit the panel shows (`head_sha`), again when
that head changes and every 20 seconds while any still runs; an older daemon's
`api/prs/failing-checks` lists only the failing ones. They are listed failing
first, then running, then passed, then skipped or neutral, each with its state
icon, its name (a GitHub Actions job's after its workflow, as GitHub lists it:
"Web CI / check"; the daemon reads the commit's workflow runs for it), its state in words (`conclusionText`), its title when it has one
and a **Details** link (`url`). While any check fails, only the failing ones
(and any retried from the list) show, the rest behind **Show N other checks**,
which **Hide other checks** folds again; with none failing every check shows.
The same bordered rows align the icon, name and status, and actions in the
review's Activity pane, independently of the Up next panel.
The row's title carries a pill counting them out of them all (`checksCount`):
red "1/9 failing" while any fails, else amber "2/9 running" while any runs,
else green "9/9 passing", with the count by state (`checksSummary`) as its
tooltip. A failing check with a
`job_id` has **Retry** when discovery says the account can push
(`viewer_can_push`): it posts `api/prs/rerun-job` at once, reads **Retrying…**,
then shows "Re-running on GitHub"; a refusal is shown under the check. The
Checks failing row has no fix of its own: the panel's fix covers the checks.

The conflicting files (`ConflictingFiles` in `src/conflicts.tsx`) are asked for
with `GET api/prs/conflicts`. The daemon fetches the base branch's tip (and the
head when missing) into the clone's `refs/baum/pull/<N>/` and merges them in
memory with `git merge-tree --write-tree` (git 2.38 or newer), touching no
working tree or branch. Without an available clone it answers `no_clone`, which
the panel shows with a link to GitHub.
Source chips of a pull request's review stay in the app where the app has a
place for them (`openSource` of the viewer, `src/review.tsx`). The chip for the
PR description ("From the PR description § <section>") opens `DescriptionDialog`
(`src/description.tsx`), a native dialog that reads `GET api/prs/description?repository=<owner/name>&pr=<n>`
(`{"body": "<markdown>", "url": "<pr url>"}`, the body bounded to 64 KiB and
possibly empty; any state of the pull request; picked repositories only). It
draws the body with the viewer's `Markdown` (raw HTML sanitized as for GitHub
comments), scrolls to and marks the cited heading when one matches, shows a
failure with Retry, and keeps a "View on GitHub" link. A chip for another pull
request navigates to its page (`?clone=<clone>&pr=<n>`, else `repository=`).
A Summary context chip for a file, whether or not the pull request changes it
(context is about the code, not the diff, and the reader stays on the page;
`openRepositoryFile` of the viewer), opens `RepositoryFileDialog`
(`src/repository-file.tsx`), a native dialog titled with the path and symbol
that reads `GET api/file?<selector>&path=<path>` for the version on screen
(`{"path", "content": "<text>" | null, "truncated": bool}`, read from the
clone at the document's head, 404 for a path the document does not cite).
It shows the file with line numbers in a monospace scroll area, coloured with
the viewer's `highlightCode` when the language is known, and scrolls to and
marks the symbol's definition (best effort, `symbolLine`: the first line that
reads as `func Name`, `func (r T) Name`, `type`, `class`, `function`, `const`
and the like, preferring one naming a qualified symbol's qualifier, else the
first line holding the name as a whole word). It shows "This file has no text
to show" for a null content, a "Truncated" note past 512 KiB, a failure with
Retry, and a "View on GitHub" link. The group page opens its context chips'
files the same way, from `GET api/groups/<id>/file?version=<n>&path=<path>`,
read at the review's head (its top member's), with the same guard: a path the
group's document does not cite is 404 (`locallink.CitedFiles`). A draft shown
before the first version keeps the chips' former behaviour: a changed file
opens the Files pane, others GitHub. A
commit chip selects the commit in the Commits group when the pull request lists
it. Comment chips and the group page's chips still go to GitHub. The route is
registered in `LocalAPI` next to the other `api/prs/` reads, and
`TestLocalAPIRoutesPullRequestDetailsToUpdate` covers it.

The walkthrough's overview is `?…&walk=landing` (`src/walkthrough.tsx`), and
`walk=tour` (`walk=all`) the tour, so a reload stays there; **Full review**
returns to the viewer. The PR page offers no button for it for now.
`buildTour` (`src/walkthrough-model.ts`) turns the document into chapters, the
logical sections with hunks in `sectionsInReviewOrder` (mechanical and obsolete
sections are left out), and behaviours, one per behaviour step: its `title` (schema revision 2026-10-27.1), its
`text`, the evidence it cites and its `question`, offered as the comment box's
prompt. A review written before titles takes the text's first sentence, cut at
the first semicolon; a section without steps is one behaviour, its summary. A
chapter carries its section's summary, its `intent` block (what the pull request
and its sessions say the change is for), its risk reason and its open risks. A behaviour's code is the viewer's
`HunkEvidence` for the step's hunk anchors, reading omitted excerpts through the
page's patch source. The walkthrough and inbox use the shared palette (`src/walkthrough.css`). It is one guided tour, a
page per chapter, in the overview's column: a row of steps under the header
leads back to the overview, names each chapter (shortened to fit, with a dot per
behaviour: looked at, commented or not yet) and ends at the wrap-up. A
chapter's page states its summary, intent and risk once, then lists its
behaviours as the full review does (title, statement, evidence badges, code);
one is picked, and only the picked one offers Something looks off. The
wrap-up lists the behaviours not looked at and sends the review. j and k move
the pick behaviour by behaviour, across chapters; → and ← move a chapter.
Moving the pick off a behaviour, leaving its chapter or commenting on it marks it
looked at; seen marks and
drafts are kept by the daemon in `app.sqlite` per repository, pull
request and review version, the 40 most recently saved. They survive browser
storage clearing, a fresh window and a daemon restart. The app loads saved
progress before writing; queued changes preserve unrelated concurrent edits.
A failed read or save leaves drafts in the window and shows a retry action.
Submitting posts
`POST api/prs/review` with `{repository, pr, head_sha, event, body, comments}`:
`head_sha` is the reviewed head, each comment `{path, line, side, start_line?,
body}` sits on the step's anchored lines (or the hunk's first added line), and
a behaviour without a place goes into the body under the overall comment. The
daemon sends it once and maps refusals to `{code, message}` (`forbidden` 403,
`not_found` 404, `own_pull_request` and `invalid_line` 422, `failed` 502); on
`invalid_line` the panel offers to resend with every comment in the body. A
sent review clears the drafts and links to it on GitHub. The tour's keys (j/k,
→/←, o, c) are listed in the shortcuts sheet.

A pull request with a merge conflict (`merge` is `conflict`), failing checks
(`checks` is `failing`) or, on the person's own pull request
(`authored_by_me`), unresolved review threads (`unresolved` above 0) offers a
fix, labelled by what it fixes: **Fix conflicts**, **Fix CI**, **Fix review
comments** or a combination such as **Fix conflicts, CI and review comments**
(`fixLabel` and `fixName` in `src/api.ts`). It is offered only when the pull
request is open, is not a fork,
discovery says the signed-in account can push to the repository
(`viewer_can_push`) and the repository has a local clone. The PR page puts the
button first beside the version menu, Pull requests offers it in the row's
menu, and the Inbox's card offers it as its action; `f` presses it on each. Any of them starts the fix at once, with no dialog and no cost
estimate: `fix` in `src/app.tsx` posts `POST api/jobs` with
`{"items":[{"clone","pr","kind":"fix"}],"mode":"single"}` (`startFix`), and
the button reads **Starting…** (in a Pull requests row, **Fixing…** beside
its review) from the click (the app's `fixStarting` set) until the job arrives. The daemon checks GitHub again and runs it with the
fix agent Settings chooses, else the review's harness and model. A fix it refuses answers `409` with
`blocked_reason` (fork, no push access, unsupported harness, nothing to fix),
which `request` turns into the reason in words (`jobReasonText`); the app
shows "Couldn't start the fix: …" where it was asked for: under the PR page's
header, in the Inbox panel, or below Pull requests.
The fix is a job like a review: the Jobs list tags it with the fix's name
and, once done, links the pushed commit and keeps the thread replies and the
agent's summary behind one fold (**What the agent did** when no thread was
answered); the PR page shows its phase with **Cancel fix**, then
the pushed commit or the failure in words, and the button reads **Fixing…**
meanwhile. Both summarise the replies on review threads ("Replied and
resolved 3 of 4 threads", from `thread_outcomes`) and list each thread's
outcome with the agent's summary (`FixDetails`). `reviewJob` ignores fix jobs and `fixJob` finds them, so a fix
never shows as a review being generated. The daemon polls discovery after a
push, so the new head appears without a manual refresh.

A fix can also be made from a conversation with the review’s agent.
`startFix` sends `fix_chat: {conversation_id, turn}` with the fix item to
`POST api/jobs`: only ids, never the question or answer. The daemon reads
that conversation up to the selected answer from the clone’s store. It starts
at once, like other fixes, and is named “Fix from this answer”. It is offered
where `fixEligible` permits a fix, even when nothing is broken, and is never
combined with conflicts, CI or review comments. Refusals such as
`chat_unavailable`, `chat_changed` and `chat_outdated` appear beside the pull
request. The job’s `fix.chat` is the conversation’s id.

The Jobs page lists a repository's first feature build (`kind:
"feature_build"`, `pr` 0) tagged "Feature build" beside its repository, with no link; it
starts at once when asked for, like any other run.

Every job has a page, `?page=jobs&job=<id>` (`JobPage` and `JobTranscript` in
`src/job-transcript.tsx`), which a Jobs row opens (its menu still opens the
pull request or group). Under the job's state it lists what the daemon keeps:
kind, repository, pull request or group, branch, head commit, review version,
who or what started it, when it was queued, started and finished, its run
time, agent and model, model calls and repairs, sections published without
details and the job id; then a fix's thread replies, open, and its usage.
Fix, question, review and details jobs keep the agent's transcript (a feature
build does not). A review's has every model call it made, each marked by a
`call` entry named for its stage and repair ("Outline", "Details · repair 1",
"Codebase context"; "Review" for a one-pass review), and each step names its
`call`; where stages run at once their steps interleave, so a step whose call
differs from the one before it shows its call's name, and a run of tool calls
never spans two calls. The Anthropic API backend reports no steps, so its
review's transcript holds only the call marks. Jobs from before reviews kept
transcripts say none was kept. A fix can be watched live. A fix job with `has_transcript` shows
its transcript on that page, its panels in the inbox's light sand on its darker
sand: the job's phase while it runs, then its summary or failure, above the
agent's messages, collapsed reasoning, tool calls (a status mark, expandable to
their output) and errors.
The summary, messages and reasoning are Markdown, without raw HTML. Complete
JSON objects and arrays are folded under "Structured output" ("Review output"
for `baum.review/1`), with indented JSON in a bounded, scrollable area when
expanded, including in older transcripts. Long tool commands stay within the
transcript width and truncate with an ellipsis.
Consecutive
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

Questions to the review agent (chat). The daemon
serves a pull request's conversations with the agent that wrote a saved review,
by registered clone ID and pull request as for `api/versions`:

- `GET api/prs/chat?clone=<id>&pr=<n>` returns `{conversations: [{id, version,
  backend, model, seeded, created, updated, turns: [{seq, question, focus,
  answer?, job_id, state, reason?, note?, cost_usd?, cost_reason?, created,
  finished?}]}]}`, newest first. `answer` is `{answer_markdown, citations:
  [{path, start_line, end_line}], suggests_fix: {summary} | null}`, masked for
  secrets, its citations checked against the head. `question` is the
  reviewer's own text as typed, not masked (it is masked only when sent to the
  agent). A turn whose `note` says the agent kept no session answered, but the
  next question starts without the conversation's context. `state` is `queued`,
  `running`, `completed`, `failed`, `cancelled` or `interrupted` (a turn whose
  job a stopped daemon left behind). A conversation whose `version` is not the
  latest review is about an older one. `seeded` with a turn's `note` means the
  conversation started without the review's own context.
- `POST api/prs/chat` with `{clone, pr, version, conversation_id?, question,
  focus?}` (question 1–4000 characters; `focus` `{kind: "section" | "stop" |
  "overview", id?, title?, anchors?: [{path, start_line?, end_line?}]}`, at
  most 20 anchors) starts a conversation, or asks the next question of one,
  and answers 202 `{conversation_id, turn, job}`. Refusals are `{code,
  message}`: `invalid_request` 400 (unknown fields included), `not_found` 404
  (an unregistered clone or an unknown conversation), `no_review` 404 (no
  saved review at that version), `needs_clone` 409, `busy` 409 (the
  conversation's last question is still queued or running),
  `unsupported_backend` 422 (the review was not written by Claude Code or
  Codex on this computer), `restarting` 503. Without the capability both
  answer 503 `{code: "chat_unavailable"}`.

Each question is a job of `kind: "chat"` (`chat: {conversation, turn,
version}`, the pull request in `pr`), queued at once without an estimate, which
the app can label "Question on #<pr>". It runs beside reviews, not behind
them, keeps a transcript (`has_transcript`, `GET api/jobs/{id}/transcript`),
reports its phase and cost, and can be cancelled, which settles the turn as
cancelled. A `chat` event `{conversation, repository, pr, turn, state}` goes
out on the SSE stream when a question is queued and when it settles; the
answer itself is read with `GET api/prs/chat`.

The **Ask** panel (`AskPanel` in `src/chat.tsx`) is how the app asks them. A
boxed **Ask** button in the PR page's header and in the
walkthrough's header, or the key `a` (ignored while typing in a text field;
inside the panel's own box it types an a), opens it; Escape with focus in the
panel, its **Close** button or **Ask** again closes it. It is kept in the URL
as `ask=1`, like the walkthrough's `walk=`, so a reload keeps it. `LocalReview`
renders it beside the review (`.ask-layout`): a 22rem right-hand column from
72rem, where the review takes the rest and nothing is covered (under 104rem the viewer folds its side pane to its strip meanwhile, `foldSidePane`), and below that
a modal overlay from the right edge with a backdrop (a click on it closes the
panel) and the review inert behind it. Closing returns the keyboard to the Ask
button. The button needs a loaded review. The PR page's sections have no Ask
button of their own; in the walkthrough, **Ask about this** on a tour stop and
**Ask** on an Everything row set the question's focus: a removable chip above the
box, sent as the API's `focus`, which also opens the panel (`sectionFocus` and
`stopFocus` turn the section's or the stop's hunks into `{path, start_line,
end_line}` anchors, at most 20). The pull request's overview has no button of
its own; a question without a focus is about the pull request as a whole.

The panel lists the pull request's conversations (`GET api/prs/chat`, read
when one of the pull request's chat jobs changes state, through the app's own
event stream, and every 4 s while a question is being answered), named by their
first question. Conversations belong to a review version, and the panel follows
the version on screen: reading an older version in the viewer makes the
questions go to that version. The version on screen is first, under "Review
v<N>"; the others are read-only, under "Outdated: asked about review v<M>" when
M is older than the latest review and "Asked about review v<M>" when it is newer
than the one on screen. A read-only conversation has no box, a note, and **New
conversation**, which starts one on the version on screen (also offered beside
the list). A conversation
with `seeded` shows "Started without the review's own context.", and a turn's
`note` shows under it. The box takes Enter to send and Shift+Enter for a new
line; **Send** reads "Sending…" at once and is disabled while a question of the
conversation is queued or running, which the daemon also refuses as `busy`. A
refusal's `message` shows beside the box as the daemon wrote it, and the
question stays for another try.

A queued or running turn shows its question, its status and the agent's live
transcript (`useJobTranscript` and `EntryView` of `src/job-transcript.tsx`, tool
calls collapsed) with **Cancel** (`POST api/jobs/{id}/cancel`). An answered turn
draws `answer_markdown` with the viewer's `Markdown` component (raw HTML stays
text) and the citations as `path:start–end`
buttons. A citation inside one of the review's hunks opens that hunk's cited
lines with `HunkEvidence` (an omitted excerpt is read through the page's patch
source, `api/patch`); one outside the review's changes shows its path and lines
with a link to the file at the review's head on GitHub, because the daemon
serves no file text other than patches. A turn that failed, was cancelled or was
interrupted says why in words (`turnReasonText`: `validation`, `budget`,
`backend`, `input`, `internal`, `needs_clone`, `unsupported_backend`,
`cancelled`, `interrupted`). The chat does not display costs. Each answer has a
`data-answer-actions` slot (`AskPanel`'s `answerActions` prop) for the actions
that follow an answer: `AnswerActions` (`src/answer-actions.tsx`), drawn only
under answers of the newest review version. **Fix this** (boxed, emphasised
and with the answer's `suggests_fix.summary` above it when there is one) starts
a fix with the conversation's id and the answer's turn; it is offered
where a fix is (`fixEligible` and a local clone) and disabled while a fix of the
pull request runs. **Add as comment** writes the answer's markdown and one
`path:start–end` line per citation into the walkthrough draft through
`walkthrough-model.ts` (`appendToDraft`): after the comment of the stop the
question's focus names (`draftTarget`), else after the review's overall comment
(`Progress.note`). It only writes the draft, kept per review version in the
application database, and calls no GitHub API: the draft is sent only with the
review. The
confirmation, "Added to your review draft", offers **Undo** (`removeFromDraft`,
which takes back what was added while it is still at the end of the draft) and
**Open the draft**, which opens the walkthrough's Everything view and its Submit
panel. An open walkthrough shares the same hydrated draft record. Chat jobs
are not reviews or fixes: `reviewJob` and
`fixJob` ignore them, `chatJobs` lists a pull request's, and the Jobs list and
transcript page call one "Question on #<pr>". The key `a` is in the shortcuts
sheet for the pull request page and the walkthrough.
`t` opens a pull request's fix transcript wherever the pull request is in hand:
the PR page, the focused row in Pull requests, the selected one in Up next and
the focused job in Jobs. It opens the running fix's transcript (Watch), else
the latest run that kept one, so the key keeps working after the agent stops
and after the fix notice leaves the page. The PR page also keeps the pull
request's fix runs (`fixJobs`, newest first) one fold away under the notice
(`FixRuns`): a single line, "2 fix runs, the last 3 hours ago" ("2 earlier fix
runs" while the notice shows the latest), that opens to a line per run with
when it started, how it ended (its phase, the pushed commit, the failure's
reason) and its transcript. It lists the five newest and links the rest to
Jobs. The daemon keeps the 50 newest transcripts; an older run's link finds
"No transcript to show". A finished fix's notice has a dismiss button (×,
beside its Transcript): the notice goes and the run joins the fix runs below,
remembered in this browser (`useDismissedFixes`, the 50 latest). A running
fix's notice cannot be dismissed.

Merging: a pull request whose Merge state is Ready (open, not a draft) shows a
Merge button in the PR page header, **Merge…** in its Pull requests row's
menu, and the Inbox card's action. It opens a confirmation dialog
that reads `GET /api/prs/merge-options` for the repository's allowed methods
(merge commit, squash, rebase), preselects GitHub's default for the user and
offers only the allowed ones. "Merge pull request" posts `POST /api/prs/merge`
with the head commit the page showed, so GitHub refuses the merge if the branch
has moved. The daemon accepts only picked repositories and never retries. A
refusal (head moved, conflicts, required checks or branch protection, no
permission, method not allowed) comes back as `{code, message}` with a 409, 422
or 403 status and shows inline in the dialog, in words; on success the daemon
records the merge, so the lists drop it at once, and the dialog closes without
waiting for the reload. The daemon then re-reads the repository's pull requests
in the background and the lists reload again when that read lands.

Refreshing a page: the Inbox, Pull requests, Timeline, Sessions, Analytics,
Repositories, Jobs, and a pull request's or review group's page end their bar
(the Inbox its list's head; a pull request its walkthrough's head, or the full
review's title row through the viewer's `actions` header slot) with a
circular-arrow button (`RefreshButton` in `src/shell.tsx`). It reads what the
page shows again: the pages of pull requests (the Inbox, Pull requests, a pull
request or group) ask GitHub at once for their repositories' pull requests
(`api/prs/refresh`, by the local clone where there is one) and then read the
daemon again, one repository at a time and each such refresh after the one
before it, unless pressed again (below). A page's latest refresh names the
repositories that failed in an alert, if the page is still shown. The other
pages read their own data again, and a page whose reads an effect makes
(Timeline, Analytics) settles the refresh when the read made for it ends
(`useReread`); the Timeline's lists count as read once the open tab's list
holds a read at the refresh's revision. With the Repositories picker open, the
refresh also reads the picker's list from GitHub again. Until the refresh
settles, and on the Inbox and Pull requests while any refresh from GitHub runs
and during the first read, the arrow becomes the nine-dots loader. Pressing it
again meanwhile starts it again: the refresh under way is aborted, a refresh
from GitHub drops every other one under way (on any page, as one that never
answers holds the queue and the lists' loader) and goes first, and the daemon
read starts afresh, so a read that never answers can always be retried. The
app keeps the page's refresh above the page (`usePageRefresh`, keyed by the
page shown), so a refresh under way survives a pull request switching between
its walkthrough and full review, and each page's refresh runs and shows on its
own: leaving a page and coming back while its refresh runs shows it running. A
page registers its refresh also where it draws no button
(`usePageRefreshAction`), as a pull request or group while its review loads,
or a walkthrough with nothing to walk through; the key and the button press
the latest registered, so the open Repositories picker's refresh takes the
page's. `⌘R` (Ctrl+R elsewhere) is one of the app-wide keys and presses it, in
text fields too; under a dialog it does nothing. It never reloads the app: a
page with no refresh of its own, such as one still loading, reads all of the
daemon's data again, restarting a read under way. `⇧⌘R` stays the browser's
reload.

Keyboard shortcuts (`src/shortcuts.tsx`, one key table that both the handling and
the sheet read). Every page is reached from the keyboard. With the command key
(⌘ on a Mac, Ctrl elsewhere): `1` to `8` open the sidebar's pages in its order
(their modifier can be changed in Settings › General: Command, Control or
Option on a Mac, Ctrl or Alt elsewhere; the sidebar, its tooltips and the sheet
show the chosen one, and the other app-wide keys keep the command key)
(Inbox, which opens the first saved inbox, Sessions, Timeline, Pull requests,
Analytics, Repositories, Jobs, Settings; digits
are read by their place on the keyboard, so layouts that shift them work),
`,` opens Settings, `K` opens **Go to** (`src/go-to.tsx`) and `R` refreshes
the page (above); letters are read by their place on a layout without Latin
ones. Back and forward
are `⌘[` and `⌘]` on a Mac and `Alt+←` and `Alt+→` elsewhere. These work in
text fields too, but not under a dialog other than the sheet or Go to, which
they close. Go to searches the pages, the pull requests (by title, repository,
number, author or branch; what waits in an inbox first), the review groups and
the agent sessions, and, with a daemon that has inboxes, each saved inbox by its
name (opening its queue) with **New inbox** and **Manage inboxes**. There is no
Inbox page to go to, as no page shows the whole queue: every word typed must match, a label that starts with the
query ranks first, `↑` and `↓` move, `Enter` opens and `Esc` closes. The sidebar
draws each page's key while Alt is held and names it in the link's tooltip and
`aria-keyshortcuts`.

Tab (`useTabStops`) reaches every control without leaving the keys' model
behind. On a page it moves by place: the sidebar is one stop, on the current
page, where `↑` and `↓` move through its links, and so is each Analytics
chart, whose bars they move through; all of a page's list rows are one stop,
landing on the focused or selected row (else the first, which becomes the
focused row), so `j`, `k`, `Enter` and the row's keys go on from there, and a row's other controls stay with its keys (`x`,
⌘. for its ⋯) except a form opened in it, such as a repository's clone form in
Settings, whose controls are stops; every other control (a page bar's, a
detail pane's, a section heading's) is a stop of its own. Shift+Tab goes back,
and both wrap. In a dialog Tab goes through each of its controls, a radio
group as one, and stays inside it. A row's menu and the sidebar's inbox menu
close on Tab, with the focus back on their ⋯. With focus on a link or button
that Tab reached, `Enter` and `Space` press it; every other key still
acts on the selection, and `Esc` brings the focus back to the selected row
(or lets it go where there is none) instead of clearing the row. Rows keep
their own `Enter` and `Space`, and Settings its own keys. Nothing changes for
whoever never presses Tab: the sidebar's arrows, `Esc` back to the row and
`Enter` and `Space` pressing a focused button apply only once Tab was
pressed, until the pointer is next pressed, and a
link or button clicked with the pointer gives up its focus
(`usePointerBlurs`), so the next `j` or `k` paints no ring on it;
rows, tabs, menus, dialogs and Settings keep a click's focus, being where the
keys act. Rings show only for the keyboard (`:focus-visible`). `Esc` in a text
field gives it up, as before. On a pull request, a group, a job's
transcript or a session, `Esc` and `u` go back to the page it was opened from
(an inbox, Pull requests, a group), past moves within it (a
walkthrough step, the Ask panel): each history entry the app adds records its
place and the entry it was opened from (`writeHistory`, `backToOpener`). A window
opened on the page itself goes to its list.

On Pull requests, `j`/`↓` and `k`/`↑` move a row focus through the
rows as drawn (group rows count; rows of collapsed buckets and collapsed groups
are skipped), `Enter` or `o` opens the focused pull request or group, `x` toggles
its selection, `Space` opens or closes its peek (on a group's row, shows or hides its pull requests), `m` opens Merge and `f` starts the fix where the row offers them (Merge wins
where both could), `t` opens its fix transcript, `r` refreshes from GitHub at once rather than after the polling backoff,
`h`/`←` and `l`/`→` move to the previous and next tab (Open, the saved tabs, Closed),
and `Esc` clears the focus. On a pull request's or group's page, `Shift+K`
(`Shift+↑`, `←`) and `Shift+J` (`Shift+↓`, `→`) move to the previous and next
place in the review rail, as it shows
them: Summary, Changes and, while it is open, each change, Files changed,
Decisions, and Sessions and Commits with theirs while open (the viewer's
`ReviewSteps` `rail`); a folded group is one place. `Space` on a selected group
(Changes, Files changed, Sessions, Commits) opens or closes it in the rail
(`toggleGroup`). `j`/`↓` and `k`/`↑` move through
the blocks of the one read (its attention, each behavior, risk or finding, a
commit's or session's card, each file of a commit opened alone, a decision,
or, in a session's transcript, each prompt and agent reply as the transcript
page moves through them: the viewer's `data-review-block`; a
block holding blocks, such as a session's card of decisions, is passed over for
what it holds), bringing it into
view, marking it and giving it the focus; a new section starts from its first.
`Space` opens or closes the marked block's first fold (a behavior's excerpts, a
risk's details, a decision; `toggleActiveBlock`), and still presses a focused
button or link. `Enter` opens the marked block where it opens somewhere (its
`data-block-open` control, as a commit row's subject; `openActiveBlock`). The
arrows then no longer scroll the review. Sessions, Jobs, Repositories and the Timeline share the list keys
(`useRowFocus` and `listKeys`): `j`/`↓` and `k`/`↑` move, `Enter` or `o` opens
the row (a session's transcript, a job's page, a repository's
settings) and `Esc` clears the focus. On Sessions the three sections are one
list and `p` opens the focused session's pull request; on Jobs `t` opens the
transcript (Watch while a fix runs); on Repositories `a` opens or closes the
picker and `/` searches it (the search takes the focus when the picker opens,
and `Esc` in an empty search leaves it); on the Timeline `Enter` or `o` opens or
closes the feature or decision, an open feature's side panel follows the
focus, and `/` focuses the search box (its hint shows inside the box). Settings' section list is its
selection, as Up next's list is: `j`/`↓` and `k`/`↑` move it, `1` to `5` jump
to a section, and `Enter`, `l` or `→` go into the shown section, where `j` and
`k` step through its controls, `Space` or `Enter` changes the focused one and
`Esc`, `h` or `←` go back to the list (`Esc` in a text field or select gives
it up first, and `j` and `k` go on from it). On a pull request or group page `j`
and `k` step to the next and previous place in reading order, as the
navigator's arrows do (`ReviewViewer`'s `steps` ref), `m` and `f`
merge and fix where the header offers them, and `t`
opens the fix transcript (its hint is on the notice's Watch or Transcript, else
on the runs' fold). On any
page, `?` (or the `?` button in Pull
requests' page bar) shows the sheet of every key, grouped by where it works
(anywhere, the Inbox, Pull requests, a pull request or group, Sessions, Jobs,
Repositories, a session or job transcript), with the current page's group
first; `?` or `Esc` closes it. Key hints drawn
on buttons (class `key-hint`) show only while Alt, Option on a Mac, is held:
`useKeyHints` marks the document root then and clears it on release or when
the window loses focus. Every page also shows its own keys then, in one line
along the foot of its panel (`KeyBar` in `src/shortcuts.tsx`): the keys of its
group in the sheet that carry a short name, those sharing one together ("j k
move"), then ⌘. for the row menu on a list. Footer mnemonics are specific to
the current page; app-wide keys such as Go to and `?` stay in the shortcuts
sheet. The bar reads the sheet's table, so it names only keys that work. The
Inbox draws it at the foot of its list instead. The focused row has an
accent bar and a faint fill, distinct from hover and selection, and keeps it
when the focus goes elsewhere (`data-focused`); focus moves to the row's own
link, so screen readers follow, and the row scrolls into view only when any
of it is out of sight; the list's first row takes it back to the top, with
its heading. Each list (Pull requests, Inbox, Sessions, Jobs, Repositories,
Timeline) keeps its focused or selected row for as long as the app runs
(`useLastRow`), and a link clicked in a row, or a row button that opens it in
place, keeps that row too, so coming back from what a row opened
(`Esc`, `u`, Back or the sidebar) lands on the same row, scrolled to the middle.
`Esc` on the list gives the row up.
Keys are ignored with Ctrl, Meta or Alt held, in text fields, selects, text areas
and editable content (checkboxes and radios still pass them) and while any dialog
is open. On a pull request or group page, `u`, or `Esc` with nothing focused,
returns to the page it was opened from (the inbox it was opened in, whose
address the pull request or group keeps as `inbox=<id>`, or Pull requests with
the filter kept; a link with no inbox goes to the first one); on a session's or job's transcript the same keys return to
Sessions or Jobs. Analytics has no keys of its own beyond Tab: its controls
are native buttons (its window, chart and breakdown toggles are pressed
buttons in a group), and its charts' bars take the focus for their tooltips.

The PR page header keeps buttons at its end: **Ask**, **Approve** for an open
pull request (see [Your review](#your-review)) and, when it can merge,
**Merge…**. The pull request's GitHub state beside the version is a
menu (`StatusMenu` in `src/review.tsx`) holding what can be done about it: the
fix (as `fixLabel` names it), **Re-run checks** and **Update branch**; with
none to offer it is the state alone, and while a fix starts or runs a live dot
says so beside it. **Regenerate review** is at the end of the version menu
(the viewer's `header.versionActions`), read "Generating review…" or "Review
queued" while one runs. Update branch and Re-run checks (`src/unblock.tsx`)
are on the PR page, not in Pull requests: a row's menu
holds the one action its state asks for (Merge on a ready row, else Fix), and
the page is where the rest of a pull request's state is read. `canUpdateBranch` offers the first
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
so `Enter` confirms and `Esc` cancels with no Tab needed. Confirming posts
`POST /api/prs/update-branch` or `POST /api/prs/rerun-checks`
with the head commit the page showed (labels turn to "Updating…" and
"Re-running…" in the same frame), so a moved branch is refused in words
(`{code, message}`, shown inline). A successful update stays open until Closed
because GitHub merges asynchronously; a re-run where GitHub refused only some
runs stays open and names them. Either success makes the daemon re-read the
repository's pull requests.

An inbox's queue (`?inbox=<id>` on the app root, `src/up-next.tsx`) is a split view: a list of
what waits on the person, grouped by what they do about it, beside the panel
of the selected pull request. The page uses the shared palette in both themes; its list and
panel scroll on their own, and below 760px the list stacks above the panel.
`upNext` places each open pull request in at most one kind, from discovery's
flags only: **Review** (their review is requested, or someone else's pull
request is assigned to them), **Clone** (such a pull request whose repository
has no local clone), **Merge** (theirs, `canMerge`
and no checks running: GitHub can call a head just pushed clean before its
checks are queued), **Fix** (theirs, with changes requested, unresolved threads, a
conflict or failing checks) and **Update** (theirs, and `canUpdateBranch`).
The queue runs in that order, which is the order of the list's groups:
**Needs your eyes** (Review, Clone), **Ready to merge** (Merge) and **Hand to
an agent** (Fix, Update, then the person's pull requests a fix agent is working
on, each with the loader and the fix's phase). Within Review a saved review
ready to read comes before one being written, then the riskier first;
otherwise the lower number first. Each pull request appears once, whatever
clones hold it. After the groups, their own pull requests with nothing to do
(drafts, and those waiting on reviewers or checks) are listed under **Waiting
on checks or reviews**, and every other open pull request (`others`: someone
else's that neither asks for their review nor is assigned to them), oldest
first, under **Not waiting on you**. Rows of both say where the pull request
stands instead of a kind: the person's own what it waits on, others' "Draft",
"Unclaimed" (no assignee and no review requested from anyone), "Approved",
"Changes requested", "Review requested" or "Assigned" (`othersState`). Both are
waiting rows: the selection does not open on them, they have no kind's action,
and the Inbox total leaves them out.

The list's header counts what is the person's, sums baum's reading estimates
into minutes (only reviews have one), counts the fixes with agents and what was
done today, and draws a bar of cleared against cleared plus remaining. A row
shows the title, then the kind, repository and reading estimate (or the
agent's phase, "last fix failed" or "last fix changed nothing"), and a red dot at risk 5. With two or more
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
a running fix, else nothing (the GitHub icon among its facts opens the pull
request on GitHub); then **Approve** for an open pull request (see [Your
review](#your-review)) and **Snooze**. A scored pull request has
a risk card: the score as five blocks, its meaning and, from the saved review,
its first reason. Below it the panel lists, by kind: for a Review or Merge,
"What changed" (the review's summary and its first six sections in review
order, with their scores and lines added and removed); for a Fix, what needs
fixing; for an Update, where it stands; for a Clone, what baum will do. The
saved review is read when its pull request is selected, and again only for a
new version. While the first review is being generated, What changed shows
the available draft summary and section titles, updating as the draft advances.
Until a summary exists, a live line under the actions says the review is
being generated. A saved review stays visible while a replacement is being generated.
Without a review, or when the last one failed, a card in the same place says
"No review yet." (or "baum's review failed: <reason>") with **Generate** (or
**Try again**), which queues it as the PR page's Generate does; a refusal shows
in the card. A pull request without a clone has no button; a Clone item lists
what baum will do instead.
A pull request back in the queue as a Fix whose latest fix job
failed says "The last fix failed: <reason>", with **See what the agent did**
(the transcript, else the job in the list). One whose latest fix changed
nothing (`no_changes`, which is the agent's judgement, not a failure) says so
in a neutral notice with the first paragraph of what the agent said, which
says why, and **Read what the agent said**; its row says "last fix changed
nothing". The PR page's fix notice and the job page say it the same way,
without the failure icon. Agent sessions working on its
branch are counted at the foot of the panel, as a link to Sessions
(`?page=sessions&peek=<agent:id>`) that opens that session's card there, its
peek and its reply where it takes one, as a click on it does (with several,
each by its agent's name).

Snooze offers 1 hour, this afternoon (3 PM, while that is more than an hour
away), tomorrow morning (9 AM) and **Until it changes**, which hides the pull
request until its head moves. A snooze selects the next pull request and shows
a toast with **Undo**; **Bring back**, in a snoozed row's menu under Snoozed,
ends one early. Waiting pull requests, including those waiting on another
member of their stack, offer Snooze too. Snoozed waiting PRs keep their
waiting classification, so the same inbox filters apply before and after
snoozing. Snoozes still apply when a pull
request moves between needing an action and waiting; a stack disappears
from the active queue when all its pull requests are snoozed. A snoozed pull
request stays under Snoozed when its Fix clears, rather than Done today. Keys:
`j`/`↓` and `k`/`↑` move the selection, `Enter` opens, `m` merges a Merge, `f`
starts a Fix item's fix (where its panel offers the button), `t` opens its fix
transcript (Watch the agent, See what the agent did, or the last run's), `v` opens
Approve's menu, `s` opens Snooze
and `1`–`4` choose from it, `u` undoes a snooze. A review sent from Up next
shows a toast with **Open it** (the review on GitHub).

"Done today" lists the pull requests that left the queue since first seen
today because the person's part is done: it merged or closed, the review
request went ("Reviewed", or "Handled" for one that needed a clone), their fix
pushed (a fix counts only once its job completed with a commit: never while it
runs, nor after it failed or was cancelled) and left it waiting on checks or
reviewers, or their branch update did. Their own pull request that only stopped
being mergeable (a push, checks running) waits uncleared, and nothing is
cleared for a repository the latest read lists no open pull request of, since
GitHub may only be unreachable; one that comes back is no longer cleared.
Snoozes and today's record are kept in the daemon's `app.sqlite`,
independently of the browser origin, and start again each local day, snoozes
excepted. Initial bookkeeping waits until saved state has loaded. Failed reads
or saves show a retry action and preserve unsaved changes in the window. The
sidebar's Inbox heading shows the total of the queue without snoozed pull
requests: each pull request once, whether it is in several saved inboxes or in
none, and whatever the inboxes' badge switches say. The daemon counts the same
total for the desktop tray and dock badge (`internal/inboxqueue`), so a change
to these rules belongs there too.

**Inboxes** are saved filters over this same queue, defined in the daemon (see
its [inboxes README](../internal/inboxes/README.md)) and matched by it:
`GET api/inbox` ([`internal/inboxqueue`](../internal/inboxqueue/README.md)),
read with the index, says which saved inboxes hold each open pull request, so
the page does not run every inbox's query over every pull request. The
inbox's page, its sidebar count, the panel's tags, the edit page's preview and
the new-inbox page's counts all take it from there (`Held` and `holds` in
`src/inboxes.ts`). A filter typed over an inbox, an inbox changed on its edit
page and the templates are asked of the daemon too, as
`api/inbox?query=…`, 150 ms after the query last changed and again after each
read of the index. Until the daemon has answered for a query, when it matched a
pull request as another kind than the page did (a read in between), and with
an older daemon, the page matches itself with the same language
(`src/pr-filter.ts`, whose Go twin is `internal/prfilter`; both run the
shared cases in `internal/prfilter/testdata/cases.json`). A pull request done
today, and the filter menu's counts of each value, are still matched here. An inbox holds the
open pull requests of this page (the queue's items, the person's own that
wait, and others' as `waiting=other`) that match its query (see the filter
below; one that does not read narrows nothing). An inbox with no query filters
nothing: it lists every open pull request. The daemon's older parts of an inbox read as part of it
(`foldedQuery`): what it holds as `waiting=review` (Review and Clone items),
`waiting=merge` (Merge items), `waiting=agent` (Fix and Update items and the
pull requests a fix agent is working on now) or, for the person's own,
`author=@me`; owners as `org=`; risk as `risk>=3` or `risk>=4`; repositories as
`repo=`; and leaving out bots as `is!=bot`, each before the query, which is
parenthesised when it has an `OR`. The next save writes the inbox with only the
query. A pull request can
be in several inboxes. There is no separate page of everything. The daemon
always keeps at least one inbox (a fresh installation gets **Default**, whose
query `author=@me OR assignee=@me OR review-requested=@me` (`FOR_ME`) keeps
what the person wrote, is assigned or was asked to review, and which is an
ordinary inbox that can be renamed, edited or deleted while another remains;
an upgrade with saved inboxes adds none and instead puts that query in front
of each inbox's own, since inboxes used to filter only the queue), and the app
root, a legacy link to it, an `inbox` the
daemon does not have (as one just deleted) and the start-up and the end of
setup all open the first inbox in the sidebar's order. The address is replaced
by `?inbox=<first id>` in the same history entry, so Back does not return to
the root, and its other parameters stay. An address naming no page counts as the root. A pull request or group
link with no `inbox` (from the Timeline, Go to or a session) keeps its address,
and while its pull request is not in the index the page behind it is the first
inbox's queue, never empty. Until the first read of the inboxes has
answered the page says "Loading inboxes…", and after a first read that failed it
shows the read error with Retry; with an older daemon it says "Inboxes need a
newer baum". None of them shows a queue. An inbox's queue has the same groups, order, panel, keys, snoozes and **Merge all N** (which counts only
that inbox's Merge items). Its header shows the inbox's colour dot and name:
the dot opens its six colours, and a click on the name renames it in place
(Enter or leaving saves, Esc keeps the old name, a blank or taken name is
refused there); a right click on the head offers **Edit…**. An empty one says
"Nothing here right now." under a dozing Stumpy (an awake one while no repository
is picked). Snoozed, **Done today**, the header's done count and
progress bar, and the group of the person's own pull requests that wait on
checks or reviews list only the inbox's pull requests. A pull request done today matches as it was when last
in the queue, from its row kept with today's record, even once the app no
longer lists it. A row carries no inbox tag; the panel tags the other inboxes
the pull request is also in. A pull request or group opened from an inbox keeps
`inbox=<id>` in its address, so its back link and `u` or `Esc` return to that
inbox. Colours are six palette names (`--inbox-*` in `src/style.css`, with
dark variants).

A Help button at the right of the sidebar's baum title (under it when the
sidebar is folded) opens a help menu (`HelpButton` in `src/shell.tsx`):
**Show shortcuts** opens the shortcuts sheet, **Report a problem** opens the
Report a problem dialog (`src/report-problem.tsx`, also Diagnostics' button:
what happened, what to attach with a preview, then **Open issue**, which opens
a new issue in baum-ai/baum on GitHub; see the
[desktop README](../desktop/README.md#report-a-problem)), and **Open the docs** opens https://docs.usebaum.com/ in a new
window (the desktop app hands it to the browser). The `?` key opens the same
menu with the keys on Show shortcuts, so `?` then Enter shows the sheet; with
the sheet open `?` hides it. Opened by the key, the menu gives the keys back to
where they were rather than to the button. With the glyph-only rail the button
sits under the Settings glyph.

In the sidebar Inbox is a heading, not a link: it has no address, is never the
current page and shows the total of what waits (and, for screen readers, ", N
waiting" after its name; nothing at zero). That total counts the distinct
unsnoozed queue items, ignoring the selected inbox, filters and badge switches;
with an older daemon, which has no inboxes, it is the same count. Its chevron folds the saved inboxes
nested under it
(remembered in `localStorage`; hidden with the glyph-only rail). The pages and
inboxes scroll within the sidebar, so with many inboxes plan usage, Jobs and
Settings stay in view; a row's menu is placed against the window, under its row
or above it near the bottom, and closes when the sidebar scrolls. Each row links
to `?inbox=<id>`, is the current page when selected, and
shows a dot, the name and how many unsnoozed items match (`queueItems`: the
queue's items and others' open pull requests, not the person's own that
wait), none when 0 or when
the inbox's badge switch is off (that switch hides only this row's count, never
the Inbox total). The
`⋯` button, or a right click on the row, opens a short list of buttons,
focused on the first: **Edit inbox**, which opens the inbox's edit page, and
**Delete inbox**, which is disabled with "Keep at least one inbox" when only one
is left. Esc, a click elsewhere or choosing closes it and returns
focus to the `⋯` button (after a delete, to the neighbouring inbox; after a
cancelled one, to the `⋯`); focus never drops to the page. Delete first
asks (only the view goes), then toasts "Deleted <name>." with **Undo**, which puts back only that inbox,
with its id so links to it still work, into the list as it is then, at its old
place (or the end), so a change made while the toast was up is kept; an inbox
with that id already there is left alone. Deleting the open inbox opens the first
one left, in its entry. The chevron is labelled "Inboxes",
with `aria-expanded` and `aria-controls` for the nested list. Changes show at once and are sent to the
daemon one at a time, each carrying the newest revision, so an older change
cannot overwrite a newer one; a conflict adopts the daemon's list and any other
failure restores the last saved list with an error. The daemon refuses to save an empty list (400 `last_inbox`), and the app does
not offer it; if it answers so, its message is shown as any failed save. An older
daemon (the read answers 404, or `available: false`) has none of this: no
chevron, rows, tags or changed badge, and the inbox page says it needs a newer
baum. Any other failed read (a 500 `inboxes_unreadable`, a dropped
connection) is not an older daemon: it keeps the list shown (on the very first
read, no queue, only the error with Retry) and says "Couldn't read your inboxes"
in an alert until a read succeeds, and later saves still go out. Entries the daemon sends
are read defensively (missing lists are empty, unknown values the plain ones,
entries without an id and name dropped).

**The filter** narrows each inbox's queue and Pull requests. It is a
query written like a GitHub search, parsed and matched in `src/pr-filter.ts`.
A term is a field, an operator and a value:

| Write | Keeps pull requests |
| --- | --- |
| `repo=acme/app`, `repo=app` | in the repository (whole name, or the part after the owner) |
| `org=acme` | whose repository's owner is acme (an Enterprise one's too) |
| `author=@me`, `author=sam` | by the person, or by that login |
| `assignee=@me`, `review-requested=@me` | assigned to the person (first 20 assignees), asking for their review |
| `assignee=none`, `review-requested=none` | assigned to nobody, asking nobody (no person or team) for a review |
| `review=approved`, `changes_requested`, `none` | with GitHub's review decision (none: neither) |
| `checks=passing`, `failing`, `running`, `none` | with that state of checks |
| `is=draft`, `mergeable`, `conflict`, `blocked`, `behind`, `bot` | drafts, GitHub's merge state, behind their base, by a bot |
| `risk=4`, `risk>=3`, `risk<3`, `risk=none` | by the baum review's score (1 to 5); without a score only `risk=none` |
| `base=main` | merging into the branch |
| `waiting=review`, `merge`, `agent`, `other` | in an inbox's queue only: waiting for the person's review, to merge, for an agent's fix, or someone else's waiting on nothing from the person |
| any other word, or a "quoted phrase" | with it in the title or repository, or that `#number` |

The operators are `=` and `!=` (not any of the values), and for risk also
`>=`, `<=`, `>` and `<`; `:` reads as `=` (`risk:>=3` as `risk>=3`), and a
comparison takes scores only. Keys and values ignore case. `key=a,b` takes
either value, `-` before a term or a group leaves it out, terms side by side must
all hold (`AND` may be written), `OR` takes either side and binds looser than
`AND`, and parentheses group. A query that does not read leaves the last one
in force, and the bar says why ("Not applied: …").

The filter icon (`FilterButton`, `src/filter-menu.tsx`) sits left of the
refresh in the Inbox's and an inbox's head, and at the end of Pull requests'
tabs; it is lit, with a dot, while a query is in force, and no query line
shows on the page. Its menu, as Linear's, sits on the window under the icon,
kept on screen: an "Add filter…" box over the fields (Repository,
Organization, Author, Assignee, Review requested, Review, Checks, State, and in
an inbox's queue Waiting for, then Risk, Base branch); pointing at one, or
Enter or → on it, opens its values beside the menu (or in its place when
neither side has room, where pointing alone does not open it), under the
operator they are picked with: **is** or **is not**, and for Risk also **≥**
and **≤** (Tab moves it along). On every page (the Inbox, an inbox's queue,
an inbox's edit page and Pull requests), Repository, Organization, Author and
Base branch offer every value known, not only those of the pull requests the
page holds now: the repositories picked, registered as clones or with any pull
request read, their owners, and the authors and base branches of every pull
request read (`Known`, `valuesOf` in `src/pr-filter.ts`); one with nothing on
the page counts 0. Each value is ticked while the query requires
it with that operator and counted by how many of the page's pull requests
picking it would leave. Picking a value adds it to the term of that field and
operator as one more choice, or takes it out (with an `AND` beside it and
parentheses left empty), and adds a term when there is none (around a query
with `OR`, in parentheses); the menu stays open. Typed text also lists
matching values of every field, then adds the text itself (a phrase to look
for, quoted when it would read as syntax, or a query that reads); **Write a
query…**, offered only while there is no query (one shows its box already)
and only in a menu with the box at its foot, puts the focus in the query. Esc or ← steps out of the values, then closes
the menu.

The menu's foot holds the query as text (`FilterBar`), wrapping onto more
lines as it grows, with **Clear** (not on an inbox's queue or edit page,
where the tokens' × and Command+C empty it) and the page's save. As it is typed it
suggests, one part at a time, the fields that start with the word at the caret
(`-` for not), then the field's operators, then its values, and `AND` and
`OR` only after a term or a closing parenthesis (first, while nothing
is typed), above the box; nothing is suggested inside a word or at its start.
With the caret right after a whole term (as on focus, which puts it at the
end), the list opens with what may follow, taken with a space before it. The
arrows move through them, Enter or a click takes one, Tab takes the
highlighted or first while a word is being typed (otherwise, and after a whole
term, it moves on), and Esc closes them, then the menu. Each edit that reads applies a moment
after the last key, and at Enter or leaving the box; one longer than 500
characters is not applied and says so. Without the focus, a box holding the
query in force draws each term as a token (`queryTerms`), its text as written
with a × whose label says it in words ("Remove Assignee is Me"); parentheses,
`AND`, `OR` and `-(` stay plain text between them. The × applies the query
without that term (`withoutTerm`): the `AND` or `OR` beside it goes too, a
group left with one choice loses its parentheses, an emptied group (with any `-` before it)
goes, and what the removal does not touch keeps its text. The box lies
under the tokens: a click anywhere but a × focuses it with the caret at the
end, and the text shows until the focus leaves.

There is no whole-queue filter: the filter is always an inbox's own. **Create
inbox** (below, and Command+Enter) is not offered at 20 inboxes or with an
older daemon. In a saved inbox the menu starts from the
inbox's own query, its values ticked; a change is a draft that narrows the
queue (kept per inbox) until the save, as GitHub's merge button: **Update
inbox** gives the inbox the query, and the arrow beside it offers **Create
inbox**, a copy of the inbox with it, and **Discard changes** (all only while
the draft differs; the draft goes once the inbox holds it, so a failed save
keeps it). Command+C there empties the draft, which then shows every open pull request.
`waiting=` is refused outside an inbox's queue. With nothing matching while
the filter hides something, the queue's panel says "Nothing matches the
filter", how many it hides, and **Discard the changes**. Tabbing out of the menu closes it. Each page keeps its own filter or draft (`inbox`,
`inbox:<id>`, `pulls` or a saved tab's `tab:<id>`) in the daemon's
`ui/filters` document (see the [uistate README](../internal/uistate/README.md)),
across windows and restarts; a failed save says so with **Retry**.

Inboxes are made and changed on three pages that sit under Inbox in the sidebar
but are not sidebar pages of their own, so they have no number key, and ⌘1 opens
the first saved inbox (`src/inbox-pages.tsx`; routed in `src/app.tsx` by `?page=`). A
right click on the Inbox heading in the sidebar offers **New inbox…** and **Manage
inboxes…**; a right click on an inbox's row, like its ⋯, offers **Edit inbox**
and **Delete inbox**, and on its queue head **Edit…**. A right click on a pull request in
an inbox's queue (or ⌘. on it) offers what its panel and keys do: **Open**, the
agent's transcript, **Merge…**, **Update the branch…** or the fix, the review
verdicts, **Snooze…** (which picks the row and opens its snooze choices) and
**Open on GitHub**; on a group's row, **Open** and Show or Hide its pull requests; on a
row under Done today, **Open** and **Open on GitHub**. Deleting an inbox, from
there or its edit page, first asks, saying only the view goes and its pull
requests, reviews and snoozes stay; then it is deleted with Undo. With an
older daemon none of these links, pages or Go to entries exist, and a direct
address to them says "Inboxes need a newer baum", only for an older daemon (the
read answers 404 or `available: false`): before the first read has answered the
page shows "Loading inboxes…", and after a first read that failed it
shows the read error with Retry. On New inbox and Manage inboxes the Inbox
heading is only the section (it is never the current page); the
edit page keeps the inbox's own row current.
Every control is a real button, link or input with
a label, with no key of its own, reached with Tab.

- **New inbox** (`?page=inbox-new`): "What should this inbox catch?" and seven
  templates, each a button with a colour dot, name, sentence and how many
  unsnoozed pull requests it would show now. Custom (blue) has no filter and
  says "Every open pull request, until you add filters.";
  Urgent (red) is what is the person's (`FOR_ME`) at risk 4 or 5; Work (blue)
  is what is the person's in every owner of their repositories except their
  login (any repository when there is none);
  Personal (green) is the login's own repositories with the badge off; Quick
  merges (amber) and For an agent (grey) pick that kind; Unclaimed (purple) is
  `waiting=other assignee=none review-requested=none is!=draft is!=bot`,
  others' pull requests nobody has picked up. A name another inbox
  has becomes "Urgent 2", "Urgent 3"… Picking one saves nothing: it opens the
  edit page, titled New inbox, on an inbox not saved yet ("Not saved yet.
  Adjust anything below, then Save.", its Name input focused, "← New inbox"
  back), whose changes stay in the page; **Save**, in Delete inbox's place,
  checks the name as typed, appends the inbox to the list with an id the app
  makes in the daemon's shape and, once the daemon has it, opens its queue (a
  refused save leaves the unsaved inbox on the page). Leaving the inbox pages
  drops it. At 20 inboxes the templates are
  disabled, with a line saying so.
- **Edit** (`?page=inbox-edit&inbox=<id>`): "← <name>" back to its queue, a
  "Saved" mark after each save the daemon accepted, and "Changes save as you
  make them". The name is an input saved on blur or Enter; a blank or
  too-long name, or one another inbox has (ignoring case), is refused in place
  and not sent. Six colour swatches have radio semantics. "What it catches" is
the inbox's whole query in one box, a quiet **+ Add filter** at its end
(opening the same menu, Waiting for among the fields); the
query, its terms tokens whose × saves the query without them (see the filter
above), each change saved a moment after it reads. The box stays with no
query, its placeholder "No filter: every open pull request". Under it, **Quick
filters** (`QUICK_FILTERS` in `inboxes.ts`: For me, Risky, Unclaimed,
No drafts, No bots) each put their
query into the box as one more thing that must hold, an OR in parentheses
(`togglePart`); a pressed one (ticked, `aria-pressed`) takes it out again.
They are pressed while every part of their query must already hold as written
(`hasPart`), and disabled while the query does not read. A small **Filter
syntax** link beside the "What it catches" heading opens the docs' Inbox page
at "Filter what you see" (`DOCS_URL` + `inbox/#filter-what-you-see`). A
switch shows or hides the inbox's count in the sidebar; its pull requests still
count in the Inbox total. **Delete inbox** asks, then
deletes with Undo and goes to the first inbox left with focus on its row in the
sidebar; with only one inbox it is disabled and the page says "Keep at least one
inbox".
The right-hand preview is "Would show right now" with the same unsnoozed
items (`queueItems`) and `matches` as the sidebar's count, grouped like the
queue with Not waiting on you last, each row with its
repository and tags of the other inboxes it is in. A change that alters the
matches says "N added", "N removed" or "N added · N removed" (a zero half is
left out) for four seconds, in a live region that is always on
the page and empty otherwise, the removed rows struck through and fading out
  over the last moments (no fade with reduced motion). An unknown id says "This inbox no longer
  exists" and links to Inbox, which opens the first inbox.
- **Manage** (`?page=inboxes`): the inboxes in the sidebar's order, each with
  its colour dot, name (a link to edit it) and how many are in it now ("count
  hidden" when its badge switch is off). Its menu holds
  Edit…, Move up and Move down (each only where the inbox can move) and
  **Show count in the sidebar**, an on-or-off choice; a save sends the whole
  list in its new order. It has no delete: an inbox is deleted from its row in the
  sidebar or its edit page, neither of which deletes the last one.

Pull requests (`?page=pulls`, `src/inbox.tsx`) lists every open pull request
of the picked repositories, with or without a local clone, with Risk,
Pull request, Review time, Review and Status columns. The pull requests of a
repository without a local clone are listed from discovery (its entries have
`needs_clone` and no clone); a review needs a local clone. Risk is the latest
saved review's score as a coloured pill (a lower bound reads `≥`). Review time
is that review's reading estimate in whole minutes. For staged PR reviews,
it counts prose at 400 words a minute, rounded up within 1–240 minutes;
code excerpts add no time. Review is GitHub's review
decision with a count of unresolved threads beside it. Status combines checks
and merge state into the one that stands first between the pull request and
merging: a conflict, failing checks, a draft, running checks, ready, blocked,
then passing checks; its tooltip names both. GitHub state and unresolved
thread counts also appear for pull requests without saved reviews. Normal or
unknown states (no score, no estimate, no GitHub review, no unresolved
threads, nothing reported) leave the cell empty and are read only by screen
readers. A review being generated shows the nine-dots loader under the title.
Discovery fetches
open pull requests with paginated GraphQL queries per repository, including
review decisions, head checks, merge state and the first 100 review threads;
when more threads exist the unresolved count shows a lower bound (`N+`). Most
polls read only the pull requests updated since the last one and merge them
into the cache; every open one is read again at least every ten minutes (see
`internal/discovery`). The
columns follow the hosted Focus view's labels, icons and tones and fold under
the title below 72rem. The list is drawn as the Up next design's live table,
with the shared palette on the app's type scale. The page bar says
when the list last read the daemon ("Updated just now", then how long ago,
with the exact time as a tooltip), with a turning mark while a refresh runs.
A row's menu (its ⋯, a right click or ⌘.) holds Peek, Merge… on a ready row
or the fix it offers, the fix's transcript, Generate or Regenerate review,
Select and Open on GitHub, each with its key. Peek (or `Space` on the focused row) opens
the saved review under the row, read when first opened and again only for a
new version: the summary (the row already shows the risk score), the first four sections in review order with their lines added
and removed, and **Open the review**. Without a saved review it says why (no
clone, being written, failed, none yet) and offers **Generate the review**
where it can run. A row whose state (review, checks, merge state, threads,
score, estimate or version) changed in the latest read flashes once. The keys that work on the list are
in the shortcuts sheet (`?`). Generating and failed reviews remain visible under the
title, with agent sessions in progress beside them. Incomplete risk assessments show a lower bound (`≥N/5`) with an explicit
“at least” description and caution styling rather than a reassuring low-risk
colour. Collapsible groups say who acts next, with each row in one group:
Review requested; the person's own pull requests as Needs changes (changes
requested, failing checks or a conflict), Ready to merge (GitHub's merge state
is ready) and Your pull requests (the rest); Other pull requests; and Saved
history. Its tabs are **Open**, every open pull request, then the saved tabs,
then **Closed** (`filter=history`), the saved reviews of pull requests closed,
merged or no longer listed, always last; a tab with nothing in it is dimmed,
and h and l walk them in that order. Without `filter=` the page opens on the
tab last shown in this browser, else Open. The filter (see above) narrows Open
and Closed and their counts. Saving it there is **Create tab**, which asks for
a name and saves a tab of that view and query (`filter=tab:<id>`, at most 20,
in `ui/filters`); the query moves into the tab, so the view's own filter is
cleared. On a saved tab the menu starts from its query, its values ticked,
and a change is a draft until **Update tab** saves it into the tab (the arrow
offers **Create tab** from it too). A right click on a saved tab offers
**Edit…**, which opens the menu with the tab's name and **Done** at its foot,
and **Delete…**, which asks (only the tab goes; its pull requests stay under
Open or Closed), then returns to Open or Closed; an unknown tab opens Open. A
link to Pull requests with `repo=` (Analytics and Timeline keep their own
`repo=`), once the saved filters are read, replaces the page's filter with
`repo=<that repository>`, and the address drops `repo=`.
Collapsing keeps selections, and Select all selects only currently expanded
visible rows. The views, the filter and saved tabs do not discard cached
reviews. Unknown team membership
remains explicit. Refresh events share the active read and queue one follow-up,
so a completed inbox snapshot is shown even while more updates arrive.
Cached discovery keeps the inbox readable when refresh fails;
one notice covers every repository GitHub could not refresh, says whether
GitHub is disconnected or needs attention, and links to Settings' Connections; another
covers an exhausted GitHub allowance. Neither appears merely because the cache
is old. Refresh asks
GitHub through the local server, which gives up on a repository once GitHub
has not answered for 45 s and answers it as not refreshed (stale, with
the error, and with whatever it read before), which the app names in the
refresh's alert; the app gives up on an answer after 10 minutes and names
the repository as one baum did not answer for in time, so a
connection that drops mid-refresh cannot leave the loader running. While anything is selected a dark bar over
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
rebuilds the inbox and jobs. Jobs appear newest first and can be cancelled. The app's
global jobs are `GET api/jobs` without parameters, the current jobs (every running, queued and
unseen failed job, and the latest of each kind for each pull request, group or repository), which
is all `reviewJob`, `detailsJob`, `fixJob`, `groupJob`, `buildJobOf`, Up next and the tray need. The PR
page reads its own pull request's jobs (`GET api/jobs?repository=&pr=`, again whenever the app
reads its jobs) for `fixJobs` and `chatJobs`; Inbox and Up next find `t`'s fix transcript among the
current jobs, so there it is the latest fix's. The Jobs page lists the running jobs from the current
jobs and finished ones a page of 50 at a time (`GET api/jobs?limit=&before=`, **Load older jobs**),
starting again from the newest page when the jobs change more than a page since; a transcript page
for a job the current jobs lack reads it with `GET api/jobs/{id}`. Existing reviews stay readable
while regeneration runs. A PR page uses the queue’s active job for that
repository and PR across registered clones, showing queued/running progress
and cancellation even before its first document exists. Duplicate generation
controls stay disabled until that job finishes, and completion/cancellation
updates the page through SSE.

A review of one pull request is written in stages (outline, each section's
details, then the codebase context), and the daemon holds a draft while the
job runs (`Job.draft`, read from `GET api/jobs/{id}/draft`). A page with no
saved version shows the draft in the viewer as soon as there is one, with
sections still being written reading "Writing details…" and a notice that it
is a draft; before that it keeps the facts view. A page with a saved version
keeps showing it until the new one is saved; it offers no view of the draft.
A draft is never a version: no version menu, Ask or
retry, and nothing is read or written under its number. A version published
without some sections' details or without its context says so in a notice
("2 sections couldn't be written.", "The codebase context couldn't be
written."; the context notice only when the job that wrote it recorded the
failure, since older reviews have none). The latest saved version's failed
sections have **Retry**, which queues a details job (`POST api/jobs` with
`kind: "details"` and the version) that writes all its failed sections again
and updates the same version in place; meanwhile they read "Writing details…".
If the pull request has new commits (`head_changed`) the page says the details
can't be retried and offers **Regenerate**; a details job that ends cancelled
or failed shows why until dismissed. Repository settings, including automatic reviews, decision capture, backend and model overrides, are shared across registered clones.
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
the members of expanded groups. The group row shows the group's risk, its name
with a "Group of N" badge, "repository · #n, …" and, when it applies,
"Generating group review", "Group review outdated" or "No group review yet". A
stack group, which baum made for stacked pull requests, shows the hosted
list's stack icon and member count before its name instead of the badge, and
"Stack" instead of "Review group" on its page.
The group page (`?group=<id>`) is the shared viewer with the group's name as
heading, member labels on sections, and, folded to a line under the title ("7
pull requests · 4 merged", landed members counted), the members (state and
their own review status, each linking to its page) with Rename, Add pull
request, Remove (never below two members, nor above eight) and Delete group;
the line opens and closes them and starts closed.
Its rail has the Decisions, Sessions and Commits rows of a pull request's page, aggregated from the members' own saved reviews (the daemon has no group routes for them): a session linked to several members is listed once, commits are listed once, oldest first, and a commit's detail or a session's transcript is read through a member whose review linked it; sessions in progress on any open member are marked.
Deleting never deletes saved reviews. Generating needs an available clone of
the repository, and the group review builds on a saved review of every member:
Generate queues the reviews of members without a review of their current head
with the group's (members already being reviewed are not queued again), and
the group review starts after them even if the person leaves the page. It also
waits while a member is being fixed, and when a member's head moves while it
waits, it reviews that member again and then the group, at the new heads. Meanwhile the page says "Reviewing the pull requests first",
the members' rows show a loader, and Cancel stops the group's job. If a
member's review fails, the group's job fails with that reason and Try again
starts over. A fork or an author without
write access asks for the same confirmation as a pull request. An outdated
group offers Regenerate. A pull request's page links the stacks and groups it
belongs to on the line under its title, after the repository and the time to
review (the viewer's `header.subtitle`), each as its kind and name ("Stack ·
Treat local Entire checkpoint sessions as candidates"),
and the Jobs page tags a group job "Review group" above its name. Groups across
repositories are not supported yet.

The sidebar keeps plan usage in view above Jobs and Settings: per provider,
each window (for example 5-hour and weekly) with the share left, when it
resets and a thin bar coloured by what remains, and how old the oldest figure
is; hovering "resets in" shows the exact date and time. A narrow window folds
it to the lowest share left. Only
plans the daemon could read show: a provider that is not installed, signed
out or has not run yet is left out, and with neither readable (or a failed
request) the box is not drawn. Which plans show is a choice in Settings
(below): by default only those of the backends baum uses. The desktop setup
shows no plan usage. The sidebar asks
`GET api/plan-usage?refresh=1` on mount and every five minutes while the
window is visible (the daemon reads again at most once a minute), and works
out "resets in" again every 30 seconds between readings. Codex figures are live when Codex is signed in with
ChatGPT; otherwise they come from its last session and, once older than 15
minutes, say how old they are.
See the daemon's [plan usage README](../internal/planusage/README.md).

The app's sidebar leads to the saved inboxes under an Inbox heading, Sessions, Timeline, Pull requests, Analytics, Repositories, Jobs
and Settings; the Inbox heading shows how many things wait on the person there (a heading, not a link; its inboxes are nested beneath it), and Sessions shows the number of agent sessions waiting on the person (in the caution tone), or
else the number working now. The sidebar and the page's panel stay in place:
the panel's notices and each page's bar (`PageBar`) stay at its top, and only
what is under them scrolls (`.local-main`, which pages scroll through
`pageScroll` rather than the window); pages with panes of their own (the Inbox,
Settings, the Timeline) scroll each pane instead.

The Timeline page (`?page=timeline&repo=&tab=all&q=&person=&file=&from=&to=&status=&category=&feature=&open=`,
`src/timeline.tsx`) is the hosted Timeline over the local decision log. It reads
the daemon's `api/timeline` routes (see the
[decision log README](../internal/decisionlog/README.md#timeline-api)) and draws
them with the hosted Timeline's own page, the viewer's `TimelinePage` (see the
[viewer README](../viewer/README.md#decision-log)): the tabs, lists, paging and
empty states, the opened decision and feature and the address they live in are
the same code. The app supplies the lists, details and authors, its own bar
in place of the shared controls, the lines above each list, the empty lists'
wording and the session a decision opens. It shows every picked repository whose decisions are
on, as the hosted Timeline shows all of an organisation's, or those named by
`repo=` (repeated). The page's name, its bar and the tabs stay at the top of
the panel; only the open tab's list scrolls, and the opened feature's panel
stays beside it. The bar holds a search box for the text (`q`) and the
Inbox's filter menu and query (`FilterButton` and `FilterBar` from
`src/filter-menu.tsx`, with the Timeline's fields from
`src/timeline-filter.ts`): `repo=`, `person=`, `file=` (whose values follow
the path typed, from `api/timeline/files`), `date>=` and `date<=`, on
Features `status=`, and on All decisions `who=` (`who!=agent` hides the
agents' own decisions), `category=` and `is=superseded`; any other word is
the text. The query is written into the address the shared page reads (`q`,
`file`, `person`, `who`, `category`, `from`, `to`, `superseded`, `agent`,
`status`) and read back from it. The server applies the filter to each page it
reads, so every term must hold and each field is named once (`who=a,b` for
either): OR, `-` and parentheses are refused with a reason, and the last
filter stays. The sidebar, its key and
Go to open it on human decisions (`who=human`), as the hosted rail does;
clearing the filters there shows every decision. The app supplies its
file and folder suggestions from `api/timeline/files` and its authors from
`api/timeline/people` on All decisions and `api/timeline/features/people` on
Features (the user is one person, whichever address or login names them),
and sends the filters as the viewer's `decisionQuery` and `featureQuery`
read them, with the repositories. A file picked from a decision's detail
filters the tab shown. Everything lives in the address. Over several
repositories each row names its repository and links its pull requests to
that repository's PR page. Features groups them by
month and day of last activity; on a screen 96rem wide the opened feature sits
in a panel beside the timeline, otherwise it opens in place. A feature merged
into another opens the survivor. All decisions lists the current decisions by
day, with a line of what capture made of the repository's sessions (read,
skipped by reason, being read); a decision opened by address but not listed
shows above the list. A decision a later capture replaced opens with a line
saying so. A decision's detail shows the prompts it cites, read from the
session's transcript on this computer, or says "The session's transcript is
no longer on this computer." once it is gone. **Open session at turn N**
opens the session in the detail, at that turn, ended or not
(`api/timeline/sessions/<agent>:<id>/transcript`): the same card the Sessions
page draws (`TranscriptCard` in `src/session-transcript.tsx`), with the cited
turn marked and scrolled to, and "Turn N is not among the prompts shown for
this session." when the transcript does not list it. A pull request links to its PR page
(`?repository=&pr=`). Opening or closing a decision or feature, and the
redirect from a merged feature, replace the history entry rather than add one,
as hosted. Only the tab shown reads its list, 50 rows at a time with a button
for more. The app's event stream counts `timeline` events and
every connection after the first; on each the page reads its repositories, list
(as many rows as it had loaded, in requests of at most 500) and open details
again. A load of more rows or a detail still under way from before is dropped,
so an older answer never replaces a newer one. A feature in the address that
the loaded list does not hold (further down, filtered out, archived or gone)
shows above the list as the opened feature. With no features yet, the page
explains that features appear as pull requests and sessions are placed and
offers **Build feature history** (in a view of one repository) and All
decisions. The feature build below belongs to a view of one repository. The
kebab menu at the end of the filter bar offers a quieter **Rebuild feature
history**: over several repositories always, over one when it has features at
all (whatever the filters). Over several it starts one build for each
repository with a local clone, and says for how many of them when some have
none; a start the daemon refuses names its repository and the others still
start. While a build of any repository shown runs, the item is disabled and
says so on hover, as it does when no repository shown has a clone. Either
starts a `feature_build` job for each clone at once, as every run the person
asks for starts (`src/feature-build.tsx`, `POST api/jobs` with `kind:
"feature_build"`, one request per repository): there is no dialog, no estimate
and nothing about the price stops it. A rebuild of a repository whose build
stopped resumes it, as **Resume build** does. The build places the merged and open pull requests of the
repository's whole history, discovering its features first when it has none
newer, and the weekly consolidation pass takes the new features right after
they become current. The button reads "Starting…" while the request is under
way; a start the daemon refuses says why in the build's own words above the
page ("Couldn't start the feature build: …"). While the job runs the Features
tab says "Building feature history… N of M pull requests placed" (from
`api/timeline/build`) and links to it. A build that stopped says why (its
version's reason, else its latest job's: one that failed, was cancelled or was
interrupted) with what it spent at API prices and offers **Resume build**,
which starts it again at once; a building version with no reason and no job
says that a build is in progress or stopped and offers Resume too, which goes
on with the same version. A latest build that ended before it made a version
shows "The last feature history build stopped before it made a version" with
its reason and **Try again**, until a version newer than the job is current or
the person closes it; closed jobs are remembered in `localStorage`, so a later
job shows again. One the person cancelled shows nothing.
Feature builds' reasons have their own words, here and in Jobs (`jobReasonText`
with the job's kind). A failed read of the build state keeps what was shown.
The current features stay listed throughout, and the lists are read again when
a build finishes. Without a local clone the build buttons are disabled. The
page also says when the daemon has no decision store, when the decisions switch
is off, and which picked repositories have their decisions turned off.

The Analytics page (`?page=analytics&days=7|30|90&repo=&agent=`, `src/analytics.tsx`)
reads `GET api/analytics` (see the daemon's [analytics README](../internal/analytics/README.md)).
Its bar holds the window (7, 30 or 90 days, as a segmented control) and the
repository and agent selects, bordered like the Timeline's filters; all three
live in the address. The page uses the shared palette as the Inbox does, and
its charts the hosted dashboard's warm series (forest, then `--data-caramel`,
`--data-sage` and `--data-espresso` from `../viewer/src/palette.css`). A new
choice shows at once and the last report stays, faded, until the new one
answers. Cards: summary tiles (sessions, active agent time, human prompts,
the cost at API prices, marked "Incomplete" when part is unpriced, and the
agent-written share of commits); agent activity per day, sessions or active time
stacked by harness or by model (each session under its main model; the top
four named, the rest as Other), with a legend that shows each harness's or
model maker's logo and a table behind "Show as a table"; a breakdown
by agent, model or repository (sessions, prompts, tool calls, failed share, active
time, tokens, cost at API prices); pull requests, yours or the team's (one
switch), created and shipped as bars side by side, then the merge rate and the
median time to merge as lines on their own axes (left and right) beneath, by day over 7 days and by week otherwise, read
from the pull requests the Timeline's feature log keeps (a repository it does
not read, with decisions off or no clone, is named as not counted); delivery
from GitHub ("Loading from GitHub…" for that card only while the daemon
searches) beside the busiest hours; plan usage over time per provider, with
how often each window hit its limit, beside the ten longest sessions, in a list that scrolls
within its card; and baum's work, only what it
did for the person (reviews that completed, fixes pushed, threads resolved, merges,
branch updates and check re-runs from the app, decision capture runs that succeeded), its
failures left to Jobs. A figure that is unknown is "—" with its reason as a tooltip and to
screen readers, never 0; a floor reads "≥". Charts are HTML and inline SVG in the
viewer's `--data-*` slots, agents in a fixed order (Claude Code, Codex, then any other recorded, such as earlier Cursor sessions, in a muted colour; the agent filter offers Claude Code and Codex only),
with tooltips on hover and focus. The page reads again every two seconds while
the daemon is still indexing or searching GitHub, then every five minutes. An
older daemon without the capability gets a note to update baum.
Settings (`?page=settings&section=…`, `SettingsPage` in `src/settings.tsx`)
shows one section at a time, listed on the left with a line on where each
stands and a dot when it needs attention; at narrow widths the list becomes
a scrolling strip above the section. The section is in the address, so a
reload or Back keeps it, and the GitHub notice's **Open settings** goes
straight to Connections. **General**, the default, offers System, Light and Dark
themes, the modifier held with `1` to `8` for the sidebar's pages, and whether
holding Option (Alt elsewhere) shows keyboard shortcut hints; each names only
the platform's key. Preferences
are read and saved through `GET`/`PUT api/settings/appearance` (`theme`,
`key_hints`, `page_modifier`), in the daemon’s SQLite registry, so they survive daemon restarts
and loopback port changes. **Plan usage** below them has a switch per plan
(Claude Code, Codex) for whether it shows in the sidebar and the desktop tray.
With no choice a plan shows when one of the backends baum uses is its own: the
reviews', the fixes' and decision capture's (Auto counts the first plan it can
read, Claude Code before Codex; the Anthropic API and Cursor have no plan). The
row says which. Flipping a switch is the person's choice and stays as chosen
when the backends change; "Use baum's default" gives it back. A change of backend reaches the
sidebar at its next reading (within five minutes) and the tray at its next poll. It is read and
saved through `GET`/`PUT api/settings/plan-usage` (`{provider, shown}`, `shown`
null to clear), and the sidebar reads again at once. Saves report failures
and keep the previous choice;
returning to a window rereads the saved preferences. Orange or red markers
beside section names identify issues needing attention. Fixes needs attention when it follows reviews whose
harness cannot fix. **Reviews** holds
the global review settings, one per row: whether reviews run automatically,
for which pull requests (authored, requested, both, or all open pull requests
in the repository), the harness and its model (a listed model or a custom
name, saved when its field is left or Enter is pressed) and the reasoning
effort (Default, which leaves it to the agent, Low, Medium or High; higher levels
selected in onboarding remain visible). The harness is Claude Code or Codex
only: the daemon refuses `cursor-agent` and `anthropic` (400 when saving, a
failed job for one already in `config.toml`), and when neither is installed
the section says so. Each change is saved
at once, with "Saved" beside the title, through `PUT api/automatic`
(`enabled`, `backend`, `model`, `effort`, `pull_requests`), which the daemon
keeps as app preferences over the config file; `GET api/settings` reports the
effective harness, model and effort. Each section sends all its choices and runs one
save at a time; changes made while one runs are sent together afterwards, so
an older choice never lands after a newer one. A failed save shows the error
and puts the control back. **Fixes** chooses the agent that fixes: "Same as reviews" (the
default) or Claude Code
or Codex with a model and a reasoning effort, sent as `fix_backend`,
`fix_model` and `fix_effort` (`""` returns to the reviews', effort included)
and reported by `GET api/settings`. A chosen fix agent applies
in every repository. In the desktop app, the agent selects of Reviews, Fixes
and Data extraction label a CLI that reports no sign-in "(not signed in)",
and while the agent a section uses is one, its row says that section's jobs
will fail and how to sign in. **Connections** holds the GitHub connection and, in the
desktop app, the Agent hooks card with installation, enablement, update
(to the latest plugin, without removing it first; shown with the installed
plugin version) and removal for each agent. **This computer** holds the diagnostics, the background
daemon note, **Jobs at once** (how many reviews, fixes and feature builds run
in parallel, 1–4 and 4 by default, saved with `PUT
api/settings/job-concurrency` and reported as `job_concurrency` by `GET
api/settings`; the daemon applies it at once, and lowering it lets running jobs
finish), **Review time limit** (10 minutes by default) and **Fix time limit**
(how long a fix, a question to a review's agent or a feature build may run, 30
minutes by default), each chosen from 5 to 240 minutes, saved together with
`PUT api/settings/time-limits` as `review_time_limit_minutes` and
`run_time_limit_minutes`, reported by `GET api/settings` and applied to runs
that start after the change (runs have no call or token limit unless the
config file sets one), in the desktop app the Command line card (inline CLI install,
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
matching remote, including an upstream remote in a fork clone, or one using
an earlier name GitHub forwards to the repository, which the picker and
Repositories rows then show (`earlier_names`); a folder of another repository
shows the daemon's error naming that repository. Settings apply
to the repository through `PUT api/repositories/{owner}/{name}/settings`, with
automatic generation and decision capture only: the harnesses and models are
global; there is no spending cap.
Removing a repository unpicks it but leaves its files and saved
reviews on disk. Change local clone offers the existing clone registrations and
lets the person remove one or register another folder. Explicit unpicks are
remembered locally across picker reopens and app reloads; picking it again is
always available.

Each Jobs row tags its kind (Review, Review group, the fix's name, Question,
Details, Feature build) beside the repository, then names the pull request by
number and, when baum still lists it, its title, cut to one line. In place of
calls it shows when the job started (`started_at`, set when it first leaves
the queue and kept across a requeue) and how long it took or has taken so far;
a job still queued, or cancelled before it ran, shows when it was queued.
Jobs saved before `started_at` count from when they were queued.

The jobs list and the PR page mark an automatic job "Started
automatically because new commits were pushed" (or "because your review was
requested", "because the pull request was marked ready for review", or
"Started automatically again" for the one retry of a run a daemon stop or
restart interrupted); it is cancelled like any other. A stack group's review,
which the daemon generates with its members' automatic reviews, carries no
such note, like a group review generated by hand. Drafts get no automatic
review, but Generate works on them as usual. `automatic` SSE events (a notice,
and every repository's status after a settings change in any window) reload
the page.
Agent sessions in progress (`GET api/live-sessions`, tracked by the daemon from
hook events, see "Live sessions" in ARCHITECTURE.md) show as a compact "Agent
running" or "Agent idle" marker on the Pull requests rows of their pull request and on
its PR page's review rail. A session is a session whether it runs or not: the
Sessions row counts and lists every session the daemon lists on the pull
request (working, waiting, or ended and still listed) together with the
sessions linked through its commits, once each, and carries a pulsing dot
while one runs (amber while one waits on the person; the list slot's
`indicator`, and the same dot on that session's own row). The Sessions pane is
one list: the listed sessions first, each as a card (agent, working, waiting
or ended, title, branch, last activity or end, Open transcript and its
decisions) that opens the session alone with its transcript read live, then
the linked ones (`pullRequestSessions` in `src/review.tsx`). A listed
session that is also linked shows as the listed one until it ends, and then
as its linked row. "No agent sessions" shows only when there are none of
either kind. The Summary keeps a "Sessions in progress" panel of the running
ones above the review. The list is updated by the `live` event, which carries no
data: the app rereads the list. Only the agent, session id, state, times, last
event kind, why and since when it waits on the person with the one masked line
its agent showed, repository, branch, PR number, the session's title and its
activity (latest tool calls with masked targets, files written, latest assistant
message; see "Live sessions" in ARCHITECTURE.md) reach the app in the list,
never a path; a session's transcript comes only when the Sessions page opens it
(below), or the PR page's Sessions pane opens it. When a session ends the page
reads its sessions from disk again, where the full session is. A daemon without the record serves
none.
The PR page keeps its linked sessions and commits visible during background
refreshes, so their rows and an open transcript do not blink back to a loader.
Switching pull requests or review versions loads the corresponding list.

Decision capture (`GET api/decisions`, see the daemon's
[decision capture README](../internal/decisionlog/README.md)) adds
"Decisions (n)" to each row of that panel: the session's decision titles
behind a disclosure, and "Updated N minutes ago", "Capturing…" or why it was
not captured, in words (Cursor Agent as the review harness cannot capture),
followed by why capture will not run again (paused, off, off for the
repository, or not a picked repository; `capture_off`).
The PR page's review rail has a **Decisions** row above Sessions and
Commits, with the count (the viewer's `decisions` slot, `?pane=decisions`):
its pane lists, session by session as cards, the decisions of every session
of the pull request, in progress or linked by the saved review, marking those
made after that review was generated "Provisional", ended sessions the
tracker still holds included (`usePullRequestDecisions`,
`PullRequestDecisionsPane`). A session's decisions are split into **Decided by
you** (the person's own, and those the agent proposed and they approved) and
**Decided by the agent**, each with its count; an empty group is left out.
Each decision is one row to scan: its title, its
category as a pill and the time it was made, with its summary cut to a line
under it; the row opens to the whole summary, **Why** (its rationale) and its
files as code chips, code spans rendered as code. Who decided is the group it
is under; the alternative it rejected is not shown. The row is not drawn while no session has
decisions, a capture state or a capture in flight. The walkthrough, which has
no rail, keeps them above it behind a closed "Decisions from sessions"
disclosure. Both read again on the `decisions`
event, which carries no data (events within 250 ms make one read), and when
the event stream opens or resets or the automatic switch changes. Settings' **Data extraction** section, in
Reviews, holds the switch for decision capture and the feature log, the agent
("Same as reviews", the default, or Claude Code, Codex or the Anthropic API)
and the model (the harness's cheap default, or another of its models; a new
agent starts over at its default) and the reasoning effort, one per row like
the review settings and saved as they change, read with `GET
api/settings/decisions` and saved with `PUT api/settings/decisions`
(`enabled`, `backend`, `model`, `effort`; the whole object, `""` for the reviews' agent and the default model, which is saved as
none so it follows the agent). Only these
two answer with what the daemon makes of them: the harness extraction
resolves to (`effective_backend`), its model and whether the saved model is
ignored (unknown, with a note that extraction does not run, when no harness
resolves); the section reads them again when the review harness changes. A saved model it ignores is shown with a note naming the harness, so
choosing the default clears it. When the harness cannot be resolved the section says so, apart
from a check that did not answer in time. A repository's dialog turns capture off for it (`decisions: false`; on sends
`null`). The desktop setup's review step (which shows `auto` as the harness the daemon resolved, else
its own guess) offers the decision log model beside the harness, reset to that
harness's cheap model when the harness changes, and says what capture sends;
the default is saved as none, so it follows the harness, and a saved model
the daemon reports ignored (`baum internal reviews show`, with the harness it
resolved) is shown the same way while that harness is chosen, and kept unless
changed.
Analytics counts the decisions captured under baum's work.

Every list page draws its list one way: Pull requests, the Inbox's queue, Sessions,
Jobs, Repositories and Manage inboxes. The page lies on the panel's warm paper
(`--surface-card`), its sections are `ListSection`s (`src/list.tsx`: a heading with a dot
in the section's tone, its name and count, folding where the list is long), and its
rows are `list-row`s spanning the panel between hairlines, with no boxed panels. The
row the keyboard is on, and the Inbox's selected pull request, take the same bar and
fill. Jobs groups its jobs under Running now and Finished, newest first in each. The
Timeline and Analytics keep their own layouts.

A row carries no buttons. Its title opens it (a pull request, a job's pull
request, a repository's settings, an inbox's editor), and everything else it
offers is in its menu (`MenuButton` and `useContextMenu` in
`src/context-menu.tsx`), opened three ways: the row's ⋯ at its end, a right
click on the row, and ⌘. (Ctrl+. elsewhere) on the focused row. The ⋯ shows
while the row is under the pointer, holds the keys or has its menu open, and
always on a touch screen; it keeps its place while hidden, so nothing moves.
It is a WAI-ARIA menu button: named "Actions for <row>", `aria-haspopup="menu"`,
`aria-expanded` while open, and Enter, Space or ↓ open the menu on its first
choice, ↑ on its last. In choice menus (including Snooze and Save), ↑/↓,
`j`/`k` and Ctrl+N/P focus choices and wrap, keeping the page's selection in
place. Inline menus focus their first choice when opened. Home and End go to
either end. In a row's menu, another letter goes to the next choice starting
with it, Enter chooses, and Escape or Tab close it with the keys back on the
⋯. An on-or-off choice is
a `menuitemcheckbox` with its tick; what stops or removes something is in the
critical tone, set apart below a separator. A row offers only what applies to
it now, each choice with its key where it has one. Choices that open more
(a path form for a clone, a job's usage, a pull request's peek) open it under
the row. Pull requests: Peek, Merge… or the fix, the fix's transcript,
Generate review, Select, Open on GitHub. Jobs: Open pull request, Watch or
Transcript, Show usage, Cancel job. Repositories: Settings…, Change local
clone… or, without a clone, Choose folder… and Clone it…. Manage inboxes:
Edit…, Move up, Move down, Show count in the sidebar. Snoozed: Bring back.
Sessions: the session menu below.

The Sessions page (`?page=sessions`) is a live feed of the same sessions, in three
sections drawn as every list (headings with a dot and a count, then rows between
hairlines, no panels). Each heading folds its section, as an inbox
bucket's does; Ended recently starts folded, and the fold holds while the app runs.
The keys move only through rows on show. The `live` event updates it and the sidebar count, and times
tick every second. The daemon keeps ended sessions for 24 hours, across restarts, and
leaves out a session that never had a conversation (no title and no activity).
Space on a focused row, in any section, peeks at the session under it (`SessionPeek`):
the row grows open, in its sand and behind its bar, the full width under the title:
"Original request", what its compaction summary (`summary`) says it was started for
without Claude Code's "Initial:" label and quotes, cut at two lines, and "Latest
response", the agent's last message rendered, each beside its label (above it in a
narrow window), scrolled past 22rem; what the user last wrote is on the row already. Space
again or Escape closes it, before Escape clears the row.

What the person does on the page (answering, replying, queueing, ending) is
confirmed in a toast: the app's own (`src/toast.tsx`; any page raises one with
`useToast()`), dark over the window's foot, saying what happened, what it was about
(the question, the command, the message) and the session with its agent's logo. It
goes after six seconds or when dismissed, is read out from one live region, and can
carry an action such as Undo; a failure shows in the critical tone.

A session card's ⋯, or a right click on the card, opens the app's own menu
(`src/context-menu.tsx`) in place of the browser's: Reply when the daemon offers it (`messageable`, with its
key r), Focus session or Show window when the daemon can bring the session forward
(see below, key f), Open transcript, Peek (Space), Open pull request when it has one, and End session when the
daemon offers it (`endable`). ⌘. (Ctrl+. elsewhere), anywhere in the app, opens
the focused element's menu (`useActionsKey`), as the Menu key or Shift+F10 do; here
it opens under the focused card. Ctrl+N and Ctrl+P (`useReadlineArrows`) move as ↓
and ↑ in every list and menu of the app, and in Go to, but not in a plain text field. In the menu the arrows move, Enter chooses, and a click
elsewhere or a scroll closes it; Escape closes it wherever the keys are and gives them
back to the row that opened it. In a field, or over selected text, the browser's menu
stays; everywhere else that offers no menu of its own, a right click does nothing
(`blockBrowserMenu` in `src/native-menu.ts`, installed by every window: the app, setup,
the tray and the connecting screen).

- "Needs you" (`SessionsPage` in `src/sessions.tsx`) shows only while something asks
  the person for a permission or an answer (`asks`), and "Waiting for you", after it,
  only while a session finished its turn or went quiet: the same rows, split by
  whether the session is blocked on a decision or only waits for the next word. Each
  lists the requests the app can answer first, then the longest wait. A session with a pending `permission`
  request (from `api/agent-requests`) gets an amber card titled by its tool ("wants to
  run a command", "wants to edit a file", "wants to use X"), the session's title and
  branch, the agent's last message, what the request would do (`code` rows in a dark
  mono block) and the buttons Allow once, one Always allow per suggestion, and Deny. A
  `question` request shows its options as numbered rows, each with its description
  under its label and "(Recommended)" as a tag (toggles and Submit for
  multi-select or several questions). Several questions show one at a time under a
  tab each, ticked once answered; a single choice moves on to the next unanswered
  question, and Back, Next and Submit sit under it. On a focused request card the
  number keys press its choices and Enter presses Next or Submit; where the card
  has neither (a permission, a single choice) Enter does nothing rather than open
  the transcript. A button disables the card and shows "Sending…"
  at once; the card says "Sent" until the next `live` refresh drops the request, and a
  409 `request_gone` shows "Already answered in the terminal". A green notice says
  what was done, and when nobody waits it reads "Nobody is waiting on you." with the
  last action. A session waiting with no request (it finished its turn, asks for
  something only its terminal can answer, or went quiet) gets a quieter card headed by the agent's
  mark, the session's title, its branch and the wait, then the opening paragraph of
  the agent's last message (at most two lines, its Markdown emphasis and code
  rendered) and the line it showed, plus "Asks permission in its
  terminal" or "Asks a question in its terminal" when only the terminal can answer.
  A session that went quiet (`quiet`: the daemon heard nothing for 10 minutes and no
  tool call of it is still running, as after an interrupted turn or an agent that
  died) has "quiet", in a muted tone, before the time since its last activity (a
  waiting one has "waiting" in amber), and no note. Rows carry the agent's logo, no
  status dot. Every card in Needs you and Running now shows the latest message the user
  typed (`activity.last_prompt`) on one line under its head, after "You". A session
  the daemon marks `messageable` (waiting at its prompt) can be replied to: r on its
  focused card, a click on it, or Reply in its right-click menu, opens a "Reply" box under it, Enter
  posts to `api/live-sessions/{agent}/{id}/message` and Escape closes it. The reply
  starts its next turn in its own terminal and the card moves to Running now; a
  notice says what was sent. One marked `endable` (waiting or quiet, Claude Code
  only) can be ended from its right-click menu, which posts `.../end`; the card leaves
  when the session's end arrives. Neither shows on a card until asked. A daemon without `api/agent-requests`
  shows no requests.
- "Running now" has one card per running session, ordered by the user's latest
  prompt (`last_prompt_at`, else its start), newest first, so the agents' steps
  do not reorder it, including one silent for a while
  whose tool call (a subagent, a long command) has not returned, or, from a daemon
  older than `quiet`, one quiet for a while ("Quiet for 12 min"): the agent, title, branch, pull request link, and the time
  since it started. From the session's `activity` it shows the opening of the agent's
  latest message on one line (its Markdown emphasis and code rendered), else, until
  the agent has said anything, the current step (the tool in its colour and its
  target); and "N files touched" before the time;
  without `activity` it reads "Active 2 min ago". r on its focused row, a click on it, or "Reply when
  it finishes" in its right-click menu, opens "Send when it finishes" while no message is queued (Enter posts a
  message to `api/agent-messages`, Escape closes it); the daemon delivers it when the session's turn
  ends, one per session, and the card then shows "Queued for when it finishes" with
  Cancel.
- "Ended recently" has one compact row per session that ended in the last 24 hours,
  newest first: the agent, title, how long it ran and the files it touched, when it
  ended.

The Codex model picker offers `gpt-6-luna` (the default), `gpt-6.1-sol`,
`gpt-6.1-terra` and `gpt-6-astra`, following the
[OpenAI model recommendations](https://developers.openai.com/codex/models).
Terra requires Codex CLI 0.159.0 or later. Other model IDs can still be entered
as custom models.

The Settings page has "Answer Codex approvals from the app" (`codex_approvals`, saved
through `PUT api/agent-requests/settings`, hidden when the daemon lacks the route):
while on, Codex hides its own approval prompt for up to 25 seconds while it waits for an
answer from the app.

A session the daemon can bring forward (its `focus`, absent once it ended) offers it
in its right-click menu, with the key f on its focused card, and as a button on
its transcript page, with the key f there too: "Focus session" when the daemon can select
its own tmux pane, terminal tab or Claude desktop conversation, "Show window" when
only the window it runs in. It posts `api/live-sessions/{agent}/{id}/focus` (see
"Live sessions" in ARCHITECTURE.md). What could not be done (a kitty without remote
control, a refused Automation permission) or why nothing could (the session's process
exited, no terminal attached to its tmux server) is a notice on the Sessions page; on
the transcript page the button reads "Focusing…" until the daemon answers, and the
note shows on its own line after the actions, a failure as an alert.

Inherited tmux variables without a recorded terminal do not identify a session's
pane. Codex's shared app server runs hooks separately from its terminal clients;
baum locates those clients by the thread ID in their terminal titles.
Connecting or updating Codex through baum automatically puts `thread-id` last
in `tui.terminal_title`, preserving the user's other title items. If an older
connection lacks it, Focus repairs the setting and asks the user to restart
the Codex terminal session and resume their conversation. No manual config
edit is needed. A unique matching tmux pane,
kitty tab (with remote control), iTerm2 session, Terminal tab, Hyprland window or
X11 window can then be selected. Shortened thread IDs need at least 24 characters;
missing or ambiguous titles produce a notice without selecting another session.
Sessions whose hooks still record their own terminal keep the existing focus path.

A session in Needs you or Waiting for you is not listed again under Running now; a session idle only
because it went silent, or with a reason this app does not know, stays there. Times
are relative where they are not clocks, with the exact time on hover. While anything
asks the person something (Needs you, `asksYou`) the sidebar's Sessions badge counts
those sessions in the caution tone; otherwise it counts the running ones, so a
finished or quiet session waiting for its next word does not raise it. The desktop app notifies when a session
starts asking for a permission or an answer (see the desktop README).

A click on a session's card (anywhere but its buttons, links, fields and reply
or answer box, and not while selecting text) focuses it and opens its peek and,
where the session takes one, its reply (a waiting session's Reply, a running
one's message for when it finishes, unless one is queued); a click on another
card moves both there. A click on a card showing both, or its peek where it
takes no reply, closes them; one showing only its peek (after Space or Escape
in the box) gains its reply. Its link (the waiting card's title, else the agent's
name), which j/k focus and Enter or o follow, opens its transcript; a click on it
peeks, as a click anywhere on the card does. For a pointer, Open transcript sits
at the right of the reply's field, or beside the latest response in a peek
without one, shown while the pointer is over the card or its peek.

The transcript opens at `?page=sessions&session=<agent>:<id>`
(`src/session-transcript.tsx`), on the Sessions page's paper. Its bar, titled
with the session's title (else "Claude Code session") after a back chevron
(Escape's hint while Alt is held), and two lines of small print under it stay
at the top of the page while the transcript scrolls: the agent, its state,
repository, branch, pull request and times, then its models, tokens, files read
and edited, lines and tool calls. The agent's compaction summary, when it has
one, is closed above the transcript. The transcript is read in one column held
to a reading measure, drawn by the viewer's `SessionTranscript` in its `log`
layout: each prompt under a rule in the app's accent, with the images pasted
with it as thumbnails that open full size; the agent named once per turn; its
messages on the way to its reply folded under "N progress updates"; its reply
in full, set as long text (headings, quieter code, spaced lists and tables). j
and k move a message at a time (prompts and replies, passing over the progress
updates), Space shows all of the marked one or less again, and Enter opens it
on its own, nearly the window's size, where j and k scroll it and Escape closes
it. It reads
`GET api/live-sessions/{agent}/{id}/transcript` (see "Live sessions" in
ARCHITECTURE.md) when it opens, 500 ms after the last of a burst of `live`
events, and every 3 seconds while the session works (15 while it waits for
you, never once it ended), since an agent writes its replies between hook
events. Each read names the version shown, so an unchanged transcript costs a
few bytes and draws nothing again; a new version keeps the turns the reader
opened (the card's source carries the version as its `revision`, so an opened
turn's activity is read again) and, when the reader is at the end of the page,
keeps them there; every session opens at its end, on its latest turn. A transcript the daemon
does not serve says why (the agent named no transcript, it is outside the
agent's session folder, the file is gone), and a failed read keeps what is
shown. The back chevron, `u` or Esc returns to the list. A reply bar stays at
the foot of the panel, at the foot of the window while the transcript is short,
while the session is not over: a reply to a session
waiting at its prompt, as the Sessions page's Reply sends it, or a message for
when a running session's turn ends, the queued message in its place once
there is one. `r` focuses it.

GitHub connection is available in Settings' Connections section and the
desktop setup's first step, which share
`src/github-auth.ts`. Sign in with GitHub starts device authorization through
the daemon and, in the same click, opens the fixed
`https://github.com/login/device` page in the browser and copies the code once
the daemon returns it (a clipboard write given a promise, since by then the
click no longer counts and the window has lost focus). Desktop setup instead
waits five seconds after the code is available before asking the native shell
to open the browser, leaving time to read the code and clipboard confirmation.
The launch does not wait for clipboard access; a failed copy offers manual
copying. Cancelling or leaving the step cancels that pending launch.
The panel observes only safe local connection status, every 300 ms while
sign-in is pending (1.5 s until the first answer) and not on a timer once
settled, with an immediate check when the window regains focus, and never while
the window is hidden. Go polls GitHub at GitHub's required interval,
even after the window closes. Denial, expiry, network failures,
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
fragment. Desktop setup opens the inbox instead. The dialog groups the
checks into needs attention, working and details, and shows this user's home
directory, which the response's `home` names, as `~`. Each warning and
problem shows its fix. Copy puts the report on the clipboard as Markdown for a
GitHub issue, with the same `~` paths. The app runs the diagnostics once when it opens and,
while they find a problem, shows a notice on every page linking to them; the dialog
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
The suite keeps browser/daemon integration for credential bootstrap in a fresh
window, namespaced requests, saved review versions, automatic generation after
a head push and its persisted settings, a live redacted transcript without a
reload, merging a ready pull request, a walkthrough comment surviving reload and
submission, agent questions with citations and saved answers, inbox edits and
Undo through the real store, and a local feature's source transcript. Shared
viewer behavior is covered by the standalone viewer and hosted UI suites;
formatting, controls, filtering and state matrices belong in unit/component or
Go handler tests. Keep browser stories focused on one risky workflow and avoid
repeating them for themes or collecting screenshots on passing runs. The inbox
fixture uses the `state/inboxes` document in its shared `app.sqlite`.
`e2e/inbox-screens.spec.ts` takes pictures of the inbox pages, light and dark,
only with `INBOX_SCREENSHOTS=1`.

Generation usage is retained as provider-reported input, output, cache-read and
cache-creation buckets, with available TTL, speed, prompt range and model-call
reports. Jobs save each update before publishing progress, including failed and
cancelled attempts. Provider-controlled model and speed strings appear only
after secret masking; progress retains numeric buckets while masking runs.
Budget tokens (the charged-token budget counter) exclude cache reads;
processed tokens include them. TTL, speed and prompt range appear only when
the provider reported them. Missing older accounting remains unknown,
and interrupted providers show reported subtotals rather than exact totals.
Saved review usage follows the version selected in history. Its cost reads
"At API prices": what the usage would cost at API prices, not what a
subscription charges. A cost is shown whenever any of the usage can be priced:
usage that is incomplete (a call still running or cut off), or that lacks a
speed, cache TTL or prompt tier, is priced at the lowest rates it could have
been billed at and shown as a lower bound ("≥") with a note naming what was
missing. Only usage with no tokens, or with no model the price list covers,
leaves the cost unknown, with a reason. Configured model identity is
distinguished from a provider report: when only the configured model is known,
its rates are used and the cost is labelled "estimated" with an explanation.
A running review is priced as its calls report usage, not only between calls
(at most every two seconds, as each update is saved): while the provider's
model and speed strings are masked, each attempt is priced at the configured
model's rates, or at the model it reported when none is configured, without
showing that string, and the exact cost replaces the estimate once masking
finishes. A run interrupted by a restart keeps its last estimate as a lower
bound. Each attempt is priced independently. A cost with `unpriced_calls` (a
feature build some of whose calls could not be priced) is also a lower bound. Pricing only reports: no budget, cap, confirmation or decision
about a run reads it.

GitHub browser coverage uses the real auth manager/controller with mocked
GitHub HTTP and credential-store boundaries. It checks device-page navigation,
clipboard access, reload/background completion, saved-account replacement and
use by generation, disconnect, and access to saved reviews. Denial, expiry,
network/scope/persistence failures and the endpoint namespace/CSRF matrix stay
in component and Go handler tests.

Linked session evidence on an open review refreshes every 30 seconds while the
window is visible, without reloading the review document. Polling allows late transcript/checkpoint arrivals
to appear; a transient poll failure retains the last successful evidence.

Live-session linkage reports `link_status` and `link_method`, with recorded
branch/upstream/HEAD evidence. Ambiguous matches have no selected PR. A
`stale` status shows no note on the session: the next lookup settles it. Recently
ended sessions can acquire a PR after ending, using their saved Git observation
rather than the current checkout. Active observations are also saved without
notification message text or local paths for restart recovery.

A session shows the PR of its agent's latest commit or push that has one,
wherever the agent ran Git (another worktree, or a branch it left), with
`link_method` `commit` or `push`, even when its directory has since checked
out another PR's branch (see "Commits and pushes" under "Live sessions" in
ARCHITECTURE.md). Its branch chip names that branch. Only a session with no
such commit or push shows its directory's PR.

A session retains up to 32 timestamped Git work observations across branch
changes; these are context associations, not asserted code contributions.
The daemon separately retains session-owned post-commit and pre-push hook
observations in the application database, beyond live-session expiry. Reviews,
bundles and feature attribution use post-commit observations as creation
links, even when transcript parsing misses the commit. Pushes alone remain
context. Existing saved hook records are backfilled on startup; failed writes
are retried without discarding the observations.

## Your review

Without the walkthrough, a reader reviews from the PR page and Up next
(`src/pending-review.tsx`, `src/pending-review-model.ts`). **Approve** is one
button with an arrow, as Snooze is (`ReviewButton`; `v` presses it): it opens
a menu of Approve, Comment and Request changes, and each opens `ReviewDialog`
on that verdict, with the others still to pick, an optional summary and the
comments that go with it; ⌘↵ (Ctrl+↵) sends. On the reader's own pull request
GitHub takes neither Approve nor Request changes, so the button is **Comment**,
straight to the dialog. Comment and Request changes can always be picked;
without comments, Send waits for a summary (GitHub requires a body for both),
and a dialog opened on one of them starts in the summary. When GitHub cannot place an
inline comment (`invalid_line`), the dialog offers to put the comments in the
summary instead. Once GitHub has the review, what was sent is cleared from the
draft and the page reloads.

On the PR page, `c` comments on the item being read (`commentTarget`): the
selected section or risk, placed on the first line its anchors cite (else the
section's first hunk), a changed file, on its first hunk, or else the pull
request as a whole. It opens the side pane's **Comments** tab (the viewer's
`comments` slot) with a box for the comment, saved with ⌘↵ and put away with
Esc. The tab lists the comments in the order written, each with its item and
place; its ⋯ (or a right click) edits or deletes one, and the tab ends with
the same Approve button. Below it, **Sent from baum** lists the reviews sent
from the app, newest first and as they were sent (verdict, when, summary and
comments, with a link to each on GitHub); the pull request's other reviews are
not listed. The latest 20 are kept, fewer when they would not fit the record.
The comments and summary are stored per pull request
in the daemon (`api/ui-state/review?key=<repository>#<PR>`), so they survive
reloads and new review versions. `pendingSubmission` posts the review on the
pull request's current head: a comment goes on its line only when it was
placed on that head; the others, and general comments, go in the body under
the summary with their item's title and place.

## Durable reading state

`GET` and `PUT api/ui-state/inbox` store the Inbox's local-day seen/cleared
record, time snoozes and head-based snoozes. `GET` and
`PUT api/ui-state/walkthrough?key=<repository>#<PR>@v<version>` store seen stops,
per-stop draft comments and the overall comment, and
`api/ui-state/review?key=<repository>#<PR>` the comments and summary of the
reader's review until it is sent. The key is URL encoded;
`src/api.ts` keeps these routes under the authenticated session namespace.
The backend contract is in [`internal/uistate`](../internal/uistate/README.md).

Each record has its own revision. The window shares a hydrated record across
the walkthrough and Ask panel, queues changes while a read or save is in
flight, and rebases semantic changes on a conflict. A stable client/sequence
mutation ID makes a retry safe even when a save committed but its response was lost. An Ask
answer appends to the current draft; Undo removes only its suffix while no
later text follows it. Neither operation sends the draft to GitHub.

Sidebar folding and viewer panel/tab layout remain presentation preferences
in browser storage. Durable inbox records and review drafts do not use or
import legacy localStorage values.


Codex hook readiness is separate from plugin installation. Onboarding and
Settings show Connected only after Codex reports all baum hook definitions
enabled and trusted. Untrusted or changed hooks show Needs approval in Codex
with `/hooks` instructions; disabled, missing and unverified hooks have
distinct states. Setup may still be finished with an unfinished connection.

The bottom Settings link has a caution badge and an accessible description
while Codex session reporting needs attention. Opening Settings shows the
reason and Check again. The app reads `GET api/agent-hooks/codex` on load and
on window focus (at most once a minute), and every minute while visible only
while the last answer is a warning. Check again and the reads after native
Settings actions pass `?refresh=1`, because the daemon caches a healthy answer
for five minutes (warnings are always read fresh). A
re-read that returns the same answer leaves the state, and so the page,
untouched. The warning clears after a successful ready result; a failed
request retains the previous warning. Older daemons without this endpoint show
no new global badge. Onboarding rechecks on focus or Check again. Checking never trusts a
hook, restarts Codex or starts a conversation.

In the desktop app the same badge and description also cover a missing baum
CLI: one the app could install but hasn't (a CLI the app can't install on this
system raises nothing). The app reads `setup_state` on load, on window focus
and after native Settings actions. While the CLI is missing, every Settings
section opens with a banner whose Install the CLI button goes to This computer
and scrolls to the Command line card; the section list marks This computer
with a caution dot and "baum CLI not installed", and the card itself is
outlined in the caution colour with its install button promoted to primary.
No agent with baum's hooks installed (an unreadable hook state, overall or for
any one agent, does not count)
gets the same treatment: the badge, a banner whose Connect an agent button
opens Connections at the Agent hooks card, a caution dot and "No coding agent
connected" on Connections, and the card outlined with its Connect buttons
promoted to primary.
While the app checks the agents (on opening and on Check again), the Agent
hooks card keeps its Claude Code and Codex rows and shows Checking… beside each
name in place of its state.

The Sessions page shows a notice while the baum CLI is missing or no agent has
baum's hooks installed (an unreadable hook state, overall or for any one agent,
counts as neither), since the
hooks run `baum` from the PATH and report nothing without it. It links to
whichever fixes apply: Connect an agent (Settings, Connections) and Install
baum CLI (Settings, This computer).

On an inbox and Pull requests, `/` opens the filter with Add filter focused.
Escape closes the filter without leaving a focus ring on its button.
The query box appears only with an actual filter and has no placeholder.
Organisation option counts show each organisation’s matching pull requests.
On an inbox, Command+Enter or Control+Enter creates an inbox from the current filter.
When creating an inbox, Command+Enter (Control+Enter on other keyboards) saves it.

While the filter menu is open, Command+C or Control+C clears its filters and
returns focus to Add filter. Holding Option/Alt reveals the filter, Clear (where shown) and
Create inbox button shortcuts.

New inboxes choose a colour not used by another inbox, preserving their template’s
preferred colour when available; once all colours are used, that preference repeats.
Shift+J/K or Ctrl+Down/Up moves between the saved inboxes in
sidebar order, stopping at the first and the last. Typing fields and dialogs keep their keys.
Ctrl+K always opens Go to.
Enter is drawn as ↵ in button shortcut hints.

On PR and stack review pages, `]` toggles the right-hand Details/Activity
sidebar, or its sheet on narrow windows. It is listed in the review shortcut hints.
Opening it with `]` gives it the keys: `h` and `l` then step through Details,
Activity and Comments, until Esc (or a click in the review) gives them back.

The page shortcut bar labels Shift+J/K as sections and lists only page actions;
Go to (Command/Control+K) and help (?) remain available globally.

J/K moves through the Summary’s text, context, intent, risk score and change/risk
items as well as other review sections. Generate has no keyboard shortcut; use
the visible Generate/Regenerate action or its menu item.

Enter on a Summary item with a section jump link opens that section, just as
clicking its link does.

Session PR links also match cached merged or closed PRs by their exact head
commit in the same repository, so merging does not by itself hide the PR.
Branch names alone never recover a closed PR, and ambiguous heads stay unlinked.

Session PR links are compact numbered pills: green for Open, purple for Merged
and red for Closed, with status in the tooltip and accessible link label. If the daemon omits session PR state, the UI uses
its matching indexed PR’s known state. Only missing metadata uses a neutral pill with a Status unknown tooltip.

Transcript continuation summaries and standalone system reminders are folded
to a short label by default. Expand them to inspect the original text and activity.
