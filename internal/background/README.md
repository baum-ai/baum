# background

Keeps local GitHub metadata fresh while the daemon runs: discovery polls of
every picked repository, then reads of the saved reviews' evidence. It never
queues or starts generation. The daemon hands each successful snapshot to its
automatic scheduler through `Discovered`.

## A pass

`Run` makes a pass at least every 30 seconds, or sooner when a repository is
due sooner, as one settling after a new head is. A pass (`Sync`):

1. **Probes** (`discovery.Probe`), at most once per interval (a minute). A
   repository whose open pull requests changed since its last poll polls now
   (`PollNow`), past discovery's backoff. A pull request opened or pushed from
   anywhere therefore shows within about a minute. A probe that succeeds after
   one that failed makes every repository whose poll failed meanwhile poll at
   once.
2. **Polls** every due repository, `pollConcurrency` (3) at a time. Each poll
   gets `pollTimeout` (45 s). Discovery limits each of its requests and keeps
   what it read when time runs out. The polls share the credential's GitHub
   budget, which discovery checks before every request. Their results are
   handled one at a time: `Discovered`, the repository's state, and the reads
   each poll makes due.
3. **Reads** the due saved pull requests within `refreshBudget`: those GitHub
   reported changed first, then the oldest. A rate-limited read pauses reads
   on that host (`Paused`, `Limited`), which the PR page's facts share.
4. Reconciles retained review refs (`Retain`) once a day per common git
   directory.

A repository's state (`GET /api/background`) records its last success, its
next refresh and the error of the last pass: `discovery_unavailable`,
`discovery_timeout` (the poll ran out of time), `rate_limited`,
`metadata_unavailable`, `store_unreadable` or `cache_unavailable`. A failed
poll backs off exponentially, for at most 15 minutes. A settling poll is due
when discovery says.

## Tests

```sh
just oss-go test -race ./internal/background
```
