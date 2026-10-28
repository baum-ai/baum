# Local decision capture

The daemon extracts the decisions of the coding-agent sessions running on this
machine and keeps them in `<state>/decisions/decisions.sqlite`, a private
(directory 0700, file 0600) SQLite database. Extraction uses the published
prompt (`decisions.NewPublicExtractor`, `decisions-public-v1`) and the
`decisions` package's renderer, parser and citation validator, so a local
extraction follows the same rules as a hosted one.

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
   unchanged and the session is covered, nothing happens, except that a
   stored path that failed is replaced by the one found.
3. baum's own generation runs are skipped (`generation_run`).
4. The transcript is rendered and redacted. When it has the head extraction's
   content hash (an empty head included), the head still covers it and no call
   is made. When a head exists, the call extends it: the message lists the
   existing decisions by handle and then the whole transcript, with a marker
   after the turns the head's prefix covered; existing decisions keep their
   IDs. Otherwise the call reads the whole transcript.
5. A prompt estimated over 272,000 tokens is skipped (`over_token_cap`).
6. The backend is the one reviews use (config file, app preferences, repository
   settings), and `auto` resolves as for reviews: the first of claude, codex
   and anthropic that passes its check, never cursor-agent; none is
   `backend_unavailable`. The model follows the resolved backend:
   `preferences.decisions.model` unless it names another provider's family,
   else `claude-haiku-5-5` for claude and anthropic and `gpt-6-luna` for codex.
   cursor-agent cannot make a single structured call (`backend_unsupported`); a
   backend that does not pass its check is `backend_unavailable`.
7. The cost is reserved before the call: the prompt's byte bound and 16,384
   output tokens at the model's list price. Claude Code caches the prompt, so
   claude's call is reserved at the cache-write rate when that is higher. It
   may make a second call, after the harness rejected its first reply against
   the schema; that resends the cached prompt, so it is reserved at the
   cache-read rate plus another reply. The Codex CLI has no option capping the
   reply, so its 16,384 tokens are an estimate rather than a limit. A model the
   price list does not price is skipped (`unpriced_model`). There is no
   spending cap: as for reviews, each call keeps its token and time budgets.
