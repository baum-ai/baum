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
   reply, so its 16,384 tokens are an estimate rather than a limit. Prices
   only report: a model the price list does not price runs, reserved at 0.
   There is no spending cap: as for reviews, each call keeps its token and
   time budgets.
8. The attempt is recorded as pending and the completion made: one request for
   anthropic; `claude -p` with no tools besides its structured reply, which
   normally ends after one call and may retry a rejected reply once; or one
   `codex exec` turn with its shell tools off, normally one request (a call to
   a tool Codex still offers, such as its patch or plan tool, makes another
   within the turn). The completion, a retry included, has 10 minutes, the
   default `pass_timeout` of a review pass: a backend still running then is
   stopped and the attempt fails (reason `timeout`), so a hung backend does
   not hold up the sessions queued behind it. The attempt is settled with its
   cost from the reported usage, priced as reviews price theirs
   (`pricing.List.OutcomeCost`): when any part of it is unknown (a run cut short, a speed or 1-hour cache-write
   split not reported, as Codex never reports its speed, or several calls
   whose prompt sizes may straddle a long-prompt tier) the cost is unknown,
   not estimated. An attempt still pending when the daemon starts is settled
   as abandoned.
9. The reply is masked for secrets before it is parsed, validated against the
   transcript, given IDs and saved.

A failed call is not cached. After 3 failures in a row the session is marked
`extraction_failed` until its transcript changes. Skips for a missing or
unsupported backend are tried again the next day or when the transcript
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
  reservation, cost, tokens, state), with the `stage` that made the call
  (`capture`, or a feature log stage) and the `item` it was about.

Nothing is pruned: the ledger and the decisions, obsolete ones included, are
kept for good, as hosted extraction keeps them.
The store holds decisions, rationale and evidence quotes derived from redacted
transcripts, never the transcripts themselves.

## Feature log

The feature log will group a repository's pull requests and sessions into
features as the hosted feature log does, with the `features` package's rules
and published prompts. Its choices are in `featureconfig.go`, and it shares
the store with capture. `FeatureWorker` now syncs a repository's pull
requests, reads their paths and commits from the clone, and links the
commits to this machine's sessions; placing them in features builds on that.
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

### Stored data

- `pull_request`, `pull_commit`, `pull_sync`: the listed pull requests with
  their paths at a head and failed reads, their commits, and the listing's
  ETag, cursor, activation and last full re-link.
- `session_commit`: each session linked to a commit, with the method and
  confidence.
- `feature_version`, `feature`, `feature_pull`, `feature_session`,
  `feature_settle`, `pr_summary`, `feature_consolidation`: the tables
  placement writes. A repository has at most one `current` and one
  `building` version; `liveVersion` is the one place that chooses the
  version in use (the current one, made when first needed).

These rows are not pruned.

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
counts as JSON. The clone defaults to the repository's registered one. They
are for testing, with the daemon stopped: they write the daemon's store.
