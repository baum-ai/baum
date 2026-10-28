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
   usage, or none when it cannot be priced. An attempt still pending when the
   daemon starts is settled as abandoned.
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
  holds get `obsolete_at` and are never deleted while current. A decision is
  dated by its first cited turn's time, else the date its ID had, else the
  extraction's time.
- `capture_attempt`: the ledger of calls (backend, model, full or extend,
  reservation, cost, tokens, state).

Settled attempts and obsolete decisions older than 90 days are pruned daily.
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
`preferences.decisions`, served only by `GET` and `PUT
/api/settings/decisions` (in `internal/uiserver`), which add
`EffectiveModel`: the backend and model a capture would use, resolved by the
same helper a capture uses, `auto` included, and whether the chosen model is
ignored (never with cursor-agent, which uses no model). Its backend checks
get 5 seconds; no backend, or none in time (`backend_undetermined`), is
unknown (empty) and ignores nothing. `baum internal reviews show` reports the
same as `decisions_backend`, `decisions_effective_model` and
`decisions_model_ignored`. A repository's own setting is `settings.decisions`,
through the repository settings API. Analytics counts the ledger
(`Store.Ledger`) under baum's work.

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
