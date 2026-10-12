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
| hunk patch source | no | `HunkPatchProvider` loads a file's patch for cited code whose stored excerpt was omitted |
| file opener | no | `ReviewFilesProvider` opens a cited file in the host's own diff view instead of following the link |
| row actions | no | `checks` on `ReviewViewer`, or `renderCheck` on `ChangesOverview` and `RisksOverview`, puts a host's read mark or approval beside each row |
| side pane, header slots and other features | no | the `ReviewViewer` slots below |

## The review page

`ReviewViewer` (`@baum/viewer/components/review-viewer`) renders a whole
review: the head, the version menu with comparison, the rail (Summary,
Changes and Critical risks, folding as the reader goes), the pane for the
selection, and a side pane with the document's Details and Activity. It keeps
the selection and the version being read in the URL through
`UrlStateProvider`, and remembers the side pane's tab, fold and width, and
the rail's width, in `localStorage`. Both the rail and the side pane resize
from the border they share with the pane, by pointer or keyboard; the rail
starts at 264px.

Below `xl` (1280px) the panes stack, and the rail becomes a sheet from the
bottom of the window. A bar sticks over the pane in its place: it names
where the reader is ("Behavior changes, 1 of 5"), opens the sheet, carries
the host's read mark for the section on screen, and steps to the previous
and next place in reading order: the Summary, then each section as the rail
lists them. The foot of the pane offers the same steps, so a reader who
reaches the end of a section goes straight on to the next. A step lands the
new place at its top, under the bar. The side pane is a sheet too, nearly the
window's height, opened from the bar's last button (named after the pane,
and flagged while CI is failing) rather than stacked under the review, so
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
| `versions`, `loadVersion` | the version menu: read an earlier version alone, or compare it with the current one. The loader should cache, as section panes reuse it for their "From v1" links |
| `history` | the host's own `useHistoryView` result, for a host whose requests follow the version being read; otherwise the viewer keeps its own |
| `versionsFailed` | a retry for a version list that could not be loaded |
| `header` | the head's live pull request facts (title, state, repository, branches, size, link), a back link, an approval state, a statistics line and the generation state; each falls back to what the document recorded. `heading` titles a review of something other than one pull request, such as a stack's: the document's own pull request number, link and state are then not shown |
| `placeholder`, `asidePlaceholder`, `fallbackSections` | what the pane, the Details and Activity tabs and the rail show while a host has no document |
| `checks` | read marks or approvals beside each section and risk on the current version, whether each row counts as read (the rail's progress), and notices above and below the rail's rows |
| `renderSectionAside` | a strip at the top of a section's pane on the current version, such as who approved it |
| `renderSectionLabels` | labels at the end of a section's badges, in its pane and in the Changes overview, on whichever version is read, such as the pull requests a stack review's section covers |
| `notices` | alerts at the top of the pane, such as a generation in progress |
| `files` | a Files group: the host's tree in the rail, under a row that writes how many files changed, over a bar of how many are viewed, and its list of file diffs in the pane, with code links in the review opening the cited file there. `?pane=files` lists every file folded; `?pane=file&file=<id>` hands `renderFiles` that file to unfold and scroll to, whether the reader clicked it, followed a link or went Back. With `tracksViewed: false` the rail counts no progress through the files |
| `sessions`, `commits` | rail groups listing the host's linked sessions and commits, each with the host's pane; `sessionsSlot` and `commitsSlot` build them from plain data (see below) |
| `conversation` | the side pane's first tab, given where the reader is (section, risk, version) and a way to open a file at a line. Without it the side pane holds only Details and Activity, and nothing is labelled as a conversation |
| `detailsChecks` | the Details tab's Checks part for the document the tab describes, in place of the document's own checks, and whether it shows a failure (which flags the tab); null content leaves the part out. For checks gathered from elsewhere, such as a stack review's members |
| `hideActivity` | leaves out the side pane's Activity tab; a reader whose last tab was Activity sees Details |
| `focusConversation` | puts the cursor in the conversation's text box when the reader asks to discuss the review |
| `hunkPatches` | the hunk patch source for the section and risk panes |
| `onVersionsShown` | called with the versions whose documents are on screen whenever that set changes, for example to record that a version was read |

Decisions show who took them: a section's Key decisions and the Activity
tab's list badge each one Human or Agent, and an agent decision carrying
`approved_by_human` reads "Agent · approved" with a tick, so the approval is
in words rather than colour alone. It still counts as the agent's in the
Activity tab's agent and person tally. A decision that carries them also
shows what prompted it and why ("Prompted by review: …") and the session's
own words as a quote; both are plain text from the session, never Markdown.

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
same card in the hosted app and in `baum view`: a header with the agent,
title, date, duration, prompt count and outcome (or linked commits), the
session's figures (models, tokens, cost when the host has prices, files,
lines, tool calls, working directory), then the session as a conversation
(`SessionTranscript`, `@baum/viewer/components/transcript`, built from the
`components/chat.tsx` pieces below): each prompt as a bubble on the right, the
agent's text replies on the left, and a quiet line of what the turn caused
with an **Agent activity** toggle. Opened, the activity replaces the turn's
replies: model usage, then the agent's messages in time order with each run
of tool calls between them folded into one row, each call with its input
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
text folded behind it, as written, then what the agent did about it. An input
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
`src/legacy-review-tiles.json` keeps the definitions of kinds the schema has
retired, so stored tiles of those kinds still render. `pnpm generate` writes
the tile types to `generated/review-tile-types.d.ts` from both files, which is
not committed. `pnpm generate:validators` writes the validators to
`src/precompiled/review-tile-validators.js` with Ajv's standalone code
generation, and that file is committed. They are precompiled because Ajv
otherwise compiles with `new Function`, which a page under a strict
Content-Security-Policy (as `baum view` serves) cannot run. No other script
rewrites the file, so a unit test fails when it is out of date: run
`pnpm generate:validators` after changing either schema file, then rebuild the
standalone app; `just viewer-build` from the module root does both.

## Standalone app

`app/` is a single-page app that renders one review document with
`ReviewViewer`. It is meant to be served by `baum view`, and talks only to the
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
  again retries.

The app follows the system colour scheme, keeps the reader's place in the
URL's search parameters, and has no file picker, no upload and no requests to
any other host.

`pnpm build:app` builds it with Vite into `../internal/viewer/dist`, the Go
package that embeds it. That output is committed, so `go install` needs
nothing but Go: run `just viewer-build` from the module root and commit the
result whenever the viewer's source or the schema changes. It runs
`pnpm generate:validators` and `pnpm build:app`, and `just viewer-fresh` fails
when the committed output differs from a fresh build, as CI does. The
build writes no sourcemaps, and two builds of the same source are
byte-identical.

## Development

Requires Node.js 24 and pnpm.

```sh
pnpm install
pnpm check            # lint, format check, typecheck and unit tests
just check            # the same, through this directory's justfile
pnpm format           # format with Biome
pnpm build:app        # build the standalone app into ../internal/viewer/dist
pnpm test:unit:watch  # watch the unit tests while editing
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
