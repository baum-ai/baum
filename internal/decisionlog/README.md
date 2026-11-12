# Local decision capture

The daemon extracts the decisions of the coding-agent sessions running on this
machine and keeps them in the shared private
[`<state>/app.sqlite`](../appdb/README.md) application database
(directory 0700, file 0600). Extraction uses the extraction prompt resolved
for the session's repository (the repository's custom prompt, else the global
one, else the published prompt, `decisions.NewPublicExtractor`,
`decisions-public-v1`) and the `decisions` package's renderer, parser and
citation validator, so a local extraction follows the same rules as a hosted
one. A custom prompt is recorded as a `+custom` prompt version. The prompt is
read for every capture, and a failed read fails the capture rather than
sending the published prompt in its place. Changing the prompt does not by
itself extract a covered session again: covered is decided by the transcript
fingerprint and repository-context version, so the new prompt applies when the
transcript next changes. The repository context is versioned separately as
`repository-decisions-v1`, appended to the stored prompt version. A head made
without repository context misses both the fingerprint and content-hash caches
once, retaining its existing decisions and stable IDs during the extension.

`baum decisions` uses the registry's prompts when a registry exists, else the
published prompt.

## Gate

A session is captured only when all of these hold, read for every capture:

- the automatic master switch is on (the desktop tray's pause turns it off);
- the decisions switch (`preferences.decisions.enabled`) is not false;
- the session's repository is picked and its `settings.decisions` is not
  false.

Capture is on by default. A session outside a picked repository, or in one
turned off, is not read and leaves no row.

## When a session is captured

The scheduler reads the live session tracker every 30 seconds; it keeps no
per-session timers. A session is queued when:

- it settled: it ended, or it is not running and its last activity is at
  least 2 minutes old (its turn ended, it waits on its user, or it went
  silent); once per last activity;
- it has been running for 20 minutes since it started running after it last
  settled, or since it was last queued for running long (a brief wait between
  prompts does not restart the count);
- it is about to compact (`pre-compact` hook event), at once;
- every 5 minutes, it is settled (the backstop, for a transcript that changed
  without new activity; an unchanged one is left alone).

One worker captures the queued sessions one at a time; a session already
queued is not queued twice. Before a call, the ledger throttles a session: at
most one attempt every 5 minutes, none within 10 minutes of a failed one, and
after 12 attempts in a day only once it ended. A capture cut short by the
daemon stopping settles its attempt as abandoned (outcome `abandoned`) and is
not a failure.

## Capture

1. The transcript is located as the live session view locates it: a regular
   file inside the agent's own session store, symbolic links resolved. When
   the hooks named no transcript, the path the last capture stored is tried
   first, Claude Code and Codex alike; if it no longer holds the session, a
   Codex session is looked up by id, which scans the store (a Claude Code one
   cannot be found without a path). The transcript must map to the hook
   event's session, otherwise the session is skipped (`session_mismatch`); a
   path is stored only once it does. A session whose own files exceed 64 MiB
   is skipped (`too_large`).
2. When the stored fingerprint (size and modification time of its files) is
   unchanged and the session is covered by the current repository-context
   version, nothing happens, except that a stored path that failed is replaced
   by the one found. An unrelated repository change does not remine a covered
   transcript.
3. baum's own generation runs are skipped (`generation_run`).
4. The transcript is rendered and redacted. When it has the head extraction's
   content hash and the current repository-context version, the head still
   covers it and no call is made. When a head exists, the call extends it: the message lists the
   existing decisions by handle and then the whole transcript, with a marker
   after the turns the head's prefix covered; existing decisions keep their
   IDs. Otherwise the call reads the whole transcript.
5. A prompt estimated over 272,000 tokens is skipped (`over_token_cap`).
6. The backend is `preferences.decisions.backend`, the data extraction backend
   the app chose, else the one reviews use (config file, app preferences), and
   `auto` resolves as for reviews in the desktop app (`generate.SelectDesktop`):
   the first of claude and codex that passes its check, never the Anthropic API
   or cursor-agent; none is `backend_unavailable`. A requested backend other than
   auto, claude or codex is refused (`backend_unsupported`). The model
   follows the resolved backend: `preferences.decisions.model` unless it names
   another provider's family, else `claude-haiku-5-5` for claude and
   `gpt-6-luna` for codex. A backend that does not pass its check is
   `backend_unavailable`.
7. A read-only source snapshot is exported from an available registered clone
   of the repository, using the session's observed commit when known, else the
   clone's head only when it is on the session's branch. No remote fetch is
   required. A missing commit, clone or matching branch is `source_unavailable`,
   retried as a backend-unavailable skip; existing decisions stay readable.
   The snapshot's `source.json` records the repository, revision, resolution,
   omissions from the configured checkout caps, and the limitation that
   uncommitted files are not included (the transcript may describe them).
   Source selection never switches or modifies the person's checkout.
8. The attempt is recorded as pending and a code-capable agent task runs through
   `generate.RunTask` on the data extraction backend. It has `head/`, the exact
   resolved extraction instructions and the cleaned transcript/extension input.
   Claude Code can Read/Glob/Grep the source; Codex has the review task's read-only
   source access. Neither uses the tool-disabled `Complete` path. The task returns
   the original extraction schema inside `extraction`, plus `source_refs`, a list
   of existing repository-relative files it used for context. Schema/reply
   checks and source references are validated, with up to two repairs. A failed
   provider attempt is retried by capture's existing failure/backoff policy.

   The initial reservation estimates the transcript and reply; repository reads
   and additional task turns can add usage. The ledger records the aggregate
   actual task usage, priced as reviews price it; unpriced usage stays unknown.
   There is no spending cap. Configured call/token budgets apply, and the existing
   capture call timeout (10 minutes by default) includes repairs. A cancelled
   task is abandoned without changing decisions; a timed-out one fails so it
   cannot hold the capture queue indefinitely. A ledger write failure is logged
   without discarding an otherwise usable result.
9. The reply is masked for secrets before it is parsed, validated against the
   transcript, given IDs and saved.

A failed call is not cached. After 3 failures in a row the session is marked
`extraction_failed` until its transcript changes. Skips for a missing or
unsupported backend, or missing source are tried again the next day or when
the transcript changes; other skips only when it changes.

## Stored data

- `capture_session`: per agent and session id, the repository and branch, the
  transcript path, the fingerprint, the head extraction (content and prefix
  hash, prompt version, backend, model, decisions with IDs, dropped citations,
  tokens and cost), the coverage (`extracted` or `skipped` with a reason) and
  consecutive failures. It is indexed by `(repository, extracted_at)`, which
  the decisions page, its coverage counts, the people count and the feature
  assigner filter on.
- `capture_source`: the latest successful head extraction's source context,
  including the version, revision, omissions, limitations and validated source
  references. `decision_source` keeps the same extraction-level context for each
  newly created decision; extending a session preserves older decisions' original
  provenance. Earlier decisions without recorded source remain explicitly
  without provenance. These additive tables are created on writable open; a
  read-only old store remains readable without them.
- `decision` and the `current_decision` view, `decision_file`: the decision
  log. A decision is upserted by ID; decisions a newer extraction no longer
  holds get `obsolete_at` and are never deleted. A decision is
  dated by its first cited turn's time, else the date its ID had, else the
  extraction's time.
- `timeline_revision`: per repository, a counter the Timeline's snapshot is
  keyed on (see Timeline API). Triggers move it, in the same transaction, on
  every insert or delete of, and every change of a column the snapshot reads
  in, `feature_version`, `feature`, `feature_pull`, `feature_session`,
  `pull_request`, `pull_commit`, `session_commit`, `capture_session`,
  `decision`, `decision_file`, source provenance, durable scopes and local
  semantic attribution; for the feature tables only rows of the
  current version count, a building version moves it when it is switched in.
  A decision moves the revision of its own repository and of its session's.
  A write to anything else in `app.sqlite`, or to a column the snapshot does
  not read, leaves it alone. A writer that deletes and inserts the same rows
  again (a relink, a path refresh, a re-extraction's files) moves it
  although nothing differs. The triggers are named after a hash of their
  definitions and replaced, in one transaction, when a build opens a store
  with others.
- `capture_attempt`: the ledger of calls (backend, model, full or extend,
  reservation, cost, tokens, state, failure reason, rejected replies), with the `stage` that made the call
  (`capture`, or a feature log stage) and the `item` it was about.

Nothing is pruned: the ledger and the decisions, obsolete ones included, are
kept for good, as hosted extraction keeps them.
The store holds decisions, rationale and evidence quotes derived from redacted
transcripts, never the transcripts themselves.

## In the app

`API` serves `GET /api/decisions`, mounted by `internal/uiserver`:

- `?agent=&session=`: one session's current decisions, its coverage and skip
  reason, when its row last changed (`updated_at`, null when never captured)
  and `capturing`, true while one of its calls is pending, and
  `capture_off`, why the gate keeps capture from running for its repository
  now (`paused`, `off`, `repository_off` or `not_picked`; the repository is
  the tracker's, where the session works now, else the one it was last
  captured in). A registry that cannot be read leaves it empty and is
  logged.
- `?repository=&pr=`: the same for each session of the pull request, resolved
  when read: the tracked sessions whose pull request it is (the live
  tracker's `prlink` link), ended ones it still holds included so their
  provisional decisions stay, then the sessions its latest saved review links
  on this machine (`locallink`, the computation its PR page's sessions list
  shares; a failure to link is logged and links none, and a review too large
  to serve links none but still dates the provisional marks). A session in
  both appears once; `live` is a tracked session not over. Decisions dated
  after the review's `generated_at` are `provisional`. A decision is dated
  when it was first raised (its first cited turn, as hosted decisions are), so
  provisional counts from then, not from when it was last revised; without a
  saved review none are, and only the tracked sessions are listed.

A decision carries only what the app shows (`DecisionView`): id, title,
summary, category, who, rationale, rejected alternative, superseded, files
and date; evidence, turns and branch are not served, nor a session's
repository.

`Store.Changed` is called after every write the app shows (a skip, a cover,
a save, an attempt beginning or settling; the beginning is what shows
"Capturing…", and the settling ends it when no save or skip follows); the
daemon publishes the app's `decisions` event, which carries no data, with it,
and also after the settings or a repository's are saved. The settings are
`preferences.decisions` (`enabled`, `backend` and `model`), served only by
`GET` and `PUT /api/settings/decisions` (in `internal/uiserver`), which add
`EffectiveModel`: the backend and model a capture would use, resolved by the
same helper a capture uses, `auto` included, and whether the chosen model is
ignored. Its backend checks
get 5 seconds; no backend, or none in time (`backend_undetermined`), is
unknown (empty) and ignores nothing. `baum internal reviews show` reports the
chosen backend and model as `decisions_chosen_backend` and `decisions_model`,
and the effective ones as `decisions_backend`, `decisions_effective_model`
and `decisions_model_ignored`; `baum internal reviews set --decisions-backend`
and `--decisions-model` change them. A repository's own setting is `settings.decisions`,
through the repository settings API. Analytics counts capture's calls in
the ledger (`Store.Ledger`) under baum's work.

## Feature log

The feature log groups a repository's pull requests and sessions into
features as the hosted feature log does, with the `features` package's rules
and published prompts. Its choices are in `featureconfig.go`, and it shares
the store with capture. `FeatureWorker` syncs a repository's pull
requests, reads their paths and commits from the clone, and links the
commits to this machine's sessions, then places them in features, writes
the features' text and consolidates them.
It runs in the daemon beside the capture scheduler, every minute and when
settings change, for each picked repository with an available clone whose
decisions are captured (the gate above); a repository turned off is not read
and makes no GitHub call. A repository whose listing fails, whose clone
cannot be read or whose remote is out of reach is tried again after 15
minutes.

### Pull requests

- The first run for a repository records when the feature log was activated
  for it (`pull_sync.activated_at`), for placement to leave earlier work
  out.
- The repository's pull requests of every state are listed through the
  GitHub REST API, most recently updated first, 64 a page (a page too large
  to read is read again at half the size, down to 8), until one updated
  before the cursor (the first time, 180 days back); one updated exactly at
  the cursor is listed again. The first page is read with the stored ETag,
  so an unchanged repository costs one conditional request. The cursor moves
  to the latest update listed, or to the start of the window when none was.
  A listing stops at about 10,000 pull requests; older ones still due are
  skipped and logged. The explicit legacy full-history build lists all history once; the
  requested adaptive build chooses its own metadata windows.
- Only pull requests that can be placed in a feature are read from the
  clone: open or merged ones not opened by a bot. Each pass fetches what
  its due pull requests lack, as a review fetches a missing pull request
  commit: a head from `refs/pull/<N>/head` into `refs/baum/pull/<N>/head`
  and a base by its id into `refs/baum/pull/<N>/base`, both force-updated,
  100 refspecs to a `git fetch`. So each such pull request of the 180 days
  before activation (of the chosen window after an adaptive build, or the
  whole history after a legacy build), and every
  one after, gets refs in the clone; they are
  kept, not pruned, as they may back saved reviews. Each pull request's
  commits, from its base to its head, are then read from the clone's own
  objects, never fetched. Its paths are the files its commits change; its
  commits replace those recorded. A new base has them read again. GitHub's file list is never read.
- When a fetch fails, the remote is asked for its HEAD (`git ls-remote`).
  When it does not answer, the pass stops, nothing is counted against any
  pull request and the repository waits; when it answers, the refs of the
  failed fetch are fetched one at a time, so one the remote lacks keeps out
  no other. A pull request whose commits then cannot be read (its head or
  base is missing, or it has too many) is read again after 15 minutes;
  after 3 failures at its head its paths are unknown at that head. A new
  head starts the count again, and the daily re-link has every failed pull
  request read again at once, an unknown one 3 more times. A pull request
  that fails never stops the others or the linking.
- The gate is read again before the fetch, before each pull request and
  before linking; a repository turned off during a pass stops there, and
  its commits are not linked in that pass.

### Sessions linked to pull requests

The commits are linked to the sessions on this machine with the local
linkage of the review page (`locallink.LinkCommits`: checkpoint candidate discovery followed by
independent creation or edit evidence; generation runs left out), and their links replace the ones
they had:

- each run, the commits just recorded, made in the last 7 days (hosted looks
  back a day; a laptop may be off for days);
- every 15 minutes, the commits within the session window of now, which a
  session still running may yet link to, read from the clone without
  fetching, at the head their commits were recorded at;
- once a day, every commit of the last 7 days.

A 15-minute re-link without fresh commits reads no session when its commits,
the session stores it reads (`sessions.Discovery`'s fingerprint) and the
commits' observed creations are as at the repository's last links: it would
link the same. The daily re-link and a daemon start always link again.

A commit linked to a session more than one way keeps its strongest link.

`Store.SessionLinks` reads what attributes a captured session's decisions
(`features.SessionLinks`): the feature pull requests its linked commits are
in, each with the paths it touches and those it was assigned with; a
session with no capture row has none.

### Model calls

Local placement, semantic decision attribution, feature text and consolidation
run code-capable tasks through the same `generate.RunTask` adapter as capture.
They read the full system/user/schema contract plus a pinned read-only `head/`
and `source.json`; source references are validated before redaction and again
afterwards. Incoming PR placement uses that PR's committed head, including new
paths absent from an older map. Session context prefers capture provenance, then
a linked session commit; a pre-source capture with neither uses explicitly
labeled map/current committed context, never claimed to be session-time code.
Unavailable source backs off explicitly. Map revisions remain separate.

The same backend and accounting apply (capture steps 6 to 8): model,
reservation (the request's byte bound and
the call's reply bound: 4,096 tokens for a placement, 32,768 for a batch,
merge or split, 8,192 for a text or pull request summary), 10-minute bound
and ledger, each attempt recorded with its stage (`feature_assign`,
`feature_attribution`, `feature_summary`, legacy `pr_summary` or `feature_consolidate`) and item. Rejected
replies and a ledger that cannot be written are handled as capture's. The
preferences and the repository's settings are read again before every call,
so a repository turned off makes no further call. A repository turned off or
a missing backend stops the stage until its next run, with no failure counted.

### Placement

After the sync and the linking, each run places at most 100 pending items,
oldest trigger first, one task each with `feature-assign-repository-v1`. An
item is pending when:

- a pull request is in no feature, was opened at or after the activation and
  within the last 7 days (whatever its state), was not opened by a bot, and
  its paths are known or it opened 15 minutes ago;
- a pull request the assigner placed has merged since, once;
- an open pull request's head is not the one it was placed at and its paths
  changed enough (`features.LargeUpdate` with the paths it was placed with,
  0.5 and 3); a head that changed too little is recorded as checked
  (`checked_head_sha`) and left until the next push;
- a captured session with current decisions, extracted in the last 7 days
  and not since 15 minutes ago, is in no feature and linked to no pull
  request's commit. A session linked to one is never placed on its own: it
  follows that pull request's feature.

Its candidates are at most 16 features of the live version not merged into
another, changed in the last 90 days (a change to the feature, or one of its
pull requests or their placements), or the item's current feature. A
feature's paths are those its pull requests were placed with and those cited
by the decisions of its sessions placed on their own that follow no feature
pull request. Each of the item's paths a feature holds scores one over the
number of these features holding it. The current feature comes first, then
the features holding a pull request linked to the item (stacked on it or it
on them: one's base branch is the other's head branch, other than the
default branch (none while the default branch is not known), both in the
repository rather than a fork, and opened from
by one pull request only; sharing a linked session; or mentioned as `#N`),
best path
score first, then the best path scores of the rest (10), then the most
recently changed. Each candidate shows its digest and its pull requests'
titles with their saved review summaries, else already generated ones. Durable
`feature_scope` purpose, exclusion boundaries, paths and validated examples
remain candidate context after first membership. Source-only scopes are also
candidates and materialize only on their first supported join. Scope aliases
follow consolidation merges to valid targets. Direct links, matching examples,
purpose-word relevance and source/path evidence affect retrieval; broad paths
and a direct link do not establish a common outcome. A chosen placement or
explicit skip records its reason, incoming source and separate map context in
`feature_judgment` under `repository-placement-v1`.

The reply joins a candidate, starts a feature with its provisional text, or
skips a small change. A session only joins: with no candidate it is settled
without a call, and a reply starting a feature or skipping settles it. A
skipped pull request in no feature is settled for its head; one already in
a feature stays in it, as if the reply chose it. Settled items wait for
their next trigger (`feature_settle`). A failed call, an invalid reply or a
failed write after a call backs the item off for 10 minutes; after 3 counted
failures for one trigger it waits for the next.

The preferences and the repository's settings are read again before each
item, so a repository turned off stops the run. A call cut short by the daemon stopping counts no
failure. A pull request placed at its head before its paths were known gets
them as the paths it was placed with once they are recorded, with no call.

A placement marks every feature it changes: the one joined, the one left
and the features of the pull request's linked sessions; an identical one
marks none. Also marked: a session's features when its decisions are saved;
a feature pull request's feature when its title, description or state
changes (in the current version and in a building one), and, with the features of the sessions linked to its old and new
commits, when its commits or paths change; and when the sessions linked to
a commit change, the features holding a pull request with it and the
features of those sessions (an unchanged re-link marks none). Marking
raises `membership_version` and sets `dirty_since` and `changed_at`.

### Semantic decision attribution

`FeatureWorker.AttributeDecisions`, also invoked by public `Summarize`, refreshes
local decisions before text is written. It uses `feature-attribution-repository-v1`
tasks with meanings, quoted extracted evidence, cited turns, linked commits/PRs,
durable scopes and source. It never rereads complete transcripts. One task
answers at most 40 pending decisions from one session; a pass takes at most 50
pending session units (or its explicit limit), including unavailable-source
attempts. Inputs over 512 KiB remain unresolved with a reason. Failures use the
same per-input backoff as placement. Every supplied ID must occur exactly once;
only supplied eligible features/PRs may be chosen, and explicit no-match is valid.

`feature_decision` stores repository/version/decision ID, input hash, context
version, selected feature and optional supported PR, key eligibility, reason and
source manifest/references. Immutable `decision_source` provenance accompanies
the current capture context; an original decision from another repository stays
unresolved. Cache identity includes decision IDs/text/citations/source, eligible
scope definitions and linked PR heads/text/paths/commits. Display text derived by
a summary is excluded from cache identity to avoid a feedback loop. Unrelated
clone HEAD changes do not invalidate pinned evidence. Repository input revision
is compared inside the write transaction, so changes during the task cannot
persist stale relevance or highlights. Accepted answers dirty affected features,
including initially clean adaptive scopes, without blocking build promotion.

Timeline and `summarySource` share one bulk storage-only resolver. A current
semantic answer wins, including explicit none. While absent/stale, only file
evidence supporting exactly one feature is accepted; multiple positively
supported features, ties, no match and fileless/own-session guesses remain in
All decisions. No dominant or earliest-feature fallback runs locally. Until a
semantic answer establishes significance, fallback decisions cannot be keys.
The shared hosted `features.Attributor` keeps its published behavior.

### Feature text

Each run writes the text of at most 50 features behind their members, not
changed for 15 minutes, longest behind first, 4 at once, with `feature-summary-repository-v1`. A stop or an error starts no further feature; those being
written finish. A feature's input is its pull requests, earliest opened first
(title, description, state, and the summary of the latest review saved in
the clone, else a generated one), the current decisions of its sessions that
attribution gives it (what was decided and why and the cited files, never a
session's prompts or quoted evidence) and its paths. The call also reads the
repository's name and the feature's current title and description.

Live repository summaries read PR titles, descriptions, paths, available saved
review/generated summaries and source directly; they never fan out to a model
call per PR. The explicitly selected legacy full-history build retains its old
per-PR fallback. Only semantically eligible, significant choices may be keys:
routine agent tests, lint/build checks, PR preparation and bookkeeping are
excluded. A deliberate testing/tooling policy may qualify; merely performing
checks does not. Zero key decisions is valid. Explicit human choices and stable
citations are retained. A reply naming an ineligible key cannot store it; reads
also hide legacy/stale keys until relevance is established.

The text is kept with the hash of its input and its own text, decisions
named by session and first cited turn, so the same decisions extracted again
under new IDs keep it. Key decisions are kept by ID and address; reused
text takes the IDs of the current decisions at their addresses. When a
feature's input hash is unchanged, its text is kept and it is marked clean
without a call. The text is stored only when the feature's members did not
change during the call; otherwise it stays behind for the next run. Up to
three key decisions are kept by ID. A feature with neither pull requests nor
decisions keeps its text, loses its key decisions and is marked clean. A
failed pull request summary still writes the text, leaving the feature
behind; failures back off and count as placement's do, per membership
version.

### Consolidation

After the text, a repository's features are consolidated with the local
`feature-consolidate-repository-v1` repository task, at most once every 7 days (from the
start of the last pass) and only when their fingerprint (every feature's id,
membership and text versions and `merged_into`, merged ones included)
changed since the last pass (`feature_consolidation`). A pass's own merges
and splits are changes, so one that changed features is followed by another
a week later. A pass reads the features not merged into another and active
in the last 90 days (as placement's candidates), most recently active first.
Its calls' replies may run to 32,768 tokens rather than hosted's 8,192:
Claude Code's models think within the reply's bound, and a merge over a
first build's features needs the room.

- **Merge.** With two features or more, one call sees each feature's text,
  digest, paths, pull requests with their summaries (the saved review's,
  else the generated one) and how many sessions are in it on their own. Each
  group's duplicates are merged into its survivor: their pull requests and
  sessions move to it as `consolidation`'s, the duplicate gets `merged_into`
  (and so do the features merged into it before), both are marked, and a
  link to the duplicate resolves to the survivor (`ResolveFeature`).
- **Split hold.** For four weeks after a split, a merge of one of its parts
  with the feature it was split from, or of two of its parts, is dropped
  from the reply; the merge call marks such a part with its origin. A part
  merged into another feature passes its origin and split time on.
- **Split.** At most 3 features, as they will be after the merges, with at least
  two pull requests,
  largest first, get one split task each (a call takes its slot whatever its
  outcome) with their pull requests' text,
  state, opening date, placed paths and summaries. A feature kept whole, or
  too large to offer (over 200 pull requests or its input over its bound,
  which takes no slot), is recorded as checked (`feature_reconcile`) and
  not offered again until its members change; a survivor of the pass's
  merges always is. Otherwise the largest part keeps the feature's id (ties
  to the part with the earliest opened pull request), and each other part
  becomes a new feature with the reply's text, recording `split_from` and
  `split_at`, its pull requests moved as `consolidation`'s at the head and
  paths they were placed with; sessions in it on their own stay. The parts
  are recorded as checked.
  Small mixed groups may split into one-PR parts when they represent independent
  substantial outcomes. Shared paths do not force a join; stages, tests and
  follow-ups of one outcome stay together. `feature_reconcile` keys checked
  memberships by prompt version, so an old policy does not suppress repair.
- **Writing.** Every call of a pass is made before anything is written. Each
  merge and split is written in its own transaction, only while its features
  are at the membership versions the pass read (as its own earlier writes
  left them) and a split's feature holds exactly the pull requests it
  partitioned; otherwise that one is skipped and the pass goes on. Writes
  mark every feature they change and the features of the sessions linked to
  the pull requests they move, for the text stage.
- **Failures.** A call that fails, or a merge reply that does not decode,
  defers the whole pass: nothing is written (a split call's failure or
  refusal discards the merges paid for, which are logged and counted), and
  the pass backs off as placement's failures do, counted against the
  fingerprint. A split reply that is invalid leaves only that feature whole
  and unchecked. A call that cannot be made (a backend reason)
  also writes nothing; the pass is made again on the next run.

### Adaptive requested build

Adaptive task inputs reference immutable per-PR evidence files, so later steps
can read selected descriptions and patches without embedding all accumulated
evidence in every state input. Durable evidence remains pinned in the checkpoint.

New builds requested from the desktop Timeline use `BuildOptions.Adaptive` and
`features/adaptive`'s published `feature-build-public-v1` protocol. A code-capable
Claude Code/Codex task reads a pinned, read-only `head/` checkout, `source.json`
and `state.json`. It can inspect source itself; host-mediated evidence requests
are valid task replies, separate from structured-output validation repairs.

The host initially lists metadata updated in the last month and all open PRs
regardless of age. There is no PR-count selection default. The agent chooses
whether to widen the history and which descriptions or code it needs. `catalog`
requests expand a date window, `details` supplies pinned descriptions or resolves a specific older PR reference, and
`code` reads commits/paths and optional focused file patches only for explicitly
selected PRs. `decisions` supplies captured decisions linked to pinned commits
for selected PRs, including extracted quotes, turn citations, session identities
and recorded source provenance. It reads no complete transcripts; at most 50
linked sessions and 200 decisions are snapshotted per PR, with explicit
truncation and missing-link limitations. There is no decision-tail pagination:
repeating a request cannot fetch beyond those bounds. This is available
evidence, not a claim of complete decision coverage. Subsequent steps reuse that exact
snapshot even if live extraction changes. Catalog listing and descriptions stay
in the companion checkpoint and never populate the live sync queue. Only accepted placements/skips and explicitly
requested selected code enter the shared PR cache. Catalog listing and
descriptions never require a Git ref fetch or session-link pass. Repeated windows and evidence reads are cached. Patches use
the PR's merge base and are bounded to 64 KiB per requested file; an unavailable
request stops with the exact request pending for a resume.

`checkpoint` replies add selected work, scopes and assignments. A scope retains
its purpose, boundaries, paths, examples and source revision after membership;
paths must exist in the source checkout or explicitly requested pinned PR code.
An empty assignment scope is an explicit small-change skip with a reason.
Source-only scopes reserve a stable identity without creating visible feature
rows; the first membership materializes that identity transactionally. Scope
text includes a concise digest, so the build does not fan out to a summary call
per PR or repeatedly summarize growing groups.

Each accepted checkpoint persists the pinned catalog (including head IDs),
source revision, requested evidence, scopes, selected work, assignments and
remaining request. Memberships and concise feature text are saved with it in one
transaction. A sequence compare-and-swap refuses competing workers or clones.
`finish` requires every selected PR to have an assignment or explicit skip and
every populated scope to have a digest. It validates and switches the candidate
to current atomically; zero substantial outcomes can finish with zero features.
The old Timeline stays current while any selected work is incomplete. Resuming
reuses accepted results and the pinned source/catalog; it does not reselect
history from mutable live metadata.

There is no outer performance deadline. After twelve successful work steps or
five minutes, the host signals `exploration_closed` as convergence guidance; necessary older-reference evidence and catalog
windows remain fetchable while selected work is finished. Each
model work unit has the configured pass timeout (ten minutes by default),
provider/context protections and stall/tool guards. Schema errors get at most
two repairs. Evidence failures retry once after a one-second backoff and then preserve their
pending request. GitHub rate-limit responses record Retry-After/reset timing as
a durable wait, with no immediate retry; early resumes preserve that wait. Three steps with no new evidence or changed accepted scope,
selection or assignment stop as `no_progress`; a changing narrative does not
count. Cancellation or failure keeps the candidate resumable. Prices report
cost, including failed task attempts and unpriced usage, without a spend cap.

Adaptive candidates use `feature_version.state=building_adaptive`. The unique
`feature_version_active_building` index spans legacy `building` and adaptive
candidates, so an older binary cannot insert a legacy build beside an adaptive
one or discover its ID as legacy work. Existing stopped legacy candidates retain
their original workflow when the user chooses Resume; they are never silently
converted. `FeatureBuildWorkflow` names it as `legacy` in the build API.
`ArchiveFeatureBuild` is the explicit replacement operation: it retires only the
candidate and leaves the current Timeline intact. A subsequent requested build
starts a fresh adaptive candidate. Explicit `BuildOptions{Adaptive:false}` keeps
the legacy full-history workflow below for operator/internal callers.

`GET /api/timeline/build` exposes `building.workflow`, optional
`building.coverage`, and `current_coverage` after promotion, including an empty
result. Coverage distinguishes metadata `catalogued`, unique PRs whose
`details`, `code` or `decisions` were requested (`inspected`), `selected`, `completed` and
`remaining`, the catalog update-date horizon and listing truncation. It does not
claim that catalog availability proves every title was read. Old read-only
stores without the adaptive tables expose no coverage.

### Legacy full-history build

The live placement places only work opened since the feature log was
activated. A repository's history is placed when the user asks for it, as a
hosted rebuild does, by a first build: a job of the job queue
(`jobs.KindFeatureBuild`) that starts at once when the user asks for it,
with no estimate, as every requested run does.

- **History.** The build first lists the repository's whole pull request
  history, once per repository (`pull_sync.history_listed_at`), with no
  ETag and without moving the sync's ETag or cursor, never replacing a pull
  request a sync saved with a later update; a listing that stops at
  its bound, leaving out the least recently updated pull requests, is
  reported (`history_truncated`) and not retried. The build then reads the
  paths of every pull request due, as a sync does, and, before it places
  any, links to this machine's sessions those of the commits it read that
  were made in the last 7 days (`FeatureLookback`), as a sync links its
  fresh commits; older commits are not linked.
- **No cap.** As for a requested review, there is no spending cap and
  nothing about the price stops a build: a model with no price runs as any
  other, and the build runs to the end. Costs are recorded at API prices
  where they are known. A repository whose decisions are not captured, or
  whose backend cannot call, does not start (the job is refused with the
  reason).
- **Dry run.** `--dry-run` counts what a build would do, with no cost: the
  pull requests left to place and its calls, a batch call per 25, the new
  features' text twice (a feature per 3 pull requests), a resumed version's
  features whose text is behind, and a summary of each pull request with a
  description; and whether it runs discovery first. It lists the history,
  saving the pull requests it lists and activating the repository's sync
  state; it writes no feature.
- **Seeding.** A new version, with no features and no seeds, is seeded with
  a feature taxonomy (`--taxonomy`, default `auto`): the newest made after
  the version, else a new discovery's (see "Discovery"); `latest` or a
  taxonomy's id a stored one; `none` nothing. Each of its features becomes a
  provisional feature with no members, whose text is the taxonomy's, and a
  seed (`feature_seed`, with its paths); the version records the taxonomy.
  A resumed version is never seeded again. A discovery that cannot run or
  fails leaves the build unseeded, with the reason in its report
  (`unseeded`), recorded on the version so a resume neither runs nor pays
  for it again. The gate is read right before discovery, as before any
  call.
- **Building.** The build makes the repository's `building` version, or
  resumes the one there is, and places in it the merged and open pull
  requests of the whole history not opened by a bot, in no feature of the
  version and not skipped or left out of it at their head, listed once when
  it starts, oldest first, in batches of 25 with the batch prompt (its seeded
  form when a candidate has no pull requests). Each item's candidates are
  ranked against the version as placement ranks them, read again for every
  call, and the call offers their union, each item's best first, at most 40.
  Seeds no pull request joined are kept out of the ranking: each is offered,
  with its text and paths and no pull requests, to an item by its path score
  (`discover.PathScores`), before the first ranked candidate not linked to
  the item with a lower score, as the hosted rebuild does. A seed a pull
  request joined is an ordinary feature from then on.
  A reply's new feature is created for the first item that starts it; a
  skipped small change is settled in the version at its head. The live
  stages keep using the current version meanwhile.
- **Failures.** A batch call that fails, for whatever reason, is made once
  more, then the batch is split in halves down to one pull request; one that
  still fails is tried once more with the others after the last batch, in
  batches of 25, and then left out (settled in the version as `left_out`). A
  failed store write stops the build.
- **Text.** Every 3 batches and after the last, the text of every feature
  of the version behind its members is written, without the debounce; a
  feature whose text fails backs off and is left for the text stage. A
  build makes no consolidation pass.
- **Switching.** The seeds no pull request joined are deleted, then the
  building version becomes current in one transaction:
  the current one is retired, the skips become placement's settlements, the
  pull requests left out lose theirs so placement takes those within its
  lookback, and the repository's last consolidation pass is forgotten, so
  the weekly pass consolidates the new version on its next run, a minute or
  so after the switch. A live stage that raced the switch never writes the
  retired version: its run stops at its next item or write
  (`version_retired`), and a consolidation pass writes, records and settles
  nothing, each write checking the version in its own transaction. Work the
  live stages placed in the old version is in no feature of the new one;
  placement takes what is within its lookback. A retired version is kept,
  and a link to one of its features resolves as archived.
- **Stopping.** A build stops before a call that cannot be made
  (`disabled` or a backend reason; `failed` for a store error), when a fetch
  cannot sign in to the remote because the credential needs a key passphrase
  or password nobody can type in the background (`git_auth_ssh`,
  `git_auth_https`; the Timeline says how to make it available), or when it
  is cancelled or the daemon stops, leaving the version `building` with
  `stopped` set to why; starting it again resumes it. Its
  job then fails with that reason. Progress (phase, calls) is reported to
  the job, and the version records its pull requests placed of the total
  and its spend over all its runs (`spent_usd`, each call's known cost, and
  `unpriced_calls`, those whose cost is unknown: the spend is then a lower
  bound, reported as such to the job). A spend the version cannot record is
  logged; the call and its ledger row stand.

### Discovery

Discovery maps a repository into its features, as hosted discovery does
(`features/discover`), and stores the reply as a taxonomy
(`feature_taxonomy`). It runs as a first build's first step or with `baum
internal features discover`, which first lists the history and reads its
paths as a build does.

- Its backend and model are the other feature calls' (see "Model calls"):
  the data extraction backend and model, whatever a build's own `--backend`
  and `--model`. cursor-agent is not supported; a model with no price runs as
  any other.
- The default branch (the latest one GitHub reported for a pull request)
  is fetched into `refs/baum/default`. The run directory holds its head's
  checkout in `head/`, bounded as a review's, and `history.jsonl`
  (read-only), the history digest of the pull requests a build places:
  titles, branches and paths, no descriptions.
- It runs as a review's agent task (`generate.RunTask`): the published
  prompt `feature-discover-public-v1`, the discovery schema and the
  taxonomy's bounds as its check, and a review run's budgets: the
  configuration's run timeout, calls and tokens (by default 45 minutes, 40
  calls and 2,000,000 tokens), 3 provider attempts and 2 repairs, whatever
  the model's price. Hosted discovery keeps its own budgets. Its output is
  masked before it is read.
- It is one ledger row (stage `feature_discovery`); in a build its cost is
  added to the version's spend.

### Stored data

- `pull_request`, `pull_commit`, `pull_sync`: the listed pull requests with
  their paths at a head and failed reads, their commits, and the listing's
  ETag, cursor, activation, last full re-link and history listing.
- `session_commit`: each session linked to a commit, with the method and
  confidence.
- `feature_judgment`, `feature_decision`, `feature_reconcile`: local source/decision
  context, durable semantic answers and prompt-aware reconciliation checks.
- `feature_version`, `feature`, `feature_pull`, `feature_session`,
  `feature_settle`, `pr_summary`, `feature_consolidation`: the tables
  placement, text, consolidation and a build write.
- `feature_build_state`: adaptive protocol/version, sequence and durable pinned
  catalog, source context, evidence, scopes, selections, assignments and pending
  request. `feature_scope`: stable scope identities and definitions retained
  after membership. These companion tables are additive and tolerate older
  read-only stores that have not been upgraded.
- `feature_taxonomy`, `feature_seed`: discovery's taxonomies and the seeds
  of the versions built from them (`feature_version.taxonomy_id`). A repository has at
  most one `current` and one `building` version; `featureVersion` is the one
  place that chooses the version a stage uses (the live stages' current
  one, made when first needed, and a build's building one).

These rows are not pruned. Only placement, text, consolidation and a build
write features, except `SeedSample`, which fills a store of the tour's sample
project (`internal/sampleapp`) as a build would have left it.

## Timeline API

`Timeline` serves the app's Timeline page under `/api/timeline/`, mounted in
the local API behind its authentication, in the view models of the review
viewer's decision log (`oss/viewer/src/decision-log.ts`). It reads the store
and never writes it. As the review index does, the feature lists and their
people share one snapshot of each repository's features, built once and kept
until the repository's `timeline_revision` moves (a write, by any
connection or process, to a row or column the snapshot reads; writes
elsewhere in `app.sqlite` do not count), the user's identity is read again,
or it is 30 seconds old (a bound for what the revision does not cover); a
search builds its own. Each repository builds under its own lock, so one
repository's build does not wait for another's; their queries still share the
store's one connection and take turns on it.

- **`GET /repositories`**: `decisions_enabled` (the decisions switch),
  `repositories`, the picked repositories whose decisions are not turned
  off (the switch and the repository's setting; the automatic master switch
  is not read, so pausing hides nothing), by name, each with
  `has_features` (its current version has a feature not merged into
  another), and `turned_off`, those whose own setting is off.
  A list reads the repositories its `repository` parameters name (repeated,
  any case; none is every one of them), and one it does not serve is a 400;
  a detail is found in any of them, and is a 404 in another. Every row and
  detail names its repository, `{"id","full_name"}`, both the registry's
  name.
- **`GET /features?repository=&q=&person=&file=&from=&to=&status=&cursor=&limit=`**: the
  repositories' features of their current versions not merged into
  another, merged in one order: by last activity (else creation) newest
  first, then id, 50 a page (`limit`, at most 500). `cursor` is the last
  row's time and id, so a page follows on from it whatever arrived
  meanwhile.
  `status` (`in_progress`, `shipped`, `abandoned`; repeated or comma
  separated), `q`, `person` (below), `file` (a pull request touching the
  path or anything under it, or a decision of the feature citing one) and
  `from`/`to` (the features whose activity, else creation, overlaps them, as
  hosted) filter before paging. A row has its title and
  description (null while none is written; `summary_pending` while it is
  behind its members), the status (`features.DeriveStatus` over its pull
  requests), the first and last activity (its pull requests' opening and
  last update and its decisions' times), `people` (the user, when it has a
  session or a pull request they opened, then its other pull requests'
  authors not opened by a bot, by login), the counts of pull requests, sessions (the
  repository's captured sessions linked to its pull requests' commits, or in
  it on their own) and decisions, and
  its key decisions, read as the text names them (`currentKeys`). A
  session's decisions are attributed as the feature's text attributes them
  (`features.Attributor`), each to one feature and, within it, to the pull
  request `Attributor.PullRequest` chooses.
- **`GET /features/{id}`**: the row with `digest`, its pull requests newest
  first (state, draft, merged, the latest saved review's state against the
  head and its summary, else the generated one, and the decisions shown
  under each) and its sessions: those in it on their own, and any whose
  attributed decisions fall under none of its pull requests. The id is
  resolved by `Store.ResolveFeature`: a feature merged into another answers
  with the survivor's detail, one of a retired version `{"archived": true}`,
  and one of a version being built, or a chain of merges too long to follow
  (`ErrMergeChain`), is not found.
- **`GET /build?repository=`** (exactly one): `building`, the repository's first build in
  progress (its `building` version: pull requests `placed` of `total`, why
  it `stopped`, `""` while it may go on, and `spent_usd` over all its
  runs), null without one; `has_features`, whether the current version
  has a feature not merged into another; and `current_since`, when it
  became current (null without one). A building version's features are
  not listed until it is current.
- **`GET /decisions?repository=&q=&person=&file=&who=&category=&superseded=&from=&to=&cursor=&limit=`**:
  the current decisions of the repositories' captured sessions, newest first
  and then by id, paged as features are, with `coverage`, summed over the
  repositories: their sessions extracted, skipped by reason, and being read
  (a capture attempt pending). `file` is the path or anything
  under it; `who` and `category` repeat or are comma separated; superseded
  decisions show only with `superseded=1` (or `true`). On both lists `from`
  (inclusive) and `to` (exclusive) are RFC 3339 times or dates, a date
  being the local day's start; another value is a 400.
- **`GET /files?repository=&prefix=&limit=`**: `files`, the paths the
  repositories' current decisions cite that start with `prefix`, and their
  folders (ending in `/`) that do, each with its `kind` (`file` or
  `folder`) and `decision_count`, the decisions citing it or anything under
  it, each counted once (superseded ones included, as hosted counts them):
  most first, folders before files, then by path; 20
  (`limit`, at most 50).
- **`GET /decisions/{id}`**: its session (`<agent>:<id>`, as the app's
  session page names it, branch and person), the session's current
  decisions (summary, category, who, rejected alternative, files, turns)
  and the pull requests its linked commits are in. A decision a later
  capture replaced (obsolete, kept for good) still opens, as on the hosted
  Timeline: with the decisions replaced with it and `replaced: true`. `turns` are
  the human prompts the decisions cite (`ordinal`, `triggered_at`, `text`),
  read from the session's transcript on this computer (its stored
  `transcript_path`, found as capture finds it, `livesessions.Locate`) and
  rendered as capture renders them (`decisions.Render` with a
  `bundle.Redactor`): the harness's wrappers stripped, secrets and
  identities masked, and a turn that was only wrappers left out. A session's
  prompts are kept by its transcript's version (`Location.Version`), the 16
  sessions last read. When the transcript is no longer on this computer,
  cannot be read, or its files are over capture's `MaxTranscriptBytes`, `prompts_withheld` is true and `turns` empty. No
  rationale or evidence is served.
- **`GET /sessions/{agent}:{id}/transcript`**: a captured session's
  transcript, as `GET /api/live-sessions/{agent}/{id}/transcript` answers
  it, whether the tracker still lists the session or it ended long ago
  (`livesessions.Transcripts.Read`, from its stored `transcript_path`;
  `live` from the tracker). A session of a repository the Timeline does not
  serve, or one never captured, is a 404.

`q` matches keywords, each word a prefix and all of them required, with
SQLite FTS5 (the `unicode61` tokenizer, diacritics folded, no stemming, so
a prefix matches as typed): a decision's title and summary; a feature's
title, description and digest or a pull request's title, or a pull
request's number. The index (`decision_search`, `feature_search`,
`pull_search`) is kept by triggers on those tables and built from the rows
already stored when a store without it is opened.

Every captured session is this computer's user's, so the Timeline knows one
person besides the pull requests' authors: the user, named by the first
served repository's clone (git `user.email` and `user.name`; avatars are
never read, the app draws initials). Every served clone's address names
them too, and so does their GitHub login's address
(`<login>@users.noreply.github.com`) once `Login` knows the login (the
daemon's is the viewer of a picked repository's cached discovery snapshot,
else the GitHub connection's): the pull requests they opened are then theirs,
and they are the login's person. It is read at most every 30 seconds. A
pull request's author is named by their login's address.

- **`GET /people?repository=`**: `people`, the user with
  `decision_count`, the repositories' current decisions, superseded ones
  included as hosted counts them (the list hides those unless asked);
  nobody without a decision or a known user.
- **`GET /features/people?repository=`**: `people`, everyone among the
  repositories' features' people with `feature_count`, most first, then by
  address.
- `person=` (an address, any case) on `/features` keeps the features that
  person is among the people of: the user (by any of their addresses) those
  with their sessions or pull requests, anyone else those with a pull
  request they opened. On `/decisions` the user keeps every decision, anyone
  else none.

A request reads its features' sessions' current decisions and links in a
few set queries (`Store.Current` and `SessionLinks` read the same way), a
feature's detail only that feature's, and opens each repository's clone at
most once (`LocalClones`): its git identity and its review store are each
read at most once.

`Changed` is called by the capturer once a capture's call is recorded as
pending, by the capture scheduler after a capture that saved decisions,
recorded a skip or settled an attempt, and by the feature worker once a
repository's pass, consolidation included, listed pull requests, linked
sessions, placed an item, wrote a feature's text, or merged or split a
feature. The daemon publishes a `timeline` event on
the app's event stream then, at most once a second, and after a settings
change.

## Verification

Decision capture and feature stages run through the desktop daemon and its
job queue. Their package tests exercise capture, sync, linkage, placement,
summary, consolidation and first-build behavior directly. Run them in Docker
from the monorepo root:

```sh
just oss-go test ./internal/decisionlog/... ./internal/jobs/...
```
