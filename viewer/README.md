# viewer

`@baum/viewer` is the React package that renders a `baum.review/1` document
and its earlier versions: the summary, the sections and their blocks, risks,
tiles, hunk evidence, description images, checks, the details and activity
panes, and the version menu with comparison. It also holds the pure
derivations behind them (review order, risk triage, anchor resolution, version
changes) and the design tokens and primitives the components are built from.

The package is private for now. Hosts consume it as TypeScript source through
a `link:` dependency and compile it with their own bundler; there is no build
step. It has no dependency on any server: it reads only the document and the
versions a host hands it. `ReviewViewer` composes the components into a whole
review page, and a host fills its optional slots with features of its own.

The document contract includes optional `index.validation`, `stats.validation`,
and per-session gaps, set-aside failures and extraction-unavailable reasons.
Older documents omit them. Rendering the dedicated Validation part is separate
work; these additive fields do not change existing review rendering.

## Using it

React, React DOM and the Markdown, sanitising, syntax highlighting (`shiki`),
validation and pluralisation libraries are peer dependencies (see
`package.json`), so the host and the viewer share one copy of each. A host that links the package should resolve
those imports from its own `node_modules` (in Vite, `resolve.dedupe`).

Modules are imported by path, for example
`@baum/viewer/components/review-section` or `@baum/viewer/review-document`;
`package.json` lists every entry point.

Styling uses Tailwind CSS v4. Import the viewer's tokens after Tailwind and
add its sources so the utilities its components use are generated:

```css
@import "tailwindcss";
@import "@baum/viewer/viewer.css";
@source "<path to this package>/src";
```

`viewer.css` defines the type scale, the colour ramps and semantic tokens
with their dark variants (applied under `:root[data-theme="dark"]`), the
categorical series, and the pane and skeleton animations. The sans-serif
family comes from `--viewer-font-sans`; set it on `:root` to restyle the
viewer. Without it the viewer uses Inter
(variable weight, Latin and Latin Extended), bundled as woff2 in `src/fonts/`
with its SIL Open Font License (`src/fonts/OFL.txt`). The host's bundler
resolves the files from `viewer.css`, and a browser downloads them only when
a page renders in Inter, so a host that sets the variable never fetches them.

## Host seams

The viewer never fetches anything and never decides who may see a document.
A host connects it through props and context:

| Seam | Required | How |
| --- | --- | --- |
| document source | yes | the `document` prop, and `loadVersion` (`LoadReviewVersion`), which answers a `ReviewVersionState` for an earlier version |
| navigation | yes, for `ReviewViewer` and the selection and version hooks | `UrlStateProvider` with the URL's search params and a writer; `ViewerLinkProvider` with an in-app link component, which `ViewerLink` renders (a plain anchor without one), such as the head's back link |
| attachment URL | no | `AttachmentUrlProvider` resolves an attachment id to an address; without it `images` blocks draw nothing |
| hunk patch source | no | `HunkPatchProvider` loads a file's patch for cited code whose stored excerpt was omitted, given its path, the review version and the cited hunk's id |
| file opener | no | `ReviewFilesProvider` opens a cited file in the host's own diff view instead of following the link |
| row actions | no | `checks` on `ReviewViewer`, or `renderCheck` on `ChangesOverview` and `RisksOverview`, puts a host's read mark or approval beside each row |
| side pane, header slots and other features | no | the `ReviewViewer` slots below |

## The review page

`ReviewViewer` (`@baum/viewer/components/review-viewer`) renders a whole
review: the head, a single version row combining the menu, generation status and evidence age, with comparison, the rail (Summary,
Changes, with warning icons on sections containing high or critical risks), the pane for the
selection, and a side pane with the document's Details and Activity. It keeps
the selection and the version being read in the URL through
`UrlStateProvider`, and remembers the side pane's tab, fold and width, and
the rail's width, in `localStorage`. Both the rail and the side pane resize
from the border they share with the pane, by pointer or keyboard; the rail
starts at 264px.

Changes hides obsolete sections by default, in both the rail and the overview.
A small “View obsolete changes” control reveals them with muted gray styling;
they do not count toward read progress. Opening an obsolete section by a direct
link reveals the obsolete rows so the selected section stays visible in the rail.

