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

- it ended, or went silent for the tracker's idle time;
- its turn ended (`stop`) at least 2 minutes ago and no prompt followed;
- it has been running for 20 minutes since it started or was last captured
  while running;
- it is about to compact (`pre-compact` hook event);
- every 5 minutes, it is tracked and not running (the backstop; a session whose
  transcript did not change is left alone).

One worker captures the queued sessions one at a time; a session already
queued is not queued twice. Before a call, the ledger throttles a session: at
most one attempt every 5 minutes, none within 10 minutes of a failed one, and
after 12 attempts in a day only once it ended.

## Capture

1. The transcript is located as the live session view locates it: a regular
   file inside the agent's own session store, symbolic links resolved. Its
   mapped root session must be the hook event's session. A session whose own
   files exceed 64 MiB is skipped (`too_large`).
2. When the stored fingerprint (size and modification time of its files) is
   unchanged and the session is covered, nothing happens.
3. baum's own generation runs are skipped (`generation_run`).
4. The transcript is rendered and redacted. When it has the head extraction's
   content hash, the head still covers it and no call is made. When a head
   exists, the call extends it (existing decisions keep their IDs; new turns
   after the head's prefix are read); otherwise it reads the whole transcript.
5. A prompt estimated over 272,000 tokens is skipped (`over_token_cap`).
6. The backend is the one reviews use (config file, app preferences,
   repository settings; `auto` means claude). The model is
   `preferences.decisions.model`, else `claude-haiku-5-5` for claude and
   anthropic and `gpt-6-luna` for codex. cursor-agent cannot make a single
   structured call (`backend_unsupported`); a backend that does not pass its
   check is `backend_unavailable`.
7. The call's cost is reserved before it is made: the prompt's byte bound and
   16,384 output tokens at the model's list price. A model the price list does
   not price is skipped (`unpriced_model`). A session's spend may not pass
   $0.25 a day (`over_cost_cap`), and all capture's spend may not pass
   `preferences.decisions.daily_cap` when set (`daily_cap`; no default).
8. The attempt is recorded as pending, the call made (one structured call, no
   tools), and the attempt settled with its cost from the reported usage, or
   none when it cannot be priced. Spend counts the cost, else the reservation.
   An attempt still pending when the daemon starts is settled as abandoned and
   its reservation counts.
9. The reply is masked for secrets before it is parsed, validated against the
   transcript, given IDs and saved.

A failed call is not cached. After 3 failures in a row the session is marked
`extraction_failed` until its transcript changes. Skips for a missing backend,
an unpriced model or a cap are tried again the next day or when the transcript
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

## Testing by hand

`baum internal decisions capture --agent <agent> --session <id> [--path
<transcript>] [--dry-run] [--backend <b>] [--model <m>]` runs one capture
without the gate, throttle or unchanged check and prints its report as JSON.
`--dry-run` stops before the call and writes nothing.
