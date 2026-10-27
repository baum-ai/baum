# Local analytics

`internal/analytics` answers `GET /api/analytics` for the app's Analytics
page: what the person's coding agents did in their registered clones, what it
would cost at API prices, what baum itself ran, and what the person delivered
on GitHub. The goal is keeping track of what agents are up to, so every figure
is observational: it does not show that an agent caused a change or a bug.

```
GET /api/analytics?days=7|30|90&repo=owner/name&agent=claude_code|codex|cursor
```

`days` defaults to 30; it is whole local days, today included. `repo` must
be a picked repository or a registered clone's; `agent` one of the three. Any
other value is a 400. Every figure that can be unknown is `null`, never `0`,
with the reasons beside it where there are any.

## The session index

`Index` keeps one entry per session file in `analytics-index.json` in the
daemon's state directory (`$XDG_STATE_HOME/baum`). It holds aggregates only:
the agent, the clone and its repository, the top-level session's branch, its
start and end, active seconds (in total and by hour), human prompts, tool
calls and failed ones, subagents, model calls, token sums by model, speed and
price band, and the commits the session recorded creating (their ids). No
prompt, message, tool input or output, or file path is stored.

- Only sessions whose working directory is inside a registered clone (any of
  its worktrees) are read, from the same stores and with the same discovery
  as the PR page (`sessions/collector`, `sessions.Map`). Sessions last written
  more than 90 days ago are skipped, and their entries dropped.
- An entry is keyed by its files' sizes and modification times (a digest for
  Cursor's generated tree), so a refresh reads only what changed. Files that
  are gone, and the entries of unregistered clones, are dropped; a clone that
  is not available keeps its entries until it is back.
- The daemon builds it in the background when it starts and refreshes it every
  five minutes and when an agent session ends. While the first pass runs, the
  report answers with what it has and `indexing: {done, total, complete}`.
- baum's own generation runs are kept as marked entries, so they are not read
  again, and never count.
- A session that does not map counts with unknown usage.

## Counting

The hosted service's counting rules apply:

- A session is a root session: nested agents add subagents, never sessions.
  One session read twice (two registrations of one clone) counts once, from
  its latest reading.
- A human prompt is a root input of kind `prompt` or `slash_command` from a
  person, with text left once wrapper constructs are stripped
  (`sessions/prompttext`). A parent's instructions to a child agent are not.
- A session is in the window when it started in it; its figures then count
  whole.
- Active time sums the gaps before each agent event (a model call, a reply, a
  tool call or its result) of at most five minutes (`IdleGap`): time waiting
  for the person, or a session left open, adds nothing. A session with no
  transcript has unknown active time, not zero. Days and hours are local.
- Tokens: every token processed, cache reads included (Codex input already
  includes its cached input). A model call or usage row that reported nothing
  makes the session's tokens a floor (`tokens_incomplete`).
- Cost is priced at read time from `pricing.Active()`, so a corrected price
  corrects history. Requests are grouped by model, speed and long-prompt band,
  as the hosted cost queries group them; an aggregate the vendor reported
  without its requests is priced only below every threshold. A group with no
  price has no cost, never `$0.00`, and marks the total incomplete with the
  reason in words. It is an estimate of API-equivalent cost, not what a
  subscription charges.
- Failed tool calls are calls whose result was an error. A tool error is not an
  interruption.

## Agent-written share

The person's commits are those on the local branches of the clones in scope
authored by the clone's `user.email`, committed in the window, merges left
out, each counted once per repository however many clones hold it. A commit is
agent-written when an indexed session of the repository recorded creating it
(the matcher's `commit_ref` evidence), it carries an `Entire-Checkpoint`
trailer, or a `Co-Authored-By` trailer names an agent by address
(`locallink.CoauthoredAgents`); each commit counts once, under the strongest.
The heuristic file-overlap match is not used, so the share is a floor. Under an
agent filter only that agent's sessions and trailers count; a checkpoint names
no agent, so its commit stays in the denominator only. A clone without
`user.email`, or that git cannot read, is named in `reasons`. Commit lists are
cached for five minutes.

## baum's work

From the jobs ledger (`jobs.json`), jobs created in the window: reviews, group
reviews and fixes, automatic ones, their states, tokens and spend (a run with
usage but no price makes it incomplete), failures by reason code (the app puts
them in words), fixes that pushed or failed, and review threads a fix replied
to and resolved. Merges, branch updates and check re-runs made from the app are
recorded by `Actions` in `analytics-actions.json` (time, kind, repository,
pull request number) when GitHub accepts them, kept 90 days; they are `null`
when there is no ledger. The agent filter matches a job's harness.
`decisions` counts decision capture's calls started in the window from its
ledger (`Captures`; null without one): calls, those that succeeded and
failed, failures by reason code, and the spend of settled calls, incomplete
when one has no cost and unknown, never zero, when none has. The agent filter
matches the backend that made the call.

## Delivery

`Delivery` caches one GitHub search per picked repository (`repo:owner/name
is:pr author:@me created:>=…`, `github.AuthoredPullRequests`) for the last 90
days, for an hour (five minutes after a failure). A request never waits for
it: a repository not searched yet makes the section `loading` and starts the
search in the background, and the app reads the page again. Figures cover
non-draft pull requests opened in the window; drafts are counted separately.
Time to merge and to the first review by someone else are medians. The
conflict and failing-checks share is what GitHub reports for each pull
request's latest head now, not its history. Line survival is not measured and
must not be added. A failed search is a reason in fixed words, never GitHub's
text. An account change forgets every answer.

## Plan usage

`planusage.History` samples the plans every 15 minutes while the daemon runs
(through the plan usage cache, so Claude's usage endpoint is asked at most
then) and keeps changed figures, plus one an hour while unchanged, for 90 days
in `plan-usage-history.json`. The report draws each provider window's samples
in the window and counts the times it reached 100%. The agent filter picks the
provider.

## Tests

Tests use small Claude Code transcripts under a temporary store, a temporary
git clone, an injected clock, a fake GitHub search and fake writes; they never
read the real home directory.