8. The attempt is recorded as pending and the completion made: one request for
   anthropic; `claude -p` with no tools besides its structured reply, which
   normally ends after one call and may retry a rejected reply once; or one
   `codex exec` turn with its shell tools off, normally one request (a call to
   a tool Codex still offers, such as its patch or plan tool, makes another
   within the turn). The attempt is settled with its cost from the reported
   usage, or none when it cannot be priced, and with how many replies the
   harness rejected against the schema (`rejections`; the last one's reason
   goes to the private log with the attempt's stage, item and id). An attempt
   still pending when the daemon starts is settled as abandoned. A reply
   stands when the ledger cannot be written after it; the failure is logged.
9. The reply is masked for secrets before it is parsed, validated against the
   transcript, given IDs and saved.

A failed call is not cached. After 3 failures in a row the session is marked
`extraction_failed` until its transcript changes. Skips for a missing backend
or an unpriced model are tried again the next day or when the transcript
changes; other skips only when it changes.

## Stored data

- `capture_session`: per agent and session id, the repository and branch, the
  transcript path, the fingerprint, the head extraction (content and prefix
  hash, prompt version, backend, model, decisions with IDs, dropped citations,
  tokens and cost), the coverage (`extracted` or `skipped` with a reason) and
  consecutive failures.
- `decision` and the `current_decision` view, `decision_file`: the decision
  log. A decision is upserted by ID; decisions a newer extraction no longer
  holds get `obsolete_at` and are never deleted. A decision is
  dated by its first cited turn's time, else the date its ID had, else the
  extraction's time.
- `capture_attempt`: the ledger of calls (backend, model, full or extend,
  reservation, cost, tokens, state, failure reason, rejected replies), with the `stage` that made the call
  (`capture`, or a feature log stage) and the `item` it was about.

Settled attempts older than 90 days are pruned daily; decisions, obsolete
ones included, are kept for good, as hosted extraction keeps them.
The store holds decisions, rationale and evidence quotes derived from redacted
transcripts, never the transcripts themselves.

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
  skipped and logged.
- Only pull requests that can be placed in a feature are read from the
  clone: open or merged ones not opened by a bot. Each pass fetches what
  its due pull requests lack, as a review fetches a missing pull request
  commit: a head from `refs/pull/<N>/head` into `refs/baum/pull/<N>/head`
  and a base by its id into `refs/baum/pull/<N>/base`, both force-updated,
  100 refspecs to a `git fetch`. So each such pull request of the 180 days
  before activation, and every one after, gets refs in the clone; they are
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
linkage of the review page (`locallink.LinkCommits`: Entire checkpoints, then
the matcher; generation runs left out), and their links replace the ones
they had:

- each run, the commits just recorded, made in the last 7 days (hosted looks
  back a day; a laptop may be off for days);
- every 15 minutes, the commits within the session window of now, which a
  session still running may yet link to, read from the clone without
  fetching, at the head their commits were recorded at;
- once a day, every commit of the last 7 days.

A commit linked to a session more than one way keeps its strongest link.

`Store.SessionLinks` reads what attributes a captured session's decisions
(`features.SessionLinks`): the feature pull requests its linked commits are
in, each with the paths it touches and those it was assigned with; a
session with no capture row has none.

### Model calls

Placement, feature text and consolidation call as capture does (steps 6 to
8 above): the same backend, model, reservation (the request's byte bound and
the call's reply bound: 4,096 tokens for a placement, 32,768 for a batch,
merge or split, 8,192 for a text or pull request summary) and ledger, each
attempt recorded with its stage (`feature_assign`, `feature_summary`,
`pr_summary` or `feature_consolidate`) and item. Rejected
replies and a ledger that cannot be written are handled as capture's. The
preferences and the repository's settings are read again before every call,
so a repository turned off makes no further call. A repository turned off,
a missing backend or an unpriced model stops the stage until its next run,
with no failure counted.

### Placement

After the sync and the linking, each run places at most 100 pending items,
oldest trigger first, one call each with the published assigner prompt. An
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
titles with their saved review summaries, else generated ones.

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
changes, and, with the features of the sessions linked to its old and new
commits, when its commits or paths change; and when the sessions linked to
a commit change, the features holding a pull request with it and the
features of those sessions (an unchanged re-link marks none). Marking
raises `membership_version` and sets `dirty_since` and `changed_at`.

### Feature text

Each run writes the text of at most 50 features behind their members, not
changed for 15 minutes, longest behind first, with the published summary
prompt. A feature's input is its pull requests, earliest opened first
(title, description, state, and the summary of the latest review saved in
the clone, else a generated one), the current decisions of its sessions that
attribution gives it (what was decided and why and the cited files, never a
session's prompts or quoted evidence) and its paths. The call also reads the
repository's name and the feature's current title and description.

A pull request with no saved review summary gets one from its own call, from
its title, description and the feature's decisions of the sessions linked
to it, kept in `pr_summary` with its input's hash and reused while that is
unchanged; one with neither a description nor such a decision gets none.

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

After the text, a repository's features are consolidated as hosted ones are,
with the published consolidation prompt, at most once every 7 days (from the
start of the last pass) and only when their fingerprint (every feature's id,
membership and text versions and `merged_into`, merged ones included)
changed since the last pass (`feature_consolidation`). A pass's own merges
and splits are changes, so one that changed features is followed by another
a week later. A pass reads the features not merged into another and active
in the last 90 days (as placement's candidates), most recently active first.
Its calls' replies may run to 32,768 tokens rather than hosted's 8,192:
Claude Code's models think within the reply's bound even with thinking
turned off, and a merge over a first build's features needs the room.

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
- **Split.** At most 3 features, as they will be after the merges, with more
  than 30 pull requests or pull requests opened more than 8 weeks apart,
  largest first, get one split call each (a call takes its slot whatever its
  outcome) with their pull requests' text,
  state, opening date, placed paths and summaries. A feature kept whole, or
  too large to offer (over 200 pull requests or its input over its bound,
  which takes no slot), is recorded as checked (`split_checked_version`) and
  not offered again until its members change; a survivor of the pass's
  merges always is. Otherwise the largest part keeps the feature's id (ties
  to the part with the earliest opened pull request), and each other part
  becomes a new feature with the reply's text, recording `split_from` and
  `split_at`, its pull requests moved as `consolidation`'s at the head and
  paths they were placed with; sessions in it on their own stay. The parts
  are recorded as checked.
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
  and unchecked. A call that cannot be made (a backend or price reason)
  also writes nothing; the pass is made again on the next run.

### First build

The live placement places only work opened since the feature log was
activated. A repository's history is placed when the user asks for it, as a
hosted rebuild does, by a first build: a job of the job queue
(`jobs.KindFeatureBuild`) that runs only from a confirmed estimate, or `baum
internal features build`.

- **Estimate.** The pull requests left to place and a resumed version's
  features whose text is behind (with their pull requests' summaries still
  to write) are counted, and their calls priced from their sizes at the
  configured backend and model: a batch call per 25 (the items' titles,
  descriptions and paths, and candidates for the features placed so far, a
  feature per 3 pull requests), the new features' text twice and a summary
  of each pull request with a description. As for a requested review, there
  is no spending cap: the estimate is what the user confirms, and the build
  runs to the end. A repository whose decisions are not captured, or whose
  model cannot be priced or called, is not estimated.
- **Building.** The build makes the repository's `building` version, or
  resumes the one there is, and places in it the merged and open pull
  requests of the last 180 days not opened by a bot, in no feature of the
  version and not skipped or left out of it at their head, listed once when
  it starts, oldest first, in batches of 25 with the batch prompt (its seeded
  form when a candidate has no pull requests). Each item's candidates are
  ranked against the version as placement ranks them, read again for every
  call, and the call offers their union, each item's best first, at most 40.
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
- **Switching.** The building version becomes current in one transaction:
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
  (`unpriced_model`, `disabled` or a backend reason; `failed` for a store
  error), or when it is cancelled or the daemon stops, leaving the version
  `building` with `stopped` set to why; confirming it again resumes it. Its
  job then fails with that reason. Progress (phase, calls) is reported to
  the job, and the version records its pull requests placed of the total
  and its spend over all its runs (`spent_usd`, each call's known cost).

### Stored data

- `pull_request`, `pull_commit`, `pull_sync`: the listed pull requests with
  their paths at a head and failed reads, their commits, and the listing's
  ETag, cursor, activation and last full re-link.
- `session_commit`: each session linked to a commit, with the method and
  confidence.
- `feature_version`, `feature`, `feature_pull`, `feature_session`,
  `feature_settle`, `pr_summary`, `feature_consolidation`: the tables
  placement, text, consolidation and a build write. A repository has at
  most one `current` and one `building` version; `featureVersion` is the one
  place that chooses the version a stage uses (the live stages' current
  one, made when first needed, and a build's building one).

These rows are not pruned.

## Timeline API

`Timeline` serves the app's Timeline page under `/api/timeline/`, mounted in
the local API behind its authentication, in the view models of the review
viewer's decision log (`oss/viewer/src/decision-log.ts`). It reads the store
and never writes it.

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
- **`GET /features?repository=&q=&status=&cursor=&limit=`**: the
  repositories' features of their current versions not merged into
  another, merged in one order: by last activity (else creation) newest
  first, then id, 50 a page (`limit`, at most 500). `cursor` is the last
  row's time and id, so a page follows on from it whatever arrived
  meanwhile.
  `status` (`in_progress`, `shipped`, `abandoned`; repeated or comma
  separated) and `q` filter before paging. A row has its title and
  description (null while none is written; `summary_pending` while it is
  behind its members), the status (`features.DeriveStatus` over its pull
  requests), the first and last activity (its pull requests' opening and
  last update and its decisions' times), `people` (the clone's git
  `user.email` when it has a session, then its pull requests' authors not
  opened by a bot, by login), the counts of pull requests, sessions (the
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
- **`GET /decisions?repository=&q=&file=&who=&category=&superseded=&from=&to=&cursor=&limit=`**:
  the current decisions of the repositories' captured sessions, newest first
  and then by id, paged as features are, with `coverage`, summed over the
  repositories: their sessions extracted, skipped by reason, and being read
  (a capture attempt pending). `file` is the path or anything
  under it; `who` and `category` repeat or are comma separated; superseded
  decisions show only with `superseded=1`; `from` (inclusive) and `to`
  (exclusive) are local dates.
- **`GET /decisions/{id}`**: its session (`<agent>:<id>`, as the app's
  session page names it, branch and person), the session's current
  decisions (summary, category, who, rejected alternative, files, turns)
  and the pull requests its linked commits are in. A decision a later
  capture replaced (obsolete, kept for good) still opens, as on the hosted
  Timeline: with the decisions replaced with it and `replaced: true`. `prompts_withheld` is
  always true: the Timeline serves no transcript text, rationale or
  evidence.

`q` matches keywords, each word a prefix and all of them required, with
SQLite FTS5 (the `unicode61` tokenizer, diacritics folded, no stemming, so
a prefix matches as typed): a decision's title and summary; a feature's
title, description and digest or a pull request's title, or a pull
request's number. The index (`decision_search`, `feature_search`,
`pull_search`) is kept by triggers on those tables and built from the rows
already stored when a store without it is opened.

A request reads its features' sessions' current decisions and links in a
few set queries (`Store.Current` and `SessionLinks` read the same way), a
feature's detail only that feature's, and opens the repository's clone once
(`LocalClones`): its git `user.email` and its review store are each read
at most once.

`Changed` is called by the capturer once a capture's call is recorded as
pending, by the capture scheduler after a capture that saved decisions,
recorded a skip or settled an attempt, and by the feature worker after a
repository's pass that listed pull requests, linked sessions, placed an
item or wrote a feature's text. The daemon publishes a `timeline` event on
the app's event stream then, at most once a second, and after a settings
change.

## Testing by hand

`baum internal decisions capture --agent <agent> --session <id> [--path
<transcript>] [--dry-run] [--backend <b>] [--model <m>]` runs one capture
without the gate, throttle or unchanged check and prints its report as JSON.
`--dry-run` stops before the call and writes nothing but the `--debug-dir`
files: it reads an existing store read-only, and uses an empty one in memory
when there is none. The hidden `--debug-dir <dir>` creates the private
directory and writes the rendered transcript, and after a call the message
sent and the raw reply, there, replacing a previous capture's files; a
capture that stops before rendering leaves it empty.

`baum internal features sync --repo <owner/name> [--clone <path>] [--days N]`
runs one pull request sync without the gate (the first lists the last N
days, default 180) and `baum internal features link --repo <owner/name>
[--clone <path>]` links every commit of the last 7 days; each prints its
counts as JSON. `baum internal features assign --repo <owner/name> [--clone
<path>] [--dry-run] [--limit N] [--backend <b>] [--model <m>]` runs placement
once, ignoring the activation, and prints each item; `--dry-run` prints each
item's candidates and input size, makes no call and writes nothing.
`baum internal features summarize` takes the same flags but `--dry-run` and
writes the text of the features behind, without the debounce. `baum
internal features consolidate` takes `--repo`, `--clone`, `--backend` and
`--model` and makes one pass over the current features without the
interval or fingerprint check. `baum
internal features build --repo <owner/name> [--clone <path>] [--dry-run]
[--backend <b>] [--model <m>]` runs a first build without the gate, a line a
batch on standard error and its report as JSON; `--dry-run` prints the
estimate and writes nothing.
These read the preferences and the repository's settings and fail when they
cannot; `sync`
and `link` read neither. The clone
defaults to the repository's registered one. They are for testing, with the
daemon stopped: they write the daemon's store.
