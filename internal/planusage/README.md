# Plan usage

`internal/planusage` tells the person how much of their Claude and Codex plans
is left, so they can choose the harness for reviews knowing what each has to
spare. The daemon serves it as `GET /api/plan-usage`; the app shows it in its
sidebar and in the desktop setup's review step, and the desktop tray in its
menu and panel.

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

When Codex is signed in with ChatGPT, the reader asks the endpoint the Codex
CLI's status line uses, `GET https://chatgpt.com/backend-api/wham/usage`, with
the `tokens.access_token` and `tokens.account_id` of `$CODEX_HOME/auth.json`
(`Authorization: Bearer` and `ChatGPT-Account-Id`). **It is undocumented and
may change.** `rate_limit.primary_window` and `secondary_window` become windows
named by `limit_window_seconds`, resetting at `reset_at` (or after
`reset_after_seconds`), and `fetched_at` is now: the figures are live. The
token is handled as Claude's is: held only for the request, never logged,
stored, returned or put in an error, never refreshed (that would rotate Codex's
sign-in), and not sent once the `exp` of the JWT has passed. The client refuses
redirects.

Without that sign-in (an API key, no `auth.json`), with an expired token, or
when the request fails or answers in a shape this reader does not know, it
falls back to what Codex writes into its own session files. The reader
finds `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl` (`CODEX_HOME` defaults to
`~/.codex`), looks at the eight newest files, newest first, and in each reads
only the last 1 MiB, scanning lines backwards for an `event_msg` whose payload
is a `token_count` carrying `rate_limits`. `primary` and `secondary` become
windows named by `window_minutes` (300 is "5-hour", 10080 "weekly"). The reset is
`resets_at` (unix seconds, newer Codex) or `resets_in_seconds` (older Codex,
counted from the snapshot). `fetched_at` is the snapshot's own timestamp, so the
figure is as old as the last Codex turn. A window whose reset has passed since
the snapshot has `reset: true` and a null `used_percent`: the old figure no longer
applies. `plan_type` becomes the plan's name as Codex shows it: "prolite",
"pro" and "promax" are "Pro 100", "Pro 200" and "Pro 500" (the monthly price),
"plus" is "Plus", and an unknown value is passed on unchanged (the endpoint's
`plan_type` is named the same way). The fallback makes no network request and
reads no credential.

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
field `claudeAiOauth.accessToken`. Its `subscriptionType` and `rateLimitTier`
name the plan as Anthropic sells it: Pro, Max 5x or Max 20x for a person, Team
Standard or Team Premium (the `max_5x` tier) for a Team seat, and Enterprise,
which is billed at API prices, without a tier. The token is held only for the request. It is
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
