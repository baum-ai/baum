# Plan usage

`internal/planusage` tells the person how much of their Claude and Codex plans
is left, so they can choose the harness for reviews knowing what each has to
spare. The daemon serves it as `GET /api/plan-usage`; the app shows it in
Settings and in the desktop setup's review step.

```json
{"plans": [{"provider": "claude", "available": true, "plan": "",
  "windows": [{"name": "5-hour", "used_percent": 38,
               "resets_at": "2026-10-26T15:00:00Z"}],
  "fetched_at": "2026-10-26T12:00:00Z"},
 {"provider": "codex", "available": false,
  "reason": "No Codex sessions found yet. Run Codex once to see its usage.",
  "windows": [], "fetched_at": "2026-10-26T12:00:00Z"}]}
```

A plan that cannot be read has `available: false` and a `reason` in words. The
reason is always a fixed sentence: no upstream body, error text, path or token
is ever in it.

## Codex

Codex writes the account's rate limits into its own session files. The reader
finds `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl` (`CODEX_HOME` defaults to
`~/.codex`), looks at the eight newest files, newest first, and in each reads
only the last 1 MiB, scanning lines backwards for an `event_msg` whose payload
is a `token_count` carrying `rate_limits`. `primary` and `secondary` become
windows named by `window_minutes` (300 is "5-hour", 10080 "weekly"). The reset is
`resets_at` (unix seconds, newer Codex) or `resets_in_seconds` (older Codex,
counted from the snapshot). `fetched_at` is the snapshot's own timestamp, so the
figure is as old as the last Codex turn. A window whose reset has passed since
the snapshot has `reset: true` and a null `used_percent`: the old figure no longer
applies. This makes no network request and reads no credential.

## Claude

The reader calls the endpoint Claude Code's own `/usage` command uses:
`GET https://api.anthropic.com/api/oauth/usage` with `Authorization: Bearer`
and `anthropic-beta: oauth-2025-04-20`. **The endpoint is undocumented and may
change or disappear**; any failure is shown as unavailable, never as an error.
`five_hour`, `seven_day`, `seven_day_opus` and `seven_day_sonnet` become windows
from `utilization` (percent used) and `resets_at`; null or malformed windows and
unknown keys are ignored.

The token is the one Claude Code stored: on macOS the Keychain item
"Claude Code-credentials", and otherwise (or if the item is missing)
`$CLAUDE_CONFIG_DIR/.credentials.json` (default `~/.claude/.credentials.json`),
field `claudeAiOauth.accessToken`. The token is held only for the request. It is
never logged, stored, cached, returned by the API or included in an error, and
the HTTP client refuses redirects. baum never refreshes it: refreshing would
rotate Claude Code's credential. An expired token, or a 401 or 403, reads "Open
Claude Code to refresh its sign-in."

## Caching

`Service` keeps the result for five minutes. `?refresh=1` (the app sends it when
a page opens) reads again, but never more than once a minute. One mutex covers a
reading, so simultaneous requests share it.

## History

`History` samples the plans every 15 minutes while the daemon runs, through
`Service`'s cache, so Claude's endpoint is asked at most that often when no
page is open, and appends what is new to `plan-usage-history.json` in the
daemon's state directory: the snapshot time, provider, window name, percent
used and reset time. No token, account or plan name is stored. A figure is
kept when it changed, when its window's reset changed, or an hour after the
last kept one; a reading no newer than the last (Codex's snapshot did not
move) is not. Samples older than 90 days are dropped. The analytics page
draws them ([analytics](../analytics/README.md)).

Tests use fixture session files, an `httptest` server and an injected credential
source and clock; they never read the real `~/.claude` or `~/.codex`.