Below `xl` (1280px) the panes stack, and the rail becomes a sheet from the
bottom of the window. A bar sticks over the pane in its place: it names
where the reader is ("Behavior changes, 1 of 5"), opens the sheet, carries
the host's read mark for the section on screen, and steps to the previous
and next place in reading order: the Summary, then each section as the rail
lists them. The foot of the pane offers the same steps, so a reader who
reaches the end of a section goes straight on to the next. A step lands the
new place at its top, under the bar. The side pane is a sheet too, nearly the
window's height, opened from the bar's last button (named after the pane,
and flagged while CI is failing or a review thread is unresolved) rather than stacked under the review, so
the page ends where the review does. Going anywhere, from either sheet,
closes it.

```tsx
<UrlStateProvider value={{ search, update }}>
  <ReviewViewer document={document} versions={[2, 1]} loadVersion={load} />
</UrlStateProvider>
```

With the document alone it draws nothing a host would have to supply: no
version menu without `versions`, no read progress, files, sessions, commits
or chat. Every other prop is optional, and a feature whose slot is absent is
left out rather than shown empty or failed:

| Prop | What it adds |
| --- | --- |
| `versions`, `loadVersion` | the version menu: read an earlier version alone, or compare it with the current one. The loader should cache, as section panes reuse it for their "Since v1" links |
| `history` | the host's own `useHistoryView` result, for a host whose requests follow the version being read; otherwise the viewer keeps its own |
| `versionsFailed` | a retry for a version list that could not be loaded |
| `header` | the head's live pull request facts (title, state, repository, branches, size, link), a back link, an approval state, a statistics line and the generation state; each falls back to what the document recorded. The head also states the document's `summary.review_minutes` as "~N min to review" beside the repository, and nothing when it is absent or null or the head has a `heading`. The change-kind tag sits beside the full page title, before its state and approval badges. `titleAside` adds a status beside the full page title. `heading` titles a review of something other than one pull request, such as a stack's: the document's own pull request number, link and state are then not shown |
| `placeholder`, `asidePlaceholder`, `fallbackSections` | what the pane, the Details and Activity tabs and the rail show while a host has no document |
| `checks` | read marks or approvals beside each section and risk on the current version, whether each row counts as read (the rail's progress), and notices above and below the rail's rows |
| `renderSectionAside` | a strip at the top of a section's pane on the current version, such as who approved it |
| `renderSectionLabels` | labels at the end of a section's badges, in its pane and in the Changes overview, on whichever version is read, such as the pull requests a stack review's section covers |
| `notices` | alerts at the top of the pane, such as a generation in progress |
| `files` | a Files group: the host's tree in the rail, under a row that writes how many files changed, over a bar of how many are viewed, and its list of file diffs in the pane, with code links in the review opening the cited file there. `?pane=files` lists every file folded; `?pane=file&file=<id>` hands `renderFiles` that file to unfold and scroll to, whether the reader clicked it, followed a link or went Back. With `tracksViewed: false` the rail counts no progress through the files |
| `sessions`, `commits` | rail groups listing the host's linked sessions and commits, each with the host's pane; `sessionsSlot` and `commitsSlot` build them from plain data (see below) |
| `conversation` | the side pane's first tab, given where the reader is (section, risk, version) and a way to open a file at a line. Without it the side pane holds only Details and Activity, and nothing is labelled as a conversation |
| `detailsChecks` | the Checks part for the document the side pane describes, in place of the document's own checks, and whether it shows a failure (which flags the tab that holds it); null content leaves the part out. The part leads the Activity tab, or the Details tab when `hideActivity` is set. For checks gathered from elsewhere, such as a stack review's members |
| `activityMembers` | a stack's member reviews for the Activity tab, for the document it describes: each with its number and a tag (such as a link to its page), whether some are still loading, and a note (such as which could not be read). Under the checks the tab then gathers the members' open review threads, each tagged with its member, their exchange figures summed with no full exchange, their sessions once each, and their decisions tagged with their member (optional `decisions` carries snapshot `items` and their `version` to keep text and links pinned to the stack snapshot when member evidence is live); an open thread in any member flags the tab |
| `hideActivity` | leaves out the side pane's Activity tab, and the Checks part moves to the head of Details; a reader whose last tab was Activity sees Details |
| `focusConversation` | puts the cursor in the conversation's text box when the reader asks to discuss the review |
| `hunkPatches` | the hunk patch source for the section and risk panes |
| `onVersionsShown` | called with the versions whose documents are on screen whenever that set changes, for example to record that a version was read |

CI uses the optional `review.checks_head`, falling back to `review.head` for
older documents. Refreshing the current document can show latest-head checks
while generation runs; earlier-head runs remain indexed for source anchors but
do not count toward the displayed CI state. Historical versions stay frozen.

The Summary header omits the duplicate update time, review version and generation
time line. Its Key changes are links with line counts, without expandable
behavior details. Its Risks section appears only for active high or critical
risks. Risk links open the complete Changes section and highlight the risk in
context; the rail has no separate Critical risks group. Older risk-overview
links open Changes. Changes are ordered by review score, with attention items
first when scores tie. The Changes count excludes obsolete sections, which are
hidden by default and can be revealed with “View obsolete changes.”

The document's `summary.context` sits under the Summary's overview, after the
breaking change and release note and before Intent and instructions, as a
Context part that starts collapsed; the reviewer expands it. Each part it
holds is labelled, in order Area, How it's reached, Contracts and Related
code, with its Markdown text and its references as chips (the path, then
`· symbol` when one is named). A chip opens the review's file view when the
pull request changes the file, and otherwise GitHub at the reviewed head, or
at the merge base for a file or directory the pull request deletes or renames
away: every hunk on or under the path removes a file or moves it elsewhere.
The host records rename sources in `index.hunks[].old_path`; older documents
without that field retain the head fallback for rename sources. A surviving
new path, including one removed and added back by a stack, still links at head.
New stack documents also carry `index.file_comparisons`: each member's base
revision and complete file changes, in member order, independently of the
hunk index's repeated-ID deduplication. Context links follow each path's final
state. A path renamed away or removed opens the member base that contained it;
a path still present opens the stack head. Within one comparison all removals
apply before surviving destinations, so simultaneous renames remain present.
Directory references use the final states of their indexed descendants.
Older stacks without comparison history retain the original fallback.
A document without a context,
generated before it existed, shows no Context part. Stack reviews show theirs
the same way.

Attention uses a flat callout with evidence directly below its risks. For a
single referenced risk, its explanation replaces the overlapping attention
summary. Category labels are omitted and lifecycle labels use quiet text
(“Since v1”). Attention includes referenced high and critical risks only.
Medium and low risks appear in Risks after Key decisions even when attention
references them; a callout referencing only lower-tier risks is omitted.

The Author pane groups models under their harness, in small text without a
second icon. Only the first model is shown, followed by the number of additional
models; hovering reveals all model names.

Key decisions omit source chips (filenames and prompt numbers).
Multiple decisions are collapsed to their short titles by default, with full
text as a fallback for older reviews. A single decision is expanded without
a toggle. Opening one reveals regular-weight decision
text and a quieter rationale below. Session quotes appear directly below the explanation;
rejected alternatives are omitted.
The Activity panel places decisions above linked sessions, whose cards use compact
spacing and a single muted title/branch line. It uses each decision's short `title` when available. Choosing
one opens its Changes section, expands the decision and briefly highlights it.
The URL's `decision` parameter preserves that destination when arriving from
another page or reloading. Older documents
fall back to `text`. Stack decisions use the same compact rows, with a quiet
member number and a link to that member's exact review version, section and
decision, where the decision expands and highlights on arrival.
Repeated decisions with identical text, source sessions and attribution appear
once in a stack, linking to the first member where they are referenced.
A decision item's optional `decision_id`, the host's stable ID of the
extracted decision it reports, is accepted and not shown.
Quotes with a readable session source have a quiet “View in session” link below
the quote, in the link color, leading to the cited turn. Decision and session jumps center their target in 400 ms. The visible target
highlights for 2 seconds, fading without a persistent border; background
refreshes do not cancel the highlight. A section with no risks omits its Risks group.
Key decisions and the Activity panel share two groups, without attribution
pills: decisions made or approved by a person first, then the agent's own calls.
Empty groups are omitted. An approved agent decision belongs with the person's
choices. The rationale and quoted session words are plain text, never Markdown.

The document's own sessions are listed in the Activity tab; the Sessions
group and its pane need the host's `sessions` slot.

### Sessions and Commits

A host need not draw these panes itself. `SessionList`
(`@baum/viewer/components/session-list`) and `CommitList`
(`@baum/viewer/components/commit-list`) render the pane for a whole list, or
for the one item `selected`, from plain view models (`SessionView`,
`CommitView`): they never fetch and know nothing of where the data came from.
Their state is `loading`, `failed` or `ready`, and a ready list may carry a
plain-text `note` shown under the heading. A session shows its agent (name
and mark), title, branch, times, outcome and the commits it is linked to; a
commit shows its short id, author, time and the sessions matched to it, with
the method and confidence of each. Both open each other through
`actions.select`. `sessionsSlot` and `commitsSlot`
(`@baum/viewer/components/list-slots`) turn a state into the
`ReviewListSlot` the `sessions` and `commits` props take, with the rail's
labels. `SessionList`'s `renderSession` (also an option of `sessionsSlot`)
puts the host's own content under a session opened alone, such as its
transcript.

### Session transcripts

`SessionCard` (`@baum/viewer/components/session-card`) draws one session, the
same borderless session container in the hosted app and in `baum internal view`:
a header with the agent,
title, date, duration, prompt count and outcome (or linked commits), the
session's main figures in four responsive groups: tokens with cost when the
host has prices, files read with edited, colored additions and deletions, and
tool calls with failures. The title leads the header, with the
harness, model names, branch pill, date and duration below. Commits appear only
in the header pill; working directory and unattributed activity notes are omitted.
Then the session appears as a conversation
(`SessionTranscript`, `@baum/viewer/components/transcript`, built from the
`components/chat.tsx` pieces below): each prompt as a bubble on the right, the
agent's text replies on the left, and a quiet line of what the turn caused
with an **Agent activity** toggle. Opened, the activity appears below the toggle in a marked region, keeping
existing replies and their height unchanged so the toggle stays in place; a prompt with no
visible activity says so explicitly. The region shows: model usage, then the agent's messages in time order with each run
of tool calls between them expanded by default and collapsible as one group.
Activity summaries align left; each call shows its input
(and output, when the host has it). A reply that is a preview is swapped for
its whole message once the activity has arrived.

An activity that carries `subagents` draws each as a card where its spawn call
happened, closed by default: the task, harness and duration in its header, a
badge when the spawn call's status says something, then the subagent's
response and its totals (models, tokens, cost when the host has prices, tool
calls, failed calls). Opening it loads the subagent's own conversation, each
instruction its parent gave it on the right and its activity below, nested
subagents included; when that conversation ends in the whole response, the
response above gives way to it. A subagent with no transcript, or a host that
cannot load one, opens onto the spawn's input. An activity that carries
`followups` draws them after the prompt's own entries: each input that came
after the prompt (a task notification, a subagent's report handed back, a
slash command, an interruption) as a dashed pill naming what it was with its
text folded behind it, as written, then what the agent did about it. A
subagent's report, message or notification is named by the input's
`subagent_task` when the host sends one, never by the agent id. A card
without a response says so; for Codex, as how Codex kept its record rather
than as a failure, and a spawn input whose message Codex recorded redacted
says so when opened. An input
that carries nothing anybody typed is not reported as missing output.

Its props are plain view models (`TranscriptSession` and the types beside it
in `@baum/viewer/transcript`, in the hosted API's field names, with the pure
helpers the card uses) and what the host adds:

- `source`, an optional `TranscriptSource` of loaders for what the session
  did not arrive with: the whole session when a card without replies opens
  (`loadSession`), a prompt's full text when a preview is expanded
  (`loadPromptText`), a prompt's agent activity (`loadTurnActivity`), and a
  subagent's own conversation (`loadSubagentActivity`, given the root
  session's run id; a rejection with `status` 404 reads as nothing captured);
- `formatCost`, which writes a cost and whether it is a lower bound;
- `renderAvatar`, the picture beside a person's prompt;
- `modelLabel`, which names models (the id by default), and
  `restrictedNote`, the host's reason a restricted session shows only that
  it is restricted (a neutral one by default).

Leaving one out turns its feature off: no "Agent activity" without
`loadTurnActivity`, no subagent conversation without `loadSubagentActivity`,
no cost anywhere without `formatCost`, no avatar column without
`renderAvatar`. Keep `source` stable, or it loads again. `focusTurn`
marks a prompt and scrolls to it; `expand` opens the card from outside.
Character counts are code points, as the hosts count a text's full length.

The `baum` binary's app (`app/`) fills both from `GET /api/sessions`: the
linked sessions and commits when the machine has them; otherwise the sessions
the document recorded (under a note giving the server's reason) and no Commits
group; and the loading state while the request is out, so a `?pane=sessions`
or `?pane=commits` link survives. A linked session opened alone
(`?pane=session&session=<id>`) asks `GET /api/sessions/{id}` for its
redacted transcript and shows it with `SessionCard` under the session: while
loading, on failure (with Retry), and with a note when the server cut it
short. The answer carries every prompt's replies and every prompt's activity,
so the card is given only a `loadTurnActivity` that reads from it, and no
cost format or avatar. `&turn=<n>` opens it at that prompt. See the "Local session linkage"
and "Session transcripts" sections of `oss/ARCHITECTURE.md` for the contract.

Wherever a session's prompts are quoted (a linked session's prompts in the
Activity tab, the prompts behind the Summary's intent), they are drawn with
`components/chat.tsx`, the pieces a host's own transcript is built from too:
each prompt is a bubble on the right with its number and, where the document
records it, its time in the corner, shown as typed with only its pasted code
fences rendered as code, and folded to a few lines with "Show all" when it is
taller. A pill marks each new day. Prompts drawn from several sessions are
introduced by a pill naming each session's agent, numbered when one agent has
several ("Codex session 2"), and each session counts its own days. Days and
times are the reader's, so they appear once the page is in the browser. A
host passes the author's name and avatar when it has them. Each prompt is
numbered `#N` in its corner, as the transcript numbers it. A prompt bubble can
instead carry an instruction one agent wrote another (`kind="instruction"`),
with the writing agent's mark (`AgentAvatar`) as its avatar. An agent reply
(`AgentText`) renders as Markdown with any HTML tags shown as written, named
by its harness or by an `author` the host gives. A
decision's evidence stays a short quote rather than a bubble: it is an
excerpt from somewhere in the session, and the document does not record who
said it.

A link to a group whose slot is absent (`?pane=sessions` without
`sessions`, say) shows the Summary, and the URL keeps what it asked for. So a
host that is still loading a group should pass its slot in a loading state
rather than leave it out: the link then opens the group once it arrives.

The loader and the version list are read by value: a host may pass a new
`loadVersion` function or `versions` array on each render without anything
being loaded again. A host whose loader starts answering for a different
pull request should remount the viewer, for example with a `key`.

The head's compact bar is `REVIEW_BAR_PX` (48px) tall and sticks to the top
of the window from `lg` (1024px). Below `xl` the navigator bar, 48px too,
sticks under it; below `lg`, where there is no compact bar, it sticks under
the host's own sticky bar, whose height the host gives as
`--review-host-bar` (0 without one). A host should pad the page's scroll by
the bars, so that a jump does not land a heading under them:

```css
html:has([data-review-page]) {
  --review-host-bar: 53px; /* the host's phone bar, if any */
  scroll-padding-top: calc(53px + 48px + 0.5rem);
}
@media (width >= 64rem) {
  html:has([data-review-page]) { scroll-padding-top: calc(48px + 48px + 0.5rem); }
}
@media (width >= 80rem) {
  html:has([data-review-page]) { scroll-padding-top: calc(48px + 0.5rem); }
}
```

The section sheet and its scrim sit at `--z-sheet` (40), over the sticky
bars (`--z-sticky`, 30).

Section attention callouts show high and critical risks with evidence and lifecycle
badges, without repeating their explanation in a nested card. Medium and low risks
appear under **Risks** below **Key decisions**. Empty risk sections are omitted.

## Syntax highlighting

`CodeDiff` colours the code in a file's hunks by the language of its path
(`src/highlight.ts`), and so does the Files pane in `ui/` (its `PRFileDiffBody`
calls `useHighlightedHunks` on its parsed hunks). Neither tokenises what it
does not draw: a `CodeDiff` file folded from its header, a closed Files card or
a large diff still collapsed behind its placeholder. Both render a line through
`HighlightedText` (`@baum/viewer/components/highlighted-text`), which wraps
each token in an inline span inside the existing code cell. It highlights in
the browser only: the server render and the first client render are the plain
text, exactly as before, and the colours replace it once they are ready, with
no change of layout. Each hunk's old side (context and removed lines) and new
side (context and added lines) are tokenised separately, so a comment or string
that starts on an unchanged line is coloured on both the removed and the added
lines that follow it. The text is never altered; a line whose tokens do not add
up to it renders plain.

- **Languages** are a fixed allow-list, picked by extension or file name
  (`languageForPath`): TypeScript, JavaScript (JSX, TSX), JSON, Go, Python,
  Rust, Java, Kotlin, Ruby, PHP, C, C++, C#, Swift, SQL, shell, YAML, TOML,
  Markdown, HTML, CSS, SCSS, Dockerfile, Makefile, Protocol Buffers, GraphQL,
  Vue and Svelte. `languageForFence` maps a Markdown fence's info string
  (`ts`, `golang`, `shell`, `yml`, case-insensitive, first word) to the same
  set. Any other file renders plain.
- **Loading.** `shiki` and each grammar are reached only through dynamic
  `import()`, so a host's bundler splits them out of its initial bundle, and
  only the grammars a page shows are fetched. The engine is shiki's JavaScript
  regex engine (no WebAssembly). The synchronous exports of `highlight.ts` do
  not load it.
- **Colours** are CSS variables, `--syntax-*` in `viewer.css`, with light and
  dark values at least 4.5:1 on the card and on the added and removed row
  tints (the lowest is 4.5:1, for comments, constants and changed text in the
  light theme on the removed-row tint). A host restyles the code by overriding
  them.
- **Fallbacks.** Diffs over 3,000 lines or 200,000 characters
  (`MAX_HIGHLIGHT_LINES`, `MAX_HIGHLIGHT_CHARS`), unknown languages and any
  failure to load or run the highlighter render plain.
- **API.** `useHighlightedHunks(path, hunks)` returns the tokens per hunk and
  line, or null; `highlightCode(code, lang)` returns the tokens per line of one
  block, or null.

`Markdown` highlights fenced code blocks the same way. A fence whose info
string `languageForFence` knows (the `language-*` class that survives
`rehype-sanitize`) renders through a small client component: the plain text on
the server and the first client render, then the coloured spans once
`highlightCode` resolves. Results for earlier content of the block, or for an
unmounted tree, are dropped, and tokens that do not add up to the source leave
the block plain. The `<pre>` and `<code>` markup and the block's text are
unchanged. Inline code, fences with no or an unknown language, blocks over the
limits and blocks holding raw HTML elements render as before. Text reaches the
page only as React text children, and colours only as `var(--syntax-*)`, so
the highlighter adds no path for contributor markup.

`Markdown` parses raw HTML into sanitized elements, as GitHub renders a
comment. With `html={false}` it skips that step and raw HTML prints as the
text it is, for prose that names tags rather than containing them.

## Schema and tile types

The document types in `src/review-document.ts` mirror
[`../schema/review-1.json`](../schema/review-1.json) by hand; the schema is
canonical. Tiles are validated against it at runtime, and
[`../schema/legacy-review-tiles.json`](../schema/legacy-review-tiles.json)
keeps the definitions of kinds the schema has retired, so stored tiles of
those kinds still render; the Go `review` package reads the same file, so
stored-document validation accepts them too. `pnpm generate` writes
the tile types to `generated/review-tile-types.d.ts` from both files, which is
not committed. `pnpm generate:validators` writes the validators to
`src/precompiled/review-tile-validators.js` with Ajv's standalone code
generation, and that file is committed. They are precompiled because Ajv
otherwise compiles with `new Function`, which a page under a strict
Content-Security-Policy (as `baum internal view` serves) cannot run. No other script
rewrites the file, so a unit test fails when it is out of date: run
`pnpm generate:validators` after changing either schema file, then rebuild the
standalone app; `just viewer-build` from the module root does both.

## Standalone app

`app/` is a single-page app that renders one review document with
`ReviewViewer`. It is meant to be served by `baum internal view`, and talks only to the
page's own origin:

- `GET /api/document` returns the `baum.review/1` document.
- `GET /api/events` is a Server-Sent Events stream. A `document` event means
  the document changed, so the app fetches it again and keeps the URL, and with
  it the selection. Other events are ignored.
- `GET /api/sessions` lists the sessions and commits linked on this machine,
  and `GET /api/sessions/{id}` one linked session's transcript, asked for
  only when that session is opened.
- `GET /api/patch?path=<path>` returns `{"patch": "<unified diff>"}` or
  `{"patch": null}` for a file the document cites, from the clone on this
  machine. The app passes `hunkPatches` to `ReviewViewer` (see `createPatchSource`
  in `app/local-patches.ts`) so cited code whose excerpt the document left out
  shows the diff when its evidence is opened. It asks once per path, only for
  the document's own version, turns a null patch, an unknown path or a body that
  is not an answer into no patch (the excerpt keeps its link to GitHub), and
  forgets a request that failed or got a server error, so opening the excerpt
  again retries. The summary shows excerpt and file counts immediately; line
  additions/deletions appear only when every cited excerpt is available, so
  missing patch data never appears as zero changes.

The app follows the system colour scheme, keeps the reader's place in the
URL's search parameters, and has no file picker, no upload and no requests to
any other host.

`pnpm build:app` builds it with Vite into `../internal/viewer/dist`, the Go
package that embeds it. That output is not committed: only a release commit,
the commit a `v*` tag points at, carries it, so `go install` of a tagged
version needs nothing but Go (see `scripts/release-commit.sh`). Elsewhere, run
`just viewer-build` from the module root before building a binary whose
viewer works; one built without it answers each page with a note saying so.
`just viewer-build` runs `pnpm generate:validators` and `pnpm build:app`, and
`just viewer-fresh` also fails when the committed validators, or on a release
commit the built app, differ from a fresh build, as CI does. The build writes
no sourcemaps, and two builds of the same source are byte-identical.

## Development

Repository checks run in Docker with `just viewer::check`, and the browser
tests with `just viewer::e2e`, from the repository root. The recipes install
dependencies in the shared `ui-test` service, which also holds Chromium, and
preserve `BAUM_DEV_CPUSET`. `just viewer::pnpm exec playwright test e2e/<file>`
runs one spec. The pnpm commands below describe in-container
commands; standalone consumers require Node.js 24 and pnpm.

```sh
pnpm install
pnpm check            # lint, format check, typecheck and unit tests
just check            # the same, through this directory's justfile
pnpm format           # format with Biome
pnpm build:app        # build the standalone app into ../internal/viewer/dist
pnpm test:unit:watch  # watch the unit tests while editing
pnpm test:e2e         # browser tests of the standalone app (Playwright)
just e2e              # the same, through this directory's justfile
```

Unit tests use Vitest and sit beside the code they cover; fixtures live in
`test/`. The tests run with the locale forced to en-US, since the formatters
follow the runtime locale. `src/components/review-viewer.test.tsx` renders
`ReviewViewer` with the schema's example document
(`../schema/review-example.json`) alone and with an earlier version to
compare. `app/app.test.tsx` renders the standalone app against a mocked `fetch` and
`EventSource`. `src/typography.test.ts` fails on a font size outside the type scale
in `viewer.css`, and `src/dependencies.test.ts` on a shipped module importing
a package that is not a peer dependency.

### Tile gallery

`gallery/` is a dev-only page for working on a review tile against real
payloads, with no model call. It renders every tile it finds with the same
`ReviewTiles` the review page uses, each beside the viewer's own verdict on it
(the compiled tile validators, then the code-source checks that need the
document), where it came from, and a filter by kind and source. It is served
by Vite's dev server, with hot module replacement, from its own config
(`vite.gallery.config.ts`): the standalone app's build roots at `app/` and
never sees it, so it is not in `just viewer-build` output.

```sh
just viewer::gallery                  # http://127.0.0.1:5180, GALLERY_PORT=... to move it
just viewer::gallery-export           # copy tiles of stored dev reviews into gallery-data/stored
```

The recipe runs the server in the `ui-test` container with the port published
on `127.0.0.1`, prints the URL and stops with Ctrl+C. It reads three sources,
listed afresh every moment, so a new run, export or saved fixture appears
without a reload:

| Source | Where it reads | Shown against |
| --- | --- | --- |
| Prompt-eval runs | `attempts/*.json` under `tools/prompt-eval/runs/` (mounted read-only), each attempt's `document` | that document; the `tools/prompt-eval/prompt-eval view <attempt>` command for the whole review sits beside it |
| Stored reviews | `gallery-data/stored/*.json`, written by `just viewer::gallery-export` from the dev database (`--repository`, `--kinds`, `--limit`) | the stored document |
| Fixtures | `gallery-data/fixtures/*.json`: one tile, an array of tiles, or `{ "document", "tiles" }` | the schema's example document, adjusted so the tiles' code sources can pass (or your `document`) |

`gallery-data/` is ignored by git, since runs and stored reviews hold
repository content. [`gallery/example-database-schema.json`](gallery/example-database-schema.json)
is a valid `database-schema` tile to copy into `gallery-data/fixtures/` and
edit. A fixture that is not valid is drawn as the viewer draws it (Tile
unavailable) under the list of what the validator rejected, so a payload can be
broken and repaired in place. The export needs the shared dev database
(`just shared-up`) and writes nothing when no stored review has tiles, which is
the case until one is generated with a tile allow-list.

Editing a tile component, or anything it imports, replaces the modules in
place and draws the page again without a reload; the kind and source filters
live in the URL, so the page keeps its view. Set `GALLERY_POLLING=1` if file
changes do not reach the container. The loop end to end:

```sh
just prompt_eval::cases migrations --limit 2     # pick PRs whose review has migration hunks
tools/prompt-eval/prompt-eval \
  -manifest "$PWD/tools/prompt-eval/experiments/migrations/manifest.json" \
  -output "$PWD/tools/prompt-eval/runs/migrations-01"   # database-schema allowed; calls the model
just viewer::gallery                             # open the URL, filter on database-schema
$EDITOR oss/viewer/src/components/review-tiles/contract-schema-diff.tsx   # the page updates on save
```

See [`tools/prompt-eval`](../../tools/prompt-eval/README.md#tiles) for the
manifest's `tile_kinds` and `tile_catalogue`.

### Browser tests

`e2e/` holds Playwright specs that drive the standalone app in Chromium.
`pnpm test:e2e` builds the app from source into `e2e-dist/` (not the committed
bundle, so a stale one cannot hide a failure) and starts `e2e/server.ts`, a
small stand-in for `baum internal view`. It serves the built files with the same
security headers as `internal/viewserver` (the content security policy is kept
equal to its `contentSecurityPolicy`), `GET /api/document` with the schema's
synthetic example (`../schema/review-example.json`, what `baum internal view --sample`
shows), a quiet `/api/events`, `/api/sessions` answering that no sessions are
available, and `/api/patch` answering no patch for a path the document cites (404 for any other, as `viewserver` does). Opening the page as `/?csp=off` serves it without the policy, which `untrusted.spec.ts` uses so that sanitization is tested on its own rather than masked by the policy. The suite needs no Go binary
and no hosted API. A spec changes what one page sees with `page.route`
(`serveDocument` and `serveSessions` in `e2e/fixtures.ts`), so specs stay
parallel.

| Spec | What it covers |
| --- | --- |
| `document.spec.ts` | the sample loads from the page's own origin with no console errors or foreign requests; a document read from a file; no file picker or upload; a failed load with Retry; a body that is not a `baum.review/1` document; a document that fails to render; the head's review time estimate, present, null and absent; the Summary's context, collapsed, expanded and absent |
| `navigation.spec.ts` | the rail, the summary's jump links, the Changes and Risks overviews, deep links to a section or risk and Back, a stale link falling back to the summary, the phone bar and sheet |
| `section.spec.ts` | a section's blocks (intent, entities, behavior, decisions, table, code, diagram); hunk excerpts stored, read from `/api/patch` when left out, with no patch, and retried after a failure; the unsupported block fallback; tiles hidden until `?tiles=1`, then drawn, and an unsupported tile kind; description images drawing nothing without an attachment source |
| `checks.spec.ts` | the Activity tab's checks (failing, blocking, green, an earlier head's checks ignored), verified, missing and pending evidence, the risk score and change size |
| `sessions.spec.ts` | the Sessions and Commits groups with and without `/api/sessions` (document fallback with the server's reason, request failure, loading), a linked session's transcript and its failure and Retry, the Activity tab |
| `theme.spec.ts` | light and dark follow the system, change while open, keep text readable, and colour highlighted code in each |
| `untrusted.spec.ts` | hostile Markdown and HTML (`<script>`, `javascript:` links, `onerror`, `onclick`, `<iframe>`, `<form>`, `<svg>`) renders as inert text and runs nothing; plain-text fields and code excerpts show markup as written |

Fixtures are synthetic: the schema's example, `e2e/local-sessions.ts` for the
local session answers, and documents the specs edit from the example. The app
has no file picker, upload or drag and drop (see Standalone app), and draws no
description images, as it supplies no `AttachmentUrlProvider`; the specs assert
both. The hosted app's integration with the viewer stays covered by `ui/e2e`.

Attachment resolvers may receive the rendered document as a second argument,
so a host whose captures are version-scoped can serve each comparison pane’s
images from that snapshot. Existing one-argument resolvers remain supported.

The local multi-repository app in [`../app/`](../app/README.md) consumes the
same public viewer components and uses the exported `local-sessions` and
`local-transcript` parsers for the local server’s redacted answers.

Review part headers wrap their controls below the label when space is tight;
wide surface-filter strips scroll within their own bounded container so the
Summary never widens a narrow page.
